package collection

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/openrevs/revs"
)

// CSV import columns. Matched case-insensitively; other columns are
// ignored.
const (
	csvTitle    = "title"
	csvYear     = "year"
	csvRating   = "rating"
	csvViewDate = "view_date"
	csvIMDB     = "imdb_id"
	csvTMDB     = "tmdb_id"
)

var csvColumns = []string{csvTitle, csvYear, csvRating, csvViewDate, csvIMDB, csvTMDB}

// parseCSV reads a CSV file of movies. The first line is a header
// naming the columns; only title is required. Each row is one movie,
// and a row with a rating or view date also records a viewing of it.
// Every row is validated before anything is returned.
func parseCSV(data []byte) ([]revs.Title2, error) {
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

	var titles []revs.Title2
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
		vf := ViewingFields{Date: get(csvViewDate), Rating: get(csvRating)}
		vin, verr := vf.Parse()
		if err := errors.Join(merr, verr); err != nil {
			return nil, fmt.Errorf("line %d: %s", line, strings.ReplaceAll(err.Error(), "\n", "; "))
		}

		t := revs.Title2{Name: in.Title}
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
			rv := revs.Review2{Date: vin.Date}
			if vin.Rating != nil {
				n := int(*vin.Rating)
				rv.Rating = &n
			}
			t.Reviews = []revs.Review2{rv}
		}
		titles = append(titles, t)
	}
	if len(titles) == 0 {
		return nil, errors.New("no rows after the header")
	}
	return titles, nil
}

// ImportCSV merges a CSV file of movies into the collection (see
// parseCSV for the format, and mergeTitles for how it merges). A file
// that fails to parse changes nothing and yields an ErrUnreadableFile.
func (c *Collection) ImportCSV(ctx context.Context, data []byte) (ImportStats, error) {
	titles, err := parseCSV(data)
	if err != nil {
		return ImportStats{}, unreadableFile{err}
	}
	return c.mergeTitles(ctx, titles)
}
