// Package ui is the personal web UI behind `revs ui`: a local,
// single-user site for managing a movie collection (movies, viewings,
// ratings, reviews) and publishing it to openrevs servers.
//
// All local data — movies, viewings, settings including the signing
// key, and the server list — lives in one SQLite database in the data
// directory. Each server additionally gets its own SQLite database
// holding the corpus pulled from it:
//
//	revs.db            the collection, settings, and servers
//	servers/<id>.db    per-server pulled corpus, sync state, review index
//
// Pages are server-rendered HTML forms; the only JavaScript is a few
// inline confirm() prompts.
package ui

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/openrevs/revs/client"
	"github.com/openrevs/revs/keys"
	"github.com/openrevs/revs/ui/localdb"
)

// DefaultListen is the default address for `revs ui`.
const DefaultListen = "127.0.0.1:1325"

type Options struct {
	DataDir     string
	Listen      string
	OpenBrowser bool
	// WikidataURL overrides the Wikidata API endpoint (for tests).
	WikidataURL string
}

type App struct {
	dataDir  string
	db       *sql.DB
	q        *localdb.Queries
	versions *client.History
	http     *http.Client
	wikidata *wikidata
	pages    pages

	remotesMu sync.Mutex
	remotes   map[int64]*remote
}

// New opens (creating if needed) the data directory and local database.
func New(opts Options) (*App, error) {
	if opts.DataDir == "" {
		return nil, errors.New("ui: data directory is required")
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

	pages, err := parsePages()
	if err != nil {
		db.Close()
		return nil, err
	}

	httpClient := &http.Client{Timeout: 60 * time.Second}
	wdURL := opts.WikidataURL
	if wdURL == "" {
		wdURL = DefaultWikidataURL
	}

	return &App{
		dataDir:  opts.DataDir,
		db:       db,
		q:        localdb.New(db),
		versions: client.NewHistory(historyStore{db}),
		http:     httpClient,
		wikidata: &wikidata{baseURL: wdURL, http: &http.Client{Timeout: 15 * time.Second}},
		pages:    pages,
		remotes:  map[int64]*remote{},
	}, nil
}

func (a *App) Close() error {
	a.closeRemotes()
	return a.db.Close()
}

// Run serves the UI until ctx is canceled.
func Run(ctx context.Context, opts Options) error {
	app, err := New(opts)
	if err != nil {
		return err
	}
	defer app.Close()

	listen := opts.Listen
	if listen == "" {
		listen = DefaultListen
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}

	srv := &http.Server{
		Handler:           app.Handler(isLoopback(ln.Addr())),
		ReadHeaderTimeout: 10 * time.Second,
	}

	addr := ln.Addr().(*net.TCPAddr)
	host := addr.IP.String()
	if addr.IP.IsUnspecified() || addr.IP.IsLoopback() {
		host = "localhost"
	}
	uiURL := fmt.Sprintf("http://%s/", net.JoinHostPort(host, strconv.Itoa(addr.Port)))
	fmt.Printf("revs ui: serving %s (data in %s)\n", uiURL, opts.DataDir)

	if opts.OpenBrowser {
		if err := openBrowser(uiURL); err != nil {
			log.Printf("revs ui: could not open a browser (%v); visit %s", err, uiURL)
		}
	}

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

func isLoopback(addr net.Addr) bool {
	tcp, ok := addr.(*net.TCPAddr)
	return ok && tcp.IP.IsLoopback()
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// Settings keys.
const (
	settingPrivateKey = "private_key" // base64 ed25519 seed
	settingDocRev     = "doc_rev"     // revision of the last export, before the version history
)

func (a *App) setting(ctx context.Context, key string) string {
	v, err := a.q.GetSetting(ctx, key)
	if err != nil {
		return ""
	}
	return v
}

func (a *App) settingInt(ctx context.Context, key string) int {
	n, _ := strconv.Atoi(a.setting(ctx, key))
	return n
}

func (a *App) setSetting(ctx context.Context, key, value string) error {
	return a.q.SetSetting(ctx, localdb.SetSettingParams{Key: key, Value: value})
}

// privateKey returns the signing key, or nil if none is configured.
func (a *App) privateKey(ctx context.Context) (ed25519.PrivateKey, error) {
	v := a.setting(ctx, settingPrivateKey)
	if v == "" {
		return nil, nil
	}
	return keys.ParsePrivate(v)
}

// userID returns the id derived from the signing key, or "".
func (a *App) userID(ctx context.Context) string {
	key, err := a.privateKey(ctx)
	if err != nil || key == nil {
		return ""
	}
	return keys.UserID(key.Public().(ed25519.PublicKey))
}
