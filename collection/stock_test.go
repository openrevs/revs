package collection

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStockMovies(t *testing.T) {
	require.GreaterOrEqual(t, len(stockMovies), 100)
	seen := map[string]bool{}
	for _, m := range stockMovies {
		require.NotEmpty(t, m.Title, m.ID)
		require.Equal(t, m.ID, Deref(normalizeWikidata(m.ID)), m.Title)
		require.Equal(t, *m.IMDB, Deref(normalizeIMDB(*m.IMDB)), m.Title)
		require.Equal(t, *m.TMDB, Deref(normalizeTMDB(*m.TMDB)), m.Title)
		require.False(t, seen[m.ID], "duplicate %s", m.ID)
		seen[m.ID] = true
	}
}

func TestSearchStock(t *testing.T) {
	titles := func(q string) []string {
		var out []string
		for _, r := range searchStock(q) {
			out = append(out, r.Title)
		}
		return out
	}
	require.Equal(t, []string{"Pulp Fiction"}, titles("  PULP fiction "))
	require.Equal(t, []string{"Amélie"}, titles("amelie"))
	require.Equal(t, []string{"Schindler's List"}, titles("schindlers list"))
	require.Equal(t, []string{"Star Wars: Episode V – The Empire Strikes Back"}, titles("empire strikes back"))
	require.Equal(t, []string{"Toy Story 3"}, titles("toy story 2010"))
	require.Len(t, titles("star wars"), 4)
	require.Empty(t, titles("pulp"+"x"))
	require.Empty(t, titles(" "))
}

func openTest(t *testing.T, opts ...func(*Options)) *Collection {
	t.Helper()
	o := Options{DataDir: t.TempDir()}
	for _, f := range opts {
		f(&o)
	}
	c, err := Open(o)
	require.NoError(t, err)
	t.Cleanup(func() { c.Close() })
	return c
}

// Offline, a search still returns stock matches, and a search with
// none gets a plain message rather than a raw network error.
func TestSearchOffline(t *testing.T) {
	// A server that is closed at once: its port refuses connections.
	wd := httptest.NewServer(http.NotFoundHandler())
	wd.Close()
	c := openTest(t, func(o *Options) { o.WikidataURL = wd.URL })

	results, msg := c.Search(context.Background(), "the matrix")
	require.NotEmpty(t, results)
	require.Equal(t, "tt0133093", *results[0].IMDB)
	require.Contains(t, msg, "you may be offline")

	results, msg = c.Search(context.Background(), "no such film")
	require.Empty(t, results)
	require.Contains(t, msg, "you may be offline")
	require.NotContains(t, msg, "connection refused")
}

// A hanging Wikidata doesn't hold up stock matches for long.
func TestSearchWikidataHangs(t *testing.T) {
	release := make(chan struct{})
	wd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(wd.Close)
	t.Cleanup(func() { close(release) })

	old := stockSearchTimeout
	stockSearchTimeout = 100 * time.Millisecond
	t.Cleanup(func() { stockSearchTimeout = old })

	c := openTest(t, func(o *Options) { o.WikidataURL = wd.URL })
	start := time.Now()
	results, msg := c.Search(context.Background(), "the matrix")
	require.Less(t, time.Since(start), 5*time.Second)
	require.Equal(t, "tt0133093", *results[0].IMDB)
	require.Contains(t, msg, "you may be offline")
}
