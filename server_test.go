package revs

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openrevs/revs/domain"
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

type tarEntry struct {
	name string
	data []byte
}

// readPart fetches one archive part and checks the archive format:
// gzipped ustar holding only regular files with plain names.
func readPart(t *testing.T, ts *httptest.Server, path string) []tarEntry {
	t.Helper()

	gr, err := gzip.NewReader(bytes.NewReader(fetch(t, ts, path)))
	require.NoError(t, err)
	tr := tar.NewReader(gr)

	var entries []tarEntry
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		require.Equal(t, tar.FormatUSTAR, hdr.Format, hdr.Name)
		require.Equal(t, byte(tar.TypeReg), hdr.Typeflag, hdr.Name)
		require.NotContains(t, hdr.Name, "/")
		data, err := io.ReadAll(tr)
		require.NoError(t, err)
		entries = append(entries, tarEntry{hdr.Name, data})
	}

	return entries
}

// readArtifact reads every part of an artifact into a name -> data map.
func readArtifact(t *testing.T, ts *httptest.Server, channel, base string, parts int) map[string][]byte {
	t.Helper()

	all := map[string][]byte{}
	for i := 1; i <= parts; i++ {
		for _, e := range readPart(t, ts, fmt.Sprintf("/v1/%s/%s_%d.tar.gz", channel, base, i)) {
			_, dup := all[e.name]
			require.False(t, dup, "duplicate entry %s", e.name)
			all[e.name] = e.data
		}
	}
	return all
}

// testManifest is a parsed manifest: its metadata line, entries by user
// id, and removals.
type testManifest struct {
	meta    map[string]any
	entries map[string]manifestEntry
	removed []string
}

// readManifest fetches a manifest and checks that it is a gzipped sjsonl
// document signed with the server key.
func readManifest(t *testing.T, s *Server, ts *httptest.Server, path string) testManifest {
	t.Helper()

	gr, err := gzip.NewReader(bytes.NewReader(fetch(t, ts, path)))
	require.NoError(t, err)
	data, err := io.ReadAll(gr)
	require.NoError(t, err)

	doc, err := jsonlines.Parse(data)
	require.NoError(t, err)
	require.Equal(t, keys.EncodePublic(s.key.Public().(ed25519.PublicKey)), keys.EncodePublic(doc.Info().SignerKey))

	m := testManifest{entries: map[string]manifestEntry{}}
	for _, line := range bytes.Split(bytes.TrimSpace(doc.JSON()), []byte("\n"))[1:] {
		switch {
		case gjson.GetBytes(line, "manifest").Exists():
			require.Nil(t, m.meta, "multiple metadata lines")
			require.NoError(t, json.Unmarshal(line, &m.meta))
		case gjson.GetBytes(line, "id").Exists():
			var e manifestEntry
			require.NoError(t, json.Unmarshal(line, &e))
			m.entries[e.ID] = e
		case gjson.GetBytes(line, "removed").Exists():
			m.removed = append(m.removed, gjson.GetBytes(line, "removed").String())
		default:
			t.Fatalf("unexpected manifest line %s", line)
		}
	}
	require.NotNil(t, m.meta)
	slices.Sort(m.removed)

	return m
}

func readIndex(t *testing.T, ts *httptest.Server) publicationIndex {
	t.Helper()

	var idx publicationIndex
	require.NoError(t, json.Unmarshal(fetch(t, ts, "/v1/index.json"), &idx))
	return idx
}

func patchRanges(ci *channelIndex) [][2]int {
	var rs [][2]int
	for _, p := range ci.Patches {
		rs = append(rs, p.Range)
	}
	return rs
}

