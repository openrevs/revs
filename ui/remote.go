package ui

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/openrevs/revs"
	"github.com/openrevs/revs/client"
	"github.com/openrevs/revs/client/sqlitestore"
	"github.com/openrevs/revs/ui/remotedb"
	"github.com/tidwall/gjson"
)

// remote is everything held locally for one server, all in a single
// SQLite file: the verified corpus and sync state (sqlitestore's
// tables), plus a derived index of other users' reviews (remotedb).
type remote struct {
	path   string
	store  *sqlitestore.Store
	db     *sql.DB
	q      *remotedb.Queries
	client *client.Client
}

func (a *App) remotePath(serverID int64) string {
	return filepath.Join(a.dataDir, "servers", strconv.FormatInt(serverID, 10)+".db")
}

// remote returns the open remote for a server, opening it on first use.
func (a *App) remote(serverID int64, serverURL string) (*remote, error) {
	a.remotesMu.Lock()
	defer a.remotesMu.Unlock()

	if r, ok := a.remotes[serverID]; ok {
		return r, nil
	}

	path := a.remotePath(serverID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}

	store, err := sqlitestore.New(path)
	if err != nil {
		return nil, err
	}
	db, err := openDB(path, remoteMigrations, "remotemigrations")
	if err != nil {
		store.Close()
		return nil, err
	}

	r := &remote{
		path:   path,
		store:  store,
		db:     db,
		q:      remotedb.New(db),
		client: client.New(serverURL, store, client.WithHTTPClient(a.http)),
	}
	a.remotes[serverID] = r
	return r, nil
}

// closeRemote closes a server's database and, if remove is set, deletes
// its file (and WAL side files). The pulled corpus is a cache, so this
// is how a server is reset or forgotten.
func (a *App) closeRemote(serverID int64, remove bool) error {
	a.remotesMu.Lock()
	defer a.remotesMu.Unlock()

	if r, ok := a.remotes[serverID]; ok {
		r.db.Close()
		r.store.Close()
		delete(a.remotes, serverID)
	}

	if remove {
		path := a.remotePath(serverID)
		for _, p := range []string{path, path + "-wal", path + "-shm"} {
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}

func (a *App) closeRemotes() {
	a.remotesMu.Lock()
	ids := make([]int64, 0, len(a.remotes))
	for id := range a.remotes {
		ids = append(ids, id)
	}
	a.remotesMu.Unlock()

	for _, id := range ids {
		a.closeRemote(id, false)
	}
}

// reindex rebuilds the review index for the given users from the
// corpus; with all set, it rebuilds it for every user.
func (r *remote) reindex(ctx context.Context, userIDs []string, all bool) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := r.q.WithTx(tx)

	index := func(doc *client.Document) error {
		if err := q.DeleteIndexedUser(ctx, doc.UserID); err != nil {
			return err
		}
		return indexDocument(ctx, q, doc)
	}

	if all {
		if _, err := tx.ExecContext(ctx, "DELETE FROM idx_users"); err != nil {
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

// indexDocument records one verified document's titles and reviews.
// Lines that don't parse as titles are skipped rather than failing the
// whole index: other people's documents may use fields this client
// doesn't know.
func indexDocument(ctx context.Context, q *remotedb.Queries, doc *client.Document) error {
	var titles []revs.Title2
	lines := bytes.Split(doc.Data, []byte("\n"))
	for _, line := range lines[min(1, len(lines)):] { // skip the control line
		if len(line) == 0 || gjson.GetBytes(line, "id").Exists() {
			continue
		}
		var t revs.Title2
		if err := json.Unmarshal(line, &t); err != nil || t.Name == "" {
			continue
		}
		if t.Type != nil && *t.Type != "" && !strings.EqualFold(*t.Type, titleTypeMovie) {
			continue
		}
		titles = append(titles, t)
	}

	err := q.InsertIndexedUser(ctx, remotedb.InsertIndexedUserParams{
		Userid: doc.UserID, Rev: int64(doc.Rev), Hash: doc.Hash, TitleCount: int64(len(titles)),
	})
	if err != nil {
		return err
	}

	for _, t := range titles {
		p := remotedb.InsertIndexedTitleParams{Userid: doc.UserID, Name: t.Name}
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
		for _, rv := range t.Reviews {
			rp := remotedb.InsertIndexedReviewParams{TitleID: titleID, ViewedOn: rv.Date}
			if rv.Rating != nil {
				n := int64(*rv.Rating)
				rp.Rating = &n
			}
			if rv.Comment != nil {
				rp.Comment = *rv.Comment
			}
			if err := q.InsertIndexedReview(ctx, rp); err != nil {
				return err
			}
		}
	}
	return nil
}

// pull syncs a server's corpus and refreshes the review index.
func (a *App) pull(ctx context.Context, serverID int64, serverURL string, rebootstrap bool) (*client.Result, error) {
	r, err := a.remote(serverID, serverURL)
	if err != nil {
		return nil, err
	}

	var res *client.Result
	if rebootstrap {
		res, err = r.client.Rebootstrap(ctx)
	} else {
		res, err = r.client.Sync(ctx)
	}
	if err != nil {
		return nil, err
	}

	n, err := r.q.CountIndexedUsers(ctx)
	if err != nil {
		return nil, err
	}
	if err := r.reindex(ctx, res.Changed, res.Bootstrapped || n == 0); err != nil {
		return nil, fmt.Errorf("indexing pulled documents: %w", err)
	}

	return res, nil
}
