package client_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openrevs/revs"
	"github.com/openrevs/revs/client"
	"github.com/openrevs/revs/client/fsstore"
	"github.com/openrevs/revs/client/sqlitestore"
	"github.com/openrevs/revs/domain"
	"github.com/stretchr/testify/require"
)

// startServer runs a real revs server over httptest. The client package
// has no dependency on the server package; this test package uses both,
// which is exactly the interop being tested.
func startServer(t *testing.T) (*revs.Server, *httptest.Server) {
	t.Helper()

	s, err := revs.NewServer(t.TempDir())
	require.NoError(t, err)

	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	return s, ts
}

// newClientAt opens a client on an explicit store directory, so tests
// can reopen the same store with a fresh client instance.
func newClientAt(t *testing.T, url, dir string, opts ...client.Option) *client.Client {
	t.Helper()

	store, err := fsstore.New(dir)
	require.NoError(t, err)

	return client.New(url, store, opts...)
}

func newClient(t *testing.T, url string, opts ...client.Option) *client.Client {
	t.Helper()

	return newClientAt(t, url, t.TempDir(), opts...)
}

func newKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()

	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	return key
}

// content builds unsigned JSONL data lines: one user line plus a title
// line per name.
func content(key ed25519.PrivateKey, titles ...string) []byte {
	uid := client.UserID(key.Public().(ed25519.PublicKey))

	var buf bytes.Buffer
	fmt.Fprintf(&buf, `{"id":%q,"min_rating":0,"max_rating":5}`+"\n", uid)
	for _, title := range titles {
		fmt.Fprintf(&buf, `{"name":%q}`+"\n", title)
	}

	return buf.Bytes()
}

func manyTitles(n int) []string {
	titles := make([]string, n)
	for i := range titles {
		titles[i] = fmt.Sprintf("A Movie With a Reasonably Long Name, Part %d", i)
	}
	return titles
}

// dataLines strips the sjsonl control line off a signed document,
// recovering the unsigned content an app would hand back to Push.
func dataLines(t *testing.T, doc []byte) []byte {
	t.Helper()

	i := bytes.IndexByte(doc, '\n')
	require.Greater(t, i, 0)

	return doc[i+1:]
}

func TestPushSyncRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, ts := startServer(t)

	key := newKey(t)
	uid := client.UserID(key.Public().(ed25519.PublicKey))

	publisher := newClient(t, ts.URL)
	consumer := newClient(t, ts.URL)

	// Sync against a server with nothing published is a clean no-op.
	res, err := consumer.Sync(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, res.Epoch)

	// First push asserts rev 1; an identical push is skipped entirely.
	pr, err := publisher.Push(ctx, key, content(key, "Alien", "Heat"))
	require.NoError(t, err)
	require.True(t, pr.Pushed)
	require.Equal(t, 1, pr.Rev)

	pr, err = publisher.Push(ctx, key, content(key, "Alien", "Heat"))
	require.NoError(t, err)
	require.False(t, pr.Pushed)
	require.Equal(t, 1, pr.Rev)

	require.Equal(t, 1, cut(t, s))

	// The fresh consumer starts from the epoch-1 snapshot and holds the
	// verified document.
	res, err = consumer.Sync(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, res.Epoch)
	require.True(t, res.Bootstrapped)
	require.Equal(t, "fresh", res.BootstrapReason)
	require.Equal(t, []string{uid}, res.Changed)

	doc, err := consumer.Document(uid)
	require.NoError(t, err)
	require.Equal(t, 1, doc.Rev)
	require.Contains(t, string(doc.Data), "Alien")

	pr, err = publisher.Push(ctx, key, content(key, "Alien", "Heat", "Ran"))
	require.NoError(t, err)
	require.Equal(t, 2, pr.Rev)

	require.Equal(t, 2, cut(t, s))

	// A changed push lands as a patch.
	res, err = consumer.Sync(ctx)
	require.NoError(t, err)
	require.False(t, res.Bootstrapped)
	require.Equal(t, 1, res.FromEpoch)
	require.Equal(t, 2, res.Epoch)
	require.Equal(t, []string{uid}, res.Changed)

	doc, err = consumer.Document(uid)
	require.NoError(t, err)
	require.Equal(t, 2, doc.Rev)
	require.Contains(t, string(doc.Data), "Ran")

	// Nothing new: sync is a no-op with no changes reported.
	res, err = consumer.Sync(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, res.Epoch)
	require.Empty(t, res.Changed)

	// Iteration yields the corpus.
	var ids []string
	for doc, err := range consumer.Documents() {
		require.NoError(t, err)
		ids = append(ids, doc.UserID)
	}
	require.Equal(t, []string{uid}, ids)

	_, err = consumer.Document("nonexistent")
	require.ErrorIs(t, err, client.ErrNotFound)
}

