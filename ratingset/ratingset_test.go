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
		// TV series: season and episode, only on tv items.
		`{"entries":[{"date":"2015-03-01","rating":9,"season":1},{"rating":8}],"title":"Fargo","type":"tv","year":2014}`: true,
		`{"entries":[{"episode":3,"season":0}],"title":"Fargo","type":"TV"}`:                                             true,
		`{"entries":[{"season":1}],"title":"Fargo","type":"movie"}`:                                                      false,
		`{"entries":[{"episode":1,"season":1}],"title":"Fargo","type":"movie"}`:                                          false,
		`{"entries":[{"episode":2}],"title":"Fargo","type":"tv"}`:                                                        false, // episode without season
		`{"entries":[{"season":-1}],"title":"Fargo","type":"tv"}`:                                                        false,
		`{"entries":[{"episode":0,"season":1}],"title":"Fargo","type":"tv"}`:                                             false,
		`{"entries":[{"season":1.5}],"title":"Fargo","type":"tv"}`:                                                       false,
		`{"entries":[{"season":"1"}],"title":"Fargo","type":"tv"}`:                                                       false,
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

	s1, e2 := 1, 2
	line, err = EncodeLine(Item{Type: ItemTypeTV, Title: "Fargo", Entries: []Entry{{Date: &d, Episode: &e2, Rating: &r, Review: &rv, Season: &s1}}})
	assert.NoError(t, err)
	assert.Equal(t, `{"entries":[{"date":"1997-07-07","episode":2,"rating":9,"review":"Tom & Jerry <3 Amélie","season":1}],"title":"Fargo","type":"tv"}`, string(line))

	line, err = EncodeLine(Author{ID: "abc", MinRating: 0, MaxRating: 10, Revision: 4})
	assert.NoError(t, err)
	assert.Equal(t, `{"id":"abc","max_rating":10,"min_rating":0,"revision":4}`, string(line))
}
