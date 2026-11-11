package sqlitestore

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Blobs are stored line-deduplicated. A blob is split on '\n' into
// lines; each distinct line is stored once in the lines table, and a
// blob is stored as the packed list of its line ids. Joining the lines
// with '\n' rebuilds the blob byte for byte, whatever it contains.
//
// sjsonl documents are canonical with one record per line, so a new
// revision of a document only adds the lines that changed (plus its
// header and signature lines), and the same document pulled from
// several servers shares every line.
//
// line_ids is a sequence of uvarints rather than one row per line: at a
// few bytes per line, the pointers stay small next to the lines
// themselves, which a (doc, pos, line) table would not.

const linesSchema = `
CREATE TABLE IF NOT EXISTS lines (
	id INTEGER PRIMARY KEY,
	hash BLOB NOT NULL UNIQUE,
	data BLOB NOT NULL
);
CREATE TABLE IF NOT EXISTS docs (
	hash TEXT PRIMARY KEY,
	line_ids BLOB NOT NULL
);
`

// putBlob stores data under hash, adding any lines not already stored.
// A blob already present is left as is (content-addressed: present
// means identical).
func putBlob(tx *sql.Tx, hash string, data []byte) error {
	var exists int
	err := tx.QueryRow(`SELECT 1 FROM docs WHERE hash = ?`, hash).Scan(&exists)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	lookup, err := tx.Prepare(`SELECT id FROM lines WHERE hash = ?`)
	if err != nil {
		return err
	}
	defer lookup.Close()
	insert, err := tx.Prepare(`INSERT INTO lines (hash, data) VALUES (?, ?)`)
	if err != nil {
		return err
	}
	defer insert.Close()

	var ids []byte
	for line := range bytes.SplitSeq(data, []byte("\n")) {
		if line == nil {
			line = []byte{} // an empty line, not NULL
		}
		sum := sha256.Sum256(line)

		var id int64
		err := lookup.QueryRow(sum[:]).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			res, err := insert.Exec(sum[:], line)
			if err != nil {
				return err
			}
			if id, err = res.LastInsertId(); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		ids = binary.AppendUvarint(ids, uint64(id))
	}

	_, err = tx.Exec(`INSERT INTO docs (hash, line_ids) VALUES (?, ?)`, hash, ids)
	return err
}

// getBlob rebuilds the blob stored under hash, or returns sql.ErrNoRows.
func getBlob(db *sql.DB, hash string) ([]byte, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var packed []byte
	if err := tx.QueryRow(`SELECT line_ids FROM docs WHERE hash = ?`, hash).Scan(&packed); err != nil {
		return nil, err
	}

	ids, err := unpackIDs(packed)
	if err != nil {
		return nil, fmt.Errorf("corrupt line list for %s: %w", hash, err)
	}

	// Fetch every distinct line in one query.
	var list strings.Builder
	list.WriteByte('[')
	seen := make(map[uint64][]byte, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		if len(seen) > 0 {
			list.WriteByte(',')
		}
		seen[id] = nil
		list.WriteString(strconv.FormatUint(id, 10))
	}
	list.WriteByte(']')

	rows, err := tx.Query(`SELECT id, data FROM lines WHERE id IN (SELECT value FROM json_each(?))`, list.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	found := 0
	for rows.Next() {
		var id uint64
		var line []byte
		if err := rows.Scan(&id, &line); err != nil {
			return nil, err
		}
		if line == nil {
			line = []byte{} // empty, but distinguishable from not found
		}
		seen[id] = line
		found++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if found != len(seen) {
		return nil, fmt.Errorf("%s references %d missing lines", hash, len(seen)-found)
	}

	size := len(ids) - 1
	for _, id := range ids {
		size += len(seen[id])
	}
	out := make([]byte, 0, size)
	for i, id := range ids {
		if i > 0 {
			out = append(out, '\n')
		}
		out = append(out, seen[id]...)
	}

	return out, nil
}

func unpackIDs(packed []byte) ([]uint64, error) {
	var ids []uint64
	for len(packed) > 0 {
		id, n := binary.Uvarint(packed)
		if n <= 0 {
			return nil, errors.New("bad uvarint")
		}
		ids = append(ids, id)
		packed = packed[n:]
	}
	if len(ids) == 0 {
		return nil, errors.New("empty")
	}
	return ids, nil
}

// migrateBlobs moves blobs from the old whole-blob table into the line
// store and drops it. It reports whether there was anything to migrate.
func migrateBlobs(db *sql.DB) (bool, error) {
	var name string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'blobs'`).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	tx, err := db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	rows, err := tx.Query(`SELECT hash, data FROM blobs`)
	if err != nil {
		return false, err
	}
	type blob struct {
		hash string
		data []byte
	}
	var blobs []blob
	for rows.Next() {
		var b blob
		if err := rows.Scan(&b.hash, &b.data); err != nil {
			rows.Close()
			return false, err
		}
		blobs = append(blobs, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}

	for _, b := range blobs {
		if err := putBlob(tx, b.hash, b.data); err != nil {
			return false, fmt.Errorf("migrating blob %s: %w", b.hash, err)
		}
	}
	if _, err := tx.Exec(`DROP TABLE blobs`); err != nil {
		return false, err
	}

	return true, tx.Commit()
}
