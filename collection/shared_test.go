package collection

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// Two processes sharing a data directory (say, revs ui and revs tui)
// must not give different content the same revision, even though each
// caches the version history.
func TestSharedDataDirRevisions(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	open := func() *Collection {
		c, err := Open(Options{DataDir: dir})
		require.NoError(t, err)
		t.Cleanup(func() { c.Close() })
		return c
	}
	a, b := open(), open()
	require.NoError(t, a.GenerateKey(ctx))

	_, rev, err := b.Export(ctx) // b caches the history at revision 1
	require.NoError(t, err)
	require.Equal(t, 1, rev)

	_, _, err = a.AddMovie(ctx, MovieInput{Title: "Alien"})
	require.NoError(t, err)
	_, rev, err = a.Export(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, rev)

	_, _, err = b.AddMovie(ctx, MovieInput{Title: "Aliens"})
	require.NoError(t, err)
	_, rev, err = b.Export(ctx)
	require.NoError(t, err)
	require.Equal(t, 3, rev, "b must see a's revision 2")
}
