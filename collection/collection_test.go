package collection

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWikidataSearch(t *testing.T) {
	claim := func(v any) map[string]any {
		raw, _ := json.Marshal(v)
		return map[string]any{"mainsnak": map[string]any{"datavalue": map[string]any{"value": json.RawMessage(raw)}}}
	}
	class := func(id string) any { return []any{claim(map[string]string{"id": id})} }
	label := func(lang, v string) map[string]any { return map[string]any{lang: map[string]string{"value": v}} }

	ids := []string{"Q104123", "Q999", "Q5", "Q7", "Q8", "Q9", "Q10", "Q11", "Q12", "Q13"}
	wd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Contains(t, r.Header.Get("User-Agent"), "revs-ui")
		switch r.URL.Query().Get("action") {
		case "query":
			// The query is restricted to film and series items in the
			// search itself.
			sr := r.URL.Query().Get("srsearch")
			require.True(t, strings.HasPrefix(sr, "pulp fiction haswbstatement:P4947|P4983|"), sr)
			require.Contains(t, sr, "|P31=Q11424")
			require.Contains(t, sr, "|P31=Q5398426")
			var hits []map[string]string
			for _, id := range ids {
				hits = append(hits, map[string]string{"title": id})
			}
			json.NewEncoder(w).Encode(map[string]any{"query": map[string]any{"search": hits}})
		case "wbgetentities":
			require.Equal(t, strings.Join(ids, "|"), r.URL.Query().Get("ids"))
			json.NewEncoder(w).Encode(map[string]any{"entities": map[string]any{
				// Its label is only in "mul", as on the real item.
				"Q104123": map[string]any{
					"labels":       label("mul", "Pulp Fiction"),
					"descriptions": label("en", "1994 film by Quentin Tarantino"),
					"claims": map[string]any{
						"P31":   class("Q11424"),
						"P345":  []any{claim("tt0110912")},
						"P4947": []any{claim("680")},
						"P577":  []any{claim(map[string]string{"time": "+1994-10-14T00:00:00Z"}), claim(map[string]string{"time": "+1994-05-21T00:00:00Z"})},
					},
				},
				// A soundtrack album: not a film.
				"Q999": map[string]any{"labels": label("en", "Pulp Fiction (soundtrack)"), "claims": map[string]any{"P31": class("Q482994")}},
				// Unknown class but a TMDB movie id; labelled only in French.
				"Q5": map[string]any{"labels": label("fr", "Fiction pulpeuse"), "claims": map[string]any{"P4947": []any{claim("12345")}}},
				// A TV special with a TMDB movie id: not a movie.
				"Q7": map[string]any{"labels": label("en", "Pulp Fiction Special"), "claims": map[string]any{"P31": class("Q1261214"), "P4947": []any{claim("777")}}},
				// A video game with an IMDb id: not a movie.
				"Q8": map[string]any{"labels": label("en", "Pulp Fiction: The Game"), "claims": map[string]any{"P31": class("Q7889"), "P345": []any{claim("tt7777777")}}},
				// A title with an IMDb id and no class we know: neither.
				"Q9": map[string]any{"labels": label("en", "Pulp Fiction: The Series"), "claims": map[string]any{"P345": []any{claim("tt8888888")}}},
				// A TV series: its year is when it started, not a later
				// publication date.
				"Q10": map[string]any{"labels": label("en", "Pulp Fiction Stories"), "claims": map[string]any{
					"P31":   class("Q5398426"),
					"P345":  []any{claim("tt2802850")},
					"P4983": []any{claim("60622")},
					"P580":  []any{claim(map[string]string{"time": "+2014-04-15T00:00:00Z"})},
					"P577":  []any{claim(map[string]string{"time": "+2016-01-01T00:00:00Z"})},
				}},
				// A season of a series, with the series's TMDB id: excluded.
				"Q11": map[string]any{"labels": label("en", "Pulp Fiction Stories, season 1"), "claims": map[string]any{"P31": class("Q3464665"), "P4983": []any{claim("60622")}}},
				// No class we know but a TMDB TV series id: a series.
				"Q12": map[string]any{"labels": label("en", "Pulp"), "claims": map[string]any{"P4983": []any{claim("555")}}},
				// A miniseries: a series.
				"Q13": map[string]any{"labels": label("en", "Pulp Fiction: Limited"), "claims": map[string]any{"P31": class("Q1259759")}},
			}})
		default:
			t.Errorf("unexpected action %q", r.URL.Query().Get("action"))
		}
	}))
	t.Cleanup(wd.Close)

	c := openTest(t, func(o *Options) { o.WikidataURL = wd.URL })
	results, err := c.wikidata.Search(context.Background(), "  pulp   fiction ")
	require.NoError(t, err)
	require.Len(t, results, 5)
	require.Equal(t, "Q104123", results[0].ID)
	require.Equal(t, TypeMovie, results[0].Type)
	require.Equal(t, "Pulp Fiction", results[0].Title)
	require.Equal(t, int64(1994), *results[0].Year)
	require.Equal(t, "tt0110912", *results[0].IMDB)
	require.Equal(t, "680", *results[0].TMDB)
	require.Equal(t, "Q5", results[1].ID)
	require.Equal(t, "Fiction pulpeuse", results[1].Title)
	require.Equal(t, TypeMovie, results[1].Type)

	require.Equal(t, "Q10", results[2].ID)
	require.Equal(t, TypeTV, results[2].Type)
	require.Equal(t, int64(2014), *results[2].Year)
	require.Equal(t, "60622", *results[2].TMDB)
	require.Equal(t, "Q12", results[3].ID)
	require.Equal(t, TypeTV, results[3].Type)
	require.Equal(t, "555", *results[3].TMDB)
	require.Equal(t, "Q13", results[4].ID)
	require.Equal(t, TypeTV, results[4].Type)
	require.Nil(t, results[4].TMDB)

}

func TestNormalizeIDs(t *testing.T) {
	for in, want := range map[string]string{
		"tt0110912": "tt0110912", "https://www.imdb.com/title/tt0110912/?ref=x": "tt0110912",
	} {
		require.Equal(t, want, Deref(normalizeIMDB(in)), in)
	}
	for in, want := range map[string]string{
		"680": "680", "680-pulp-fiction": "680", "https://www.themoviedb.org/movie/680-pulp-fiction": "680", "abc": "",
	} {
		require.Equal(t, want, Deref(normalizeTMDB(TypeMovie, in)), in)
	}
	// TMDB numbers movies and series separately: a URL must be of the
	// item's kind.
	for in, want := range map[string]string{
		"60622": "60622", "https://www.themoviedb.org/tv/60622-fargo": "60622", "https://www.themoviedb.org/movie/680": "",
	} {
		require.Equal(t, want, Deref(normalizeTMDB(TypeTV, in)), in)
	}
	require.Nil(t, normalizeTMDB(TypeMovie, "https://www.themoviedb.org/tv/60622"))
	for in, want := range map[string]string{
		"Q104123": "Q104123", "q104123": "Q104123", "https://www.wikidata.org/wiki/Q104123": "Q104123", "x": "",
	} {
		require.Equal(t, want, Deref(normalizeWikidata(in)), in)
	}
}
