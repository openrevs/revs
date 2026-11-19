package main

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/openrevs/revs/collection"
)

// entry is one title found in the text, with the fields of a CSV
// import row as strings (empty when not found).
type entry struct {
	line int // first line of the entry, for messages

	Title, Year, Type    string
	Rating, Date, Review string
	Season, Episode      string
	IMDB, TMDB           string

	hasMeta    bool // the line had values, not just a title
	strong     bool // some value was unambiguous (not a bare number or year)
	fromBullet bool // the line was a list item
}

// options control how ambiguous values are read.
type options struct {
	Scale    float64 // the scale of bare numeric ratings ("4" with no "/5")
	DayFirst bool    // read 01/02/2023 as 1 February, not January 2
}

// kind is the kind of a value found in a line.
type kind int

const (
	kIMDB kind = iota
	kTMDB
	kDate
	kEpisode
	kTV
	kYear
	kRating
)

// match is a value found in a line, at line[start:end].
type match struct {
	kind       kind
	start, end int
	strong     bool // unambiguous: can start the metadata on its own
	val        string
	val2       string // episode for kEpisode, "tv" for a TMDB tv URL
}

var (
	reIMDB = regexp.MustCompile(`(?:https?://)?(?:www\.|m\.)?(?:imdb\.com/title/)?\btt\d{5,}\b/?`)
	reTMDB = regexp.MustCompile(`(?:https?://)?(?:www\.)?themoviedb\.org/(movie|tv)/(\d+)[^\s]*`)

	months      = `(jan(?:uary)?|feb(?:ruary)?|mar(?:ch)?|apr(?:il)?|may|june?|july?|aug(?:ust)?|sep(?:t(?:ember)?)?|oct(?:ober)?|nov(?:ember)?|dec(?:ember)?)`
	reDateISO   = regexp.MustCompile(`\b(\d{4})[-/.](\d{1,2})[-/.](\d{1,2})\b`)
	reDateNum   = regexp.MustCompile(`\b(\d{1,2})[-/.](\d{1,2})[-/.](\d{4}|\d{2})\b`)
	reDateMDY   = regexp.MustCompile(`(?i)\b` + months + `\.?\s+(\d{1,2})(?:st|nd|rd|th)?,?\s+(\d{4})\b`)
	reDateDMY   = regexp.MustCompile(`(?i)\b(\d{1,2})(?:st|nd|rd|th)?\s+(?:of\s+)?` + months + `\.?,?\s+(\d{4})\b`)
	reEpisode   = regexp.MustCompile(`(?i)\bS(\d{1,2})\s?E(\d{1,3})\b|\bS(\d{1,2})\b|\bseason\s+(\d{1,2})(?:\s*,?\s*(?:episode|ep\.?)\s*(\d{1,3}))?\b|\b(\d{1,2})x(\d{2})\b`)
	reTV        = regexp.MustCompile(`(?i)[(\[](?:tv|tv series|tv show|series|mini-?series)[)\]]`)
	reParenYear = regexp.MustCompile(`[(\[](\d{4})(\s*[–—-]\s*(?:\d{4})?)?[)\]]`)

	// Ratings, strongest forms first.
	reRatingLabeled = regexp.MustCompile(`(?i)\b(?:my\s+)?(?:rating|rated|score|stars|grade)\s*[:=]?\s*(\d{1,3}(?:\.\d+)?½?|½)(?:\s*(?:/|out\s+of)\s*(\d{1,3}))?`)
	reRatingFrac    = regexp.MustCompile(`(?i)(?:^|[^\w./*-])(\d{1,3}(?:\.\d+)?½?|½)\s*(?:/|out\s+of)\s*(\d{1,3})(?:\s*stars?)?\b`)
	reRatingStars   = regexp.MustCompile(`(?i)(\d(?:\.\d)?½?|½)\s*(?:★|⭐|stars?\b)`)
	reRatingGlyphs  = regexp.MustCompile(`[★⭐✭✪]+\s*(½|1/2)?[☆✩]*|[★⭐✭✪]*½[☆✩]*`)
	reRatingAscii   = regexp.MustCompile(`(?:^|[^*\w])(\*{1,5})(\s*(?:½|1/2))?(?:$|[^*\w])`)
	reNumber        = regexp.MustCompile(`\d{1,3}(?:\.\d+)?½?|½`)
	reBareYear      = regexp.MustCompile(`\b(1[89]\d\d|20\d\d)\b`)

	// filler is text that may sit between values without ending them.
	reFiller = regexp.MustCompile(`(?i)^(?:[\s,;|:/()\[\]–—\-.]|\b(?:watched|seen|saw|viewed|on|in|rated|rating|score|stars?|my|year|date|re-?watch(?:ed)?|first|time|tv)\b)*$`)
	// reSep is a separator that splits a title from what follows.
	reSep = regexp.MustCompile(`\t|\s+[|–—-]\s+|\s*\|\s*|:\s+`)
)

