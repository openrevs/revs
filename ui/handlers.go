package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openrevs/revs/client"
	"github.com/openrevs/revs/collection"
	"github.com/openrevs/revs/collection/localdb"
)

// maxImportSize bounds an uploaded import file.
const maxImportSize = 64 << 20

// networkTimeout bounds a push or pull.
const networkTimeout = 2 * time.Minute

// Handler returns the UI's HTTP handler. With loopbackOnly, requests
// whose Host isn't a loopback name are refused, defeating DNS
// rebinding; unsafe methods are always protected against cross-origin
// requests.
func (a *App) Handler(loopbackOnly bool) http.Handler {
	r := chi.NewRouter()

	static, _ := fs.Sub(staticFS, "static")
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServerFS(static)))

	r.Get("/", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/movies", http.StatusFound) })

	r.Get("/movies", a.h(a.moviesList))
	r.Get("/movies/new", a.h(a.movieNew))
	r.Post("/movies", a.h(a.movieCreate))
	r.Get("/movies/{id}", a.h(a.movieShow))
	r.Get("/movies/{id}/edit", a.h(a.movieEdit))
	r.Post("/movies/{id}", a.h(a.movieUpdate))
	r.Post("/movies/{id}/delete", a.h(a.movieDelete))
	r.Post("/movies/{id}/viewings", a.h(a.viewingCreate))
	r.Post("/movies/{id}/rating", a.h(a.movieRate))
	r.Get("/movies/{id}/mine", a.h(a.movieMine))

	r.Get("/viewings/{id}/edit", a.h(a.viewingEdit))
	r.Post("/viewings/{id}", a.h(a.viewingUpdate))
	r.Post("/viewings/{id}/delete", a.h(a.viewingDelete))

	r.Get("/history", a.h(a.history))

	r.Get("/identity", a.h(a.identityShow))
	r.Post("/identity/generate", a.h(a.identityGenerate))
	r.Post("/identity/import", a.h(a.identityImport))
	r.Post("/identity/reveal", a.h(a.identityReveal))

	r.Get("/servers", a.h(a.serversList))
	r.Post("/servers", a.h(a.serverCreate))
	r.Get("/servers/{id}", a.h(a.serverShow))
	r.Post("/servers/{id}/push", a.h(a.serverPush))
	r.Post("/servers/{id}/pull", a.h(a.serverPull))
	r.Post("/servers/{id}/rebootstrap", a.h(a.serverRebootstrap))
	r.Post("/servers/{id}/reset", a.h(a.serverReset))
	r.Post("/servers/{id}/delete", a.h(a.serverDelete))
	r.Post("/servers/{id}/import-mine", a.h(a.serverImportMine))
	r.Get("/servers/{id}/users/{uid}", a.h(a.serverUser))

	r.Get("/data", a.h(a.dataPage))
	r.Get("/export.jsonl", a.h(a.export))
	r.Post("/import", a.h(a.importFile))
	r.Post("/import/csv", a.h(a.importCSV))

	var h http.Handler = r
	h = http.NewCrossOriginProtection().Handler(h)
	if loopbackOnly {
		h = requireLoopbackHost(h)
	}
	return h
}

func requireLoopbackHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			http.Error(w, "revs ui only answers to localhost", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) h(f func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := f(w, r)
		if err != nil && isDatastar(r) {
			// The page stays; say what went wrong in its flash area.
			msg := err.Error()
			var ue userError
			switch {
			case errors.As(err, &ue):
			case errors.Is(err, collection.ErrNotFound):
				msg = "That no longer exists. Reload the page."
			case errors.Is(err, collection.ErrNoKey):
				msg = "Create or import a key first."
			default:
				log.Printf("%s %s: %v", r.Method, r.URL.Path, err)
			}
			if perr := a.patchFlash(w, r, "error", msg); perr != nil {
				log.Printf("%s %s: %v", r.Method, r.URL.Path, perr)
			}
			return
		}
		var ue userError
		switch {
		case err == nil:
		case errors.As(err, &ue):
			redirect(w, r, localNext(r, "/movies"), "error", ue.Error())
		case errors.Is(err, collection.ErrNotFound):
			a.render(w, r, http.StatusNotFound, "error.html", view{Title: "Not found", Data: "That page doesn't exist."})
		case errors.Is(err, collection.ErrNoKey):
			redirect(w, r, "/identity", "error", "Create or import a key first.")
		default:
			log.Printf("%s %s: %v", r.Method, r.URL.Path, err)
			a.render(w, r, http.StatusInternalServerError, "error.html", view{Title: "Error", Data: err.Error()})
		}
	}
}

