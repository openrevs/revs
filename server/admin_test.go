package server

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openrevs/revs/ratingset"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testAdminPassword = "correct horse"

// testAdmin is a server with its admin UI on a test HTTP server, a
// browser-like client (cookies kept, redirects not followed), and a
// clock the test moves.
type testAdmin struct {
	s      *Server
	ts     *httptest.Server
	client *http.Client

	mu  sync.Mutex
	now time.Time
}

func newTestAdmin(t *testing.T) *testAdmin {
	t.Helper()
	s := newMemServer(t, Options{AdminPassword: testAdminPassword})
	ta := &testAdmin{s: s, now: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
	s.admin.now = ta.clock
	s.admin.refresh = 10 * time.Millisecond

	ta.ts = httptest.NewServer(s.AdminHandler())
	t.Cleanup(ta.ts.Close)
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	ta.client = &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return ta
}

func (ta *testAdmin) clock() time.Time {
	ta.mu.Lock()
	defer ta.mu.Unlock()
	return ta.now
}

func (ta *testAdmin) advance(d time.Duration) {
	ta.mu.Lock()
	defer ta.mu.Unlock()
	ta.now = ta.now.Add(d)
}

func (ta *testAdmin) get(t *testing.T, path string) (*http.Response, string) {
	t.Helper()
	resp, err := ta.client.Get(ta.ts.URL + path)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, string(body)
}

func (ta *testAdmin) login(t *testing.T, password string) (*http.Response, string) {
	t.Helper()
	resp, err := ta.client.PostForm(ta.ts.URL+"/login", url.Values{"password": {password}})
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, string(body)
}

func TestAdminRequiresPasswordToListen(t *testing.T) {
	db, err := OpenMemoryDB()
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	_, key, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	_, err = New(Config{DB: db, Key: key, Publications: &MemPublications{},
		Options: Options{AdminListen: "127.0.0.1:0"}})
	require.Error(t, err)
}

func TestAdminNoPasswordNoLogin(t *testing.T) {
	s := newMemServer(t, Options{})
	ts := httptest.NewServer(s.AdminHandler())
	t.Cleanup(ts.Close)

	resp, err := http.PostForm(ts.URL+"/login", url.Values{"password": {""}})
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestAdminLogin(t *testing.T) {
	ta := newTestAdmin(t)

	// Pages need a session.
	resp, _ := ta.get(t, "/")
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	assert.Equal(t, "/login", resp.Header.Get("Location"))
	resp, _ = ta.get(t, "/updates")
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	resp, body := ta.login(t, "wrong")
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Contains(t, body, "Wrong password")

	resp, _ = ta.login(t, testAdminPassword)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	assert.Equal(t, "/", resp.Header.Get("Location"))

	resp, body = ta.get(t, "/")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, body, ta.s.serverID())
	assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))

	// Sessions expire.
	ta.advance(adminSessionTTL)
	resp, _ = ta.get(t, "/")
	assert.Equal(t, http.StatusSeeOther, resp.StatusCode)

	// And end at logout.
	ta.login(t, testAdminPassword)
	resp, err := ta.client.PostForm(ta.ts.URL+"/logout", nil)
	require.NoError(t, err)
	resp.Body.Close()
	resp, _ = ta.get(t, "/")
	assert.Equal(t, http.StatusSeeOther, resp.StatusCode)
}

func TestAdminLoginRateLimit(t *testing.T) {
	ta := newTestAdmin(t)

	for range adminLoginBurst {
		resp, _ := ta.login(t, "wrong")
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	}

	// Out of attempts: even the right password is refused unchecked.
	resp, body := ta.login(t, testAdminPassword)
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	assert.Equal(t, "12", resp.Header.Get("Retry-After"))
	assert.Contains(t, body, "Too many failed attempts")

	// One more attempt is allowed per adminLoginEvery.
	ta.advance(adminLoginEvery)
	resp, _ = ta.login(t, "wrong")
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	resp, _ = ta.login(t, testAdminPassword)
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)

	ta.advance(adminLoginEvery)
	resp, _ = ta.login(t, testAdminPassword)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)

	// Successful logins don't use up attempts.
	for range adminLoginBurst + 1 {
		resp, _ = ta.login(t, testAdminPassword)
		require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	}
}

