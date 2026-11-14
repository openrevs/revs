package revstest

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"net/http"
	"sync"

	"github.com/openrevs/revs"
)

// Server is a revs server running on a Network, with all its state in
// memory.
type Server struct {
	// URL is the server's base URL on the network, e.g.
	// http://server1.test.
	URL string
	// Host is URL's hostname.
	Host string
	// Key is the server's private key; clients pin its public half.
	Key ed25519.PrivateKey
	// DB is the server's in-memory database.
	DB *revs.DB
	// Publications holds what the server has published.
	Publications *revs.MemPublications

	net  *Network
	opts revs.ServerOptions

	mu  sync.Mutex
	srv *revs.Server
}

// ServerOption configures a server created by Network.NewServer.
type ServerOption func(*serverConfig)

type serverConfig struct {
	name string
	key  ed25519.PrivateKey
	opts revs.ServerOptions
}

// WithName sets the server's hostname (default serverN.test).
func WithName(hostname string) ServerOption {
	return func(c *serverConfig) { c.name = hostname }
}

// WithServerKey sets the server's key (default: a new one).
func WithServerKey(key ed25519.PrivateKey) ServerOption {
	return func(c *serverConfig) { c.key = key }
}

// WithServerOptions overrides server tunables, e.g. SnapshotEvery.
func WithServerOptions(opts revs.ServerOptions) ServerOption {
	return func(c *serverConfig) { c.opts = opts }
}

// NewServer starts a new server on the network. The server does not cut
// epochs on its own: tests call Cut when they want one.
func (n *Network) NewServer(opts ...ServerOption) *Server {
	n.t.Helper()

	n.mu.Lock()
	n.servers++
	cfg := serverConfig{name: fmt.Sprintf("server%d.test", n.servers)}
	n.mu.Unlock()

	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.key == nil {
		cfg.key = NewKey(n.t)
	}

	db, err := revs.OpenMemoryDB()
	if err != nil {
		n.t.Fatalf("revstest: opening server database: %v", err)
	}
	closeOnCleanup(n.t, cfg.name+" database", db)

	s := &Server{
		Host:         cfg.name,
		Key:          cfg.key,
		DB:           db,
		Publications: &revs.MemPublications{},
		net:          n,
		opts:         cfg.opts,
	}
	s.start()
	s.URL = n.Handle(cfg.name, http.HandlerFunc(s.serveHTTP))

	return s
}

// start builds a fresh revs.Server over s's state.
func (s *Server) start() {
	s.net.t.Helper()

	srv, err := revs.NewServerFromConfig(revs.ServerConfig{
		DB:            s.DB,
		Key:           s.Key,
		Publications:  s.Publications,
		ServerOptions: s.opts,
	})
	if err != nil {
		s.net.t.Fatalf("revstest: starting %s: %v", s.Host, err)
	}

	s.mu.Lock()
	s.srv = srv
	s.mu.Unlock()
}

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	s.Server().Handler().ServeHTTP(w, r)
}

// Server returns the running revs.Server.
func (s *Server) Server() *revs.Server {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.srv
}

// PublicKey returns the server's public key.
func (s *Server) PublicKey() ed25519.PublicKey {
	return s.Key.Public().(ed25519.PublicKey)
}

// Cut cuts an epoch, failing the test on error. It returns the epoch
// cut, or 0 if nothing changed.
func (s *Server) Cut() int {
	s.net.t.Helper()

	epoch, err := s.Server().Cut(context.Background())
	if err != nil {
		s.net.t.Fatalf("revstest: cutting %s: %v", s.Host, err)
	}
	return epoch
}

// Down makes the server unreachable until Up.
func (s *Server) Down() { s.net.SetDown(s.Host, true) }

// Up makes the server reachable again.
func (s *Server) Up() { s.net.SetDown(s.Host, false) }

// Restart replaces the running server with a new one over the same
// database, key, and publications, as a process restart would.
func (s *Server) Restart() {
	s.net.t.Helper()
	s.start()
}
