package ui

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/openrevs/revs/collection"
	"github.com/openrevs/revs/ratingset"
	"github.com/openrevs/revs/revstest"
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

// onNetwork has the UI reach servers on network.
func onNetwork(network *revstest.Network) func(*Options) {
	return func(o *Options) { o.HTTPClient = network.HTTPClient() }
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

	loc, _ = u.post("/viewings/2", url.Values{"viewed_on": {"2025-01-02"}, "rating": {"0"}, "review": {""}})
	require.Empty(t, loc, "no stars is refused; ½ is the least")
	u.post("/viewings/2", url.Values{"viewed_on": {"2025-01-02"}, "rating": {"0.5"}, "review": {""}})
	v, err := u.app.c.Viewing(ctx, 2)
	require.NoError(t, err)
	require.Equal(t, int64(1), *v.Rating)

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
	network := revstest.NewNetwork(t)
	sts := network.NewServer()
	ctx := context.Background()

	alice, bob := newTestUI(t, onNetwork(network)), newTestUI(t, onNetwork(network))
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

	sts.Cut()

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

	// A second device with Alice's key: its push merges Alice's
	// published copy first.
	key := alice.app.c.PrivateKeyText(ctx)
	alice2 := newTestUI(t, onNetwork(network))
	alice2.post("/identity/import", url.Values{"private_key": {key}})
	alice2.post("/servers", url.Values{"url": {sts.URL}})
	alice2.post("/movies", url.Values{"title": {"Aliens"}, "year": {"1986"}})
	_, msg = alice2.post("/servers/1/push", nil)
	require.Contains(t, msg, "Merged your collection from the server (revision 1) first. Imported 1 new titles")
	require.Contains(t, msg, "Pushed revision 2")

	sts.Cut()
	alice.post("/servers/1/pull", nil)
	_, msg = alice.post("/servers/1/import-mine", nil)
	require.Contains(t, msg, "Imported 1 new titles (1 already present)")

	// A copy the server hasn't published yet can't be merged: pushing
	// again stays a conflict until it is, or until pushed anyway.
	alice.post("/movies", url.Values{"title": {"Alien 3"}, "year": {"1992"}})
	_, msg = alice.post("/servers/1/push", nil)
	require.Contains(t, msg, "Pushed revision 3")
	alice2.post("/movies", url.Values{"title": {"Prometheus"}, "year": {"2012"}})
	for range 2 {
		loc, _ := alice2.post("/servers/1/push", nil)
		require.Equal(t, "/servers/1?conflict=3", loc)
	}
	require.Contains(t, alice2.get("/servers/1?conflict=3"), "Push anyway")
	_, msg = alice2.post("/servers/1/push", url.Values{"force": {"1"}})
	require.Contains(t, msg, "Pushed revision 4")
	require.NotContains(t, msg, "Merged")

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
	user, _, err := ratingset.ParseRatingset(doc)
	require.NoError(t, err)
	require.Equal(t, u.app.c.UserID(ctx), user.ID)
	require.Equal(t, 1, user.Revision)

	// Unchanged content keeps its revision; a change bumps it.
	again, err := jsonlines.Verify(export())
	require.NoError(t, err)
	require.Equal(t, info.Hash, again.Hash)
	u.post("/movies", url.Values{"title": {"Heat"}, "year": {"1995"}})
	user, _, err = ratingset.ParseRatingset(export())
	require.NoError(t, err)
	require.Equal(t, 2, user.Revision)

	// Importing into a fresh collection reproduces it.
	other := newTestUI(t)
	_, msg := other.upload(doc)
	require.Contains(t, msg, "Imported 2 new titles (0 already present) and 1 new viewings")
	all, err := other.app.c.AllMovies(ctx)
	require.NoError(t, err)
	require.Len(t, all, 2)

	// Re-importing changes nothing.
	_, msg = other.upload(doc)
	require.Contains(t, msg, "Imported 0 new titles (2 already present) and 0 new viewings (1 already present)")

	// A tampered document is rejected.
	_, msg = other.upload(bytes.Replace(doc, []byte("Brazil"), []byte("Brasil"), 1))
	require.Contains(t, msg, "signature verification failed")
}

