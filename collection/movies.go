package collection

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/openrevs/revs/collection/localdb"
)

// Search timeouts: searchTimeout normally, and the shorter
// stockSearchTimeout when the built-in list already has matches to
// show. Variables so tests can shorten them.
var (
	searchTimeout      = 15 * time.Second
	stockSearchTimeout = 4 * time.Second
)

// ---- movies ----

// Movies lists movies and TV series whose title contains query (all
// when empty), with viewing summaries. typ, TypeMovie or TypeTV,
// limits the list to one type; empty lists both.
func (c *Collection) Movies(ctx context.Context, query, typ string) ([]localdb.ListMoviesRow, error) {
	return c.q.ListMovies(ctx, localdb.ListMoviesParams{Query: strings.TrimSpace(query), Type: typ})
}

// MovieSummary is one movie as Movies lists it, with its viewing
// summary.
func (c *Collection) MovieSummary(ctx context.Context, id int64) (localdb.ListMoviesRow, error) {
	m, err := c.Movie(ctx, id)
	if err != nil {
		return localdb.ListMoviesRow{}, err
	}
	rows, err := c.q.ListMovies(ctx, localdb.ListMoviesParams{Query: m.Title, Type: m.Type})
	if err != nil {
		return localdb.ListMoviesRow{}, err
	}
	for _, row := range rows {
		if row.ID == id {
			return row, nil
		}
	}
	return localdb.ListMoviesRow{}, ErrNotFound
}

// AllMovies lists every movie.
func (c *Collection) AllMovies(ctx context.Context) ([]localdb.Movie, error) {
	return c.q.AllMovies(ctx)
}

// Counts returns how many movies, TV series, and viewings there are.
func (c *Collection) Counts(ctx context.Context) (localdb.CountMoviesAndViewingsRow, error) {
	return c.q.CountMoviesAndViewings(ctx)
}

func (c *Collection) Movie(ctx context.Context, id int64) (localdb.Movie, error) {
	m, err := c.q.GetMovie(ctx, id)
	return m, notFound(err)
}

// AddMovie adds a movie or TV series. The same Wikidata item added
// twice is almost surely a mistake, so if one is already present it is
// returned instead with existed set.
func (c *Collection) AddMovie(ctx context.Context, in MovieInput) (m localdb.Movie, existed bool, err error) {
	if in.Wikidata != nil {
		m, err := c.q.FindMovieByWikidata(ctx, localdb.FindMovieByWikidataParams{Type: in.typ(), WikidataID: in.Wikidata})
		if err == nil {
			return m, true, nil
		}
		if err := notFound(err); !errors.Is(err, ErrNotFound) {
			return m, false, err
		}
	}
	m, err = c.q.CreateMovie(ctx, in.createParams())
	return m, false, err
}

// ErrHasSeasons is returned by UpdateMovie for a TV series with season
// or episode viewings that would become a movie, which can't have them.
var ErrHasSeasons = errors.New("this series has viewings of seasons or episodes; a movie can't have those, so edit or delete them first")

// UpdateMovie changes a movie's details, including whether it is a
// movie or a TV series.
func (c *Collection) UpdateMovie(ctx context.Context, id int64, in MovieInput) error {
	m, err := c.Movie(ctx, id)
	if err != nil {
		return err
	}
	if m.Type == TypeTV && in.typ() != TypeTV {
		n, err := c.q.CountSeasonViewings(ctx, id)
		if err != nil {
			return err
		}
		if n > 0 {
			return ErrHasSeasons
		}
	}
	return c.q.UpdateMovie(ctx, in.updateParams(id))
}

// DeleteMovie deletes a movie and its viewings.
func (c *Collection) DeleteMovie(ctx context.Context, id int64) error {
	if _, err := c.Movie(ctx, id); err != nil {
		return err
	}
	return c.q.DeleteMovie(ctx, id)
}

// ---- viewings ----

// Viewings lists a movie's viewings, newest first, undated last. For a
// TV series, that includes its viewings of seasons and episodes.
func (c *Collection) Viewings(ctx context.Context, movieID int64) ([]localdb.Viewing, error) {
	return c.q.ListViewingsForMovie(ctx, movieID)
}

// AllViewings lists every viewing.
func (c *Collection) AllViewings(ctx context.Context) ([]localdb.Viewing, error) {
	return c.q.AllViewings(ctx)
}

func (c *Collection) Viewing(ctx context.Context, id int64) (localdb.Viewing, error) {
	v, err := c.q.GetViewing(ctx, id)
	return v, notFound(err)
}

// AddViewing adds a viewing of a movie. Only a TV series's viewings may
// have a season or episode.
func (c *Collection) AddViewing(ctx context.Context, movieID int64, in ViewingInput) (localdb.Viewing, error) {
	m, err := c.Movie(ctx, movieID)
	if err != nil {
		return localdb.Viewing{}, err
	}
	if err := in.check(m); err != nil {
		return localdb.Viewing{}, err
	}
	return c.q.CreateViewing(ctx, localdb.CreateViewingParams{
		MovieID: movieID, ViewedOn: in.Date, Rating: in.Rating, Review: in.Review, Season: in.Season, Episode: in.Episode,
	})
}

