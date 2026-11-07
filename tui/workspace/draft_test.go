package workspace_test

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kalafut/dmrs"
	"github.com/kalafut/dmrs/client"
	"github.com/kalafut/dmrs/tui/workspace"
	jsonlines "github.com/kalafut/sjsonl"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testKey = ed25519.NewKeyFromSeed([]byte("0123456789abcdef0123456789abcdef"))

func newWorkspace(t *testing.T) (*workspace.Workspace, string) {
	t.Helper()
	dir := t.TempDir()
	ws, err := workspace.Open(workspace.Options{DataDir: dir})
	require.NoError(t, err)
	_, err = ws.ImportKey(hex.EncodeToString(testKey))
	require.NoError(t, err)
	return ws, dir
}

func writeDraft(t *testing.T, dir string, lines ...string) {
	t.Helper()
	content := strings.Join(lines, "\n") + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "draft.jsonl"), []byte(content), 0o644))
}

func userLine(rev int) string {
	uid := client.UserID(testKey.Public().(ed25519.PublicKey))
	if rev == 0 {
		return fmt.Sprintf(`{"id":%q,"max_rating":5,"min_rating":0}`, uid)
	}
	return fmt.Sprintf(`{"id":%q,"max_rating":5,"min_rating":0,"revision":%d}`, uid, rev)
}

// makeHead builds a signed document the way the server would hold it.
func makeHead(t *testing.T, rev int, titleLines ...string) *client.Document {
	t.Helper()
	content := strings.Join(append([]string{userLine(rev)}, titleLines...), "\n") + "\n"
	signed, info, err := jsonlines.Sign([]byte(content), testKey)
	require.NoError(t, err)
	return &client.Document{
		UserID: client.UserID(testKey.Public().(ed25519.PublicKey)),
		Rev:    rev,
		Hash:   info.Hash,
		Data:   signed,
	}
}

func draftBytes(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "draft.jsonl"))
	require.NoError(t, err)
	return string(data)
}

func ptr[T any](v T) *T { return &v }

func TestRoundTripFidelity(t *testing.T) {
	ws, dir := newWorkspace(t)

	edited := `{"name":"Alien","refs":{"imdb":"tt0078748"},"secret":42,"year":1979}`
	untouched := `{"name":"Brazil","futureflag":true,"year":1985}`
	writeDraft(t, dir, userLine(0), edited, untouched)

	_, err := ws.LoadDraft()
	require.NoError(t, err)

	var idx int = -1
	for _, ref := range ws.Titles() {
		if ref.Title.Name == "Alien" {
			idx = ref.Index
		}
	}
	require.NotEqual(t, -1, idx)

	require.NoError(t, ws.UpdateTitle(idx, "Alien (1979)", ptr(1979), nil))

	got := draftBytes(t, dir)
	// Unknown field and refs survive on the edited line.
	assert.Contains(t, got, `"secret":42`)
	assert.Contains(t, got, `"refs":{"imdb":"tt0078748"}`)
	assert.Contains(t, got, `"name":"Alien (1979)"`)
	// The untouched line is byte-identical.
	assert.Contains(t, got, untouched+"\n")
	// The user line is byte-identical.
	assert.Contains(t, got, userLine(0)+"\n")
}

