package collection

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/openrevs/revs/collection/localdb"
)

// DateLayout is the format of viewing dates.
const DateLayout = "2006-01-02"

var (
	imdbRe     = regexp.MustCompile(`tt\d{5,}`)
	wikidataRe = regexp.MustCompile(`(?i)\bQ\d+\b`)
	tmdbURLRe  = regexp.MustCompile(`themoviedb\.org/movie/(\d+)`)
	digitsRe   = regexp.MustCompile(`^\d+$`)
)

// normalizeIMDB extracts an IMDb title id ("tt0110912") from an id or
// URL, or returns nil.
func normalizeIMDB(s string) *string {
	m := imdbRe.FindString(s)
	if m == "" {
		return nil
	}
	return &m
}

// normalizeTMDB extracts a TMDB movie id ("680") from an id, a slug
// such as "680-pulp-fiction", or a URL, or returns nil.
func normalizeTMDB(s string) *string {
	s = strings.TrimSpace(s)
	if m := tmdbURLRe.FindStringSubmatch(s); m != nil {
		return &m[1]
	}
	s, _, _ = strings.Cut(s, "-")
	if !digitsRe.MatchString(s) {
		return nil
	}
	return &s
}

// normalizeWikidata extracts a Wikidata item id ("Q104123") from an id
// or URL, or returns nil.
func normalizeWikidata(s string) *string {
	m := wikidataRe.FindString(s)
	if m == "" {
		return nil
	}
	m = strings.ToUpper(m)
	return &m
}

// MovieInput is a validated movie.
type MovieInput struct {
	Title    string
	Year     *int64
	IMDB     *string
	TMDB     *string
	Wikidata *string
}

func (in MovieInput) createParams() localdb.CreateMovieParams {
	return localdb.CreateMovieParams{Title: in.Title, Year: in.Year, ImdbID: in.IMDB, TmdbID: in.TMDB, WikidataID: in.Wikidata}
}

func (in MovieInput) updateParams(id int64) localdb.UpdateMovieParams {
	return localdb.UpdateMovieParams{Title: in.Title, Year: in.Year, ImdbID: in.IMDB, TmdbID: in.TMDB, WikidataID: in.Wikidata, ID: id}
}

// MovieFields is a movie as the user typed it: free text that Parse
// validates. Ids may be given as ids or as URLs.
type MovieFields struct {
	Title, Year, IMDB, TMDB, Wikidata string
}

// MovieFieldsFrom returns the fields for editing an existing movie.
func MovieFieldsFrom(m localdb.Movie) MovieFields {
	f := MovieFields{Title: m.Title, IMDB: Deref(m.ImdbID), TMDB: Deref(m.TmdbID), Wikidata: Deref(m.WikidataID)}
	if m.Year != nil {
		f.Year = strconv.FormatInt(*m.Year, 10)
	}
	return f
}

// MovieFieldsFromResult returns the fields for a search result.
func MovieFieldsFromResult(r SearchResult) MovieFields {
	f := MovieFields{Title: r.Title, IMDB: Deref(r.IMDB), TMDB: Deref(r.TMDB), Wikidata: r.ID}
	if r.Year != nil {
		f.Year = strconv.FormatInt(*r.Year, 10)
	}
	return f
}

// Parse trims and validates the fields, reporting every problem found.
func (f *MovieFields) Parse() (MovieInput, error) {
	for _, s := range []*string{&f.Title, &f.Year, &f.IMDB, &f.TMDB, &f.Wikidata} {
		*s = strings.TrimSpace(*s)
	}
	in := MovieInput{Title: f.Title}

	var errs []error
	if f.Title == "" {
		errs = append(errs, errors.New("title is required"))
	}
	if f.Year != "" {
		y, err := strconv.ParseInt(f.Year, 10, 64)
		if err != nil || y < 1870 || y > int64(time.Now().Year())+10 {
			errs = append(errs, fmt.Errorf("year %q is not a plausible year", f.Year))
		} else {
			in.Year = &y
		}
	}
	if f.IMDB != "" {
		if in.IMDB = normalizeIMDB(f.IMDB); in.IMDB == nil {
			errs = append(errs, fmt.Errorf("IMDb id %q should look like tt0110912", f.IMDB))
		}
	}
	if f.TMDB != "" {
		if in.TMDB = normalizeTMDB(f.TMDB); in.TMDB == nil {
			errs = append(errs, fmt.Errorf("TMDB id %q should be a number like 680", f.TMDB))
		}
	}
	if f.Wikidata != "" {
		if in.Wikidata = normalizeWikidata(f.Wikidata); in.Wikidata == nil {
			errs = append(errs, fmt.Errorf("Wikidata id %q should look like Q104123", f.Wikidata))
		}
	}

	return in, errors.Join(errs...)
}

// ViewingInput is a validated viewing.
type ViewingInput struct {
	Date   *string
	Rating *int64
	Review string
}

// ViewingFields is a viewing as the user typed it. Rating is a number
// of stars in steps of ½ ("3.5"), or "" or "none" for unrated.
type ViewingFields struct {
	Date, Rating, Review string
}

// NewViewingFields returns the fields for a new viewing: today, unrated.
func NewViewingFields() ViewingFields {
	return ViewingFields{Date: time.Now().Format(DateLayout), Rating: "none"}
}

// ViewingFieldsFrom returns the fields for editing an existing viewing.
func ViewingFieldsFrom(v localdb.Viewing) ViewingFields {
	f := ViewingFields{Date: Deref(v.ViewedOn), Review: v.Review, Rating: "none"}
	if v.Rating != nil {
		f.Rating = FormatStars(*v.Rating)
	}
	return f
}

// Parse trims and validates the fields, reporting every problem found.
func (f *ViewingFields) Parse() (ViewingInput, error) {
	f.Date = strings.TrimSpace(f.Date)
	f.Rating = strings.TrimSpace(f.Rating)
	f.Review = strings.TrimSpace(strings.ReplaceAll(f.Review, "\r\n", "\n"))
	in := ViewingInput{Review: f.Review}

	var errs []error
	if f.Date != "" {
		if _, err := time.Parse(DateLayout, f.Date); err != nil {
			errs = append(errs, fmt.Errorf("date %q should be YYYY-MM-DD", f.Date))
		} else {
			in.Date = &f.Date
		}
	}
	switch f.Rating {
	case "", "none":
		f.Rating = "none"
	default:
		n, err := ParseStars(f.Rating)
		if err != nil {
			errs = append(errs, err)
		} else {
			in.Rating = &n
		}
	}

	return in, errors.Join(errs...)
}
