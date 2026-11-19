package collection

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func ptrTo[T any](v T) *T { return &v }

// A TV series is one item whose viewings may name a season or episode;
// it is exported as a tv item line, as in the ratingset spec's example.
func TestTVSeries(t *testing.T) {
	ctx := context.Background()
	c := openTest(t)

	film, _, err := c.AddMovie(ctx, MovieInput{Title: "Fargo", Year: ptrTo[int64](1996), IMDB: ptrTo("tt0116282")})
	require.NoError(t, err)
	series, _, err := c.AddMovie(ctx, MovieInput{
		Type: TypeTV, Title: "Fargo", Year: ptrTo[int64](2014),
		IMDB: ptrTo("tt2802850"), TMDB: ptrTo("60622"), Wikidata: ptrTo("Q15931555"),
	})
	require.NoError(t, err)
	require.Equal(t, TypeMovie, film.Type)
	require.Equal(t, TypeTV, series.Type)

	add := func(id int64, date string, rating, season int64, review string) {
		t.Helper()
		in := ViewingInput{Date: &date, Rating: &rating, Review: review}
		if season >= 0 {
			in.Season = &season
		}
		_, err := c.AddViewing(ctx, id, in)
		require.NoError(t, err)
	}
	add(series.ID, "2015-03-01", 9, 1, "")
	add(series.ID, "2016-04-10", 6, 2, "")
	add(series.ID, "2019-06-02", 8, -1, "Uneven, but the highs are very high.")

	// Only a series's viewings name seasons and episodes.
	_, err = c.AddViewing(ctx, film.ID, ViewingInput{Season: ptrTo[int64](1)})
	require.ErrorIs(t, err, errNotTV)
	_, err = c.AddViewing(ctx, series.ID, ViewingInput{Episode: ptrTo[int64](1)})
	require.Error(t, err, "an episode needs a season")

	// The series's rating is its whole-series viewing's, not the newer
	// season's.
	add(series.ID, "2020-01-01", 2, 3, "")
	sum, err := c.MovieSummary(ctx, series.ID)
	require.NoError(t, err)
	require.Equal(t, int64(8), sum.LatestRating)
	require.Equal(t, int64(4), sum.ViewingCount)

	// Rating the series changes that viewing.
	v, added, err := c.SetRating(ctx, series.ID, 7)
	require.NoError(t, err)
	require.False(t, added)
	require.Nil(t, v.Season)
	require.Equal(t, "2019-06-02", *v.ViewedOn)
	_, _, err = c.SetRating(ctx, series.ID, 8)
	require.NoError(t, err)

	// Lists filter by type.
	all, err := c.Movies(ctx, "fargo", "")
	require.NoError(t, err)
	require.Len(t, all, 2)
	tv, err := c.Movies(ctx, "", TypeTV)
	require.NoError(t, err)
	require.Len(t, tv, 1)
	require.Equal(t, series.ID, tv[0].ID)
	counts, err := c.Counts(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), counts.Movies)
	require.Equal(t, int64(1), counts.Series)

	// A series with season viewings can't become a movie.
	in := MovieInput{Title: "Fargo", Year: ptrTo[int64](2014)}
	require.ErrorIs(t, c.UpdateMovie(ctx, series.ID, in), ErrHasSeasons)

	vs, err := c.Viewings(ctx, series.ID)
	require.NoError(t, err)
	require.Equal(t, int64(3), *vs[0].Season)
	require.NoError(t, c.DeleteViewing(ctx, vs[0].ID))

	content, err := c.buildContent(ctx, "me")
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	require.Len(t, lines, 3)
	require.Equal(t, `{"entries":[{"date":"2015-03-01","rating":9,"season":1},{"date":"2016-04-10","rating":6,"season":2},{"date":"2019-06-02","rating":8,"review":"Uneven, but the highs are very high."}],"refs":{"imdb":"tt2802850","tmdb":"60622","wikidata":"Q15931555"},"title":"Fargo","type":"tv","year":2014}`, lines[2])

	// Importing that content elsewhere gives the same collection, and
	// importing it again adds nothing.
	other := openTest(t)
	stats, err := other.mergeFromContent(ctx, content)
	require.NoError(t, err)
	require.Equal(t, ImportStats{MoviesAdded: 2, ViewingsAdded: 3}, stats)
	stats, err = other.mergeFromContent(ctx, content)
	require.NoError(t, err)
	require.Equal(t, ImportStats{MoviesMatched: 2, ViewingsKept: 3}, stats)
	again, err := other.buildContent(ctx, "me")
	require.NoError(t, err)
	require.Equal(t, string(content), string(again))
}

