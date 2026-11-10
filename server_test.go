package revs

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openrevs/revs/keys"
	jsonlines "github.com/openrevs/sjsonl-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()

	s, err := NewServer(t.TempDir())
	require.NoError(t, err)
	s.snapshotEvery = 1 // exercise the snapshot path on every cut

	r := chi.NewRouter()
	s.routes(r)

	ts := httptest.NewServer(r)
	t.Cleanup(ts.Close)

	return s, ts
}

// signedDoc builds a signed ratingset for u with the given rev and titles.
func signedDoc(t *testing.T, u User, rev int, titles ...string) []byte {
	t.Helper()

	var buf bytes.Buffer
	fmt.Fprintf(&buf, `{"id":"%s","min_rating":0,"max_rating":5,"revision":%d}`+"\n", u.ID, rev)
	for _, title := range titles {
		fmt.Fprintf(&buf, `{"name":"%s"}`+"\n", title)
	}

	signed, _, err := jsonlines.Sign(buf.Bytes(), u.PrivateKey)
	require.NoError(t, err)

	return signed
}

// canonical returns the canonical encoding and doc hash the server stores.
func canonical(t *testing.T, doc []byte) ([]byte, string) {
	t.Helper()

	j, err := jsonlines.Parse(doc)
	require.NoError(t, err)

	return j.JSON(), j.Info().Hash
}

func upload(t *testing.T, ts *httptest.Server, doc []byte) *http.Response {
	t.Helper()

	resp, err := http.Post(ts.URL+"/upload", "application/jsonl", bytes.NewReader(doc))
	require.NoError(t, err)
	t.Cleanup(func() { resp.Body.Close() })

	return resp
}

func fetch(t *testing.T, ts *httptest.Server, path string) []byte {
	t.Helper()

	resp, err := http.Get(ts.URL + path)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "GET %s", path)

	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return data
}

// parseManifest verifies a manifest's signature and splits it into its
// metadata line and per-user entries (manifest lines are unordered).
func parseManifest(t *testing.T, manifest []byte, serverKey []byte) (gjson.Result, map[string]gjson.Result, string) {
	t.Helper()

	info, err := jsonlines.Verify(manifest)
	require.NoError(t, err)
	assert.Equal(t, serverKey, []byte(info.SignerKey))

	var meta gjson.Result
	entries := map[string]gjson.Result{}
	for _, line := range strings.Split(strings.TrimSpace(string(manifest)), "\n")[1:] {
		parsed := gjson.Parse(line)
		if parsed.Get("epoch").Exists() {
			meta = parsed
			continue
		}
		entries[parsed.Get("id").String()] = parsed
	}
	require.True(t, meta.Exists(), "manifest metadata line not found")

	return meta, entries, info.Hash
}

func gunzip(t *testing.T, data []byte) []byte {
	t.Helper()

	gr, err := gzip.NewReader(bytes.NewReader(data))
	require.NoError(t, err)
	out, err := io.ReadAll(gr)
	require.NoError(t, err)

	return out
}

