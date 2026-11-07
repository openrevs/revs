// Package workspace owns everything about a dmrs TUI data directory
// except the terminal UI itself: the config file, the signing key, the
// unsigned draft of the user's ratingset, and push/pull orchestration
// built on the client package. It has no bubbletea dependency and is
// fully testable headless.
//
// Layout of a data dir:
//
//	config.json          server URL
//	user.key             hex ed25519 private key, 0600
//	draft.jsonl          unsigned working copy: one user line + one line per title
//	draft.meta.json      base rev/hash of the head the draft derives from
//	draft.jsonl.bak-*    backups written before a destructive rebase
//	store/               fsstore corpus + sync state
package workspace

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/kalafut/dmrs/client"
	"github.com/kalafut/dmrs/client/fsstore"
)

const (
	configFile = "config.json"
	keyFile    = "user.key"
	draftFile  = "draft.jsonl"
	metaFile   = "draft.meta.json"
	storeDir   = "store"
)

var (
	ErrNoKey    = errors.New("workspace: no signing key")
	ErrNoServer = errors.New("workspace: no server URL configured")
	ErrNoDraft  = errors.New("workspace: no draft loaded")
)

type Options struct {
	DataDir string
	// ServerFlag is the --server value; when set it is persisted to
	// config.json immediately (set once, then plain `dmrs tui` works).
	ServerFlag string
}

type Workspace struct {
	dataDir string
	cfg     Config
	key     ed25519.PrivateKey
	client  *client.Client

	draft     *Draft
	meta      Meta
	metaFound bool
	dirty     bool
}

// Open loads config, key, and sync metadata. It does not touch the
// network and does not load the draft — call LoadDraft (or
// NewEmptyDraft) so its errors can be surfaced separately.
func Open(opts Options) (*Workspace, error) {
	if opts.DataDir == "" {
		return nil, errors.New("workspace: data dir is required")
	}
	if err := os.MkdirAll(opts.DataDir, 0o700); err != nil {
		return nil, err
	}

	w := &Workspace{dataDir: opts.DataDir}
	if err := w.loadConfig(); err != nil {
		return nil, err
	}
	if opts.ServerFlag != "" && opts.ServerFlag != w.cfg.ServerURL {
		if err := w.SetServer(opts.ServerFlag); err != nil {
			return nil, err
		}
	}
	if err := w.loadKey(); err != nil {
		return nil, err
	}
	if err := w.loadMeta(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *Workspace) path(name string) string {
	return filepath.Join(w.dataDir, name)
}

func (w *Workspace) NeedsServer() bool { return w.cfg.ServerURL == "" }
func (w *Workspace) NeedsKey() bool    { return w.key == nil }

// SetServer validates and persists the server URL.
func (w *Workspace) SetServer(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("workspace: invalid server URL: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("workspace: server URL must be http(s)://host[:port], got %q", rawURL)
	}
	w.cfg.ServerURL = rawURL
	return w.saveConfig()
}

// InitClient builds the protocol client over an fsstore in the data
// dir. Safe to call more than once.
func (w *Workspace) InitClient() error {
	if w.client != nil {
		return nil
	}
	if w.cfg.ServerURL == "" {
		return ErrNoServer
	}
	st, err := fsstore.New(w.path(storeDir))
	if err != nil {
		return err
	}
	w.client = client.New(w.cfg.ServerURL, st)
	return nil
}

// UserID returns the 32-hex user id derived from the signing key, or
// "" when no key is loaded.
func (w *Workspace) UserID() string {
	if w.key == nil {
		return ""
	}
	return client.UserID(w.key.Public().(ed25519.PublicKey))
}

func (w *Workspace) ServerURL() string   { return w.cfg.ServerURL }
func (w *Workspace) BaseRev() int        { return w.meta.BaseRev }
func (w *Workspace) LastSync() time.Time { return w.meta.LastSync }
func (w *Workspace) LastPush() time.Time { return w.meta.LastPush }
func (w *Workspace) Dirty() bool         { return w.dirty }
func (w *Workspace) HasDraft() bool      { return w.draft != nil }

func (w *Workspace) DraftPath() string { return w.path(draftFile) }

func (w *Workspace) DraftExists() bool {
	_, err := os.Stat(w.path(draftFile))
	return err == nil
}
