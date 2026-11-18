// Package keys defines how revs encodes Ed25519 keys and derives user
// IDs. Keys are standard, padded base64 (matching sjsonl headers); a
// user ID is the full SHA-256 of the public key as lowercase hex.
package keys

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// UserID derives a user's ID from their public key. A document's user
// ID must always equal the ID derived from the key that signed it.
func UserID(pub ed25519.PublicKey) string {
	h := sha256.Sum256(pub)
	return hex.EncodeToString(h[:])
}

// ValidUserID reports whether s is a well-formed user ID: 64
// lowercase hex characters.
func ValidUserID(s string) bool {
	if len(s) != 2*sha256.Size {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// EncodePublic returns the base64 encoding of a public key.
func EncodePublic(pub ed25519.PublicKey) string {
	return base64.StdEncoding.EncodeToString(pub)
}

// EncodePrivate returns the base64 encoding of a private key's 32-byte
// seed (the RFC 8032 private key), from which the full key is derived.
func EncodePrivate(prv ed25519.PrivateKey) string {
	return base64.StdEncoding.EncodeToString(prv.Seed())
}

// ParsePublic decodes a base64 public key.
func ParsePublic(s string) (ed25519.PublicKey, error) {
	raw, err := decode(s)
	if err != nil {
		return nil, fmt.Errorf("invalid public key: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("public key must be %d bytes, got %d", ed25519.PublicKeySize, len(raw))
	}
	return ed25519.PublicKey(raw), nil
}

// ParsePrivate decodes a base64 private key seed, as printed by `revs
// keygen`, and expands it to a full key. Surrounding whitespace is
// ignored so key files may end in a newline.
func ParsePrivate(s string) (ed25519.PrivateKey, error) {
	raw, err := decode(s)
	if err != nil {
		return nil, fmt.Errorf("invalid private key: %w", err)
	}
	if len(raw) != ed25519.SeedSize {
		return nil, fmt.Errorf("private key must be a %d-byte seed (%d base64 chars), got %d bytes",
			ed25519.SeedSize, base64.StdEncoding.EncodedLen(ed25519.SeedSize), len(raw))
	}
	return ed25519.NewKeyFromSeed(raw), nil
}

// decode accepts only canonical, padded standard base64.
func decode(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	raw, err := base64.StdEncoding.Strict().DecodeString(s)
	if err != nil {
		return nil, err
	}
	if base64.StdEncoding.EncodeToString(raw) != s {
		return nil, errors.New("non-canonical base64")
	}
	return raw, nil
}
