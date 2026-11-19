package main

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/openrevs/revs/collection"
)

// skipped is a line of text that held no title.
type skipped struct {
	line int
	text string
}

var (
	reHeading = regexp.MustCompile(`^#{1,6}\s+`)
	reBullet  = regexp.MustCompile(`^(?:[-+•‣◦]|\*(?:\s)|\d{1,3}[.)])\s*(?:\[[ xX]\]\s+)?`)
	reRule    = regexp.MustCompile(`^[-=_*~#\s]{3,}$`)
	reBold    = regexp.MustCompile(`\*\*([^*]+)\*\*|__([^_]+)__`)
)

// convert finds the entries in text: one per line that names a title,
// with the lines after it that carry no values of their own (indented
// or quoted, or sentences after an entry that had values) taken as its
// review.
// Lines that are neither are returned as skipped.
func convert(text string, o options) ([]*entry, []skipped) {
	text = strings.TrimPrefix(text, "\ufeff")
	text = strings.ReplaceAll(text, "\r\n", "\n")

	var (
		entries []*entry
		skips   []skipped
		cur     *entry // the entry later lines may continue
		blank   bool   // a blank line since the last line of cur
	)
	for i, raw := range strings.Split(text, "\n") {
		n := i + 1
		if strings.TrimSpace(raw) == "" {
			blank = true
			continue
		}
		indented := raw[0] == ' ' || raw[0] == '\t'
		s := strings.TrimSpace(raw)
		quoted := false
		for strings.HasPrefix(s, ">") {
			s, quoted = strings.TrimSpace(s[1:]), true
		}
		if reRule.MatchString(s) && !strings.Contains(s, "*") || s == "" {
			blank = true
			continue
		}
		heading := false
		if loc := reHeading.FindStringIndex(s); loc != nil {
			s, heading = s[loc[1]:], true
		}
		bullet := false
		if loc := reBullet.FindStringIndex(s); loc != nil && !heading {
			s, bullet = s[loc[1]:], true
		}
		s = reBold.ReplaceAllString(s, "$1$2")

		e, ok := parseLine(s, o)
		switch {
		case heading && !(ok && e.strong):
			// A section heading ("# 2023"): it ends the entry before.
			cur = nil
			continue
		case ok && (bullet || heading || cur == nil || !(indented || quoted)):
			e.line, e.fromBullet = n, bullet
			entries = append(entries, &e)
			cur, blank = &e, false
			continue
		case cur != nil && !bullet && (indented || quoted || cur.hasMeta && !titleLike(s)):
			switch {
			case cur.Review == "":
				cur.Review = s
			case blank:
				cur.Review += "\n\n" + s
			default:
				cur.Review += " " + s
			}
			blank = false
			continue
		case titleLike(s):
			e := &entry{line: n, Title: cleanTitle(s), fromBullet: bullet}
			entries = append(entries, e)
			cur, blank = e, false
			continue
		}
		skips = append(skips, skipped{n, strings.TrimSpace(raw)})
	}
	return entries, skips
}

// titleLike reports whether s, a line with no values, could be just a
// title: short, and not a sentence.
func titleLike(s string) bool {
	if utf8.RuneCountInString(s) > 100 || len(strings.Fields(s)) > 12 {
		return false
	}
	return !strings.HasSuffix(s, ".") || strings.HasSuffix(s, "...")
}

// columns are the CSV import columns written, in order.
var columns = []string{"title", "year", "type", "rating", "view_date", "season", "episode", "imdb_id", "tmdb_id", "review"}

func (e *entry) fields() []string {
	return []string{e.Title, e.Year, e.Type, e.Rating, e.Date, e.Season, e.Episode, e.IMDB, e.TMDB, e.Review}
}

// check validates e the way revs's CSV import will.
func (e *entry) check() error {
	mf := collection.MovieFields{Type: e.Type, Title: e.Title, Year: e.Year, IMDB: e.IMDB, TMDB: e.TMDB}
	in, merr := mf.Parse()
	typ := in.Type
	if typ == "" {
		typ = collection.TypeMovie
	}
	vf := collection.ViewingFields{Type: typ, Date: e.Date, Rating: e.Rating, Review: e.Review, Season: e.Season, Episode: e.Episode}
	_, verr := vf.Parse()
	return errors.Join(merr, verr)
}

// writeCSV writes the entries as a CSV import file, with only the
// columns some entry uses (title always).
func writeCSV(w io.Writer, entries []*entry) error {
	used := make([]bool, len(columns))
	used[0] = true
	for _, e := range entries {
		for i, f := range e.fields() {
			if f != "" {
				used[i] = true
			}
		}
	}
	pick := func(fs []string) []string {
		var out []string
		for i, f := range fs {
			if used[i] {
				out = append(out, f)
			}
		}
		return out
	}

	cw := csv.NewWriter(w)
	if err := cw.Write(pick(columns)); err != nil {
		return err
	}
	for _, e := range entries {
		if err := cw.Write(pick(e.fields())); err != nil {
			return err
		}
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return fmt.Errorf("writing CSV: %w", err)
	}
	return nil
}