func TestUploadVerdicts(t *testing.T) {
	_, ts := newTestServer(t)
	u := NewUser()

	// Garbage is rejected outright.
	resp := upload(t, ts, []byte("not a ratingset"))
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// A rev below 1 is invalid.
	resp = upload(t, ts, signedDoc(t, u, 0, "Alien"))
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// First upload is accepted.
	doc := signedDoc(t, u, 1, "Alien")
	resp = upload(t, ts, doc)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Re-asserting the same rev is stale.
	resp = upload(t, ts, signedDoc(t, u, 1, "Aliens"))
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	var verdict uploadResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&verdict))
	assert.Equal(t, "stale", verdict.Status)

	// A higher rev is accepted.
	resp = upload(t, ts, signedDoc(t, u, 2, "Aliens"))
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestPublishingRoundTrip(t *testing.T) {
	s, ts := newTestServer(t)
	ctx := context.Background()

	var index serverIndex
	require.NoError(t, json.Unmarshal(fetch(t, ts, "/v1/index.json"), &index))
	serverPub, err := keys.ParsePublic(index.ServerKey)
	require.NoError(t, err)

	u := NewUser()

	// Give the base doc enough lines that a later diff is smaller than the
	// full document.
	// Large enough that a one-title patch (whose header carries full
	// base/target hashes) is smaller than the full document.
	titles := []string{
		"Alien", "Blade Runner", "Casablanca", "Dune", "Eraserhead",
		"Gattaca", "Heat", "Inception", "Jaws", "Klute",
		"Laura", "Memento", "Network", "Oldboy", "Psycho",
	}
	doc1 := signedDoc(t, u, 1, titles...)
	doc1Canonical, doc1Hash := canonical(t, doc1)

	resp := upload(t, ts, doc1)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	epoch1, err := s.Cut(ctx)
	require.NoError(t, err)
	require.Greater(t, epoch1, 0)

	// A cut with nothing new does nothing.
	nothing, err := s.Cut(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, nothing)

	// The patch manifest verifies against the server key and lists our
	// user; the first appearance ships the full doc.
	manifest1 := fetch(t, ts, fmt.Sprintf("/v1/epochs/%d/manifest", epoch1))
	meta1, entries1, manifest1Hash := parseManifest(t, manifest1, serverPub)
	assert.Equal(t, int64(epoch1), meta1.Get("epoch").Int())
	assert.Equal(t, "patch", meta1.Get("kind").String())
	entry := entries1[u.ID]
	require.True(t, entry.Exists())
	assert.Equal(t, int64(1), entry.Get("rev").Int())
	assert.Equal(t, doc1Hash, entry.Get("hash").String())

	stream := gunzip(t, fetch(t, ts, fmt.Sprintf("/v1/epochs/%d/patch", epoch1)))
	off, size := int(entry.Get("offset").Int()), int(entry.Get("size").Int())
	require.LessOrEqual(t, off+size, len(stream))
	extracted := stream[off : off+size]
	assert.Equal(t, doc1Canonical, extracted)

	// Second upload: same titles plus one, higher rev. The published entry
	// should now be an sjsonl patch that applies onto doc1.
	doc2 := signedDoc(t, u, 2, append(titles, "Fargo")...)
	doc2Canonical, doc2Hash := canonical(t, doc2)

	resp = upload(t, ts, doc2)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	epoch2, err := s.Cut(ctx)
	require.NoError(t, err)
	assert.Equal(t, epoch1+1, epoch2)

	manifest2 := fetch(t, ts, fmt.Sprintf("/v1/epochs/%d/manifest", epoch2))
	meta2, entries2, _ := parseManifest(t, manifest2, serverPub)
	assert.Equal(t, manifest1Hash, meta2.Get("prev").String(), "manifests must chain")

	entry2 := entries2[u.ID]
	require.True(t, entry2.Exists())
	assert.Equal(t, doc2Hash, entry2.Get("hash").String())

	stream2 := gunzip(t, fetch(t, ts, fmt.Sprintf("/v1/epochs/%d/patch", epoch2)))
	off2, size2 := int(entry2.Get("offset").Int()), int(entry2.Get("size").Int())
	patchEntry := stream2[off2 : off2+size2]
	assert.Equal(t, "patch", gjson.GetBytes(patchEntry, "sjsonl").String(), "expected a patch entry, got a full doc")
	assert.Equal(t, doc1Hash, gjson.GetBytes(patchEntry, "base").String())

	patched, patchedInfo, err := jsonlines.Patch(doc1Canonical, patchEntry)
	require.NoError(t, err)
	assert.Equal(t, doc2Canonical, patched)
	assert.Equal(t, doc2Hash, patchedInfo.Hash)

	// Snapshot (cadence = every epoch in tests): individually gzipped
	// members packed into chunk files, addressed by global manifest
	// offsets. At the default chunk target this corpus is one chunk.
	snapManifest := fetch(t, ts, fmt.Sprintf("/v1/snapshots/%d/manifest", epoch2))
	snapMeta, snapEntries, _ := parseManifest(t, snapManifest, serverPub)
	assert.Equal(t, "snapshot", snapMeta.Get("kind").String())
	assert.Equal(t, int64(epoch2), snapMeta.Get("epoch").Int())

	chunks := snapMeta.Get("chunks").Array()
	require.Len(t, chunks, 1)

	snapshot := fetch(t, ts, fmt.Sprintf("/v1/snapshots/%d/snapshot.0", epoch2))
	require.Equal(t, int(chunks[0].Get("size").Int()), len(snapshot))
	sum := sha256.Sum256(snapshot)
	assert.Equal(t, chunks[0].Get("hash").String(), hex.EncodeToString(sum[:]))

	snapEntry := snapEntries[u.ID]
	require.True(t, snapEntry.Exists())
	soff, ssize := int(snapEntry.Get("offset").Int()), int(snapEntry.Get("size").Int())
	require.LessOrEqual(t, soff+ssize, len(snapshot))
	member := gunzip(t, snapshot[soff:soff+ssize])
	assert.Equal(t, doc2Canonical, member)

	// Each chunk file is itself a valid gzip stream.
	whole := gunzip(t, snapshot)
	assert.Contains(t, string(whole), "Fargo")

	// The index advertises the state of the world.
	var idx serverIndex
	require.NoError(t, json.Unmarshal(fetch(t, ts, "/v1/index.json"), &idx))
	assert.Equal(t, epoch2, idx.CurrentEpoch)
	assert.Contains(t, idx.SnapshotEpochs, epoch1)
	assert.Contains(t, idx.SnapshotEpochs, epoch2)
	assert.Equal(t, index.ServerKey, idx.ServerKey)
}

