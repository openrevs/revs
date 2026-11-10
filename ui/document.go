package ui

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/openrevs/revs"
	"github.com/openrevs/revs/client"
	"github.com/openrevs/revs/keys"
	"github.com/openrevs/revs/ui/localdb"
	"github.com/tidwall/gjson"
)

const (
	minRating = 0
	maxRating = 5

	titleTypeMovie = "movie"

	// Keys of a title line's "refs" object.
	refIMDB     = "imdb"
	refTMDB     = "tmdb"
	refWikidata = "wikidata"
)

// buildContent renders the whole local collection as the unsigned data
// lines of a ratingset: one user line, then one line per movie with its
// viewings as reviews. This is exactly the input client.Push expects.
// Lines are deterministic for unchanged data, and byte-identical
// duplicates (which sjsonl cannot sign) are dropped — they carry no
// information.
func buildContent(ctx context.Context, q *localdb.Queries, userID string) ([]byte, error) {
	movies, err := q.AllMovies(ctx)
	if err != nil {
		return nil, err
	}
	viewings, err := q.AllViewings(ctx)
	if err != nil {
		return nil, err
	}

	byMovie := map[int64][]localdb.Viewing{}
	for _, v := range viewings {
		byMovie[v.MovieID] = append(byMovie[v.MovieID], v)
	}

	user, err := json.Marshal(revs.User2{ID: userID, MinRating: minRating, MaxRating: maxRating})
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	buf.Write(user)
	buf.WriteByte('\n')

	seen := map[string]bool{}
	for _, m := range movies {
		line, err := json.Marshal(titleFromMovie(m, byMovie[m.ID]))
		if err != nil {
			return nil, err
		}
		if seen[string(line)] {
			continue
		}
		seen[string(line)] = true
		buf.Write(line)
		buf.WriteByte('\n')
	}

	return buf.Bytes(), nil
}

func titleFromMovie(m localdb.Movie, viewings []localdb.Viewing) revs.Title2 {
	typ := titleTypeMovie
	t := revs.Title2{Type: &typ, Name: m.Title}
	if m.Year != nil {
		y := int(*m.Year)
		t.Year = &y
	}

	refs := map[string]string{}
	for k, v := range map[string]*string{refIMDB: m.ImdbID, refTMDB: m.TmdbID, refWikidata: m.WikidataID} {
		if v != nil && *v != "" {
			refs[k] = *v
		}
	}
	if len(refs) > 0 {
		t.Refs = refs
	}

	// Oldest first, undated first; AllViewings already orders by date then id.
	for _, v := range viewings {
		r := revs.Review2{Date: v.ViewedOn}
		if v.Rating != nil {
			n := int(*v.Rating)
			r.Rating = &n
		}
		if v.Review != "" {
			c := v.Review
			r.Comment = &c
		}
		t.Reviews = append(t.Reviews, r)
	}

	return t
}

// signExport signs the collection for export as the author's newest
// version, the same one a push would upload: its revision is bumped only
// when content has changed, and never below a revision already pushed to
// a server (so an exported file supersedes what servers hold).
func (a *App) signExport(ctx context.Context, key ed25519.PrivateKey) ([]byte, int, error) {
	uid := keys.UserID(key.Public().(ed25519.PublicKey))
	content, err := buildContent(ctx, a.q, uid)
	if err != nil {
		return nil, 0, err
	}

	// Before the first version, account for revisions assigned before
	// the version history existed: the last export, and pushes.
	known := a.settingInt(ctx, settingDocRev)
	servers, err := a.q.ListServers(ctx)
	if err != nil {
		return nil, 0, err
	}
	for _, s := range servers {
		if s.LastPushRev != nil {
			known = max(known, int(*s.LastPushRev))
		}
	}
	_, err = a.versions.Latest(uid)
	switch {
	case errors.Is(err, client.ErrNotFound):
		if err := a.versions.NoteRev(uid, known, ""); err != nil {
			return nil, 0, err
		}
	case err != nil:
		return nil, 0, err
	}

	doc, err := a.versions.Commit(key, content)
	if err != nil {
		return nil, 0, err
	}
	return doc.Data, doc.Rev, nil
}

// parsedDoc is a ratingset read for import.
type parsedDoc struct {
	UserID string // empty for an unsigned file without a user line
	Signed bool
	Titles []revs.Title2
}

// parseRatingset reads a ratingset for import. A signed sjsonl document
// is fully verified (signature, and user id matching the signing key);
// an unsigned file of data lines — such as a `revs tui` draft.jsonl — is
// accepted as is.
func parseRatingset(data []byte) (*parsedDoc, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, errors.New("file is empty")
	}

	first, _, _ := bytes.Cut(data, []byte("\n"))
	pd := &parsedDoc{}
	lines := bytes.Split(data, []byte("\n"))

	if gjson.GetBytes(first, "sjsonl").Exists() {
		user, _, err := revs.ParseRatingset(append(data, '\n'))
		if err != nil {
			return nil, fmt.Errorf("verifying signed document: %w", err)
		}
		pd.UserID = user.ID
		pd.Signed = true
		lines = lines[1:]
	}

	for i, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if !json.Valid(line) {
			return nil, fmt.Errorf("line %d: invalid JSON", i+1)
		}
		if id := gjson.GetBytes(line, "id"); id.Exists() {
			if pd.UserID == "" {
				pd.UserID = id.String()
			}
			continue
		}
		var t revs.Title2
		if err := json.Unmarshal(line, &t); err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		pd.Titles = append(pd.Titles, t)
	}

	return pd, nil
}

