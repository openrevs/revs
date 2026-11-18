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

// Movies lists movies whose title contains query (all when empty),
// with viewing summaries.
func (c *Collection) Movies(ctx context.Context, query string) ([]localdb.ListMoviesRow, error) {
	return c.q.ListMovies(ctx, strings.TrimSpace(query))
}

// MovieSummary is one movie as Movies lists it, with its viewing
// summary.
func (c *Collection) MovieSummary(ctx context.Context, id int64) (localdb.ListMoviesRow, error) {
	m, err := c.Movie(ctx, id)
	if err != nil {
		return localdb.ListMoviesRow{}, err
	}
	rows, err := c.q.ListMovies(ctx, m.Title)
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

// Counts returns how many movies and viewings there are.
func (c *Collection) Counts(ctx context.Context) (localdb.CountMoviesAndViewingsRow, error) {
	return c.q.CountMoviesAndViewings(ctx)
}

func (c *Collection) Movie(ctx context.Context, id int64) (localdb.Movie, error) {
	m, err := c.q.GetMovie(ctx, id)
	return m, notFound(err)
}

// AddMovie adds a movie. The same Wikidata item added twice is almost
// surely a mistake, so if one is already present it is returned instead
// with existed set.
func (c *Collection) AddMovie(ctx context.Context, in MovieInput) (m localdb.Movie, existed bool, err error) {
	if in.Wikidata != nil {
		m, err := c.q.FindMovieByWikidata(ctx, in.Wikidata)
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

func (c *Collection) UpdateMovie(ctx context.Context, id int64, in MovieInput) error {
	if _, err := c.Movie(ctx, id); err != nil {
		return err
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

// Viewings lists a movie's viewings, newest first, undated last.
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

func (c *Collection) AddViewing(ctx context.Context, movieID int64, in ViewingInput) (localdb.Viewing, error) {
	if _, err := c.Movie(ctx, movieID); err != nil {
		return localdb.Viewing{}, err
	}
	return c.q.CreateViewing(ctx, localdb.CreateViewingParams{
		MovieID: movieID, ViewedOn: in.Date, Rating: in.Rating, Review: in.Review,
	})
}

func (c *Collection) UpdateViewing(ctx context.Context, id int64, in ViewingInput) error {
	if _, err := c.Viewing(ctx, id); err != nil {
		return err
	}
	return c.q.UpdateViewing(ctx, localdb.UpdateViewingParams{
		ViewedOn: in.Date, Rating: in.Rating, Review: in.Review, ID: id,
	})
}

// SetRating is the one-step way to rate a movie: it sets the rating
// (in half stars) on the movie's latest viewing, keeping its date and
// review, or, when the movie has no viewings yet, adds one dated today.
// It reports whether it added a viewing.
func (c *Collection) SetRating(ctx context.Context, movieID int64, rating int64) (v localdb.Viewing, added bool, err error) {
	if rating < MinUserRating || rating > MaxRating {
		return v, false, errStars
	}
	vs, err := c.Viewings(ctx, movieID)
	if err != nil {
		return v, false, err
	}
	if len(vs) == 0 {
		today := time.Now().Format(DateLayout)
		v, err = c.AddViewing(ctx, movieID, ViewingInput{Date: &today, Rating: &rating})
		return v, err == nil, err
	}
	v = vs[0] // newest first
	v.Rating = &rating
	err = c.q.UpdateViewing(ctx, localdb.UpdateViewingParams{
		ViewedOn: v.ViewedOn, Rating: v.Rating, Review: v.Review, ID: v.ID,
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

// Search looks up movies to add. Matches from the built-in stock list
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

// OtherReview is someone else's review of a movie, from a server
// (ServerID).
type OtherReview struct {
	localdb.MatchingReviewsRow
}

// OthersReviews collects pulled reviews of m from every server, with
// reviews seen on several servers listed once.
func (c *Collection) OthersReviews(ctx context.Context, m localdb.Movie) ([]OtherReview, error) {
	rows, err := c.q.MatchingReviews(ctx, localdb.MatchingReviewsParams{
		ExcludeUser: c.UserID(ctx),
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
		k := fmt.Sprintf("%s|%s|%d|%s", row.Userid, Deref(row.ViewedOn), Deref(row.Rating), row.Comment)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, OtherReview{row})
	}
	return out, nil
}