func TestUnchangedPushAfterSync(t *testing.T) {
	ctx := context.Background()
	s, ts := startServer(t)
	key := newKey(t)
	uid := client.UserID(key.Public().(ed25519.PublicKey))

	deviceA := newClient(t, ts.URL)
	_, err := deviceA.Push(ctx, key, content(key, "Alien"))
	require.NoError(t, err)
	_, err = s.Cut(ctx)
	require.NoError(t, err)

	// Device B has never pushed. After syncing, re-pushing the pulled
	// content is recognized as unchanged against the pulled head.
	deviceB := newClient(t, ts.URL)
	_, err = deviceB.Sync(ctx)
	require.NoError(t, err)

	doc, err := deviceB.Document(uid)
	require.NoError(t, err)

	pr, err := deviceB.Push(ctx, key, dataLines(t, doc.Data))
	require.NoError(t, err)
	require.False(t, pr.Pushed)
	require.Equal(t, 1, pr.Rev)
}

func TestStalePush(t *testing.T) {
	ctx := context.Background()
	_, ts := startServer(t)
	key := newKey(t)

	deviceA := newClient(t, ts.URL)
	deviceB := newClient(t, ts.URL)

	pr, err := deviceA.Push(ctx, key, content(key, "Alien"))
	require.NoError(t, err)
	require.Equal(t, 1, pr.Rev)

	// Device B knows nothing of rev 1, so its assertion collides. The
	// conflict surfaces; the recorded server revision lets the next
	// push (after the app resolves content) assert past it.
	_, err = deviceB.Push(ctx, key, content(key, "Heat"))
	var stale *client.StaleError
	require.ErrorAs(t, err, &stale)
	require.Equal(t, 1, stale.LatestRev)

	pr, err = deviceB.Push(ctx, key, content(key, "Heat"))
	require.NoError(t, err)
	require.Equal(t, 2, pr.Rev)

	// Device A is now the stale one.
	_, err = deviceA.Push(ctx, key, content(key, "Ran"))
	require.ErrorAs(t, err, &stale)
	require.Equal(t, 2, stale.LatestRev)

	pr, err = deviceA.Push(ctx, key, content(key, "Ran"))
	require.NoError(t, err)
	require.Equal(t, 3, pr.Rev)
}

// rewriteIndex proxies to upstream, mutating index.json so retention
// states the server itself never enters (v1 keeps everything) can be
// exercised.
// cut runs a cut and returns the main channel's new epoch (0 if it
// didn't change).
func cut(t *testing.T, s *revs.Server) int {
	t.Helper()

	epochs, err := s.Cut(context.Background())
	require.NoError(t, err)
	return epochs[client.DefaultChannel]
}