func TestAdminCrossOriginLoginRefused(t *testing.T) {
	ta := newTestAdmin(t)
	req, err := http.NewRequest("POST", ta.ts.URL+"/login", strings.NewReader("password="+url.QueryEscape(testAdminPassword)))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err := ta.client.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestAdminDashboard(t *testing.T) {
	ctx := context.Background()
	ta := newTestAdmin(t)
	ta.login(t, testAdminPassword)

	pub := httptest.NewServer(ta.s.Handler())
	t.Cleanup(pub.Close)
	require.Equal(t, http.StatusOK, upload(t, pub, signedDoc(t, ratingset.NewUser(), 1, "Alien")).StatusCode)
	bob := ratingset.NewUser()
	require.Equal(t, http.StatusOK, upload(t, pub, signedDoc(t, bob, 1, "Heat")).StatusCode)
	require.Equal(t, http.StatusOK, upload(t, pub, signedDoc(t, bob, 2, "Heat", "Ronin")).StatusCode)
	_, err := ta.s.Cut(ctx)
	require.NoError(t, err)

	st, err := ta.s.Stats(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, st.Users)
	assert.Equal(t, 2, st.DirectUsers)
	assert.Equal(t, 3, st.Revisions)
	assert.NotZero(t, st.Bytes)
	assert.False(t, st.LastReceived.IsZero())
	require.Len(t, st.Channels, 2)
	for _, ch := range st.Channels {
		assert.Equal(t, 1, ch.Epoch, ch.Name)
		assert.Equal(t, 2, ch.Members, ch.Name)
		assert.False(t, ch.Cut.IsZero(), ch.Name)
	}

	_, body := ta.get(t, "/")
	assert.Contains(t, body, `<td>main</td><td>1</td><td>2</td>`)
	assert.Contains(t, body, "2 uploaded here")
}

// TestAdminUpdates checks that the dashboard's stream sends the stats
// and sends them again when they change.
func TestAdminUpdates(t *testing.T) {
	ta := newTestAdmin(t)
	ta.login(t, testAdminPassword)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", ta.ts.URL+"/updates", nil)
	require.NoError(t, err)
	req.Header.Set("Datastar-Request", "true")
	resp, err := ta.client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "text/event-stream")

	events := make(chan string)
	go func() {
		defer close(events)
		sc := bufio.NewScanner(resp.Body)
		var ev strings.Builder
		for sc.Scan() {
			if sc.Text() == "" {
				if ev.Len() == 0 {
					continue
				}
				events <- ev.String()
				ev.Reset()
				continue
			}
			ev.WriteString(sc.Text() + "\n")
		}
	}()
	next := func() string {
		select {
		case ev := <-events:
			return ev
		case <-time.After(5 * time.Second):
			t.Fatal("no update")
			return ""
		}
	}

	ev := next()
	assert.Contains(t, ev, "datastar-patch-elements")
	assert.Contains(t, ev, `id="stats"`)
	assert.Contains(t, ev, "<td>main</td><td>0</td>")

	pub := httptest.NewServer(ta.s.Handler())
	t.Cleanup(pub.Close)
	require.Equal(t, http.StatusOK, upload(t, pub, signedDoc(t, ratingset.NewUser(), 1, "Alien")).StatusCode)
	_, err = ta.s.Cut(context.Background())
	require.NoError(t, err)

	// An update may come between the upload and the cut.
	for !strings.Contains(next(), "<td>main</td><td>1</td><td>1</td>") {
	}
}

func TestAdminUpstreamStats(t *testing.T) {
	ctx := context.Background()
	up, upTS := newTestServer(t)
	down, _ := newTestServer(t)
	require.NoError(t, down.AddUpstream(upTS.URL))
	require.NoError(t, down.AddUpstream("http://127.0.0.1:1/"))

	st, err := down.Stats(ctx)
	require.NoError(t, err)
	require.Len(t, st.Upstreams, 2)
	assert.True(t, st.Upstreams[0].LastPull.IsZero())

	require.Equal(t, http.StatusOK, upload(t, upTS, signedDoc(t, ratingset.NewUser(), 1, "Alien")).StatusCode)
	_, err = up.Cut(ctx)
	require.NoError(t, err)
	require.Error(t, down.Pull(ctx)) // the second upstream is unreachable

	st, err = down.Stats(ctx)
	require.NoError(t, err)
	assert.False(t, st.Upstreams[0].LastPull.IsZero())
	assert.Empty(t, st.Upstreams[0].LastErr)
	assert.Equal(t, 1, st.Upstreams[0].Accepted)
	assert.NotEmpty(t, st.Upstreams[1].LastErr)
	assert.Equal(t, 1, st.Users)
	assert.Equal(t, 0, st.DirectUsers)
}