var monthNum = map[string]time.Month{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}

// findMatches finds every value in s, without overlaps: where two
// would overlap, the one found first in the order below wins.
func findMatches(s string, o options) []match {
	var ms []match
	taken := func(a, b int) bool {
		for _, m := range ms {
			if a < m.end && m.start < b {
				return true
			}
		}
		return false
	}
	add := func(m match) bool {
		if taken(m.start, m.end) {
			return false
		}
		ms = append(ms, m)
		return true
	}
	each := func(re *regexp.Regexp, f func(idx []int)) {
		for _, idx := range re.FindAllStringSubmatchIndex(s, -1) {
			f(idx)
		}
	}
	sub := func(idx []int, n int) string {
		if idx[2*n] < 0 {
			return ""
		}
		return s[idx[2*n]:idx[2*n+1]]
	}

	each(reTMDB, func(idx []int) {
		add(match{kind: kTMDB, start: idx[0], end: idx[1], strong: true, val: sub(idx, 2), val2: sub(idx, 1)})
	})
	each(reIMDB, func(idx []int) {
		id := regexp.MustCompile(`tt\d+`).FindString(s[idx[0]:idx[1]])
		add(match{kind: kIMDB, start: idx[0], end: idx[1], strong: true, val: id})
	})
	each(reDateISO, func(idx []int) {
		if d, ok := mkDate(sub(idx, 1), sub(idx, 2), sub(idx, 3)); ok {
			add(match{kind: kDate, start: idx[0], end: idx[1], strong: true, val: d})
		}
	})
	each(reDateMDY, func(idx []int) {
		if d, ok := mkDate(sub(idx, 3), monthOf(sub(idx, 1)), sub(idx, 2)); ok {
			add(match{kind: kDate, start: idx[0], end: idx[1], strong: true, val: d})
		}
	})
	each(reDateDMY, func(idx []int) {
		if d, ok := mkDate(sub(idx, 3), monthOf(sub(idx, 2)), sub(idx, 1)); ok {
			add(match{kind: kDate, start: idx[0], end: idx[1], strong: true, val: d})
		}
	})
	each(reDateNum, func(idx []int) {
		a, b, y := sub(idx, 1), sub(idx, 2), sub(idx, 3)
		if len(y) == 2 {
			y = "20" + y
			if n, _ := strconv.Atoi(y); n > time.Now().Year() {
				y = "19" + y[2:]
			}
		}
		if o.DayFirst {
			a, b = b, a
		}
		if d, ok := mkDate(y, a, b); ok {
			add(match{kind: kDate, start: idx[0], end: idx[1], strong: true, val: d})
		}
	})
	each(reEpisode, func(idx []int) {
		m := match{kind: kEpisode, start: idx[0], end: idx[1], strong: true}
		switch {
		case sub(idx, 1) != "":
			m.val, m.val2 = sub(idx, 1), sub(idx, 2)
		case sub(idx, 3) != "":
			m.val = sub(idx, 3)
		case sub(idx, 4) != "":
			m.val, m.val2 = sub(idx, 4), sub(idx, 5)
		default:
			m.val, m.val2 = sub(idx, 6), sub(idx, 7)
		}
		m.val, m.val2 = trimZeros(m.val), trimZeros(m.val2)
		add(m)
	})
	each(reTV, func(idx []int) {
		add(match{kind: kTV, start: idx[0], end: idx[1], strong: true})
	})
	each(reParenYear, func(idx []int) {
		if plausibleYear(sub(idx, 1)) {
			m := match{kind: kYear, start: idx[0], end: idx[1], strong: true, val: sub(idx, 1)}
			if sub(idx, 2) != "" { // (2008–2013): a series's run
				m.val2 = "tv"
			}
			add(m)
		}
	})

	rating := func(start, end int, num, max string, defMax float64, strong bool) {
		v, ok := parseNum(num)
		if !ok {
			return
		}
		scale := defMax
		if max != "" {
			n, err := strconv.Atoi(max)
			if err != nil || n <= 0 {
				return
			}
			scale = float64(n)
		}
		if v > scale {
			return
		}
		add(match{kind: kRating, start: start, end: end, strong: strong, val: stars(v, scale)})
	}
	each(reRatingLabeled, func(idx []int) {
		rating(idx[0], idx[1], sub(idx, 1), sub(idx, 2), o.Scale, true)
	})
	each(reRatingGlyphs, func(idx []int) {
		g := s[idx[0]:idx[1]]
		n := float64(strings.Count(g, "★") + strings.Count(g, "⭐") + strings.Count(g, "✭") + strings.Count(g, "✪"))
		if strings.Contains(g, "½") || strings.Contains(g, "1/2") {
			n += 0.5
		}
		if n > 0 && n <= float64(collection.MaxStars) {
			add(match{kind: kRating, start: idx[0], end: idx[1], strong: true, val: collection.FormatStars(int64(n * 2))})
		}
	})
	each(reRatingAscii, func(idx []int) {
		n := float64(idx[3] - idx[2])
		if sub(idx, 2) != "" {
			n += 0.5
		}
		end := idx[3]
		if idx[4] >= 0 {
			end = idx[5]
		}
		add(match{kind: kRating, start: idx[2], end: end, strong: true, val: collection.FormatStars(int64(n * 2))})
	})

	each(reRatingFrac, func(idx []int) {
		rating(idx[2], idx[1], sub(idx, 1), sub(idx, 2), 0, true)
	})
	each(reRatingStars, func(idx []int) {
		rating(idx[0], idx[1], sub(idx, 1), "", float64(collection.MaxStars), true)
	})
	// Weak values: plausible years and plain numbers, which might
	// just as well be part of a title ("Toy Story 3"). They only
	// count next to stronger ones (see segment).
	each(reBareYear, func(idx []int) {
		if plausibleYear(sub(idx, 1)) && bounded(s, idx[0], idx[1]) {
			add(match{kind: kYear, start: idx[0], end: idx[1], val: sub(idx, 1)})
		}
	})
	each(reNumber, func(idx []int) {
		if bounded(s, idx[0], idx[1]) {
			rating(idx[0], idx[1], s[idx[0]:idx[1]], "", o.Scale, false)
		}
	})

	sort.Slice(ms, func(i, j int) bool { return ms[i].start < ms[j].start })
	return ms
}

