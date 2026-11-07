package workspace_test

import (
	"context"
	"encoding/hex"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/kalafut/dmrs"
	"github.com/kalafut/dmrs/tui/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startServer runs a real dmrs server over httptest; nothing is
// visible to Sync until a Cut publishes an epoch.
func startServer(t *testing.T) (*dmrs.Server, *httptest.Server) {
	t.Helper()
	s, err := dmrs.NewServer(t.TempDir())
	require.NoError(t, err)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts
}

// newSyncedWorkspace opens a workspace on its own data dir, pointed at
// the server, with the shared test key installed and the client ready.
func newSyncedWorkspace(t *testing.T, url string) *workspace.Workspace {
	t.Helper()
	ws, err := workspace.Open(workspace.Options{DataDir: t.TempDir(), ServerFlag: url})
	require.NoError(t, err)
	_, err = ws.ImportKey(testKeyHex(t))
	require.NoError(t, err)
	require.NoError(t, ws.InitClient())
	return ws
}

func testKeyHex(t *testing.T) string {
	t.Helper()
	return hex.EncodeToString(testKey)
}

func cut(t *testing.T, s *dmrs.Server) {
	t.Helper()
	_, err := s.Cut(context.Background())
	require.NoError(t, err)
}

func TestPushPullRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, ts := startServer(t)

	// Device A: fresh collection, two titles, push.
	a := newSyncedWorkspace(t, ts.URL)
	require.NoError(t, a.NewEmptyDraft())
	require.NoError(t, a.AddTitle("Alien", ptr(1979), nil))
	require.NoError(t, a.AddTitle("Brazil", ptr(1985), nil))
	require.NoError(t, a.AddReview(a.Titles()[0].Index, dmrs.Review2{Date: ptr("2026-08-11"), Rating: ptr(5)}))
	assert.True(t, a.Dirty())

	out, err := a.Push(ctx)
	require.NoError(t, err)
	require.Nil(t, out.Stale)
	assert.True(t, out.Result.Pushed)
	assert.Equal(t, 1, out.Result.Rev)
	assert.False(t, a.Dirty(), "push must leave the draft clean")

	// Pushing again with no changes uploads nothing.
	out, err = a.Push(ctx)
	require.NoError(t, err)
	assert.False(t, out.Result.Pushed)

	cut(t, s)

	// Device B: same key, empty data dir. Pull seeds the draft.
	b := newSyncedWorkspace(t, ts.URL)
	pr, err := b.Pull(ctx)
	require.NoError(t, err)
	require.NotNil(t, pr.Head)
	assert.True(t, pr.Seeded)
	assert.False(t, b.Dirty())
	require.Len(t, b.Titles(), 2)
	assert.Equal(t, "Alien", b.Titles()[0].Title.Name)
	require.Len(t, b.Titles()[0].Title.Reviews, 1)

	// B's draft pushes as "already up to date".
	out, err = b.Push(ctx)
	require.NoError(t, err)
	require.Nil(t, out.Stale)
	assert.False(t, out.Result.Pushed)
}

func TestConflictKeepMine(t *testing.T) {
	ctx := context.Background()
	s, ts := startServer(t)

	a := newSyncedWorkspace(t, ts.URL)
	require.NoError(t, a.NewEmptyDraft())
	require.NoError(t, a.AddTitle("Alien", ptr(1979), nil))
	_, err := a.Push(ctx)
	require.NoError(t, err)
	cut(t, s)

	b := newSyncedWorkspace(t, ts.URL)
	_, err = b.Pull(ctx)
	require.NoError(t, err)

	// A pushes rev 2 behind B's back.
	require.NoError(t, a.AddTitle("Brazil", ptr(1985), nil))
	_, err = a.Push(ctx)
	require.NoError(t, err)
	cut(t, s)

	// B edits on the stale base and pushes → conflict.
	require.NoError(t, b.AddTitle("Come and See", ptr(1985), nil))
	out, err := b.Push(ctx)
	require.NoError(t, err)
	require.NotNil(t, out.Stale)
	assert.Equal(t, 2, out.Stale.LatestRev)

	// Keep mine: the re-push asserts past the server rev.
	out, err = b.ResolveKeepMine(ctx)
	require.NoError(t, err)
	require.Nil(t, out.Stale)
	assert.True(t, out.Result.Pushed)
	assert.Greater(t, out.Result.Rev, 2)
	assert.False(t, b.Dirty())

	// The server now holds B's content (A's "Brazil" was stomped —
	// that's what keep-mine means).
	cut(t, s)
	prA, err := a.Pull(ctx)
	require.NoError(t, err)
	assert.True(t, prA.Seeded, "A was clean, so it fast-forwards onto B's push")
	names := []string{}
	for _, ref := range a.Titles() {
		names = append(names, ref.Title.Name)
	}
	assert.Equal(t, []string{"Alien", "Come and See"}, names)
}

