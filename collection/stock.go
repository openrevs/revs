package collection

import (
	_ "embed"
	"encoding/json"
	"strconv"
	"strings"
	"unicode"
)

// stockMoviesJSON is a list of about a hundred popular films from the
// last fifty years, with ids taken from Wikidata. Searches check it
// before Wikidata, so common titles are found even when Wikidata is
// slow or unreachable.
//
//go:embed stock_movies.json
var stockMoviesJSON []byte

type stockMovie struct {
	SearchResult
	words []string // normalized title words and year, for matching
}

var stockMovies = loadStockMovies()

func loadStockMovies() []stockMovie {
	var raw []struct {
		ID          string `json:"id"`
		Title       string `json:"title"`
		Year        int64  `json:"year"`
		Description string `json:"description"`
		IMDB        string `json:"imdb"`
		TMDB        string `json:"tmdb"`
	}
	if err := json.Unmarshal(stockMoviesJSON, &raw); err != nil {
		panic("ui: bad stock_movies.json: " + err.Error())
	}
	out := make([]stockMovie, len(raw))
	for i, r := range raw {
		out[i] = stockMovie{
			SearchResult: SearchResult{
				ID:          r.ID,
				Title:       r.Title,
				Description: r.Description,
				Year:        &r.Year,
				IMDB:        &r.IMDB,
				TMDB:        &r.TMDB,
			},
			words: append(searchWords(r.Title), strconv.FormatInt(r.Year, 10)),
		}
	}
	return out
}

// searchStock returns the stock movies whose title (or year) contains
// every word of query.
func searchStock(query string) []SearchResult {
	q := searchWords(query)
	if len(q) == 0 {
		return nil
	}
	var out []SearchResult
	for _, m := range stockMovies {
		if containsAll(m.words, q) {
			out = append(out, m.SearchResult)
		}
	}
	return out
}

func containsAll(have, want []string) bool {
outer:
	for _, w := range want {
		for _, h := range have {
			if h == w {
				continue outer
			}
		}
		return false
	}
	return true
}

// accentFolder maps the accented letters found in titles to plain ones.
var accentFolder = strings.NewReplacer(
	"á", "a", "à", "a", "â", "a", "ä", "a", "ã", "a", "å", "a",
	"é", "e", "è", "e", "ê", "e", "ë", "e",
	"í", "i", "ì", "i", "î", "i", "ï", "i",
	"ó", "o", "ò", "o", "ô", "o", "ö", "o", "õ", "o",
	"ú", "u", "ù", "u", "û", "u", "ü", "u",
	"ñ", "n", "ç", "c",
)

// searchWords lowercases s, folds accents, and splits it into words of
// letters and digits. Apostrophes are dropped, so "Schindler's" is one
// word ("schindlers"), and other punctuation separates words.
func searchWords(s string) []string {
	s = accentFolder.Replace(strings.ToLower(s))
	s = strings.NewReplacer("'", "", "’", "").Replace(s)
	return strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}
