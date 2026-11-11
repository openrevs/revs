package sqlitestore_test

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openrevs/revs/client"
	"github.com/openrevs/revs/client/sqlitestore"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// Blobs are rebuilt byte for byte, whatever their line structure.
func TestBlobRoundTripExact(t *testing.T) {
	store, err := sqlitestore.New(filepath.Join(t.TempDir(), "corpus.db"))
	require.NoError(t, err)
	defer store.Close()

	cases := map[string][]byte{
		"empty":            {},
		"newline":          []byte("\n"),
		"no trailing":      []byte("a\nb"),
		"trailing":         []byte("a\nb\n"),
		"blank lines":      []byte("\n\na\n\n\nb\n\n"),
		"repeated lines":   []byte("x\nx\nx\ny\nx\n"),
		"crlf":             []byte("a\r\nb\r\n"),
		"binary":           {0, 1, '\n', 0xff, 0, '\n', 0},
		"single long line": []byte(strings.Repeat("z", 100_000)),
	}

	i := 0
	for name, data := range cases {
		hash := fmt.Sprintf("%04x", i)
		i++
		require.NoError(t, store.PutBlob(hash, data), name)

		got, err := store.GetBlob(hash)
		require.NoError(t, err, name)
		require.Equal(t, data, got, name)
	}
}

// Successive revisions of a document share their unchanged lines.
func TestBlobRevisionsShareLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corpus.db")
	store, err := sqlitestore.New(path)
	require.NoError(t, err)
	defer store.Close()

	const titles = 1000
	docs := map[string][]byte{}
	body := make([]string, titles)
	for i := range body {
		body[i] = fmt.Sprintf(`{"name":"Title %d","reviews":[{"comment":"%s","date":"2024-01-01","rating":3}],"year":1990}`,
			i, strings.Repeat("words ", 30))
	}

	const revs = 50
	for rev := 1; rev <= revs; rev++ {
		body[rev] = strings.Replace(body[rev], `"rating":3`, `"rating":4`, 1) // one edit per revision
		doc := fmt.Sprintf(`{"id":"u","rev":%d}`+"\n%s\n"+`{"sig":"%d","type":"sig"}`+"\n",
			rev, strings.Join(body, "\n"), rev)
		hash := fmt.Sprintf("%04x", rev)
		docs[hash] = []byte(doc)
		require.NoError(t, store.PutBlob(hash, []byte(doc)))
	}

	for hash, want := range docs {
		got, err := store.GetBlob(hash)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}

	db, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err)
	defer db.Close()

	var lines int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM lines`).Scan(&lines))
	// The titles, plus per revision a header, a signature, and an
	// edited title (revision 1's edit replaces the original), plus the
	// empty line after the trailing newline, shared by all.
	require.Equal(t, titles+3*revs, lines)

	var full, stored int64
	for _, d := range docs {
		full += int64(len(d))
	}
	require.NoError(t, db.QueryRow(`SELECT (SELECT sum(length(data)) FROM lines) + (SELECT sum(length(line_ids)) FROM docs)`).Scan(&stored))
	t.Logf("%d revisions: %d bytes as whole blobs, %d bytes line-deduplicated (%.1fx)",
		revs, full, stored, float64(full)/float64(stored))
	require.Less(t, stored*10, full)
}

// A database written with the old whole-blob table is migrated on open.
func TestMigrateWholeBlobs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corpus.db")

	db, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE blobs (hash TEXT PRIMARY KEY, data BLOB NOT NULL)`)
	require.NoError(t, err)
	old := map[string][]byte{
		"aa": []byte("h1\nshared\nsig1\n"),
		"bb": []byte("h2\nshared\nsig2\n"),
		"cc": {},
	}
	for h, d := range old {
		_, err = db.Exec(`INSERT INTO blobs (hash, data) VALUES (?, ?)`, h, d)
		require.NoError(t, err)
	}
	require.NoError(t, db.Close())

	store, err := sqlitestore.New(path)
	require.NoError(t, err)
	for h, want := range old {
		got, err := store.GetBlob(h)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
	require.NoError(t, store.Close())

	db, err = sql.Open("sqlite", "file:"+path)
	require.NoError(t, err)
	defer db.Close()
	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = 'blobs'`).Scan(&n))
	require.Zero(t, n)

	// Reopening a migrated file is a no-op.
	store, err = sqlitestore.New(path)
	require.NoError(t, err)
	require.NoError(t, store.Close())
}

// A missing line is reported, not silently rebuilt into a short blob.
func TestMissingLineIsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corpus.db")
	store, err := sqlitestore.New(path)
	require.NoError(t, err)
	defer store.Close()
	require.NoError(t, store.PutBlob("aa", []byte("one\ntwo")))

	db, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec(`DELETE FROM lines WHERE data = CAST('two' AS BLOB)`)
	require.NoError(t, err)

	_, err = store.GetBlob("aa")
	require.ErrorContains(t, err, "missing")
}

var _ client.Store = (*sqlitestore.Store)(nil)
