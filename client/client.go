// Package client is a generic consumer of the revs publishing scheme
// (docs/publishing.md). It pulls the published corpus — epochs, patches,
// snapshots, manifests — verifying everything against the server's
// signing key, and pushes signed documents with the revision discipline
// the protocol asks of clients.
//
// The package traffics in raw signed sjsonl documents and knows nothing
// about their contents; domain parsing layers on top. Local state lives
// behind the Store interface; the fsstore subpackage is the filesystem
// implementation (stdlib only), and sqlitestore keeps the whole corpus
// in a single SQLite database file. A Client is safe for concurrent
// use, but a store must be owned by a single process.
package client

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"iter"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/openrevs/revs/keys"
)

type Client struct {
	baseURL string
	http    *http.Client
	store   Store
	pin     ed25519.PublicKey // optional pre-pinned server key
	history *History

	mu    sync.Mutex
	state *State
}

type Option func(*Client)

// WithHTTPClient replaces the http.Client used for all requests.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.http = h }
}

// WithHistory sets the author's version History that Push commits to
// and builds patches from. An author pushing to several servers should
// share one History across their Clients, since it assigns revisions
// and servers can pull versions from each other. Without it, Push uses
// a History over the Client's Store, if the Store implements
// HistoryStore.
func WithHistory(h *History) Option {
	return func(c *Client) { c.history = h }
}

// WithServerKey pre-pins the server's public key, obtained out of band.
// Without it, the key is pinned trust-on-first-use from the server's
// index. Either way, a later mismatch is a hard KeyChangedError.
func WithServerKey(pub ed25519.PublicKey) Option {
	return func(c *Client) { c.pin = pub }
}

func New(baseURL string, store Store, opts ...Option) *Client {
	c := &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    http.DefaultClient,
		store:   store,
	}
	for _, opt := range opts {
		opt(c)
	}
	if hs, ok := store.(HistoryStore); ok && c.history == nil {
		c.history = NewHistory(hs)
	}
	return c
}

// Document is a verified signed sjsonl document held for a user.
type Document struct {
	UserID string
	Rev    int
	Hash   string
	Data   []byte
}

// Document returns the current document for a user, or ErrNotFound.
func (c *Client) Document(userID string) (*Document, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.ensureState(); err != nil {
		return nil, err
	}

	head, ok := c.state.Heads[userID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, userID)
	}

	data, err := c.store.GetBlob(head.Hash)
	if err != nil {
		return nil, err
	}

	return &Document{UserID: userID, Rev: head.Rev, Hash: head.Hash, Data: data}, nil
}

// Documents iterates over the current corpus in user id order.
func (c *Client) Documents() iter.Seq2[*Document, error] {
	return func(yield func(*Document, error) bool) {
		c.mu.Lock()
		if err := c.ensureState(); err != nil {
			c.mu.Unlock()
			yield(nil, err)
			return
		}
		heads := maps.Clone(c.state.Heads)
		c.mu.Unlock()

		for _, id := range slices.Sorted(maps.Keys(heads)) {
			head := heads[id]
			data, err := c.store.GetBlob(head.Hash)
			var doc *Document
			if err == nil {
				doc = &Document{UserID: id, Rev: head.Rev, Hash: head.Hash, Data: data}
			}
			if !yield(doc, err) {
				return
			}
		}
	}
}

// Epoch returns the last fully synced epoch, 0 before any sync.
func (c *Client) Epoch() (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.ensureState(); err != nil {
		return 0, err
	}

	return c.state.Epoch, nil
}

// Reload discards cached sync state, so the next call reads it from the
// store. Call it before using a store that another process may have
// written, while holding a lock that keeps that process out.
func (c *Client) Reload() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state = nil
}

// ensureState lazily loads persisted state. Callers hold c.mu.
func (c *Client) ensureState() error {
	if c.state != nil {
		return nil
	}

	state, err := c.store.LoadState()
	if err != nil {
		return err
	}
	if state == nil {
		state = &State{}
	}
	if state.Heads == nil {
		state.Heads = map[string]Head{}
	}
	if state.Push == nil {
		state.Push = map[string]PushState{}
	}

	c.state = state
	return nil
}

// resolvePin returns the base64 server key every manifest must be signed
// with: the stored pin, else the configured key, else (trust on first
// use) the key the index advertises. Callers hold c.mu.
func (c *Client) resolvePin(idx *serverIndex) (string, error) {
	pin := ""
	if c.pin != nil {
		pin = keys.EncodePublic(c.pin)
	}

	if stored := c.state.ServerKey; stored != "" {
		if pin != "" && pin != stored {
			return "", &KeyChangedError{Pinned: pin, Got: stored}
		}
		pin = stored
	}

	if pin == "" {
		if _, err := keys.ParsePublic(idx.ServerKey); err != nil {
			return "", fmt.Errorf("client: index advertises invalid server key %q: %w", idx.ServerKey, err)
		}
		pin = idx.ServerKey
	}

	return pin, nil
}

func (c *Client) fetch(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("client: GET %s: %s", path, resp.Status)
	}

	return io.ReadAll(resp.Body)
}

var errNoSnapshot = errors.New("client: bootstrap required but server lists no snapshots")
