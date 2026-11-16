package revs

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/openrevs/revs/domain"
	"github.com/openrevs/revs/util"
	jsonlines "github.com/openrevs/sjsonl-go"
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

	// One item line per title, with all of the user's entries for it.
	itemIdx := map[int]int{}
	var items []Item
	for _, r := range ratings {
		idx, ok := itemIdx[r.Title.ID]
		if !ok {
			it := Item{Type: ItemTypeMovie, Title: r.Title.Name}
			if r.Title.Type != "" {
				it.Type = strings.ToLower(r.Title.Type)
			}
			if r.Title.Year != 0 {
				it.Year = util.IntPtr(r.Title.Year)
			}
			if r.Title.TMDBID != 0 {
				it.Refs = map[string]string{"tmdb": strconv.Itoa(r.Title.TMDBID)}
			}
			items = append(items, it)
			idx = len(items) - 1
			itemIdx[r.Title.ID] = idx
		}

		entry := Entry{
			Rating: r.Rating,
			Review: r.Comment,
		}
		if r.Date != nil {
			entry.Date = util.StrPtr(r.Date.Format(time.DateOnly))
		}
		items[idx].Entries = append(items[idx].Entries, entry)
	}

	// TODO: bump the rev only when content actually changed. For now every
	// export asserts a new rev so the server accepts it.
	user.Revision++
	if err := c.dal.UpdateUser(ctx, user); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	// The author line carries no identifying details such as email.
	authorLine := Author{
		ID:        user.Userid,
		MinRating: user.Minrating,
		MaxRating: user.Maxrating,
		Revision:  user.Revision,
	}
	d, err := EncodeLine(authorLine)
	if err != nil {
		return nil, err
	}
	buf.Write(d)
	buf.WriteByte('\n')

	for _, it := range items {
		d, err := EncodeLine(it.Normalize())
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
