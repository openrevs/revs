package dmrs

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/kalafut/dmrs/domain"
	"github.com/kalafut/dmrs/util"
	jsonlines "github.com/kalafut/sjsonl"
)

const settingsKey = "settings"

func (c *Client) profileGet(w http.ResponseWriter, r *http.Request) error {
	user := currentUser(r)
	data := map[string]interface{}{
		"User":  user,
		"email": util.StrFromPtr(user.Email),
	}

	return c.render(w, "profile", data)
}

func (c *Client) profilePost(w http.ResponseWriter, r *http.Request) error {
	user := currentUser(r)

	user.Email = util.StrPtrOrNil(r.FormValue("email"))

	min, err := strconv.Atoi(r.FormValue("min_rating"))
	if err != nil {
		return err
	}

	max, err := strconv.Atoi(r.FormValue("max_rating"))
	if err != nil {
		return err
	}

	if max <= min {
		return errors.New("max rating must be greater than min rating")
	}

	user.Minrating = min
	user.Maxrating = max
	user.ShowPosters = r.FormValue("show_posters") != ""

	if err := c.dal.UpdateUser(r.Context(), user); err != nil {
		return err
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
	return nil
}

func (c *Client) settingsGet(w http.ResponseWriter, r *http.Request) error {
	var settings domain.Settings

	err := c.dal.AppData(r.Context(), settingsKey, &settings)
	if err != nil && err != sql.ErrNoRows {
		return err
	}

	return c.render(w, "settings", settings)
}

func (c *Client) settingsPost(w http.ResponseWriter, r *http.Request) error {
	var settings domain.Settings

	settings.TMDBAPIKey = r.FormValue("tmdb_api_key")

	if err := c.dal.SetAppData(r.Context(), settingsKey, settings); err != nil {
		return err
	}

	if err := c.loadSettings(); err != nil {
		return err
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
	return nil
}

func (c *Client) registerGetPost(w http.ResponseWriter, r *http.Request) error {
	if r.Method == http.MethodGet {
		return c.render(w, "register", nil)
	}

	login := r.FormValue("login")
	password := r.FormValue("password")

	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	u := NewUser()

	user := &domain.User{
		Login:      login,
		Password:   hashed,
		Userid:     u.ID,
		Publickey:  u.PublicKey,
		Privatekey: u.PrivateKey,
		Minrating:  1,
		Maxrating:  5,
	}

	if err := c.dal.AddUser(r.Context(), user); err != nil {
		return err
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
	return nil
}

func (c *Client) ratingsetGet(w http.ResponseWriter, r *http.Request) error {
	user := currentUser(r)
	output, err := c.generateRatingsFile(r.Context(), user)
	if err != nil {
		return err
	}

	w.Header().Set("Content-Type", "application/jsonl")
	_, err = w.Write(output)
	return err
}

func (c *Client) generateRatingsFile(ctx context.Context, user *domain.User) ([]byte, error) {
	ratings, err := c.dal.UserRatings(ctx, user.ID)
	if err != nil {
		return nil, err
	}

	// One line per title, with all of the user's reviews of it attached.
	titleIdx := map[int]int{}
	var titles []Title2
	for _, r := range ratings {
		idx, ok := titleIdx[r.Title.ID]
		if !ok {
			t := Title2{
				Name: r.Title.Name,
			}
			if r.Title.Type != "" {
				t.Type = util.StrPtr(r.Title.Type)
			}
			if r.Title.Year != 0 {
				t.Year = util.IntPtr(r.Title.Year)
			}
			if r.Title.TMDBID != 0 {
				t.Refs = map[string]string{"TMDB": strconv.Itoa(r.Title.TMDBID)}
			}
			titles = append(titles, t)
			idx = len(titles) - 1
			titleIdx[r.Title.ID] = idx
		}

		review := Review2{
			Rating:  r.Rating,
			Comment: r.Comment,
		}
		if r.Date != nil {
			review.Date = util.StrPtr(r.Date.Format(time.RFC3339))
		}
		titles[idx].Reviews = append(titles[idx].Reviews, review)
	}

	// TODO: bump the rev only when content actually changed. For now every
	// export asserts a new rev so the server accepts it.
	user.Revision++
	if err := c.dal.UpdateUser(ctx, user); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	userLine := User2{
		ID:        user.Userid,
		Email:     user.Email,
		MinRating: user.Minrating,
		MaxRating: user.Maxrating,
		Revision:  user.Revision,
	}
	d, err := json.Marshal(userLine)
	if err != nil {
		return nil, err
	}
	buf.Write(d)
	buf.WriteByte('\n')

	for _, t := range titles {
		d, err := json.Marshal(t)
		if err != nil {
			return nil, err
		}
		buf.Write(d)
		buf.WriteByte('\n')
	}

	output, _, err := jsonlines.Sign(buf.Bytes(), ed25519.PrivateKey(user.Privatekey))
	if err != nil {
		return nil, err
	}

	return output, nil
}
