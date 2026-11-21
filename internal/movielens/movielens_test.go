package movielens

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openrevs/revs/ratingset"
	jsonlines "github.com/openrevs/sjsonl-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Synthetic files in the MovieLens format; the real data can't be
// checked in.
const (
	moviesCSV = `movieId,title,genres
1,Toy Story (1995),Adventure|Animation|Children|Comedy|Fantasy
2,"City of Lost Children, The (Cité des enfants perdus, La) (1995)",Drama|Fantasy
3,"Matrix, The (1999)",Action|Sci-Fi
4,Untitled Project,(no genres listed)
5,Toy Story (1995),Adventure|Animation|Children|Comedy|Fantasy
`
	linksCSV = `movieId,imdbId,tmdbId
1,0114709,862
2,0112682,902
3,0133093,
4,,
5,0114709,862
99,0000001,1
`
	ratingsCSV = `userId,movieId,rating,timestamp
1,3,4.5,944249077
1,1,0.5,943228858
1,2,5.0,944249077
1,42,3.0,943228000
2,1,3.5,1000000000
`
)

func catalog(t *testing.T) Catalog {
	t.Helper()
	c, err := LoadCatalog(strings.NewReader(moviesCSV), strings.NewReader(linksCSV))
	require.NoError(t, err)
	return c
}

func users(t *testing.T, csv string) []User {
	t.Helper()
	var us []User
	require.NoError(t, ReadUsers(strings.NewReader(csv), func(u User) error {
		us = append(us, u)
		return nil
	}))
	return us
}

func TestParseTitle(t *testing.T) {
	tests := []struct {
		in    string
		title string
		year  int
	}{
		{"Toy Story (1995)", "Toy Story", 1995},
		{"Matrix, The (1999)", "The Matrix", 1999},
		{"City of Lost Children, The (Cité des enfants perdus, La) (1995)", "The City of Lost Children", 1995},
		{"Avventura, L' (Adventure, The) (1960)", "L'Avventura", 1960},
		{"Babylon 5 (1994-1998)", "Babylon 5", 1994},
		{"Fawlty Towers (1975-1979)", "Fawlty Towers", 1975},
		{"Stranger Things (2016-)", "Stranger Things", 2016},
		{"Hamlet (2000) ", "Hamlet", 2000},
		{"1984 (1956)", "1984", 1956},
		{"Untitled Project", "Untitled Project", 0},
		{"Seven (a.k.a. Se7en) (1995)", "Seven", 1995},
	}
	for _, tt := range tests {
		title, year := ParseTitle(tt.in)
		assert.Equal(t, tt.title, title, tt.in)
		assert.Equal(t, tt.year, year, tt.in)
	}
}

func TestLoadCatalog(t *testing.T) {
	c := catalog(t)
	require.Len(t, c, 5)

	y := 1995
	assert.Equal(t, ratingset.Item{
		Type: ratingset.ItemTypeMovie, Title: "Toy Story", Year: &y,
		Refs: map[string]string{"imdb": "tt0114709", "tmdb": "862"},
	}, c[1])
	assert.Equal(t, map[string]string{"imdb": "tt0133093"}, c[3].Refs)
	assert.Nil(t, c[4].Refs)
	assert.Nil(t, c[4].Year)

	// links.csv is optional.
	c, err := LoadCatalog(strings.NewReader(moviesCSV), nil)
	require.NoError(t, err)
	assert.Nil(t, c[1].Refs)

	_, err = LoadCatalog(strings.NewReader("id,title\n1,x\n"), nil)
	assert.ErrorContains(t, err, "header")
	_, err = LoadCatalog(strings.NewReader("movieId,title,genres\nx,Heat (1995),Action\n"), nil)
	assert.ErrorContains(t, err, "movies.csv:2")
}

func TestReadUsers(t *testing.T) {
	us := users(t, ratingsCSV)
	require.Len(t, us, 2)
	assert.Equal(t, 1, us[0].ID)
	assert.Equal(t, []Rating{
		{Movie: 3, Rating: 9, Time: 944249077},
		{Movie: 1, Rating: 1, Time: 943228858},
		{Movie: 2, Rating: 10, Time: 944249077},
		{Movie: 42, Rating: 6, Time: 943228000},
	}, us[0].Ratings)
	assert.Equal(t, User{ID: 2, Ratings: []Rating{{Movie: 1, Rating: 7, Time: 1000000000}}}, us[1])

	// CRLF line ends and a trailing blank line are fine.
	us = users(t, strings.ReplaceAll(ratingsCSV, "\n", "\r\n")+"\r\n")
	assert.Len(t, us, 2)

	// fn's error stops the read.
	stop := errors.New("stop")
	calls := 0
	err := ReadUsers(strings.NewReader(ratingsCSV), func(User) error { calls++; return stop })
	assert.ErrorIs(t, err, stop)
	assert.Equal(t, 1, calls)

	for _, tt := range []struct{ csv, want string }{
		{"", "empty"},
		{"a,b,c,d\n", "header"},
		{"userId,movieId,rating,timestamp\n1,2,3.0\n", "ratings.csv:2: want 4 fields"},
		{"userId,movieId,rating,timestamp\n1,2,3.0,4,5\n", "want 4 fields"},
		{"userId,movieId,rating,timestamp\n1,2,3.3,4\n", "half star"},
		{"userId,movieId,rating,timestamp\n1,2,0.0,4\n", "half star"},
		{"userId,movieId,rating,timestamp\n1,2,5.5,4\n", "half star"},
		{"userId,movieId,rating,timestamp\n1,2,3.0,x\n", "timestamp"},
		{"userId,movieId,rating,timestamp\n1,2,3.0,4\n2,2,3.0,4\n1,3,3.0,4\n", "ratings.csv:4: user 1's ratings are not together"},
	} {
		err := ReadUsers(strings.NewReader(tt.csv), func(User) error { return nil })
		assert.ErrorContains(t, err, tt.want, tt.csv)
	}
}

