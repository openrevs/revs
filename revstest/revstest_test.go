package revstest_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"

	"github.com/openrevs/revs/client"
	"github.com/openrevs/revs/collection"
	"github.com/openrevs/revs/revstest"
	"github.com/openrevs/revs/server"
	"github.com/stretchr/testify/require"
)

func ptr[T any](v T) *T { return &v }

// content builds unsigned ratingset lines for key: a user line plus a
// title line per name.
func content(key ed25519.PrivateKey, titles ...string) []byte {
	uid := client.UserID(key.Public().(ed25519.PublicKey))

	var buf bytes.Buffer
	fmt.Fprintf(&buf, `{"id":%q,"min_rating":0,"max_rating":5}`+"\n", uid)
	for _, title := range titles {
		fmt.Fprintf(&buf, `{"name":%q}`+"\n", title)
	}
	return buf.Bytes()
}

// An author publishes to several servers; a reader following all of
// them sees the review once, and keeps it when one server goes away.
func TestCollectionsAcrossServers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	network := revstest.NewNetwork(t)

	servers := []*revstest.Server{network.NewServer(), network.NewServer(), network.NewServer()}

	author := network.NewAuthor()
	m, _, err := author.AddMovie(ctx, collection.MovieInput{Title: "Alien", Year: ptr[int64](1979)})
	require.NoError(t, err)
	_, err = author.AddViewing(ctx, m.ID, collection.ViewingInput{Rating: ptr[int64](10), Review: "Perfect."})
	require.NoError(t, err)

	for _, s := range servers {
		srv, err := author.AddServer(ctx, s.URL)
		require.NoError(t, err)
		_, err = author.Push(ctx, srv.ID)
		require.NoError(t, err)
		require.Equal(t, 1, s.Cut())
	}

	reader := network.NewAuthor()
	var ids []int64
	for _, s := range servers {
		srv, err := reader.AddServer(ctx, s.URL)
		require.NoError(t, err)
		_, err = reader.Pull(ctx, srv.ID, false)
		require.NoError(t, err)
		ids = append(ids, srv.ID)
	}

	rm, _, err := reader.AddMovie(ctx, collection.MovieInput{Title: "Alien", Year: ptr[int64](1979)})
	require.NoError(t, err)
	reviews, err := reader.OthersReviews(ctx, rm)
	require.NoError(t, err)
	require.Len(t, reviews, 1, "seen on every server, listed once")
	require.Equal(t, "Perfect.", reviews[0].Comment)

	// One server goes down: pulling from it fails, the others still work.
	servers[0].Down()
	_, err = reader.Pull(ctx, ids[0], false)
	var opErr *net.OpError
	require.ErrorAs(t, err, &opErr)
	_, err = reader.Pull(ctx, ids[1], false)
	require.NoError(t, err)

	require.NoError(t, reader.RemoveServer(ctx, ids[1]))
	reviews, err = reader.OthersReviews(ctx, rm)
	require.NoError(t, err)
	require.Len(t, reviews, 1, "still held from the other servers")
}

