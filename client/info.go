package client

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/openrevs/revs/keys"
)

// ServerInfo describes a server as its index presents it. The index is
// unsigned, so this is what the server claims; Sync verifies what it
// pulls against the key pinned from it.
type ServerInfo struct {
	ServerID  string        `json:"server_id"`
	ServerKey string        `json:"server_key"`
	Created   time.Time     `json:"created"` // when the server generated the index
	Channels  []ChannelInfo `json:"channels"`
}

// ChannelInfo summarizes one published channel.
type ChannelInfo struct {
	Name      string `json:"name"`
	Epoch     int    `json:"epoch"`
	Snapshots int    `json:"snapshots"`
	Patches   int    `json:"patches"`

	// LastPublished is when the current epoch was published; zero
	// before the first.
	LastPublished time.Time `json:"last_published,omitzero"`

	// Interval estimates how often the server publishes: the shortest
	// gap between consecutive epochs. A server only publishes when
	// something changed, so this is an upper bound on its schedule;
	// zero with fewer than two epochs to compare.
	Interval time.Duration `json:"interval,omitzero"`

	// LastWeek counts the epochs published in the seven days before
	// the index was generated.
	LastWeek int `json:"last_week"`
}

// Channel returns the named channel, or nil.
func (si *ServerInfo) Channel(name string) *ChannelInfo {
	for i := range si.Channels {
		if si.Channels[i].Name == name {
			return &si.Channels[i]
		}
	}
	return nil
}

// Info fetches the server's index and checks it is well formed: that
// it advertises a valid key matching its server ID, and publishes the
// channel the client follows. It doesn't touch the store (New may be
// given a nil one to only call Info) or the pinned key.
func (c *Client) Info(ctx context.Context) (*ServerInfo, error) {
	idx, err := c.fetchIndex(ctx)
	if err != nil {
		return nil, err
	}
	si, err := idx.info()
	if err != nil {
		return nil, err
	}
	if si.Channel(c.channel) == nil {
		return nil, fmt.Errorf("client: server publishes no channel %q", c.channel)
	}
	return si, nil
}

func (idx *publicationIndex) info() (*ServerInfo, error) {
	pub, err := keys.ParsePublic(idx.ServerKey)
	if err != nil {
		return nil, fmt.Errorf("client: index advertises invalid server key %q: %w", idx.ServerKey, err)
	}
	if id := keys.UserID(pub); id != idx.ServerID {
		return nil, fmt.Errorf("client: index server ID %s does not match its key (%s)", idx.ServerID, id)
	}
	created, err := time.Parse(time.RFC3339, idx.Created)
	if err != nil {
		return nil, fmt.Errorf("client: index: bad created time %q", idx.Created)
	}

	si := &ServerInfo{ServerID: idx.ServerID, ServerKey: keys.EncodePublic(pub), Created: created}
	for _, name := range slices.Sorted(maps.Keys(idx.Channels)) {
		ch := idx.Channels[name]
		if ch == nil {
			continue
		}
		si.Channels = append(si.Channels, ch.info(name, created))
	}
	return si, nil
}

func (ch *channelIndex) info(name string, now time.Time) ChannelInfo {
	ci := ChannelInfo{Name: name, Epoch: ch.Epoch, Snapshots: len(ch.Snapshots), Patches: len(ch.Patches)}

	// When each epoch was published: its single-epoch patch, or a
	// snapshot at it (the first epoch may have only that).
	published := map[int]time.Time{}
	note := func(epoch int, created string) {
		t, err := time.Parse(time.RFC3339, created)
		if err != nil {
			return
		}
		if old, ok := published[epoch]; !ok || t.Before(old) {
			published[epoch] = t
		}
	}
	for _, p := range ch.Patches {
		if p.Range[1] == p.Range[0]+1 {
			note(p.Range[1], p.Created)
		}
	}
	for _, s := range ch.Snapshots {
		note(s.Epoch, s.Created)
	}

	ci.LastPublished = published[ch.Epoch]
	weekAgo := now.Add(-7 * 24 * time.Hour)
	for e, t := range published {
		if t.After(weekAgo) {
			ci.LastWeek++
		}
		if prev, ok := published[e-1]; ok {
			if gap := t.Sub(prev); gap > 0 && (ci.Interval == 0 || gap < ci.Interval) {
				ci.Interval = gap
			}
		}
	}
	return ci
}
