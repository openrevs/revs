package dmrs

import (
	"database/sql"
)

// openDBs caches database handles by absolute path.
var openDBs = map[string]*DB{}

type DB struct {
	*sql.DB
}
