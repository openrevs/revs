package collection

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
	"time"

	"github.com/openrevs/revs/client"
	"github.com/openrevs/revs/collection/localdb"
	"github.com/openrevs/revs/keys"
	"github.com/openrevs/revs/ratingset"
	"github.com/tidwall/gjson"
)

const (
	// Keys of a movie or tv item's "refs" object. For a tv item they
	// name the series as a whole.
	refIMDB     = "imdb"
	refTMDB     = "tmdb"
	refWikidata = "wikidata"
)

// buildContent renders the whole local collection as the unsigned data
// lines of a ratingset: one author line, then one item line per movie
// or TV series with its viewings as entries. This is exactly the input client.Push expects.
// Lines are deterministic for unchanged data, and byte-identical
// duplicates (which sjsonl cannot sign) are dropped — they carry no
// information.
func (c *Collection) buildContent(ctx context.Context, userID string) ([]byte, error) {
	movies, err := c.q.AllMovies(ctx)
	if err != nil {
		return nil, err
	}
	viewings, err := c.q.AllViewings(ctx)
	if err != nil {
		return nil, err
	}

	byMovie := map[int64][]localdb.Viewing{}
	for _, v := range viewings {
		byMovie[v.MovieID] = append(byMovie[v.MovieID], v)
	}

	user, err := ratingset.EncodeLine(ratingset.Author{ID: userID, MinRating: MinRating, MaxRating: MaxRating})
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	buf.Write(user)
	buf.WriteByte('\n')

	seen := map[string]bool{}
	for _, m := range movies {
		line, err := ratingset.EncodeLine(itemFromMovie(m, byMovie[m.ID]).Normalize())
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

func itemFromMovie(m localdb.Movie, viewings []localdb.Viewing) ratingset.Item {
	t := ratingset.Item{Type: m.Type, Title: m.Title}
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

	// Oldest first, undated first; AllViewings already orders by date
	// then id, which for a series means by date, not season.
	for _, v := range viewings {
		e := ratingset.Entry{Date: v.ViewedOn, Season: intOf(v.Season), Episode: intOf(v.Episode)}
		if v.Rating != nil {
			n := int(*v.Rating)
			e.Rating = &n
		}
		if v.Review != "" {
			r := v.Review
			e.Review = &r
		}
		t.Entries = append(t.Entries, e)
	}

	return t
}

func intOf(p *int64) *int {
	if p == nil {
		return nil
	}
	n := int(*p)
	return &n
}

func int64Of(p *int) *int64 {
	if p == nil {
		return nil
	}
	n := int64(*p)
	return &n
}

// Export signs the collection as the author's newest version, the same
// one a push would upload: its revision is bumped only when content has
// changed, and never below a revision already pushed to a server (so an
// exported file supersedes what servers hold).
func (c *Collection) Export(ctx context.Context) (data []byte, rev int, err error) {
	key, err := c.signingKey(ctx)
	if err != nil {
		return nil, 0, err
	}
	uid := keys.UserID(key.Public().(ed25519.PublicKey))
	content, err := c.buildContent(ctx, uid)
	if err != nil {
		return nil, 0, err
	}
	err = c.syncLocked(ctx, func() error {
		data, rev, err = c.commitExport(ctx, key, uid, content)
		return err
	})
	return data, rev, err
}

// commitExport commits content to the version history. Callers hold
// the sync lock.
func (c *Collection) commitExport(ctx context.Context, key ed25519.PrivateKey, uid string, content []byte) ([]byte, int, error) {
	// Before the first version, account for revisions assigned before
	// the version history existed: the last export, and pushes.
	known := c.settingInt(ctx, settingDocRev)
	servers, err := c.q.ListServers(ctx)
	if err != nil {
		return nil, 0, err
	}
	for _, s := range servers {
		if s.LastPushRev != nil {
			known = max(known, int(*s.LastPushRev))
		}
	}
	_, err = c.versions.Latest(uid)
	switch {
	case errors.Is(err, client.ErrNotFound):
		if err := c.versions.NoteRev(uid, known, ""); err != nil {
			return nil, 0, err
		}
	case err != nil:
		return nil, 0, err
	}

	doc, err := c.versions.Commit(key, content)
	if err != nil {
		return nil, 0, err
	}
	return doc.Data, doc.Rev, nil
}

// parsedDoc is a ratingset read for import.
type parsedDoc struct {
	UserID string // empty for an unsigned file without an author line
	Signed bool
	Items  []ratingset.Item
	// Invalid counts item lines skipped as invalid.
	Invalid int
}

// parseRatingset reads a ratingset for import. A signed sjsonl document
// is fully verified (signature, one author line with a valid rating
// scale, and an author id matching the signing key); an unsigned file
// of data lines is accepted as is, though an author line in it must
// still declare a valid scale. Item lines that are invalid under the
// ratingset spec are skipped and counted. Ratings are converted from
// the document's scale to half stars; an unsigned file without an
// author line is taken to use whole stars (0–5).
func parseRatingset(data []byte) (*parsedDoc, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, errors.New("file is empty")
	}

	first, _, _ := bytes.Cut(data, []byte("\n"))
	pd := &parsedDoc{}
	lines := bytes.Split(data, []byte("\n"))

	if gjson.GetBytes(first, "sjsonl").Exists() {
		user, _, err := ratingset.ParseRatingset(append(data, '\n'))
		if err != nil {
			return nil, fmt.Errorf("verifying signed document: %w", err)
		}
		pd.UserID = user.ID
		pd.Signed = true
		lines = lines[1:]
	}

	// Find the scale first: the author line may come anywhere.
	scale := ratingScale{legacyMin, legacyMax}
	authorSeen := false
	for i, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if !json.Valid(line) {
			return nil, fmt.Errorf("line %d: invalid JSON", i+1)
		}
		if !gjson.GetBytes(line, "id").Exists() {
			continue
		}
		if authorSeen {
			return nil, errors.New("multiple author lines")
		}
		authorSeen = true
		s, err := scaleOf(line)
		if err != nil {
			return nil, err
		}
		scale = s
		pd.UserID = gjson.GetBytes(line, "id").String()
	}

	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || gjson.GetBytes(line, "id").Exists() {
			continue
		}
		it, ok := readItem(line, scale)
		if !ok {
			pd.Invalid++
			continue
		}
		// Convert ratings to half stars.
		for i, e := range it.Entries {
			if n := scale.scale(e.Rating); n != nil {
				v := int(*n)
				it.Entries[i].Rating = &v
			}
		}
		pd.Items = append(pd.Items, it)
	}

	return pd, nil
}

