package keys

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testKey = ed25519.NewKeyFromSeed([]byte("0123456789abcdef0123456789abcdef"))

func TestUserIDIsFullSHA256Hex(t *testing.T) {
	pub := testKey.Public().(ed25519.PublicKey)
	sum := sha256.Sum256(pub)

	id := UserID(pub)
	assert.Equal(t, hex.EncodeToString(sum[:]), id)
	assert.Len(t, id, 64)
}

func TestRoundTrip(t *testing.T) {
	pub := testKey.Public().(ed25519.PublicKey)

	encPub := EncodePublic(pub)
	assert.Len(t, encPub, 44)
	gotPub, err := ParsePublic(encPub)
	require.NoError(t, err)
	assert.Equal(t, pub, gotPub)

	encPrv := EncodePrivate(testKey)
	assert.Len(t, encPrv, 44)
	gotPrv, err := ParsePrivate(encPrv + "\n")
	require.NoError(t, err)
	assert.Equal(t, testKey, gotPrv)
}

func TestParseRejects(t *testing.T) {
	enc := EncodePrivate(testKey)

	tests := []struct {
		name, in string
	}{
		{"hex", hex.EncodeToString(testKey)},
		{"unpadded", enc[:len(enc)-2]},
		{"url alphabet", "-_" + enc[2:]},
		{"64-byte form", base64.StdEncoding.EncodeToString(testKey)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParsePrivate(tt.in)
			assert.Error(t, err)
		})
	}

	_, err := ParsePublic(hex.EncodeToString(testKey.Public().(ed25519.PublicKey)))
	assert.Error(t, err)
}

func TestValidUserID(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	id := UserID(pub)
	if !ValidUserID(id) {
		t.Errorf("derived id %q is not valid", id)
	}
	for _, bad := range []string{"", "abc", id[:63], id + "0", strings.ToUpper(id), id[:63] + "g"} {
		if ValidUserID(bad) {
			t.Errorf("ValidUserID(%q) = true", bad)
		}
	}
}