func titles(n int, prefix string) []string {
	ts := make([]string, n)
	for i := range ts {
		ts[i] = fmt.Sprintf("%s %d", prefix, i)
	}
	return ts
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

	// Before any cut, every channel is listed, empty.
	idx := readIndex(t, ts)
	require.Contains(t, idx.Channels, ChannelMain)
	require.Contains(t, idx.Channels, ChannelDirect)
	assert.Equal(t, 0, idx.Channels[ChannelMain].Epoch)
	assert.Empty(t, idx.Channels[ChannelMain].Patches)
	assert.Empty(t, idx.Channels[ChannelMain].Snapshots)

	// The index advertises the server's key and the id derived from it.
	pub := s.key.Public().(ed25519.PublicKey)
	assert.Equal(t, keys.EncodePublic(pub), idx.ServerKey)
	assert.Equal(t, keys.UserID(pub), idx.ServerID)

	u1, u2 := NewUser(), NewUser()
	many := titles(40, "Movie")
	doc1 := signedDoc(t, u1, 1, many...)
	doc2 := signedDoc(t, u2, 1, "Alien")
	require.Equal(t, http.StatusOK, upload(t, ts, doc1).StatusCode)
	require.Equal(t, http.StatusOK, upload(t, ts, doc2).StatusCode)

	cut, err := s.Cut(ctx)
	require.NoError(t, err)
	assert.Equal(t, map[string]int{ChannelMain: 1, ChannelDirect: 1}, cut)

	// A tick with nothing new does nothing.
	cut, err = s.Cut(ctx)
	require.NoError(t, err)
	assert.Empty(t, cut)

	idx = readIndex(t, ts)
	main := idx.Channels[ChannelMain]
	assert.Equal(t, 1, main.Epoch)
	assert.Equal(t, [][2]int{{0, 1}}, patchRanges(main))
	require.Len(t, main.Snapshots, 1)
	assert.Equal(t, 1, main.Snapshots[0].Epoch)

	// The first patch carries full documents for users new to the
	// channel, exactly as the server stores them.
	canon1, _ := canonical(t, doc1)
	canon2, _ := canonical(t, doc2)
	patch := readArtifact(t, ts, ChannelMain, "patch-0-1", 1)
	assert.Equal(t, map[string][]byte{
		u1.ID + "-1.sjsonl": canon1,
		u2.ID + "-1.sjsonl": canon2,
	}, patch)

	// Its signed manifest names the publication and lists each entry's
	// resulting document and part.
	_, hash1 := canonical(t, doc1)
	_, hash2 := canonical(t, doc2)
	m := readManifest(t, s, ts, "/v1/main/patch-manifest-0-1.sjsonl.gz")
	assert.Equal(t, "patch", m.meta["manifest"])
	assert.Equal(t, idx.ServerID, m.meta["server_id"])
	assert.Equal(t, ChannelMain, m.meta["channel"])
	assert.Equal(t, []any{0.0, 1.0}, m.meta["range"])
	assert.NotContains(t, m.meta, "epoch")
	assert.Equal(t, 1.0, m.meta["format"])
	_, err = time.Parse(time.RFC3339, m.meta["created"].(string))
	require.NoError(t, err)
	assert.Equal(t, map[string]manifestEntry{
		u1.ID: {ID: u1.ID, Rev: 1, Hash: hash1, Part: 1},
		u2.ID: {ID: u2.ID, Rev: 1, Hash: hash2, Part: 1},
	}, m.entries)
	assert.Empty(t, m.removed)

	m = readManifest(t, s, ts, "/v1/direct/snapshot-manifest-1.sjsonl.gz")
	assert.Equal(t, "snapshot", m.meta["manifest"])
	assert.Equal(t, ChannelDirect, m.meta["channel"])
	assert.Equal(t, 1.0, m.meta["epoch"])
	assert.NotContains(t, m.meta, "range")
	assert.Len(t, m.entries, 2)

	// A small change to a big document ships as an sjsonl patch from the
	// rev the channel held to the new one.
	many[7] = "Changed"
	doc1b := signedDoc(t, u1, 2, many...)
	require.Equal(t, http.StatusOK, upload(t, ts, doc1b).StatusCode)
	cut, err = s.Cut(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, cut[ChannelMain])

	patch = readArtifact(t, ts, ChannelMain, "patch-1-2", 1)
	require.Len(t, patch, 1)
	entry := patch[u1.ID+"-1-2.sjsonl"]
	require.NotNil(t, entry)
	assert.Equal(t, "patch", gjson.GetBytes(bytes.SplitN(entry, []byte("\n"), 2)[0], "sjsonl").String())
	applied, _, err := jsonlines.Patch(canon1, entry)
	require.NoError(t, err)
	canon1b, hash1b := canonical(t, doc1b)
	assert.Equal(t, canon1b, applied)

	// A patch entry's manifest line names its base.
	m = readManifest(t, s, ts, "/v1/main/patch-manifest-1-2.sjsonl.gz")
	assert.Equal(t, map[string]manifestEntry{
		u1.ID: {ID: u1.ID, Rev: 2, Hash: hash1b, BaseRev: 1, BaseHash: hash1, Part: 1},
	}, m.entries)

	// The snapshot at epoch 2 holds every user's full document.
	snap := readArtifact(t, ts, ChannelMain, "snapshot-2", 1)
	assert.Equal(t, map[string][]byte{
		u1.ID + "-2.sjsonl": canon1b,
		u2.ID + "-1.sjsonl": canon2,
	}, snap)

	// Index timestamps are RFC 3339.
	idx = readIndex(t, ts)
	_, err = time.Parse(time.RFC3339, idx.Created)
	require.NoError(t, err)
	_, err = time.Parse(time.RFC3339, idx.Channels[ChannelMain].Patches[0].Created)
	require.NoError(t, err)
}

