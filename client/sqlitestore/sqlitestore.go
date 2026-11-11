// Package sqlitestore is a client.Store backed by a single SQLite
// database file: blobs, heads, and sync state together, replacing
// FSStore's directory tree. Blobs are stored line-deduplicated (see
// lines.go), so successive revisions of a document share storage. It
// suits simple personal clients that want one corpus file they can
// inspect with the sqlite3 CLI.
//
// It lives in its own package so that applications using other stores
// don't inherit the sqlite driver dependency.
package sqlitestore

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/openrevs/revs/client"
	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS meta (
	k TEXT PRIMARY KEY,
	v TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS heads (
	userid TEXT PRIMARY KEY,
	rev INTEGER NOT NULL,
	hash TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS push (
	userid TEXT PRIMARY KEY,
	rev INTEGER NOT NULL,
	hash TEXT NOT NULL,
	server_rev INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS history (
	userid TEXT PRIMARY KEY,
	foreign_rev INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS history_versions (
	userid TEXT NOT NULL,
	seq INTEGER NOT NULL,
	rev INTEGER NOT NULL,
	hash TEXT NOT NULL,
	data BLOB NOT NULL,
	PRIMARY KEY (userid, seq)
);
`

type Store struct {
	db *sql.DB
}

// New opens (creating if needed) the database at path.
func New(path string) (*Store, error) {
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

	if _, err := db.Exec(schema + linesSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlitestore: creating schema: %w", err)
	}

	migrated, err := migrateBlobs(db)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlitestore: migrating blobs: %w", err)
	}
	if migrated {
		// Reclaim the space the whole blobs held.
		if _, err := db.Exec(`VACUUM`); err != nil {
			db.Close()
			return nil, fmt.Errorf("sqlitestore: vacuum: %w", err)
		}
	}

	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) PutBlob(hash string, data []byte) error {
	if len(hash) < 2 || !isLowerHex(hash) {
		return fmt.Errorf("sqlitestore: invalid blob hash %q", hash)
	}

	// Content-addressed: present means identical, and blobs are never
	// deleted (superseded revisions are retained history).
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := putBlob(tx, hash, data); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetBlob(hash string) ([]byte, error) {
	data, err := getBlob(s.db, hash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("sqlitestore: no blob %s", hash)
	}
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: blob %s: %w", hash, err)
	}

	return data, nil
}

func (s *Store) LoadState() (*client.State, error) {
	meta, err := s.readMeta()
	if err != nil {
		return nil, err
	}
	if meta == nil {
		return nil, nil // no state saved yet
	}

	state := &client.State{
		ManifestHash: meta["manifest_hash"],
		ServerKey:    meta["server_key"],
		Heads:        map[string]client.Head{},
		Push:         map[string]client.PushState{},
	}
	if state.Epoch, err = strconv.Atoi(meta["epoch"]); err != nil {
		return nil, fmt.Errorf("sqlitestore: corrupt epoch: %w", err)
	}

	rows, err := s.db.Query(`SELECT userid, rev, hash FROM heads`)
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

	rows, err = s.db.Query(`SELECT userid, rev, hash, server_rev FROM push`)
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

// SaveState replaces the whole persisted state in one transaction,
// keeping epoch, chain hash, and heads consistent as the Store contract
// requires.
func (s *Store) SaveState(state *client.State) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, table := range []string{"meta", "heads", "push"} {
		if _, err := tx.Exec(`DELETE FROM ` + table); err != nil {
			return err
		}
	}

	for k, v := range map[string]string{
		"epoch":         strconv.Itoa(state.Epoch),
		"manifest_hash": state.ManifestHash,
		"server_key":    state.ServerKey,
	} {
		if _, err := tx.Exec(`INSERT INTO meta (k, v) VALUES (?, ?)`, k, v); err != nil {
			return err
		}
	}

	for id, h := range state.Heads {
		if _, err := tx.Exec(`INSERT INTO heads (userid, rev, hash) VALUES (?, ?, ?)`,
			id, h.Rev, h.Hash); err != nil {
			return err
		}
	}

	for id, p := range state.Push {
		if _, err := tx.Exec(`INSERT INTO push (userid, rev, hash, server_rev) VALUES (?, ?, ?, ?)`,
			id, p.Rev, p.Hash, p.ServerRev); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *Store) LoadHistory(userID string) (*client.VersionHistory, error) {
	h := &client.VersionHistory{}
	err := s.db.QueryRow(`SELECT foreign_rev FROM history WHERE userid = ?`, userID).Scan(&h.ForeignRev)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	rows, err := s.db.Query(`SELECT rev, hash, data FROM history_versions WHERE userid = ? ORDER BY seq`, userID)
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
func (s *Store) SaveHistory(userID string, h *client.VersionHistory) error {
	tx, err := s.db.Begin()
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

// readMeta returns the meta table as a map, or nil if no state has ever
// been saved.
func (s *Store) readMeta() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT k, v FROM meta`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	meta := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		meta[k] = v
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(meta) == 0 {
		return nil, nil
	}
	return meta, nil
}

func isLowerHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
