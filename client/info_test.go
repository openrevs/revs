package client

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/openrevs/revs/keys"
	"github.com/stretchr/testify/require"
)

func TestIndexInfo(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	idx := &publicationIndex{
		Created:   "2026-11-20T12:00:00Z",
		ServerID:  keys.UserID(pub),
		ServerKey: keys.EncodePublic(pub),
		Channels: map[string]*channelIndex{
			"main": {
				Epoch:     4,
				Snapshots: []snapshotInfo{{Epoch: 1, Parts: 1, Created: "2026-11-01T09:00:00Z"}},
				Patches: []patchInfo{
					// Epochs 2-4: three hours apart, then one.
					{Range: [2]int{1, 2}, Parts: 1, Created: "2026-11-19T06:00:00Z"},
					{Range: [2]int{2, 3}, Parts: 1, Created: "2026-11-19T09:00:00Z"},
					{Range: [2]int{3, 4}, Parts: 1, Created: "2026-11-19T10:00:00Z"},
					// Compacted ranges don't count as publications.
					{Range: [2]int{1, 4}, Parts: 1, Created: "2026-11-19T10:00:05Z"},
				},
			},
			"direct": {Epoch: 0, Snapshots: []snapshotInfo{}, Patches: []patchInfo{}},
		},
	}

	si, err := idx.info()
	require.NoError(t, err)
	require.Equal(t, idx.ServerID, si.ServerID)
	require.Equal(t, time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC), si.Created)
	require.Len(t, si.Channels, 2)

	main := si.Channel("main")
	require.NotNil(t, main)
	require.Equal(t, 4, main.Epoch)
	require.Equal(t, 1, main.Snapshots)
	require.Equal(t, 4, main.Patches)
	require.Equal(t, time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC), main.LastPublished)
	require.Equal(t, time.Hour, main.Interval)
	require.Equal(t, 3, main.LastWeek)

	direct := si.Channel("direct")
	require.NotNil(t, direct)
	require.Zero(t, direct.Epoch)
	require.True(t, direct.LastPublished.IsZero())
	require.Zero(t, direct.Interval)

	// The server ID must match the key.
	idx.ServerID = "00ff"
	_, err = idx.info()
	require.ErrorContains(t, err, "does not match its key")
}
