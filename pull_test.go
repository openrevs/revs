package revs

import (
	"context"
	"net/http"
	"testing"

	"github.com/openrevs/revs/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// latestByUser returns each user's latest accepted document on s.
func latestByUser(t *testing.T, s *Server) map[string]*domain.Document {
	t.Helper()

	docs, err := s.dal.LatestDocuments(context.Background())
	require.NoError(t, err)
	m := map[string]*domain.Document{}
	for _, d := range docs {
		m[d.Userid] = d
	}
	return m
}

func TestServerPull(t *testing.T) {
	ctx := context.Background()
	up, upTS := newTestServer(t)
	down, downTS := newTestServer(t)
	require.NoError(t, down.AddUpstream(upTS.URL+"/"))

	alice, bob := NewUser(), NewUser()

	// Nothing published upstream yet: pulling is a no-op.
	require.NoError(t, down.Pull(ctx))
	assert.Empty(t, latestByUser(t, down))

	require.Equal(t, http.StatusOK, upload(t, upTS, signedDoc(t, alice, 1, "Alien")).StatusCode)
	require.Equal(t, http.StatusOK, upload(t, upTS, signedDoc(t, bob, 1, "Heat")).StatusCode)
	_, err := up.Cut(ctx)
	require.NoError(t, err)

	// The first pull bootstraps from the snapshot and accepts both
	// documents, recording where they came from.
	require.NoError(t, down.Pull(ctx))
	got := latestByUser(t, down)
	require.Len(t, got, 2)
	_, aliceHash := canonical(t, signedDoc(t, alice, 1, "Alien"))
	assert.Equal(t, aliceHash, got[alice.ID].Hash)
	assert.Equal(t, upTS.URL, got[alice.ID].Source)
	assert.Equal(t, upTS.URL, got[bob.ID].Source)

	// Pulled documents are republished like uploaded ones.
	epochs, err := down.Cut(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, epochs[ChannelMain])

	// Bob uploads a newer revision directly downstream; the upstream's
	// older one must not displace it.
	require.Equal(t, http.StatusOK, upload(t, downTS, signedDoc(t, bob, 3, "Heat", "Ronin")).StatusCode)

	// Alice's next revision reaches the downstream through a patch epoch.
	require.Equal(t, http.StatusOK, upload(t, upTS, signedDoc(t, alice, 2, "Alien", "Aliens")).StatusCode)
	require.Equal(t, http.StatusOK, upload(t, upTS, signedDoc(t, bob, 2, "Heat", "Thief")).StatusCode)
	_, err = up.Cut(ctx)
	require.NoError(t, err)

	require.NoError(t, down.Pull(ctx))
	got = latestByUser(t, down)
	assert.Equal(t, 2, got[alice.ID].Rev)
	assert.Equal(t, upTS.URL, got[alice.ID].Source)
	assert.Equal(t, 3, got[bob.ID].Rev)
	assert.Equal(t, "", got[bob.ID].Source, "a direct upload has no source")

	// Servers pulling from each other don't loop: the upstream takes
	// only what is newer, Bob's rev 3, and records the downstream as
	// its source.
	_, err = down.Cut(ctx)
	require.NoError(t, err)
	require.NoError(t, up.AddUpstream(downTS.URL))
	require.NoError(t, up.Pull(ctx))
	got = latestByUser(t, up)
	assert.Equal(t, 2, got[alice.ID].Rev)
	assert.Equal(t, "", got[alice.ID].Source)
	assert.Equal(t, 3, got[bob.ID].Rev)
	assert.Equal(t, downTS.URL, got[bob.ID].Source)

	// Pulling again changes nothing.
	require.NoError(t, down.Pull(ctx))
	require.NoError(t, up.Pull(ctx))
	assert.Equal(t, got, latestByUser(t, up))
}

// A restarted server reconsiders everything its upstream feeds hold,
// so documents that were synced but never accepted are not lost.
func TestServerPullReconcilesOnStart(t *testing.T) {
	ctx := context.Background()
	up, upTS := newTestServer(t)
	dir := t.TempDir()
	down, err := NewServer(dir)
	require.NoError(t, err)
	require.NoError(t, down.AddUpstream(upTS.URL))

	alice := NewUser()
	require.Equal(t, http.StatusOK, upload(t, upTS, signedDoc(t, alice, 1, "Alien")).StatusCode)
	_, err = up.Cut(ctx)
	require.NoError(t, err)
	require.NoError(t, down.Pull(ctx))
	require.Len(t, latestByUser(t, down), 1)

	// Lose the accepted document while keeping the synced feed, as a
	// crash between the two would.
	_, err = down.db.Exec(`DELETE FROM documents`)
	require.NoError(t, err)

	// The running server's feed is already at the upstream's epoch, so a
	// plain pull has nothing new to look at.
	require.NoError(t, down.Pull(ctx))
	require.Empty(t, latestByUser(t, down))

	restarted, err := NewServer(dir)
	require.NoError(t, err)
	require.NoError(t, restarted.AddUpstream(upTS.URL))
	require.NoError(t, restarted.Pull(ctx))
	got := latestByUser(t, restarted)
	require.Len(t, got, 1)
	assert.Equal(t, upTS.URL, got[alice.ID].Source)
}

func TestAddUpstreamValidates(t *testing.T) {
	s, _ := newTestServer(t)

	for _, bad := range []string{"", "revs.example.com", "ftp://revs.example.com", "https://"} {
		assert.Error(t, s.AddUpstream(bad), "%q", bad)
	}

	require.NoError(t, s.AddUpstream("https://revs.example.com/"))
	assert.Error(t, s.AddUpstream("https://revs.example.com"), "duplicate")
}
