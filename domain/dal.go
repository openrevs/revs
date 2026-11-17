package domain

import (
	"database/sql"

	"github.com/openrevs/revs/sqlc"
)

type DAL struct {
	q  *sqlc.Queries
	db *sql.DB
}

func NewDAL(db *sql.DB) DAL {
	return DAL{q: sqlc.New(db), db: db}
}