// proxy fronts a server, recording the paths requested and optionally
// rewriting the main channel's entry in index.json.
func proxy(t *testing.T, upstream string, mutate func(ch map[string]any)) (*httptest.Server, *[]string) {
	t.Helper()

	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)

		resp, err := http.Get(upstream + r.URL.Path)
		require.NoError(t, err)
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		if mutate != nil && resp.StatusCode == http.StatusOK && r.URL.Path == "/v1/index.json" {
			var idx map[string]any
			require.NoError(t, json.Unmarshal(body, &idx))
			mutate(idx["channels"].(map[string]any)[client.DefaultChannel].(map[string]any))
			body, err = json.Marshal(idx)
			require.NoError(t, err)
		}

		w.WriteHeader(resp.StatusCode)
		w.Write(body)
	}))
	t.Cleanup(srv.Close)

	return srv, &paths
}

func archivePaths(paths []string) []string {
	var out []string
	for _, p := range paths {
		if strings.HasSuffix(p, ".tar.gz") {
			out = append(out, p)
		}
	}
	return out
}

func TestSnapshotBootstrap(t *testing.T) {
	ctx := context.Background()
	s, ts := startServer(t)
	s.SetSnapshotEvery(4)
	key := newKey(t)
	uid := client.UserID(key.Public().(ed25519.PublicKey))

	publisher := newClient(t, ts.URL)
	for i, title := range []string{"Alien", "Heat", "Ran"} {
		_, err := publisher.Push(ctx, key, content(key, title))
		require.NoError(t, err)
		require.Equal(t, i+1, cut(t, s))
	}

	// A fresh consumer loads the newest snapshot (epoch 1), then takes
	// patches the rest of the way.
	behindDir := t.TempDir()
	consumer := newClientAt(t, ts.URL, behindDir)
	res, err := consumer.Sync(ctx)
	require.NoError(t, err)
	require.True(t, res.Bootstrapped)
	require.Equal(t, "fresh", res.BootstrapReason)
	require.Equal(t, 3, res.Epoch)

	doc, err := consumer.Document(uid)
	require.NoError(t, err)
	require.Equal(t, 3, doc.Rev)
	require.Contains(t, string(doc.Data), "Ran")

	// When no patch starts at the consumer's epoch, it falls back to the
	// newest snapshot and continues from there.
	_, err = publisher.Push(ctx, key, content(key, "Stalker"))
	require.NoError(t, err)
	require.Equal(t, 4, cut(t, s))

	noPatches, paths := proxy(t, ts.URL, func(ch map[string]any) {
		ch["patches"] = []any{}
	})
	behind := newClientAt(t, noPatches.URL, behindDir)
	res, err = behind.Sync(ctx)
	require.NoError(t, err)
	require.True(t, res.Bootstrapped)
	require.Equal(t, "fallback", res.BootstrapReason)
	require.Equal(t, 4, res.Epoch)
	require.Equal(t, []string{uid}, res.Changed)
	require.Equal(t, []string{"/v1/main/snapshot-4_1.tar.gz"}, archivePaths(*paths))

	doc, err = behind.Document(uid)
	require.NoError(t, err)
	require.Equal(t, 4, doc.Rev)
}

// TestMultiPartSync syncs snapshots and patches split across several
// parts (tiny part target: one entry per part).
func TestMultiPartSync(t *testing.T) {
	ctx := context.Background()
	s, ts := startServer(t)
	s.SetPartSize(1)

	keys := []ed25519.PrivateKey{newKey(t), newKey(t), newKey(t)}
	publisher := newClient(t, ts.URL)
	for i, key := range keys {
		_, err := publisher.Push(ctx, key, content(key, fmt.Sprintf("Movie %d", i)))
		require.NoError(t, err)
	}
	cut(t, s)

	consumer := newClient(t, ts.URL)
	res, err := consumer.Sync(ctx)
	require.NoError(t, err)
	require.True(t, res.Bootstrapped)
	require.Len(t, res.Changed, len(keys))

	for i, key := range keys {
		_, err := publisher.Push(ctx, key, content(key, fmt.Sprintf("Movie %d", i), "Sequel"))
		require.NoError(t, err)
	}
	cut(t, s)

	res, err = consumer.Sync(ctx)
	require.NoError(t, err)
	require.False(t, res.Bootstrapped)
	require.Len(t, res.Changed, len(keys))

	for i, key := range keys {
		doc, err := consumer.Document(client.UserID(key.Public().(ed25519.PublicKey)))
		require.NoError(t, err)
		require.Equal(t, 2, doc.Rev)
		require.Contains(t, string(doc.Data), fmt.Sprintf("Movie %d", i))
	}
}

