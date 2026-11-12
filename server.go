package revs

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"log"
	"net/http"
	"os"
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
	dal  domain.DAL
	db   *DB
	docs BlobStore
	key  ed25519.PrivateKey

	// pubDir is the root of the published static file tree (contains v1/).
	pubDir string

	cutInterval       time.Duration
	snapshotEvery     int
	snapshotChunkSize int

	cutMu sync.Mutex
}

func NewServer(dataDir string) (*Server, error) {
	db, err := GetDB(dataDir)
	if err != nil {
		return nil, err
	}

	docs, err := NewSQLiteStore(db.DB)
	if err != nil {
		return nil, err
	}

	key, err := loadOrCreateServerKey(dataDir)
	if err != nil {
		return nil, err
	}

	s := &Server{
		dal:               domain.NewDAL(db.DB),
		db:                db,
		docs:              docs,
		key:               key,
		pubDir:            filepath.Join(dataDir, "public"),
		cutInterval:       DefaultCutInterval,
		snapshotEvery:     DefaultSnapshotEvery,
		snapshotChunkSize: DefaultSnapshotChunkSize,
	}

	if err := os.MkdirAll(filepath.Join(s.pubDir, "v1"), 0755); err != nil {
		return nil, err
	}

	// The index is regenerated from the DB, so a crash between publishing
	// an epoch and advertising it self-heals here.
	if err := s.writeIndex(context.Background()); err != nil {
		return nil, err
	}

	return s, nil
}

// ServerOptions overrides server tunables. Zero values keep the
// defaults set in NewServer.
type ServerOptions struct {
	Listen            string
	CutInterval       time.Duration
	SnapshotEvery     int
	SnapshotChunkSize int
}

func Start2(dataDir string, opts ServerOptions) {
	server, err := NewServer(dataDir)
	if err != nil {
		panic(err)
	}

	if opts.CutInterval > 0 {
		server.cutInterval = opts.CutInterval
	}
	if opts.SnapshotEvery > 0 {
		server.snapshotEvery = opts.SnapshotEvery
	}
	if opts.SnapshotChunkSize > 0 {
		server.snapshotChunkSize = opts.SnapshotChunkSize
	}
	listen := opts.Listen
	if listen == "" {
		listen = DefaultServerListen
	}

	server.startCutter(context.Background())

	log.Printf("server listening on %s", listen)
	log.Fatal(http.ListenAndServe(listen, server.Handler()))
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
	fs := http.FileServer(http.Dir(filepath.Join(s.pubDir, "v1")))
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
