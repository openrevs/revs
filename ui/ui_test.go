package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/openrevs/revs"
	jsonlines "github.com/openrevs/sjsonl-go"
	"github.com/stretchr/testify/require"
)

// testUI is a running UI with a browser-like client that keeps cookies
// (for flash messages) and doesn't follow redirects.
type testUI struct {
	t   *testing.T
	app *App
	ts  *httptest.Server
	c   *http.Client
}

func newTestUI(t *testing.T, opts ...func(*Options)) *testUI {
	t.Helper()
	o := Options{DataDir: t.TempDir()}
	for _, f := range opts {
		f(&o)
	}
	app, err := New(o)
	require.NoError(t, err)
	t.Cleanup(func() { app.Close() })

	ts := httptest.NewServer(app.Handler(false))
	t.Cleanup(ts.Close)

	jar, _ := cookiejar.New(nil)
	return &testUI{t: t, app: app, ts: ts, c: &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// post submits a form and returns the redirect target (or "" for a
// rendered page) and the flash message shown on the next page.
func (u *testUI) post(path string, form url.Values) (string, string) {
	u.t.Helper()
	resp, err := u.c.PostForm(u.ts.URL+path, form)
	require.NoError(u.t, err)
	resp.Body.Close()
	require.Less(u.t, resp.StatusCode, 500, "POST %s", path)

	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", ""
	}
	page := u.get(loc)
	_, msg, _ := strings.Cut(page, `role="status">`)
	msg, _, _ = strings.Cut(msg, "<")
	return loc, msg
}

func (u *testUI) get(path string) string {
	u.t.Helper()
	resp, err := u.c.Get(u.ts.URL + path)
	require.NoError(u.t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(u.t, err)
	require.Equal(u.t, http.StatusOK, resp.StatusCode, "GET %s: %s", path, body)
	return string(body)
}

func TestCollectionCRUD(t *testing.T) {
	u := newTestUI(t)
	ctx := context.Background()

	loc, msg := u.post("/movies", url.Values{
		"title": {"Pulp Fiction"}, "year": {"1994"},
		"imdb_id":     {"https://www.imdb.com/title/tt0110912/"},
		"tmdb_id":     {"680-pulp-fiction"},
		"wikidata_id": {"https://www.wikidata.org/wiki/Q104123"},
	})
	require.Equal(t, "/movies/1", loc)
	require.Contains(t, msg, "Added Pulp Fiction")

	m, err := u.app.q.GetMovie(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, "tt0110912", *m.ImdbID)
	require.Equal(t, "680", *m.TmdbID)
	require.Equal(t, "Q104123", *m.WikidataID)

	// Adding the same Wikidata item again leads to the existing entry.
	loc, msg = u.post("/movies", url.Values{"title": {"Pulp Fiction"}, "wikidata_id": {"Q104123"}})
	require.Equal(t, "/movies/1", loc)
	require.Contains(t, msg, "already in your collection")

	u.post("/movies/1/viewings", url.Values{"viewed_on": {"2024-03-02"}, "rating": {"5"}, "review": {"Great."}})
	u.post("/movies/1/viewings", url.Values{"viewed_on": {"2025-01-01"}, "rating": {"none"}})
	loc, _ = u.post("/movies/1/viewings", url.Values{"viewed_on": {"yesterday"}, "rating": {"7"}})
	require.Empty(t, loc, "invalid viewing re-renders the form")

	vs, err := u.app.q.ListViewingsForMovie(ctx, 1)
	require.NoError(t, err)
	require.Len(t, vs, 2)
	require.Nil(t, vs[0].Rating, "newest first, unrated")

	page := u.get("/movies/1")
	require.Contains(t, page, "Great.")
	require.Contains(t, page, "★★★★★")

	u.post("/viewings/2", url.Values{"viewed_on": {"2025-01-02"}, "rating": {"0"}, "review": {""}})
	v, err := u.app.q.GetViewing(ctx, 2)
	require.NoError(t, err)
	require.Equal(t, int64(0), *v.Rating)

	require.Contains(t, u.get("/movies?q=pulp"), "Pulp Fiction")
	require.NotContains(t, u.get("/movies?q=brazil"), "/movies/1\"")
	require.Contains(t, u.get("/history"), "2025-01-02")

	u.post("/movies/1", url.Values{"title": {"Pulp Fiction"}, "year": {"1994"}})
	m, err = u.app.q.GetMovie(ctx, 1)
	require.NoError(t, err)
	require.Nil(t, m.ImdbID, "cleared ids are removed")

	loc, _ = u.post("/movies/1/delete", nil)
	require.Equal(t, "/movies", loc)
	all, err := u.app.q.AllViewings(ctx)
	require.NoError(t, err)
	require.Empty(t, all, "viewings are deleted with their movie")
}

func TestIdentity(t *testing.T) {
	u := newTestUI(t)
	ctx := context.Background()

	u.post("/identity/generate", nil)
	first := u.app.userID(ctx)
	require.Len(t, first, 64)

	// Replacing requires confirmation.
	_, msg := u.post("/identity/generate", nil)
	require.Contains(t, msg, "already have a key")
	require.Equal(t, first, u.app.userID(ctx))

	u.post("/identity/generate", url.Values{"confirm_replace": {"yes"}})
	require.NotEqual(t, first, u.app.userID(ctx))

	_, msg = u.post("/identity/import", url.Values{"private_key": {"nope"}, "confirm_replace": {"yes"}})
	require.Contains(t, msg, "private key must be")
}

// TestPushPull runs two users against a real publishing server: each
// pushes, the server cuts an epoch, and each sees the other's review.
func TestPushPull(t *testing.T) {
	srv, err := revs.NewServer(t.TempDir())
	require.NoError(t, err)
	sts := httptest.NewServer(srv.Handler())
	t.Cleanup(sts.Close)
	ctx := context.Background()

	alice, bob := newTestUI(t), newTestUI(t)
	for _, u := range []*testUI{alice, bob} {
		u.post("/identity/generate", nil)
		u.post("/servers", url.Values{"url": {sts.URL}})
	}

	alice.post("/movies", url.Values{"title": {"Alien"}, "year": {"1979"}, "wikidata_id": {"Q103569"}})
	alice.post("/movies/1/viewings", url.Values{"viewed_on": {"2020-10-31"}, "rating": {"5"}, "review": {"In space no one can hear you scream."}})
	bob.post("/movies", url.Values{"title": {"alien"}, "year": {"1979"}})
	bob.post("/movies/1/viewings", url.Values{"viewed_on": {"2021-01-01"}, "rating": {"3"}})

	_, msg := alice.post("/servers/1/push", nil)
	require.Contains(t, msg, "Pushed revision 1")
	_, msg = alice.post("/servers/1/push", nil)
	require.Contains(t, msg, "Nothing to push")
	_, msg = bob.post("/servers/1/push", nil)
	require.Contains(t, msg, "Pushed revision 1")

	_, err = srv.Cut(ctx)
	require.NoError(t, err)

	_, msg = alice.post("/servers/1/pull", nil)
	require.Contains(t, msg, "2 users changed")
	_, msg = bob.post("/servers/1/pull", nil)
	require.Contains(t, msg, "2 users changed")

	// Bob sees Alice's review on his copy of the movie (matched by
	// title and year, since Bob has no ids), and vice versa by id.
	require.Contains(t, bob.get("/movies/1"), "In space no one can hear you scream.")
	page := alice.get("/movies/1")
	require.Contains(t, page, "★★★☆☆")
	require.Contains(t, page, bob.app.userID(ctx)[:12])

	// Alice's page of Bob lists his movie.
	require.Contains(t, alice.get("/servers/1/users/"+bob.app.userID(ctx)), "alien")

	// A second device with Alice's key: its push is stale until it
	// merges Alice's published copy.
	key := alice.app.setting(ctx, settingPrivateKey)
	alice2 := newTestUI(t)
	alice2.post("/identity/import", url.Values{"private_key": {key}})
	alice2.post("/servers", url.Values{"url": {sts.URL}})
	alice2.post("/movies", url.Values{"title": {"Aliens"}, "year": {"1986"}})
	loc, _ := alice2.post("/servers/1/push", nil)
	require.Equal(t, "/servers/1?conflict=1", loc)

	alice2.post("/servers/1/pull", nil)
	_, msg = alice2.post("/servers/1/import-mine", nil)
	require.Contains(t, msg, "Imported 1 new movies")
	_, msg = alice2.post("/servers/1/push", nil)
	require.Contains(t, msg, "Pushed revision 2")

	_, err = srv.Cut(ctx)
	require.NoError(t, err)
	alice.post("/servers/1/pull", nil)
	_, msg = alice.post("/servers/1/import-mine", nil)
	require.Contains(t, msg, "Imported 1 new movies (1 already present)")

	// Removing the server deletes its database.
	alice.post("/servers/1/delete", nil)
	servers, err := alice.app.q.ListServers(ctx)
	require.NoError(t, err)
	require.Empty(t, servers)
}

func TestExportImport(t *testing.T) {
	u := newTestUI(t)
	ctx := context.Background()

	resp, err := u.c.Get(u.ts.URL + "/export.jsonl")
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, "/identity", resp.Header.Get("Location"), "export needs a key")

	u.post("/identity/generate", nil)
	u.post("/movies", url.Values{"title": {"Brazil"}, "year": {"1985"}})
	u.post("/movies/1/viewings", url.Values{"viewed_on": {"2019-05-05"}, "rating": {"4"}})
	// Identical movies would make identical lines; export must still sign.
	u.post("/movies", url.Values{"title": {"Untitled"}})
	u.post("/movies", url.Values{"title": {"Untitled"}})

	export := func() []byte {
		resp, err := u.c.Get(u.ts.URL + "/export.jsonl")
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		data, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return data
	}

	doc := export()
	info, err := jsonlines.Verify(doc)
	require.NoError(t, err)
	user, _, err := revs.ParseRatingset(doc)
	require.NoError(t, err)
	require.Equal(t, u.app.userID(ctx), user.ID)
	require.Equal(t, 1, user.Revision)

	// Unchanged content keeps its revision; a change bumps it.
	again, err := jsonlines.Verify(export())
	require.NoError(t, err)
	require.Equal(t, info.Hash, again.Hash)
	u.post("/movies", url.Values{"title": {"Heat"}, "year": {"1995"}})
	user, _, err = revs.ParseRatingset(export())
	require.NoError(t, err)
	require.Equal(t, 2, user.Revision)

	// Importing into a fresh collection reproduces it.
	other := newTestUI(t)
	_, msg := other.upload(doc)
	require.Contains(t, msg, "Imported 2 new movies (0 already present) and 1 new viewings")
	all, err := other.app.q.AllMovies(ctx)
	require.NoError(t, err)
	require.Len(t, all, 2)

	// Re-importing changes nothing.
	_, msg = other.upload(doc)
	require.Contains(t, msg, "Imported 0 new movies (2 already present) and 0 new viewings (1 already present)")

	// A tampered document is rejected.
	_, msg = other.upload(bytes.Replace(doc, []byte("Brazil"), []byte("Brasil"), 1))
	require.Contains(t, msg, "signature verification failed")
}

func TestImportUnsignedDraft(t *testing.T) {
	u := newTestUI(t)
	ctx := context.Background()

	// A `revs tui` draft: unsigned, refs keyed in other cases, a
	// non-movie title, and a TMDB slug.
	draft := strings.Join([]string{
		`{"id":"abc","max_rating":5,"min_rating":0}`,
		`{"name":"Pulp Fiction","year":1994,"refs":{"TMDB":"680-pulp-fiction","IMDb":"tt0110912"},"reviews":[{"date":"2020-01-01","rating":5,"comment":"wow"},{"rating":4}]}`,
		`{"type":"book","name":"Dune"}`,
	}, "\n")

	_, msg := u.upload([]byte(draft))
	require.Contains(t, msg, "Imported 1 new movies (0 already present) and 2 new viewings")
	require.Contains(t, msg, "Skipped 1 non-movie titles")

	m, err := u.app.q.GetMovie(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, "680", *m.TmdbID)
	require.Equal(t, "tt0110912", *m.ImdbID)

	// A later import matching by id fills in missing ids.
	_, msg = u.upload([]byte(`{"name":"Pulp Fiction (1994)","refs":{"imdb":"tt0110912","wikidata":"Q104123"}}`))
	require.Contains(t, msg, "0 new movies (1 already present)")
	m, err = u.app.q.GetMovie(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, "Q104123", *m.WikidataID)
	require.Equal(t, "Pulp Fiction", m.Title, "an existing title is kept")
}

func (u *testUI) upload(data []byte) (string, string) {
	u.t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", "revs.sjsonl")
	require.NoError(u.t, err)
	fw.Write(data)
	mw.Close()

	resp, err := u.c.Post(u.ts.URL+"/import", mw.FormDataContentType(), &body)
	require.NoError(u.t, err)
	resp.Body.Close()
	page := u.get(resp.Header.Get("Location"))
	_, msg, _ := strings.Cut(page, `role="status">`)
	msg, _, _ = strings.Cut(msg, "<")
	return resp.Header.Get("Location"), msg
}

func TestWikidataSearch(t *testing.T) {
	claim := func(v any) map[string]any {
		raw, _ := json.Marshal(v)
		return map[string]any{"mainsnak": map[string]any{"datavalue": map[string]any{"value": json.RawMessage(raw)}}}
	}
	class := func(id string) any { return []any{claim(map[string]string{"id": id})} }
	label := func(lang, v string) map[string]any { return map[string]any{lang: map[string]string{"value": v}} }

	ids := []string{"Q104123", "Q999", "Q5", "Q7", "Q8", "Q9"}
	wd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Contains(t, r.Header.Get("User-Agent"), "revs-ui")
		switch r.URL.Query().Get("action") {
		case "query":
			// The query is restricted to film items in the search itself.
			sr := r.URL.Query().Get("srsearch")
			require.True(t, strings.HasPrefix(sr, "pulp fiction haswbstatement:P4947|"), sr)
			require.Contains(t, sr, "|P31=Q11424")
			var hits []map[string]string
			for _, id := range ids {
				hits = append(hits, map[string]string{"title": id})
			}
			json.NewEncoder(w).Encode(map[string]any{"query": map[string]any{"search": hits}})
		case "wbgetentities":
			require.Equal(t, strings.Join(ids, "|"), r.URL.Query().Get("ids"))
			json.NewEncoder(w).Encode(map[string]any{"entities": map[string]any{
				// Its label is only in "mul", as on the real item.
				"Q104123": map[string]any{
					"labels":       label("mul", "Pulp Fiction"),
					"descriptions": label("en", "1994 film by Quentin Tarantino"),
					"claims": map[string]any{
						"P31":   class("Q11424"),
						"P345":  []any{claim("tt0110912")},
						"P4947": []any{claim("680")},
						"P577":  []any{claim(map[string]string{"time": "+1994-10-14T00:00:00Z"}), claim(map[string]string{"time": "+1994-05-21T00:00:00Z"})},
					},
				},
				// A soundtrack album: not a film.
				"Q999": map[string]any{"labels": label("en", "Pulp Fiction (soundtrack)"), "claims": map[string]any{"P31": class("Q482994")}},
				// Unknown class but a TMDB movie id; labelled only in French.
				"Q5": map[string]any{"labels": label("fr", "Fiction pulpeuse"), "claims": map[string]any{"P4947": []any{claim("12345")}}},
				// A TV special with a TMDB movie id: not a movie.
				"Q7": map[string]any{"labels": label("en", "Pulp Fiction Special"), "claims": map[string]any{"P31": class("Q1261214"), "P4947": []any{claim("777")}}},
				// A video game with an IMDb id: not a movie.
				"Q8": map[string]any{"labels": label("en", "Pulp Fiction: The Game"), "claims": map[string]any{"P31": class("Q7889"), "P345": []any{claim("tt7777777")}}},
				// A TV series with an IMDb id and no class we know: not a movie.
				"Q9": map[string]any{"labels": label("en", "Pulp Fiction: The Series"), "claims": map[string]any{"P345": []any{claim("tt8888888")}}},
			}})
		default:
			t.Errorf("unexpected action %q", r.URL.Query().Get("action"))
		}
	}))
	t.Cleanup(wd.Close)

	u := newTestUI(t, func(o *Options) { o.WikidataURL = wd.URL })
	results, err := u.app.wikidata.Search(context.Background(), "  pulp   fiction ")
	require.NoError(t, err)
	require.Len(t, results, 2)
	require.Equal(t, "Q104123", results[0].ID)
	require.Equal(t, "Pulp Fiction", results[0].Title)
	require.Equal(t, int64(1994), *results[0].Year)
	require.Equal(t, "tt0110912", *results[0].IMDB)
	require.Equal(t, "680", *results[0].TMDB)
	require.Equal(t, "Q5", results[1].ID)
	require.Equal(t, "Fiction pulpeuse", results[1].Title)

	page := u.get("/movies/new?q=pulp+fiction")
	require.Contains(t, page, "1994 film by Quentin Tarantino")
	for _, absent := range []string{"soundtrack", "Special", "The Game", "The Series"} {
		require.NotContains(t, page, absent)
	}
}