// Many clients push to and sync from several servers concurrently.
func TestManyClients(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	network := revstest.NewNetwork(t)

	const nServers, nAuthors = 3, 20
	var servers []*revstest.Server
	for range nServers {
		servers = append(servers, network.NewServer())
	}

	var wg sync.WaitGroup
	errs := make(chan error, nServers*nAuthors)
	for i := range nAuthors {
		wg.Go(func() {
			key := revstest.NewKey(t)
			// One history per author, shared by their clients, as an
			// author's revisions are theirs across servers.
			history := client.NewHistory(network.NewStore())
			for _, s := range servers {
				c := network.NewClient(s, client.WithHistory(history))
				if _, err := c.Push(ctx, key, content(key, fmt.Sprintf("Movie %d", i))); err != nil {
					errs <- err
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	for _, s := range servers {
		require.Equal(t, 1, s.Cut())

		reader := network.NewClient(s, client.WithServerKey(s.PublicKey()))
		_, err := reader.Sync(ctx)
		require.NoError(t, err)

		n := 0
		for doc, err := range reader.Documents() {
			require.NoError(t, err)
			require.NotEmpty(t, doc.Data)
			n++
		}
		require.Equal(t, nAuthors, n, s.Host)
	}
}

// A restarted server serves what it published before, and a client
// picks up from where it left off.
func TestServerRestart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	network := revstest.NewNetwork(t)

	s := network.NewServer(revstest.WithName("origin.test"), revstest.WithServerOptions(server.Options{SnapshotEvery: 2}))
	require.Equal(t, "http://origin.test", s.URL)

	key := revstest.NewKey(t)
	author := network.NewClient(s)
	_, err := author.Push(ctx, key, content(key, "Alien"))
	require.NoError(t, err)
	require.Equal(t, 1, s.Cut())

	reader := network.NewClient(s)
	_, err = reader.Sync(ctx)
	require.NoError(t, err)

	s.Restart()

	_, err = author.Push(ctx, key, content(key, "Alien", "Aliens"))
	require.NoError(t, err)
	require.Equal(t, 2, s.Cut())

	_, err = reader.Sync(ctx)
	require.NoError(t, err)
	epoch, err := reader.Epoch()
	require.NoError(t, err)
	require.Equal(t, 2, epoch)

	doc, err := reader.Document(client.UserID(key.Public().(ed25519.PublicKey)))
	require.NoError(t, err)
	require.Equal(t, 2, doc.Rev)

	// The snapshot cadence came through the restart.
	_, err = s.Publications.ReadFile("main/snapshot-manifest-2.sjsonl.gz")
	require.NoError(t, err)
}

func TestUnknownHost(t *testing.T) {
	t.Parallel()
	network := revstest.NewNetwork(t)

	_, err := network.HTTPClient().Get("http://nowhere.test/v1/index.json")
	var opErr *net.OpError
	require.True(t, errors.As(err, &opErr), "got %v", err)
}

// Documents travel down a chain of servers pulling from each other: an
// author publishes to the origin, a mirror pulls from it, and an edge
// pulls from the mirror. Upstream outages and restarts don't lose the
// chain.
func TestServerPullChain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	network := revstest.NewNetwork(t)

	origin, mirror, edge := network.NewServer(), network.NewServer(), network.NewServer()
	mirror.AddUpstream(origin)
	edge.AddUpstream(mirror)

	key := revstest.NewKey(t)
	uid := client.UserID(key.Public().(ed25519.PublicKey))
	author := network.NewClient(origin)
	_, err := author.Push(ctx, key, content(key, "Alien"))
	require.NoError(t, err)
	origin.Cut()

	propagate := func() {
		t.Helper()
		require.NoError(t, mirror.Pull())
		mirror.Cut()
		require.NoError(t, edge.Pull())
		edge.Cut()
	}
	propagate()

	reader := network.NewClient(edge)
	_, err = reader.Sync(ctx)
	require.NoError(t, err)
	doc, err := reader.Document(uid)
	require.NoError(t, err)
	require.Equal(t, 1, doc.Rev)

	// The origin goes down: the mirror's pull fails, and nothing new
	// arrives downstream.
	origin.Down()
	var opErr *net.OpError
	require.ErrorAs(t, mirror.Pull(), &opErr)

	// Back up, with a new revision, and the mirror restarted meanwhile:
	// it still pulls from the origin.
	origin.Up()
	_, err = author.Push(ctx, key, content(key, "Alien", "Aliens"))
	require.NoError(t, err)
	origin.Cut()
	mirror.Restart()
	propagate()

	_, err = reader.Sync(ctx)
	require.NoError(t, err)
	doc, err = reader.Document(uid)
	require.NoError(t, err)
	require.Equal(t, 2, doc.Rev)
}
