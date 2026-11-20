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

// Stars renders a rating in half stars as five stars ("★★★½☆"), or a
// dash when unrated. A negative rating means unrated (Movies uses -1).
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
	full, half := int(n/2), int(n%2)
	return strings.Repeat("★", full) + strings.Repeat("½", half) + strings.Repeat("☆", MaxStars-full-half)
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

// TimeAgoAt is TimeAgo for a time.Time; the zero time is "never".
func TimeAgoAt(t time.Time) string {
	if t.IsZero() {
		return TimeAgo(nil)
	}
	ts := t.UTC().Format("2006-01-02T15:04:05Z")
	return TimeAgo(&ts)
}

// Duration describes a span like a publishing interval, roughly:
// "45 min", "1 h", "3 days".
func Duration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Round(time.Minute).Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h", int(d.Round(time.Hour).Hours()))
	default:
		return fmt.Sprintf("%d days", int(d.Round(24*time.Hour).Hours()/24))
	}
}

// IMDBURL and WikidataURL link to a movie or TV series on those sites.
func IMDBURL(id string) string     { return "https://www.imdb.com/title/" + id + "/" }
func WikidataURL(id string) string { return "https://www.wikidata.org/wiki/" + id }

// TMDBURL links to an item of type typ on TMDB, which numbers movies
// and TV series separately.
func TMDBURL(typ, id string) string {
	if typ == TypeTV {
		return "https://www.themoviedb.org/tv/" + id
	}
	return "https://www.themoviedb.org/movie/" + id
}

// TypeName names an item type for display: "Movie" or "TV series".
func TypeName(typ string) string {
	if typ == TypeTV {
		return "TV series"
	}
	return "Movie"
}

// PartName names the part of a TV series a viewing is about: "Season
// 2", "S2 E5", or for season 0, "Specials" and "Special 5". It is ""
// for a viewing of the whole item. It accepts *int64 or *int.
func PartName(season, episode any) string {
	s, ok := intPtr(season)
	if !ok {
		return ""
	}
	e, hasEp := intPtr(episode)
	switch {
	case s == 0 && hasEp:
		return fmt.Sprintf("Special %d", e)
	case s == 0:
		return "Specials"
	case hasEp:
		return fmt.Sprintf("S%d E%d", s, e)
	default:
		return fmt.Sprintf("Season %d", s)
	}
}

func intPtr(v any) (int64, bool) {
	switch p := v.(type) {
	case *int64:
		if p != nil {
			return *p, true
		}
	case *int:
		if p != nil {
			return int64(*p), true
		}
	}
	return 0, false
}