func (c *Collection) UpdateViewing(ctx context.Context, id int64, in ViewingInput) error {
	v, err := c.Viewing(ctx, id)
	if err != nil {
		return err
	}
	m, err := c.Movie(ctx, v.MovieID)
	if err != nil {
		return err
	}
	if err := in.check(m); err != nil {
		return err
	}
	return c.q.UpdateViewing(ctx, localdb.UpdateViewingParams{
		ViewedOn: in.Date, Rating: in.Rating, Review: in.Review, Season: in.Season, Episode: in.Episode, ID: id,
	})
}

// LatestWhole returns the newest of vs (as Viewings orders them) that
// is about the whole item rather than one season or episode: for a
// movie, simply the newest. Its rating is the item's rating.
func LatestWhole(vs []localdb.Viewing) (localdb.Viewing, bool) {
	for _, v := range vs {
		if v.Season == nil {
			return v, true
		}
	}
	return localdb.Viewing{}, false
}

// SetRating is the one-step way to rate a movie or TV series: it sets
// the rating (in half stars) on its latest viewing of the whole item
// (see LatestWhole), keeping its date and review, or, when there is
// none yet, adds one dated today. It reports whether it added a
// viewing.
func (c *Collection) SetRating(ctx context.Context, movieID int64, rating int64) (v localdb.Viewing, added bool, err error) {
	if rating < MinUserRating || rating > MaxRating {
		return v, false, errStars
	}
	vs, err := c.Viewings(ctx, movieID)
	if err != nil {
		return v, false, err
	}
	v, ok := LatestWhole(vs)
	if !ok {
		today := time.Now().Format(DateLayout)
		v, err = c.AddViewing(ctx, movieID, ViewingInput{Date: &today, Rating: &rating})
		return v, err == nil, err
	}
	v.Rating = &rating
	err = c.q.UpdateViewing(ctx, localdb.UpdateViewingParams{
		ViewedOn: v.ViewedOn, Rating: v.Rating, Review: v.Review, Season: v.Season, Episode: v.Episode, ID: v.ID,
	})
	return v, false, err
}

func (c *Collection) DeleteViewing(ctx context.Context, id int64) error {
	if _, err := c.Viewing(ctx, id); err != nil {
		return err
	}
	return c.q.DeleteViewing(ctx, id)
}

// History lists up to limit viewings across all movies, newest first.
func (c *Collection) History(ctx context.Context, limit int64) ([]localdb.HistoryRow, error) {
	return c.q.History(ctx, limit)
}

// ---- search ----

// Search looks up movies and TV series to add. Matches from the built-in stock list
// come first, followed by any other Wikidata hits. A Wikidata failure
// (e.g. when offline) is returned as a message to show alongside
// whatever the stock list found, never as an error.
func (c *Collection) Search(ctx context.Context, q string) ([]SearchResult, string) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, ""
	}
	results := searchStock(q)

	timeout := searchTimeout
	if len(results) > 0 {
		timeout = stockSearchTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	remote, err := c.wikidata.Search(ctx, q)
	if err != nil {
		log.Printf("wikidata search %q: %v", q, err)
		var uerr *url.Error
		if errors.As(err, &uerr) || errors.Is(err, context.DeadlineExceeded) {
			return results, "Couldn't reach Wikidata; you may be offline."
		}
		return results, err.Error()
	}
	for _, r := range remote {
		if !slices.ContainsFunc(results, func(s SearchResult) bool { return s.ID == r.ID }) {
			results = append(results, r)
		}
	}
	return results, ""
}

// ---- others' reviews ----

// OtherReview is someone else's review of a movie or TV series (or one
// of its seasons or episodes), from a server (ServerID).
type OtherReview struct {
	localdb.MatchingReviewsRow
}

// OthersReviews collects pulled reviews of m from every server, with
// reviews seen on several servers listed once. Only items of m's type
// match.
func (c *Collection) OthersReviews(ctx context.Context, m localdb.Movie) ([]OtherReview, error) {
	rows, err := c.q.MatchingReviews(ctx, localdb.MatchingReviewsParams{
		ExcludeUser: c.UserID(ctx),
		Type:        m.Type,
		WikidataID:  m.WikidataID,
		ImdbID:      m.ImdbID,
		TmdbID:      m.TmdbID,
		Name:        m.Title,
		Year:        m.Year,
	})
	if err != nil {
		return nil, err
	}

	var out []OtherReview
	seen := map[string]bool{}
	for _, row := range rows {
		k := fmt.Sprintf("%s|%s|%d|%s|%s", row.Userid, Deref(row.ViewedOn), Deref(row.Rating), PartName(row.Season, row.Episode), row.Comment)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, OtherReview{row})
	}
	return out, nil
}