func TestNormalizeIDs(t *testing.T) {
	for in, want := range map[string]string{
		"tt0110912": "tt0110912", "https://www.imdb.com/title/tt0110912/?ref=x": "tt0110912",
	} {
		require.Equal(t, want, deref(normalizeIMDB(in)), in)
	}
	for in, want := range map[string]string{
		"680": "680", "680-pulp-fiction": "680", "https://www.themoviedb.org/movie/680-pulp-fiction": "680", "abc": "",
	} {
		require.Equal(t, want, deref(normalizeTMDB(in)), in)
	}
	for in, want := range map[string]string{
		"Q104123": "Q104123", "q104123": "Q104123", "https://www.wikidata.org/wiki/Q104123": "Q104123", "x": "",
	} {
		require.Equal(t, want, deref(normalizeWikidata(in)), in)
	}
}

func TestLoopbackHost(t *testing.T) {
	app, err := New(Options{DataDir: t.TempDir()})
	require.NoError(t, err)
	defer app.Close()
	h := app.Handler(true)

	for host, want := range map[string]int{"localhost:1325": 200, "127.0.0.1:1325": 200, "evil.example": 403} {
		req := httptest.NewRequest("GET", "/movies", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		require.Equal(t, want, rec.Code, host)
	}

	// Cross-site form posts are refused.
	req := httptest.NewRequest("POST", "/identity/generate", nil)
	req.Host = "localhost:1325"
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Empty(t, app.userID(context.Background()))
}
