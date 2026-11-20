package collection

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSetRating(t *testing.T) {
	ctx := context.Background()
	c := openTest(t)
	m, _, err := c.AddMovie(ctx, MovieInput{Title: "Alien"})
	require.NoError(t, err)

	// Unrated: adds an undated viewing.
	v, added, err := c.SetRating(ctx, m.ID, 8)
	require.NoError(t, err)
	require.True(t, added)
	require.Nil(t, v.ViewedOn)

	// Rated: changes the latest viewing, keeping its date and review.
	older, newer := "2020-01-01", "2024-06-01"
	_, err = c.AddViewing(ctx, m.ID, ViewingInput{Date: &older, Review: "first"})
	require.NoError(t, err)
	_, err = c.AddViewing(ctx, m.ID, ViewingInput{Date: &newer, Review: "second"})
	require.NoError(t, err)
	require.NoError(t, c.UpdateViewing(ctx, v.ID, ViewingInput{Date: &older, Rating: v.Rating}))

	sum, err := c.MovieSummary(ctx, m.ID)
	require.NoError(t, err)
	require.Equal(t, int64(3), sum.ViewingCount)
	require.Equal(t, newer, sum.LastViewed)

	v, added, err = c.SetRating(ctx, m.ID, 3)
	require.NoError(t, err)
	require.False(t, added)
	vs, err := c.Viewings(ctx, m.ID)
	require.NoError(t, err)
	require.Len(t, vs, 3)
	require.Equal(t, v.ID, vs[0].ID)
	require.Equal(t, newer, *vs[0].ViewedOn)
	require.Equal(t, "second", vs[0].Review)
	require.Equal(t, int64(3), *vs[0].Rating)

	_, _, err = c.SetRating(ctx, m.ID, 11)
	require.Error(t, err)
	_, _, err = c.SetRating(ctx, m.ID, 0)
	require.Error(t, err, "½ star is the least")
	_, _, err = c.SetRating(ctx, 999, 4)
	require.ErrorIs(t, err, ErrNotFound)
}
