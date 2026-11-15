package revs

import (
	"database/sql"
	"embed"
	"os"
	"path/filepath"
	"sync"

	"github.com/openrevs/revs/internal/sqlitedb"
)

const dbfilename = "revs.db"

var (
	//go:embed migrations2/*.sql
	migrations embed.FS
	mu         sync.Mutex
)

// GetDB opens (or returns the already-open) database at dir/revs.db and
// runs migrations. Callers using the same directory share one handle, so
// client and server can run in one process against one database.
func GetDB(dir string) (*DB, error) {
	mu.Lock()
	defer mu.Unlock()

	abs, err := filepath.Abs(filepath.Join(dir, dbfilename))
	if err != nil {
		return nil, err
	}

	if db, ok := openDBs[abs]; ok {
		return db, nil
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}

	db, err := openDB(sqlitedb.FileDSN(abs))
	if err != nil {
		return nil, err
	}

	openDBs[abs] = db

	return db, nil
}

// OpenMemoryDB opens a new, private, migrated in-memory database. It
// is not cached: every call returns a different database, which is gone
// once the handle is closed. Tests use it to run any number of servers
// in one process without touching the filesystem.
func OpenMemoryDB() (*DB, error) {
	return openDB(sqlitedb.MemoryDSN())
}

func openDB(dsn string) (*DB, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)

	if err := sqlitedb.Migrate(db, migrations, "migrations2"); err != nil {
		db.Close()
		return nil, err
	}

	return &DB{db}, nil
}
