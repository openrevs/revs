package fsstore_test

import (
	"path/filepath"
	"testing"

	"github.com/openrevs/revs/client"
	"github.com/openrevs/revs/client/fsstore"
	"github.com/openrevs/revs/client/storetest"
	"github.com/stretchr/testify/require"
)

func TestState(t *testing.T) {
	storetest.TestState(t, func(t *testing.T) client.Store {
		store, err := fsstore.New(t.TempDir())
		require.NoError(t, err)
		return store
	})
}

func TestStoreContract(t *testing.T) {
	dir := t.TempDir()
	store, err := fsstore.New(dir)
	require.NoError(t, err)

	heads := map[string]*client.Head{"u2": {Rev: 1, Hash: "aaaa"}}
	require.NoError(t, store.UpdateState(client.StateChange{Channel: "main", Heads: heads}))

	// Blobs round trip; re-put of the same hash is a no-op; bad hashes
	// are rejected (they are also path segments here).
	require.NoError(t, store.PutBlob("00ff", []byte("data")))
	require.NoError(t, store.PutBlob("00ff", []byte("data")))
	data, err := store.GetBlob("00ff")
	require.NoError(t, err)
	require.Equal(t, []byte("data"), data)

	_, err = store.GetBlob("beef")
	require.Error(t, err)
	require.Error(t, store.PutBlob("../evil", []byte("x")))
	_, err = store.GetBlob("UPPER")
	require.Error(t, err)

	// A second store on the same directory sees everything: state and
	// blobs persist on disk.
	reopened, err := fsstore.New(dir)
	require.NoError(t, err)

	loaded, err := reopened.LoadState()
	require.NoError(t, err)
	require.Equal(t, map[string]client.Head{"u2": {Rev: 1, Hash: "aaaa"}}, loaded.Heads)

	data, err = reopened.GetBlob("00ff")
	require.NoError(t, err)
	require.Equal(t, []byte("data"), data)

	// Blobs use the server-mirroring content-addressed layout.
	_, err = filepath.Glob(filepath.Join(dir, "docs", "00", "00ff"))
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(dir, "docs", "00", "00ff"))
	require.FileExists(t, filepath.Join(dir, "state.json"))
}
