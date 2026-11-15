package collection

import "database/sql"

// DB exposes a collection's database to external tests.
func DB(c *Collection) *sql.DB { return c.db }
