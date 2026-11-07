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
	"testing"

	"github.com/kalafut/dmrs"
	"github.com/kalafut/dmrs/client"
	"github.com/kalafut/dmrs/client/fsstore"
	"github.com/stretchr/testify/require"
)

// startServer runs a real dmrs server over httptest. The client package
// has no dependency on the server package; this test package uses both,
// which is exactly the interop being tested.
func startServer(t *testing.T) (*dmrs.Server, *httptest.Server) {
	t.Helper()

	s, err := dmrs.NewServer(t.TempDir())
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

	epoch, err := s.Cut(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, epoch)

	// The fresh consumer catches up (epoch 1 retains the full chain, so
	// no snapshot is needed) and holds the verified document.
	res, err = consumer.Sync(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, res.Epoch)
	require.False(t, res.Bootstrapped)
	require.Equal(t, []string{uid}, res.Changed)

	doc, err := consumer.Document(uid)
	require.NoError(t, err)
	require.Equal(t, 1, doc.Rev)
	require.Contains(t, string(doc.Data), "Alien")

	// A changed push lands as a chained patch epoch.
	pr, err = publisher.Push(ctx, key, content(key, "Alien", "Heat", "Ran"))
	require.NoError(t, err)
	require.Equal(t, 2, pr.Rev)

	epoch, err = s.Cut(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, epoch)

	res, err = consumer.Sync(ctx)
	require.NoError(t, err)
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
func rewriteIndex(t *testing.T, upstream string, mutate func(map[string]any)) *httptest.Server {
	t.Helper()

	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp, err := http.Get(upstream + r.URL.Path)
		require.NoError(t, err)
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		if resp.StatusCode == http.StatusOK && r.URL.Path == "/v1/index.json" {
			var idx map[string]any
			require.NoError(t, json.Unmarshal(body, &idx))
			mutate(idx)
			body, err = json.Marshal(idx)
			require.NoError(t, err)
		}

		w.WriteHeader(resp.StatusCode)
		w.Write(body)
	}))
	t.Cleanup(proxy.Close)

	return proxy
}

func TestSnapshotBootstrap(t *testing.T) {
	ctx := context.Background()
	s, ts := startServer(t)
	key := newKey(t)
	uid := client.UserID(key.Public().(ed25519.PublicKey))

	publisher := newClient(t, ts.URL)
	for i, title := range []string{"Alien", "Heat", "Ran"} {
		_, err := publisher.Push(ctx, key, content(key, title))
		require.NoError(t, err)
		epoch, err := s.Cut(ctx)
		require.NoError(t, err)
		require.Equal(t, i+1, epoch)
	}

	// Claim the early patches are gone: a fresh consumer must bootstrap
	// from the snapshot (cut at epoch 1), then chain the rest.
	proxy := rewriteIndex(t, ts.URL, func(idx map[string]any) {
		idx["oldest_retained_epoch"] = 2
	})

	consumer := newClient(t, proxy.URL)
	res, err := consumer.Sync(ctx)
	require.NoError(t, err)
	require.True(t, res.Bootstrapped)
	require.Equal(t, "fresh", res.BootstrapReason)
	require.Equal(t, 3, res.Epoch)

	doc, err := consumer.Document(uid)
	require.NoError(t, err)
	require.Equal(t, 3, doc.Rev)
	require.Contains(t, string(doc.Data), "Ran")

	// A consumer stuck before the horizon re-bootstraps the same way.
	behindDir := t.TempDir()
	behind := newClientAt(t, ts.URL, behindDir)
	_, err = behind.Sync(ctx)
	require.NoError(t, err)

	_, err = publisher.Push(ctx, key, content(key, "Stalker"))
	require.NoError(t, err)
	_, err = s.Cut(ctx)
	require.NoError(t, err)

	proxy2 := rewriteIndex(t, ts.URL, func(idx map[string]any) {
		idx["oldest_retained_epoch"] = 6
	})
	stale := newClientAt(t, proxy2.URL, behindDir)
	res, err = stale.Sync(ctx)
	require.NoError(t, err)
	require.True(t, res.Bootstrapped)
	require.Equal(t, "stale", res.BootstrapReason)
	require.Equal(t, 4, res.Epoch)
}

