package revs

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openrevs/revs/domain"
	"log/slog"
)

const (
	DefaultCutInterval   = time.Hour
	DefaultSnapshotEvery = 100

	// DefaultPartSize targets 256 MiB archive parts: comfortably under
	// CDN cacheable-object caps and a sane unit for parallel fetch/retry.
	DefaultPartSize = 256 << 20

	DefaultServerListen = ":1323"
)

// DefaultCompactEvery is the default set of aligned compaction
// intervals, in epochs: with hourly cuts, daily and weekly.
var DefaultCompactEvery = []int{24, 168}

type Server struct {
	dal  domain.DAL
	db   *DB
	docs BlobStore
	// key is the server's identity: it signs every manifest, and its
	// derived id is how consumers track the server.
	key ed25519.PrivateKey

	// pubDir is the root of the published static file tree (contains v1/).
	pubDir string

	channels      []channel
	cutInterval   time.Duration
	snapshotEvery int
	partSize      int
	// compactEvery lists the aligned intervals, in epochs, at which
	// patches are compacted into coarser ranges.
	compactEvery []int

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
		dal:           domain.NewDAL(db.DB),
		db:            db,
		docs:          docs,
		key:           key,
		pubDir:        filepath.Join(dataDir, "public"),
		cutInterval:   DefaultCutInterval,
		snapshotEvery: DefaultSnapshotEvery,
		partSize:      DefaultPartSize,
		compactEvery:  slices.Clone(DefaultCompactEvery),
	}
	s.channels = s.defaultChannels()

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
	Listen        string
	CutInterval   time.Duration
	SnapshotEvery int
	PartSize      int
	// CompactEvery replaces the compaction intervals when non-nil; an
	// empty, non-nil slice disables compaction.
	CompactEvery []int
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
	if opts.PartSize > 0 {
		server.partSize = opts.PartSize
	}
	if opts.CompactEvery != nil {
		server.compactEvery = slices.Clone(opts.CompactEvery)
	}
	listen := opts.Listen
	if listen == "" {
		listen = DefaultServerListen
	}

	server.startCutter(context.Background())

	log.Printf("server listening on %s", listen)
	log.Fatal(http.ListenAndServe(listen, server.Handler()))
}

// SetPartSize overrides the archive part target size in bytes. It
// exists so tests can exercise multi-part archives without gigabytes of
// fixture data; the default suits production.
func (s *Server) SetPartSize(n int) {
	s.partSize = n
}

// SetCompactEvery overrides the patch compaction intervals.
func (s *Server) SetCompactEvery(intervals ...int) {
	s.compactEvery = slices.Clone(intervals)
}

// SetSnapshotEvery overrides the snapshot cadence, in epochs.
func (s *Server) SetSnapshotEvery(n int) {
	s.snapshotEvery = n
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
