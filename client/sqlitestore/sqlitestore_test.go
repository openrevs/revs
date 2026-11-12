package sqlitestore_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/openrevs/revs"
	"github.com/openrevs/revs/client"
	"github.com/openrevs/revs/client/sqlitestore"
	"github.com/stretchr/testify/require"
)

func TestStoreContract(t *testing.T) {
	db, err := sqlitestore.Open(filepath.Join(t.TempDir(), "corpus.db"))
	require.NoError(t, err)
	defer db.Close()
	store := db.Feed("s")

	// No state yet.
	state, err := store.LoadState()
	require.NoError(t, err)
	require.Nil(t, state)

	// Full round trip, including push state.
	saved := &client.State{
		Epoch:        7,
		ManifestHash: "abcd",
		ServerKey:    "eeee",
		Heads:        map[string]client.Head{"u1": {Rev: 3, Hash: "ffff"}},
		Push:         map[string]client.PushState{"u1": {Rev: 3, Hash: "ffff", ServerRev: 4}},
	}
	require.NoError(t, store.SaveState(saved))

	loaded, err := store.LoadState()
	require.NoError(t, err)
	require.Equal(t, saved, loaded)

	// Saving replaces wholesale: removed entries don't linger.
	saved.Heads = map[string]client.Head{"u2": {Rev: 1, Hash: "aaaa"}}
	saved.Push = map[string]client.PushState{}
	require.NoError(t, store.SaveState(saved))

	loaded, err = store.LoadState()
	require.NoError(t, err)
	require.Equal(t, saved.Heads, loaded.Heads)
	require.Empty(t, loaded.Push)

	// Blobs round trip; re-put of the same hash is a no-op; bad hashes
	// are rejected.
	require.NoError(t, store.PutBlob("00ff", []byte("data")))
	require.NoError(t, store.PutBlob("00ff", []byte("data")))
	data, err := store.GetBlob("00ff")
	require.NoError(t, err)
	require.Equal(t, []byte("data"), data)

	_, err = store.GetBlob("beef")
	require.Error(t, err)
	require.Error(t, store.PutBlob("../evil", []byte("x")))
}

// TestClientRoundTrip runs the sqlite store under a real client against
// a real server, and confirms the corpus survives reopening the file.
func TestClientRoundTrip(t *testing.T) {
	ctx := context.Background()

	s, err := revs.NewServer(t.TempDir())
	require.NoError(t, err)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	uid := client.UserID(key.Public().(ed25519.PublicKey))

	var content bytes.Buffer
	fmt.Fprintf(&content, `{"id":%q,"min_rating":0,"max_rating":5}`+"\n", uid)
	content.WriteString(`{"name":"Alien"}` + "\n")

	dbPath := filepath.Join(t.TempDir(), "corpus.db")
	db, err := sqlitestore.Open(dbPath)
	require.NoError(t, err)

	c := client.New(ts.URL, db.Feed("s"))

	pr, err := c.Push(ctx, key, content.Bytes())
	require.NoError(t, err)
	require.True(t, pr.Pushed)
	require.Equal(t, 1, pr.Rev)

	_, err = s.Cut(ctx)
	require.NoError(t, err)

	res, err := c.Sync(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, res.Epoch)

	doc, err := c.Document(uid)
	require.NoError(t, err)
	require.Contains(t, string(doc.Data), "Alien")
	require.NoError(t, db.Close())

	// Reopen the same file: state and corpus persisted.
	db2, err := sqlitestore.Open(dbPath)
	require.NoError(t, err)
	defer db2.Close()

	c2 := client.New(ts.URL, db2.Feed("s"))

	epoch, err := c2.Epoch()
	require.NoError(t, err)
	require.Equal(t, 1, epoch)

	doc, err = c2.Document(uid)
	require.NoError(t, err)
	require.Equal(t, 1, doc.Rev)
	require.Contains(t, string(doc.Data), "Alien")

	// An identical push through the reopened store is unchanged.
	pr, err = c2.Push(ctx, key, content.Bytes())
	require.NoError(t, err)
	require.False(t, pr.Pushed)
	require.Equal(t, 1, pr.Rev)
}
