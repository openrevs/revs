package revstest_test

import (
	"context"
	"slices"
	"testing"

	"github.com/openrevs/revs/client"
	"github.com/openrevs/revs/collection"
	"github.com/openrevs/revs/revstest"
	"github.com/stretchr/testify/require"
)

// published returns the titles the server publishes for uid, after
// cutting an epoch.
func published(t *testing.T, network *revstest.Network, s *revstest.Server, uid string) []string {
	t.Helper()
	ctx := context.Background()
	s.Cut()
	reader := network.NewCollection()
	srv, err := reader.AddServer(ctx, s.URL)
	require.NoError(t, err)
	_, err = reader.Pull(ctx, srv.ID, false)
	require.NoError(t, err)
	_, titles, err := reader.ServerUser(ctx, srv.ID, uid)
	require.NoError(t, err)
	var names []string
	for _, title := range titles {
		names = append(names, title.Name)
	}
	slices.Sort(names)
	return names
}

func titles(t *testing.T, c *collection.Collection) []string {
	t.Helper()
	movies, err := c.Movies(context.Background(), "", "")
	require.NoError(t, err)
	var names []string
	for _, m := range movies {
		names = append(names, m.Title)
	}
	slices.Sort(names)
	return names
}

// adopt returns a fresh collection holding title and the identity of
// author, with s added as a server: author's data directory wiped and
// the key imported again, or a second device.
func adopt(t *testing.T, network *revstest.Network, author *collection.Collection, s *revstest.Server, title string) (*collection.Collection, int64) {
	t.Helper()
	ctx := context.Background()
	c := network.NewCollection()
	_, _, err := c.AddMovie(ctx, collection.MovieInput{Title: title})
	require.NoError(t, err)
	require.NoError(t, c.ImportKey(ctx, author.PrivateKeyText(ctx)))
	srv, err := c.AddServer(ctx, s.URL)
	require.NoError(t, err)
	return c, srv.ID
}

func pushedAuthor(t *testing.T, network *revstest.Network, s *revstest.Server, title string) (*collection.Collection, int64) {
	t.Helper()
	ctx := context.Background()
	author := network.NewAuthor()
	_, _, err := author.AddMovie(ctx, collection.MovieInput{Title: title})
	require.NoError(t, err)
	srv, err := author.AddServer(ctx, s.URL)
	require.NoError(t, err)
	_, err = author.Push(ctx, srv.ID)
	require.NoError(t, err)
	return author, srv.ID
}

// Pushing from a collection that adopted an identity merges what the
// server holds for it first, rather than replacing it (issue #6).
func TestAdoptedIdentityPushMerges(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	for _, pullFirst := range []bool{false, true} {
		network := revstest.NewNetwork(t)
		s := network.NewServer()
		old, _ := pushedAuthor(t, network, s, "Alien")
		s.Cut()

		fresh, id := adopt(t, network, old, s, "Brazil")
		if pullFirst {
			_, err := fresh.Pull(ctx, id, false)
			require.NoError(t, err)
		}
		res, err := fresh.Push(ctx, id)
		require.NoError(t, err, "pullFirst=%v", pullFirst)
		require.True(t, res.Pushed)
		require.Equal(t, 2, res.Rev)
		require.NotNil(t, res.Merged)
		require.Equal(t, 1, res.Merged.Rev)
		require.Equal(t, 1, res.Merged.MoviesAdded)
		require.Contains(t, collection.PushMessage(res), "Merged your collection from the server (revision 1)")

		require.Equal(t, []string{"Alien", "Brazil"}, titles(t, fresh))
		require.Equal(t, []string{"Alien", "Brazil"}, published(t, network, s, fresh.UserID(ctx)))

		// Merged once: deleting the merged movie and pushing again
		// doesn't bring it back.
		movies, err := fresh.Movies(ctx, "Alien", "")
		require.NoError(t, err)
		require.NoError(t, fresh.DeleteMovie(ctx, movies[0].ID))
		_, err = fresh.Pull(ctx, id, false)
		require.NoError(t, err)
		res, err = fresh.Push(ctx, id)
		require.NoError(t, err)
		require.Nil(t, res.Merged)
		require.Equal(t, []string{"Brazil"}, published(t, network, s, fresh.UserID(ctx)))
	}
}

// A copy the server holds but hasn't published can't be merged, so the
// push stays stale until it is published, unless forced.
func TestPushUnpublishedCopy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	network := revstest.NewNetwork(t)
	s := network.NewServer()
	old, _ := pushedAuthor(t, network, s, "Alien")

	fresh, id := adopt(t, network, old, s, "Brazil")
	for range 2 {
		_, err := fresh.Push(ctx, id)
		var stale *client.StaleError
		require.ErrorAs(t, err, &stale, "pushing again must not replace the unmerged copy")
		require.Equal(t, 1, stale.LatestRev)
	}

	s.Cut()
	res, err := fresh.Push(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, res.Merged)
	require.Equal(t, []string{"Alien", "Brazil"}, published(t, network, s, fresh.UserID(ctx)))
}

// ForcePush replaces the server's copy, and a later push doesn't merge
// the replaced copy back in once it is published.
func TestForcePush(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	network := revstest.NewNetwork(t)
	s := network.NewServer()
	old, _ := pushedAuthor(t, network, s, "Alien")

	fresh, id := adopt(t, network, old, s, "Brazil")
	_, err := fresh.Push(ctx, id)
	var stale *client.StaleError
	require.ErrorAs(t, err, &stale)

	res, err := fresh.ForcePush(ctx, id)
	require.NoError(t, err)
	require.Nil(t, res.Merged)
	require.Equal(t, 2, res.Rev)
	require.Equal(t, []string{"Brazil"}, published(t, network, s, fresh.UserID(ctx)))

	_, _, err = fresh.AddMovie(ctx, collection.MovieInput{Title: "Casablanca"})
	require.NoError(t, err)
	_, err = fresh.Pull(ctx, id, false)
	require.NoError(t, err)
	res, err = fresh.Push(ctx, id)
	require.NoError(t, err)
	require.Nil(t, res.Merged)
	require.Equal(t, []string{"Brazil", "Casablanca"}, published(t, network, s, fresh.UserID(ctx)))
}

// The author's own pushes, pulled back, are not merged: the local
// collection already supersedes them, including deletions.
func TestPushDoesNotMergeOwnVersions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	network := revstest.NewNetwork(t)
	s := network.NewServer()
	author, id := pushedAuthor(t, network, s, "Alien")
	s.Cut()
	_, err := author.Pull(ctx, id, false)
	require.NoError(t, err)

	movies, err := author.Movies(ctx, "", "")
	require.NoError(t, err)
	require.NoError(t, author.DeleteMovie(ctx, movies[0].ID))
	_, _, err = author.AddMovie(ctx, collection.MovieInput{Title: "Brazil"})
	require.NoError(t, err)

	res, err := author.Push(ctx, id)
	require.NoError(t, err)
	require.Nil(t, res.Merged)
	require.Equal(t, []string{"Brazil"}, published(t, network, s, author.UserID(ctx)))
}