// TestSnapshotChunking forces a tiny chunk target so every member gets
// a chunk file to itself, and checks that the chunk table, per-chunk
// hashes, and global offsets agree with the stored files.
func TestSnapshotChunking(t *testing.T) {
	s, ts := newTestServer(t)
	s.snapshotChunkSize = 1 // every member overflows the target
	ctx := context.Background()

	users := []User{NewUser(), NewUser(), NewUser()}
	for i, u := range users {
		resp := upload(t, ts, signedDoc(t, u, 1, fmt.Sprintf("Movie %d", i)))
		require.Equal(t, http.StatusOK, resp.StatusCode)
	}

	epoch, err := s.Cut(ctx)
	require.NoError(t, err)

	var idx serverIndex
	require.NoError(t, json.Unmarshal(fetch(t, ts, "/v1/index.json"), &idx))
	serverPub, err := keys.ParsePublic(idx.ServerKey)
	require.NoError(t, err)

	manifest := fetch(t, ts, fmt.Sprintf("/v1/snapshots/%d/manifest", epoch))
	meta, entries, _ := parseManifest(t, manifest, serverPub)

	chunks := meta.Get("chunks").Array()
	require.Len(t, chunks, len(users))
	require.Len(t, entries, len(users))

	// Reassemble the logical stream chunk by chunk, verifying each file
	// against its table row.
	var stream []byte
	for i, c := range chunks {
		data := fetch(t, ts, fmt.Sprintf("/v1/snapshots/%d/snapshot.%d", epoch, i))
		require.Equal(t, int(c.Get("size").Int()), len(data))
		sum := sha256.Sum256(data)
		require.Equal(t, c.Get("hash").String(), hex.EncodeToString(sum[:]))
		stream = append(stream, data...)
	}

	// Global offsets slice members out of the reassembled stream, and
	// each member decompresses to its user's document.
	for _, u := range users {
		e := entries[u.ID]
		require.True(t, e.Exists())
		off, size := int(e.Get("offset").Int()), int(e.Get("size").Int())
		require.LessOrEqual(t, off+size, len(stream))
		member := gunzip(t, stream[off:off+size])
		assert.Contains(t, string(member), u.ID)
	}
}

func TestPublishOnUpload(t *testing.T) {
	s, ts := newTestServer(t)
	u := NewUser()

	epoch := func() int {
		var index serverIndex
		require.NoError(t, json.Unmarshal(fetch(t, ts, "/v1/index.json"), &index))
		return index.CurrentEpoch
	}

	// By default an accepted upload waits for the next cut.
	resp := upload(t, ts, signedDoc(t, u, 1, "Alien"))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 0, epoch())

	// With publish-on-upload, it is published before the response.
	s.SetPublishOnUpload(true)
	resp = upload(t, ts, signedDoc(t, u, 2, "Aliens"))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 1, epoch())

	resp = upload(t, ts, signedDoc(t, u, 3, "Alien 3"))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 2, epoch())

	// A rejected upload publishes nothing.
	resp = upload(t, ts, signedDoc(t, u, 3, "Alien Resurrection"))
	require.Equal(t, http.StatusConflict, resp.StatusCode)
	assert.Equal(t, 2, epoch())
}

