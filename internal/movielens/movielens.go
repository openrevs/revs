// Package movielens turns the MovieLens datasets
// (https://grouplens.org/datasets/movielens/) into ratingset documents,
// to seed servers and clients with data at a realistic scale (issue
// #38).
//
// It reads the CSV files of the current datasets (ml-latest, ml-25m,
// ml-32m, ...): movies.csv and links.csv make a Catalog of items, and
// ratings.csv is streamed one user at a time. Each MovieLens user
// becomes an author with a key derived from a salt and the user's id,
// so the same input always gives the same authors and documents.
//
// MovieLens rates in half stars, 0.5 to 5.0; ratingset ratings are
// integers, so documents use the scale MinRating..MaxRating (1..10)
// and each rating is doubled. Rating timestamps become entry dates
// (UTC), and Content can cut a user's history off at any time, giving
// the successive revisions an author would have pushed.
//
// The datasets' license doesn't allow redistributing them, so none of
// their data is checked in here; tests use small synthetic files.
package movielens

import (
	"bufio"
	"bytes"
	"cmp"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/openrevs/revs/keys"
	"github.com/openrevs/revs/ratingset"
)

// The rating scale of the documents made here: MovieLens's half stars
// (0.5 to 5.0), doubled.
const (
	MinRating = 1
	MaxRating = 10
)

// Catalog maps MovieLens movie ids to the items they become, without
// entries.
type Catalog map[int]ratingset.Item