func (c *Collection) mergeFromContent(ctx context.Context, content []byte) (ImportStats, error) {
	res, err := c.Import(ctx, content)
	return res.ImportStats, err
}

// Items of different types never match, even with the same title and
// year, and season viewings are told apart by season and episode.
func TestImportTV(t *testing.T) {
	ctx := context.Background()
	c := openTest(t)
	doc := strings.Join([]string{
		`{"id":"someone","max_rating":10,"min_rating":0}`,
		`{"entries":[{"rating":8}],"title":"The Office","type":"movie","year":2005}`,
		`{"entries":[{"rating":8,"season":1},{"episode":2,"rating":8,"season":1},{"rating":8}],"title":"The Office","type":"TV","year":2005}`,
		`{"entries":[{"season":1}],"title":"Not A Series","type":"movie"}`, // invalid
		`{"entries":[{"rating":4}],"title":"A Book","type":"book"}`,
	}, "\n")
	res, err := c.Import(ctx, []byte(doc))
	require.NoError(t, err)
	require.Equal(t, ImportStats{MoviesAdded: 2, ViewingsAdded: 4, SkippedNonFilm: 1, SkippedInvalid: 1}, res.ImportStats)

	tv, err := c.Movies(ctx, "", TypeTV)
	require.NoError(t, err)
	require.Len(t, tv, 1)
	require.Equal(t, int64(3), tv[0].ViewingCount)
	require.Equal(t, int64(8), tv[0].LatestRating)
	vs, err := c.Viewings(ctx, tv[0].ID)
	require.NoError(t, err)
	var parts []string
	for _, v := range vs {
		parts = append(parts, PartName(v.Season, v.Episode))
	}
	require.ElementsMatch(t, []string{"Season 1", "S1 E2", ""}, parts)
}

func TestViewingFieldsSeason(t *testing.T) {
	for _, tc := range []struct {
		typ, season, episode string
		ok                   bool
	}{
		{TypeTV, "", "", true},
		{TypeTV, "1", "", true},
		{TypeTV, "0", "3", true},
		{TypeTV, " 2 ", "10", true},
		{TypeTV, "", "3", false},
		{TypeTV, "-1", "", false},
		{TypeTV, "1", "0", false},
		{TypeTV, "one", "", false},
		{TypeMovie, "1", "", false},
		{"", "", "1", false},
		{TypeMovie, "", "", true},
	} {
		f := ViewingFields{Type: tc.typ, Season: tc.season, Episode: tc.episode, Rating: "none"}
		_, err := f.Parse()
		require.Equal(t, tc.ok, err == nil, "%+v: %v", tc, err)
	}

	require.Equal(t, "", PartName((*int64)(nil), (*int64)(nil)))
	require.Equal(t, "Season 2", PartName(ptrTo[int64](2), (*int64)(nil)))
	require.Equal(t, "S2 E5", PartName(ptrTo(2), ptrTo(5)))
	require.Equal(t, "Specials", PartName(ptrTo[int64](0), (*int64)(nil)))
	require.Equal(t, "Special 4", PartName(ptrTo[int64](0), ptrTo[int64](4)))
}