// TestChunkedSnapshotBootstrap bootstraps from a snapshot spanning
// multiple chunk files (tiny chunk target: one chunk per member).
func TestChunkedSnapshotBootstrap(t *testing.T) {
	ctx := context.Background()
	s, ts := startServer(t)
	s.SetSnapshotChunkSize(1)

	// Three users published before the first cut, so the epoch-1
	// snapshot spans three chunks.
	keys := []ed25519.PrivateKey{newKey(t), newKey(t), newKey(t)}
	publisher := newClient(t, ts.URL)
	for i, key := range keys {
		_, err := publisher.Push(ctx, key, content(key, fmt.Sprintf("Movie %d", i)))
		require.NoError(t, err)
	}
	_, err := s.Cut(ctx)
	require.NoError(t, err)

	// Force a fresh consumer through the snapshot path.
	proxy := rewriteIndex(t, ts.URL, func(idx map[string]any) {
		idx["oldest_retained_epoch"] = 2
	})

	consumer := newClient(t, proxy.URL)
	res, err := consumer.Sync(ctx)
	require.NoError(t, err)
	require.True(t, res.Bootstrapped)
	require.Len(t, res.Changed, len(keys))

	for i, key := range keys {
		doc, err := consumer.Document(client.UserID(key.Public().(ed25519.PublicKey)))
		require.NoError(t, err)
		require.Contains(t, string(doc.Data), fmt.Sprintf("Movie %d", i))
	}
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
	_, err = s.Cut(ctx)
	require.NoError(t, err)

	storeDir := t.TempDir()
	store, err := fsstore.New(storeDir)
	require.NoError(t, err)

	consumer := client.New(ts.URL, store)
	_, err = consumer.Sync(ctx)
	require.NoError(t, err)

	titles[7] = "替身演員" // one changed line: diff is far smaller than the doc
	_, err = publisher.Push(ctx, key, content(key, titles...))
	require.NoError(t, err)
	_, err = s.Cut(ctx)
	require.NoError(t, err)

	// Corrupt the consumer's idea of what it holds. The next patch's
	// base check must catch it, and must not silently repair.
	state, err := store.LoadState()
	require.NoError(t, err)
	head := state.Heads[uid]
	head.Hash = "deadbeefdeadbeefdeadbeefdeadbeef"
	state.Heads[uid] = head
	require.NoError(t, store.SaveState(state))

	tampered := client.New(ts.URL, store)
	_, err = tampered.Sync(ctx)
	var diverged *client.DivergedError
	require.ErrorAs(t, err, &diverged)
	require.Equal(t, uid, diverged.UserID)

	// Explicit recovery rebuilds from the snapshot and chains forward.
	res, err := tampered.Rebootstrap(ctx)
	require.NoError(t, err)
	require.True(t, res.Bootstrapped)
	require.Equal(t, "rebootstrap", res.BootstrapReason)
	require.Equal(t, 2, res.Epoch)

	doc, err := tampered.Document(uid)
	require.NoError(t, err)
	require.Equal(t, 2, doc.Rev)
	require.Contains(t, string(doc.Data), "替身演員")
}

func TestKeyPinning(t *testing.T) {
	ctx := context.Background()
	s, ts := startServer(t)
	key := newKey(t)

	publisher := newClient(t, ts.URL)
	_, err := publisher.Push(ctx, key, content(key, "Alien"))
	require.NoError(t, err)
	_, err = s.Cut(ctx)
	require.NoError(t, err)

	// Fetch the server's real key from its index.
	resp, err := http.Get(ts.URL + "/v1/index.json")
	require.NoError(t, err)
	var idx struct {
		ServerKey string `json:"server_key"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&idx))
	resp.Body.Close()

	// Pre-pinning the correct key works.
	realKey := mustHexKey(t, idx.ServerKey)
	pinned := newClient(t, ts.URL, client.WithServerKey(realKey))
	_, err = pinned.Sync(ctx)
	require.NoError(t, err)

	// Pre-pinning a different key: manifests fail verification.
	wrongPub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	misPinned := newClient(t, ts.URL, client.WithServerKey(wrongPub))
	_, err = misPinned.Sync(ctx)
	var keyChanged *client.KeyChangedError
	require.ErrorAs(t, err, &keyChanged)

	// A store that TOFU'd the real key refuses a conflicting
	// configured key outright.
	tofuDir := t.TempDir()
	tofu := newClientAt(t, ts.URL, tofuDir)
	_, err = tofu.Sync(ctx)
	require.NoError(t, err)

	conflicted := newClientAt(t, ts.URL, tofuDir, client.WithServerKey(wrongPub))
	_, err = conflicted.Sync(ctx)
	require.ErrorAs(t, err, &keyChanged)
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

func mustHexKey(t *testing.T, h string) ed25519.PublicKey {
	t.Helper()

	var key []byte
	_, err := fmt.Sscanf(h, "%x", &key)
	require.NoError(t, err)
	require.Len(t, key, ed25519.PublicKeySize)

	return ed25519.PublicKey(key)
}