// TestCompactedCatchUp checks a stale consumer takes the largest patch
// starting at its epoch.
func TestCompactedCatchUp(t *testing.T) {
	ctx := context.Background()
	s, ts := startServer(t)
	s.SetCompactEvery(2)
	key := newKey(t)
	uid := client.UserID(key.Public().(ed25519.PublicKey))
	titles := manyTitles(40)

	publisher := newClient(t, ts.URL)
	push := func(rev int) {
		titles[0] = fmt.Sprintf("Rev %d", rev)
		pr, err := publisher.Push(ctx, key, content(key, titles...))
		require.NoError(t, err)
		require.Equal(t, rev, pr.Rev)
		require.Equal(t, rev, cut(t, s))
	}
	push(1)
	push(2)

	rec, paths := proxy(t, ts.URL, nil)
	consumer := newClient(t, rec.URL)
	_, err := consumer.Sync(ctx)
	require.NoError(t, err)

	push(3)
	push(4)

	*paths = nil
	res, err := consumer.Sync(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, res.FromEpoch)
	require.Equal(t, 4, res.Epoch)
	require.False(t, res.Bootstrapped)
	require.Equal(t, []string{"/v1/main/patch-2-4_1.tar.gz"}, archivePaths(*paths))

	doc, err := consumer.Document(uid)
	require.NoError(t, err)
	require.Equal(t, 4, doc.Rev)
	require.Contains(t, string(doc.Data), "Rev 4")
}

func TestChannels(t *testing.T) {
	ctx := context.Background()
	s, ts := startServer(t)

	// A channel that leaves out users that fail vetting.
	rejected := map[string]bool{}
	require.NoError(t, s.AddChannel("vetted", func(d *domain.Document) bool { return !rejected[d.Userid] }))

	k1, k2 := newKey(t), newKey(t)
	u1, u2 := client.UserID(k1.Public().(ed25519.PublicKey)), client.UserID(k2.Public().(ed25519.PublicKey))
	publisher := newClient(t, ts.URL)
	for _, k := range []ed25519.PrivateKey{k1, k2} {
		_, err := publisher.Push(ctx, k, content(k, "Alien"))
		require.NoError(t, err)
	}
	cut(t, s)

	dir := t.TempDir()
	vetted := newClientAt(t, ts.URL, dir, client.WithChannel("vetted"))
	res, err := vetted.Sync(ctx)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{u1, u2}, res.Changed)

	// A user removed from the channel leaves the consumer's view of it.
	rejected[u2] = true
	cut(t, s)
	res, err = vetted.Sync(ctx)
	require.NoError(t, err)
	require.False(t, res.Bootstrapped)
	require.Equal(t, []string{u2}, res.Changed)
	_, err = vetted.Document(u2)
	require.ErrorIs(t, err, client.ErrNotFound)
	_, err = vetted.Document(u1)
	require.NoError(t, err)

	// Switching the store to another channel rebuilds the corpus.
	mainClient := newClientAt(t, ts.URL, dir)
	res, err = mainClient.Sync(ctx)
	require.NoError(t, err)
	require.True(t, res.Bootstrapped)
	_, err = mainClient.Document(u2)
	require.NoError(t, err)

	// An unknown channel is an error.
	_, err = newClient(t, ts.URL, client.WithChannel("nope")).Sync(ctx)
	require.ErrorContains(t, err, "nope")
}