func decodeVerdict(t *testing.T, resp *http.Response) uploadResponse {
	t.Helper()

	var verdict uploadResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&verdict))
	return verdict
}

func diff(t *testing.T, base, target []byte) []byte {
	t.Helper()

	patch, err := jsonlines.Diff(base, target)
	require.NoError(t, err)
	return patch
}

func TestPatchUpload(t *testing.T) {
	s, ts := newTestServer(t)
	ctx := context.Background()
	u := NewUser()
	other := NewUser()

	rev1 := signedDoc(t, u, 1, "Alien")
	rev2 := signedDoc(t, u, 2, "Alien", "Heat")
	rev3 := signedDoc(t, u, 3, "Alien", "Heat", "Ran")

	// A patch with no accepted base is a base mismatch, not a stale rev.
	resp := upload(t, ts, diff(t, rev1, rev2))
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	assert.Equal(t, "base_mismatch", decodeVerdict(t, resp).Status)

	require.Equal(t, http.StatusOK, upload(t, ts, rev1).StatusCode)

	// A patch against the latest document is applied and stored as the
	// full signed target.
	resp = upload(t, ts, diff(t, rev1, rev2))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	verdict := decodeVerdict(t, resp)
	assert.Equal(t, 2, verdict.Rev)

	want, wantHash := canonical(t, rev2)
	assert.Equal(t, wantHash, verdict.Hash)
	stored, err := s.docs.Get(wantHash)
	require.NoError(t, err)
	assert.Equal(t, want, stored)

	latest, err := s.dal.LatestDocument(ctx, u.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, latest.Rev)

	// A patch against an older document is a base mismatch, reporting
	// the latest rev.
	resp = upload(t, ts, diff(t, rev1, rev3))
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	verdict = decodeVerdict(t, resp)
	assert.Equal(t, "base_mismatch", verdict.Status)
	assert.Equal(t, 2, verdict.Rev)
	assert.Equal(t, wantHash, verdict.Hash)

	// The right base but a non-advancing rev is still stale.
	resp = upload(t, ts, diff(t, rev2, signedDoc(t, u, 2, "Alien", "Heat", "Ran")))
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	verdict = decodeVerdict(t, resp)
	assert.Equal(t, "stale", verdict.Status)
	assert.Equal(t, wantHash, verdict.Hash)

	// Unsigned patches are rejected.
	unsigned, err := jsonlines.DiffUnsigned(rev2, rev3)
	require.NoError(t, err)
	resp = upload(t, ts, unsigned)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// Another user can't patch this user's document: the patch's key
	// selects the other user, whose latest document isn't the base.
	resp = upload(t, ts, diff(t, rev2, signedDoc(t, other, 3, "Alien")))
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	assert.Equal(t, "base_mismatch", decodeVerdict(t, resp).Status)

	resp = upload(t, ts, diff(t, rev2, rev3))
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestUploadSizeLimits(t *testing.T) {
	_, ts := newTestServer(t)
	u := NewUser()

	// titles returns n distinct title lines of about 64 bytes each,
	// starting at index from.
	titles := func(from, n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("A Movie With a Fairly Long Name For Padding, Number %08d", from+i)
		}
		return out
	}

	// A full document over the limit is rejected outright.
	resp := upload(t, ts, signedDoc(t, u, 1, titles(0, maxDocumentSize/64+100)...))
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// A full document between the old 256 KiB body cap and the document
	// limit is fine without a patch.
	prev := signedDoc(t, u, 1, titles(0, 6000)...)
	require.Greater(t, len(prev), 256<<10)
	require.Equal(t, http.StatusOK, upload(t, ts, prev).StatusCode)

	// Grow the document with small patches until the result would exceed
	// the document limit: the limit applies to the patched result.
	const step = 2000
	for rev := 2; ; rev++ {
		next := signedDoc(t, u, rev, titles(0, 6000+(rev-1)*step)...)
		patch := diff(t, prev, next)
		require.Less(t, len(patch), maxDocumentSize)

		resp := upload(t, ts, patch)
		if len(next) <= maxDocumentSize {
			require.Equal(t, http.StatusOK, resp.StatusCode, "rev %d (%d bytes)", rev, len(next))
			prev = next
			continue
		}

		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		assert.Contains(t, decodeVerdict(t, resp).Reason, "byte limit")
		break
	}
}
