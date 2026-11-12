// Package sqlitestore keeps client corpora in a single SQLite database.
//
// One database holds any number of feeds: the corpus and sync state
// pulled from one server is one feed, and DB.Feed returns the
// client.Store for it. Documents are stored once however many feeds
// hold them, and line-deduplicated (see lines.go), so successive
// revisions of a document, and the same document pulled from several
// servers, share storage. A DB is also a client.HistoryStore, so an
// author's version history can live in the same file.
//
// A DB can own its database file (Open), or add its tables to a
// database the application already uses (New), so an application can
// keep everything in one file.
//
// It lives in its own package so that applications using other stores
// don't inherit the sqlite driver dependency.
package sqlitestore

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/openrevs/revs/client"
	_ "modernc.org/sqlite"
)

// The tables are prefixed with corpus_ and history so they can share a
// database with an application's own.
const schema = `
CREATE TABLE IF NOT EXISTS corpus_lines (
	id   INTEGER PRIMARY KEY,
	hash BLOB NOT NULL UNIQUE, -- sha256 of data
	data BLOB NOT NULL
);
CREATE TABLE IF NOT EXISTS corpus_docs (
	hash     TEXT PRIMARY KEY,
	line_ids BLOB NOT NULL -- uvarint corpus_lines ids, in order
);

-- A feed's sync state; a feed without a row has never saved state.
CREATE TABLE IF NOT EXISTS corpus_feeds (
	feed          TEXT PRIMARY KEY,
	epoch         INTEGER NOT NULL,
	manifest_hash TEXT NOT NULL,
	server_key    TEXT NOT NULL
);
-- Which feeds hold which documents, so removing a feed can drop the
-- documents no other feed holds.
CREATE TABLE IF NOT EXISTS corpus_feed_docs (
	feed TEXT NOT NULL,
	hash TEXT NOT NULL,
	PRIMARY KEY (feed, hash)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS corpus_feed_docs_hash ON corpus_feed_docs (hash);
CREATE TABLE IF NOT EXISTS corpus_heads (
	feed   TEXT NOT NULL,
	userid TEXT NOT NULL,
	rev    INTEGER NOT NULL,
	hash   TEXT NOT NULL,
	PRIMARY KEY (feed, userid)
);
CREATE TABLE IF NOT EXISTS corpus_push (
	feed       TEXT NOT NULL,
	userid     TEXT NOT NULL,
	rev        INTEGER NOT NULL,
	hash       TEXT NOT NULL,
	server_rev INTEGER NOT NULL,
	PRIMARY KEY (feed, userid)
);

CREATE TABLE IF NOT EXISTS history (
	userid      TEXT PRIMARY KEY,
	foreign_rev INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS history_versions (
	userid TEXT NOT NULL,
	seq    INTEGER NOT NULL,
	rev    INTEGER NOT NULL,
	hash   TEXT NOT NULL,
	data   BLOB NOT NULL,
	PRIMARY KEY (userid, seq)
);
`

// DB is a SQLite database of feeds and version histories.
type DB struct {
	db  *sql.DB
	own bool
}

// Open opens (creating if needed) the database at path.
func Open(path string) (*DB, error) {
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		return nil, err
	}

	// The client serializes mutations but reads concurrently; a single
	// connection sidesteps SQLITE_BUSY behavior entirely. Pragmas are
	// set via Exec (not DSN params) to stay driver-portable.
	db.SetMaxOpenConns(1)

	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("sqlitestore: %s: %w", pragma, err)
		}
	}

	d, err := New(db)
	if err != nil {
		db.Close()
		return nil, err
	}
	d.own = true
	return d, nil
}

// New adds the store's tables to db, if they aren't there yet, and
// returns a DB using it. The caller remains responsible for closing db.
func New(db *sql.DB) (*DB, error) {
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("sqlitestore: creating schema: %w", err)
	}
	return &DB{db: db}, nil
}

// Close closes the database if Open opened it.
func (d *DB) Close() error {
	if !d.own {
		return nil
	}
	return d.db.Close()
}

// Feed returns the store for the named feed, e.g. one per server.
func (d *DB) Feed(name string) *Store {
	return &Store{d: d, feed: name}
}