func TestDivergenceAndRebootstrap(t *testing.T) {
	ctx := context.Background()
	s, ts := startServer(t)
	key := newKey(t)
	uid := client.UserID(key.Public().(ed25519.PublicKey))

	// A large corpus with a small change, so the cut ships an sjsonl
	// patch (with its base-hash check) rather than a full doc.
	titles := manyTitles(40)

	publisher := newClient(t, ts.URL)
	_, err := publisher.Push(ctx, key, content(key, titles...))
	require.NoError(t, err)
	cut(t, s)

	storeDir := t.TempDir()
	consumer := newClientAt(t, ts.URL, storeDir)
	_, err = consumer.Sync(ctx)
	require.NoError(t, err)
	base, err := consumer.Document(uid)
	require.NoError(t, err)

	titles[7] = "替身演員" // one changed line: diff is far smaller than the doc
	_, err = publisher.Push(ctx, key, content(key, titles...))
	require.NoError(t, err)
	cut(t, s)

	// Lose the document the next patch applies to. With no snapshot to
	// fall back on, no route leads forward.
	require.NoError(t, os.Remove(filepath.Join(storeDir, "docs", base.Hash[:2], base.Hash)))

	noSnapshots, _ := proxy(t, ts.URL, func(ch map[string]any) {
		ch["snapshots"] = []any{}
	})
	_, err = newClientAt(t, noSnapshots.URL, storeDir).Sync(ctx)
	var diverged *client.DivergedError
	require.ErrorAs(t, err, &diverged)
	require.Equal(t, uid, diverged.UserID)
	require.Equal(t, base.Hash, diverged.WantBase)

	// With a snapshot available, sync repairs the corpus from it.
	res, err := newClientAt(t, ts.URL, storeDir).Sync(ctx)
	require.NoError(t, err)
	require.True(t, res.Bootstrapped)
	require.Equal(t, "fallback", res.BootstrapReason)
	require.Equal(t, 2, res.Epoch)

	// An explicit rebuild starts over from the snapshot.
	res, err = newClientAt(t, ts.URL, storeDir).Rebootstrap(ctx)
	require.NoError(t, err)
	require.True(t, res.Bootstrapped)
	require.Equal(t, "rebootstrap", res.BootstrapReason)
	require.Equal(t, 2, res.Epoch)

	doc, err := newClientAt(t, ts.URL, storeDir).Document(uid)
	require.NoError(t, err)
	require.Equal(t, 2, doc.Rev)
	require.Contains(t, string(doc.Data), "替身演員")
}

func TestPushContentValidation(t *testing.T) {
	ctx := context.Background()
	_, ts := startServer(t)
	key := newKey(t)
	c := newClient(t, ts.URL)

	// No id line.
	_, err := c.Push(ctx, key, []byte(`{"name":"Alien"}`+"\n"))
	require.ErrorContains(t, err, "no id line")

	// Multiple id lines.
	two := append(content(key), content(key, "x")...)
	_, err = c.Push(ctx, key, two)
	require.ErrorContains(t, err, "multiple id lines")

	// id not matching the signing key.
	otherKey := newKey(t)
	_, err = c.Push(ctx, key, content(otherKey, "Alien"))
	require.ErrorContains(t, err, "does not match signing key")
}

// recordUploads proxies to upstream, recording the sjsonl type ("doc"
// or "patch") of each upload body.
func recordUploads(t *testing.T, upstream string) (*httptest.Server, *[]string) {
	t.Helper()

	var kinds []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)

		if r.URL.Path == "/upload" {
			first, _, _ := bytes.Cut(body, []byte("\n"))
			var ctl struct {
				SJSONL string `json:"sjsonl"`
			}
			require.NoError(t, json.Unmarshal(first, &ctl))
			kinds = append(kinds, ctl.SJSONL)
		}

		req, err := http.NewRequest(r.Method, upstream+r.URL.Path, bytes.NewReader(body))
		require.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		respBody, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		w.WriteHeader(resp.StatusCode)
		w.Write(respBody)
	}))
	t.Cleanup(proxy.Close)

	return proxy, &kinds
}

