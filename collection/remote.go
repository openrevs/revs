package collection

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/openrevs/revs"
	"github.com/openrevs/revs/client"
	"github.com/openrevs/revs/collection/localdb"
	"github.com/tidwall/gjson"
)

// remote is the client for one server. What is pulled from it is a
// feed in the shared corpus (c.corpus), and its review index is the
// idx_ rows for its server id, all in the collection's database.
type remote struct {
	id     int64
	feed   string
	client *client.Client
}

func feedName(serverID int64) string {
	return strconv.FormatInt(serverID, 10)
}

// remote returns the client for a server, creating it on first use.
func (c *Collection) remote(serverID int64, serverURL string) *remote {
	c.remotesMu.Lock()
	defer c.remotesMu.Unlock()

	if r, ok := c.remotes[serverID]; ok {
		return r
	}

	feed := feedName(serverID)
	r := &remote{
		id:     serverID,
		feed:   feed,
		client: client.New(serverURL, c.corpus.Feed(feed), client.WithHTTPClient(c.http), client.WithHistory(c.versions)),
	}
	c.remotes[serverID] = r
	return r
}

// forgetRemote discards everything pulled from a server: its feed in
// the corpus (and the documents no other server holds), its sync state,
// and its review index. The pulled corpus is a cache, so this is how a
// server is reset or forgotten.
func (c *Collection) forgetRemote(ctx context.Context, serverID int64) error {
	c.remotesMu.Lock()
	delete(c.remotes, serverID)
	c.remotesMu.Unlock()

	if err := c.q.DeleteIndexedServer(ctx, serverID); err != nil {
		return err
	}
	return c.corpus.DeleteFeed(feedName(serverID))
}

// reindex rebuilds the review index for the given users from the
// corpus; with all set, it rebuilds it for every user.
func (c *Collection) reindex(ctx context.Context, r *remote, userIDs []string, all bool) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := c.q.WithTx(tx)

	index := func(doc *client.Document) error {
		if err := q.DeleteIndexedUser(ctx, localdb.DeleteIndexedUserParams{ServerID: r.id, Userid: doc.UserID}); err != nil {
			return err
		}
		return indexDocument(ctx, q, r.id, doc)
	}

	if all {
		if err := q.DeleteIndexedServer(ctx, r.id); err != nil {
			return err
		}
		for doc, err := range r.client.Documents() {
			if err != nil {
				return err
			}
			if err := index(doc); err != nil {
				return err
			}
		}
	} else {
		for _, id := range userIDs {
			doc, err := r.client.Document(id)
			if err != nil {
				return err
			}
			if err := index(doc); err != nil {
				return err
			}
		}
	}

	return tx.Commit()
}

// indexDocument records one verified document's items and entries.
// Following the ratingset spec, a document whose author line lacks a
// valid rating scale is indexed with no items, and item lines that
// don't parse or are invalid are skipped rather than failing the whole
// index. Items of types other than movie are skipped too.
func indexDocument(ctx context.Context, q *localdb.Queries, serverID int64, doc *client.Document) error {
	var items []revs.Item
	lines := bytes.Split(doc.Data, []byte("\n"))
	lines = lines[min(1, len(lines)):] // skip the control line

	var scale ratingScale
	validScale := false
	for _, line := range lines {
		if gjson.GetBytes(line, "id").Exists() {
			var err error
			scale, err = scaleOf(line)
			validScale = err == nil
		}
	}
	if validScale {
		for _, line := range lines {
			if len(line) == 0 || gjson.GetBytes(line, "id").Exists() {
				continue
			}
			it, ok := readItem(line, scale)
			if !ok || !it.IsType(revs.ItemTypeMovie) {
				continue
			}
			items = append(items, it)
		}
	}

	err := q.InsertIndexedUser(ctx, localdb.InsertIndexedUserParams{
		ServerID: serverID, Userid: doc.UserID, Rev: int64(doc.Rev), Hash: doc.Hash, TitleCount: int64(len(items)),
	})
	if err != nil {
		return err
	}

	for _, t := range items {
		p := localdb.InsertIndexedTitleParams{ServerID: serverID, Userid: doc.UserID, Name: strings.TrimSpace(t.Title)}
		if t.Year != nil {
			y := int64(*t.Year)
			p.Year = &y
		}
		for k, v := range t.Refs {
			switch strings.ToLower(k) {
			case refIMDB:
				p.ImdbID = normalizeIMDB(v)
			case refTMDB:
				p.TmdbID = normalizeTMDB(v)
			case refWikidata:
				p.WikidataID = normalizeWikidata(v)
			}
		}
		titleID, err := q.InsertIndexedTitle(ctx, p)
		if err != nil {
			return err
		}
		for _, rv := range t.Entries {
			rp := localdb.InsertIndexedReviewParams{TitleID: titleID, ViewedOn: rv.Date}
			rp.Rating = scale.scale(rv.Rating)
			if rv.Review != nil {
				rp.Comment = *rv.Review
			}
			if err := q.InsertIndexedReview(ctx, rp); err != nil {
				return err
			}
		}
	}
	return nil
}

// pull syncs a server's corpus and refreshes the review index.
func (c *Collection) pull(ctx context.Context, serverID int64, serverURL string, rebootstrap bool) (*client.Result, error) {
	r := c.remote(serverID, serverURL)

	var (
		res *client.Result
		err error
	)
	if rebootstrap {
		res, err = r.client.Rebootstrap(ctx)
	} else {
		res, err = r.client.Sync(ctx)
	}
	if err != nil {
		return nil, err
	}

	n, err := c.q.CountIndexedUsers(ctx, serverID)
	if err != nil {
		return nil, err
	}
	if err := c.reindex(ctx, r, res.Changed, res.Bootstrapped || n == 0); err != nil {
		return nil, fmt.Errorf("indexing pulled documents: %w", err)
	}

	return res, nil
}