// bounded reports whether s[a:b] stands alone: at the ends of s or
// between spaces and separators, not inside a word, a number, or a
// fraction.
func bounded(s string, a, b int) bool {
	ok := func(r rune) bool { return unicode.IsSpace(r) || strings.ContainsRune(",;|()[]", r) }
	if a > 0 {
		r := lastRune(s[:a])
		if !ok(r) {
			return false
		}
	}
	if b < len(s) {
		r := []rune(s[b:])[0]
		if !ok(r) && !(r == '.' && (b+1 == len(s) || s[b+1] == ' ')) {
			return false
		}
	}
	return true
}

func lastRune(s string) rune {
	r := []rune(s)
	return r[len(r)-1]
}

// segment splits a line into title, values, and review. The values are
// the run of matches, separated only by filler, that starts at the
// first strong match (pulled back over weak ones just before it) or,
// with none, at a weak one that plausibly ends the title. The title is
// the text before the run, and the review the text after it.
func segment(s string, ms []match) (title string, used []match, review string, ok bool) {
	start := -1
	for i, m := range ms {
		if m.strong {
			start = i
			break
		}
	}
	if start < 0 {
		for i := range ms {
			if weakStart(s, ms, i) {
				start = i
				break
			}
		}
	}
	if start < 0 {
		return "", nil, "", false
	}
	for start > 0 && filler(s[ms[start-1].end:ms[start].start]) && !isFillerOnly(s[:ms[start-1].start]) {
		start--
	}

	title = s[:ms[start].start]
	pos := ms[start].start
	seen := map[kind]bool{}
	for i := start; i < len(ms); i++ {
		m := ms[i]
		gap := s[pos:m.start]
		if !filler(gap) {
			if isFillerOnly(title) && len(used) > 0 {
				// The values came first ("★★★★ Heat (1995)"): the
				// first stretch of text is the title.
				t, rest, found := cutSep(gap)
				title = t
				if found {
					review = rest + s[m.start:]
					return cleanTitle(title), used, cleanReview(review), true
				}
			} else {
				return cleanTitle(title), used, cleanReview(s[pos:]), true
			}
		}
		if !seen[m.kind] || m.kind == kTV {
			used = append(used, m)
			seen[m.kind] = true
		}
		pos = m.end
	}
	rest := s[pos:]
	if isFillerOnly(title) {
		title, rest, _ = cutSep(rest)
	}
	return cleanTitle(title), used, cleanReview(rest), true
}

