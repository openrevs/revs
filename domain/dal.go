package domain

import (
	"database/sql"

	"github.com/kalafut/dmrs/sqlc"
)

type DAL struct {
	q  *sqlc.Queries
	db *sql.DB
}

func NewDAL(db *sql.DB) DAL {
	return DAL{q: sqlc.New(db), db: db}
}

func userFromSqlc(u sqlc.User) *User {
	return &User{
		ID:          u.UserID,
		Login:       u.Login,
		Password:    u.Password,
		Email:       u.Email,
		Userid:      u.Userid,
		Publickey:   u.Publickey,
		Privatekey:  u.Privatekey,
		Minrating:   u.Minrating,
		Maxrating:   u.Maxrating,
		Revision:    u.Revision,
		ShowPosters: u.Showposters,
	}
}

func titleFromSqlc(t sqlc.Title) *Title {
	return &Title{
		ID:            t.TitleID,
		Type:          t.Type,
		Name:          t.Name,
		Year:          t.Year,
		TMDBID:        t.TmdbID,
		TMDBPosterURL: t.TmdbPosterUrl,
	}
}

// ratingFromRow converts a joined rating+title row. The generated row types
// for UserRatings, TitleRatings, and Rating are structurally identical, so
// callers convert to RatingRow first.
func ratingFromRow(r sqlc.RatingRow) *Rating {
	id := r.RatingID
	return &Rating{
		ID:      &id,
		TitleID: r.TitleID,
		UserID:  r.UserID,
		Rating:  r.Rating,
		Comment: r.Comment,
		Date:    r.Date,
		Updated: r.Updated,
		Title: Title{
			ID:            r.TitleID,
			Type:          r.Type,
			Name:          r.Name,
			Year:          r.Year,
			TMDBID:        r.TmdbID,
			TMDBPosterURL: r.TmdbPosterUrl,
		},
	}
}
