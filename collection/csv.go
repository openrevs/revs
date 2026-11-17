package collection

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/openrevs/revs/ratingset"
)

// CSV import columns. Matched case-insensitively; other columns are
// ignored.
const (
	csvTitle    = "title"
	csvYear     = "year"
	csvRating   = "rating"
	csvViewDate = "view_date"
	csvIMDB     = "imdb_id"
	csvIMDBNoTT = "imdb_id_no_tt" // "0110912": some exports drop the "tt"
	csvTMDB     = "tmdb_id"
)

var csvColumns = []string{csvTitle, csvYear, csvRating, csvViewDate, csvIMDB, csvIMDBNoTT, csvTMDB}

// parseCSV reads a CSV file of movies. The first line is a header
// naming the columns; only title is required. Each row is one movie,
// and a row with a rating or view date also records a viewing of it.
// Every row is validated before anything is returned.
func parseCSV(data []byte) ([]ratingset.Item, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")) // spreadsheet BOM
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, errors.New("file is empty")
	}

	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = -1
	r.TrimLeadingSpace = true

	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("reading header: %w", err)
	}
	col := map[string]int{}
	for i, h := range header {
		h = strings.ToLower(strings.TrimSpace(h))
		for _, known := range csvColumns {
			if h != known {
				continue
			}
			if _, dup := col[h]; dup {
				return nil, fmt.Errorf("header has more than one %q column", h)
			}
			col[h] = i
		}
	}
	if _, ok := col[csvTitle]; !ok {
		return nil, fmt.Errorf("header must include a %q column (supported: %s)", csvTitle, strings.Join(csvColumns, ", "))
	}

	var titles []ratingset.Item
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err // includes the line number
		}
		line, _ := r.FieldPos(0)
		get := func(name string) string {
			if i, ok := col[name]; ok && i < len(rec) {
				return rec[i]
			}
			return ""
		}

		mf := MovieFields{Title: get(csvTitle), Year: get(csvYear), IMDB: get(csvIMDB), TMDB: get(csvTMDB)}
		in, merr := mf.Parse()
		imdb, ierr := imdbNoTT(get(csvIMDBNoTT), in.IMDB)
		if imdb != nil {
			in.IMDB = imdb
		}
		vf := ViewingFields{Date: get(csvViewDate), Rating: get(csvRating)}
		vin, verr := vf.Parse()
		if err := errors.Join(merr, ierr, verr); err != nil {
			return nil, fmt.Errorf("line %d: %s", line, strings.ReplaceAll(err.Error(), "\n", "; "))
		}

		t := ratingset.Item{Type: ratingset.ItemTypeMovie, Title: in.Title}
		if in.Year != nil {
			y := int(*in.Year)
			t.Year = &y
		}
		refs := map[string]string{}
		if in.IMDB != nil {
			refs[refIMDB] = *in.IMDB
		}
		if in.TMDB != nil {
			refs[refTMDB] = *in.TMDB
		}
		if len(refs) > 0 {
			t.Refs = refs
		}
		if vin.Date != nil || vin.Rating != nil {
			e := ratingset.Entry{Date: vin.Date}
			if vin.Rating != nil {
				n := int(*vin.Rating)
				e.Rating = &n
			}
			t.Entries = []ratingset.Entry{e}
		}
		titles = append(titles, t)
	}
	if len(titles) == 0 {
		return nil, errors.New("no rows after the header")
	}
	return titles, nil
}

// imdbNoTT parses an imdb_id_no_tt value, an IMDb id without its "tt"
// prefix ("0110912"). It must agree with imdb, the row's imdb_id, when
// both are given.
func imdbNoTT(s string, imdb *string) (*string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	id := normalizeIMDB("tt" + s)
	if !digitsRe.MatchString(s) || id == nil {
		return nil, fmt.Errorf("%s %q should be digits like 0110912", csvIMDBNoTT, s)
	}
	if imdb != nil && *imdb != *id {
		return nil, fmt.Errorf("%s %q doesn't match %s %q", csvIMDBNoTT, s, csvIMDB, *imdb)
	}
	return id, nil
}

// ImportCSV merges a CSV file of movies into the collection (see
// parseCSV for the format, and mergeItems for how it merges). A file
// that fails to parse changes nothing and yields an ErrUnreadableFile.
func (c *Collection) ImportCSV(ctx context.Context, data []byte) (ImportStats, error) {
	titles, err := parseCSV(data)
	if err != nil {
		return ImportStats{}, unreadableFile{err}
	}
	return c.mergeItems(ctx, titles)
}
