package movielens

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/openrevs/revs/keys"
)

// Dataset is a MovieLens dataset directory: movies.csv, ratings.csv,
// and optionally links.csv.
type Dataset struct {
	Dir     string
	Catalog Catalog
}

// Open loads a dataset directory's catalog. Ratings are read later, as
// they're needed (see Users).
func Open(dir string) (*Dataset, error) {
	movies, err := os.Open(filepath.Join(dir, "movies.csv"))
	if err != nil {
		return nil, err
	}
	defer movies.Close()
	links, err := os.Open(filepath.Join(dir, "links.csv"))
	switch {
	case err == nil:
		defer links.Close()
	case errors.Is(err, fs.ErrNotExist):
		links = nil
	default:
		return nil, err
	}

	var cat Catalog
	if links != nil {
		cat, err = LoadCatalog(movies, links)
	} else {
		cat, err = LoadCatalog(movies, nil)
	}
	if err != nil {
		return nil, err
	}
	return &Dataset{Dir: dir, Catalog: cat}, nil
}

// Users streams the dataset's users, as ReadUsers.
func (d *Dataset) Users(fn func(User) error) error {
	f, err := os.Open(filepath.Join(d.Dir, "ratings.csv"))
	if err != nil {
		return err
	}
	defer f.Close()
	return ReadUsers(f, fn)
}

// Selection picks which of a dataset's users to use.
type Selection struct {
	// Sample, if between 0 and 1, keeps about that fraction of the
	// users. Which users are kept depends only on their ids, so a
	// smaller sample is a subset of a larger one, and every run picks
	// the same users.
	Sample float64
	// Limit, if positive, stops after that many users (after sampling).
	Limit int
}

// sampled reports whether the user with the given id is in the sample.
func (s Selection) sampled(id int) bool {
	if s.Sample <= 0 || s.Sample >= 1 {
		return true
	}
	h := sha256.Sum256([]byte("revs-movielens-sample\x00" + strconv.Itoa(id)))
	return float64(binary.BigEndian.Uint64(h[:])>>11)/(1<<53) < s.Sample
}

// ExportOptions control Export.
type ExportOptions struct {
	Selection
	Salt     string // for the authors' keys; see Key
	Until    int64  // include ratings at or before this Unix time; 0 for all
	Revision int    // of every document; 0 means 1
	Keys     bool   // write each author's private key to the index
	Workers  int    // documents built at once; 0 means GOMAXPROCS

	// Progress, if set, is called after each user with the totals so
	// far. Calls aren't concurrent.
	Progress func(ExportStats)
}

// ExportStats counts what Export did.
type ExportStats struct {
	Users   int   `json:"users"`   // documents written
	Empty   int   `json:"empty"`   // users skipped for having no ratings to include
	Ratings int   `json:"ratings"` // ratings included
	Skipped int   `json:"skipped"` // ratings dropped because their movie isn't in the catalog
	Bytes   int64 `json:"bytes"`   // of the documents written
}

// Index and metadata files Export writes next to the documents.
const (
	IndexFile = "index.csv"
	MetaFile  = "export.json"
)

// ExportMeta is the content of an export's MetaFile: what it was made
// from, so it can be reproduced or extended with later revisions.
type ExportMeta struct {
	Dataset  string      `json:"dataset"`
	Salt     string      `json:"salt"`
	Until    *string     `json:"until,omitempty"` // RFC 3339, UTC
	Revision int         `json:"revision"`
	Sample   float64     `json:"sample,omitempty"`
	Limit    int         `json:"limit,omitempty"`
	Created  string      `json:"created"`
	Stats    ExportStats `json:"stats"`
	Scale    [2]int      `json:"scale"`
}

