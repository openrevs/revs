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
	sdb, err := sqlitestore.Open(filepath.Join(t.TempDir(), "corpus.db"))
	require.NoError(t, err)
	defer sdb.Close()
	store := sdb.Feed("s")

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
	sdb, err := sqlitestore.Open(path)
	require.NoError(t, err)
	defer sdb.Close()
	store := sdb.Feed("s")

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
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM corpus_lines`).Scan(&lines))
	// The titles, plus per revision a header, a signature, and an
	// edited title (revision 1's edit replaces the original), plus the
	// empty line after the trailing newline, shared by all.
	require.Equal(t, titles+3*revs, lines)

	var full, stored int64
	for _, d := range docs {
		full += int64(len(d))
	}
	require.NoError(t, db.QueryRow(`SELECT (SELECT sum(length(data)) FROM corpus_lines) + (SELECT sum(length(line_ids)) FROM corpus_docs)`).Scan(&stored))
	t.Logf("%d revisions: %d bytes as whole blobs, %d bytes line-deduplicated (%.1fx)",
		revs, full, stored, float64(full)/float64(stored))
	require.Less(t, stored*10, full)
}

// A missing line is reported, not silently rebuilt into a short blob.
func TestMissingLineIsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corpus.db")
	sdb, err := sqlitestore.Open(path)
	require.NoError(t, err)
	defer sdb.Close()
	store := sdb.Feed("s")
	require.NoError(t, store.PutBlob("aa", []byte("one\ntwo")))

	db, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec(`DELETE FROM corpus_lines WHERE data = CAST('two' AS BLOB)`)
	require.NoError(t, err)

	_, err = store.GetBlob("aa")
	require.ErrorContains(t, err, "missing")
}

// Feeds keep separate sync state but share documents; deleting a feed
// drops only what no other feed holds.
func TestFeeds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corpus.db")
	sdb, err := sqlitestore.Open(path)
	require.NoError(t, err)
	defer sdb.Close()
	a, b := sdb.Feed("a"), sdb.Feed("b")

	require.NoError(t, a.UpdateState(client.StateChange{Epoch: 1, Heads: map[string]*client.Head{"u": {Rev: 1, Hash: "aa"}}}))
	st, err := b.LoadState()
	require.NoError(t, err)
	require.Nil(t, st)

	shared := []byte("header\nshared line\nsig")
	require.NoError(t, a.PutBlob("aa", shared))
	require.NoError(t, b.PutBlob("aa", shared))
	require.NoError(t, a.PutBlob("bb", []byte("header2\nshared line\nonly in a")))

	db, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err)
	defer db.Close()
	count := func(table string) int {
		var n int
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM `+table).Scan(&n))
		return n
	}
	require.Equal(t, 2, count("corpus_docs"))
	require.Equal(t, 5, count("corpus_lines"))

	require.NoError(t, sdb.DeleteFeed("a"))

	st, err = a.LoadState()
	require.NoError(t, err)
	require.Nil(t, st)

	// b's document and its lines survive; a's own are gone.
	got, err := b.GetBlob("aa")
	require.NoError(t, err)
	require.Equal(t, shared, got)
	_, err = b.GetBlob("bb")
	require.Error(t, err)
	require.Equal(t, 1, count("corpus_docs"))
	require.Equal(t, 3, count("corpus_lines"))

	require.NoError(t, sdb.DeleteFeed("b"))
	require.Equal(t, 0, count("corpus_docs"))
	require.Equal(t, 0, count("corpus_lines"))
}