// weakStart reports whether weak match i can start the values on its
// own: a year after some title text ("Heat 1995"), or a number set off
// by a separator ("Heat, 4" or "Heat 4.5", but not "Toy Story 3").
func weakStart(s string, ms []match, i int) bool {
	m := ms[i]
	before := s[:m.start]
	if isFillerOnly(before) {
		return false
	}
	if m.kind == kYear {
		return true
	}
	num := s[m.start:m.end]
	if strings.ContainsAny(num, ".½") {
		return true
	}
	t := strings.TrimRightFunc(before, unicode.IsSpace)
	return t != before && t != "" && strings.ContainsRune(",;|-–—:", lastRune(t)) || strings.HasSuffix(before, "\t")
}

func filler(s string) bool { return reFiller.MatchString(s) }

func isFillerOnly(s string) bool { return strings.TrimFunc(s, isSepRune) == "" }

func isSepRune(r rune) bool {
	return unicode.IsSpace(r) || strings.ContainsRune(",;|:-–—/()[].\"'“”*_", r)
}

// cutSep splits s at its first separator.
func cutSep(s string) (before, after string, found bool) {
	s = strings.TrimLeftFunc(s, isSepRune)
	if loc := reSep.FindStringIndex(s); loc != nil {
		return s[:loc[0]], s[loc[1]:], true
	}
	return s, "", false
}

func cleanTitle(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimRight(s, " \t,;|:-–—/")
	s = strings.TrimSpace(s)
	s = unwrap(s)
	return s
}

func cleanReview(s string) string {
	s = strings.TrimLeftFunc(s, func(r rune) bool { return unicode.IsSpace(r) || strings.ContainsRune(",;|:-–—/.)]", r) })
	s = strings.TrimSpace(s)
	return unwrap(s)
}

// unwrap removes quotes or emphasis around all of s.
func unwrap(s string) string {
	for _, p := range [][2]string{{`"`, `"`}, {"“", "”"}, {"'", "'"}, {"*", "*"}, {"_", "_"}} {
		if len(s) > 2*len(p[0]) && strings.HasPrefix(s, p[0]) && strings.HasSuffix(s, p[1]) {
			inner := s[len(p[0]) : len(s)-len(p[1])]
			if !strings.Contains(inner, p[0]) && !strings.Contains(inner, p[1]) {
				return strings.TrimSpace(inner)
			}
		}
	}
	return s
}

