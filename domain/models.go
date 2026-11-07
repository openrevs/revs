package domain

import (
	"errors"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// A note a ID columns. I found that sqlx gets confused when dealing
// with embedded structs that have the same field names if the DB column
// name is also the same. To address this, primary keys in the database
// will use a more specific name like "rating_id" instead of "id".

type Rating struct {
	ID      *int `db:"rating_id"`
	TitleID int  `db:"title_id"`
	UserID  int  `db:"user_id"`
	Rating  *int
	Comment *string
	Date    *time.Time
	Updated time.Time
	Title
}

// Document is metadata for an accepted signed ratingset document. The
// document bytes themselves live in content-addressed blob storage,
// keyed by Hash.
type Document struct {
	Userid       string
	Rev          int
	Hash         string
	Size         int
	ReceivedTime time.Time
}

// Epoch is a row in the cut ledger. Kind is "patch" for epoch-advancing
// cuts and "snapshot" for corpus materializations at an existing epoch.
type Epoch struct {
	Epoch        int
	Kind         string
	ManifestHash string
	Created      time.Time
}

// PublishedHead records the rev of a user's document as of the last cut.
type PublishedHead struct {
	Userid string
	Rev    int
}

type Title struct {
	ID            int `db:"title_id"`
	Type          string
	Name          string
	Year          int
	TMDBID        int    `db:"tmdb_id"`
	TMDBPosterURL string `db:"tmdb_poster_url"`
}

type TitleStats struct {
	RatingCount   int
	AverageRating float32
}

type User struct {
	ID          int `db:"user_id"`
	Login       string
	Password    []byte
	Email       *string
	Userid      string
	Publickey   []byte
	Privatekey  []byte
	Minrating   int
	Maxrating   int
	Revision    int
	ShowPosters bool
}

type Settings struct {
	TMDBAPIKey string
}

var InvalidPassword = errors.New("Invalid Password")

func (u *User) VerifyPassword(password string) bool {
	return bcrypt.CompareHashAndPassword(u.Password, []byte(password)) == nil
}

type TitleQuery struct {
	Name *string
}