func TestMultiPartArchives(t *testing.T) {
	s, ts := newTestServer(t)
	s.partSize = 1 // every entry overflows the target: one entry per part
	ctx := context.Background()

	users := []User{NewUser(), NewUser(), NewUser()}
	for i, u := range users {
		require.Equal(t, http.StatusOK, upload(t, ts, signedDoc(t, u, 1, fmt.Sprintf("Movie %d", i))).StatusCode)
	}
	_, err := s.Cut(ctx)
	require.NoError(t, err)

	main := readIndex(t, ts).Channels[ChannelMain]
	require.Len(t, main.Snapshots, 1)
	require.Equal(t, 3, main.Snapshots[0].Parts)
	require.Equal(t, 3, main.Patches[0].Parts)

	for i := 1; i <= 3; i++ {
		require.Len(t, readPart(t, ts, fmt.Sprintf("/v1/main/snapshot-1_%d.tar.gz", i)), 1)
	}
	snap := readArtifact(t, ts, ChannelMain, "snapshot-1", 3)
	for _, u := range users {
		require.Contains(t, snap, u.ID+"-1.sjsonl")
	}

	// One manifest covers all parts, saying which part holds each entry.
	m := readManifest(t, s, ts, "/v1/main/snapshot-manifest-1.sjsonl.gz")
	require.Len(t, m.entries, 3)
	for i := 1; i <= 3; i++ {
		name := readPart(t, ts, fmt.Sprintf("/v1/main/snapshot-1_%d.tar.gz", i))[0].name
		var holder string
		for id, e := range m.entries {
			if e.Part == i {
				require.Empty(t, holder, "two entries in part %d", i)
				holder = id
			}
		}
		assert.Equal(t, holder+"-1.sjsonl", name)
	}
}

func TestPatchCompaction(t *testing.T) {
	s, ts := newTestServer(t)
	s.snapshotEvery = 0 // only the epoch-1 snapshot
	s.compactEvery = []int{2, 4}
	ctx := context.Background()

	u1, u2 := NewUser(), NewUser()
	many := titles(40, "Movie")
	docs := map[int][]byte{}
	for rev := 1; rev <= 4; rev++ {
		many[0] = fmt.Sprintf("Rev %d", rev)
		docs[rev] = signedDoc(t, u1, rev, many...)
		require.Equal(t, http.StatusOK, upload(t, ts, docs[rev]).StatusCode)
		if rev == 2 {
			require.Equal(t, http.StatusOK, upload(t, ts, signedDoc(t, u2, 1, "Alien")).StatusCode)
		}
		cut, err := s.Cut(ctx)
		require.NoError(t, err)
		require.Equal(t, rev, cut[ChannelMain])
	}

	main := readIndex(t, ts).Channels[ChannelMain]
	assert.Equal(t, [][2]int{{0, 1}, {1, 2}, {0, 2}, {2, 3}, {3, 4}, {2, 4}, {0, 4}}, patchRanges(main))

	// A compacted patch spans its range: intermediate revs are never
	// published, and users new to the channel in the range get full
	// documents.
	patch := readArtifact(t, ts, ChannelMain, "patch-2-4", 1)
	require.Len(t, patch, 1)
	entry := patch[u1.ID+"-2-4.sjsonl"]
	require.NotNil(t, entry)
	base, _ := canonical(t, docs[2])
	applied, _, err := jsonlines.Patch(base, entry)
	require.NoError(t, err)
	want, _ := canonical(t, docs[4])
	assert.Equal(t, want, applied)

	patch = readArtifact(t, ts, ChannelMain, "patch-0-4", 1)
	assert.Len(t, patch, 2)
	assert.Contains(t, patch, u1.ID+"-4.sjsonl")
	assert.Contains(t, patch, u2.ID+"-1.sjsonl")
}