func TestConflictTakeServer(t *testing.T) {
	ctx := context.Background()
	s, ts := startServer(t)

	a := newSyncedWorkspace(t, ts.URL)
	require.NoError(t, a.NewEmptyDraft())
	require.NoError(t, a.AddTitle("Alien", ptr(1979), nil))
	_, err := a.Push(ctx)
	require.NoError(t, err)
	cut(t, s)

	b := newSyncedWorkspace(t, ts.URL)
	_, err = b.Pull(ctx)
	require.NoError(t, err)

	require.NoError(t, a.AddTitle("Brazil", ptr(1985), nil))
	_, err = a.Push(ctx)
	require.NoError(t, err)
	cut(t, s)

	require.NoError(t, b.AddTitle("Come and See", ptr(1985), nil))
	out, err := b.Push(ctx)
	require.NoError(t, err)
	require.NotNil(t, out.Stale)

	// Take server: draft reseeds to A's content, local edit gone,
	// backup written.
	pr, err := b.ResolveTakeServer(ctx, out.Stale.LatestRev)
	require.NoError(t, err)
	assert.True(t, pr.Seeded)
	assert.False(t, b.Dirty())
	names := []string{}
	for _, ref := range b.Titles() {
		names = append(names, ref.Title.Name)
	}
	assert.Equal(t, []string{"Alien", "Brazil"}, names)

	backups, err := filepath.Glob(b.DraftPath() + ".bak-*")
	require.NoError(t, err)
	require.NotEmpty(t, backups)
}

func TestConflictTakeServerUncutRev(t *testing.T) {
	ctx := context.Background()
	s, ts := startServer(t)

	a := newSyncedWorkspace(t, ts.URL)
	require.NoError(t, a.NewEmptyDraft())
	require.NoError(t, a.AddTitle("Alien", ptr(1979), nil))
	_, err := a.Push(ctx)
	require.NoError(t, err)
	cut(t, s)

	b := newSyncedWorkspace(t, ts.URL)
	_, err = b.Pull(ctx)
	require.NoError(t, err)

	// A pushes rev 2 but the server has NOT cut it into an epoch.
	require.NoError(t, a.AddTitle("Brazil", ptr(1985), nil))
	_, err = a.Push(ctx)
	require.NoError(t, err)

	require.NoError(t, b.AddTitle("Come and See", ptr(1985), nil))
	out, err := b.Push(ctx)
	require.NoError(t, err)
	require.NotNil(t, out.Stale)

	// Take-server can't see rev 2 yet: the draft must be left alone.
	pr, err := b.ResolveTakeServer(ctx, out.Stale.LatestRev)
	require.NoError(t, err)
	assert.False(t, pr.Seeded)
	assert.True(t, b.Dirty())
	names := []string{}
	for _, ref := range b.Titles() {
		names = append(names, ref.Title.Name)
	}
	assert.Contains(t, names, "Come and See")
}

func TestPullWhileDirty(t *testing.T) {
	ctx := context.Background()
	s, ts := startServer(t)

	a := newSyncedWorkspace(t, ts.URL)
	require.NoError(t, a.NewEmptyDraft())
	require.NoError(t, a.AddTitle("Alien", ptr(1979), nil))
	_, err := a.Push(ctx)
	require.NoError(t, err)
	cut(t, s)

	b := newSyncedWorkspace(t, ts.URL)
	_, err = b.Pull(ctx)
	require.NoError(t, err)

	require.NoError(t, a.AddTitle("Brazil", ptr(1985), nil))
	_, err = a.Push(ctx)
	require.NoError(t, err)
	cut(t, s)

	// B has local edits; pull must surface the conflict and not touch
	// the draft.
	require.NoError(t, b.AddTitle("Come and See", ptr(1985), nil))
	pr, err := b.Pull(ctx)
	require.NoError(t, err)
	assert.True(t, pr.HeadAdvanced)
	assert.True(t, pr.DirtyConflict)
	assert.False(t, pr.Seeded)
	names := []string{}
	for _, ref := range b.Titles() {
		names = append(names, ref.Title.Name)
	}
	assert.Equal(t, []string{"Alien", "Come and See"}, names)
}

func TestPullNoDocumentYet(t *testing.T) {
	ctx := context.Background()
	_, ts := startServer(t)

	ws := newSyncedWorkspace(t, ts.URL)
	pr, err := ws.Pull(ctx)
	require.NoError(t, err)
	assert.Nil(t, pr.Head)
	assert.False(t, pr.HeadAdvanced)
	assert.False(t, ws.LastSync().IsZero())
}
