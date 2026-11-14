package collection_test

import (
	"context"
	"os"
	"testing"

	"github.com/openrevs/revs/collection"
	"github.com/openrevs/revs/revstest"
	"github.com/stretchr/testify/require"
)

func ptr[T any](v T) *T { return &v }

// Everything pulled from several servers lives in the one database: a
// document both servers publish is stored once, its reviews are listed
// once, and forgetting one server leaves the other's data intact.
func TestServersShareDatabase(t *testing.T) {
	ctx := context.Background()

	network := revstest.NewNetwork(t)
	srvs := []*revstest.Server{network.NewServer(), network.NewServer()}
	var urls []string
	for _, s := range srvs {
		urls = append(urls, s.URL)
	}

	// An author publishes a review to both servers.
	author := network.NewAuthor()
	m, _, err := author.AddMovie(ctx, collection.MovieInput{Title: "Alien", Year: ptr[int64](1979)})
	require.NoError(t, err)
	_, err = author.AddViewing(ctx, m.ID, collection.ViewingInput{Rating: ptr[int64](5), Review: "Perfect."})
	require.NoError(t, err)
	for _, u := range urls {
		s, err := author.AddServer(ctx, u)
		require.NoError(t, err)
		_, err = author.Push(ctx, s.ID)
		require.NoError(t, err)
	}
	for _, srv := range srvs {
		srv.Cut()
	}

	// A reader pulls from both.
	reader := network.NewAuthor()
	dir := reader.DataDir()
	var ids []int64
	for _, u := range urls {
		s, err := reader.AddServer(ctx, u)
		require.NoError(t, err)
		_, err = reader.Pull(ctx, s.ID, false)
		require.NoError(t, err)
		ids = append(ids, s.ID)
	}

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		require.False(t, e.IsDir(), "unexpected directory %s", e.Name())
	}

	count := func(query string, args ...any) int {
		var n int
		require.NoError(t, collection.DB(reader).QueryRow(query, args...).Scan(&n))
		return n
	}
	require.Equal(t, 1, count(`SELECT count(*) FROM corpus_docs`), "both servers' copy is stored once")
	require.Equal(t, 2, count(`SELECT count(*) FROM corpus_feed_docs`))

	movie := func() []collection.OtherReview {
		rm, _, err := reader.AddMovie(ctx, collection.MovieInput{Title: "Alien", Year: ptr[int64](1979)})
		require.NoError(t, err)
		reviews, err := reader.OthersReviews(ctx, rm)
		require.NoError(t, err)
		return reviews
	}
	reviews := movie()
	require.Len(t, reviews, 1, "seen on both servers, listed once")
	require.Equal(t, "Perfect.", reviews[0].Comment)

	for _, id := range ids {
		st, err := reader.ServerStatus(ctx, id)
		require.NoError(t, err)
		require.Equal(t, 1, st.Epoch)
		require.Len(t, st.Users, 1)
	}

	// Reset the first server: the second still has everything.
	require.NoError(t, reader.ResetServer(ctx, ids[0]))
	st, err := reader.ServerStatus(ctx, ids[0])
	require.NoError(t, err)
	require.Zero(t, st.Epoch)
	require.Empty(t, st.Users)
	require.Equal(t, 1, count(`SELECT count(*) FROM corpus_docs`))
	_, titles, err := reader.ServerUser(ctx, ids[1], author.UserID(ctx))
	require.NoError(t, err)
	require.Len(t, titles, 1)
	require.Len(t, movie(), 1)

	// Pulling again after the reset works from scratch.
	_, err = reader.Pull(ctx, ids[0], false)
	require.NoError(t, err)
	require.Equal(t, 2, count(`SELECT count(*) FROM corpus_feed_docs`))

	// Removing both servers leaves no pulled data behind.
	for _, id := range ids {
		require.NoError(t, reader.RemoveServer(ctx, id))
	}
	for _, table := range []string{"corpus_docs", "corpus_lines", "corpus_feeds", "corpus_heads", "corpus_feed_docs", "idx_users", "idx_titles", "idx_reviews"} {
		require.Zero(t, count(`SELECT count(*) FROM `+table), table)
	}
	require.Empty(t, movie())
}
