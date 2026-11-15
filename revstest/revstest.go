// Package revstest builds in-process networks of revs servers and
// clients for tests, so multi-server, multi-client behavior is tested
// by `go test ./...` alone: no extra processes and no listening
// sockets.
//
// A Network routes HTTP between the servers and clients created on it.
// Each server gets a hostname (http://server1.test, ...) and keeps its
// database, key, and publications in memory; each client keeps its
// store in memory, and each collection its data in a temporary
// directory. All reach servers through the Network's transport:
//
//	net := revstest.NewNetwork(t)
//	a, b := net.NewServer(), net.NewServer()
//
//	author := net.NewAuthor() // a collection.Collection with a key
//	srv, err := author.AddServer(ctx, a.URL)
//	...
//	_, err = author.Push(ctx, srv.ID)
//	a.Cut()
//
//	reader := net.NewClient(a) // a client.Client on an in-memory store
//	_, err = reader.Sync(ctx)
//
// Servers can be taken down (requests fail as unreachable) and brought
// back, or restarted on their existing state, to test sync dynamics.
//
// Everything is safe for concurrent use, so tests may drive many
// clients at once, and tests using separate Networks may run in
// parallel.
package revstest

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// Network is an in-process HTTP network: a registry of hosts and a
// transport that delivers requests straight to their handlers.
type Network struct {
	t testing.TB

	mu      sync.Mutex
	hosts   map[string]*host
	servers int

	httpClient *http.Client
}

type host struct {
	handler http.Handler
	down    bool
}

// NewNetwork returns an empty network. Everything created on it is
// closed when the test ends.
func NewNetwork(t testing.TB) *Network {
	n := &Network{t: t, hosts: map[string]*host{}}
	n.httpClient = &http.Client{Transport: n}

	// Collections search Wikidata; keep tests off the internet.
	n.Handle("wikidata.test", http.NotFoundHandler())

	return n
}

// HTTPClient returns an http.Client whose requests go over the
// network. It fails requests to any host not registered on it.
func (n *Network) HTTPClient() *http.Client { return n.httpClient }

// Handle registers h as the host hostname and returns its base URL,
// e.g. to add a fake or misbehaving server to the network.
func (n *Network) Handle(hostname string, h http.Handler) string {
	n.mu.Lock()
	defer n.mu.Unlock()

	if _, ok := n.hosts[hostname]; ok {
		n.t.Fatalf("revstest: host %s already registered", hostname)
	}
	n.hosts[hostname] = &host{handler: h}

	return "http://" + hostname
}

// SetDown makes a host unreachable (down) or reachable again.
func (n *Network) SetDown(hostname string, down bool) {
	n.mu.Lock()
	defer n.mu.Unlock()

	h, ok := n.hosts[hostname]
	if !ok {
		n.t.Fatalf("revstest: unknown host %s", hostname)
	}
	h.down = down
}

func (n *Network) handler(hostname string) (http.Handler, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	h, ok := n.hosts[hostname]
	if !ok || h.down {
		// What a refused connection looks like to callers.
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: fmt.Errorf("revstest: host %s unreachable", hostname)}
	}
	return h.handler, nil
}

// RoundTrip implements http.RoundTripper by serving the request in
// process with the target host's handler.
func (n *Network) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		defer req.Body.Close()
	}

	h, err := n.handler(req.URL.Hostname())
	if err != nil {
		return nil, err
	}
	if err := req.Context().Err(); err != nil {
		return nil, err
	}

	// Make the request look as it would arriving at a server.
	in := req.Clone(req.Context())
	in.RequestURI = req.URL.RequestURI()
	in.RemoteAddr = "192.0.2.1:1234"
	if in.Body == nil {
		in.Body = http.NoBody
	}
	if in.Host == "" {
		in.Host = req.URL.Host
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, in)

	resp := rec.Result()
	resp.Request = req
	return resp, nil
}

// NewKey returns a new ed25519 private key.
func NewKey(t testing.TB) ed25519.PrivateKey {
	t.Helper()

	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// closeOnCleanup closes c when the test ends, failing it on error.
func closeOnCleanup(t testing.TB, what string, c io.Closer) {
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Errorf("revstest: closing %s: %v", what, err)
		}
	})
}