// LoadCatalog reads movies.csv (movieId,title,genres) and, if links is
// not nil, links.csv (movieId,imdbId,tmdbId) for the items' refs.
// Genres have no place in a ratingset and are dropped.
func LoadCatalog(movies, links io.Reader) (Catalog, error) {
	cat := Catalog{}
	err := readCSV(movies, "movies.csv", []string{"movieId", "title", "genres"}, func(rec []string) error {
		id, err := strconv.Atoi(rec[0])
		if err != nil {
			return fmt.Errorf("movieId %q: %w", rec[0], err)
		}
		title, year := ParseTitle(rec[1])
		it := ratingset.Item{Type: ratingset.ItemTypeMovie, Title: title}
		if year != 0 {
			it.Year = &year
		}
		cat[id] = it
		return nil
	})
	if err != nil {
		return nil, err
	}
	if links == nil {
		return cat, nil
	}

	err = readCSV(links, "links.csv", []string{"movieId", "imdbId", "tmdbId"}, func(rec []string) error {
		id, err := strconv.Atoi(rec[0])
		if err != nil {
			return fmt.Errorf("movieId %q: %w", rec[0], err)
		}
		it, ok := cat[id]
		if !ok {
			return nil
		}
		refs := map[string]string{}
		if imdb := strings.TrimSpace(rec[1]); imdb != "" {
			refs["imdb"] = "tt" + imdb
		}
		if tmdb := strings.TrimSpace(rec[2]); tmdb != "" {
			refs["tmdb"] = tmdb
		}
		if len(refs) > 0 {
			it.Refs = refs
			cat[id] = it
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return cat, nil
}

// readCSV reads a CSV file with the given header, calling fn for each
// record after it.
func readCSV(r io.Reader, name string, header []string, fn func([]string) error) error {
	cr := csv.NewReader(bufio.NewReader(r))
	cr.FieldsPerRecord = len(header)
	cr.ReuseRecord = true
	got, err := cr.Read()
	if err != nil {
		return fmt.Errorf("%s: reading header: %w", name, err)
	}
	if !slices.Equal(got, header) {
		return fmt.Errorf("%s: header is %q, want %q", name, got, header)
	}
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if err := fn(rec); err != nil {
			line, _ := cr.FieldPos(0)
			return fmt.Errorf("%s:%d: %w", name, line, err)
		}
	}
}

var (
	// A trailing release year, "(1995)", or range, "(2006-2007)".
	reYear = regexp.MustCompile(`\s*\((\d{4})(?:[-–]\d{0,4})?\)\s*$`)
	// A trailing alternative title, "(Cité des enfants perdus, La)".
	reAlt = regexp.MustCompile(`^(.+?)\s+\(([^()]+)\)$`)
)

// Articles MovieLens moves to the end of titles ("Matrix, The"),
// which ParseTitle moves back. Those ending in an apostrophe take no
// space after them ("Avventura, L'" is "L'Avventura").
var articles = []string{
	"The", "A", "An",
	"La", "Le", "Les", "L'", "Il", "Lo", "Gli", "I", "Un", "Une",
	"El", "Los", "Las", "Der", "Die", "Das", "Den", "Det",
}

// ParseTitle splits a MovieLens title into the title and its release
// year (0 if it has none), dropping any alternative title in
// parentheses and moving a trailing article back to the front:
// "City of Lost Children, The (Cité des enfants perdus, La) (1995)" is
// "The City of Lost Children", 1995.
func ParseTitle(s string) (string, int) {
	s = strings.TrimSpace(s)
	year := 0
	if m := reYear.FindStringSubmatchIndex(s); m != nil {
		year, _ = strconv.Atoi(s[m[2]:m[3]])
		s = s[:m[0]]
	}
	if m := reAlt.FindStringSubmatch(s); m != nil {
		s = m[1]
	}
	for _, a := range articles {
		if rest, ok := strings.CutSuffix(s, ", "+a); ok {
			if strings.HasSuffix(a, "'") {
				s = a + rest
			} else {
				s = a + " " + rest
			}
			break
		}
	}
	return s, year
}

// Rating is one MovieLens rating.
type Rating struct {
	Movie  int
	Rating int   // in half stars, MinRating..MaxRating
	Time   int64 // Unix seconds
}

// User is one MovieLens user and their ratings, in file order.
type User struct {
	ID      int
	Ratings []Rating
}

// ReadUsers streams ratings.csv (userId,movieId,rating,timestamp),
// calling fn with each user's ratings in turn, until fn returns an
// error. The datasets list each user's ratings together; a user who
// turns up again later is an error, since their earlier ratings have
// already been passed on.
func ReadUsers(r io.Reader, fn func(User) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return fmt.Errorf("ratings.csv: %w", err)
		}
		return errors.New("ratings.csv: empty")
	}
	if h := strings.TrimSpace(sc.Text()); h != "userId,movieId,rating,timestamp" {
		return fmt.Errorf("ratings.csv: header is %q, want userId,movieId,rating,timestamp", h)
	}

	var cur User
	done := map[int]bool{}
	flush := func() error {
		if cur.Ratings == nil {
			return nil
		}
		done[cur.ID] = true
		err := fn(cur)
		cur = User{}
		return err
	}

	for line := 2; sc.Scan(); line++ {
		b := bytes.TrimSpace(sc.Bytes())
		if len(b) == 0 {
			continue
		}
		uid, rt, err := parseRating(b)
		if err != nil {
			return fmt.Errorf("ratings.csv:%d: %w", line, err)
		}
		if cur.Ratings != nil && uid != cur.ID {
			if err := flush(); err != nil {
				return err
			}
		}
		if cur.Ratings == nil {
			if done[uid] {
				return fmt.Errorf("ratings.csv:%d: user %d's ratings are not together", line, uid)
			}
			cur.ID = uid
		}
		cur.Ratings = append(cur.Ratings, rt)
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("ratings.csv: %w", err)
	}
	return flush()
}