// parseLine finds the entry in one line of text (with list markers and
// such already removed). ok is false for a line with no values.
func parseLine(s string, o options) (e entry, ok bool) {
	ms := findMatches(s, o)
	title, used, review, ok := segment(s, ms)
	if !ok || title == "" {
		return entry{}, false
	}
	e = entry{Title: title, Review: review, hasMeta: true}
	for _, m := range used {
		e.strong = e.strong || m.strong
		switch m.kind {
		case kIMDB:
			e.IMDB = m.val
		case kTMDB:
			e.TMDB = m.val
			if m.val2 == "tv" {
				e.Type = collection.TypeTV
			}
		case kDate:
			e.Date = m.val
		case kEpisode:
			e.Type = collection.TypeTV
			e.Season, e.Episode = m.val, m.val2
		case kTV:
			e.Type = collection.TypeTV
		case kYear:
			e.Year = m.val
			if m.val2 == "tv" {
				e.Type = collection.TypeTV
			}
		case kRating:
			e.Rating = m.val
		}
	}
	if e.Date == "" {
		// "Great. Watched 2023-04-01." A date in the review is most
		// likely when it was seen; the review keeps it too.
		for _, m := range ms {
			if m.kind == kDate && m.start >= len(s)-len(review) {
				e.Date = m.val
				break
			}
		}
	}
	return e, true
}

// stars converts v on a 0–scale scale to stars, to the nearest half
// star, and at least half a star (the lowest rating revs records).
func stars(v, scale float64) string {
	if scale <= 0 {
		scale = float64(collection.MaxStars)
	}
	halves := int64(math.Round(v / scale * float64(collection.MaxRating)))
	halves = max(halves, collection.MinUserRating)
	halves = min(halves, collection.MaxRating)
	return collection.FormatStars(halves)
}

func parseNum(s string) (float64, bool) {
	s = strings.Replace(s, "½", ".5", 1)
	if strings.HasPrefix(s, ".") {
		s = "0" + s
	}
	if strings.HasSuffix(s, ".") {
		s += "0"
	}
	if strings.Count(s, ".") > 1 { // "3.5" + "½"
		return 0, false
	}
	if i := strings.Index(s, "."); i >= 0 && strings.HasSuffix(s, ".5") && i != len(s)-2 {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	return v, err == nil && v >= 0
}

func plausibleYear(s string) bool {
	y, err := strconv.Atoi(s)
	return err == nil && y >= 1870 && y <= time.Now().Year()+2
}

func monthOf(s string) string {
	return strconv.Itoa(int(monthNum[strings.ToLower(s)[:3]]))
}

func mkDate(y, m, d string) (string, bool) {
	yi, err1 := strconv.Atoi(y)
	mi, err2 := strconv.Atoi(m)
	di, err3 := strconv.Atoi(d)
	if err1 != nil || err2 != nil || err3 != nil || yi < 1870 || yi > time.Now().Year()+1 {
		return "", false
	}
	t := time.Date(yi, time.Month(mi), di, 0, 0, 0, 0, time.UTC)
	if t.Year() != yi || int(t.Month()) != mi || t.Day() != di {
		return "", false
	}
	return t.Format(collection.DateLayout), true
}

func trimZeros(s string) string {
	if s == "" {
		return s
	}
	t := strings.TrimLeft(s, "0")
	if t == "" {
		return "0"
	}
	return t
}

func (e entry) String() string {
	return fmt.Sprintf("%q year=%q type=%q rating=%q date=%q s=%q e=%q imdb=%q tmdb=%q review=%q",
		e.Title, e.Year, e.Type, e.Rating, e.Date, e.Season, e.Episode, e.IMDB, e.TMDB, e.Review)
}
