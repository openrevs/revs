package revs

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/openrevs/revs/domain"
)

func (c *Client) loginGetPost(w http.ResponseWriter, r *http.Request) error {
	if r.Method == http.MethodGet {
		return c.render(w, "login", nil)
	}

	login := r.FormValue("login")
	password := r.FormValue("password")

	user, err := c.dal.LoginUser(r.Context(), login, password)

	switch {
	case err == nil:
	case errors.Is(err, sql.ErrNoRows), errors.Is(err, domain.InvalidPassword):
		http.Redirect(w, r, LoginPath, http.StatusSeeOther)
		return nil
	default:
		return err
	}

	if err := c.sessions.RenewToken(r.Context()); err != nil {
		return err
	}
	c.sessions.Put(r.Context(), sessionUserKey, user.ID)

	http.Redirect(w, r, "/", http.StatusSeeOther)
	return nil
}

func (c *Client) logoutGet(w http.ResponseWriter, r *http.Request) error {
	if err := c.sessions.Destroy(r.Context()); err != nil {
		return err
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
	return nil
}

func (c *Client) indexGet(w http.ResponseWriter, r *http.Request) error {
	user := currentUser(r)
	ratings, err := c.dal.UserRatings(r.Context(), user.ID)
	if err != nil {
		return err
	}

	data := map[string]interface{}{
		"ratings":     ratings,
		"showPosters": user.ShowPosters,
	}

	return c.render(w, "index", data)
}