// ImportStats reports what a merge did. Movies counts movies and TV
// series alike; SkippedNonFilm counts items of other types.
type ImportStats struct {
	MoviesAdded    int
	MoviesMatched  int
	ViewingsAdded  int
	ViewingsKept   int
	SkippedNonFilm int
	SkippedInvalid int
}

// ErrUnreadableFile is returned by Import for a file that isn't a valid
// ratingset. errors.Unwrap on the returned error gives the reason.
var ErrUnreadableFile = errors.New("could not read that file")

type unreadableFile struct{ err error }

func (e unreadableFile) Error() string        { return ErrUnreadableFile.Error() + ": " + e.err.Error() }
func (e unreadableFile) Unwrap() error        { return e.err }
func (e unreadableFile) Is(target error) bool { return target == ErrUnreadableFile }

// ExportFilename is a file name for an export of revision rev.
func ExportFilename(rev int) string {
	return fmt.Sprintf("revs-%s-r%d.sjsonl", time.Now().Format("20060102"), rev)
}

// ImportResult reports what Import did.
type ImportResult struct {
	ImportStats
	// ForeignUser is set when the file was another user's collection
	// than the signing key's.
	ForeignUser bool
}

func (r ImportResult) String() string {
	msg := r.ImportStats.String()
	if r.ForeignUser {
		msg += " Note: the file belongs to a different user ID than your key; its reviews are now yours."
	}
	return msg
}

// Import merges a ratingset file into the collection: a signed sjsonl
// document, which must verify, or unsigned data lines. See mergeItems.
func (c *Collection) Import(ctx context.Context, data []byte) (ImportResult, error) {
	pd, err := parseRatingset(data)
	if err != nil {
		return ImportResult{}, unreadableFile{err}
	}
	stats, err := c.mergeItems(ctx, pd.Items)
	if err != nil {
		return ImportResult{}, err
	}
	stats.SkippedInvalid += pd.Invalid
	uid := c.UserID(ctx)
	return ImportResult{ImportStats: stats, ForeignUser: pd.UserID != "" && uid != "" && pd.UserID != uid}, nil
}

