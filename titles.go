package dmrs

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/kalafut/dmrs/domain"
	"github.com/kalafut/dmrs/util"
)

func (c *Client) titlesGet(w http.ResponseWriter, r *http.Request) error {
	query := domain.TitleQuery{
		Name: util.StrPtrOrNil(r.FormValue("q")),
	}
	titlesDB, err := c.dal.TitleSearch(r.Context(), query)
	if err != nil {
		return err
	}

	var titles []map[string]interface{}
	for _, title := range titlesDB {
		stats, err := c.dal.TitleStats(r.Context(), title.ID)
		if err != nil {
			return err
		}
		titles = append(titles, map[string]interface{}{
			"Title":       title,
			"RatingCount": stats.RatingCount,
			"AvgRating":   stats.AverageRating,
		})
	}

	return c.render(w, "titles", titles)
}

func (c *Client) titleDetailGet(w http.ResponseWriter, r *http.Request) error {
	user := currentUser(r)

	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		return err
	}

	title, err := c.dal.Title(r.Context(), id)
	if err != nil {
		return err
	}

	ratings, err := c.dal.TitleRatings(r.Context(), id)
	if err != nil {
		return err
	}

	rating := &domain.Rating{}
	for _, rt := range ratings {
		if rt.UserID == user.ID {
			rating = rt
			break
		}
	}

	userRating := 0
	if rating.Rating != nil {
		userRating = *rating.Rating
	}

	data := map[string]interface{}{
		"Title":       title,
		"AvgRating":   averageRating(ratings),
		"RatingCount": len(ratings),
		"Rating":      rating,
		"UserRating":  userRating,
		"Comment":     util.StrFromPtr(rating.Comment),
	}

	return c.render(w, "title_detail", data)
}

func (c *Client) titleDetailPost(w http.ResponseWriter, r *http.Request) error {
	user := currentUser(r)
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		return err
	}
	comment := r.FormValue("comment")

	title, err := c.dal.Title(r.Context(), id)
	if err != nil {
		return err
	}

	rating := &domain.Rating{
		TitleID: title.ID,
		UserID:  user.ID,
	}
	newRating := true

	ratings, err := c.dal.UserRatings(r.Context(), user.ID)
	if err != nil {
		return err
	}

	for _, rt := range ratings {
		if rt.UserID == user.ID {
			rating = rt
			newRating = false
			break
		}
	}

	if comment != "" || rating.Comment != nil {
		rating.Comment = &comment
	}

	ratingRaw := r.FormValue("rating")
	switch ratingRaw {
	case "-":
		rating.Rating = nil
	default:
		ratingInt, err := strconv.Atoi(ratingRaw)
		if err != nil {
			return err
		}
		// TODO: check valid range
		rating.Rating = &ratingInt
	}

	if newRating {
		if err := c.dal.AddRating(r.Context(), rating); err != nil {
			return err
		}
	} else {
		if err := c.dal.UpdateRating(r.Context(), rating); err != nil {
			return err
		}
	}

	http.Redirect(w, r, "/titles", http.StatusSeeOther)
	return nil
}

func averageRating(ratings []*domain.Rating) *float32 {
	var sum, count float32

	for _, r := range ratings {
		if r.Rating != nil {
			count++
			sum += float32(*r.Rating)
		}
	}

	if count != 0 {
		sum /= count
	}

	return &sum
}
