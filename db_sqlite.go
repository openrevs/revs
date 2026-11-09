package revs

import (
	"database/sql"
	"embed"
	"os"
	"path/filepath"
	"sync"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
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

	db, err := sql.Open("sqlite", "file:"+abs+"?cache=shared")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA foreign_keys = ON;"); err != nil {
		return nil, err
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL;"); err != nil {
		return nil, err
	}

	// Migrate
	goose.SetBaseFS(migrations)

	if err := goose.SetDialect("sqlite"); err != nil {
		return nil, err
	}

	if err := goose.Up(db, "migrations2"); err != nil {
		return nil, err
	}

	openDBs[abs] = &DB{db}

	return openDBs[abs], nil
}