func TestKey(t *testing.T) {
	assert.Equal(t, Key("a", 1), Key("a", 1))
	assert.NotEqual(t, Key("a", 1), Key("a", 2))
	assert.NotEqual(t, Key("a", 1), Key("b", 1))
	assert.NotEqual(t, Key("a", 12), Key("a1", 2))
	assert.Equal(t, AuthorID("a", 1), AuthorID("a", 1))
	assert.NotEqual(t, AuthorID("a", 1), AuthorID("a", 2))
}

func TestContent(t *testing.T) {
	c := catalog(t)
	u := users(t, ratingsCSV)[0]
	id := AuthorID("test", u.ID)

	content, n, skipped, err := c.Content(id, u, 0)
	require.NoError(t, err)
	assert.Equal(t, 3, n)
	assert.Equal(t, 1, skipped) // movie 42 isn't in the catalog
	assert.Equal(t, `{"id":"`+id+`","max_rating":10,"min_rating":1}
{"entries":[{"date":"1999-11-22","rating":1}],"refs":{"imdb":"tt0114709","tmdb":"862"},"title":"Toy Story","type":"movie","year":1995}
{"entries":[{"date":"1999-12-03","rating":10}],"refs":{"imdb":"tt0112682","tmdb":"902"},"title":"The City of Lost Children","type":"movie","year":1995}
{"entries":[{"date":"1999-12-03","rating":9}],"refs":{"imdb":"tt0133093"},"title":"The Matrix","type":"movie","year":1999}
`, string(content))

	// Cut off before the last two ratings: an earlier revision.
	early, n, skipped, err := c.Content(id, u, 943228858)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, 1, skipped)
	assert.True(t, bytes.HasPrefix(content, early), "a later revision extends the earlier one")

	// Before any rating: just the author line.
	empty, n, _, err := c.Content(id, u, 1)
	require.NoError(t, err)
	assert.Zero(t, n)
	assert.Equal(t, 1, bytes.Count(empty, []byte("\n")))

	// A document signs and verifies as the user's ratingset, and one
	// revision patches to the next.
	key := Key("test", u.ID)
	signed, _, err := jsonlines.Sign(content, key)
	require.NoError(t, err)
	author, _, err := ratingset.ParseRatingset(signed)
	require.NoError(t, err)
	assert.Equal(t, id, author.ID)
	assert.Equal(t, MinRating, author.MinRating)
	assert.Equal(t, MaxRating, author.MaxRating)

	signedEarly, _, err := jsonlines.Sign(early, key)
	require.NoError(t, err)
	_, err = jsonlines.Diff(signedEarly, signed)
	require.NoError(t, err)
}

func TestContentDropsDuplicateLines(t *testing.T) {
	// Movies 1 and 5 are the same film; equal ratings on the same day
	// make equal lines, which can't both be signed.
	c := catalog(t)
	u := User{ID: 7, Ratings: []Rating{
		{Movie: 1, Rating: 8, Time: 1000000000},
		{Movie: 5, Rating: 8, Time: 1000000001},
	}}
	content, n, _, err := c.Content(AuthorID("test", 7), u, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	_, _, err = jsonlines.Sign(content, Key("test", 7))
	require.NoError(t, err)
}

// TestDataset converts a real MovieLens dataset, if REVS_MOVIELENS names
// its directory (holding movies.csv, links.csv and ratings.csv). Every
// user's full document must build, sign and verify; with -short only
// the first thousand users are checked.
func TestDataset(t *testing.T) {
	dir := os.Getenv("REVS_MOVIELENS")
	if dir == "" {
		t.Skip("set REVS_MOVIELENS to a MovieLens dataset directory to run")
	}
	open := func(name string) *os.File {
		f, err := os.Open(filepath.Join(dir, name))
		require.NoError(t, err)
		t.Cleanup(func() { f.Close() })
		return f
	}

	start := time.Now()
	c, err := LoadCatalog(open("movies.csv"), open("links.csv"))
	require.NoError(t, err)
	t.Logf("catalog: %d movies in %v", len(c), time.Since(start))

	errDone := errors.New("done")
	var nUsers, nRatings, nSkipped, bytesOut int
	start = time.Now()
	err = ReadUsers(open("ratings.csv"), func(u User) error {
		if testing.Short() && nUsers == 1000 {
			return errDone
		}
		id := AuthorID("test", u.ID)
		content, n, skipped, err := c.Content(id, u, 0)
		if err != nil {
			return err
		}
		signed, _, err := jsonlines.Sign(content, Key("test", u.ID))
		if err != nil {
			return err
		}
		author, _, err := ratingset.ParseRatingset(signed)
		if err != nil {
			return err
		}
		if author.ID != id {
			return errors.New("author id mismatch")
		}
		nUsers++
		nRatings += n
		nSkipped += skipped
		bytesOut += len(signed)
		return nil
	})
	if !errors.Is(err, errDone) {
		require.NoError(t, err)
	}
	t.Logf("%d users, %d ratings (%d skipped), %d MB signed, in %v",
		nUsers, nRatings, nSkipped, bytesOut>>20, time.Since(start))
}