func TestImportUnsignedDraft(t *testing.T) {
	u := newTestUI(t)
	ctx := context.Background()

	// A `revs tui` draft: unsigned, refs keyed in other cases, a
	// non-movie item, a TMDB slug, the author line not first, and items
	// that are invalid under the ratingset spec (no type, an
	// out-of-range rating, a bad date, a blank title).
	draft := strings.Join([]string{
		`{"entries":[{"date":"2020-01-01","rating":5,"review":"wow"},{"rating":4}],"refs":{"IMDb":"tt0110912","TMDB":"680-pulp-fiction"},"title":"Pulp Fiction","type":"Movie","year":1994}`,
		`{"id":"abc","max_rating":5,"min_rating":0}`,
		`{"title":"Dune","type":"book"}`,
		`{"title":"Rocky","year":1976}`,
		`{"entries":[{"rating":6}],"title":"Rocky","type":"movie"}`,
		`{"entries":[{"date":"2020-02-30"}],"title":"Rocky","type":"movie"}`,
		`{"title":"  ","type":"movie"}`,
	}, "\n")

	_, msg := u.upload([]byte(draft))
	require.Contains(t, msg, "Imported 1 new titles (0 already present) and 2 new viewings")
	require.Contains(t, msg, "Skipped 1 titles that are neither movies nor TV series")
	require.Contains(t, msg, "Skipped 4 invalid titles")

	m, err := u.app.c.Movie(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, "680", *m.TmdbID)
	vs, err := u.app.c.Viewings(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, int64(10), *vs[0].Rating, "5 of 5 is 10 half stars")
	require.Equal(t, "tt0110912", *m.ImdbID)

	// A later import matching by id fills in missing ids.
	_, msg = u.upload([]byte(`{"refs":{"imdb":"tt0110912","wikidata":"Q104123"},"title":"Pulp Fiction (1994)","type":"movie"}`))
	require.Contains(t, msg, "0 new titles (1 already present)")
	m, err = u.app.c.Movie(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, "Q104123", *m.WikidataID)
	require.Equal(t, "Pulp Fiction", m.Title, "an existing title is kept")

	// Items whose refs conflict (a shared key with a different value)
	// are kept apart, even when another ref or the title and year agree.
	_, msg = u.upload([]byte(`{"refs":{"imdb":"tt0110912","tmdb":"999"},"title":"Pulp Fiction","type":"movie","year":1994}`))
	require.Contains(t, msg, "1 new titles (0 already present)")
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
	require.Contains(t, msg, "Imported 3 new titles (1 already present) and 3 new viewings (0 already present)")

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
	require.Contains(t, msg, "Imported 0 new titles (4 already present) and 0 new viewings (3 already present)")

	// imdb_id_no_tt supplies the id without "tt", alone or agreeing
	// with imdb_id.
	_, msg = u.uploadTo("/import/csv", []byte("title,imdb_id_no_tt,imdb_id\nThe Thing,0084787,\nHeat,0113277,tt0113277\n"))
	require.Contains(t, msg, "Imported 1 new titles (1 already present)")
	m, err = u.app.c.Movie(ctx, 4)
	require.NoError(t, err)
	require.Equal(t, "The Thing", m.Title)
	require.Equal(t, "tt0084787", *m.ImdbID)
	m, err = u.app.c.Movie(ctx, 3)
	require.NoError(t, err)
	require.Equal(t, "tt0113277", *m.ImdbID, "filled in on the existing movie")

	// Title-only files work.
	_, msg = u.uploadTo("/import/csv", []byte("title\nAlien\n"))
	require.Contains(t, msg, "Imported 1 new titles")

	// review is free text: quoted fields may hold commas, doubled
	// quotes, and line breaks (LF or CRLF), and a review alone adds a
	// viewing.
	file = "title,review,rating\r\n" +
		"Brazil,\"Odd, \"\"funny\"\",\r\nand long.\n\nSee it.\",4\r\n" +
		"Brazil,  Second look  ,\r\n"
	_, msg = u.uploadTo("/import/csv", []byte(file))
	require.Contains(t, msg, "Imported 1 new titles (1 already present) and 2 new viewings (0 already present)")
	vs, err = u.app.c.Viewings(ctx, 6)
	require.NoError(t, err)
	require.Len(t, vs, 2)
	reviews := []string{vs[0].Review, vs[1].Review}
	require.ElementsMatch(t, []string{"Odd, \"funny\",\nand long.\n\nSee it.", "Second look"}, reviews)

	// Bad files are rejected whole.
	for file, want := range map[string]string{
		"year,rating\n1994,5\n":                                 `must include a &#34;title&#34; column`,
		"title,rating\nOk,3\nAliens,9\n":                        "line 3: rating must be ½–5 stars",
		"title,rating\nOk,3\nAliens,3.25\n":                     "line 3: rating must be ½–5 stars, in steps of ½",
		"title,view_date\nAliens,01/02/2020\n":                  `line 2: date &#34;01/02/2020&#34; should be YYYY-MM-DD`,
		"title,year\n,1994\n":                                   "line 2: title is required",
		"title\n":                                               "no rows after the header",
		"title,imdb_id,imdb_id_no_tt\nHeat,tt0113277,0113278\n": "line 2: imdb_id_no_tt &#34;0113278&#34; doesn&#39;t match imdb_id &#34;tt0113277&#34;",
		"title,imdb_id_no_tt\nHeat,tt0113277\n":                 "line 2: imdb_id_no_tt &#34;tt0113277&#34; should be digits like 0110912",
		"title,type\nHeat,book\n":                               `line 2: type &#34;book&#34; should be`,
		"title,season\nHeat,1\n":                                "line 2: only a TV series",
		"title,type,episode\nFargo,tv,2\n":                      "line 2: an episode needs a season",
	} {
		_, msg = u.uploadTo("/import/csv", []byte(file))
		require.Contains(t, msg, "Could not read that CSV file", file)
		require.Contains(t, msg, want, file)
	}
	counts, err := u.app.c.Counts(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 6, counts.Movies, "a rejected file imports nothing")
	require.EqualValues(t, 0, counts.Series)

	// type tv makes a row a TV series; season and episode give its
	// viewing's part, and a season alone records a viewing.
	file = "title,type,year,tmdb_id,season,episode,rating\n" +
		"Fargo,tv,2014,https://www.themoviedb.org/tv/60622,1,,4.5\n" +
		"Fargo,TV,2014,,2,3,\n" +
		"Fargo,tv,2014,,,,4\n" +
		"Fargo,,1996,,,,5\n"
	_, msg = u.uploadTo("/import/csv", []byte(file))
	require.Contains(t, msg, "Imported 2 new titles (2 already present) and 4 new viewings (0 already present)")
	series, err := u.app.c.Movies(ctx, "fargo", collection.TypeTV)
	require.NoError(t, err)
	require.Len(t, series, 1)
	require.Equal(t, "60622", *series[0].TmdbID)
	require.Equal(t, int64(8), series[0].LatestRating, "the whole-series viewing's")
	vs, err = u.app.c.Viewings(ctx, series[0].ID)
	require.NoError(t, err)
	var parts []string
	for _, v := range vs {
		parts = append(parts, collection.PartName(v.Season, v.Episode))
	}
	require.ElementsMatch(t, []string{"Season 1", "S2 E3", ""}, parts)
	films, err := u.app.c.Movies(ctx, "fargo", collection.TypeMovie)
	require.NoError(t, err)
	require.Len(t, films, 1, "the 1996 film is a separate movie")
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
	network := revstest.NewNetwork(t)
	servers := []string{network.NewServer().URL, network.NewServer().URL}

	u := newTestUI(t, onNetwork(network))
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
		user, _, err := ratingset.ParseRatingset(doc)
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

// ds sends a Datastar request (as datastar.js does for a form) and
// returns the SSE answer's body.
func (u *testUI) ds(method, path string, form url.Values) string {
	u.t.Helper()
	req, err := http.NewRequest(method, u.ts.URL+path, strings.NewReader(form.Encode()))
	require.NoError(u.t, err)
	req.Header.Set("Datastar-Request", "true")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := u.c.Do(req)
	require.NoError(u.t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(u.t, err)
	require.Equal(u.t, http.StatusOK, resp.StatusCode, "%s %s: %s", method, path, body)
	require.Equal(u.t, "text/event-stream", resp.Header.Get("Content-Type"))
	return string(body)
}

// One click rates a movie: from a search result (adding it), from the
// movie list, and from a movie's page, with or without JavaScript.
func TestQuickRating(t *testing.T) {
	u := newTestUI(t)
	ctx := context.Background()

	// Without JavaScript, a star on a search result adds and rates, then
	// returns to the results.
	loc, msg := u.post("/movies", url.Values{
		"title": {"Alien"}, "year": {"1979"}, "wikidata_id": {"Q103569"},
		"rating": {"4.5"}, "next": {"/movies/new?q=alien"},
	})
	require.Equal(t, "/movies/new?q=alien", loc)
	require.Contains(t, msg, "Added Alien, rated ★★★★½")
	vs, err := u.app.c.Viewings(ctx, 1)
	require.NoError(t, err)
	require.Len(t, vs, 1)
	require.Equal(t, int64(9), *vs[0].Rating)

	// With it, the result's stars are patched in place.
	sse := u.ds("POST", "/movies", url.Values{
		"title": {"Aliens"}, "year": {"1986"}, "wikidata_id": {"Q103474"}, "rating": {"4"},
	})
	require.Contains(t, sse, "event: datastar-patch-elements")
	require.Contains(t, sse, `id="rate-Q103474"`)
	require.Contains(t, sse, `href="/movies/2">Added`)
	require.Contains(t, sse, "Added Aliens, rated ★★★★☆")

	// Rating again from the list changes that rating, not a new viewing.
	loc, msg = u.post("/movies/1/rating", url.Values{"rating": {"3"}, "next": {"/movies?q=al"}})
	require.Equal(t, "/movies?q=al", loc)
	require.Contains(t, msg, "Rated Alien ★★★☆☆")
	vs, err = u.app.c.Viewings(ctx, 1)
	require.NoError(t, err)
	require.Len(t, vs, 1)
	require.Equal(t, int64(6), *vs[0].Rating)

	// next must stay on this site.
	loc, _ = u.post("/movies/1/rating", url.Values{"rating": {"3"}, "next": {"//evil.example/"}})
	require.Equal(t, "/movies/1", loc)

	// From the list with JavaScript, the row is patched.
	sse = u.ds("POST", "/movies/1/rating", url.Values{"rating": {"2.5"}, "view": {"row"}})
	require.Contains(t, sse, `<tr id="movie-1">`)
	require.Equal(t, 5, strings.Count(sse, `class="l on"`)+strings.Count(sse, `class="r on"`))
	require.Contains(t, sse, "Rated Alien ★★½☆☆")

	// From the movie page, its "mine" section is.
	sse = u.ds("POST", "/movies/1/rating", url.Values{"rating": {"5"}})
	require.Contains(t, sse, `<div id="mine">`)

	// No stars, or a nonsense rating, is refused with a message.
	for _, bad := range []string{"0", "6"} {
		sse = u.ds("POST", "/movies/1/rating", url.Values{"rating": {bad}})
		require.Contains(t, sse, "flash-error")
		require.Contains(t, sse, "rating must be ½–5 stars")
	}
	loc, msg = u.post("/movies/1/rating", url.Values{"rating": {"0"}, "next": {"/movies"}})
	require.Equal(t, "/movies", loc)
	require.Contains(t, msg, "rating must be")

	// The list shows lit star bars.
	page := u.get("/movies")
	require.Contains(t, page, `action="/movies/1/rating"`)
	require.Contains(t, page, `data-on:submit`)
}

// A movie's page edits viewings in place: open one, save it (or get
// its error back in place), add another, and delete one.
func TestInlineViewings(t *testing.T) {
	u := newTestUI(t)
	ctx := context.Background()
	u.post("/movies", url.Values{"title": {"Heat"}, "year": {"1995"}})

	page := u.get("/movies/1")
	require.Contains(t, page, "Edit movie")
	require.Contains(t, page, `<div id="mine">`)
	require.Contains(t, page, "Add a date or review")

	sse := u.ds("POST", "/movies/1/viewings", url.Values{"viewed_on": {"2024-05-01"}, "rating": {"4"}, "review": {"Great diner scene."}})
	require.Contains(t, sse, "Your review")
	require.Contains(t, sse, "Great diner scene.")
	require.Contains(t, sse, "Viewing added.")

	// Opening it for editing.
	sse = u.ds("GET", "/movies/1/mine?edit=1", nil)
	require.Contains(t, sse, `action="/viewings/1"`)
	require.Contains(t, sse, `value="4" aria-label="4 stars" checked`)

	// A bad date comes back in the open form, keeping what was typed.
	sse = u.ds("POST", "/viewings/1", url.Values{"viewed_on": {"someday"}, "rating": {"4"}, "review": {"Edited."}})
	require.Contains(t, sse, `action="/viewings/1"`)
	require.Contains(t, sse, "should be YYYY-MM-DD")
	require.Contains(t, sse, "Edited.")

	sse = u.ds("POST", "/viewings/1", url.Values{"viewed_on": {"2024-05-02"}, "rating": {"none"}, "review": {"Edited."}})
	require.NotContains(t, sse, `action="/viewings/1"`, "closed again")
	require.Contains(t, sse, "Saved.")
	v, err := u.app.c.Viewing(ctx, 1)
	require.NoError(t, err)
	require.Nil(t, v.Rating)
	require.Equal(t, "Edited.", v.Review)

	// A second viewing turns "Your review" into a list.
	sse = u.ds("POST", "/movies/1/viewings", url.Values{"viewed_on": {"2025-01-01"}, "rating": {"3.5"}})
	require.Contains(t, sse, "Your viewings")
	sse = u.ds("POST", "/movies/1/viewings", url.Values{"viewed_on": {"bad"}})
	require.Contains(t, sse, "<details class=\"add-viewing\" open>")
	require.Contains(t, sse, "should be YYYY-MM-DD")

	sse = u.ds("POST", "/viewings/1/delete", nil)
	require.Contains(t, sse, "Viewing deleted.")
	vs, err := u.app.c.Viewings(ctx, 1)
	require.NoError(t, err)
	require.Len(t, vs, 1)

	// A viewing that's gone: the page says so instead of breaking.
	sse = u.ds("POST", "/viewings/1/delete", nil)
	require.Contains(t, sse, "no longer exists")

	// Without JavaScript the old edit page still works.
	require.Contains(t, u.get("/viewings/2/edit"), "Edit viewing")
}

// A TV series is added like a movie, with type tv; its viewings may
// name a season and episode, and its rating is of the whole series.
func TestTVSeries(t *testing.T) {
	u := newTestUI(t)
	ctx := context.Background()

	loc, msg := u.post("/movies", url.Values{
		"type": {"tv"}, "title": {"Fargo"}, "year": {"2014"},
		"tmdb_id": {"https://www.themoviedb.org/tv/60622-fargo"},
	})
	require.Equal(t, "/movies/1", loc)
	require.Contains(t, msg, "Added Fargo")
	m, err := u.app.c.Movie(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, collection.TypeTV, m.Type)
	require.Equal(t, "60622", *m.TmdbID)

	// A movie TMDB URL doesn't name a series.
	loc, _ = u.post("/movies", url.Values{"type": {"tv"}, "title": {"Fargo"}, "tmdb_id": {"https://www.themoviedb.org/movie/275"}})
	require.Empty(t, loc, "re-renders the form")
	u.post("/movies", url.Values{"title": {"Fargo"}, "year": {"1996"}})

	page := u.get("/movies/1")
	require.Contains(t, page, "TV series")
	require.Contains(t, page, "Edit series")
	require.Contains(t, page, `name="season"`)
	require.Contains(t, page, "https://www.themoviedb.org/tv/60622")
	require.NotContains(t, u.get("/movies/2"), `name="season"`, "a movie's viewings have no season")

	sse := u.ds("POST", "/movies/1/viewings", url.Values{"season": {"1"}, "viewed_on": {"2015-03-01"}, "rating": {"4.5"}})
	require.Contains(t, sse, "Season 1")
	sse = u.ds("POST", "/movies/1/viewings", url.Values{"season": {"2"}, "episode": {"3"}, "viewed_on": {"2016-04-10"}, "rating": {"3"}})
	require.Contains(t, sse, "S2 E3")
	sse = u.ds("POST", "/movies/1/viewings", url.Values{"episode": {"3"}, "rating": {"3"}})
	require.Contains(t, sse, "an episode needs a season")
	sse = u.ds("POST", "/movies/2/viewings", url.Values{"season": {"1"}, "rating": {"3"}})
	require.Contains(t, sse, "only a TV series")

	// The star bar rates the whole series, not the latest season.
	sse = u.ds("POST", "/movies/1/rating", url.Values{"rating": {"4"}})
	require.Contains(t, sse, "Rated Fargo")
	vs, err := u.app.c.Viewings(ctx, 1)
	require.NoError(t, err)
	require.Len(t, vs, 3)
	whole, ok := collection.LatestWhole(vs)
	require.True(t, ok)
	require.Equal(t, int64(8), *whole.Rating)

	// Editing a season viewing keeps its season.
	for _, v := range vs {
		if collection.PartName(v.Season, v.Episode) == "Season 1" {
			sse = u.ds("GET", fmt.Sprintf("/movies/1/mine?edit=%d", v.ID), nil)
			require.Contains(t, sse, `name="season" value="1"`)
			require.Contains(t, u.get(fmt.Sprintf("/viewings/%d/edit", v.ID)), `name="season" value="1"`)
		}
	}

	// It can't become a movie while it has season viewings.
	loc, _ = u.post("/movies/1", url.Values{"type": {"movie"}, "title": {"Fargo"}})
	require.Empty(t, loc)
	m, err = u.app.c.Movie(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, collection.TypeTV, m.Type)

	// The list marks and filters series.
	list := u.get("/movies")
	require.Contains(t, list, `<span class="tag">TV</span>`)
	require.Contains(t, list, "1 movies, 1 TV series")
	require.NotContains(t, u.get("/movies?type=tv"), `href="/movies/2"`)
	require.NotContains(t, u.get("/movies?type=movie"), `href="/movies/1"`)
	require.Contains(t, u.get("/history"), "S2 E3")
}
