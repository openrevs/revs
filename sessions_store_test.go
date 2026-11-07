package dmrs

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSessionStore(t *testing.T) {
	db, err := GetDB(t.TempDir())
	require.NoError(t, err)

	store := &sessionStore{db: db.DB}

	// Unknown token.
	_, found, err := store.Find("nope")
	require.NoError(t, err)
	require.False(t, found)

	// Commit and find.
	require.NoError(t, store.Commit("tok", []byte("payload"), time.Now().Add(time.Hour)))
	data, found, err := store.Find("tok")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, []byte("payload"), data)

	// Replacing an existing token updates it.
	require.NoError(t, store.Commit("tok", []byte("payload2"), time.Now().Add(time.Hour)))
	data, _, err = store.Find("tok")
	require.NoError(t, err)
	require.Equal(t, []byte("payload2"), data)

	// Expired tokens are not found.
	require.NoError(t, store.Commit("dead", []byte("x"), time.Now().Add(-time.Minute)))
	_, found, err = store.Find("dead")
	require.NoError(t, err)
	require.False(t, found)

	// Delete.
	require.NoError(t, store.Delete("tok"))
	_, found, err = store.Find("tok")
	require.NoError(t, err)
	require.False(t, found)
}