// Export writes the selected users' signed documents to dir, one file
// per user named <userId>.sjsonl, with an index (IndexFile) listing
// each user's MovieLens id, author id, revision, ratings and size (and
// private key, with Keys), and the export's settings (MetaFile). dir
// is created if needed and must otherwise be empty, so an export never
// mixes with an earlier one. Users with no ratings to include get no
// document.
func (d *Dataset) Export(ctx context.Context, dir string, o ExportOptions) (ExportStats, error) {
	var stats ExportStats
	if o.Revision == 0 {
		o.Revision = 1
	}
	if o.Revision < 1 {
		return stats, fmt.Errorf("revision %d is less than 1", o.Revision)
	}
	if o.Workers <= 0 {
		o.Workers = runtime.GOMAXPROCS(0)
	}
	if err := emptyDir(dir); err != nil {
		return stats, err
	}

	type result struct {
		user       int
		n, skipped int
		size       int
		err        error
	}
	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan User, o.Workers)
	results := make(chan result, o.Workers)

	var wg sync.WaitGroup
	for range o.Workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for u := range jobs {
				r := result{user: u.ID}
				var doc []byte
				doc, r.n, r.skipped, r.err = d.Catalog.Document(o.Salt, u, o.Until, o.Revision)
				if r.err == nil && r.n > 0 {
					r.size = len(doc)
					r.err = os.WriteFile(filepath.Join(dir, strconv.Itoa(u.ID)+".sjsonl"), doc, 0o644)
				}
				select {
				case results <- r:
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	readErr := make(chan error, 1)
	go func() {
		defer close(jobs)
		errLimit := errors.New("limit reached")
		count := 0
		err := d.Users(func(u User) error {
			if !o.sampled(u.ID) {
				return nil
			}
			if o.Limit > 0 && count == o.Limit {
				return errLimit
			}
			count++
			select {
			case jobs <- u:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		if errors.Is(err, errLimit) {
			err = nil
		}
		readErr <- err
	}()
	go func() {
		wg.Wait()
		close(results)
	}()

	var rows []result
	var firstErr error
	for r := range results {
		if r.err != nil {
			if firstErr == nil {
				firstErr = r.err
				cancel()
			}
			continue
		}
		stats.Skipped += r.skipped
		if r.n == 0 {
			stats.Empty++
		} else {
			stats.Users++
			stats.Ratings += r.n
			stats.Bytes += int64(r.size)
			rows = append(rows, r)
		}
		if o.Progress != nil {
			o.Progress(stats)
		}
	}
	if err := <-readErr; err != nil && firstErr == nil {
		firstErr = err
	}
	if firstErr == nil {
		// Workers stop early, dropping results, if the caller cancels.
		firstErr = parent.Err()
	}
	if firstErr != nil {
		return stats, firstErr
	}

	// The index, in user order whatever order the workers finished in.
	slices.SortFunc(rows, func(a, b result) int { return cmp.Compare(a.user, b.user) })
	idx := [][]string{{"userId", "authorId", "revision", "ratings", "bytes"}}
	if o.Keys {
		idx[0] = append(idx[0], "privateKey")
	}
	for _, r := range rows {
		key := Key(o.Salt, r.user)
		rec := []string{
			strconv.Itoa(r.user),
			AuthorID(o.Salt, r.user),
			strconv.Itoa(o.Revision),
			strconv.Itoa(r.n),
			strconv.Itoa(r.size),
		}
		if o.Keys {
			rec = append(rec, keys.EncodePrivate(key))
		}
		idx = append(idx, rec)
	}
	if err := writeCSV(filepath.Join(dir, IndexFile), idx); err != nil {
		return stats, err
	}

	meta := ExportMeta{
		Dataset:  d.Dir,
		Salt:     o.Salt,
		Revision: o.Revision,
		Sample:   o.Sample,
		Limit:    o.Limit,
		Created:  time.Now().UTC().Format(time.RFC3339),
		Stats:    stats,
		Scale:    [2]int{MinRating, MaxRating},
	}
	if o.Until != 0 {
		u := time.Unix(o.Until, 0).UTC().Format(time.RFC3339)
		meta.Until = &u
	}
	b, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return stats, err
	}
	return stats, os.WriteFile(filepath.Join(dir, MetaFile), append(b, '\n'), 0o644)
}

// emptyDir makes sure dir exists and is empty.
func emptyDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(ents) > 0 {
		return fmt.Errorf("%s is not empty", dir)
	}
	return nil
}

func writeCSV(name string, recs [][]string) error {
	f, err := os.Create(name)
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	if err := w.WriteAll(recs); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
