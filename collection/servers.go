package collection

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"strconv"
	"strings"

	"github.com/openrevs/revs/client"
	"github.com/openrevs/revs/collection/localdb"
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
		if err := c.forgetRemote(ctx, id); err != nil {
			return err
		}
		if err := c.q.DeleteSetting(ctx, unmergedKey(id)); err != nil {
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
	return c.syncLocked(ctx, func() error { return c.forgetRemote(ctx, id) })
}

// serverRemote loads a server and its remote.
func (c *Collection) serverRemote(ctx context.Context, id int64) (localdb.Server, *remote, error) {
	s, err := c.Server(ctx, id)
	if err != nil {
		return s, nil, err
	}
	return s, c.remote(s.ID, s.Url), nil
}

// ServerStatus is what is known locally about a server.
type ServerStatus struct {
	Server localdb.Server
	Epoch  int               // last pulled epoch
	Users  []localdb.IdxUser // users in the pulled corpus
	Mine   *localdb.IdxUser  // the local user's published copy, if pulled
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
	users, err := c.q.ListIndexedUsers(ctx, rem.id)
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
// version, first merging in any copy of the author's collection the
// server holds that this collection hasn't seen: one pushed from
// another device, or from this one before its data was wiped and the
// identity imported again. Without that, the push would replace the
// server's copy, losing whatever only it holds.
//
// A copy is merged from the server's pulled corpus. When the server
// reports a newer revision than the corpus has, Push pulls and merges
// that; a *client.StaleError means the server hasn't published it yet,
// so it can't be merged. Pushing again after the server's next epoch
// merges it then; ForcePush replaces it instead.
func (c *Collection) Push(ctx context.Context, id int64) (*PushResult, error) {
	return c.push(ctx, id, false)
}

// ForcePush uploads the collection to a server as the author's newest
// version without merging the server's copy first, replacing it.
func (c *Collection) ForcePush(ctx context.Context, id int64) (*PushResult, error) {
	return c.push(ctx, id, true)
}

// PushResult reports what Push did.
type PushResult struct {
	*client.PushResult
	// Merged is set when the server's copy of the author's collection
	// was merged into the local one before pushing.
	Merged *MergeResult
}

// MergeResult describes a copy of the author's collection merged from
// a server.
type MergeResult struct {
	Rev int
	ImportStats
}

func (c *Collection) push(ctx context.Context, id int64, force bool) (*PushResult, error) {
	s, rem, err := c.serverRemote(ctx, id)
	if err != nil {
		return nil, err
	}
	key, err := c.signingKey(ctx)
	if err != nil {
		return nil, err
	}
	uid := c.UserID(ctx)

	res := &PushResult{}
	err = c.syncLocked(ctx, func() error {
		if force {
			if err := c.q.DeleteSetting(ctx, unmergedKey(s.ID)); err != nil {
				return err
			}
		}
		for attempt := 0; ; attempt++ {
			if !force {
				m, err := c.mergeServerCopy(ctx, s, rem, uid)
				if err != nil {
					return err
				}
				if m != nil {
					res.Merged = m
				}
			}
			content, err := c.buildContent(ctx, uid)
			if err != nil {
				return err
			}
			pr, err := rem.client.Push(ctx, key, content)
			var stale *client.StaleError
			if errors.As(err, &stale) && !force {
				// Remember the revision to merge, so a later Push doesn't
				// assert past it before merging it.
				if err := c.setSetting(ctx, unmergedKey(s.ID), strconv.Itoa(stale.LatestRev)); err != nil {
					return err
				}
				if attempt == 0 {
					continue
				}
			}
			if err != nil {
				return err
			}
			res.PushResult = pr
			return nil
		}
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

// unmergedKey is the setting holding the revision of the author's
// collection a server reported in a stale verdict, until it has been
// merged.
func unmergedKey(serverID int64) string {
	return "unmerged_rev:" + feedName(serverID)
}

// mergedKey is the setting that records that the author's document
// with this hash, pulled from a server, has been merged, so it isn't
// merged again (bringing back what was since deleted locally).
func mergedKey(hash string) string {
	return "merged_doc:" + hash
}

// mergeServerCopy merges the server's copy of the author's collection
// into the local one, unless it is a version this collection wrote or
// has merged before. If the server reported an unmerged revision that
// the pulled corpus lacks, it pulls first; a *client.StaleError means
// the server still doesn't publish it. Callers hold the sync lock.
func (c *Collection) mergeServerCopy(ctx context.Context, s localdb.Server, rem *remote, uid string) (*MergeResult, error) {
	unmerged := c.settingInt(ctx, unmergedKey(s.ID))

	doc, err := rem.client.Document(uid)
	if errors.Is(err, client.ErrNotFound) {
		err = nil
	}
	if err != nil {
		return nil, err
	}
	if unmerged > 0 && (doc == nil || doc.Rev < unmerged) {
		if _, err := c.pullLocked(ctx, s); err != nil {
			return nil, fmt.Errorf("pulling the server's newer copy of your collection to merge it: %w", err)
		}
		if doc, err = rem.client.Document(uid); err != nil && !errors.Is(err, client.ErrNotFound) {
			return nil, err
		}
		if doc == nil || doc.Rev < unmerged {
			return nil, &client.StaleError{UserID: uid, LatestRev: unmerged, Reason: "the server has not published it yet"}
		}
	}
	if doc == nil {
		return nil, nil
	}

	m, err := c.mergeDocument(ctx, s, doc)
	if err != nil {
		return nil, err
	}
	if unmerged > 0 {
		if err := c.q.DeleteSetting(ctx, unmergedKey(s.ID)); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// mergeDocument merges doc, the author's own document pulled from a
// server, unless the local collection already supersedes it: it is a
// version this collection wrote, or has merged before, or is older
// than a revision since pushed to the server (which either merged it
// or, with ForcePush, chose to replace it).
func (c *Collection) mergeDocument(ctx context.Context, s localdb.Server, doc *client.Document) (*MergeResult, error) {
	if s.LastPushRev != nil && int64(doc.Rev) < *s.LastPushRev {
		return nil, nil
	}
	if _, err := c.versions.Version(doc.UserID, doc.Hash); err == nil {
		return nil, nil
	} else if !errors.Is(err, client.ErrNotFound) {
		return nil, err
	}
	if c.setting(ctx, mergedKey(doc.Hash)) != "" {
		return nil, nil
	}
	pd, err := parseRatingset(doc.Data)
	if err != nil {
		return nil, fmt.Errorf("reading your collection from %s: %w", s.Url, err)
	}
	stats, err := c.mergeTitles(ctx, pd.Titles)
	if err != nil {
		return nil, err
	}
	if err := c.setSetting(ctx, mergedKey(doc.Hash), strconv.Itoa(doc.Rev)); err != nil {
		return nil, err
	}
	return &MergeResult{Rev: doc.Rev, ImportStats: stats}, nil
}

// PushMessage describes a successful push.
func PushMessage(res *PushResult) string {
	var msg string
	if m := res.Merged; m != nil {
		msg = fmt.Sprintf("Merged your collection from the server (revision %d) first. %s ", m.Rev, m.ImportStats)
	}
	if !res.Pushed {
		return msg + fmt.Sprintf("Nothing to push: the server already has this content (revision %d).", res.Rev)
	}
	return msg + fmt.Sprintf("Pushed revision %d. It will appear once the server publishes its next epoch.", res.Rev)
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
	return res, c.recordPull(ctx, s, res)
}

// pullLocked pulls a server's corpus, for callers holding the sync
// lock.
func (c *Collection) pullLocked(ctx context.Context, s localdb.Server) (*client.Result, error) {
	res, err := c.pull(ctx, s.ID, s.Url, false)
	if err != nil {
		return nil, err
	}
	return res, c.recordPull(ctx, s, res)
}

func (c *Collection) recordPull(ctx context.Context, s localdb.Server, res *client.Result) error {
	epoch := int64(res.Epoch)
	return c.q.RecordPull(ctx, localdb.RecordPullParams{LastEpoch: &epoch, ID: s.ID})
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
	if stats, err = c.mergeTitles(ctx, pd.Titles); err != nil {
		return 0, stats, err
	}
	// Push needn't merge it again.
	if err := c.setSetting(ctx, mergedKey(doc.Hash), strconv.Itoa(doc.Rev)); err != nil {
		return 0, stats, err
	}
	if doc.Rev >= c.settingInt(ctx, unmergedKey(id)) {
		if err := c.q.DeleteSetting(ctx, unmergedKey(id)); err != nil {
			return 0, stats, err
		}
	}
	return doc.Rev, stats, nil
}

// UserTitle is one title in another user's pulled collection.
type UserTitle struct {
	Name                 string
	Year                 *int64
	IMDB, TMDB, Wikidata *string
	Reviews              []localdb.ListUserReviewsRow
}

// ServerUser returns a user's pulled collection from a server.
func (c *Collection) ServerUser(ctx context.Context, serverID int64, uid string) (localdb.IdxUser, []*UserTitle, error) {
	if _, err := c.Server(ctx, serverID); err != nil {
		return localdb.IdxUser{}, nil, err
	}
	u, err := c.q.GetIndexedUser(ctx, localdb.GetIndexedUserParams{ServerID: serverID, Userid: uid})
	if err != nil {
		return u, nil, notFound(err)
	}
	rows, err := c.q.ListUserReviews(ctx, localdb.ListUserReviewsParams{ServerID: serverID, Userid: uid})
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