func (s ImportStats) String() string {
	msg := fmt.Sprintf("Imported %d new titles (%d already present) and %d new viewings (%d already present).",
		s.MoviesAdded, s.MoviesMatched, s.ViewingsAdded, s.ViewingsKept)
	if s.SkippedNonFilm > 0 {
		msg += fmt.Sprintf(" Skipped %d titles that are neither movies nor TV series.", s.SkippedNonFilm)
	}
	if s.SkippedInvalid > 0 {
		msg += fmt.Sprintf(" Skipped %d invalid titles.", s.SkippedInvalid)
	}
	return msg
}

// mergeItems adds movie and tv items and their entries to the local
// collection without removing or overwriting anything. Items are expected to be
// valid (see ratingset.Item.Validate), with ratings in half stars. An item
// matches an existing movie as findMovie describes; missing ids are
// filled in on a match. An entry is added unless the movie already has
// a viewing with the same date, rating, text, season, and episode.
func (c *Collection) mergeItems(ctx context.Context, items []ratingset.Item) (ImportStats, error) {
	var stats ImportStats

	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return stats, err
	}
	defer tx.Rollback()
	q := c.q.WithTx(tx)

	for _, t := range items {
		typ, ok := itemType(t.Type)
		if !ok {
			stats.SkippedNonFilm++
			continue
		}
		name := strings.TrimSpace(t.Title)
		if name == "" {
			stats.SkippedInvalid++
			continue
		}

		in := MovieInput{Type: typ, Title: name}
		if t.Year != nil {
			y := int64(*t.Year)
			in.Year = &y
		}
		for k, v := range t.Refs {
			switch strings.ToLower(k) {
			case refIMDB:
				in.IMDB = normalizeIMDB(v)
			case refTMDB:
				in.TMDB = normalizeTMDB(typ, v)
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
			upd := MovieInput{
				Type:     m.Type,
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
		for _, r := range t.Entries {
			v := localdb.CreateViewingParams{MovieID: m.ID, Season: int64Of(r.Season), Episode: int64Of(r.Episode)}
			if r.Date != nil && *r.Date != "" {
				v.ViewedOn = r.Date
			}
			if r.Rating != nil && *r.Rating >= MinRating && *r.Rating <= MaxRating {
				n := int64(*r.Rating)
				v.Rating = &n
			}
			if r.Review != nil {
				v.Review = *r.Review
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
	return ptrEq(e.ViewedOn, v.ViewedOn) && ptrEq(e.Rating, v.Rating) && e.Review == v.Review &&
		ptrEq(e.Season, v.Season) && ptrEq(e.Episode, v.Episode)
}

// findMovie looks up an existing movie of in's type for in by external
// ids (in the spec's preference order: IMDb, Wikidata, TMDB), then by
// title and year. Items of different types never match. Following the ratingset spec's matching rule, a candidate whose
// ids conflict with in's (a shared ref key with a different value) is
// never a match: the two are kept apart.
func findMovie(ctx context.Context, q *localdb.Queries, in MovieInput) (localdb.Movie, bool, error) {
	typ := in.typ()
	lookups := []func() (localdb.Movie, error){}
	if in.IMDB != nil {
		lookups = append(lookups, func() (localdb.Movie, error) {
			return q.FindMovieByIMDB(ctx, localdb.FindMovieByIMDBParams{Type: typ, ImdbID: in.IMDB})
		})
	}
	if in.Wikidata != nil {
		lookups = append(lookups, func() (localdb.Movie, error) {
			return q.FindMovieByWikidata(ctx, localdb.FindMovieByWikidataParams{Type: typ, WikidataID: in.Wikidata})
		})
	}
	if in.TMDB != nil {
		lookups = append(lookups, func() (localdb.Movie, error) {
			return q.FindMovieByTMDB(ctx, localdb.FindMovieByTMDBParams{Type: typ, TmdbID: in.TMDB})
		})
	}
	lookups = append(lookups, func() (localdb.Movie, error) {
		return q.FindMovieByTitleYear(ctx, localdb.FindMovieByTitleYearParams{Type: typ, Title: in.Title, Year: in.Year})
	})

	for _, lookup := range lookups {
		m, err := lookup()
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return m, false, err
		}
		if refsConflict(m, in) {
			continue
		}
		return m, true, nil
	}
	return localdb.Movie{}, false, nil
}

// refsConflict reports whether m and in have a ref key in common whose
// values differ.
func refsConflict(m localdb.Movie, in MovieInput) bool {
	differ := func(a, b *string) bool {
		return a != nil && b != nil && *a != "" && *b != "" && *a != *b
	}
	return differ(m.ImdbID, in.IMDB) || differ(m.WikidataID, in.Wikidata) || differ(m.TmdbID, in.TMDB)
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
