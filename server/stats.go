package server

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Stats is a summary of a server's state, as the admin UI shows it.
type Stats struct {
	// ID is the server's identity, derived from its key.
	ID      string
	Started time.Time

	// Users is the number of users the server holds a document for, and
	// DirectUsers those of them whose latest document was uploaded here
	// rather than pulled from an upstream.
	Users       int
	DirectUsers int
	// Revisions counts every document revision accepted, and Bytes the
	// total size of each user's latest document.
	Revisions int
	Bytes     int64
	// LastReceived is when the newest document was accepted (zero if
	// none has been).
	LastReceived time.Time

	CutInterval time.Duration
	Channels    []ChannelStats

	PullInterval time.Duration
	Upstreams    []UpstreamStats
}

// ChannelStats describes one publication channel.
type ChannelStats struct {
	Name string
	// Epoch is the latest epoch published (0 if none), cut at Cut.
	Epoch int
	Cut   time.Time
	// Members is how many users the latest epoch holds.
	Members int
}

// UpstreamStats describes an upstream server and its last pull.
type UpstreamStats struct {
	URL string
	// LastPull is when the last pull finished (zero if none has), and
	// LastErr its error, if it failed.
	LastPull time.Time
	LastErr  string
	// Accepted counts documents accepted from the upstream since the
	// server started.
	Accepted int
}

// upstreamStatus is what the server remembers about an upstream's
// pulls, for Stats.
type upstreamStatus struct {
	lastPull time.Time
	lastErr  string
	accepted int
}

// Stats summarizes the server's state.
func (s *Server) Stats(ctx context.Context) (Stats, error) {
	st := Stats{
		ID:           s.serverID(),
		Started:      s.started,
		CutInterval:  s.cutInterval,
		PullInterval: s.pullInterval,
	}

	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(source = ''), 0), COALESCE(SUM(size), 0),
		       (SELECT COUNT(*) FROM documents)
		FROM documents d
		WHERE d.rev = (SELECT MAX(rev) FROM documents m WHERE m.userid = d.userid)`,
	).Scan(&st.Users, &st.DirectUsers, &st.Bytes, &st.Revisions)
	if err != nil {
		return Stats{}, err
	}
	// Selecting the column itself, not MAX of it, keeps its datetime
	// type, so it scans as a time.
	err = s.db.QueryRowContext(ctx,
		`SELECT received_time FROM documents ORDER BY received_time DESC LIMIT 1`,
	).Scan(&st.LastReceived)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Stats{}, err
	}

	s.cutMu.Lock()
	names := make([]string, len(s.channels))
	for i, ch := range s.channels {
		names[i] = ch.name
	}
	s.cutMu.Unlock()

	for _, name := range names {
		cs := ChannelStats{Name: name}
		err := s.db.QueryRowContext(ctx, `
			SELECT epoch, created FROM channel_epochs
			WHERE channel = ? ORDER BY epoch DESC LIMIT 1`, name,
		).Scan(&cs.Epoch, &cs.Cut)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return Stats{}, err
		}
		// A user is a member if their latest change holds a document.
		err = s.db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM channel_changes c
			WHERE c.channel = ? AND c.rev IS NOT NULL
			  AND c.epoch = (SELECT MAX(m.epoch) FROM channel_changes m
			                 WHERE m.channel = c.channel AND m.userid = c.userid)`, name,
		).Scan(&cs.Members)
		if err != nil {
			return Stats{}, err
		}
		st.Channels = append(st.Channels, cs)
	}

	s.statusMu.Lock()
	for _, up := range s.upstreams {
		st.Upstreams = append(st.Upstreams, UpstreamStats{
			URL:      up.url,
			LastPull: up.status.lastPull,
			LastErr:  up.status.lastErr,
			Accepted: up.status.accepted,
		})
	}
	s.statusMu.Unlock()

	return st, nil
}
