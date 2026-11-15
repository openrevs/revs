package client

import (
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"strings"
	"testing"

	"github.com/openrevs/revs/keys"
	jsonlines "github.com/openrevs/sjsonl-go"
	"github.com/stretchr/testify/require"
)

// signManifest builds a gzipped manifest signed with key.
func signManifest(t *testing.T, key ed25519.PrivateKey, lines ...string) []byte {
	t.Helper()

	signed, _, err := jsonlines.Sign([]byte(strings.Join(lines, "\n")+"\n"), key)
	require.NoError(t, err)

	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, err = zw.Write(signed)
	require.NoError(t, err)
	require.NoError(t, zw.Close())

	return buf.Bytes()
}

func TestParseManifest(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	pub := key.Public().(ed25519.PublicKey)
	pin, sid := keys.EncodePublic(pub), keys.UserID(pub)

	const uid2 = "c40b5005a10cbe5a3d105334f11db40fec185ca70a64670b705c482e84efef62"
	meta := fmt.Sprintf(`{"manifest":"patch","server_id":%q,"channel":"main","range":[3,5],"created":"2026-11-13T00:00:00Z","format":1}`, sid)
	entry := fmt.Sprintf(`{"id":%q,"rev":4,"hash":"aa","base_rev":2,"base_hash":"bb","part":2}`, testUID)
	removal := fmt.Sprintf(`{"removed":%q}`, uid2)
	want := manifestWant{kind: "patch", serverID: sid, channel: "main", rng: [2]int{3, 5}, parts: 2}

	m, err := parseManifest(signManifest(t, key, meta, entry, removal), pin, want)
	require.NoError(t, err)
	require.Equal(t, map[string]manifestEntry{
		testUID: {ID: testUID, Rev: 4, Hash: "aa", BaseRev: 2, BaseHash: "bb", Part: 2},
	}, m.entries)
	require.Equal(t, []string{uid2}, m.removed)

	// Signed by another key.
	_, other, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	_, err = parseManifest(signManifest(t, other, meta, entry), pin, want)
	var keyChanged *KeyChangedError
	require.ErrorAs(t, err, &keyChanged)

	snapWant := manifestWant{kind: "snapshot", serverID: sid, channel: "main", epoch: 5, parts: 2}
	snapMeta := strings.Replace(strings.Replace(meta, `"patch"`, `"snapshot"`, 1), `"range":[3,5]`, `"epoch":5`, 1)
	_, err = parseManifest(signManifest(t, key, snapMeta, strings.Replace(entry, `,"base_rev":2,"base_hash":"bb"`, "", 1)), pin, snapWant)
	require.NoError(t, err)

	for name, tc := range map[string]struct {
		lines []string
		want  manifestWant
	}{
		"no metadata":         {[]string{entry}, want},
		"two metadata lines":  {[]string{meta, strings.Replace(meta, "00:00:00", "00:00:01", 1)}, want},
		"other kind":          {[]string{meta}, snapWant},
		"other server":        {[]string{meta}, manifestWant{kind: "patch", serverID: testUID, channel: "main", rng: [2]int{3, 5}, parts: 2}},
		"other channel":       {[]string{meta}, manifestWant{kind: "patch", serverID: sid, channel: "direct", rng: [2]int{3, 5}, parts: 2}},
		"other range":         {[]string{meta}, manifestWant{kind: "patch", serverID: sid, channel: "main", rng: [2]int{4, 5}, parts: 2}},
		"other format":        {[]string{strings.Replace(meta, `"format":1`, `"format":2`, 1)}, want},
		"part out of range":   {[]string{meta, strings.Replace(entry, `"part":2`, `"part":3`, 1)}, want},
		"partial base":        {[]string{meta, strings.Replace(entry, `"base_rev":2,`, "", 1)}, want},
		"backwards base":      {[]string{meta, strings.Replace(entry, `"base_rev":2`, `"base_rev":4`, 1)}, want},
		"bad user id":         {[]string{meta, strings.Replace(entry, testUID, "ABC", 1)}, want},
		"removes and updates": {[]string{meta, entry, fmt.Sprintf(`{"removed":%q}`, testUID)}, want},
		"ambiguous line":      {[]string{meta, fmt.Sprintf(`{"id":%q,"removed":%q}`, testUID, uid2)}, want},
		"unknown line":        {[]string{meta, `{"note":"x"}`}, want},
		"snapshot removal":    {[]string{snapMeta, removal}, snapWant},
		"snapshot with base":  {[]string{snapMeta, entry}, snapWant},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseManifest(signManifest(t, key, tc.lines...), pin, tc.want)
			require.Error(t, err)
		})
	}
}

