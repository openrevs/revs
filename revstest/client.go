package revstest

import (
	"context"
	"database/sql"

	"github.com/openrevs/revs/client"
	"github.com/openrevs/revs/client/sqlitestore"
	"github.com/openrevs/revs/collection"
	"github.com/openrevs/revs/internal/sqlitedb"
)

// NewStore returns a new in-memory sqlitestore database, which can hold
// the feeds of any number of servers and an author's history.
func (n *Network) NewStore() *sqlitestore.DB {
	n.t.Helper()

	db, err := sql.Open("sqlite", sqlitedb.MemoryDSN())
	if err != nil {
		n.t.Fatalf("revstest: opening store: %v", err)
	}
	// One connection: it also keeps the in-memory database alive.
	db.SetMaxOpenConns(1)
	closeOnCleanup(n.t, "store", db)

	store, err := sqlitestore.New(db)
	if err != nil {
		n.t.Fatalf("revstest: creating store: %v", err)
	}
	return store
}

// NewClient returns a client of s with its own in-memory store. Pass
// client.WithHistory to share an author's history across clients, as
// an author pushing to several servers does.
func (n *Network) NewClient(s *Server, opts ...client.Option) *client.Client {
	n.t.Helper()

	return n.NewClientOn(s, n.NewStore().Feed(s.Host), opts...)
}

// NewClientOn returns a client of s on the given store, e.g. a feed of
// a store shared with other clients, or one of the store to reopen.
func (n *Network) NewClientOn(s *Server, store client.Store, opts ...client.Option) *client.Client {
	opts = append([]client.Option{client.WithHTTPClient(n.httpClient)}, opts...)
	return client.New(s.URL, store, opts...)
}

// NewCollection returns a new, empty collection that reaches servers
// over the network. Wikidata searches find nothing.
//
// Unlike servers, a collection is kept in a temporary directory rather
// than in memory: it relies on WAL journaling (reads on one connection
// while another writes), which in-memory SQLite databases lack.
func (n *Network) NewCollection() *collection.Collection {
	n.t.Helper()

	c, err := collection.Open(collection.Options{
		DataDir:     n.t.TempDir(),
		HTTPClient:  n.httpClient,
		WikidataURL: "http://wikidata.test/w/api.php",
	})
	if err != nil {
		n.t.Fatalf("revstest: opening collection: %v", err)
	}
	closeOnCleanup(n.t, "collection", c)

	return c
}

// NewAuthor returns a new collection with a signing key, ready to
// publish.
func (n *Network) NewAuthor() *collection.Collection {
	n.t.Helper()

	c := n.NewCollection()
	if err := c.GenerateKey(context.Background()); err != nil {
		n.t.Fatalf("revstest: generating key: %v", err)
	}
	return c
}
