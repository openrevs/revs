// Package collection is a personal movie collection — movies,
// viewings, ratings, reviews — and its publishing to openrevs servers.
// It is the model shared by the web UI (`revs ui`) and the terminal UI
// (`revs tui`): both are front ends over a Collection.
//
// All local data — movies, viewings, settings including the signing
// key, the author's version history, and the server list — lives in one
// SQLite database in the data directory. Each server additionally gets
// its own SQLite database holding the corpus pulled from it:
//
//	revs.db            the collection, settings, and servers
//	servers/<id>.db    per-server pulled corpus, sync state, review index
//
// Several processes may open the same data directory at once (say, the
// web UI and the TUI): SQLite's locking keeps the databases consistent.
package collection

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/openrevs/revs/client"
	"github.com/openrevs/revs/collection/localdb"
	"github.com/openrevs/revs/keys"
)

const (
	MinRating = 0
	MaxRating = 5
)

var (
	// ErrNotFound is returned when a movie, viewing, server, or user
	// doesn't exist.
	ErrNotFound = errors.New("not found")
	// ErrNoKey is returned by operations that sign when no key is set.
	ErrNoKey = errors.New("no signing key: create or import one first")
)

type Options struct {
	DataDir string
	// WikidataURL overrides the Wikidata API endpoint (for tests).
	WikidataURL string
}

type Collection struct {
	dataDir  string
	db       *sql.DB
	q        *localdb.Queries
	versions *client.History
	http     *http.Client
	wikidata *wikidata

	remotesMu sync.Mutex
	remotes   map[int64]*remote

	syncMu chan struct{} // see syncLocked

	watch *sql.Conn // see DataVersion
}

// Open opens (creating if needed) the data directory and local database.
func Open(opts Options) (*Collection, error) {
	if opts.DataDir == "" {
		return nil, errors.New("collection: data directory is required")
	}
	if err := os.MkdirAll(opts.DataDir, 0o700); err != nil {
		return nil, err
	}

	path := filepath.Join(opts.DataDir, "revs.db")
	db, err := openDB(path, localMigrations, "migrations")
	if err != nil {
		return nil, err
	}
	// The database holds the private key.
	if err := os.Chmod(path, 0o600); err != nil {
		db.Close()
		return nil, err
	}

	wdURL := opts.WikidataURL
	if wdURL == "" {
		wdURL = DefaultWikidataURL
	}

	watch, err := db.Conn(context.Background())
	if err != nil {
		db.Close()
		return nil, err
	}

	return &Collection{
		dataDir:  opts.DataDir,
		db:       db,
		q:        localdb.New(db),
		versions: client.NewHistory(historyStore{db}),
		http:     &http.Client{Timeout: 60 * time.Second},
		wikidata: &wikidata{baseURL: wdURL, http: &http.Client{Timeout: 15 * time.Second}},
		remotes:  map[int64]*remote{},
		syncMu:   make(chan struct{}, 1),
		watch:    watch,
	}, nil
}

func (c *Collection) Close() error {
	c.closeRemotes()
	c.watch.Close()
	return c.db.Close()
}

// DataVersion returns a number that changes whenever the collection
// changes: movies, viewings, settings, servers, and pulls (which record
// their epoch). It covers changes made by any process, so a front end
// can poll it to notice edits made elsewhere. Only equality is
// meaningful.
func (c *Collection) DataVersion(ctx context.Context) (int64, error) {
	// SQLite's data_version changes when any other connection commits;
	// watch is a connection that never writes, so it sees every commit.
	var v int64
	err := c.watch.QueryRowContext(ctx, "PRAGMA data_version").Scan(&v)
	return v, err
}

// DataDir returns the directory the collection lives in.
func (c *Collection) DataDir() string { return c.dataDir }

// notFound maps sql.ErrNoRows to ErrNotFound.
func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// ---- settings ----

const (
	settingPrivateKey = "private_key" // base64 ed25519 seed
	settingDocRev     = "doc_rev"     // revision of the last export, before the version history
)

func (c *Collection) setting(ctx context.Context, key string) string {
	v, err := c.q.GetSetting(ctx, key)
	if err != nil {
		return ""
	}
	return v
}

func (c *Collection) settingInt(ctx context.Context, key string) int {
	n, _ := strconv.Atoi(c.setting(ctx, key))
	return n
}

func (c *Collection) setSetting(ctx context.Context, key, value string) error {
	return c.q.SetSetting(ctx, localdb.SetSettingParams{Key: key, Value: value})
}

// ---- identity ----

// Identity describes the signing key.
type Identity struct {
	UserID    string // empty when there is no usable key
	PublicKey string
	KeyErr    string // set when the stored key can't be read
}

// privateKey returns the signing key, or nil if none is configured.
func (c *Collection) privateKey(ctx context.Context) (ed25519.PrivateKey, error) {
	v := c.setting(ctx, settingPrivateKey)
	if v == "" {
		return nil, nil
	}
	return keys.ParsePrivate(v)
}

// signingKey returns the signing key, or ErrNoKey.
func (c *Collection) signingKey(ctx context.Context) (ed25519.PrivateKey, error) {
	key, err := c.privateKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("stored key is unreadable: %w", err)
	}
	if key == nil {
		return nil, ErrNoKey
	}
	return key, nil
}

// HasKey reports whether a key is stored (readable or not).
func (c *Collection) HasKey(ctx context.Context) bool {
	return c.setting(ctx, settingPrivateKey) != ""
}

// UserID returns the id derived from the signing key, or "".
func (c *Collection) UserID(ctx context.Context) string {
	key, err := c.privateKey(ctx)
	if err != nil || key == nil {
		return ""
	}
	return keys.UserID(key.Public().(ed25519.PublicKey))
}

func (c *Collection) Identity(ctx context.Context) Identity {
	key, err := c.privateKey(ctx)
	if err != nil {
		return Identity{KeyErr: "The stored key is unreadable: " + err.Error()}
	}
	if key == nil {
		return Identity{}
	}
	pub := key.Public().(ed25519.PublicKey)
	return Identity{UserID: keys.UserID(pub), PublicKey: keys.EncodePublic(pub)}
}

// PrivateKeyText returns the stored private key as text, for backing
// it up, or "".
func (c *Collection) PrivateKeyText(ctx context.Context) string {
	return c.setting(ctx, settingPrivateKey)
}

// GenerateKey creates and stores a new signing key, replacing any
// existing one. Callers confirm replacement with the user first.
func (c *Collection) GenerateKey(ctx context.Context) error {
	_, prv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	return c.setSetting(ctx, settingPrivateKey, keys.EncodePrivate(prv))
}

// ImportKey parses and stores a private key, replacing any existing
// one. Callers confirm replacement with the user first.
func (c *Collection) ImportKey(ctx context.Context, text string) error {
	prv, err := keys.ParsePrivate(text)
	if err != nil {
		return err
	}
	return c.setSetting(ctx, settingPrivateKey, keys.EncodePrivate(prv))
}
