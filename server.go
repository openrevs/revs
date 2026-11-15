package revs

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openrevs/revs/domain"
	"log/slog"
)

const (
	DefaultCutInterval   = time.Hour
	DefaultSnapshotEvery = 100

	// DefaultSnapshotChunkSize targets 256 MiB chunks: comfortably under
	// CDN cacheable-object caps and a sane unit for parallel fetch/retry.
	DefaultSnapshotChunkSize = 256 << 20

	DefaultServerListen = ":1323"
)

type Server struct {
	dal    domain.DAL
	db     *DB
	docs   BlobStore
	corpus *SQLiteStore // docs, plus the feeds pulled from upstreams
	key    ed25519.PrivateKey

	// pub holds the published static file tree (served under /v1/).
	pub Publications

	cutInterval       time.Duration
	snapshotEvery     int
	snapshotChunkSize int

	cutMu sync.Mutex

	httpClient   *http.Client // for upstreams
	pullInterval time.Duration
	upstreams    []*upstream
	pullMu       sync.Mutex
}

// ServerOptions overrides server tunables. Zero values keep the
// defaults.
type ServerOptions struct {
	Listen            string
	CutInterval       time.Duration
	SnapshotEvery     int
	SnapshotChunkSize int

	// Pull lists the URLs of upstream servers to pull documents from,
	// every PullInterval.
	Pull         []string
	PullInterval time.Duration
}

// ServerConfig holds everything a Server depends on. NewServer builds
// one from a data directory; tests build their own, typically with an
// in-memory database (OpenMemoryDB) and MemPublications, to run any
// number of servers in one process.
type ServerConfig struct {
	// DB is the server's migrated database (GetDB or OpenMemoryDB).
	DB *DB
	// Key is the server's identity: it signs manifests, and clients
	// pin it. Required.
	Key ed25519.PrivateKey
	// Publications receives the published file tree.
	Publications Publications
	// HTTPClient is used to pull from upstreams. Optional; tests use it
	// to reach in-process servers (see the revstest package).
	HTTPClient *http.Client

	ServerOptions
}

// NewServer opens (creating if needed) a server whose database, key,
// and publications live in dataDir.
func NewServer(dataDir string) (*Server, error) {
	return NewServerWithOptions(dataDir, ServerOptions{})
}

// NewServerWithOptions is NewServer with tunables overridden.
func NewServerWithOptions(dataDir string, opts ServerOptions) (*Server, error) {
	db, err := GetDB(dataDir)
	if err != nil {
		return nil, err
	}

	key, err := loadOrCreateServerKey(dataDir)
	if err != nil {
		return nil, err
	}

	return NewServerFromConfig(ServerConfig{
		DB:            db,
		Key:           key,
		Publications:  DirPublications{Root: filepath.Join(dataDir, "public", "v1")},
		ServerOptions: opts,
	})
}

// NewServerFromConfig builds a server from explicit dependencies.
func NewServerFromConfig(cfg ServerConfig) (*Server, error) {
	if cfg.DB == nil || cfg.Key == nil || cfg.Publications == nil {
		return nil, errors.New("server config requires a DB, key, and publications")
	}

	docs, err := NewSQLiteStore(cfg.DB.DB)
	if err != nil {
		return nil, err
	}

	s := &Server{
		dal:               domain.NewDAL(cfg.DB.DB),
		db:                cfg.DB,
		docs:              docs,
		corpus:            docs,
		key:               cfg.Key,
		pub:               cfg.Publications,
		httpClient:        cfg.HTTPClient,
		cutInterval:       DefaultCutInterval,
		snapshotEvery:     DefaultSnapshotEvery,
		snapshotChunkSize: DefaultSnapshotChunkSize,
		pullInterval:      DefaultPullInterval,
	}
	if s.httpClient == nil {
		s.httpClient = &http.Client{Timeout: upstreamTimeout}
	}

	if cfg.CutInterval > 0 {
		s.cutInterval = cfg.CutInterval
	}
	if cfg.SnapshotEvery > 0 {
		s.snapshotEvery = cfg.SnapshotEvery
	}
	if cfg.SnapshotChunkSize > 0 {
		s.snapshotChunkSize = cfg.SnapshotChunkSize
	}
	if cfg.PullInterval > 0 {
		s.pullInterval = cfg.PullInterval
	}
	for _, u := range cfg.Pull {
		if err := s.AddUpstream(u); err != nil {
			return nil, err
		}
	}

	// The index is regenerated from the DB, so a crash between publishing
	// an epoch and advertising it self-heals here.
	if err := s.writeIndex(context.Background()); err != nil {
		return nil, err
	}

	return s, nil
}

func Start2(dataDir string, opts ServerOptions) {
	server, err := NewServerWithOptions(dataDir, opts)
	if err != nil {
		log.Fatal(err)
	}

	listen := opts.Listen
	if listen == "" {
		listen = DefaultServerListen
	}

	server.startCutter(context.Background())
	server.startPuller(context.Background(), server.pullInterval)

	log.Printf("server listening on %s", listen)
	log.Fatal(http.ListenAndServe(listen, server.Handler()))
}

// PublicKey returns the server's public key, which clients pin.
func (s *Server) PublicKey() ed25519.PublicKey {
	return s.key.Public().(ed25519.PublicKey)
}

// SetSnapshotChunkSize overrides the snapshot chunk target size in
// bytes. It exists so tests can exercise multi-chunk snapshots without
// gigabytes of fixture data; the default suits production.
func (s *Server) SetSnapshotChunkSize(n int) {
	s.snapshotChunkSize = n
}

// Handler returns the server's HTTP handler, for embedding or tests.
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	s.routes(r)
	return r
}

func (s *Server) routes(r chi.Router) {
	r.Post("/upload", s.uploadPost)

	// Publications are static files served as static files.
	fs := http.FileServer(s.pub.FS())
	r.Handle("/v1/*", http.StripPrefix("/v1/", fs))
}

// startCutter runs epoch cuts on a fixed interval. A tick with nothing
// accepted since the last cut does nothing.
func (s *Server) startCutter(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(s.cutInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := s.Cut(ctx); err != nil {
					slog.Error("epoch cut failed", "error", err)
				}
			}
		}
	}()
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("error encoding response", "error", err)
	}
}
