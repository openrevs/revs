package ui

import (
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
		require.Equal(t, m.ID, deref(normalizeWikidata(m.ID)), m.Title)
		require.Equal(t, *m.IMDB, deref(normalizeIMDB(*m.IMDB)), m.Title)
		require.Equal(t, *m.TMDB, deref(normalizeTMDB(*m.TMDB)), m.Title)
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

// Offline, a search still shows stock matches, and a search with none
// gets a plain message rather than a raw network error.
func TestSearchOffline(t *testing.T) {
	// A server that is closed at once: its port refuses connections.
	wd := httptest.NewServer(http.NotFoundHandler())
	wd.Close()
	u := newTestUI(t, func(o *Options) { o.WikidataURL = wd.URL })

	page := u.get("/movies/new?q=the+matrix")
	require.Contains(t, page, "tt0133093")
	require.Contains(t, page, "Showing built-in matches only")
	require.Contains(t, page, "you may be offline")
	require.NotContains(t, page, "flash-error")

	page = u.get("/movies/new?q=no+such+film")
	require.Contains(t, page, "you may be offline")
	require.Contains(t, page, "enter the details yourself")
	require.NotContains(t, page, "connection refused")
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

	u := newTestUI(t, func(o *Options) { o.WikidataURL = wd.URL })
	start := time.Now()
	page := u.get("/movies/new?q=the+matrix")
	require.Less(t, time.Since(start), 5*time.Second)
	require.Contains(t, page, "tt0133093")
	require.Contains(t, page, "you may be offline")
}
