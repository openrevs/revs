package ui

import (
	"bytes"
	"context"
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

	m, err := u.app.c.Movie(ctx, 1)
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

	vs, err := u.app.c.Viewings(ctx, 1)
	require.NoError(t, err)
	require.Len(t, vs, 2)
	require.Nil(t, vs[0].Rating, "newest first, unrated")

	page := u.get("/movies/1")
	require.Contains(t, page, "Great.")
	require.Contains(t, page, "★★★★★")

	u.post("/viewings/2", url.Values{"viewed_on": {"2025-01-02"}, "rating": {"0"}, "review": {""}})
	v, err := u.app.c.Viewing(ctx, 2)
	require.NoError(t, err)
	require.Equal(t, int64(0), *v.Rating)

	require.Contains(t, u.get("/movies?q=pulp"), "Pulp Fiction")
	require.NotContains(t, u.get("/movies?q=brazil"), "/movies/1\"")
	require.Contains(t, u.get("/history"), "2025-01-02")

	u.post("/movies/1", url.Values{"title": {"Pulp Fiction"}, "year": {"1994"}})
	m, err = u.app.c.Movie(ctx, 1)
	require.NoError(t, err)
	require.Nil(t, m.ImdbID, "cleared ids are removed")

	loc, _ = u.post("/movies/1/delete", nil)
	require.Equal(t, "/movies", loc)
	all, err := u.app.c.AllViewings(ctx)
	require.NoError(t, err)
	require.Empty(t, all, "viewings are deleted with their movie")
}

func TestIdentity(t *testing.T) {
	u := newTestUI(t)
	ctx := context.Background()

	u.post("/identity/generate", nil)
	first := u.app.c.UserID(ctx)
	require.Len(t, first, 64)

	// Replacing requires confirmation.
	_, msg := u.post("/identity/generate", nil)
	require.Contains(t, msg, "already have a key")
	require.Equal(t, first, u.app.c.UserID(ctx))

	u.post("/identity/generate", url.Values{"confirm_replace": {"yes"}})
	require.NotEqual(t, first, u.app.c.UserID(ctx))

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
	require.Contains(t, page, bob.app.c.UserID(ctx)[:12])

	// Alice's page of Bob lists his movie.
	require.Contains(t, alice.get("/servers/1/users/"+bob.app.c.UserID(ctx)), "alien")

	// A second device with Alice's key: its push is stale until it
	// merges Alice's published copy.
	key := alice.app.c.PrivateKeyText(ctx)
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
	servers, err := alice.app.c.Servers(ctx)
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
	require.Equal(t, u.app.c.UserID(ctx), user.ID)
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
	all, err := other.app.c.AllMovies(ctx)
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

	m, err := u.app.c.Movie(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, "680", *m.TmdbID)
	vs, err := u.app.c.Viewings(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, int64(10), *vs[0].Rating, "5 of 5 is 10 half stars")
	require.Equal(t, "tt0110912", *m.ImdbID)

	// A later import matching by id fills in missing ids.
	_, msg = u.upload([]byte(`{"name":"Pulp Fiction (1994)","refs":{"imdb":"tt0110912","wikidata":"Q104123"}}`))
	require.Contains(t, msg, "0 new movies (1 already present)")
	m, err = u.app.c.Movie(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, "Q104123", *m.WikidataID)
	require.Equal(t, "Pulp Fiction", m.Title, "an existing title is kept")
}

