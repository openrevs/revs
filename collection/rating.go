package collection

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/openrevs/revs/ratingset"
)

// legacyMin and legacyMax are the scale assumed for an unsigned import
// file with no author line: whole stars, as this client used to
// publish. (A signed ratingset must declare its scale.)
const (
	legacyMin = 0
	legacyMax = 5
)

// errStars describes the accepted star ratings.
var errStars = fmt.Errorf("rating must be ½–%d stars, in steps of ½ (e.g. 3.5)", MaxStars)

// ParseStars parses a star rating someone entered, such as "4", "3.5",
// or "3½", into half stars. It accepts ½ to MaxStars stars.
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
	if halves != math.Trunc(halves) || halves < MinUserRating || halves > MaxRating {
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

// scaleOf reads the scale from an author line. An author line without a
// valid scale makes the document invalid.
func scaleOf(authorLine []byte) (ratingScale, error) {
	a, err := ratingset.ParseAuthorLine(authorLine)
	if err != nil {
		return ratingScale{}, err
	}
	return ratingScale{a.MinRating, a.MaxRating}, nil
}

// scale converts a rating on s to half stars, rounding to the nearest
// half star. Ratings outside s are dropped (valid items have none).
func (s ratingScale) scale(r *int) *int64 {
	if r == nil || *r < s.min || *r > s.max {
		return nil
	}
	n := int64(math.Round(float64(*r-s.min) * float64(MaxRating-MinRating) / float64(s.max-s.min)))
	n += MinRating
	return &n
}

// readItem parses and validates an item line against the document's
// scale. ok is false for a line that isn't a valid item, which
// consumers skip.
func readItem(line []byte, s ratingScale) (it ratingset.Item, ok bool) {
	it, err := ratingset.ParseItemLine(line)
	if err != nil || it.Validate(s.min, s.max) != nil {
		return ratingset.Item{}, false
	}
	return it, true
}