func TestReviewCRUD(t *testing.T) {
	ws, dir := newWorkspace(t)
	writeDraft(t, dir, userLine(0), `{"name":"Alien","year":1979}`)
	_, err := ws.LoadDraft()
	require.NoError(t, err)
	idx := ws.Titles()[0].Index

	// Add to a title with no reviews array.
	require.NoError(t, ws.AddReview(idx, dmrs.Review2{Date: ptr("2026-08-11"), Rating: ptr(5)}))
	title, err := ws.Title(idx)
	require.NoError(t, err)
	require.Len(t, title.Reviews, 1)
	assert.Equal(t, 5, *title.Reviews[0].Rating)

	// A hand-added unknown field inside the review survives a rating edit.
	writeDraft(t, dir, userLine(0),
		`{"name":"Alien","reviews":[{"date":"2026-08-11","mood":"tense","rating":5}],"year":1979}`)
	_, err = ws.LoadDraft()
	require.NoError(t, err)
	idx = ws.Titles()[0].Index
	require.NoError(t, ws.UpdateReview(idx, 0, dmrs.Review2{Date: ptr("2026-08-11"), Rating: ptr(4)}))
	got := draftBytes(t, dir)
	assert.Contains(t, got, `"mood":"tense"`)
	assert.Contains(t, got, `"rating":4`)
	// comment was nil → not added
	assert.NotContains(t, got, `"comment"`)

	// Second review, then delete the first.
	require.NoError(t, ws.AddReview(idx, dmrs.Review2{Date: ptr("2026-08-12"), Rating: ptr(3), Comment: ptr("rewatch")}))
	title, _ = ws.Title(idx)
	require.Len(t, title.Reviews, 2)
	require.NoError(t, ws.DeleteReview(idx, 0))
	title, _ = ws.Title(idx)
	require.Len(t, title.Reviews, 1)
	assert.Equal(t, "rewatch", *title.Reviews[0].Comment)

	// Deleting the last review drops the reviews key entirely.
	require.NoError(t, ws.DeleteReview(idx, 0))
	got = draftBytes(t, dir)
	assert.NotContains(t, got, `"reviews"`)
}

func TestDirtyIsHashBased(t *testing.T) {
	ws, dir := newWorkspace(t)

	// Fresh draft, never pushed: always dirty.
	require.NoError(t, ws.NewEmptyDraft())
	assert.True(t, ws.Dirty())

	// Seeded verbatim from a head: clean.
	head := makeHead(t, 3, `{"name":"Alien","year":1979}`)
	require.NoError(t, ws.SeedFromHead(head))
	assert.False(t, ws.Dirty())
	assert.Equal(t, 3, ws.BaseRev())

	// Edit → dirty.
	idx := ws.Titles()[0].Index
	require.NoError(t, ws.UpdateTitle(idx, "Alien!", ptr(1979), nil))
	assert.True(t, ws.Dirty())

	// Revert the edit → clean again (proves hash-based, not a flag).
	require.NoError(t, ws.UpdateTitle(idx, "Alien", ptr(1979), nil))
	assert.False(t, ws.Dirty())

	// Reload from disk agrees.
	ws2, err := workspace.Open(workspace.Options{DataDir: dir})
	require.NoError(t, err)
	_, err = ws2.LoadDraft()
	require.NoError(t, err)
	assert.False(t, ws2.Dirty())
}

func TestSeedBacksUpExistingDraft(t *testing.T) {
	ws, dir := newWorkspace(t)
	require.NoError(t, ws.NewEmptyDraft())
	require.NoError(t, ws.AddTitle("Local Only", nil, nil))

	require.NoError(t, ws.SeedFromHead(makeHead(t, 1, `{"name":"Alien","year":1979}`)))

	backups, err := filepath.Glob(filepath.Join(dir, "draft.jsonl.bak-*"))
	require.NoError(t, err)
	require.Len(t, backups, 1)
	data, err := os.ReadFile(backups[0])
	require.NoError(t, err)
	assert.Contains(t, string(data), "Local Only")
	assert.NotContains(t, draftBytes(t, dir), "Local Only")
}