func TestPatchPush(t *testing.T) {
	ctx := context.Background()
	s, ts := startServer(t)
	proxy, kinds := recordUploads(t, ts.URL)

	key := newKey(t)
	uid := client.UserID(key.Public().(ed25519.PublicKey))
	titles := manyTitles(50)

	deviceA := newClient(t, proxy.URL)

	// The first push has no base, so it is a full document; later pushes
	// patch against the previous push.
	_, err := deviceA.Push(ctx, key, content(key, titles[:40]...))
	require.NoError(t, err)
	pr, err := deviceA.Push(ctx, key, content(key, titles[:41]...))
	require.NoError(t, err)
	require.Equal(t, 2, pr.Rev)
	require.Equal(t, []string{"doc", "patch"}, *kinds)

	_, err = s.Cut(ctx)
	require.NoError(t, err)

	consumer := newClient(t, ts.URL)
	_, err = consumer.Sync(ctx)
	require.NoError(t, err)
	doc, err := consumer.Document(uid)
	require.NoError(t, err)
	require.Equal(t, 2, doc.Rev)
	require.Equal(t, pr.Hash, doc.Hash)

	// A device that has only pulled patches against its synced head.
	deviceB := newClient(t, proxy.URL)
	_, err = deviceB.Sync(ctx)
	require.NoError(t, err)
	*kinds = nil
	pr, err = deviceB.Push(ctx, key, content(key, titles[:42]...))
	require.NoError(t, err)
	require.Equal(t, 3, pr.Rev)
	require.Equal(t, []string{"patch"}, *kinds)

	// Device A's base (rev 2) is no longer the server's latest: the patch
	// is refused, and the server's reported rev 3 makes it stale.
	*kinds = nil
	_, err = deviceA.Push(ctx, key, content(key, titles[:43]...))
	var stale *client.StaleError
	require.ErrorAs(t, err, &stale)
	require.Equal(t, 3, stale.LatestRev)
	require.Equal(t, []string{"patch"}, *kinds)
}

