package collection

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestViewingFieldsDate(t *testing.T) {
	for date, ok := range map[string]bool{
		"":           true,
		"2024-02-29": true,
		" 2024-03 ":  true,
		"1999":       true,
		"2023-02-29": false,
		"2023-13":    false,
		"2023-2":     false,
		"99":         false,
		"yesterday":  false,
	} {
		f := ViewingFields{Date: date, Rating: "none"}
		in, err := f.Parse()
		require.Equal(t, ok, err == nil, "%q: %v", date, err)
		if ok && strings.TrimSpace(date) == "" {
			require.Nil(t, in.Date, "a blank date is no date")
		}
	}
	require.Empty(t, NewViewingFields(TypeMovie).Date, "a new viewing starts undated")
}

// Partial dates are stored, exported, and ordered: a newest-first list
// puts a day before the month and year it's in, and undated viewings
// last, most recently added first.
func TestPartialDates(t *testing.T) {
	ctx := context.Background()
	c := openTest(t)
	m, _, err := c.AddMovie(ctx, MovieInput{Title: "Alien"})
	require.NoError(t, err)
	for _, d := range []string{"2019", "2019-05-04", "", "2019-05", ""} {
		in := ViewingInput{Review: d}
		if d != "" {
			in.Date = &d
		}
		_, err := c.AddViewing(ctx, m.ID, in)
		require.NoError(t, err)
	}

	vs, err := c.Viewings(ctx, m.ID)
	require.NoError(t, err)
	var dates []string
	var ids []int64
	for _, v := range vs {
		dates = append(dates, Deref(v.ViewedOn))
		ids = append(ids, v.ID)
	}
	require.Equal(t, []string{"2019-05-04", "2019-05", "2019", "", ""}, dates)
	require.Greater(t, ids[3], ids[4], "undated: most recently added first")

	hist, err := c.History(ctx, 10)
	require.NoError(t, err)
	require.Equal(t, ids[3], hist[3].ID)

	recent, err := c.RecentlyAdded(ctx, 10)
	require.NoError(t, err)
	var reviews []string
	for _, r := range recent {
		reviews = append(reviews, r.Review)
	}
	require.Equal(t, []string{"", "2019-05", "", "2019-05-04", "2019"}, reviews)

	// Clearing a date leaves the viewing undated.
	require.NoError(t, c.UpdateViewing(ctx, ids[0], ViewingInput{}))
	v, err := c.Viewing(ctx, ids[0])
	require.NoError(t, err)
	require.Nil(t, v.ViewedOn)

	// Partial dates survive an import.
	c2 := openTest(t)
	doc := `{"id":"someone","max_rating":10,"min_rating":0}` + "\n" +
		`{"entries":[{"date":"1997"},{"date":"2001-09","rating":8}],"title":"Alien","type":"movie","year":1979}`
	res, err := c2.Import(ctx, []byte(doc))
	require.NoError(t, err)
	require.Equal(t, 2, res.ViewingsAdded)
	ms, err := c2.Movies(ctx, "", "")
	require.NoError(t, err)
	require.Equal(t, "2001-09", ms[0].LastViewed)

	// And a CSV import.
	stats, err := c2.ImportCSV(ctx, []byte("title,view_date\nBrazil,1985-12\nHeat,1995\n"))
	require.NoError(t, err)
	require.Equal(t, 2, stats.ViewingsAdded)
}
