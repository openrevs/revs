package revs

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	tmdb "github.com/cyruzin/golang-tmdb"
	"github.com/go-chi/chi/v5"
	"github.com/openrevs/revs/domain"
	"github.com/openrevs/revs/util"
)

func (c *Client) uploadGet(w http.ResponseWriter, r *http.Request) error {
	user := currentUser(r)
	output, err := c.generateRatingsFile(r.Context(), user)
	if err != nil {
		return err
	}

	resp, err := http.Post("http://127.0.0.1:1323/upload", "application/jsonl", bytes.NewReader(output))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("upload failed: server returned %s", resp.Status)
	}

	return nil
}

func (c *Client) addGet(w http.ResponseWriter, r *http.Request) error {
	return c.render(w, "edit", domain.Rating{})
}

func (c *Client) addPost(w http.ResponseWriter, r *http.Request) error {
	var ratingID *int
	user := currentUser(r)

	idRaw := chi.URLParam(r, "id")
	if idRaw != "" {
		id, err := strconv.Atoi(idRaw)
		if err != nil {
			return err
		}

		rating, err := c.dal.Rating(r.Context(), id)
		if err != nil {
			return err
		}

		if rating.UserID != user.ID {
			return errors.New("This should be 403...")
		}

		ratingID = &id
	}

	if r.FormValue("delete") != "" {
		if ratingID == nil {
			return errors.New("Cannot delete without an ID")
		}

		if err := c.dal.DeleteRating(r.Context(), *ratingID); err != nil {
			return err
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return nil
	}

	itemType := r.FormValue("type")
	if itemType != "movie" && itemType != "tv" {
		return fmt.Errorf("unexpected item type %q", itemType)
	}

	title := domain.Title{
		Type: itemType,
	}

	if tmdbIDRaw := r.FormValue("tmdb_id"); tmdbIDRaw != "" {
		tmdbID, err := strconv.Atoi(tmdbIDRaw)
		if err != nil {
			return err
		}

		apikey := os.Getenv("TMDB_API_KEY")

		info, err := loadTMDBInfo(apikey, tmdbID, itemType)
		if err != nil {
			return err
		}
		title.Name = info.title
		year, err := strconv.Atoi(info.releaseDate[:4])

		if err != nil {
			return fmt.Errorf("invalid date: %s", info.releaseDate)
		}
		title.Year = year

		title.TMDBID = tmdbID // TODO: deal with nil
		title.TMDBPosterURL = info.posterPath
	} else {
		name := strings.TrimSpace(r.FormValue("name"))
		if name == "" {
			return errors.New("A title name must be provided")
		}
		title.Name = name

		yearStr := strings.TrimSpace(r.FormValue("year"))
		if yearStr != "" {
			year, err := strconv.Atoi(yearStr)
			if err != nil {
				return fmt.Errorf("invalid year %q", yearStr)
			}
			title.Year = year
		}
	}

	var rating *int
	if ratingRaw, err := strconv.Atoi(r.FormValue("rating")); err == nil {
		rating = &ratingRaw
	}

	if err := c.dal.AddTitle(r.Context(), &title); err != nil {
		return err
	}

	rt := domain.Rating{
		ID:      ratingID,
		TitleID: title.ID,
		UserID:  user.ID,
		Rating:  rating,
		Comment: util.StrPtrOrNil(r.FormValue("comment")),
		Updated: util.Now(),
	}

	if err := c.dal.AddRating(r.Context(), &rt); err != nil {
		return err
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
	return nil
}

type tmdbDetails struct {
	title       string
	releaseDate string
	posterPath  string
}

func loadTMDBInfo(apikey string, id int, itemType string) (*tmdbDetails, error) {
	tmdbClient, err := tmdb.Init(apikey)
	if err != nil {
		return nil, err
	}

	tmdbClient.SetClientConfig(http.Client{Timeout: time.Second * 5})
	tmdbClient.SetClientAutoRetry()

	deets := &tmdbDetails{}
	switch itemType {
	case "movie":
		details, err := tmdbClient.GetMovieDetails(id, nil)
		if err != nil {
			return nil, err
		}
		deets.title = details.Title
		deets.releaseDate = details.ReleaseDate
		deets.posterPath = details.PosterPath
	case "tv":
		details, err := tmdbClient.GetTVDetails(id, nil)
		if err != nil {
			return nil, err
		}
		deets.title = details.OriginalName
		deets.releaseDate = details.FirstAirDate
		deets.posterPath = details.PosterPath
	}
	return deets, nil
}

func (c *Client) updateGet(w http.ResponseWriter, r *http.Request) error {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		return err
	}

	rating, err := c.dal.Rating(r.Context(), id)
	if err != nil {
		return err
	}

	return c.render(w, "edit", rating)
}
