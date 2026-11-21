// Package storetest checks that a client.Store keeps sync state as the
// client expects. Implementations of client.Store run it from their
// tests.
package storetest

import (
	"testing"

	"github.com/openrevs/revs/client"
	"github.com/stretchr/testify/require"
)

// TestState checks a store's LoadState and UpdateState. newStore must
// return a store with no state saved.
func TestState(t *testing.T, newStore func(t *testing.T) client.Store) {
	t.Run("empty", func(t *testing.T) {
		state, err := newStore(t).LoadState()
		require.NoError(t, err)
		require.Nil(t, state)
	})

	t.Run("first change", func(t *testing.T) {
		s := newStore(t)
		require.NoError(t, s.UpdateState(client.StateChange{
			Channel: "main", Epoch: 7, ServerKey: "key",
			Heads: map[string]*client.Head{"u1": {Rev: 3, Hash: "ffff"}, "u2": nil},
			Push:  map[string]client.PushState{"u1": {Rev: 3, Hash: "ffff", ServerRev: 4}},
		}))
		requireState(t, s, &client.State{
			Channel: "main", Epoch: 7, ServerKey: "key",
			Heads: map[string]client.Head{"u1": {Rev: 3, Hash: "ffff"}},
			Push:  map[string]client.PushState{"u1": {Rev: 3, Hash: "ffff", ServerRev: 4}},
		})
	})

	t.Run("changes touch only what they list", func(t *testing.T) {
		s := newStore(t)
		require.NoError(t, s.UpdateState(client.StateChange{
			Channel: "main", Epoch: 1,
			Heads: map[string]*client.Head{"u1": {Rev: 1, Hash: "aa"}, "u2": {Rev: 1, Hash: "bb"}, "u3": {Rev: 1, Hash: "cc"}},
			Push:  map[string]client.PushState{"u1": {Rev: 1, Hash: "aa"}, "u2": {Rev: 1, Hash: "bb"}},
		}))
		// u1's head changes, u2's goes, u4 arrives; u1's push state
		// changes and u9's is new.
		require.NoError(t, s.UpdateState(client.StateChange{
			Channel: "main", Epoch: 2, ServerKey: "key",
			Heads: map[string]*client.Head{"u1": {Rev: 2, Hash: "a2"}, "u2": nil, "u4": {Rev: 1, Hash: "dd"}},
			Push:  map[string]client.PushState{"u1": {Rev: 2, Hash: "a2"}, "u9": {ServerRev: 5}},
		}))
		requireState(t, s, &client.State{
			Channel: "main", Epoch: 2, ServerKey: "key",
			Heads: map[string]client.Head{"u1": {Rev: 2, Hash: "a2"}, "u3": {Rev: 1, Hash: "cc"}, "u4": {Rev: 1, Hash: "dd"}},
			Push:  map[string]client.PushState{"u1": {Rev: 2, Hash: "a2"}, "u2": {Rev: 1, Hash: "bb"}, "u9": {ServerRev: 5}},
		})

		// Just the epoch and pin: everything else stays.
		require.NoError(t, s.UpdateState(client.StateChange{Channel: "main", Epoch: 3, ServerKey: "key2"}))
		requireState(t, s, &client.State{
			Channel: "main", Epoch: 3, ServerKey: "key2",
			Heads: map[string]client.Head{"u1": {Rev: 2, Hash: "a2"}, "u3": {Rev: 1, Hash: "cc"}, "u4": {Rev: 1, Hash: "dd"}},
			Push:  map[string]client.PushState{"u1": {Rev: 2, Hash: "a2"}, "u2": {Rev: 1, Hash: "bb"}, "u9": {ServerRev: 5}},
		})
	})

	t.Run("reset heads", func(t *testing.T) {
		s := newStore(t)
		require.NoError(t, s.UpdateState(client.StateChange{
			Channel: "main", Epoch: 4,
			Heads: map[string]*client.Head{"u1": {Rev: 1, Hash: "aa"}, "u2": {Rev: 1, Hash: "bb"}},
			Push:  map[string]client.PushState{"u1": {Rev: 1, Hash: "aa"}},
		}))
		// A rebootstrap onto another channel: the heads start over, the
		// push state stays.
		require.NoError(t, s.UpdateState(client.StateChange{
			Channel: "other", Epoch: 9, ResetHeads: true,
			Heads: map[string]*client.Head{"u3": {Rev: 2, Hash: "cc"}},
		}))
		requireState(t, s, &client.State{
			Channel: "other", Epoch: 9,
			Heads: map[string]client.Head{"u3": {Rev: 2, Hash: "cc"}},
			Push:  map[string]client.PushState{"u1": {Rev: 1, Hash: "aa"}},
		})

		require.NoError(t, s.UpdateState(client.StateChange{Channel: "other", ResetHeads: true}))
		requireState(t, s, &client.State{
			Channel: "other",
			Heads:   map[string]client.Head{},
			Push:    map[string]client.PushState{"u1": {Rev: 1, Hash: "aa"}},
		})
	})
}

// requireState checks the store's state is want. Empty maps and nil
// ones are taken as the same.
func requireState(t *testing.T, s client.Store, want *client.State) {
	t.Helper()
	got, err := s.LoadState()
	require.NoError(t, err)
	require.NotNil(t, got)
	norm := func(st *client.State) client.State {
		out := *st
		if len(out.Heads) == 0 {
			out.Heads = nil
		}
		if len(out.Push) == 0 {
			out.Push = nil
		}
		return out
	}
	require.Equal(t, norm(want), norm(got))
}
