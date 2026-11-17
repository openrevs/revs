// Package server is the revs publishing server: it accepts signed
// ratingset uploads, pulls documents from upstream servers, and
// periodically publishes everything as signed, static epochs.
//
// An application embeds a server by building one (Open for a data
// directory, New for explicit dependencies), mounting Handler on its
// own HTTP server, and calling Run for the background epoch cutter and
// upstream puller. ListenAndServe does all of that, as "revs server"
// does.
package server

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openrevs/revs/domain"
	"github.com/openrevs/revs/internal/httplog"
)

const (
	DefaultCutInterval   = time.Hour
	DefaultSnapshotEvery = 100

	// DefaultPartSize targets 256 MiB archive parts: comfortably under
	// CDN cacheable-object caps and a sane unit for parallel fetch/retry.
	DefaultPartSize = 256 << 20

	// DefaultListen is the address ListenAndServe uses when given none.
	DefaultListen = ":1323"
)

// DefaultCompactEvery is the default set of aligned compaction
// intervals, in epochs: with hourly cuts, daily and weekly.
var DefaultCompactEvery = []int{24, 168}

type Server struct {
	dal    domain.DAL
	db     *DB
	docs   BlobStore
	corpus *SQLiteStore // docs, plus the feeds pulled from upstreams
	// key is the server's identity: it signs every manifest, and its
	// derived id is how consumers track the server.
	key ed25519.PrivateKey

	// pub holds the published static file tree (served under /v1/).
	pub Publications

	channels      []channel
	cutInterval   time.Duration
	snapshotEvery int
	partSize      int
	// compactEvery lists the aligned intervals, in epochs, at which
	// patches are compacted into coarser ranges.
	compactEvery []int

	cutMu sync.Mutex

	httpClient   *http.Client // for upstreams
	pullInterval time.Duration
	upstreams    []*upstream
	pullMu       sync.Mutex

	logRequests bool
}

// Options overrides server tunables. Zero values keep the defaults.
type Options struct {
	CutInterval   time.Duration
	SnapshotEvery int
	PartSize      int
	// CompactEvery replaces the compaction intervals when non-nil; an
	// empty, non-nil slice disables compaction.
	CompactEvery []int

	// Pull lists the URLs of upstream servers to pull documents from,
	// every PullInterval.
	Pull         []string
	PullInterval time.Duration

	// LogRequests makes Handler log every HTTP request served (method,
	// path, status, size, duration); never bodies.
	LogRequests bool
}

// Config holds everything a Server depends on. Open builds one from a
// data directory; tests and embedding applications can build their
// own, e.g. with an in-memory database (OpenMemoryDB) and
// MemPublications, to run any number of servers in one process.
type Config struct {
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

	Options
}

// Open opens (creating if needed) a server whose database, key, and
// publications live in dataDir: revs.db, server.key, and public/v1.
func Open(dataDir string, opts Options) (*Server, error) {
	db, err := GetDB(dataDir)
	if err != nil {
		return nil, err
	}

	key, err := LoadOrCreateKey(dataDir)
	if err != nil {
		return nil, err
	}

	return New(Config{
		DB:           db,
		Key:          key,
		Publications: DirPublications{Root: filepath.Join(dataDir, "public", "v1")},
		Options:      opts,
	})
}

// New builds a server from explicit dependencies.
func New(cfg Config) (*Server, error) {
	if cfg.DB == nil || cfg.Key == nil || cfg.Publications == nil {
		return nil, errors.New("server config requires a DB, key, and publications")
	}

	docs, err := NewSQLiteStore(cfg.DB.DB)
	if err != nil {
		return nil, err
	}

	s := &Server{
		dal:           domain.NewDAL(cfg.DB.DB),
		db:            cfg.DB,
		docs:          docs,
		corpus:        docs,
		key:           cfg.Key,
		pub:           cfg.Publications,
		httpClient:    cfg.HTTPClient,
		cutInterval:   DefaultCutInterval,
		snapshotEvery: DefaultSnapshotEvery,
		partSize:      DefaultPartSize,
		compactEvery:  slices.Clone(DefaultCompactEvery),
		pullInterval:  DefaultPullInterval,
		logRequests:   cfg.LogRequests,
	}
	if s.httpClient == nil {
		s.httpClient = &http.Client{Timeout: upstreamTimeout}
	}
	s.channels = s.defaultChannels()

	if cfg.CutInterval > 0 {
		s.cutInterval = cfg.CutInterval
	}
	if cfg.SnapshotEvery > 0 {
		s.snapshotEvery = cfg.SnapshotEvery
	}
	if cfg.PartSize > 0 {
		s.partSize = cfg.PartSize
	}
	if cfg.CompactEvery != nil {
		s.compactEvery = slices.Clone(cfg.CompactEvery)
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

// Run runs the server's background work until ctx is done: an epoch
// cut every CutInterval, and a pull from every upstream right away and
// then every PullInterval. Applications that serve Handler themselves
// call Run alongside it; tests that want full control call Cut and Pull
// directly instead.
func (s *Server) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.runCutter(ctx)
	}()
	if len(s.upstreams) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.runPuller(ctx)
		}()
	}
	wg.Wait()
}

// ListenAndServe serves Handler on addr (DefaultListen if empty) and
// runs the background work (see Run) until ctx is done, then shuts the
// HTTP server down gracefully.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	if addr == "" {
		addr = DefaultListen
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	slog.Info("server listening", "addr", ln.Addr().String())
	return serve(ctx, ln, s.Handler(), s.Run)
}

// serve serves h on ln, with background running alongside, until ctx
// is done or the HTTP server fails.
func serve(ctx context.Context, ln net.Listener, h http.Handler, background func(context.Context)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		background(ctx)
	}()

	hs := &http.Server{Handler: h}
	errc := make(chan error, 1)
	go func() { errc <- hs.Serve(ln) }()

	var err error
	select {
	case err = <-errc:
	case <-ctx.Done():
		shutdownCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		err = hs.Shutdown(shutdownCtx)
		stop()
	}
	cancel()
	<-done
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return err
}

// PublicKey returns the server's public key, which clients pin.
func (s *Server) PublicKey() ed25519.PublicKey {
	return s.key.Public().(ed25519.PublicKey)
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
	if s.logRequests {
		return httplog.Middleware(nil, r)
	}
	return r
}

func (s *Server) routes(r chi.Router) {
	r.Post("/upload", s.uploadPost)

	// Publications are static files served as static files.
	fs := http.FileServer(s.pub.FS())
	r.Handle("/v1/*", http.StripPrefix("/v1/", fs))
}

// runCutter runs epoch cuts on a fixed interval until ctx is done. A
// tick with nothing accepted since the last cut does nothing. A cut in
// progress when ctx is done runs to completion.
func (s *Server) runCutter(ctx context.Context) {
	ticker := time.NewTicker(s.cutInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := s.Cut(context.WithoutCancel(ctx)); err != nil {
				slog.Error("epoch cut failed", "error", err)
			}
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("error encoding response", "error", err)
	}
}