// memStore is an in-memory Store.
type memStore struct {
	blobs map[string][]byte
	state *State
}

func (s *memStore) PutBlob(hash string, data []byte) error { s.blobs[hash] = data; return nil }
func (s *memStore) GetBlob(hash string) ([]byte, error) {
	if b, ok := s.blobs[hash]; ok {
		return b, nil
	}
	return nil, fmt.Errorf("memstore: %s: %w", hash, ErrBlobNotFound)
}
func (s *memStore) LoadState() (*State, error) { return s.state, nil }
func (s *memStore) SaveState(st *State) error  { s.state = st; return nil }

func TestPlan(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	uid := UserID(key.Public().(ed25519.PublicKey))
	sign := func(rev int, title string) (string, []byte) {
		signed, info, err := jsonlines.Sign([]byte(fmt.Sprintf(`{"id":%q,"revision":%d}`+"\n"+`{"name":%q}`+"\n", uid, rev, title)), key)
		require.NoError(t, err)
		return info.Hash, signed
	}
	h1, d1 := sign(1, "Alien")
	h2, d2 := sign(2, "Heat")
	h2b, _ := sign(2, "Ran") // the same rev, signed twice

	store := &memStore{blobs: map[string][]byte{h1: d1, h2: d2}}
	c := New("http://unused", store)
	require.NoError(t, c.ensureState())
	c.state.Heads = map[string]Head{uid: {Rev: 2, Hash: h2}}

	// A held document is taken as is.
	res := &Result{}
	heads := map[string]Head{}
	need, parts, err := c.plan(&manifest{entries: map[string]manifestEntry{uid: {ID: uid, Rev: 2, Hash: h2, BaseRev: 1, BaseHash: h1, Part: 1}}}, heads, res)
	require.NoError(t, err)
	require.Empty(t, need)
	require.Empty(t, parts)
	require.Equal(t, Head{Rev: 2, Hash: h2}, heads[uid])
	require.Empty(t, res.Equivocations)

	// A held blob must be the document the manifest says it is.
	c.state.Heads = map[string]Head{}
	_, _, err = c.plan(&manifest{entries: map[string]manifestEntry{uid: {ID: uid, Rev: 3, Hash: h2, Part: 1}}}, map[string]Head{}, res)
	require.ErrorContains(t, err, "isn't")
	c.state.Heads = map[string]Head{uid: {Rev: 2, Hash: h2}}

	// The same rev with a different document is an equivocation; the
	// published document is still fetched.
	need, parts, err = c.plan(&manifest{entries: map[string]manifestEntry{uid: {ID: uid, Rev: 2, Hash: h2b, BaseRev: 1, BaseHash: h1, Part: 2}}}, map[string]Head{}, res)
	require.NoError(t, err)
	require.Contains(t, need, uid)
	require.Equal(t, []int{2}, parts)
	require.Equal(t, []Equivocation{{UserID: uid, Rev: 2, Held: h2, Published: h2b}}, res.Equivocations)

	// A patch whose base isn't held is refused before any download.
	_, _, err = c.plan(&manifest{entries: map[string]manifestEntry{uid: {ID: uid, Rev: 3, Hash: "cc", BaseRev: 2, BaseHash: "dd", Part: 1}}}, map[string]Head{}, res)
	var missing *missingBaseError
	require.ErrorAs(t, err, &missing)
}