// uploadDoc posts a signed document straight to a server, standing in
// for one server pulling it from another.
func uploadDoc(t *testing.T, url string, doc []byte) {
	t.Helper()

	resp, err := http.Post(url+"/upload", "application/jsonl", bytes.NewReader(doc))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestPushAcrossServers(t *testing.T) {
	ctx := context.Background()
	_, tsA := startServer(t)
	_, tsB := startServer(t)
	proxyA, kindsA := recordUploads(t, tsA.URL)
	proxyB, kindsB := recordUploads(t, tsB.URL)

	key := newKey(t)
	uid := client.UserID(key.Public().(ed25519.PublicKey))
	titles := manyTitles(50)

	// One author history shared by a client per server.
	hs, err := fsstore.New(t.TempDir())
	require.NoError(t, err)
	history := client.NewHistory(hs)
	toA := newClient(t, proxyA.URL, client.WithHistory(history))
	toB := newClient(t, proxyB.URL, client.WithHistory(history))

	pr, err := toA.Push(ctx, key, content(key, titles[:40]...))
	require.NoError(t, err)
	require.Equal(t, 1, pr.Rev)

	// Revisions are the author's, not per server: B gets the same rev 1
	// document, then rev 2.
	pr, err = toB.Push(ctx, key, content(key, titles[:40]...))
	require.NoError(t, err)
	require.True(t, pr.Pushed)
	require.Equal(t, 1, pr.Rev)

	pr, err = toB.Push(ctx, key, content(key, titles[:41]...))
	require.NoError(t, err)
	require.Equal(t, 2, pr.Rev)
	require.Equal(t, []string{"doc", "patch"}, *kindsB)

	// A pulls rev 2 from B behind our back.
	rev2, err := history.Version(uid, pr.Hash)
	require.NoError(t, err)
	uploadDoc(t, tsA.URL, rev2.Data)

	// Pushing rev 3 to A: the patch against rev 1 is refused, A reports
	// rev 2, which the history holds, so the second patch is against it.
	*kindsA = nil
	pr, err = toA.Push(ctx, key, content(key, titles[:42]...))
	require.NoError(t, err)
	require.True(t, pr.Pushed)
	require.Equal(t, 3, pr.Rev)
	require.Equal(t, []string{"patch", "patch"}, *kindsA)

	// B pulls rev 3 from A; pushing the same content to B finds it there.
	rev3, err := history.Version(uid, pr.Hash)
	require.NoError(t, err)
	uploadDoc(t, tsB.URL, rev3.Data)

	*kindsB = nil
	pr, err = toB.Push(ctx, key, content(key, titles[:42]...))
	require.NoError(t, err)
	require.False(t, pr.Pushed)
	require.Equal(t, 3, pr.Rev)
	require.Equal(t, []string{"patch"}, *kindsB)
}

func historyStores(t *testing.T) map[string]func() client.HistoryStore {
	dir := t.TempDir()
	path := filepath.Join(t.TempDir(), "history.db")

	return map[string]func() client.HistoryStore{
		"fsstore": func() client.HistoryStore {
			s, err := fsstore.New(dir)
			require.NoError(t, err)
			return s
		},
		"sqlitestore": func() client.HistoryStore {
			s, err := sqlitestore.Open(path)
			require.NoError(t, err)
			t.Cleanup(func() { s.Close() })
			return s
		},
	}
}

func TestHistory(t *testing.T) {
	for name, open := range historyStores(t) {
		t.Run(name, func(t *testing.T) {
			key := newKey(t)
			uid := client.UserID(key.Public().(ed25519.PublicKey))
			titles := manyTitles(30)

			store := open()
			h := client.NewHistory(store, client.WithHistoryLimit(5))

			_, err := h.Latest(uid)
			require.ErrorIs(t, err, client.ErrNotFound)

			// Each change is a new revision; unchanged content is not.
			var docs []*client.Document
			for i := range 8 {
				doc, err := h.Commit(key, content(key, titles[:20+i]...))
				require.NoError(t, err)
				require.Equal(t, i+1, doc.Rev)
				docs = append(docs, doc)
			}
			again, err := h.Commit(key, content(key, titles[:27]...))
			require.NoError(t, err)
			require.Equal(t, docs[7], again)

			// A revision known elsewhere forces the next commit past it,
			// even with unchanged content.
			// A known rev of our own changes nothing.
			require.NoError(t, h.NoteRev(uid, 8, docs[7].Hash))
			again, err = h.Commit(key, content(key, titles[:27]...))
			require.NoError(t, err)
			require.Equal(t, docs[7], again)

			require.NoError(t, h.NoteRev(uid, 10, ""))
			bumped, err := h.Commit(key, content(key, titles[:27]...))
			require.NoError(t, err)
			require.Equal(t, 11, bumped.Rev)
			docs = append(docs, bumped)

			// Reopened from the store: the newest 5 versions are rebuilt
			// exactly; older ones were dropped.
			h = client.NewHistory(open(), client.WithHistoryLimit(5))
			latest, err := h.Latest(uid)
			require.NoError(t, err)
			require.Equal(t, bumped, latest)

			for i, doc := range docs {
				got, err := h.Version(uid, doc.Hash)
				if i < len(docs)-5 {
					require.ErrorIs(t, err, client.ErrNotFound, "rev %d", doc.Rev)
					continue
				}
				require.NoError(t, err, "rev %d", doc.Rev)
				require.Equal(t, doc, got)
			}

			// Older versions are stored as patches, not full documents.
			vh, err := store.LoadHistory(uid)
			require.NoError(t, err)
			require.Len(t, vh.Versions, 5)
			for _, v := range vh.Versions[:4] {
				require.True(t, bytes.HasPrefix(v.Data, []byte(`{"sjsonl":"patch"`)), "rev %d", v.Rev)
			}
		})
	}
}
