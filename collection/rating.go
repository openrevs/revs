package collection

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// legacyMin and legacyMax are the scale assumed for a ratingset that
// doesn't declare one: whole stars, as this client used to publish.
const (
	legacyMin = 0
	legacyMax = 5
)

// errStars describes the accepted star ratings.
var errStars = fmt.Errorf("rating must be 0–%d stars, in steps of ½ (e.g. 3.5)", MaxStars)

// ParseStars parses a star rating such as "4", "3.5", or "3½" into
// half stars.
func ParseStars(s string) (int64, error) {
	s = strings.TrimSpace(s)
	s = strings.Replace(s, "½", ".5", 1)
	if strings.HasPrefix(s, ".") {
		s = "0" + s
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) {
		return 0, errStars
	}
	halves := f * 2
	if halves != math.Trunc(halves) || halves < MinRating || halves > MaxRating {
		return 0, errStars
	}
	return int64(halves), nil
}

// FormatStars renders a rating in half stars as a number of stars:
// "4", "3.5".
func FormatStars(n int64) string {
	if n%2 == 0 {
		return strconv.FormatInt(n/2, 10)
	}
	return strconv.FormatInt(n/2, 10) + ".5"
}

// ratingScale is the rating range a ratingset declares.
type ratingScale struct{ min, max int }

// scaleOf reads the scale from a user line, falling back to the legacy
// whole-star scale when it is missing or nonsensical.
func scaleOf(userLine []byte) ratingScale {
	lo, hi := gjson.GetBytes(userLine, "min_rating"), gjson.GetBytes(userLine, "max_rating")
	if !hi.Exists() || hi.Int() <= lo.Int() {
		return ratingScale{legacyMin, legacyMax}
	}
	return ratingScale{int(lo.Int()), int(hi.Int())}
}

// scale converts a rating on s to half stars, rounding to the nearest
// half star. Ratings outside s are dropped.
func (s ratingScale) scale(r *int) *int64 {
	if r == nil || *r < s.min || *r > s.max {
		return nil
	}
	n := int64(math.Round(float64(*r-s.min) * float64(MaxRating-MinRating) / float64(s.max-s.min)))
	n += MinRating
	return &n
}
