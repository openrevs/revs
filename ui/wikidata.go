package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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

// WikidataResult is one candidate movie from a search.
type WikidataResult struct {
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

// Search finds movies matching query. Only items that look like films
// (by class, or by having an IMDb title or TMDB movie id) are returned.
func (w *wikidata) Search(ctx context.Context, query string) ([]WikidataResult, error) {
	var search struct {
		Search []struct {
			ID string `json:"id"`
		} `json:"search"`
	}
	err := w.get(ctx, url.Values{
		"action":   {"wbsearchentities"},
		"search":   {query},
		"language": {"en"},
		"uselang":  {"en"},
		"type":     {"item"},
		"limit":    {"20"},
	}, &search)
	if err != nil {
		return nil, err
	}
	if len(search.Search) == 0 {
		return nil, nil
	}

	ids := make([]string, len(search.Search))
	for i, s := range search.Search {
		ids[i] = s.ID
	}

	var entities struct {
		Entities map[string]wdEntity `json:"entities"`
	}
	err = w.get(ctx, url.Values{
		"action":    {"wbgetentities"},
		"ids":       {strings.Join(ids, "|")},
		"props":     {"labels|descriptions|claims"},
		"languages": {"en"},
	}, &entities)
	if err != nil {
		return nil, err
	}

	var results []WikidataResult
	for _, id := range ids { // keep search relevance order
		e, ok := entities.Entities[id]
		if !ok {
			continue
		}
		r := e.result(id)
		if r.isFilm {
			results = append(results, r.WikidataResult)
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
	WikidataResult
	isFilm bool
}

func (e wdEntity) result(id string) wdCandidate {
	c := wdCandidate{WikidataResult: WikidataResult{
		ID:          id,
		Title:       e.Labels["en"].Value,
		Description: e.Descriptions["en"].Value,
	}}
	if c.Title == "" {
		c.Title = id
	}

	for _, v := range e.strings("P345") { // IMDb ID
		if imdb := normalizeIMDB(v); imdb != nil {
			c.IMDB = imdb
			c.isFilm = true
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