// DeleteFeed removes a feed's sync state and the documents (and lines)
// that no other feed holds.
func (d *DB) DeleteFeed(name string) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, table := range []string{"corpus_feeds", "corpus_heads", "corpus_push", "corpus_feed_docs"} {
		if _, err := tx.Exec(`DELETE FROM `+table+` WHERE feed = ?`, name); err != nil {
			return err
		}
	}
	res, err := tx.Exec(`DELETE FROM corpus_docs WHERE hash NOT IN (SELECT hash FROM corpus_feed_docs)`)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n > 0 {
		if err := deleteUnusedLines(tx); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// Store is one feed's client.Store.
type Store struct {
	d    *DB
	feed string
}

var _ client.Store = (*Store)(nil)

func (s *Store) PutBlob(hash string, data []byte) error {
	if len(hash) < 2 || !isLowerHex(hash) {
		return fmt.Errorf("sqlitestore: invalid blob hash %q", hash)
	}

	tx, err := s.d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Content-addressed: present means identical, and blobs are never
	// deleted while a feed holds them (superseded revisions are
	// retained history).
	if err := putBlob(tx, hash, data); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO corpus_feed_docs (feed, hash) VALUES (?, ?)`, s.feed, hash); err != nil {
		return err
	}

	return tx.Commit()
}

// GetBlob returns a stored blob. Blobs are shared between feeds, so it
// may return one that another feed stored.
func (s *Store) GetBlob(hash string) ([]byte, error) {
	data, err := getBlob(s.d.db, hash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("sqlitestore: no blob %s", hash)
	}
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: blob %s: %w", hash, err)
	}

	return data, nil
}

func (s *Store) LoadState() (*client.State, error) {
	tx, err := s.d.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	state := &client.State{
		Heads: map[string]client.Head{},
		Push:  map[string]client.PushState{},
	}
	err = tx.QueryRow(`SELECT epoch, manifest_hash, server_key FROM corpus_feeds WHERE feed = ?`, s.feed).
		Scan(&state.Epoch, &state.ManifestHash, &state.ServerKey)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil // no state saved yet
	}
	if err != nil {
		return nil, err
	}

	rows, err := tx.Query(`SELECT userid, rev, hash FROM corpus_heads WHERE feed = ?`, s.feed)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var h client.Head
		if err := rows.Scan(&id, &h.Rev, &h.Hash); err != nil {
			return nil, err
		}
		state.Heads[id] = h
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = tx.Query(`SELECT userid, rev, hash, server_rev FROM corpus_push WHERE feed = ?`, s.feed)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var p client.PushState
		if err := rows.Scan(&id, &p.Rev, &p.Hash, &p.ServerRev); err != nil {
			return nil, err
		}
		state.Push[id] = p
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return state, nil
}

// SaveState replaces the feed's whole persisted state in one
// transaction, keeping epoch, chain hash, and heads consistent as the
// Store contract requires.
func (s *Store) SaveState(state *client.State) error {
	tx, err := s.d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`INSERT INTO corpus_feeds (feed, epoch, manifest_hash, server_key) VALUES (?, ?, ?, ?)
		ON CONFLICT (feed) DO UPDATE SET epoch = excluded.epoch,
			manifest_hash = excluded.manifest_hash, server_key = excluded.server_key`,
		s.feed, state.Epoch, state.ManifestHash, state.ServerKey); err != nil {
		return err
	}

	for _, table := range []string{"corpus_heads", "corpus_push"} {
		if _, err := tx.Exec(`DELETE FROM `+table+` WHERE feed = ?`, s.feed); err != nil {
			return err
		}
	}
	for id, h := range state.Heads {
		if _, err := tx.Exec(`INSERT INTO corpus_heads (feed, userid, rev, hash) VALUES (?, ?, ?, ?)`,
			s.feed, id, h.Rev, h.Hash); err != nil {
			return err
		}
	}
	for id, p := range state.Push {
		if _, err := tx.Exec(`INSERT INTO corpus_push (feed, userid, rev, hash, server_rev) VALUES (?, ?, ?, ?, ?)`,
			s.feed, id, p.Rev, p.Hash, p.ServerRev); err != nil {
			return err
		}
	}

	return tx.Commit()
}

var (
	_ client.HistoryStore = (*DB)(nil)
	_ client.HistoryStore = (*Store)(nil)
)

// A feed's store is a HistoryStore too, so a Client given one finds the
// author's history. History is the author's, not per feed: every feed
// of a DB shares it.

func (s *Store) LoadHistory(userID string) (*client.VersionHistory, error) {
	return s.d.LoadHistory(userID)
}

func (s *Store) SaveHistory(userID string, h *client.VersionHistory) error {
	return s.d.SaveHistory(userID, h)
}

func (d *DB) LoadHistory(userID string) (*client.VersionHistory, error) {
	tx, err := d.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	h := &client.VersionHistory{}
	err = tx.QueryRow(`SELECT foreign_rev FROM history WHERE userid = ?`, userID).Scan(&h.ForeignRev)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	rows, err := tx.Query(`SELECT rev, hash, data FROM history_versions WHERE userid = ? ORDER BY seq`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v client.StoredVersion
		if err := rows.Scan(&v.Rev, &v.Hash, &v.Data); err != nil {
			return nil, err
		}
		h.Versions = append(h.Versions, v)
	}

	return h, rows.Err()
}

// SaveHistory replaces a user's history in one transaction.
func (d *DB) SaveHistory(userID string, h *client.VersionHistory) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`INSERT INTO history (userid, foreign_rev) VALUES (?, ?)
		ON CONFLICT (userid) DO UPDATE SET foreign_rev = excluded.foreign_rev`, userID, h.ForeignRev); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM history_versions WHERE userid = ?`, userID); err != nil {
		return err
	}
	for i, v := range h.Versions {
		if _, err := tx.Exec(`INSERT INTO history_versions (userid, seq, rev, hash, data) VALUES (?, ?, ?, ?, ?)`,
			userID, i, v.Rev, v.Hash, v.Data); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func isLowerHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
