package collection

import (
	"fmt"
	"strings"
	"time"
)

// Deref returns *p, or the zero value for nil.
func Deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

// Stars renders a 0–5 rating, or a dash when unrated. A negative rating
// means unrated (Movies uses -1).
func Stars(r any) string {
	var n int64 = -1
	switch v := r.(type) {
	case *int64:
		if v != nil {
			n = *v
		}
	case int64:
		n = v
	}
	if n < 0 {
		return "–"
	}
	n = min(n, MaxRating)
	return strings.Repeat("★", int(n)) + strings.Repeat("☆", MaxRating-int(n))
}

// ShortID abbreviates a user id for display.
func ShortID(id string) string {
	if len(id) > 12 {
		return id[:12] + "…"
	}
	return id
}

// TimeAgo renders a stored UTC timestamp relative to now.
func TimeAgo(ts *string) string {
	if ts == nil {
		return "never"
	}
	t, err := time.Parse("2006-01-02T15:04:05Z", *ts)
	if err != nil {
		return *ts
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	default:
		return t.Local().Format("2006-01-02 15:04")
	}
}

// IMDBURL, TMDBURL, and WikidataURL link to a movie on those sites.
func IMDBURL(id string) string     { return "https://www.imdb.com/title/" + id + "/" }
func TMDBURL(id string) string     { return "https://www.themoviedb.org/movie/" + id }
func WikidataURL(id string) string { return "https://www.wikidata.org/wiki/" + id }
