package ratingset

import (
	"bytes"
	"testing"

	jsonlines "github.com/openrevs/sjsonl-go"
	"github.com/stretchr/testify/assert"
)

func TestParseRatingset(t *testing.T) {
	u := NewUser()

	doc := `{"title":"Pulp Fiction","type":"movie","year":1995}
{"id":"` + u.ID + `","max_rating":5,"min_rating":0,"revision":3}
`
	signed, _, err := jsonlines.Sign([]byte(doc), u.PrivateKey)
	assert.NoError(t, err)

	parsed, parsedDoc, err := ParseRatingset(signed)
	assert.NoError(t, err)
	assert.Equal(t, u.ID, parsed.ID)
	assert.Equal(t, 3, parsed.Revision)
	assert.Equal(t, []byte(u.PublicKey), []byte(parsedDoc.Info().SignerKey))

	// Tampered content must fail verification.
	tampered := bytes.Replace(signed, []byte("Pulp"), []byte("Gulp"), 1)
	_, _, err = ParseRatingset(tampered)
	assert.Error(t, err)

	// Invalid author lines must be rejected: an ID that doesn't match
	// the signing key, no valid rating scale, or a second author line.
	for _, bad := range []string{
		`{"id":"0000000000000000000000000000000000000000000000000000000000000000","max_rating":5,"min_rating":0}` + "\n",
		`{"id":"` + u.ID + `","revision":1}` + "\n",
		`{"id":"` + u.ID + `","max_rating":5,"min_rating":5}` + "\n",
		`{"id":"` + u.ID + `","max_rating":5,"min_rating":0}` + "\n" + `{"id":"` + u.ID + `","max_rating":10,"min_rating":0}` + "\n",
	} {
		signedBad, _, err := jsonlines.Sign([]byte(bad), u.PrivateKey)
		assert.NoError(t, err)
		_, _, err = ParseRatingset(signedBad)
		assert.Error(t, err, bad)
	}

	// A document with no author line must be rejected.
	noUser := `{"title":"Pulp Fiction","type":"movie","year":1995}` + "\n"
	signedNoUser, _, err := jsonlines.Sign([]byte(noUser), u.PrivateKey)
	assert.NoError(t, err)
	_, _, err = ParseRatingset(signedNoUser)
	assert.Error(t, err)
}

func TestUser_ValidIDs(t *testing.T) {
	u := NewUser()
	assert.True(t, u.ValidIDs())

	u.ID = "abc"
	assert.False(t, u.ValidIDs())
}

func TestItemValidate(t *testing.T) {
	cases := map[string]bool{
		`{"title":"Rocky","type":"movie"}`:                                               true,
		`{"entries":[{}],"title":"Rocky","type":"MOVIE","x":1}`:                          true,
		`{"entries":[{"date":"2024-02-29","rating":10}],"title":"Rocky","type":"movie"}`: true,
		`{"title":"Rocky"}`:                                                  false, // no type
		`{"title":" ","type":"movie"}`:                                       false, // blank title
		`{"entries":[{"rating":11}],"title":"Rocky","type":"movie"}`:         false,
		`{"entries":[{"rating":-1}],"title":"Rocky","type":"movie"}`:         false,
		`{"entries":[{"date":"2023-02-29"}],"title":"Rocky","type":"movie"}`: false,
		`{"entries":[{"date":"2023-2-1"}],"title":"Rocky","type":"movie"}`:   false,
		`{"entries":[{"rating":7.5}],"title":"Rocky","type":"movie"}`:        false,
		`{"id":"x","title":"Rocky","type":"movie"}`:                          false,
		`[1]`: false,
	}
	for line, want := range cases {
		it, err := ParseItemLine([]byte(line))
		if err == nil {
			err = it.Validate(0, 10)
		}
		assert.Equal(t, want, err == nil, "%s: %v", line, err)
	}
}

func TestEncodeLine(t *testing.T) {
	y, r, rv := 1994, 9, "Tom & Jerry <3 Ame\u0301lie"
	d := "1997-07-07"
	it := Item{
		Type:    ItemTypeMovie,
		Title:   "Ame\u0301lie",
		Year:    &y,
		Refs:    map[string]string{"wikidata": "Q104123", "imdb": "tt0110912"},
		Entries: []Entry{{Date: &d, Rating: &r, Review: &rv}, {}},
	}
	line, err := EncodeLine(it.Normalize())
	assert.NoError(t, err)
	assert.Equal(t, `{"entries":[{"date":"1997-07-07","rating":9,"review":"Tom & Jerry <3 Amélie"},{}],"refs":{"imdb":"tt0110912","wikidata":"Q104123"},"title":"Amélie","type":"movie","year":1994}`, string(line))

	line, err = EncodeLine(Author{ID: "abc", MinRating: 0, MaxRating: 10, Revision: 4})
	assert.NoError(t, err)
	assert.Equal(t, `{"id":"abc","max_rating":10,"min_rating":0,"revision":4}`, string(line))
}

// Verify checks only the identity binding; ParseRatingset adds the
// rating scale. Both share one author-line scan, so they agree on it.
func TestVerify(t *testing.T) {
	u, other := NewUser(), NewUser()
	sign := func(content string) []byte {
		signed, _, err := jsonlines.Sign([]byte(content), u.PrivateKey)
		assert.NoError(t, err)
		return signed
	}
	item := `{"title":"Rocky","type":"movie"}` + "\n"
	author := func(fields string) string { return `{"id":"` + u.ID + `"` + fields + "}\n" }

	cases := []struct {
		name          string
		content       string
		verify, parse bool
	}{
		{"complete", item + author(`,"max_rating":5,"min_rating":0,"revision":2`), true, true},
		{"no rating scale", author(`,"revision":2`) + item, true, false},
		{"no revision", author(`,"max_rating":5,"min_rating":0`), false, false},
		{"revision 0", author(`,"max_rating":5,"min_rating":0,"revision":0`), false, false},
		{"fractional revision", author(`,"max_rating":5,"min_rating":0,"revision":1.5`), false, false},
		{"string revision", author(`,"max_rating":5,"min_rating":0,"revision":"1"`), false, false},
		{"numeric id", `{"id":7,"revision":1}` + "\n", false, false},
		{"other user's id", `{"id":"` + other.ID + `","max_rating":5,"min_rating":0,"revision":1}` + "\n", false, false},
		{"two author lines", author(`,"revision":1`) + author(`,"revision":2`), false, false},
		{"no author line", item, false, false},
	}
	for _, c := range cases {
		signed := sign(c.content)

		doc, id, err := Verify(signed)
		assert.Equal(t, c.verify, err == nil, "Verify %s: %v", c.name, err)
		if err == nil {
			assert.Equal(t, Identity{ID: u.ID, Revision: 2}, id, c.name)
			got, err := ReadIdentity(doc.JSON())
			assert.NoError(t, err, c.name)
			assert.Equal(t, id, got, c.name)
		}

		_, _, err = ParseRatingset(signed)
		assert.Equal(t, c.parse, err == nil, "ParseRatingset %s: %v", c.name, err)
	}

	// ReadIdentity trusts the signature: it reads a tampered document.
	signed := sign(author(`,"revision":2`))
	tampered := bytes.Replace(signed, []byte(`"revision":2`), []byte(`"revision":9`), 1)
	_, _, err := Verify(tampered)
	assert.Error(t, err)
	id, err := ReadIdentity(tampered)
	assert.NoError(t, err)
	assert.Equal(t, 9, id.Revision)
}