// parseRating reads one line of ratings.csv. It splits by hand rather
// than with encoding/csv, which is several times slower and buys
// nothing for a file of plain numbers.
func parseRating(b []byte) (int, Rating, error) {
	var f [4][]byte
	for i := range 3 {
		j := bytes.IndexByte(b, ',')
		if j < 0 {
			return 0, Rating{}, errors.New("want 4 fields")
		}
		f[i], b = b[:j], b[j+1:]
	}
	f[3] = b
	if bytes.IndexByte(b, ',') >= 0 {
		return 0, Rating{}, errors.New("want 4 fields")
	}

	uid, err := strconv.Atoi(string(f[0]))
	if err != nil {
		return 0, Rating{}, fmt.Errorf("userId: %w", err)
	}
	mid, err := strconv.Atoi(string(f[1]))
	if err != nil {
		return 0, Rating{}, fmt.Errorf("movieId: %w", err)
	}
	stars, err := strconv.ParseFloat(string(f[2]), 64)
	if err != nil {
		return 0, Rating{}, fmt.Errorf("rating: %w", err)
	}
	half := stars * 2
	if half != float64(int(half)) || half < MinRating || half > MaxRating {
		return 0, Rating{}, fmt.Errorf("rating %s is not a half star from 0.5 to 5", f[2])
	}
	ts, err := strconv.ParseInt(string(f[3]), 10, 64)
	if err != nil {
		return 0, Rating{}, fmt.Errorf("timestamp: %w", err)
	}
	return uid, Rating{Movie: mid, Rating: int(half), Time: ts}, nil
}

// Key derives the ed25519 key of MovieLens user id: the same salt and
// id always give the same key, and so the same author. Different salts
// give unrelated sets of authors from the same data.
func Key(salt string, id int) ed25519.PrivateKey {
	seed := sha256.Sum256([]byte("revs-movielens\x00" + salt + "\x00" + strconv.Itoa(id)))
	return ed25519.NewKeyFromSeed(seed[:])
}

// AuthorID is the ratingset author id of the key Key(salt, id).
func AuthorID(salt string, id int) string {
	return keys.UserID(Key(salt, id).Public().(ed25519.PublicKey))
}

// Content renders the user's ratings made at or before until (Unix
// seconds; 0 means all of them) as the unsigned data lines of a
// ratingset, the input client.Push expects: an author line, then an
// item line per movie rated, each with one entry. Items are in the
// order they were first rated (then by movie id), as a client that
// adds items as they're rated would list them, so a later revision
// mostly adds lines at the end.
//
// It also returns how many ratings it included, and how many it
// skipped because their movie isn't in the catalog. With no ratings
// included the document is just the author line.
func (c Catalog) Content(authorID string, u User, until int64) (content []byte, n, skipped int, err error) {
	rs := make([]Rating, 0, len(u.Ratings))
	for _, r := range u.Ratings {
		if until != 0 && r.Time > until {
			continue
		}
		if _, ok := c[r.Movie]; !ok {
			skipped++
			continue
		}
		rs = append(rs, r)
	}
	slices.SortStableFunc(rs, func(a, b Rating) int {
		if a.Time != b.Time {
			return cmp.Compare(a.Time, b.Time)
		}
		return a.Movie - b.Movie
	})

	line, err := ratingset.EncodeLine(ratingset.Author{ID: authorID, MinRating: MinRating, MaxRating: MaxRating})
	if err != nil {
		return nil, 0, 0, err
	}
	var buf bytes.Buffer
	buf.Write(line)
	buf.WriteByte('\n')

	// Lines must be distinct to be signed. MovieLens has no repeat
	// ratings, but its catalog has the odd duplicate movie, which can
	// give identical lines; a repeat carries nothing, so drop it.
	seen := map[string]bool{}
	for _, r := range rs {
		it := c[r.Movie]
		rating := r.Rating
		date := time.Unix(r.Time, 0).UTC().Format(ratingset.DateDay)
		it.Entries = []ratingset.Entry{{Date: &date, Rating: &rating}}
		if err := it.Validate(MinRating, MaxRating); err != nil {
			return nil, 0, 0, fmt.Errorf("movie %d: %w", r.Movie, err)
		}
		line, err := ratingset.EncodeLine(it.Normalize())
		if err != nil {
			return nil, 0, 0, err
		}
		if seen[string(line)] {
			continue
		}
		seen[string(line)] = true
		buf.Write(line)
		buf.WriteByte('\n')
		n++
	}
	return buf.Bytes(), n, skipped, nil
}
