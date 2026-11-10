package ui

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openrevs/revs/client"
	"github.com/openrevs/revs/keys"
	"github.com/openrevs/revs/ui/localdb"
	"github.com/openrevs/revs/ui/remotedb"
)

// networkTimeout bounds a push or pull.
const networkTimeout = 2 * time.Minute

// Wikidata search timeouts: searchTimeout normally, and the shorter
// stockSearchTimeout when the built-in list already has matches to
// show. Variables so tests can shorten them.
var (
	searchTimeout      = 15 * time.Second
	stockSearchTimeout = 4 * time.Second
)

// maxImportSize bounds an uploaded import file.
const maxImportSize = 64 << 20

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

// errNotFound renders as a 404.
var errNotFound = errors.New("not found")

func (a *App) h(f func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := f(w, r)
		switch {
		case err == nil:
		case errors.Is(err, errNotFound), errors.Is(err, sql.ErrNoRows):
			a.render(w, r, http.StatusNotFound, "error.html", view{Title: "Not found", Data: "That page doesn't exist."})
		default:
			log.Printf("%s %s: %v", r.Method, r.URL.Path, err)
			a.render(w, r, http.StatusInternalServerError, "error.html", view{Title: "Error", Data: err.Error()})
		}
	}
}

func idParam(r *http.Request, name string) (int64, error) {
	id, err := strconv.ParseInt(chi.URLParam(r, name), 10, 64)
	if err != nil {
		return 0, errNotFound
	}
	return id, nil
}

// ---- movies ----

