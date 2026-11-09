package fsstore_test

import (
	"path/filepath"
	"testing"

	"github.com/openrevs/revs/client"
	"github.com/openrevs/revs/client/fsstore"
	"github.com/stretchr/testify/require"
)

func TestStoreContract(t *testing.T) {
	dir := t.TempDir()
	store, err := fsstore.New(dir)
	require.NoError(t, err)

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
	saved.Push = nil
	require.NoError(t, store.SaveState(saved))

	loaded, err = store.LoadState()
	require.NoError(t, err)
	require.Equal(t, saved.Heads, loaded.Heads)
	require.Empty(t, loaded.Push)

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

	loaded, err = reopened.LoadState()
	require.NoError(t, err)
	require.Equal(t, saved.Heads, loaded.Heads)

	data, err = reopened.GetBlob("00ff")
	require.NoError(t, err)
	require.Equal(t, []byte("data"), data)

	// Blobs use the server-mirroring content-addressed layout.
	_, err = filepath.Glob(filepath.Join(dir, "docs", "00", "00ff"))
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(dir, "docs", "00", "00ff"))
	require.FileExists(t, filepath.Join(dir, "state.json"))
}