func idParam(r *http.Request, name string) (int64, error) {
	id, err := strconv.ParseInt(chi.URLParam(r, name), 10, 64)
	if err != nil {
		return 0, collection.ErrNotFound
	}
	return id, nil
}

// ---- movies ----

// typeFilter is one of the movie list's filters by type.
type typeFilter struct{ Type, Label string }

var typeFilters = []typeFilter{{"", "All"}, {collection.TypeMovie, "Movies"}, {collection.TypeTV, "TV series"}}

func (a *App) moviesList(w http.ResponseWriter, r *http.Request) error {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	typ := r.URL.Query().Get("type")
	if typ != collection.TypeMovie && typ != collection.TypeTV {
		typ = ""
	}
	movies, err := a.c.Movies(r.Context(), q, typ)
	if err != nil {
		return err
	}
	counts, err := a.c.Counts(r.Context())
	if err != nil {
		return err
	}
	a.render(w, r, http.StatusOK, "movies.html", view{Title: "Movies & TV", Nav: "movies", Data: map[string]any{
		"Query":   q,
		"Type":    typ,
		"Filters": typeFilters,
		"Movies":  movies,
		"Counts":  counts,
		"Next":    r.URL.RequestURI(),
	}})
	return nil
}

func movieFields(r *http.Request) collection.MovieFields {
	return collection.MovieFields{
		Type:     r.PostFormValue("type"),
		Title:    r.PostFormValue("title"),
		Year:     r.PostFormValue("year"),
		IMDB:     r.PostFormValue("imdb_id"),
		TMDB:     r.PostFormValue("tmdb_id"),
		Wikidata: r.PostFormValue("wikidata_id"),
	}
}

// viewingFields reads a posted viewing of an item of type typ.
func viewingFields(r *http.Request, typ string) collection.ViewingFields {
	return collection.ViewingFields{
		Type:    typ,
		Date:    r.PostFormValue("viewed_on"),
		Rating:  r.PostFormValue("rating"),
		Review:  r.PostFormValue("review"),
		Season:  r.PostFormValue("season"),
		Episode: r.PostFormValue("episode"),
	}
}

func (a *App) movieNew(w http.ResponseWriter, r *http.Request) error {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	results, searchErr := a.c.Search(r.Context(), q)
	// Results already in the collection show their current rating.
	ratings := map[string]int64{}
	if len(results) > 0 {
		mine, err := a.c.Movies(r.Context(), "", "")
		if err != nil {
			return err
		}
		for _, m := range mine {
			if m.WikidataID != nil {
				ratings[*m.WikidataID] = m.LatestRating
			}
		}
	}
	a.render(w, r, http.StatusOK, "movie_new.html", view{Title: "Add a movie or series", Nav: "movies", Data: map[string]any{
		"Next":      r.URL.RequestURI(),
		"Ratings":   ratings,
		"Query":     q,
		"Results":   results,
		"SearchErr": searchErr,
		"Form":      collection.MovieFields{Title: q},
	}})
	return nil
}

