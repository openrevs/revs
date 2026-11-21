package movielens

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openrevs/revs/ratingset"
	"github.com/openrevs/revs/revstest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseInterval(t *testing.T) {
	for in, want := range map[string]Interval{
		"1y": {Years: 1}, "6m": {Months: 6}, "2w": {Days: 14}, "30d": {Days: 30},
	} {
		got, err := ParseInterval(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	for _, in := range []string{"", "y", "0y", "-1d", "3h", "1.5y"} {
		_, err := ParseInterval(in)
		assert.Error(t, err, in)
	}
}

// replayDataset is the synthetic catalog with ratings spread over
// 2000-2002:
//
//	user 1: movies 1-4 in 2000, movie 5 in mid 2001
//	user 2: movie 1 in early 2001
//	user 3: movie 5 in early 2002, and a movie not in the catalog in 2000
func replayDataset(t *testing.T) *Dataset {
	t.Helper()
	day := func(y, m, d int) int64 { return time.Date(y, time.Month(m), d, 12, 0, 0, 0, time.UTC).Unix() }
	ratings := "userId,movieId,rating,timestamp\n"
	for _, r := range []struct {
		user, movie int
		stars       string
		at          int64
	}{
		{1, 1, "4.0", day(2000, 2, 1)},
		{1, 2, "3.5", day(2000, 3, 1)},
		{1, 3, "5.0", day(2000, 4, 1)},
		{1, 4, "2.0", day(2000, 5, 1)},
		{1, 5, "4.5", day(2001, 6, 1)},
		{2, 1, "3.0", day(2001, 3, 1)},
		{3, 5, "1.0", day(2002, 1, 15)},
		{3, 42, "2.5", day(2000, 1, 10)},
	} {
		ratings += fmt.Sprintf("%d,%d,%s,%d\n", r.user, r.movie, r.stars, r.at)
	}

	dir := t.TempDir()
	for name, data := range map[string]string{"movies.csv": moviesCSV, "links.csv": linksCSV, "ratings.csv": ratings} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644))
	}
	ds, err := Open(dir)
	require.NoError(t, err)
	return ds
}

func TestReplay(t *testing.T) {
	ds := replayDataset(t)
	net := revstest.NewNetwork(t)
	srv := net.NewServer()

	var reported []StepStats
	steps, err := Replay(context.Background(), ds, ReplayOptions{
		Salt:       "test",
		From:       time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC),
		To:         time.Date(2003, 1, 1, 0, 0, 0, 0, time.UTC),
		Every:      Interval{Years: 1},
		URL:        srv.URL,
		HTTPClient: net.HTTPClient(),
		Cut:        func(context.Context) error { srv.Cut(); return nil },
		Dir:        t.TempDir(),
		Workers:    2,
		Step:       func(s StepStats) { reported = append(reported, s) },
	})
	require.NoError(t, err)
	assert.Equal(t, steps, reported)
	require.Len(t, steps, 3)

	type counts struct{ Authors, Ratings, Pushed, New, Patches, Full, Epoch, Changed int }
	got := func(s StepStats) counts {
		return counts{s.Authors, s.Ratings, s.Pushed, s.New, s.Patches, s.Full, s.Epoch, s.Changed}
	}
	// 2000: user 1 arrives with four ratings.
	assert.Equal(t, counts{Authors: 1, Ratings: 4, Pushed: 1, New: 1, Full: 1, Epoch: 1, Changed: 1}, got(steps[0]))
	// 2001: user 1 adds one, as a patch; user 2 arrives.
	assert.Equal(t, counts{Authors: 2, Ratings: 6, Pushed: 2, New: 1, Patches: 1, Full: 1, Epoch: 2, Changed: 2}, got(steps[1]))
	// 2002: only user 3, whose rating of an unknown movie never counts.
	assert.Equal(t, counts{Authors: 3, Ratings: 7, Pushed: 1, New: 1, Full: 1, Epoch: 3, Changed: 1}, got(steps[2]))

	assert.Equal(t, time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC), steps[0].Until)
	assert.True(t, steps[0].Bootstrapped)
	assert.False(t, steps[1].Bootstrapped)
	assert.Less(t, steps[1].UploadBytes, steps[1].DocBytes, "the patch is smaller than the document")
	assert.Equal(t, steps[0].UploadBytes, steps[0].DocBytes, "a first push is the document")
	for _, s := range steps {
		assert.Positive(t, s.SyncBytes)
		assert.Positive(t, s.SyncRequests)
		assert.Positive(t, s.Corpus)
	}

	// The server ends up with every author's full history, at one
	// revision per step they pushed in.
	c := net.NewClient(srv)
	_, err = c.Sync(context.Background())
	require.NoError(t, err)
	for _, tt := range []struct{ user, rev, items int }{{1, 2, 5}, {2, 1, 1}, {3, 1, 1}} {
		d, err := c.Document(AuthorID("test", tt.user))
		require.NoError(t, err, "user %d", tt.user)
		assert.Equal(t, tt.rev, d.Rev, "user %d", tt.user)
		_, _, err = ratingset.ParseRatingset(d.Data)
		require.NoError(t, err)
		assert.Equal(t, tt.items+1, countLines(d.Data)-1, "user %d: author line and items", tt.user)
	}
}

func TestReplayDefaults(t *testing.T) {
	// Without From and To, the replay spans the ratings: the last step
	// takes in the last rating.
	ds := replayDataset(t)
	net := revstest.NewNetwork(t)
	srv := net.NewServer()
	steps, err := Replay(context.Background(), ds, ReplayOptions{
		URL:        srv.URL,
		HTTPClient: net.HTTPClient(),
		Cut:        func(context.Context) error { srv.Cut(); return nil },
		Dir:        t.TempDir(),
	})
	require.NoError(t, err)
	last := steps[len(steps)-1]
	assert.Equal(t, 3, last.Authors)
	assert.Equal(t, 7, last.Ratings)
	assert.Equal(t, time.Date(2002, 1, 15, 12, 0, 1, 0, time.UTC), last.Until)
	assert.Len(t, steps, 3) // 2001-01-10, 2002-01-10, then the end

	// A sample of nobody is an error.
	_, err = Replay(context.Background(), ds, ReplayOptions{
		Selection: Selection{Sample: 1e-9}, URL: srv.URL, Dir: t.TempDir(),
	})
	assert.ErrorContains(t, err, "no users")
	_, err = Replay(context.Background(), ds, ReplayOptions{URL: srv.URL})
	assert.ErrorContains(t, err, "directory")
}

func countLines(b []byte) int {
	n := 0
	for _, c := range b {
		if c == '\n' {
			n++
		}
	}
	if len(b) > 0 && b[len(b)-1] != '\n' {
		n++
	}
	return n
}