func TestChannelRemoval(t *testing.T) {
	s, ts := newTestServer(t)
	ctx := context.Background()

	// A channel that leaves out users that fail vetting.
	rejected := map[string]bool{}
	require.NoError(t, s.AddChannel("vetted", func(d *domain.Document) bool { return !rejected[d.Userid] }))

	u1, u2 := NewUser(), NewUser()
	require.Equal(t, http.StatusOK, upload(t, ts, signedDoc(t, u1, 1, "Alien")).StatusCode)
	require.Equal(t, http.StatusOK, upload(t, ts, signedDoc(t, u2, 1, "Heat")).StatusCode)
	cut, err := s.Cut(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, cut["vetted"])

	rejected[u2.ID] = true
	cut, err = s.Cut(ctx)
	require.NoError(t, err)
	assert.Equal(t, map[string]int{"vetted": 2}, cut)

	// The removal is in the manifest; the archive is empty.
	patch := readArtifact(t, ts, "vetted", "patch-1-2", 1)
	assert.Empty(t, patch)
	m := readManifest(t, s, ts, "/v1/vetted/patch-manifest-1-2.sjsonl.gz")
	assert.Empty(t, m.entries)
	assert.Equal(t, []string{u2.ID}, m.removed)

	snap := readArtifact(t, ts, "vetted", "snapshot-2", 1)
	assert.Equal(t, []string{u1.ID + "-1.sjsonl"}, slices.Collect(maps.Keys(snap)))

	// Re-admitted within a compacted range, the user is an ordinary entry.
	delete(rejected, u2.ID)
	cut, err = s.Cut(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, cut["vetted"])
	patch = readArtifact(t, ts, "vetted", "patch-2-3", 1)
	assert.Equal(t, []string{u2.ID + "-1.sjsonl"}, slices.Collect(maps.Keys(patch)))
	m = readManifest(t, s, ts, "/v1/vetted/patch-manifest-2-3.sjsonl.gz")
	assert.Empty(t, m.removed)
	assert.Contains(t, m.entries, u2.ID)
}

func TestDirectChannel(t *testing.T) {
	s, ts := newTestServer(t)
	ctx := context.Background()

	direct := NewUser()
	require.Equal(t, http.StatusOK, upload(t, ts, signedDoc(t, direct, 1, "Alien")).StatusCode)

	// A document that arrived from another server.
	relayed := NewUser()
	canon, hash := canonical(t, signedDoc(t, relayed, 1, "Heat"))
	require.NoError(t, s.docs.Put(hash, canon))
	require.NoError(t, s.dal.AddDocument(ctx, &domain.Document{
		Userid: relayed.ID, Rev: 1, Hash: hash, Size: len(canon), ReceivedTime: time.Now(),
	}))

	_, err := s.Cut(ctx)
	require.NoError(t, err)

	main := readArtifact(t, ts, ChannelMain, "snapshot-1", 1)
	assert.Len(t, main, 2)
	only := readArtifact(t, ts, ChannelDirect, "snapshot-1", 1)
	assert.Equal(t, []string{direct.ID + "-1.sjsonl"}, slices.Collect(maps.Keys(only)))
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