func TestImportCSV(t *testing.T) {
	u := newTestUI(t)
	ctx := context.Background()

	// Header in any case and order, with an ignored column, a BOM, a
	// quoted title, and two viewings of one movie.
	file := "\xef\xbb\xbfNotes,Title,YEAR,rating,view_date,imdb_id,tmdb_id\n" +
		"great,Pulp Fiction,1994,4.5,2020-01-01,tt0110912,680\n" +
		"again,Pulp Fiction,1994,4,2021-06-01,,\n" +
		`,"Crouching Tiger, Hidden Dragon",2000,,,,` + "\n" +
		",Heat,,3,,,\n"
	_, msg := u.uploadTo("/import/csv", []byte(file))
	require.Contains(t, msg, "Imported 3 new movies (1 already present) and 3 new viewings (0 already present)")

	m, err := u.app.c.Movie(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, "Pulp Fiction", m.Title)
	require.Equal(t, "tt0110912", *m.ImdbID)
	require.Equal(t, "680", *m.TmdbID)
	m, err = u.app.c.Movie(ctx, 2)
	require.NoError(t, err)
	require.Equal(t, "Crouching Tiger, Hidden Dragon", m.Title)
	vs, err := u.app.c.Viewings(ctx, 1)
	require.NoError(t, err)
	require.Len(t, vs, 2)
	require.Equal(t, int64(9), *vs[1].Rating, "4.5 stars")

	// Re-importing changes nothing.
	_, msg = u.uploadTo("/import/csv", []byte(file))
	require.Contains(t, msg, "Imported 0 new movies (4 already present) and 0 new viewings (3 already present)")

	// imdb_id_no_tt supplies the id without "tt", alone or agreeing
	// with imdb_id.
	_, msg = u.uploadTo("/import/csv", []byte("title,imdb_id_no_tt,imdb_id\nThe Thing,0084787,\nHeat,0113277,tt0113277\n"))
	require.Contains(t, msg, "Imported 1 new movies (1 already present)")
	m, err = u.app.c.Movie(ctx, 4)
	require.NoError(t, err)
	require.Equal(t, "The Thing", m.Title)
	require.Equal(t, "tt0084787", *m.ImdbID)
	m, err = u.app.c.Movie(ctx, 3)
	require.NoError(t, err)
	require.Equal(t, "tt0113277", *m.ImdbID, "filled in on the existing movie")

	// Title-only files work.
	_, msg = u.uploadTo("/import/csv", []byte("title\nAlien\n"))
	require.Contains(t, msg, "Imported 1 new movies")

	// Bad files are rejected whole.
	for file, want := range map[string]string{
		"year,rating\n1994,5\n":                                 `must include a &#34;title&#34; column`,
		"title,rating\nOk,3\nAliens,9\n":                        "line 3: rating must be 0–5 stars",
		"title,rating\nOk,3\nAliens,3.25\n":                     "line 3: rating must be 0–5 stars, in steps of ½",
		"title,view_date\nAliens,01/02/2020\n":                  `line 2: date &#34;01/02/2020&#34; should be YYYY-MM-DD`,
		"title,year\n,1994\n":                                   "line 2: title is required",
		"title\n":                                               "no rows after the header",
		"title,imdb_id,imdb_id_no_tt\nHeat,tt0113277,0113278\n": "line 2: imdb_id_no_tt &#34;0113278&#34; doesn&#39;t match imdb_id &#34;tt0113277&#34;",
		"title,imdb_id_no_tt\nHeat,tt0113277\n":                 "line 2: imdb_id_no_tt &#34;tt0113277&#34; should be digits like 0110912",
	} {
		_, msg = u.uploadTo("/import/csv", []byte(file))
		require.Contains(t, msg, "Could not read that CSV file", file)
		require.Contains(t, msg, want, file)
	}
	counts, err := u.app.c.Counts(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 5, counts.Movies, "a rejected file imports nothing")
}

func (u *testUI) upload(data []byte) (string, string) {
	u.t.Helper()
	return u.uploadTo("/import", data)
}

func (u *testUI) uploadTo(path string, data []byte) (string, string) {
	u.t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", "upload")
	require.NoError(u.t, err)
	fw.Write(data)
	mw.Close()

	resp, err := u.c.Post(u.ts.URL+path, mw.FormDataContentType(), &body)
	require.NoError(u.t, err)
	resp.Body.Close()
	page := u.get(resp.Header.Get("Location"))
	_, msg, _ := strings.Cut(page, `role="status">`)
	msg, _, _ = strings.Cut(msg, "<")
	return resp.Header.Get("Location"), msg
}

func TestSearchPage(t *testing.T) {
	// Wikidata unreachable: the page shows built-in matches and a note.
	wd := httptest.NewServer(http.NotFoundHandler())
	wd.Close()
	u := newTestUI(t, func(o *Options) { o.WikidataURL = wd.URL })

	page := u.get("/movies/new?q=the+matrix")
	require.Contains(t, page, "tt0133093")
	require.Contains(t, page, "Showing built-in matches only")
	require.Contains(t, page, "you may be offline")
	require.NotContains(t, page, "flash-error")

	page = u.get("/movies/new?q=no+such+film")
	require.Contains(t, page, "enter the details yourself")
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
	require.Empty(t, app.c.UserID(context.Background()))
}

func TestRevisionsSharedAcrossServers(t *testing.T) {
	var servers []string
	for range 2 {
		srv, err := revs.NewServer(t.TempDir())
		require.NoError(t, err)
		sts := httptest.NewServer(srv.Handler())
		t.Cleanup(sts.Close)
		servers = append(servers, sts.URL)
	}

	u := newTestUI(t)
	u.post("/identity/generate", nil)
	for _, s := range servers {
		u.post("/servers", url.Values{"url": {s}})
	}
	u.post("/movies", url.Values{"title": {"Alien"}, "year": {"1979"}})

	exportRev := func() int {
		resp, err := u.c.Get(u.ts.URL + "/export.jsonl")
		require.NoError(t, err)
		defer resp.Body.Close()
		doc, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		user, _, err := revs.ParseRatingset(doc)
		require.NoError(t, err)
		return user.Revision
	}

	// Export and both servers get the same version.
	require.Equal(t, 1, exportRev())
	_, msg := u.post("/servers/1/push", nil)
	require.Contains(t, msg, "Pushed revision 1")
	_, msg = u.post("/servers/2/push", nil)
	require.Contains(t, msg, "Pushed revision 1")

	// A change pushed to one server is the next revision everywhere.
	u.post("/movies", url.Values{"title": {"Heat"}, "year": {"1995"}})
	_, msg = u.post("/servers/2/push", nil)
	require.Contains(t, msg, "Pushed revision 2")
	_, msg = u.post("/servers/1/push", nil)
	require.Contains(t, msg, "Pushed revision 2")
	require.Equal(t, 2, exportRev())
}
