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
// lines; each distinct line is stored once in corpus_lines, and a blob
// is a corpus_docs row holding the packed list of its line ids. Joining the lines
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

// blobWriter stores blobs in a transaction, with its statements
// prepared once for however many blobs it stores.
type blobWriter struct {
	exists, lookup, insertLine, insertDoc *sql.Stmt
}

func newBlobWriter(tx *sql.Tx) (*blobWriter, error) {
	w := &blobWriter{}
	for _, p := range []struct {
		stmt  **sql.Stmt
		query string
	}{
		{&w.exists, `SELECT 1 FROM corpus_docs WHERE hash = ?`},
		{&w.lookup, `SELECT id FROM corpus_lines WHERE hash = ?`},
		{&w.insertLine, `INSERT INTO corpus_lines (hash, data) VALUES (?, ?)`},
		{&w.insertDoc, `INSERT INTO corpus_docs (hash, line_ids) VALUES (?, ?)`},
	} {
		stmt, err := tx.Prepare(p.query)
		if err != nil {
			w.close()
			return nil, err
		}
		*p.stmt = stmt
	}
	return w, nil
}

func (w *blobWriter) close() {
	for _, s := range []*sql.Stmt{w.exists, w.lookup, w.insertLine, w.insertDoc} {
		if s != nil {
			s.Close()
		}
	}
}

// put stores data under hash, adding any lines not already stored. A
// blob already present is left as is (content-addressed: present means
// identical).
func (w *blobWriter) put(hash string, data []byte) error {
	var exists int
	err := w.exists.QueryRow(hash).Scan(&exists)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	var ids []byte
	for line := range bytes.SplitSeq(data, []byte("\n")) {
		if line == nil {
			line = []byte{} // an empty line, not NULL
		}
		sum := sha256.Sum256(line)

		var id int64
		err := w.lookup.QueryRow(sum[:]).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			res, err := w.insertLine.Exec(sum[:], line)
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

	_, err = w.insertDoc.Exec(hash, ids)
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
	if err := tx.QueryRow(`SELECT line_ids FROM corpus_docs WHERE hash = ?`, hash).Scan(&packed); err != nil {
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

	rows, err := tx.Query(`SELECT id, data FROM corpus_lines WHERE id IN (SELECT value FROM json_each(?))`, list.String())
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

// deleteUnusedLines deletes the lines no document uses. Line lists are
// packed, so this reads every document; it runs only when documents are
// deleted, which is rare.
func deleteUnusedLines(tx *sql.Tx) error {
	used := map[uint64]bool{}
	rows, err := tx.Query(`SELECT line_ids FROM corpus_docs`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var packed []byte
		if err := rows.Scan(&packed); err != nil {
			return err
		}
		ids, err := unpackIDs(packed)
		if err != nil {
			return err
		}
		for _, id := range ids {
			used[id] = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	rows, err = tx.Query(`SELECT id FROM corpus_lines`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var unused []uint64
	for rows.Next() {
		var id uint64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		if !used[id] {
			unused = append(unused, id)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	stmt, err := tx.Prepare(`DELETE FROM corpus_lines WHERE id = ?`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, id := range unused {
		if _, err := stmt.Exec(id); err != nil {
			return err
		}
	}
	return nil
}
