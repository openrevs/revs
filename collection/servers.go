package collection

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"strings"

	"github.com/openrevs/revs/client"
	"github.com/openrevs/revs/collection/localdb"
	"github.com/openrevs/revs/collection/remotedb"
)

var (
	// ErrBadServerURL is returned by AddServer for a malformed URL.
	ErrBadServerURL = errors.New("not a server URL like https://revs.example.com")
	// ErrServerExists is returned by AddServer for a URL already listed.
	ErrServerExists = errors.New("server is already in your list")
	// ErrNotPublished is returned by ImportMine when the server has no
	// published collection for the user.
	ErrNotPublished = errors.New("this server has no published collection for you yet; pull first")
)

func (c *Collection) Servers(ctx context.Context) ([]localdb.Server, error) {
	return c.q.ListServers(ctx)
}

func (c *Collection) Server(ctx context.Context, id int64) (localdb.Server, error) {
	s, err := c.q.GetServer(ctx, id)
	return s, notFound(err)
}

// AddServer adds a server by URL.
func (c *Collection) AddServer(ctx context.Context, rawURL string) (localdb.Server, error) {
	raw := strings.TrimRight(strings.TrimSpace(rawURL), "/")
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return localdb.Server{}, fmt.Errorf("%q: %w", raw, ErrBadServerURL)
	}
	s, err := c.q.CreateServer(ctx, raw)
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return s, fmt.Errorf("%s: %w", raw, ErrServerExists)
	}
	return s, err
}

// RemoveServer forgets a server and everything pulled from it. What
// was pushed to it stays there.
func (c *Collection) RemoveServer(ctx context.Context, id int64) error {
	if _, err := c.Server(ctx, id); err != nil {
		return err
	}
	return c.syncLocked(ctx, func() error {
		if err := c.closeRemote(id, true); err != nil {
			return err
		}
		return c.q.DeleteServer(ctx, id)
	})
}

// ResetServer discards everything pulled from a server, including its
// pinned key, so the next pull starts fresh.
func (c *Collection) ResetServer(ctx context.Context, id int64) error {
	if _, err := c.Server(ctx, id); err != nil {
		return err
	}
	return c.syncLocked(ctx, func() error { return c.closeRemote(id, true) })
}

// serverRemote loads a server and its remote.
func (c *Collection) serverRemote(ctx context.Context, id int64) (localdb.Server, *remote, error) {
	s, err := c.Server(ctx, id)
	if err != nil {
		return s, nil, err
	}
	rem, err := c.remote(s.ID, s.Url)
	return s, rem, err
}

// ServerStatus is what is known locally about a server.
type ServerStatus struct {
	Server localdb.Server
	Epoch  int                // last pulled epoch
	Users  []remotedb.IdxUser // users in the pulled corpus
	Mine   *remotedb.IdxUser  // the local user's published copy, if pulled
	MyID   string
}

// ServerStatus reports a server's sync state and pulled users. It
// doesn't contact the server.
func (c *Collection) ServerStatus(ctx context.Context, id int64) (*ServerStatus, error) {
	s, rem, err := c.serverRemote(ctx, id)
	if err != nil {
		return nil, err
	}
	rem.client.Reload() // another process may have pulled
	epoch, err := rem.client.Epoch()
	if err != nil {
		return nil, err
	}
	users, err := rem.q.ListIndexedUsers(ctx)
	if err != nil {
		return nil, err
	}
	st := &ServerStatus{Server: s, Epoch: epoch, Users: users, MyID: c.UserID(ctx)}
	for i := range users {
		if users[i].Userid == st.MyID {
			st.Mine = &users[i]
		}
	}
	return st, nil
}

func (c *Collection) recordServerError(ctx context.Context, id int64, err error) {
	msg := err.Error()
	if e := c.q.RecordServerError(ctx, localdb.RecordServerErrorParams{LastError: &msg, ID: id}); e != nil {
		log.Printf("recording server error: %v", e)
	}
}

// Push uploads the collection to a server as the author's newest
// version. A *client.StaleError means the server holds a newer revision
// (probably pushed from another device): the user can merge it with
// Pull and ImportMine, or push again to overwrite it.
func (c *Collection) Push(ctx context.Context, id int64) (*client.PushResult, error) {
	s, rem, err := c.serverRemote(ctx, id)
	if err != nil {
		return nil, err
	}
	key, err := c.signingKey(ctx)
	if err != nil {
		return nil, err
	}
	content, err := c.buildContent(ctx, c.UserID(ctx))
	if err != nil {
		return nil, err
	}

	var res *client.PushResult
	err = c.syncLocked(ctx, func() error {
		res, err = rem.client.Push(ctx, key, content)
		return err
	})
	if err != nil {
		c.recordServerError(context.WithoutCancel(ctx), s.ID, err)
		return nil, err
	}
	rev := int64(res.Rev)
	if err := c.q.RecordPush(ctx, localdb.RecordPushParams{LastPushRev: &rev, ID: s.ID}); err != nil {
		return nil, err
	}
	return res, nil
}