func (a *App) moviesList(w http.ResponseWriter, r *http.Request) error {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	movies, err := a.q.ListMovies(r.Context(), q)
	if err != nil {
		return err
	}
	counts, err := a.q.CountMoviesAndViewings(r.Context())
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

// wikidataSearch runs a search for the movie forms. Matches from the
// built-in stock list come first, followed by any other Wikidata hits.
// A Wikidata failure (e.g. when offline) is returned as a message to
// show alongside whatever the stock list found, never as an error.
func (a *App) wikidataSearch(ctx context.Context, q string) ([]WikidataResult, string) {
	if q == "" {
		return nil, ""
	}
	results := searchStock(q)

	timeout := searchTimeout
	if len(results) > 0 {
		timeout = stockSearchTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	remote, err := a.wikidata.Search(ctx, q)
	if err != nil {
		log.Printf("wikidata search %q: %v", q, err)
		var uerr *url.Error
		if errors.As(err, &uerr) || errors.Is(err, context.DeadlineExceeded) {
			return results, "Couldn't reach Wikidata; you may be offline."
		}
		return results, err.Error()
	}
	for _, r := range remote {
		if !slices.ContainsFunc(results, func(s WikidataResult) bool { return s.ID == r.ID }) {
			results = append(results, r)
		}
	}
	return results, ""
}

func (a *App) movieNew(w http.ResponseWriter, r *http.Request) error {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	results, searchErr := a.wikidataSearch(r.Context(), q)
	a.render(w, r, http.StatusOK, "movie_new.html", view{Title: "Add a movie", Nav: "movies", Data: map[string]any{
		"Query":     q,
		"Results":   results,
		"SearchErr": searchErr,
		"Form":      movieForm{Title: q},
	}})
	return nil
}

func (a *App) movieCreate(w http.ResponseWriter, r *http.Request) error {
	form, in, err := parseMovieForm(r)
	if err != nil {
		a.render(w, r, http.StatusUnprocessableEntity, "movie_new.html", view{
			Title: "Add a movie", Nav: "movies", Flash: &flash{Kind: "error", Msg: err.Error()},
			Data: map[string]any{"Form": form},
		})
		return nil
	}

	// The same film picked from Wikidata twice is almost surely a
	// mistake; send the user to the existing entry instead.
	if in.Wikidata != nil {
		existing, err := a.q.FindMovieByWikidata(r.Context(), in.Wikidata)
		if err == nil {
			redirect(w, r, movieURL(existing.ID), "ok", fmt.Sprintf("%s is already in your collection.", existing.Title))
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}

	m, err := a.q.CreateMovie(r.Context(), in.createParams())
	if err != nil {
		return err
	}
	redirect(w, r, movieURL(m.ID), "ok", fmt.Sprintf("Added %s.", m.Title))
	return nil
}

func movieURL(id int64) string { return "/movies/" + strconv.FormatInt(id, 10) }

func (a *App) movieShow(w http.ResponseWriter, r *http.Request) error {
	id, err := idParam(r, "id")
	if err != nil {
		return err
	}
	m, err := a.q.GetMovie(r.Context(), id)
	if err != nil {
		return err
	}
	viewings, err := a.q.ListViewingsForMovie(r.Context(), id)
	if err != nil {
		return err
	}
	others, err := a.othersReviews(r.Context(), m)
	if err != nil {
		log.Printf("finding others' reviews: %v", err)
	}

	a.render(w, r, http.StatusOK, "movie.html", view{Title: m.Title, Nav: "movies", Data: map[string]any{
		"Movie":    m,
		"Viewings": viewings,
		"Form":     viewingForm{Date: time.Now().Format(dateLayout), Rating: "none"},
		"Others":   others,
	}})
	return nil
}

// otherReview is someone else's review of a movie, from a server.
type otherReview struct {
	remotedb.MatchingReviewsRow
	ServerID int64
}

// othersReviews collects pulled reviews of m from every server, with
// reviews seen on several servers listed once.
func (a *App) othersReviews(ctx context.Context, m localdb.Movie) ([]otherReview, error) {
	servers, err := a.q.ListServers(ctx)
	if err != nil {
		return nil, err
	}

	var out []otherReview
	seen := map[string]bool{}
	for _, s := range servers {
		rem, err := a.remote(s.ID, s.Url)
		if err != nil {
			return out, err
		}
		rows, err := rem.q.MatchingReviews(ctx, remotedb.MatchingReviewsParams{
			ExcludeUser: a.userID(ctx),
			WikidataID:  m.WikidataID,
			ImdbID:      m.ImdbID,
			TmdbID:      m.TmdbID,
			Name:        m.Title,
			Year:        m.Year,
		})
		if err != nil {
			return out, err
		}
		for _, row := range rows {
			k := fmt.Sprintf("%s|%s|%d|%s", row.Userid, deref(row.ViewedOn), deref(row.Rating), row.Comment)
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, otherReview{MatchingReviewsRow: row, ServerID: s.ID})
		}
	}
	return out, nil
}

func (a *App) movieEdit(w http.ResponseWriter, r *http.Request) error {
	id, err := idParam(r, "id")
	if err != nil {
		return err
	}
	m, err := a.q.GetMovie(r.Context(), id)
	if err != nil {
		return err
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	results, searchErr := a.wikidataSearch(r.Context(), q)

	a.render(w, r, http.StatusOK, "movie_edit.html", view{Title: "Edit " + m.Title, Nav: "movies", Data: map[string]any{
		"Movie":     m,
		"Form":      movieFormFrom(m),
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
	m, err := a.q.GetMovie(r.Context(), id)
	if err != nil {
		return err
	}

	form, in, err := parseMovieForm(r)
	if err != nil {
		a.render(w, r, http.StatusUnprocessableEntity, "movie_edit.html", view{
			Title: "Edit " + m.Title, Nav: "movies", Flash: &flash{Kind: "error", Msg: err.Error()},
			Data: map[string]any{"Movie": m, "Form": form},
		})
		return nil
	}
	if err := a.q.UpdateMovie(r.Context(), in.updateParams(id)); err != nil {
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
	m, err := a.q.GetMovie(r.Context(), id)
	if err != nil {
		return err
	}
	if err := a.q.DeleteMovie(r.Context(), id); err != nil {
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
	m, err := a.q.GetMovie(r.Context(), id)
	if err != nil {
		return err
	}

	form, in, err := parseViewingForm(r)
	if err != nil {
		viewings, lerr := a.q.ListViewingsForMovie(r.Context(), id)
		if lerr != nil {
			return lerr
		}
		a.render(w, r, http.StatusUnprocessableEntity, "movie.html", view{
			Title: m.Title, Nav: "movies", Flash: &flash{Kind: "error", Msg: err.Error()},
			Data: map[string]any{"Movie": m, "Viewings": viewings, "Form": form},
		})
		return nil
	}

	_, err = a.q.CreateViewing(r.Context(), localdb.CreateViewingParams{
		MovieID: id, ViewedOn: in.Date, Rating: in.Rating, Review: in.Review,
	})
	if err != nil {
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
	v, err := a.q.GetViewing(r.Context(), id)
	if err != nil {
		return err
	}
	m, err := a.q.GetMovie(r.Context(), v.MovieID)
	if err != nil {
		return err
	}
	a.render(w, r, http.StatusOK, "viewing_edit.html", view{Title: "Edit viewing", Nav: "movies", Data: map[string]any{
		"Movie": m, "Viewing": v, "Form": viewingFormFrom(v),
	}})
	return nil
}

func (a *App) viewingUpdate(w http.ResponseWriter, r *http.Request) error {
	id, err := idParam(r, "id")
	if err != nil {
		return err
	}
	v, err := a.q.GetViewing(r.Context(), id)
	if err != nil {
		return err
	}

	form, in, err := parseViewingForm(r)
	if err != nil {
		m, merr := a.q.GetMovie(r.Context(), v.MovieID)
		if merr != nil {
			return merr
		}
		a.render(w, r, http.StatusUnprocessableEntity, "viewing_edit.html", view{
			Title: "Edit viewing", Nav: "movies", Flash: &flash{Kind: "error", Msg: err.Error()},
			Data: map[string]any{"Movie": m, "Viewing": v, "Form": form},
		})
		return nil
	}

	err = a.q.UpdateViewing(r.Context(), localdb.UpdateViewingParams{
		ViewedOn: in.Date, Rating: in.Rating, Review: in.Review, ID: id,
	})
	if err != nil {
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
	v, err := a.q.GetViewing(r.Context(), id)
	if err != nil {
		return err
	}
	if err := a.q.DeleteViewing(r.Context(), id); err != nil {
		return err
	}
	redirect(w, r, movieURL(v.MovieID), "ok", "Viewing deleted.")
	return nil
}

func (a *App) history(w http.ResponseWriter, r *http.Request) error {
	rows, err := a.q.History(r.Context(), 500)
	if err != nil {
		return err
	}
	a.render(w, r, http.StatusOK, "history.html", view{Title: "History", Nav: "history", Data: rows})
	return nil
}

// ---- identity ----

type identityData struct {
	UserID     string
	PublicKey  string
	PrivateKey string // only when revealed
	KeyErr     string
}

func (a *App) identity(ctx context.Context) identityData {
	key, err := a.privateKey(ctx)
	if err != nil {
		return identityData{KeyErr: "The stored key is unreadable: " + err.Error()}
	}
	if key == nil {
		return identityData{}
	}
	pub := key.Public().(ed25519.PublicKey)
	return identityData{UserID: keys.UserID(pub), PublicKey: keys.EncodePublic(pub)}
}

func (a *App) identityShow(w http.ResponseWriter, r *http.Request) error {
	a.render(w, r, http.StatusOK, "identity.html", view{Title: "Identity", Nav: "identity", Data: a.identity(r.Context())})
	return nil
}

func (a *App) identityReveal(w http.ResponseWriter, r *http.Request) error {
	d := a.identity(r.Context())
	d.PrivateKey = a.setting(r.Context(), settingPrivateKey)
	a.render(w, r, http.StatusOK, "identity.html", view{Title: "Identity", Nav: "identity", Data: d})
	return nil
}

// replacingKey reports whether a key exists and the user hasn't
// confirmed replacing it, redirecting with an explanation if so.
func (a *App) replacingKey(w http.ResponseWriter, r *http.Request) bool {
	if a.setting(r.Context(), settingPrivateKey) == "" || r.PostFormValue("confirm_replace") == "yes" {
		return false
	}
	redirect(w, r, "/identity", "error", "You already have a key. Tick the confirmation box to replace it.")
	return true
}

func (a *App) identityGenerate(w http.ResponseWriter, r *http.Request) error {
	if a.replacingKey(w, r) {
		return nil
	}
	_, prv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	if err := a.setSetting(r.Context(), settingPrivateKey, keys.EncodePrivate(prv)); err != nil {
		return err
	}
	redirect(w, r, "/identity", "ok", "Generated a new key. Back up the private key somewhere safe.")
	return nil
}

func (a *App) identityImport(w http.ResponseWriter, r *http.Request) error {
	if a.replacingKey(w, r) {
		return nil
	}
	prv, err := keys.ParsePrivate(r.PostFormValue("private_key"))
	if err != nil {
		redirect(w, r, "/identity", "error", err.Error())
		return nil
	}
	if err := a.setSetting(r.Context(), settingPrivateKey, keys.EncodePrivate(prv)); err != nil {
		return err
	}
	redirect(w, r, "/identity", "ok", "Key imported.")
	return nil
}

// ---- servers ----

func (a *App) serversList(w http.ResponseWriter, r *http.Request) error {
	servers, err := a.q.ListServers(r.Context())
	if err != nil {
		return err
	}
	a.render(w, r, http.StatusOK, "servers.html", view{Title: "Servers", Nav: "servers", Data: servers})
	return nil
}

func (a *App) serverCreate(w http.ResponseWriter, r *http.Request) error {
	raw := strings.TrimRight(strings.TrimSpace(r.PostFormValue("url")), "/")
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		redirect(w, r, "/servers", "error", fmt.Sprintf("%q is not a server URL like https://revs.example.com", raw))
		return nil
	}
	s, err := a.q.CreateServer(r.Context(), raw)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			redirect(w, r, "/servers", "error", raw+" is already in your list.")
			return nil
		}
		return err
	}
	redirect(w, r, serverURL(s.ID), "ok", "Server added. Pull to fetch its reviews, or push yours.")
	return nil
}

func serverURL(id int64) string { return "/servers/" + strconv.FormatInt(id, 10) }

// serverFor loads the {id} server and its remote.
func (a *App) serverFor(r *http.Request) (localdb.Server, *remote, error) {
	id, err := idParam(r, "id")
	if err != nil {
		return localdb.Server{}, nil, err
	}
	s, err := a.q.GetServer(r.Context(), id)
	if err != nil {
		return s, nil, err
	}
	rem, err := a.remote(s.ID, s.Url)
	return s, rem, err
}

func (a *App) serverShow(w http.ResponseWriter, r *http.Request) error {
	s, rem, err := a.serverFor(r)
	if err != nil {
		return err
	}
	epoch, err := rem.client.Epoch()
	if err != nil {
		return err
	}
	users, err := rem.q.ListIndexedUsers(r.Context())
	if err != nil {
		return err
	}

	uid := a.userID(r.Context())
	var mine *remotedb.IdxUser
	for i := range users {
		if users[i].Userid == uid {
			mine = &users[i]
		}
	}

	a.render(w, r, http.StatusOK, "server.html", view{Title: s.Url, Nav: "servers", Data: map[string]any{
		"Server":    s,
		"Epoch":     epoch,
		"Users":     users,
		"Mine":      mine,
		"MyID":      uid,
		"Conflict":  r.URL.Query().Get("conflict"),
		"Diverged":  r.URL.Query().Get("diverged") != "",
		"KeyChange": r.URL.Query().Get("keychanged") != "",
	}})
	return nil
}

func (a *App) recordServerError(ctx context.Context, id int64, err error) {
	msg := err.Error()
	if e := a.q.RecordServerError(ctx, localdb.RecordServerErrorParams{LastError: &msg, ID: id}); e != nil {
		log.Printf("recording server error: %v", e)
	}
}

func (a *App) serverPush(w http.ResponseWriter, r *http.Request) error {
	s, rem, err := a.serverFor(r)
	if err != nil {
		return err
	}
	key, err := a.privateKey(r.Context())
	if err != nil {
		return err
	}
	if key == nil {
		redirect(w, r, "/identity", "error", "Create or import a key before pushing.")
		return nil
	}

	uid := keys.UserID(key.Public().(ed25519.PublicKey))
	content, err := buildContent(r.Context(), a.q, uid)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(r.Context(), networkTimeout)
	defer cancel()
	res, err := rem.client.Push(ctx, key, content)

	var stale *client.StaleError
	switch {
	case errors.As(err, &stale):
		a.recordServerError(r.Context(), s.ID, err)
		redirect(w, r, serverURL(s.ID)+"?conflict="+strconv.Itoa(stale.LatestRev), "", "")
		return nil
	case err != nil:
		a.recordServerError(r.Context(), s.ID, err)
		redirect(w, r, serverURL(s.ID), "error", "Push failed: "+err.Error())
		return nil
	}

	rev := int64(res.Rev)
	if err := a.q.RecordPush(r.Context(), localdb.RecordPushParams{LastPushRev: &rev, ID: s.ID}); err != nil {
		return err
	}
	msg := fmt.Sprintf("Pushed revision %d. It will appear once the server publishes its next epoch.", res.Rev)
	if !res.Pushed {
		msg = fmt.Sprintf("Nothing to push: the server already has this content (revision %d).", res.Rev)
	}
	redirect(w, r, serverURL(s.ID), "ok", msg)
	return nil
}

func (a *App) serverPull(w http.ResponseWriter, r *http.Request) error {
	return a.doPull(w, r, false)
}

func (a *App) serverRebootstrap(w http.ResponseWriter, r *http.Request) error {
	return a.doPull(w, r, true)
}

func (a *App) doPull(w http.ResponseWriter, r *http.Request, rebootstrap bool) error {
	s, _, err := a.serverFor(r)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(r.Context(), networkTimeout)
	defer cancel()
	res, err := a.pull(ctx, s.ID, s.Url, rebootstrap)

	var diverged *client.DivergedError
	var keyChanged *client.KeyChangedError
	switch {
	case errors.As(err, &diverged):
		a.recordServerError(r.Context(), s.ID, err)
		redirect(w, r, serverURL(s.ID)+"?diverged=1", "", "")
		return nil
	case errors.As(err, &keyChanged):
		a.recordServerError(r.Context(), s.ID, err)
		redirect(w, r, serverURL(s.ID)+"?keychanged=1", "", "")
		return nil
	case err != nil:
		a.recordServerError(r.Context(), s.ID, err)
		redirect(w, r, serverURL(s.ID), "error", "Pull failed: "+err.Error())
		return nil
	}

	epoch := int64(res.Epoch)
	if err := a.q.RecordPull(r.Context(), localdb.RecordPullParams{LastEpoch: &epoch, ID: s.ID}); err != nil {
		return err
	}

	var msg string
	switch {
	case res.Epoch == 0:
		msg = "The server hasn't published anything yet."
	case res.FromEpoch == res.Epoch && !res.Bootstrapped:
		msg = fmt.Sprintf("Already up to date (epoch %d).", res.Epoch)
	default:
		msg = fmt.Sprintf("Pulled epoch %d: %d users changed.", res.Epoch, len(res.Changed))
	}
	redirect(w, r, serverURL(s.ID), "ok", msg)
	return nil
}

func (a *App) serverReset(w http.ResponseWriter, r *http.Request) error {
	s, _, err := a.serverFor(r)
	if err != nil {
		return err
	}
	if err := a.closeRemote(s.ID, true); err != nil {
		return err
	}
	redirect(w, r, serverURL(s.ID), "ok", "Discarded everything pulled from this server, including its pinned key. Pull to start fresh.")
	return nil
}

func (a *App) serverDelete(w http.ResponseWriter, r *http.Request) error {
	id, err := idParam(r, "id")
	if err != nil {
		return err
	}
	s, err := a.q.GetServer(r.Context(), id)
	if err != nil {
		return err
	}
	if err := a.closeRemote(id, true); err != nil {
		return err
	}
	if err := a.q.DeleteServer(r.Context(), id); err != nil {
		return err
	}
	redirect(w, r, "/servers", "ok", "Removed "+s.Url+".")
	return nil
}

// serverImportMine merges the collection this server holds for the
// local user (e.g. pushed from another device) into the local one.
func (a *App) serverImportMine(w http.ResponseWriter, r *http.Request) error {
	s, rem, err := a.serverFor(r)
	if err != nil {
		return err
	}
	uid := a.userID(r.Context())
	if uid == "" {
		redirect(w, r, "/identity", "error", "Create or import a key first.")
		return nil
	}
	doc, err := rem.client.Document(uid)
	if errors.Is(err, client.ErrNotFound) {
		redirect(w, r, serverURL(s.ID), "error", "This server has no published collection for you yet. Pull first.")
		return nil
	}
	if err != nil {
		return err
	}
	pd, err := parseRatingset(doc.Data)
	if err != nil {
		return err
	}
	stats, err := a.mergeTitles(r.Context(), pd.Titles)
	if err != nil {
		return err
	}
	redirect(w, r, serverURL(s.ID), "ok", fmt.Sprintf("Merged revision %d. %s", doc.Rev, stats))
	return nil
}

func (a *App) serverUser(w http.ResponseWriter, r *http.Request) error {
	s, rem, err := a.serverFor(r)
	if err != nil {
		return err
	}
	uid := chi.URLParam(r, "uid")
	u, err := rem.q.GetIndexedUser(r.Context(), uid)
	if err != nil {
		return err
	}
	rows, err := rem.q.ListUserReviews(r.Context(), uid)
	if err != nil {
		return err
	}

	// Group the flat join into titles.
	type titleGroup struct {
		Name                 string
		Year                 *int64
		IMDB, TMDB, Wikidata *string
		Reviews              []remotedb.ListUserReviewsRow
	}
	var titles []*titleGroup
	byID := map[int64]*titleGroup{}
	for _, row := range rows {
		t, ok := byID[row.TitleID]
		if !ok {
			t = &titleGroup{Name: row.Name, Year: row.Year, IMDB: row.ImdbID, TMDB: row.TmdbID, Wikidata: row.WikidataID}
			byID[row.TitleID] = t
			titles = append(titles, t)
		}
		if row.ViewedOn != nil || row.Rating != nil || row.Comment != nil {
			t.Reviews = append(t.Reviews, row)
		}
	}

	a.render(w, r, http.StatusOK, "server_user.html", view{Title: "User " + shortID(uid), Nav: "servers", Data: map[string]any{
		"Server": s, "User": u, "Titles": titles, "IsMe": uid == a.userID(r.Context()),
	}})
	return nil
}

// ---- import / export ----

func (a *App) dataPage(w http.ResponseWriter, r *http.Request) error {
	counts, err := a.q.CountMoviesAndViewings(r.Context())
	if err != nil {
		return err
	}
	a.render(w, r, http.StatusOK, "data.html", view{Title: "Import & export", Nav: "data", Data: map[string]any{
		"Counts": counts,
		"HasKey": a.setting(r.Context(), settingPrivateKey) != "",
	}})
	return nil
}

func (a *App) export(w http.ResponseWriter, r *http.Request) error {
	key, err := a.privateKey(r.Context())
	if err != nil {
		return err
	}
	if key == nil {
		redirect(w, r, "/identity", "error", "Create or import a key before exporting: exports are signed.")
		return nil
	}
	signed, rev, err := a.signExport(r.Context(), key)
	if err != nil {
		return err
	}
	name := fmt.Sprintf("revs-%s-r%d.sjsonl", time.Now().Format("20060102"), rev)
	w.Header().Set("Content-Type", "application/jsonl")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
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

	pd, err := parseRatingset(data)
	if err != nil {
		redirect(w, r, "/data", "error", "Could not read that file: "+err.Error())
		return nil
	}
	stats, err := a.mergeTitles(r.Context(), pd.Titles)
	if err != nil {
		return err
	}
	msg := stats.String()
	if uid := a.userID(r.Context()); pd.UserID != "" && uid != "" && pd.UserID != uid {
		msg += " Note: the file belongs to a different user id than your key; its reviews are now yours."
	}
	redirect(w, r, "/data", "ok", msg)
	return nil
}
