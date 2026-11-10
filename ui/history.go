package ui

import (
	"database/sql"

	"github.com/openrevs/revs/client"
)

// historyStore is the client.HistoryStore for the author's version
// history, kept in the local database alongside the collection it
// versions.
type historyStore struct {
	db *sql.DB
}

func (s historyStore) LoadHistory(userID string) (*client.VersionHistory, error) {
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

func (s historyStore) SaveHistory(userID string, h *client.VersionHistory) error {
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
