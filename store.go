package revs

import (
	"database/sql"
	"os"
	"path/filepath"

	"github.com/openrevs/revs/client/sqlitestore"
)

// BlobStore is minimal content-addressed storage. Keys are sjsonl doc
// hashes, so blobs are immutable and identical uploads dedup for free.
type BlobStore interface {
	Put(hash string, data []byte) error
	Get(hash string) ([]byte, error)
}

// SQLiteStore keeps blobs line-deduplicated in the server's database,
// using sqlitestore's tables as a single "server" feed. Successive
// revisions of a ratings document share every unchanged line, and a
// collection keeping its corpora in the same file shares storage too.
type SQLiteStore struct {
	corpus *sqlitestore.DB
	feed   *sqlitestore.Store
}

// serverFeed is the sqlitestore feed holding the server's documents.
// Being a feed keeps them from looking unreferenced to a client's
// DeleteFeed should the database be shared.
const serverFeed = "server"

func NewSQLiteStore(db *sql.DB) (*SQLiteStore, error) {
	d, err := sqlitestore.New(db)
	if err != nil {
		return nil, err
	}

	return &SQLiteStore{corpus: d, feed: d.Feed(serverFeed)}, nil
}

// upstreamFeed returns the sqlitestore feed holding what was pulled from
// an upstream server. Its documents share storage with the server's own.
func (s *SQLiteStore) upstreamFeed(url string) *sqlitestore.Store {
	return s.corpus.Feed("upstream:" + url)
}

func (s *SQLiteStore) Put(hash string, data []byte) error {
	return s.feed.PutBlob(hash, data)
}

func (s *SQLiteStore) Get(hash string) ([]byte, error) {
	return s.feed.GetBlob(hash)
}

// writeFileAtomic writes data to a temp file in the target directory and
// renames it into place, so partially written files are never visible.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	return os.Rename(tmp.Name(), path)
}
