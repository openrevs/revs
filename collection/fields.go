package collection

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/openrevs/revs/collection/localdb"
	"github.com/openrevs/revs/ratingset"
)

// DateLayout is the format of a viewing date given to the day. A date
// may also be just a month (YYYY-MM) or a year (YYYY); see
// ratingset.ValidDate.
const DateLayout = ratingset.DateDay

var (
	imdbRe     = regexp.MustCompile(`tt\d{5,}`)
	wikidataRe = regexp.MustCompile(`(?i)\bQ\d+\b`)
	tmdbURLRe  = regexp.MustCompile(`themoviedb\.org/(movie|tv)/(\d+)`)
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

// normalizeTMDB extracts a TMDB id ("680") for an item of type typ
// from an id, a slug such as "680-pulp-fiction", or a URL, or returns
// nil. TMDB numbers movies and TV series separately, so a URL of the
// other kind ("/tv/60622" for a movie) is rejected.
func normalizeTMDB(typ, s string) *string {
	s = strings.TrimSpace(s)
	if m := tmdbURLRe.FindStringSubmatch(s); m != nil {
		if m[1] != typ {
			return nil
		}
		return &m[2]
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

// Item types, as stored in a movie's Type: ratingset item types.
const (
	TypeMovie = ratingset.ItemTypeMovie
	TypeTV    = ratingset.ItemTypeTV
)

// itemType returns the stored form of a ratingset item type, and
// whether it is one the collection supports. Types compare without
// regard to case.
func itemType(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	return s, s == TypeMovie || s == TypeTV
}

// MovieInput is a validated movie or TV series (see Type).
type MovieInput struct {
	Type     string // TypeMovie or TypeTV; empty means TypeMovie
	Title    string
	Year     *int64
	IMDB     *string
	TMDB     *string
	Wikidata *string
}

func (in MovieInput) typ() string {
	if in.Type == "" {
		return TypeMovie
	}
	return in.Type
}

func (in MovieInput) createParams() localdb.CreateMovieParams {
	return localdb.CreateMovieParams{Type: in.typ(), Title: in.Title, Year: in.Year, ImdbID: in.IMDB, TmdbID: in.TMDB, WikidataID: in.Wikidata}
}

func (in MovieInput) updateParams(id int64) localdb.UpdateMovieParams {
	return localdb.UpdateMovieParams{Type: in.typ(), Title: in.Title, Year: in.Year, ImdbID: in.IMDB, TmdbID: in.TMDB, WikidataID: in.Wikidata, ID: id}
}

// MovieFields is a movie or TV series as the user typed it: free text
// that Parse validates. Ids may be given as ids or as URLs. Type is
// TypeMovie or TypeTV; empty means TypeMovie.
type MovieFields struct {
	Type, Title, Year, IMDB, TMDB, Wikidata string
}

// MovieFieldsFrom returns the fields for editing an existing movie.
func MovieFieldsFrom(m localdb.Movie) MovieFields {
	f := MovieFields{Type: m.Type, Title: m.Title, IMDB: Deref(m.ImdbID), TMDB: Deref(m.TmdbID), Wikidata: Deref(m.WikidataID)}
	if m.Year != nil {
		f.Year = strconv.FormatInt(*m.Year, 10)
	}
	return f
}

// MovieFieldsFromResult returns the fields for a search result.
func MovieFieldsFromResult(r SearchResult) MovieFields {
	f := MovieFields{Type: r.Type, Title: r.Title, IMDB: Deref(r.IMDB), TMDB: Deref(r.TMDB), Wikidata: r.ID}
	if r.Year != nil {
		f.Year = strconv.FormatInt(*r.Year, 10)
	}
	return f
}

// Parse trims and validates the fields, reporting every problem found.
func (f *MovieFields) Parse() (MovieInput, error) {
	for _, s := range []*string{&f.Type, &f.Title, &f.Year, &f.IMDB, &f.TMDB, &f.Wikidata} {
		*s = strings.TrimSpace(*s)
	}
	in := MovieInput{Title: f.Title, Type: TypeMovie}

	var errs []error
	if f.Type != "" {
		typ, ok := itemType(f.Type)
		if !ok {
			errs = append(errs, fmt.Errorf("type %q should be %q or %q", f.Type, TypeMovie, TypeTV))
		} else {
			f.Type, in.Type = typ, typ
		}
	}
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
			errs = append(errs, fmt.Errorf("IMDb ID %q should look like tt0110912", f.IMDB))
		}
	}
	if f.TMDB != "" {
		if in.TMDB = normalizeTMDB(in.Type, f.TMDB); in.TMDB == nil {
			what := "movie"
			if in.Type == TypeTV {
				what = "TV series"
			}
			errs = append(errs, fmt.Errorf("TMDB ID %q should be a number like 680, or a TMDB %s URL", f.TMDB, what))
		}
	}
	if f.Wikidata != "" {
		if in.Wikidata = normalizeWikidata(f.Wikidata); in.Wikidata == nil {
			errs = append(errs, fmt.Errorf("Wikidata ID %q should look like Q104123", f.Wikidata))
		}
	}

	return in, errors.Join(errs...)
}

// ViewingInput is a validated viewing. Season and Episode, for a TV
// series only, say which part of it the viewing is about; with
// neither, it is about the whole item.
type ViewingInput struct {
	Date    *string
	Rating  *int64
	Review  string
	Season  *int64
	Episode *int64
}

// ViewingFields is a viewing as the user typed it. Rating is a number
// of stars in steps of ½ ("3.5"), or "" or "none" for unrated. Type is
// the type of the viewed item (empty means TypeMovie): Season and
// Episode may be given only for TypeTV.
type ViewingFields struct {
	Type                 string
	Date, Rating, Review string
	Season, Episode      string
}

// TV reports whether the fields are for a TV series's viewing.
func (f ViewingFields) TV() bool { return f.Type == TypeTV }

// NewViewingFields returns the fields for a new viewing of an item of
// type typ: undated and unrated.
func NewViewingFields(typ string) ViewingFields {
	return ViewingFields{Type: typ, Rating: "none"}
}

// ViewingFieldsFrom returns the fields for editing an existing viewing
// of an item of type typ.
func ViewingFieldsFrom(typ string, v localdb.Viewing) ViewingFields {
	f := ViewingFields{Type: typ, Date: Deref(v.ViewedOn), Review: v.Review, Rating: "none"}
	if v.Rating != nil {
		f.Rating = FormatStars(*v.Rating)
	}
	if v.Season != nil {
		f.Season = strconv.FormatInt(*v.Season, 10)
	}
	if v.Episode != nil {
		f.Episode = strconv.FormatInt(*v.Episode, 10)
	}
	return f
}

// Parse trims and validates the fields, reporting every problem found.
func (f *ViewingFields) Parse() (ViewingInput, error) {
	f.Date = strings.TrimSpace(f.Date)
	f.Rating = strings.TrimSpace(f.Rating)
	f.Review = strings.TrimSpace(strings.ReplaceAll(f.Review, "\r\n", "\n"))
	f.Season = strings.TrimSpace(f.Season)
	f.Episode = strings.TrimSpace(f.Episode)
	in := ViewingInput{Review: f.Review}

	var errs []error
	if f.Date != "" {
		if !ratingset.ValidDate(f.Date) {
			errs = append(errs, fmt.Errorf("date %q should be YYYY-MM-DD, or YYYY-MM or YYYY if you don't know the day", f.Date))
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
	if (f.Season != "" || f.Episode != "") && !f.TV() {
		errs = append(errs, errNotTV)
	} else {
		if f.Season != "" {
			n, err := strconv.ParseInt(f.Season, 10, 64)
			if err != nil || n < 0 {
				errs = append(errs, fmt.Errorf("season %q should be a number: 1 or more, or 0 for specials", f.Season))
			} else {
				in.Season = &n
			}
		}
		if f.Episode != "" {
			n, err := strconv.ParseInt(f.Episode, 10, 64)
			switch {
			case err != nil || n < 1:
				errs = append(errs, fmt.Errorf("episode %q should be a number, 1 or more", f.Episode))
			case f.Season == "":
				errs = append(errs, errors.New("an episode needs a season"))
			default:
				in.Episode = &n
			}
		}
	}

	return in, errors.Join(errs...)
}

// errNotTV is the error for a season or episode given for a movie.
var errNotTV = errors.New("only a TV series's viewings can have a season or episode")

// check reports whether in is a valid viewing of m, which it is unless
// it names a season or episode of something other than a TV series.
func (in ViewingInput) check(m localdb.Movie) error {
	if (in.Season != nil || in.Episode != nil) && m.Type != TypeTV {
		return errNotTV
	}
	if in.Episode != nil && in.Season == nil {
		return errors.New("an episode needs a season")
	}
	return nil
}