func (a *App) movieCreate(w http.ResponseWriter, r *http.Request) error {
	form := movieFields(r)
	in, err := form.Parse()
	rated := r.PostFormValue("rating") != ""
	if err == nil && rated {
		_, err = collection.ParseStars(r.PostFormValue("rating"))
	}
	if err != nil {
		if isDatastar(r) {
			return a.patchFlash(w, r, "error", err.Error())
		}
		a.render(w, r, http.StatusUnprocessableEntity, "movie_new.html", view{
			Title: "Add a movie or series", Nav: "movies", Flash: &flash{Kind: "error", Msg: err.Error()},
			Data: map[string]any{"Form": form},
		})
		return nil
	}

	m, existed, err := a.c.AddMovie(r.Context(), in)
	if err != nil {
		return err
	}
	if !rated {
		msg := fmt.Sprintf("Added %s.", m.Title)
		if existed {
			msg = fmt.Sprintf("%s is already in your collection.", m.Title)
		}
		return a.done(w, r, movieURL(m.ID), "", "ok", msg)
	}

	// A star on a search result: add and rate in one step, and stay on
	// the results.
	n, msg, err := a.rate(r, m, existed)
	if err != nil {
		return err
	}
	var wd string
	if m.WikidataID != nil {
		wd = *m.WikidataID
	}
	return a.done(w, r, localNext(r, movieURL(m.ID)), "movie_new.html", "ok", msg,
		frag("result-rate", map[string]any{"ID": wd, "Rating": n, "URL": movieURL(m.ID)}))
}

// movieRate sets a movie's rating in one step (see
// collection.SetRating), from a star bar, and patches the part of the
// page the bar is in: a movie list row (view=row) or a movie's page.
func (a *App) movieRate(w http.ResponseWriter, r *http.Request) error {
	id, err := idParam(r, "id")
	if err != nil {
		return err
	}
	m, err := a.c.Movie(r.Context(), id)
	if err != nil {
		return err
	}
	_, msg, err := a.rate(r, m, true)
	if err != nil {
		return err
	}
	if !isDatastar(r) {
		redirect(w, r, localNext(r, movieURL(m.ID)), "ok", msg)
		return nil
	}
	if r.PostFormValue("view") == "row" {
		row, err := a.c.MovieSummary(r.Context(), m.ID)
		if err != nil {
			return err
		}
		return a.patch(w, r, "movies.html", &flash{Kind: "ok", Msg: msg},
			frag("movie-row", map[string]any{"M": row, "Next": localNext(r, "/movies")}))
	}
	mine, err := a.mine(r, m)
	if err != nil {
		return err
	}
	return a.patch(w, r, "movie.html", &flash{Kind: "ok", Msg: msg}, frag("mine", mine))
}

// rate applies the posted rating to m, returning it and a message. A
// bad rating is a user error, returned as errUser.
func (a *App) rate(r *http.Request, m localdb.Movie, existed bool) (int64, string, error) {
	n, err := collection.ParseStars(r.PostFormValue("rating"))
	if err != nil {
		return 0, "", userError{err}
	}
	if _, _, err := a.c.SetRating(r.Context(), m.ID, n); err != nil {
		return 0, "", err
	}
	if !existed {
		return n, fmt.Sprintf("Added %s, rated %s", m.Title, collection.Stars(n)), nil
	}
	return n, fmt.Sprintf("Rated %s %s", m.Title, collection.Stars(n)), nil
}

// userError is a mistake in what someone entered, shown to them as is.
type userError struct{ error }

// localNext returns the posted "next" page to go back to, if it is a
// path on this site, or else fallback.
func localNext(r *http.Request, fallback string) string {
	next := r.PostFormValue("next")
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return fallback
	}
	return next
}

func movieURL(id int64) string { return "/movies/" + strconv.FormatInt(id, 10) }

// mineData is the "mine" section of a movie's page: your rating and
// viewings, with at most one viewing being edited in place (EditID)
// and the add-a-viewing form.
type mineData struct {
	Movie    localdb.Movie
	TV       bool // a TV series, whose viewings may be of seasons or episodes
	Viewings []localdb.Viewing
	Rating   *int64 // the newest whole-item viewing's, which the star bar sets
	Single   bool   // exactly one viewing, shown as "Your review"

	EditID   int64
	EditForm collection.ViewingFields
	EditErr  string

	Form    collection.ViewingFields
	AddOpen bool
	AddErr  string
}

