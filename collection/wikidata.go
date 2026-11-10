package collection

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// DefaultWikidataURL is the Wikidata API endpoint used for movie lookup.
const DefaultWikidataURL = "https://www.wikidata.org/w/api.php"

// userAgent identifies this client to Wikimedia, whose API policy asks
// for a descriptive agent.
const userAgent = "revs-ui/0.1 (+https://github.com/openrevs/revs)"

// filmClasses are "instance of" (P31) values treated as films.
var filmClasses = map[string]bool{
	"Q11424":    true, // film
	"Q24869":    true, // feature film
	"Q24862":    true, // short film
	"Q506240":   true, // television film
	"Q202866":   true, // animated film
	"Q93204":    true, // documentary film
	"Q226730":   true, // silent film
	"Q229390":   true, // 3D film
	"Q20650540": true, // anime film
	"Q17517379": true, // animated short film
}

// filmFilter is a CirrusSearch keyword matching items that are an
// instance of a film class or have a TMDB movie id ("|" means or).
func filmFilter() string {
	terms := []string{"P4947"}
	for _, class := range slices.Sorted(maps.Keys(filmClasses)) {
		terms = append(terms, "P31="+class)
	}
	return "haswbstatement:" + strings.Join(terms, "|")
}

// nonFilmClasses are "instance of" values that rule an item out even
// if it otherwise looks like a film.
var nonFilmClasses = map[string]bool{
	"Q5398426":  true, // television series
	"Q1259759":  true, // miniseries
	"Q21191270": true, // television series episode
	"Q3464665":  true, // television series season
	"Q1261214":  true, // television special
	"Q7889":     true, // video game
	"Q24856":    true, // film series
	"Q196600":   true, // media franchise
	"Q482994":   true, // album
}

// SearchResult is one candidate movie from a search, with ids from Wikidata.
type SearchResult struct {
	ID          string
	Title       string
	Description string
	Year        *int64
	IMDB        *string
	TMDB        *string
}

type wikidata struct {
	baseURL string
	http    *http.Client
}

// Search finds movies matching query. It uses Wikidata's full-text
// search restricted to items with a film class or a TMDB movie id:
// the label search (wbsearchentities) can't filter, and for a query
// like "star wars" its hits are almost all games and albums. Items
// are checked again after fetching, which also drops TV series,
// games, and franchises that carry a film statement by mistake.
func (w *wikidata) Search(ctx context.Context, query string) ([]SearchResult, error) {
	query = strings.Join(strings.Fields(query), " ")
	if query == "" {
		return nil, nil
	}

	var search struct {
		Query struct {
			Search []struct {
				Title string `json:"title"` // the item id, e.g. "Q104123"
			} `json:"search"`
		} `json:"query"`
	}
	err := w.get(ctx, url.Values{
		"action":   {"query"},
		"list":     {"search"},
		"srsearch": {query + " " + filmFilter()},
		"srlimit":  {"25"},
		"srprop":   {""},
	}, &search)
	if err != nil {
		return nil, err
	}
	if len(search.Query.Search) == 0 {
		return nil, nil
	}

	ids := make([]string, len(search.Query.Search))
	for i, s := range search.Query.Search {
		ids[i] = s.Title
	}

	var entities struct {
		Entities map[string]wdEntity `json:"entities"`
	}
	err = w.get(ctx, url.Values{
		"action": {"wbgetentities"},
		"ids":    {strings.Join(ids, "|")},
		"props":  {"labels|descriptions|claims"},
		// All languages: many items carry their main label only in
		// "mul" (the multilingual default), and some obscure films only
		// in their original language.
	}, &entities)
	if err != nil {
		return nil, err
	}

	var results []SearchResult
	for _, id := range ids { // keep search relevance order
		e, ok := entities.Entities[id]
		if !ok {
			continue
		}
		r := e.result(id)
		if r.isFilm {
			results = append(results, r.SearchResult)
		}
	}
	return results, nil
}

func (w *wikidata) get(ctx context.Context, params url.Values, out any) error {
	params.Set("format", "json")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.baseURL+"?"+params.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := w.http.Do(req)
	if err != nil {
		return fmt.Errorf("wikidata: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("wikidata: %s", resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("wikidata: decoding response: %w", err)
	}
	return nil
}

type wdEntity struct {
	Labels       map[string]struct{ Value string } `json:"labels"`
	Descriptions map[string]struct{ Value string } `json:"descriptions"`
	Claims       map[string][]struct {
		Mainsnak struct {
			Datavalue struct {
				Value json.RawMessage `json:"value"`
			} `json:"datavalue"`
		} `json:"mainsnak"`
	} `json:"claims"`
}

// strings returns the values of a claim with string or item values (an
// item's value is reported as its id).
func (e wdEntity) strings(prop string) []string {
	var out []string
	for _, c := range e.Claims[prop] {
		raw := c.Mainsnak.Datavalue.Value
		var s string
		if json.Unmarshal(raw, &s) == nil {
			out = append(out, s)
			continue
		}
		var v struct {
			ID   string `json:"id"`
			Time string `json:"time"`
		}
		if json.Unmarshal(raw, &v) == nil {
			out = append(out, v.ID+v.Time)
		}
	}
	return out
}

type wdCandidate struct {
	SearchResult
	isFilm bool
}

// anyValue returns a deterministic value from a language map, or "".
func anyValue(m map[string]struct{ Value string }) string {
	for _, lang := range slices.Sorted(maps.Keys(m)) {
		if v := m[lang].Value; v != "" {
			return v
		}
	}
	return ""
}

// firstNonEmpty returns the first non-empty string.
func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func (e wdEntity) result(id string) wdCandidate {
	c := wdCandidate{SearchResult: SearchResult{
		ID:          id,
		Title:       firstNonEmpty(e.Labels["en"].Value, e.Labels["mul"].Value, anyValue(e.Labels), id),
		Description: firstNonEmpty(e.Descriptions["en"].Value, e.Descriptions["mul"].Value),
	}}

	for _, v := range e.strings("P345") { // IMDb ID
		if imdb := normalizeIMDB(v); imdb != nil {
			c.IMDB = imdb
			break
		}
	}
	for _, v := range e.strings("P4947") { // TMDB movie ID
		if tmdb := normalizeTMDB(v); tmdb != nil {
			c.TMDB = tmdb
			c.isFilm = true
			break
		}
	}
	for _, v := range e.strings("P31") { // instance of
		if filmClasses[v] {
			c.isFilm = true
		}
	}
	for _, v := range e.strings("P31") {
		if nonFilmClasses[v] {
			c.isFilm = false
		}
	}
	// Publication date: times look like "+1994-05-21T00:00:00Z". Use
	// the earliest year, as films list several regional releases.
	for _, v := range e.strings("P577") {
		var y int64
		if _, err := fmt.Sscanf(strings.TrimPrefix(v, "+"), "%4d", &y); err == nil && y > 0 {
			if c.Year == nil || y < *c.Year {
				c.Year = &y
			}
		}
	}

	return c
}
