package collection

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

func TestParseStars(t *testing.T) {
	for in, want := range map[string]int64{"0": 0, "0.5": 1, ".5": 1, "3": 6, "3.5": 7, " 3½ ": 7, "½": 1, "5": 10, "5.0": 10} {
		got, err := ParseStars(in)
		require.NoError(t, err, in)
		require.Equal(t, want, got, in)
		if in == "3.5" || in == "3" {
			require.Equal(t, in, FormatStars(got))
		}
	}
	for _, in := range []string{"", "-1", "5.5", "6", "3.25", "abc", "NaN", "Inf", "3½½"} {
		_, err := ParseStars(in)
		require.Error(t, err, in)
	}
}

func TestStars(t *testing.T) {
	require.Equal(t, "☆☆☆☆☆", Stars(int64(0)))
	require.Equal(t, "½☆☆☆☆", Stars(int64(1)))
	require.Equal(t, "★★★½☆", Stars(int64(7)))
	require.Equal(t, "★★★★★", Stars(int64(10)))
	require.Equal(t, "–", Stars(int64(-1)))
	require.Equal(t, "–", Stars((*int64)(nil)))
}

func TestRatingScale(t *testing.T) {
	r := func(n int) *int { return &n }
	cases := []struct {
		user string
		in   int
		want *int64
	}{
		{`{"id":"x"}`, 4, ptr(int64(8))},                                // undeclared: whole stars
		{`{"id":"x","min_rating":0,"max_rating":5}`, 5, ptr(int64(10))}, // legacy
		{`{"id":"x","min_rating":0,"max_rating":10}`, 7, ptr(int64(7))}, // ours
		{`{"id":"x","min_rating":1,"max_rating":10}`, 10, ptr(int64(10))},
		{`{"id":"x","min_rating":1,"max_rating":10}`, 1, ptr(int64(0))},
		{`{"id":"x","min_rating":0,"max_rating":100}`, 73, ptr(int64(7))}, // rounds to nearest half star
		{`{"id":"x","min_rating":0,"max_rating":5}`, 6, nil},              // out of range
		{`{"id":"x","min_rating":5,"max_rating":5}`, 4, ptr(int64(8))},    // nonsense: whole stars
	}
	for _, c := range cases {
		require.Equal(t, c.want, scaleOf([]byte(c.user)).scale(r(c.in)), "%s %d", c.user, c.in)
	}
}

func ptr[T any](v T) *T { return &v }

// Whole-star ratings in an existing database become half stars.
func TestHalfStarMigration(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "revs.db"))
	require.NoError(t, err)
	goose.SetBaseFS(localMigrations)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, "migrations", 2))
	goose.SetBaseFS(nil)
	_, err = db.Exec(`INSERT INTO movies (id, title) VALUES (1, 'Alien');
		INSERT INTO viewings (movie_id, rating) VALUES (1, 4), (1, NULL), (1, 0)`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	c, err := Open(Options{DataDir: dir})
	require.NoError(t, err)
	t.Cleanup(func() { c.Close() })
	vs, err := c.AllViewings(ctx)
	require.NoError(t, err)
	var got []*int64
	for _, v := range vs {
		got = append(got, v.Rating)
	}
	require.ElementsMatch(t, []*int64{ptr(int64(8)), nil, ptr(int64(0))}, got)

	// The widened constraint accepts half stars.
	_, err = c.AddViewing(ctx, 1, ViewingInput{Rating: ptr(int64(9))})
	require.NoError(t, err)
}
