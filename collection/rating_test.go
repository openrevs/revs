package collection

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseStars(t *testing.T) {
	for in, want := range map[string]int64{"0.5": 1, ".5": 1, "3": 6, "3.5": 7, " 3½ ": 7, "½": 1, "5": 10, "5.0": 10} {
		got, err := ParseStars(in)
		require.NoError(t, err, in)
		require.Equal(t, want, got, in)
		if in == "3.5" || in == "3" {
			require.Equal(t, in, FormatStars(got))
		}
	}
	for _, in := range []string{"", "0", "0.0", "-1", "5.5", "6", "3.25", "abc", "NaN", "Inf", "3½½"} {
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
		{`{"id":"x","min_rating":0,"max_rating":5}`, 5, ptr(int64(10))}, // whole stars
		{`{"id":"x","min_rating":0,"max_rating":10}`, 7, ptr(int64(7))}, // ours
		{`{"id":"x","min_rating":1,"max_rating":10}`, 10, ptr(int64(10))},
		{`{"id":"x","min_rating":1,"max_rating":10}`, 1, ptr(int64(0))},
		{`{"id":"x","min_rating":0,"max_rating":100}`, 73, ptr(int64(7))}, // rounds to nearest half star
		{`{"id":"x","min_rating":0,"max_rating":5}`, 6, nil},              // out of range
	}
	for _, c := range cases {
		s, err := scaleOf([]byte(c.user))
		require.NoError(t, err, c.user)
		require.Equal(t, c.want, s.scale(r(c.in)), "%s %d", c.user, c.in)
	}

	// An author line without a valid scale is invalid.
	for _, bad := range []string{
		`{"id":"x"}`,
		`{"id":"x","max_rating":10}`,
		`{"id":"x","min_rating":5,"max_rating":5}`,
		`{"id":"x","min_rating":0,"max_rating":"10"}`,
	} {
		_, err := scaleOf([]byte(bad))
		require.Error(t, err, bad)
	}
}

func ptr[T any](v T) *T { return &v }

// The schema accepts half-star ratings (0-10).
func TestHalfStarRatings(t *testing.T) {
	ctx := context.Background()
	c, err := Open(Options{DataDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { c.Close() })

	_, err = c.db.Exec(`INSERT INTO movies (id, title) VALUES (1, 'Alien')`)
	require.NoError(t, err)
	_, err = c.AddViewing(ctx, 1, ViewingInput{Rating: ptr(int64(9))})
	require.NoError(t, err)
	_, err = c.AddViewing(ctx, 1, ViewingInput{Rating: ptr(int64(11))})
	require.Error(t, err)
}