func TestLoadDraftHardErrors(t *testing.T) {
	uid := userLine(0)
	cases := []struct {
		name  string
		lines []string
		want  string
	}{
		{"invalid json", []string{uid, `{"name": "Broken`}, "invalid JSON"},
		{"no user line", []string{`{"name":"Alien"}`}, "no user line"},
		{"two user lines", []string{uid, `{"id":"deadbeef"}`}, "second user line"},
		{"duplicate lines", []string{uid, `{"name":"Alien"}`, `{"name":"Alien"}`}, "duplicate"},
		{"title missing name", []string{uid, `{"year":1979}`}, "missing name"},
		{"control line", []string{`{"sjsonl":"doc","version":"0.1"}`, uid}, "control line"},
		{"id mismatch", []string{`{"id":"00000000000000000000000000000000"}`}, "does not match"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws, dir := newWorkspace(t)
			writeDraft(t, dir, tc.lines...)
			_, err := ws.LoadDraft()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestLoadDraftWarnings(t *testing.T) {
	ws, dir := newWorkspace(t)
	writeDraft(t, dir, userLine(0),
		`{"name":"Alien","reviews":[{"date":"not-a-date","rating":11}],"year":1979}`)

	report, err := ws.LoadDraft()
	require.NoError(t, err)

	joined := strings.Join(report.Warnings, "; ")
	assert.Contains(t, joined, "rating 11 outside 0..5")
	assert.Contains(t, joined, `"not-a-date"`)
	assert.Contains(t, joined, "sync state missing")
}

func TestDuplicateTitleRejected(t *testing.T) {
	ws, _ := newWorkspace(t)
	require.NoError(t, ws.NewEmptyDraft())
	require.NoError(t, ws.AddTitle("Alien", ptr(1979), nil))
	err := ws.AddTitle("Alien", ptr(1979), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "identical entry")

	// Same name, different year is fine.
	require.NoError(t, ws.AddTitle("Alien", ptr(2079), nil))
}

func TestDeleteTitle(t *testing.T) {
	ws, dir := newWorkspace(t)
	require.NoError(t, ws.NewEmptyDraft())
	require.NoError(t, ws.AddTitle("Alien", ptr(1979), nil))
	require.NoError(t, ws.AddTitle("Brazil", ptr(1985), nil))

	for _, ref := range ws.Titles() {
		if ref.Title.Name == "Alien" {
			require.NoError(t, ws.DeleteTitle(ref.Index))
		}
	}
	require.Len(t, ws.Titles(), 1)
	assert.Equal(t, "Brazil", ws.Titles()[0].Title.Name)
	assert.NotContains(t, draftBytes(t, dir), "Alien")
}

func TestTitlesSorted(t *testing.T) {
	ws, _ := newWorkspace(t)
	require.NoError(t, ws.NewEmptyDraft())
	require.NoError(t, ws.AddTitle("zodiac", ptr(2007), nil))
	require.NoError(t, ws.AddTitle("Alien", ptr(1979), nil))
	require.NoError(t, ws.AddTitle("Brazil", ptr(1985), nil))

	names := []string{}
	for _, ref := range ws.Titles() {
		names = append(names, ref.Title.Name)
	}
	assert.Equal(t, []string{"Alien", "Brazil", "zodiac"}, names)
}

func TestKeyLifecycle(t *testing.T) {
	dir := t.TempDir()
	ws, err := workspace.Open(workspace.Options{DataDir: dir})
	require.NoError(t, err)
	assert.True(t, ws.NeedsKey())

	uid, err := ws.GenerateKey()
	require.NoError(t, err)
	assert.Len(t, uid, 32)

	info, err := os.Stat(filepath.Join(dir, "user.key"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	// Reopen: same identity.
	ws2, err := workspace.Open(workspace.Options{DataDir: dir})
	require.NoError(t, err)
	assert.Equal(t, uid, ws2.UserID())

	// Second key is refused.
	_, err = ws2.GenerateKey()
	require.Error(t, err)

	// Import rejects garbage.
	ws3, err := workspace.Open(workspace.Options{DataDir: t.TempDir()})
	require.NoError(t, err)
	_, err = ws3.ImportKey("not-hex")
	require.Error(t, err)
	_, err = ws3.ImportKey("abcd")
	require.Error(t, err)
}

func TestConfigLifecycle(t *testing.T) {
	dir := t.TempDir()
	ws, err := workspace.Open(workspace.Options{DataDir: dir, ServerFlag: "http://localhost:9999"})
	require.NoError(t, err)
	assert.False(t, ws.NeedsServer())

	// Flag was persisted: reopening without it keeps the server.
	ws2, err := workspace.Open(workspace.Options{DataDir: dir})
	require.NoError(t, err)
	assert.Equal(t, "http://localhost:9999", ws2.ServerURL())

	require.Error(t, ws2.SetServer("not a url"))
	require.Error(t, ws2.SetServer("ftp://example.com"))
	require.NoError(t, ws2.SetServer("https://example.com"))
}
