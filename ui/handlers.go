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
		switch {
		case err == nil:
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

func (a *App) moviesList(w http.ResponseWriter, r *http.Request) error {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	movies, err := a.c.Movies(r.Context(), q)
	if err != nil {
		return err
	}
	counts, err := a.c.Counts(r.Context())
	if err != nil {
		return err
	}
	a.render(w, r, http.StatusOK, "movies.html", view{Title: "Movies", Nav: "movies", Data: map[string]any{
		"Query":  q,
		"Movies": movies,
		"Counts": counts,
	}})
	return nil
}

func movieFields(r *http.Request) collection.MovieFields {
	return collection.MovieFields{
		Title:    r.PostFormValue("title"),
		Year:     r.PostFormValue("year"),
		IMDB:     r.PostFormValue("imdb_id"),
		TMDB:     r.PostFormValue("tmdb_id"),
		Wikidata: r.PostFormValue("wikidata_id"),
	}
}

func viewingFields(r *http.Request) collection.ViewingFields {
	return collection.ViewingFields{
		Date:   r.PostFormValue("viewed_on"),
		Rating: r.PostFormValue("rating"),
		Review: r.PostFormValue("review"),
	}
}

func (a *App) movieNew(w http.ResponseWriter, r *http.Request) error {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	results, searchErr := a.c.Search(r.Context(), q)
	a.render(w, r, http.StatusOK, "movie_new.html", view{Title: "Add a movie", Nav: "movies", Data: map[string]any{
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
	if err != nil {
		a.render(w, r, http.StatusUnprocessableEntity, "movie_new.html", view{
			Title: "Add a movie", Nav: "movies", Flash: &flash{Kind: "error", Msg: err.Error()},
			Data: map[string]any{"Form": form},
		})
		return nil
	}

	m, existed, err := a.c.AddMovie(r.Context(), in)
	if err != nil {
		return err
	}
	if existed {
		redirect(w, r, movieURL(m.ID), "ok", fmt.Sprintf("%s is already in your collection.", m.Title))
		return nil
	}
	redirect(w, r, movieURL(m.ID), "ok", fmt.Sprintf("Added %s.", m.Title))
	return nil
}

func movieURL(id int64) string { return "/movies/" + strconv.FormatInt(id, 10) }

func (a *App) movieShow(w http.ResponseWriter, r *http.Request) error {
	return a.renderMovie(w, r, http.StatusOK, nil, collection.NewViewingFields())
}

// renderMovie renders a movie's page, with form in its add-viewing form.
func (a *App) renderMovie(w http.ResponseWriter, r *http.Request, status int, fl *flash, form collection.ViewingFields) error {
	id, err := idParam(r, "id")
	if err != nil {
		return err
	}
	m, err := a.c.Movie(r.Context(), id)
	if err != nil {
		return err
	}
	viewings, err := a.c.Viewings(r.Context(), id)
	if err != nil {
		return err
	}
	others, err := a.c.OthersReviews(r.Context(), m)
	if err != nil {
		log.Printf("finding others' reviews: %v", err)
	}

	a.render(w, r, status, "movie.html", view{Title: m.Title, Nav: "movies", Flash: fl, Data: map[string]any{
		"Movie":    m,
		"Viewings": viewings,
		"Form":     form,
		"Others":   others,
	}})
	return nil
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
	if err != nil {
		a.render(w, r, http.StatusUnprocessableEntity, "movie_edit.html", view{
			Title: "Edit " + m.Title, Nav: "movies", Flash: &flash{Kind: "error", Msg: err.Error()},
			Data: map[string]any{"Movie": m, "Form": form},
		})
		return nil
	}
	if err := a.c.UpdateMovie(r.Context(), id, in); err != nil {
		return err
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
	form := viewingFields(r)
	in, err := form.Parse()
	if err != nil {
		return a.renderMovie(w, r, http.StatusUnprocessableEntity, &flash{Kind: "error", Msg: err.Error()}, form)
	}
	if _, err := a.c.AddViewing(r.Context(), id, in); err != nil {
		return err
	}
	redirect(w, r, movieURL(id), "ok", "Viewing added.")
	return nil
}

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
		"Movie": m, "Viewing": v, "Form": collection.ViewingFieldsFrom(v),
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

	form := viewingFields(r)
	in, err := form.Parse()
	if err != nil {
		m, merr := a.c.Movie(r.Context(), v.MovieID)
		if merr != nil {
			return merr
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
	redirect(w, r, movieURL(v.MovieID), "ok", "Viewing saved.")
	return nil
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
	if err := a.c.DeleteViewing(r.Context(), id); err != nil {
		return err
	}
	redirect(w, r, movieURL(v.MovieID), "ok", "Viewing deleted.")
	return nil
}

func (a *App) history(w http.ResponseWriter, r *http.Request) error {
	rows, err := a.c.History(r.Context(), 500)
	if err != nil {
		return err
	}
	a.render(w, r, http.StatusOK, "history.html", view{Title: "History", Nav: "history", Data: rows})
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
	res, err := a.c.Push(ctx, id)

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
		"Counts": counts,
		"HasKey": a.c.HasKey(r.Context()),
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

func (a *App) importFile(w http.ResponseWriter, r *http.Request) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxImportSize)
	f, _, err := r.FormFile("file")
	if err != nil {
		redirect(w, r, "/data", "error", "Choose a file to import.")
		return nil
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
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