// mine loads the "mine" section for m, with a blank add form and
// nothing being edited.
func (a *App) mine(r *http.Request, m localdb.Movie) (mineData, error) {
	vs, err := a.c.Viewings(r.Context(), m.ID)
	if err != nil {
		return mineData{}, err
	}
	d := mineData{Movie: m, TV: m.Type == collection.TypeTV, Viewings: vs, Single: len(vs) == 1, Form: collection.NewViewingFields(m.Type)}
	if v, ok := collection.LatestWhole(vs); ok {
		d.Rating = v.Rating
	}
	return d, nil
}

func (a *App) movieShow(w http.ResponseWriter, r *http.Request) error {
	id, err := idParam(r, "id")
	if err != nil {
		return err
	}
	m, err := a.c.Movie(r.Context(), id)
	if err != nil {
		return err
	}
	mine, err := a.mine(r, m)
	if err != nil {
		return err
	}
	return a.renderMovie(w, r, http.StatusOK, nil, mine)
}

// renderMovie renders a movie's whole page around mine.
func (a *App) renderMovie(w http.ResponseWriter, r *http.Request, status int, fl *flash, mine mineData) error {
	others, err := a.c.OthersReviews(r.Context(), mine.Movie)
	if err != nil {
		log.Printf("finding others' reviews: %v", err)
	}
	a.render(w, r, status, "movie.html", view{Title: mine.Movie.Title, Nav: "movies", Flash: fl, Data: map[string]any{
		"Mine":   mine,
		"Others": others,
	}})
	return nil
}

// movieMine re-renders a movie's "mine" section, optionally with a
// viewing open for editing (?edit=id). Datastar only.
func (a *App) movieMine(w http.ResponseWriter, r *http.Request) error {
	id, err := idParam(r, "id")
	if err != nil {
		return err
	}
	if !isDatastar(r) {
		http.Redirect(w, r, movieURL(id), http.StatusSeeOther)
		return nil
	}
	m, err := a.c.Movie(r.Context(), id)
	if err != nil {
		return err
	}
	mine, err := a.mine(r, m)
	if err != nil {
		return err
	}
	if e, err := strconv.ParseInt(r.URL.Query().Get("edit"), 10, 64); err == nil {
		for _, v := range mine.Viewings {
			if v.ID == e {
				mine.EditID, mine.EditForm = v.ID, collection.ViewingFieldsFrom(m.Type, v)
			}
		}
	}
	return a.patch(w, r, "movie.html", nil, frag("mine", mine))
}

