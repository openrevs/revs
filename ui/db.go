package ui

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"net/url"
	"sync"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var localMigrations embed.FS

//go:embed remotemigrations/*.sql
var remoteMigrations embed.FS

// gooseMu serializes migrations: goose keeps its base FS and dialect in
// package globals, and remote databases are opened lazily from handlers.
var gooseMu sync.Mutex

// openDB opens a SQLite database with foreign keys enforced, WAL
// journaling, and a busy timeout, then applies the migrations found in
// dir of migrations.
func openDB(path string, migrations fs.FS, dir string) (*sql.DB, error) {
	q := url.Values{}
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")

	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}

	if err := migrate(db, migrations, dir); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrating %s: %w", path, err)
	}

	return db, nil
}

func migrate(db *sql.DB, migrations fs.FS, dir string) error {
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
