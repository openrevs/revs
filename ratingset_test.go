package revs

import (
	"bytes"
	"testing"

	jsonlines "github.com/openrevs/sjsonl-go"
	"github.com/stretchr/testify/assert"
)

func TestParseRatingset(t *testing.T) {
	u := NewUser()

	doc := `{"id":"` + u.ID + `","min_rating":0,"max_rating":5,"revision":3}
{"name":"Pulp Fiction","year":1995}
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

	// A user line whose ID doesn't match the signing key must be rejected.
	badDoc := `{"id":"00000000000000000000000000000000","min_rating":0,"max_rating":5}` + "\n"
	signedBad, _, err := jsonlines.Sign([]byte(badDoc), u.PrivateKey)
	assert.NoError(t, err)
	_, _, err = ParseRatingset(signedBad)
	assert.Error(t, err)

	// A document with no user line must be rejected.
	noUser := `{"name":"Pulp Fiction","year":1995}` + "\n"
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
