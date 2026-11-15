package collection

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"

	"github.com/openrevs/revs/internal/sqlitedb"
)

//go:embed migrations/*.sql
var localMigrations embed.FS

// openDB opens a SQLite database file with foreign keys enforced, WAL
// journaling, and a busy timeout, then applies the migrations found in
// dir of migrations.
func openDB(path string, migrations fs.FS, dir string) (*sql.DB, error) {
	db, err := sqlitedb.Open(sqlitedb.FileDSN(path), migrations, dir)
	if err != nil {
		return nil, fmt.Errorf("migrating %s: %w", path, err)
	}
	return db, nil
}
