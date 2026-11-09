package ui

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/openrevs/revs/ui/localdb"
)

const dateLayout = "2006-01-02"

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

// movieInput is a validated movie form (or imported title).
type movieInput struct {
	Title    string
	Year     *int64
	IMDB     *string
	TMDB     *string
	Wikidata *string
}

func (in movieInput) createParams() localdb.CreateMovieParams {
	return localdb.CreateMovieParams{Title: in.Title, Year: in.Year, ImdbID: in.IMDB, TmdbID: in.TMDB, WikidataID: in.Wikidata}
}

func (in movieInput) updateParams(id int64) localdb.UpdateMovieParams {
	return localdb.UpdateMovieParams{Title: in.Title, Year: in.Year, ImdbID: in.IMDB, TmdbID: in.TMDB, WikidataID: in.Wikidata, ID: id}
}

// movieForm is the movie form's raw values, redisplayed on error.
type movieForm struct {
	Title, Year, IMDB, TMDB, Wikidata string
}

func movieFormFrom(m localdb.Movie) movieForm {
	f := movieForm{Title: m.Title, IMDB: deref(m.ImdbID), TMDB: deref(m.TmdbID), Wikidata: deref(m.WikidataID)}
	if m.Year != nil {
		f.Year = strconv.FormatInt(*m.Year, 10)
	}
	return f
}

func parseMovieForm(r *http.Request) (movieForm, movieInput, error) {
	f := movieForm{
		Title:    strings.TrimSpace(r.PostFormValue("title")),
		Year:     strings.TrimSpace(r.PostFormValue("year")),
		IMDB:     strings.TrimSpace(r.PostFormValue("imdb_id")),
		TMDB:     strings.TrimSpace(r.PostFormValue("tmdb_id")),
		Wikidata: strings.TrimSpace(r.PostFormValue("wikidata_id")),
	}
	in := movieInput{Title: f.Title}

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

	return f, in, errors.Join(errs...)
}

// viewingForm is the viewing form's raw values.
type viewingForm struct {
	Date, Rating, Review string
}

func viewingFormFrom(v localdb.Viewing) viewingForm {
	f := viewingForm{Date: deref(v.ViewedOn), Review: v.Review, Rating: "none"}
	if v.Rating != nil {
		f.Rating = strconv.FormatInt(*v.Rating, 10)
	}
	return f
}

type viewingInput struct {
	Date   *string
	Rating *int64
	Review string
}

func parseViewingForm(r *http.Request) (viewingForm, viewingInput, error) {
	f := viewingForm{
		Date:   strings.TrimSpace(r.PostFormValue("viewed_on")),
		Rating: r.PostFormValue("rating"),
		Review: strings.TrimSpace(strings.ReplaceAll(r.PostFormValue("review"), "\r\n", "\n")),
	}
	in := viewingInput{Review: f.Review}

	var errs []error
	if f.Date != "" {
		if _, err := time.Parse(dateLayout, f.Date); err != nil {
			errs = append(errs, fmt.Errorf("date %q should be YYYY-MM-DD", f.Date))
		} else {
			in.Date = &f.Date
		}
	}
	switch f.Rating {
	case "", "none":
		f.Rating = "none"
	default:
		n, err := strconv.ParseInt(f.Rating, 10, 64)
		if err != nil || n < minRating || n > maxRating {
			errs = append(errs, fmt.Errorf("rating must be %d–%d stars", minRating, maxRating))
		} else {
			in.Rating = &n
		}
	}

	return f, in, errors.Join(errs...)
}