// ImportStats reports what a merge did.
type ImportStats struct {
	MoviesAdded    int
	MoviesMatched  int
	ViewingsAdded  int
	ViewingsKept   int
	SkippedNonFilm int
}

func (s ImportStats) String() string {
	msg := fmt.Sprintf("Imported %d new movies (%d already present) and %d new viewings (%d already present).",
		s.MoviesAdded, s.MoviesMatched, s.ViewingsAdded, s.ViewingsKept)
	if s.SkippedNonFilm > 0 {
		msg += fmt.Sprintf(" Skipped %d non-movie titles.", s.SkippedNonFilm)
	}
	return msg
}

// mergeTitles adds titles and reviews to the local collection without
// removing or overwriting anything. A title matches an existing movie
// by Wikidata, IMDb, or TMDB id, else by title and year; missing ids
// are filled in on a match. A review is added unless the movie already
// has a viewing with the same date, rating, and text.
func (a *App) mergeTitles(ctx context.Context, titles []revs.Title2) (ImportStats, error) {
	var stats ImportStats

	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return stats, err
	}
	defer tx.Rollback()
	q := a.q.WithTx(tx)

	for _, t := range titles {
		if t.Type != nil && *t.Type != "" && !strings.EqualFold(*t.Type, titleTypeMovie) {
			stats.SkippedNonFilm++
			continue
		}
		name := strings.TrimSpace(t.Name)
		if name == "" {
			continue
		}

		in := movieInput{Title: name}
		if t.Year != nil {
			y := int64(*t.Year)
			in.Year = &y
		}
		for k, v := range t.Refs {
			switch strings.ToLower(k) {
			case refIMDB:
				in.IMDB = normalizeIMDB(v)
			case refTMDB:
				in.TMDB = normalizeTMDB(v)
			case refWikidata:
				in.Wikidata = normalizeWikidata(v)
			}
		}

		m, found, err := findMovie(ctx, q, in)
		if err != nil {
			return stats, err
		}
		if found {
			stats.MoviesMatched++
			upd := movieInput{
				Title:    m.Title,
				Year:     cmpOr(m.Year, in.Year),
				IMDB:     cmpOr(m.ImdbID, in.IMDB),
				TMDB:     cmpOr(m.TmdbID, in.TMDB),
				Wikidata: cmpOr(m.WikidataID, in.Wikidata),
			}
			if err := q.UpdateMovie(ctx, upd.updateParams(m.ID)); err != nil {
				return stats, err
			}
		} else {
			stats.MoviesAdded++
			if m, err = q.CreateMovie(ctx, in.createParams()); err != nil {
				return stats, err
			}
		}

		existing, err := q.ListViewingsForMovie(ctx, m.ID)
		if err != nil {
			return stats, err
		}
		for _, r := range t.Reviews {
			v := localdb.CreateViewingParams{MovieID: m.ID}
			if r.Date != nil && *r.Date != "" {
				v.ViewedOn = r.Date
			}
			if r.Rating != nil && *r.Rating >= minRating && *r.Rating <= maxRating {
				n := int64(*r.Rating)
				v.Rating = &n
			}
			if r.Comment != nil {
				v.Review = *r.Comment
			}
			if slices.ContainsFunc(existing, func(e localdb.Viewing) bool { return sameViewing(e, v) }) {
				stats.ViewingsKept++
				continue
			}
			created, err := q.CreateViewing(ctx, v)
			if err != nil {
				return stats, err
			}
			existing = append(existing, created)
			stats.ViewingsAdded++
		}
	}

	return stats, tx.Commit()
}

func sameViewing(e localdb.Viewing, v localdb.CreateViewingParams) bool {
	return ptrEq(e.ViewedOn, v.ViewedOn) && ptrEq(e.Rating, v.Rating) && e.Review == v.Review
}

// findMovie looks up an existing movie for in by external ids, then by
// title and year.
func findMovie(ctx context.Context, q *localdb.Queries, in movieInput) (localdb.Movie, bool, error) {
	lookups := []func() (localdb.Movie, error){}
	if in.Wikidata != nil {
		lookups = append(lookups, func() (localdb.Movie, error) { return q.FindMovieByWikidata(ctx, in.Wikidata) })
	}
	if in.IMDB != nil {
		lookups = append(lookups, func() (localdb.Movie, error) { return q.FindMovieByIMDB(ctx, in.IMDB) })
	}
	if in.TMDB != nil {
		lookups = append(lookups, func() (localdb.Movie, error) { return q.FindMovieByTMDB(ctx, in.TMDB) })
	}
	lookups = append(lookups, func() (localdb.Movie, error) {
		return q.FindMovieByTitleYear(ctx, localdb.FindMovieByTitleYearParams{Title: in.Title, Year: in.Year})
	})

	for _, lookup := range lookups {
		m, err := lookup()
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return m, false, err
		}
		return m, true, nil
	}
	return localdb.Movie{}, false, nil
}

func ptrEq[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// cmpOr returns a unless it is nil or empty, else b.
func cmpOr[T comparable](a, b *T) *T {
	var zero T
	if a != nil && *a != zero {
		return a
	}
	return b
}
