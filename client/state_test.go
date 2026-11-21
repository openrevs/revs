package client_test

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/openrevs/revs/client"
	"github.com/openrevs/revs/client/sqlitestore"
	revkeys "github.com/openrevs/revs/keys"
	"github.com/stretchr/testify/require"
)

// requireSaved checks that a fresh client on the store finds the corpus
// c holds in memory: that every change c made was saved.
func requireSaved(t *testing.T, c *client.Client, url string, store client.Store) {
	t.Helper()
	fresh := client.New(url, store)
	wantEpoch, err := c.Epoch()
	require.NoError(t, err)
	gotEpoch, err := fresh.Epoch()
	require.NoError(t, err)
	require.Equal(t, wantEpoch, gotEpoch)

	docs := func(c *client.Client) map[string]int {
		out := map[string]int{}
		for d, err := range c.Documents() {
			require.NoError(t, err)
			out[d.UserID] = d.Rev
		}
		return out
	}
	require.Equal(t, docs(c), docs(fresh))
}

// TestStateSavedIncrementally runs syncs and pushes through a store that
// saves only what changed, checking after each that the store holds
// what the client does.
func TestStateSavedIncrementally(t *testing.T) {
	s, ts := startServer(t)
	db, err := sqlitestore.Open(filepath.Join(t.TempDir(), "c.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()

	keys := []ed25519.PrivateKey{newKey(t), newKey(t), newKey(t)}
	pusher := client.New(ts.URL, db.Feed("pusher"))
	push := func(i int, titles ...string) *client.PushResult {
		t.Helper()
		res, err := pusher.Push(ctx, keys[i], content(keys[i], titles...))
		require.NoError(t, err)
		return res
	}
	sub := client.New(ts.URL, db.Feed("sub"))
	sync := func() {
		t.Helper()
		_, err := sub.Sync(ctx)
		require.NoError(t, err)
		requireSaved(t, sub, ts.URL, db.Feed("sub"))
	}

	push(0, "A")
	push(1, "B")
	cut(t, s)
	sync() // bootstraps

	push(0, "A", "A2")
	push(2, "C")
	cut(t, s)
	push(1, "B", "B2")
	cut(t, s)
	sync() // two patches

	cut(t, s)
	sync() // nothing new

	_, err = sub.Rebootstrap(ctx)
	require.NoError(t, err)
	requireSaved(t, sub, ts.URL, db.Feed("sub"))

	// The pusher's state was saved too: a fresh client knows the server
	// holds these versions, so it has nothing to upload.
	fresh := client.New(ts.URL, db.Feed("pusher"))
	for i, titles := range [][]string{{"A", "A2"}, {"B", "B2"}, {"C"}} {
		res, err := fresh.Push(ctx, keys[i], content(keys[i], titles...))
		require.NoError(t, err)
		require.False(t, res.Pushed, "author %d", i)
	}
}

// A change whose save fails isn't lost: it's saved with the next one.
func TestFailedSaveRetried(t *testing.T) {
	s, ts := startServer(t)
	db, err := sqlitestore.Open(filepath.Join(t.TempDir(), "c.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()

	store := &flakyStore{Store: db.Feed("f")}
	c := client.New(ts.URL, store, client.WithHistory(client.NewHistory(db)))
	key := newKey(t)
	uid := revkeys.UserID(key.Public().(ed25519.PublicKey))
	_, err = c.Push(ctx, key, content(key, "A"))
	require.NoError(t, err)
	cut(t, s)
	_, err = c.Sync(ctx)
	require.NoError(t, err)

	// The upload goes through, but its push state isn't saved; nor is
	// the corpus the next sync brings.
	store.fail = true
	_, err = c.Push(ctx, key, content(key, "A", "B"))
	require.ErrorIs(t, err, errFlaky)
	cut(t, s)
	_, err = c.Sync(ctx)
	require.ErrorIs(t, err, errFlaky)
	epoch, err := c.Epoch()
	require.NoError(t, err)
	require.Equal(t, 2, epoch)

	// The next save that works carries all of it.
	store.fail = false
	other := newKey(t)
	_, err = c.Push(ctx, other, content(other, "X"))
	require.NoError(t, err)
	requireSaved(t, c, ts.URL, db.Feed("f"))
	st, err := db.Feed("f").LoadState()
	require.NoError(t, err)
	require.Equal(t, 2, st.Push[uid].Rev)
	require.Equal(t, 2, st.Heads[uid].Rev)
}

var errFlaky = errors.New("flaky store")

// flakyStore fails UpdateState while fail is set.
type flakyStore struct {
	client.Store
	fail bool
}

func (f *flakyStore) UpdateState(ch client.StateChange) error {
	if f.fail {
		return errFlaky
	}
	return f.Store.UpdateState(ch)
}

// countingStore counts PutBlobs calls.
type countingStore struct {
	client.Store
	puts int
}

func (c *countingStore) PutBlobs(blobs []client.Blob) error {
	c.puts++
	return c.Store.PutBlobs(blobs)
}

// A sync stores what it brings in batches, and ends up with all of it
// however many batches that takes.
func TestSyncStoresInBatches(t *testing.T) {
	defer client.SetBlobBatch(2, 1<<20)()
	s, ts := startServer(t)
	ctx := context.Background()

	pusher := newClient(t, ts.URL)
	var keys []ed25519.PrivateKey
	for i := range 7 {
		k := newKey(t)
		keys = append(keys, k)
		_, err := pusher.Push(ctx, k, content(k, fmt.Sprintf("T%d", i)))
		require.NoError(t, err)
	}
	cut(t, s)

	db, err := sqlitestore.Open(filepath.Join(t.TempDir(), "c.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	store := &countingStore{Store: db.Feed("f")}
	c := client.New(ts.URL, store)
	res, err := c.Sync(ctx)
	require.NoError(t, err)
	require.Len(t, res.Changed, 7)
	require.Equal(t, 4, store.puts, "7 documents, 2 at a time")
	requireSaved(t, c, ts.URL, db.Feed("f"))

	// New revisions arrive as patches against the stored documents.
	store.puts = 0
	for i, k := range keys[:5] {
		_, err := pusher.Push(ctx, k, content(k, fmt.Sprintf("T%d", i), "more"))
		require.NoError(t, err)
	}
	cut(t, s)
	res, err = c.Sync(ctx)
	require.NoError(t, err)
	require.Len(t, res.Changed, 5)
	require.Equal(t, 3, store.puts)
	requireSaved(t, c, ts.URL, db.Feed("f"))
	for _, k := range keys[:5] {
		d, err := c.Document(revkeys.UserID(k.Public().(ed25519.PublicKey)))
		require.NoError(t, err)
		require.Equal(t, 2, d.Rev)
	}
}
