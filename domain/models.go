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
	// Source is where the document came from: "" for a direct upload,
	// else the URL of the upstream server it was pulled from.
	Source string
}

// Artifact is a published snapshot or patch of a channel. A snapshot
// has Start == End, the epoch it materializes.
type Artifact struct {
	Channel string
	Kind    string
	Start   int
	End     int
	Parts   int
	Created time.Time
}

// ChannelChange records that, from Epoch on, a channel holds rev Rev of
// a user's document, or none if Rev is nil (the user was removed).
type ChannelChange struct {
	Userid string
	Rev    *int
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
