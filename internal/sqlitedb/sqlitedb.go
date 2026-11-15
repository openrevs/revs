// Package sqlitedb holds the SQLite plumbing shared by the server and
// collection databases: opening file-backed or in-memory databases and
// applying goose migrations.
package sqlitedb

import (
	"database/sql"
	"fmt"
	"io/fs"
	"net/url"
	"sync"
	"sync/atomic"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

// pragmas apply to every connection: foreign keys enforced, a busy
// timeout so concurrent writers wait instead of failing, and WAL
// journaling (in-memory databases silently keep their memory journal).
func pragmas() url.Values {
	q := url.Values{}
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	return q
}

// FileDSN is the data source name for the database file at path.
func FileDSN(path string) string {
	return "file:" + path + "?" + pragmas().Encode()
}

var memSeq atomic.Int64

// MemoryDSN returns the data source name of a new, empty in-memory
// database. Every connection opened with it (from one or several
// sql.DB handles) sees the same database, which lives until the last
// connection to it closes. Each call names a different database.
func MemoryDSN() string {
	q := pragmas()
	q.Set("vfs", "memdb")
	return fmt.Sprintf("file:/revs-mem-%d?%s", memSeq.Add(1), q.Encode())
}

// Open opens the database at dsn and applies the migrations in dir of
// migrations.
func Open(dsn string, migrations fs.FS, dir string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}

	if err := Migrate(db, migrations, dir); err != nil {
		db.Close()
		return nil, err
	}

	return db, nil
}

// gooseMu serializes migrations: goose keeps its base FS and dialect in
// package globals, so concurrent migrations (say, several in-memory
// servers and collections created by one test) would otherwise race.
var gooseMu sync.Mutex

// Migrate applies the migrations found in dir of migrations.
func Migrate(db *sql.DB, migrations fs.FS, dir string) error {
	gooseMu.Lock()
	defer gooseMu.Unlock()

	goose.SetLogger(goose.NopLogger())
	goose.SetBaseFS(migrations)
	defer goose.SetBaseFS(nil)

	if err := goose.SetDialect("sqlite3"); err != nil {
		return err
	}

	return goose.Up(db, dir)
}