// PushMessage describes a successful push.
func PushMessage(res *client.PushResult) string {
	if !res.Pushed {
		return fmt.Sprintf("Nothing to push: the server already has this content (revision %d).", res.Rev)
	}
	return fmt.Sprintf("Pushed revision %d. It will appear once the server publishes its next epoch.", res.Rev)
}

// Pull syncs a server's published corpus and refreshes the index of
// its reviews; with rebootstrap, it starts over from a snapshot. A
// *client.DivergedError suggests rebootstrapping; a
// *client.KeyChangedError suggests ResetServer, if the user trusts the
// change.
func (c *Collection) Pull(ctx context.Context, id int64, rebootstrap bool) (*client.Result, error) {
	s, err := c.Server(ctx, id)
	if err != nil {
		return nil, err
	}
	var res *client.Result
	err = c.syncLocked(ctx, func() error {
		res, err = c.pull(ctx, s.ID, s.Url, rebootstrap)
		return err
	})
	if err != nil {
		c.recordServerError(context.WithoutCancel(ctx), s.ID, err)
		return nil, err
	}
	epoch := int64(res.Epoch)
	if err := c.q.RecordPull(ctx, localdb.RecordPullParams{LastEpoch: &epoch, ID: s.ID}); err != nil {
		return nil, err
	}
	return res, nil
}

// PullMessage describes a successful pull.
func PullMessage(res *client.Result) string {
	switch {
	case res.Epoch == 0:
		return "The server hasn't published anything yet."
	case res.FromEpoch == res.Epoch && !res.Bootstrapped:
		return fmt.Sprintf("Already up to date (epoch %d).", res.Epoch)
	default:
		return fmt.Sprintf("Pulled epoch %d: %d users changed.", res.Epoch, len(res.Changed))
	}
}

// ImportMine merges the collection this server publishes for the local
// user (e.g. pushed from another device) into the local one. It uses
// the pulled corpus, so pull first.
func (c *Collection) ImportMine(ctx context.Context, id int64) (rev int, stats ImportStats, err error) {
	_, rem, err := c.serverRemote(ctx, id)
	if err != nil {
		return 0, stats, err
	}
	uid := c.UserID(ctx)
	if uid == "" {
		return 0, stats, ErrNoKey
	}
	var doc *client.Document
	err = c.syncLocked(ctx, func() error {
		doc, err = rem.client.Document(uid)
		return err
	})
	if errors.Is(err, client.ErrNotFound) {
		return 0, stats, ErrNotPublished
	}
	if err != nil {
		return 0, stats, err
	}
	pd, err := parseRatingset(doc.Data)
	if err != nil {
		return 0, stats, err
	}
	stats, err = c.mergeTitles(ctx, pd.Titles)
	return doc.Rev, stats, err
}

// UserTitle is one title in another user's pulled collection.
type UserTitle struct {
	Name                 string
	Year                 *int64
	IMDB, TMDB, Wikidata *string
	Reviews              []remotedb.ListUserReviewsRow
}

// ServerUser returns a user's pulled collection from a server.
func (c *Collection) ServerUser(ctx context.Context, serverID int64, uid string) (remotedb.IdxUser, []*UserTitle, error) {
	_, rem, err := c.serverRemote(ctx, serverID)
	if err != nil {
		return remotedb.IdxUser{}, nil, err
	}
	u, err := rem.q.GetIndexedUser(ctx, uid)
	if err != nil {
		return u, nil, notFound(err)
	}
	rows, err := rem.q.ListUserReviews(ctx, uid)
	if err != nil {
		return u, nil, err
	}

	// Group the flat join into titles.
	var titles []*UserTitle
	byID := map[int64]*UserTitle{}
	for _, row := range rows {
		t, ok := byID[row.TitleID]
		if !ok {
			t = &UserTitle{Name: row.Name, Year: row.Year, IMDB: row.ImdbID, TMDB: row.TmdbID, Wikidata: row.WikidataID}
			byID[row.TitleID] = t
			titles = append(titles, t)
		}
		if row.ViewedOn != nil || row.Rating != nil || row.Comment != nil {
			t.Reviews = append(t.Reviews, row)
		}
	}
	return u, titles, nil
}