func (a *App) movieEdit(w http.ResponseWriter, r *http.Request) error {
	id, err := idParam(r, "id")
	if err != nil {
		return err
	}
	m, err := a.c.Movie(r.Context(), id)
	if err != nil {
		return err
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	results, searchErr := a.c.Search(r.Context(), q)

	a.render(w, r, http.StatusOK, "movie_edit.html", view{Title: "Edit " + m.Title, Nav: "movies", Data: map[string]any{
		"Movie":     m,
		"Form":      collection.MovieFieldsFrom(m),
		"Query":     q,
		"Results":   results,
		"SearchErr": searchErr,
	}})
	return nil
}

func (a *App) movieUpdate(w http.ResponseWriter, r *http.Request) error {
	id, err := idParam(r, "id")
	if err != nil {
		return err
	}
	m, err := a.c.Movie(r.Context(), id)
	if err != nil {
		return err
	}

	form := movieFields(r)
	in, err := form.Parse()
	if err == nil {
		err = a.c.UpdateMovie(r.Context(), id, in)
		if err != nil && !errors.Is(err, collection.ErrHasSeasons) {
			return err
		}
	}
	if err != nil {
		a.render(w, r, http.StatusUnprocessableEntity, "movie_edit.html", view{
			Title: "Edit " + m.Title, Nav: "movies", Flash: &flash{Kind: "error", Msg: err.Error()},
			Data: map[string]any{"Movie": m, "Form": form},
		})
		return nil
	}
	redirect(w, r, movieURL(id), "ok", "Saved.")
	return nil
}

func (a *App) movieDelete(w http.ResponseWriter, r *http.Request) error {
	id, err := idParam(r, "id")
	if err != nil {
		return err
	}
	m, err := a.c.Movie(r.Context(), id)
	if err != nil {
		return err
	}
	if err := a.c.DeleteMovie(r.Context(), id); err != nil {
		return err
	}
	redirect(w, r, "/movies", "ok", fmt.Sprintf("Deleted %s and its viewings.", m.Title))
	return nil
}

// ---- viewings ----

func (a *App) viewingCreate(w http.ResponseWriter, r *http.Request) error {
	id, err := idParam(r, "id")
	if err != nil {
		return err
	}
	m, err := a.c.Movie(r.Context(), id)
	if err != nil {
		return err
	}
	mine, err := a.mine(r, m)
	if err != nil {
		return err
	}
	form := viewingFields(r, m.Type)
	in, err := form.Parse()
	if err != nil {
		mine.Form, mine.AddOpen = form, true
		if isDatastar(r) {
			mine.AddErr = err.Error()
			return a.patch(w, r, "movie.html", nil, frag("mine", mine))
		}
		return a.renderMovie(w, r, http.StatusUnprocessableEntity, &flash{Kind: "error", Msg: err.Error()}, mine)
	}
	if _, err := a.c.AddViewing(r.Context(), id, in); err != nil {
		return err
	}
	return a.doneMine(w, r, m, "Viewing added.")
}

// doneMine finishes a change to one of m's viewings: the page's "mine"
// section is patched, or the page reloaded.
func (a *App) doneMine(w http.ResponseWriter, r *http.Request, m localdb.Movie, msg string) error {
	if !isDatastar(r) {
		redirect(w, r, movieURL(m.ID), "ok", msg)
		return nil
	}
	mine, err := a.mine(r, m)
	if err != nil {
		return err
	}
	return a.patch(w, r, "movie.html", &flash{Kind: "ok", Msg: msg}, frag("mine", mine))
}

// viewingEdit is the page for editing a viewing without JavaScript;
// with it, the movie page edits viewings in place.
func (a *App) viewingEdit(w http.ResponseWriter, r *http.Request) error {
	id, err := idParam(r, "id")
	if err != nil {
		return err
	}
	v, err := a.c.Viewing(r.Context(), id)
	if err != nil {
		return err
	}
	m, err := a.c.Movie(r.Context(), v.MovieID)
	if err != nil {
		return err
	}
	a.render(w, r, http.StatusOK, "viewing_edit.html", view{Title: "Edit viewing", Nav: "movies", Data: map[string]any{
		"Movie": m, "Viewing": v, "Form": collection.ViewingFieldsFrom(m.Type, v),
	}})
	return nil
}

func (a *App) viewingUpdate(w http.ResponseWriter, r *http.Request) error {
	id, err := idParam(r, "id")
	if err != nil {
		return err
	}
	v, err := a.c.Viewing(r.Context(), id)
	if err != nil {
		return err
	}
	m, err := a.c.Movie(r.Context(), v.MovieID)
	if err != nil {
		return err
	}
	form := viewingFields(r, m.Type)
	in, err := form.Parse()
	if err != nil {
		if isDatastar(r) {
			mine, merr := a.mine(r, m)
			if merr != nil {
				return merr
			}
			mine.EditID, mine.EditForm, mine.EditErr = v.ID, form, err.Error()
			return a.patch(w, r, "movie.html", nil, frag("mine", mine))
		}
		a.render(w, r, http.StatusUnprocessableEntity, "viewing_edit.html", view{
			Title: "Edit viewing", Nav: "movies", Flash: &flash{Kind: "error", Msg: err.Error()},
			Data: map[string]any{"Movie": m, "Viewing": v, "Form": form},
		})
		return nil
	}
	if err := a.c.UpdateViewing(r.Context(), id, in); err != nil {
		return err
	}
	return a.doneMine(w, r, m, "Saved.")
}

func (a *App) viewingDelete(w http.ResponseWriter, r *http.Request) error {
	id, err := idParam(r, "id")
	if err != nil {
		return err
	}
	v, err := a.c.Viewing(r.Context(), id)
	if err != nil {
		return err
	}
	m, err := a.c.Movie(r.Context(), v.MovieID)
	if err != nil {
		return err
	}
	if err := a.c.DeleteViewing(r.Context(), id); err != nil {
		return err
	}
	return a.doneMine(w, r, m, "Viewing deleted.")
}

// history lists viewings by date watched or, with sort=added, most
// recently added first (the way to find undated ones).
func (a *App) history(w http.ResponseWriter, r *http.Request) error {
	byAdded := r.URL.Query().Get("sort") == "added"
	list := a.c.History
	if byAdded {
		list = a.c.RecentlyAdded
	}
	rows, err := list(r.Context(), 500)
	if err != nil {
		return err
	}
	a.render(w, r, http.StatusOK, "history.html", view{Title: "History", Nav: "history", Data: map[string]any{
		"Rows": rows, "ByAdded": byAdded,
	}})
	return nil
}

// ---- identity ----

type identityData struct {
	collection.Identity
	PrivateKey string // only when revealed
}

func (a *App) identityShow(w http.ResponseWriter, r *http.Request) error {
	a.render(w, r, http.StatusOK, "identity.html", view{Title: "Identity", Nav: "identity",
		Data: identityData{Identity: a.c.Identity(r.Context())}})
	return nil
}

func (a *App) identityReveal(w http.ResponseWriter, r *http.Request) error {
	d := identityData{Identity: a.c.Identity(r.Context()), PrivateKey: a.c.PrivateKeyText(r.Context())}
	a.render(w, r, http.StatusOK, "identity.html", view{Title: "Identity", Nav: "identity", Data: d})
	return nil
}

// replacingKey reports whether a key exists and the user hasn't
// confirmed replacing it, redirecting with an explanation if so.
func (a *App) replacingKey(w http.ResponseWriter, r *http.Request) bool {
	if !a.c.HasKey(r.Context()) || r.PostFormValue("confirm_replace") == "yes" {
		return false
	}
	redirect(w, r, "/identity", "error", "You already have a key. Tick the confirmation box to replace it.")
	return true
}

func (a *App) identityGenerate(w http.ResponseWriter, r *http.Request) error {
	if a.replacingKey(w, r) {
		return nil
	}
	if err := a.c.GenerateKey(r.Context()); err != nil {
		return err
	}
	redirect(w, r, "/identity", "ok", "Generated a new key. Back up the private key somewhere safe.")
	return nil
}

func (a *App) identityImport(w http.ResponseWriter, r *http.Request) error {
	if a.replacingKey(w, r) {
		return nil
	}
	if err := a.c.ImportKey(r.Context(), r.PostFormValue("private_key")); err != nil {
		redirect(w, r, "/identity", "error", err.Error())
		return nil
	}
	redirect(w, r, "/identity", "ok", "Key imported.")
	return nil
}

// ---- servers ----

func (a *App) serversList(w http.ResponseWriter, r *http.Request) error {
	servers, err := a.c.Servers(r.Context())
	if err != nil {
		return err
	}
	a.render(w, r, http.StatusOK, "servers.html", view{Title: "Servers", Nav: "servers", Data: servers})
	return nil
}

func (a *App) serverCreate(w http.ResponseWriter, r *http.Request) error {
	s, err := a.c.AddServer(r.Context(), r.PostFormValue("url"))
	switch {
	case errors.Is(err, collection.ErrBadServerURL), errors.Is(err, collection.ErrServerExists):
		redirect(w, r, "/servers", "error", err.Error())
		return nil
	case err != nil:
		return err
	}
	redirect(w, r, serverURL(s.ID), "ok", "Server added. Pull to fetch its reviews, or push yours.")
	return nil
}

func serverURL(id int64) string { return "/servers/" + strconv.FormatInt(id, 10) }

func (a *App) serverShow(w http.ResponseWriter, r *http.Request) error {
	id, err := idParam(r, "id")
	if err != nil {
		return err
	}
	st, err := a.c.ServerStatus(r.Context(), id)
	if err != nil {
		return err
	}
	a.render(w, r, http.StatusOK, "server.html", view{Title: st.Server.Url, Nav: "servers", Data: map[string]any{
		"Server":    st.Server,
		"Epoch":     st.Epoch,
		"Users":     st.Users,
		"Mine":      st.Mine,
		"MyID":      st.MyID,
		"Conflict":  r.URL.Query().Get("conflict"),
		"Diverged":  r.URL.Query().Get("diverged") != "",
		"KeyChange": r.URL.Query().Get("keychanged") != "",
	}})
	return nil
}

func (a *App) serverPush(w http.ResponseWriter, r *http.Request) error {
	id, err := idParam(r, "id")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(r.Context(), networkTimeout)
	defer cancel()
	push := a.c.Push
	if r.PostFormValue("force") != "" {
		push = a.c.ForcePush
	}
	res, err := push(ctx, id)

	var stale *client.StaleError
	switch {
	case errors.As(err, &stale):
		redirect(w, r, serverURL(id)+"?conflict="+strconv.Itoa(stale.LatestRev), "", "")
		return nil
	case errors.Is(err, collection.ErrNoKey), errors.Is(err, collection.ErrNotFound):
		return err
	case err != nil:
		redirect(w, r, serverURL(id), "error", "Push failed: "+err.Error())
		return nil
	}
	redirect(w, r, serverURL(id), "ok", collection.PushMessage(res))
	return nil
}

func (a *App) serverPull(w http.ResponseWriter, r *http.Request) error {
	return a.doPull(w, r, false)
}

func (a *App) serverRebootstrap(w http.ResponseWriter, r *http.Request) error {
	return a.doPull(w, r, true)
}

func (a *App) doPull(w http.ResponseWriter, r *http.Request, rebootstrap bool) error {
	id, err := idParam(r, "id")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(r.Context(), networkTimeout)
	defer cancel()
	res, err := a.c.Pull(ctx, id, rebootstrap)

	var diverged *client.DivergedError
	var keyChanged *client.KeyChangedError
	switch {
	case errors.As(err, &diverged):
		redirect(w, r, serverURL(id)+"?diverged=1", "", "")
		return nil
	case errors.As(err, &keyChanged):
		redirect(w, r, serverURL(id)+"?keychanged=1", "", "")
		return nil
	case errors.Is(err, collection.ErrNotFound):
		return err
	case err != nil:
		redirect(w, r, serverURL(id), "error", "Pull failed: "+err.Error())
		return nil
	}
	redirect(w, r, serverURL(id), "ok", collection.PullMessage(res))
	return nil
}

func (a *App) serverReset(w http.ResponseWriter, r *http.Request) error {
	id, err := idParam(r, "id")
	if err != nil {
		return err
	}
	if err := a.c.ResetServer(r.Context(), id); err != nil {
		return err
	}
	redirect(w, r, serverURL(id), "ok", "Discarded everything pulled from this server, including its pinned key. Pull to start fresh.")
	return nil
}

func (a *App) serverDelete(w http.ResponseWriter, r *http.Request) error {
	id, err := idParam(r, "id")
	if err != nil {
		return err
	}
	s, err := a.c.Server(r.Context(), id)
	if err != nil {
		return err
	}
	if err := a.c.RemoveServer(r.Context(), id); err != nil {
		return err
	}
	redirect(w, r, "/servers", "ok", "Removed "+s.Url+".")
	return nil
}

// serverImportMine merges the collection this server holds for the
// local user (e.g. pushed from another device) into the local one.
func (a *App) serverImportMine(w http.ResponseWriter, r *http.Request) error {
	id, err := idParam(r, "id")
	if err != nil {
		return err
	}
	rev, stats, err := a.c.ImportMine(r.Context(), id)
	if errors.Is(err, collection.ErrNotPublished) {
		redirect(w, r, serverURL(id), "error", "This server has no published collection for you yet. Pull first.")
		return nil
	}
	if err != nil {
		return err
	}
	redirect(w, r, serverURL(id), "ok", fmt.Sprintf("Merged revision %d. %s", rev, stats))
	return nil
}

func (a *App) serverUser(w http.ResponseWriter, r *http.Request) error {
	id, err := idParam(r, "id")
	if err != nil {
		return err
	}
	s, err := a.c.Server(r.Context(), id)
	if err != nil {
		return err
	}
	uid := chi.URLParam(r, "uid")
	u, titles, err := a.c.ServerUser(r.Context(), id, uid)
	if err != nil {
		return err
	}
	a.render(w, r, http.StatusOK, "server_user.html", view{Title: "User " + collection.ShortID(uid), Nav: "servers", Data: map[string]any{
		"Server": s, "User": u, "Titles": titles, "IsMe": uid == a.c.UserID(r.Context()),
	}})
	return nil
}

// ---- import / export ----

func (a *App) dataPage(w http.ResponseWriter, r *http.Request) error {
	counts, err := a.c.Counts(r.Context())
	if err != nil {
		return err
	}
	a.render(w, r, http.StatusOK, "data.html", view{Title: "Import & export", Nav: "data", Data: map[string]any{
		"Counts":   counts,
		"HasKey":   a.c.HasKey(r.Context()),
		"MaxStars": collection.MaxStars,
	}})
	return nil
}

func (a *App) export(w http.ResponseWriter, r *http.Request) error {
	signed, rev, err := a.c.Export(r.Context())
	if errors.Is(err, collection.ErrNoKey) {
		redirect(w, r, "/identity", "error", "Create or import a key before exporting: exports are signed.")
		return nil
	}
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/jsonl")
	w.Header().Set("Content-Disposition", `attachment; filename="`+collection.ExportFilename(rev)+`"`)
	_, err = w.Write(signed)
	return err
}

// uploadedFile reads the "file" field of an import form. It returns
// nil data, having redirected, when no file was chosen.
func uploadedFile(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxImportSize)
	f, _, err := r.FormFile("file")
	if err != nil {
		redirect(w, r, "/data", "error", "Choose a file to import.")
		return nil, nil
	}
	defer f.Close()
	return io.ReadAll(f)
}

func (a *App) importFile(w http.ResponseWriter, r *http.Request) error {
	data, err := uploadedFile(w, r)
	if data == nil || err != nil {
		return err
	}

	res, err := a.c.Import(r.Context(), data)
	if errors.Is(err, collection.ErrUnreadableFile) {
		redirect(w, r, "/data", "error", "Could not read that file: "+errors.Unwrap(err).Error())
		return nil
	}
	if err != nil {
		return err
	}
	redirect(w, r, "/data", "ok", res.String())
	return nil
}

func (a *App) importCSV(w http.ResponseWriter, r *http.Request) error {
	data, err := uploadedFile(w, r)
	if data == nil || err != nil {
		return err
	}

	stats, err := a.c.ImportCSV(r.Context(), data)
	if errors.Is(err, collection.ErrUnreadableFile) {
		redirect(w, r, "/data", "error", "Could not read that CSV file: "+errors.Unwrap(err).Error())
		return nil
	}
	if err != nil {
		return err
	}
	redirect(w, r, "/data", "ok", stats.String())
	return nil
}
