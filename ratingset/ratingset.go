// Package ratingset is the ratingset document format: the author and
// item lines of a revs ratings document, how they are parsed, validated
// and encoded, and the user identities that sign them. It is shared by
// both sides of revs: the server checks uploads with it, and clients
// (see the collection package) build documents with it.
package ratingset

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openrevs/revs/keys"
	jsonlines "github.com/openrevs/sjsonl-go"
	"github.com/tidwall/gjson"
	"golang.org/x/text/unicode/norm"
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
	return keys.UserID(pub)
}

func (u User) ValidIDs() bool {
	return userID(u.PublicKey) == u.ID
}

func (u User) Keys() (string, string) {
	pub := keys.EncodePublic(u.PublicKey)
	prv := ""
	if len(u.PrivateKey) > 0 {
		prv = keys.EncodePrivate(u.PrivateKey)
	}

	return pub, prv
}

// The lines of a ratingset (https://openrevs.org/specs/ratingset/).
// Struct fields are declared in lexicographic order of their JSON keys,
// so encoding/json emits them in the order the line encoding requires.

// Author is a ratingset's author line. It carries no identifying
// details beyond the key-derived ID.
type Author struct {
	ID        string `json:"id" validate:"required"`
	MaxRating int    `json:"max_rating"`
	MinRating int    `json:"min_rating"`
	Revision  int    `json:"revision,omitempty"`
}

// ItemTypeMovie is the only item type defined so far.
const ItemTypeMovie = "movie"

// Item is a ratingset's item line: one item and the author's entries
// for it.
type Item struct {
	Entries []Entry           `json:"entries,omitempty" yaml:",omitempty"`
	Refs    map[string]string `json:"refs,omitempty" yaml:",omitempty"`
	Title   string            `json:"title" validate:"required"`
	Type    string            `json:"type"`
	Year    *int              `json:"year,omitempty" yaml:",omitempty"`
}

// Entry is one occasion of the author engaging with an item, such as
// one viewing of a movie.
type Entry struct {
	Date   *string `json:"date,omitempty" yaml:",omitempty"`
	Rating *int    `json:"rating,omitempty" yaml:",omitempty"`
	Review *string `json:"review,omitempty" yaml:",omitempty"`
}

// ParseAuthorLine reads an author line. It must have a string id and a
// valid rating scale: integer min_rating and max_rating, with max_rating
// greater than min_rating.
func ParseAuthorLine(line []byte) (Author, error) {
	var a Author
	if !gjson.ValidBytes(line) || !gjson.ParseBytes(line).IsObject() {
		return a, errors.New("author line is not a JSON object")
	}
	if gjson.GetBytes(line, "id").Type != gjson.String {
		return a, errors.New("author line id is not a string")
	}
	for _, f := range []string{"min_rating", "max_rating"} {
		if !gjson.GetBytes(line, f).Exists() {
			return a, fmt.Errorf("author line has no %s", f)
		}
	}
	if err := json.Unmarshal(line, &a); err != nil {
		return a, fmt.Errorf("author line: %w", err)
	}
	if a.MaxRating <= a.MinRating {
		return a, errors.New("author line max_rating must be greater than min_rating")
	}
	return a, nil
}

// ParseItemLine reads an item line, without validating its contents
// (see Item.Validate). A line with an "id" member is an author line,
// not an item line.
func ParseItemLine(line []byte) (Item, error) {
	var it Item
	if !gjson.ValidBytes(line) || !gjson.ParseBytes(line).IsObject() {
		return it, errors.New("item line is not a JSON object")
	}
	if gjson.GetBytes(line, "id").Exists() {
		return it, errors.New("item line has an id")
	}
	if err := json.Unmarshal(line, &it); err != nil {
		return it, err
	}
	return it, nil
}

// Validate checks an item against the ratingset spec, reading ratings
// against the document's scale minRating..maxRating. An invalid item is
// skipped by consumers.
func (it Item) Validate(minRating, maxRating int) error {
	if it.Type == "" {
		return errors.New("item has no type")
	}
	if strings.TrimSpace(it.Title) == "" {
		return errors.New("item has no title")
	}
	for _, e := range it.Entries {
		if e.Rating != nil && (*e.Rating < minRating || *e.Rating > maxRating) {
			return fmt.Errorf("rating %d is outside the scale %d..%d", *e.Rating, minRating, maxRating)
		}
		if e.Date != nil && !ValidDate(*e.Date) {
			return fmt.Errorf("date %q is not a valid YYYY-MM-DD date", *e.Date)
		}
	}
	return nil
}

// IsType reports whether the item is of type typ, ignoring case.
func (it Item) IsType(typ string) bool {
	return strings.EqualFold(it.Type, typ)
}

// ValidDate reports whether s is a calendar date in YYYY-MM-DD form.
func ValidDate(s string) bool {
	if len(s) != len(time.DateOnly) {
		return false
	}
	_, err := time.Parse(time.DateOnly, s)
	return err == nil
}

// Normalize returns a copy of the item with its strings in Unicode NFC,
// as the line encoding requires of generators.
func (it Item) Normalize() Item {
	out := it
	out.Title = norm.NFC.String(it.Title)
	out.Type = norm.NFC.String(it.Type)
	if it.Refs != nil {
		out.Refs = make(map[string]string, len(it.Refs))
		for k, v := range it.Refs {
			out.Refs[norm.NFC.String(k)] = norm.NFC.String(v)
		}
	}
	if it.Entries != nil {
		out.Entries = make([]Entry, len(it.Entries))
		for i, e := range it.Entries {
			if e.Review != nil {
				r := norm.NFC.String(*e.Review)
				e.Review = &r
			}
			out.Entries[i] = e
		}
	}
	return out
}

// EncodeLine encodes one ratingset line as the line encoding asks of
// generators: compact JSON, keys sorted (for the types in this package,
// by declaration order), and no escaping of <, >, or & beyond what
// JSON requires.
func EncodeLine(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// ParseRatingset verifies a signed sjsonl ratingset and returns its
// author line and the parsed document. The document must have exactly
// one author line, with a valid rating scale and an ID matching the
// signing key. Item lines are not checked.
func ParseRatingset(data []byte) (*Author, *jsonlines.JSONL, error) {
	doc, err := jsonlines.Parse(data)
	if err != nil {
		return nil, nil, err
	}

	var author *Author
	lines := bytes.Split(data, []byte("\n"))
	for _, line := range lines[1:] { // skip the control line
		if !gjson.GetBytes(line, "id").Exists() {
			continue
		}
		if author != nil {
			return nil, nil, errors.New("multiple author lines")
		}
		a, err := ParseAuthorLine(line)
		if err != nil {
			return nil, nil, err
		}
		author = &a
	}
	if author == nil {
		return nil, nil, errors.New("author line not found")
	}

	if userID(doc.Info().SignerKey) != author.ID {
		return nil, nil, errors.New("author id does not match signing key")
	}

	return author, doc, nil
}
