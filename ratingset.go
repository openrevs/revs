package revs

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	jsonlines "github.com/openrevs/sjsonl-go"
	"github.com/tidwall/gjson"
)

type User struct {
	ID         string             `json:"id" validate:"required"`
	Email      *string            `json:"email"`
	MinRating  int                `json:"min_rating"`
	MaxRating  int                `json:"max_rating"`
	PublicKey  ed25519.PublicKey  `json:"public_key"`
	PrivateKey ed25519.PrivateKey `json:"-"`
}

func NewUser() User {
	pub, prv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}

	return User{
		ID:         userID(pub),
		MinRating:  0,
		MaxRating:  5,
		PublicKey:  pub,
		PrivateKey: prv,
	}
}

// userID derives a user ID from a public key.
func userID(pub ed25519.PublicKey) string {
	pubHash := sha256.Sum256(pub)
	return hex.EncodeToString(pubHash[:16])
}

func (u User) ValidIDs() bool {
	return userID(u.PublicKey) == u.ID
}

func (u User) Keys() (string, string) {
	pub := hex.EncodeToString(u.PublicKey)
	prv := ""
	if len(u.PrivateKey) > 0 {
		prv = hex.EncodeToString(u.PrivateKey)
	}

	return pub, prv
}

type Title2 struct {
	Type    *string           `json:"type,omitempty"`
	Name    string            `json:"name,omitempty" validate:"required"`
	Year    *int              `json:"year,omitempty"`
	Refs    map[string]string `json:"refs,omitempty"`
	Reviews []Review2         `json:"reviews,omitempty" yaml:",omitempty"`
}
type Review2 struct {
	Date    *string `json:"date,omitempty"`
	Rating  *int    `json:"rating,omitempty"`
	Comment *string `json:"comment,omitempty"`
}

// ParseRatingset verifies a signed sjsonl ratingset and returns its user line
// and the parsed document. The user line's ID must match the signing key.
func ParseRatingset(data []byte) (*User2, *jsonlines.JSONL, error) {
	doc, err := jsonlines.Parse(data)
	if err != nil {
		return nil, nil, err
	}

	var user *User2
	lines := bytes.Split(data, []byte("\n"))
	for _, line := range lines[1:] { // skip the control line
		if !gjson.GetBytes(line, "id").Exists() {
			continue
		}
		if user != nil {
			return nil, nil, errors.New("multiple user lines")
		}
		user = &User2{}
		if err := json.Unmarshal(line, user); err != nil {
			return nil, nil, err
		}
	}
	if user == nil {
		return nil, nil, errors.New("user line not found")
	}

	if userID(doc.Info().SignerKey) != user.ID {
		return nil, nil, errors.New("user id does not match signing key")
	}

	return user, doc, nil
}
