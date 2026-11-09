package workspace

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

func (w *Workspace) loadKey() error {
	data, err := os.ReadFile(w.path(keyFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	key, err := parseKeyHex(string(data))
	if err != nil {
		return fmt.Errorf("workspace: %s: %w", keyFile, err)
	}
	w.key = key
	return nil
}

// GenerateKey creates a fresh keypair, writes it to the key file, and
// returns the derived user id.
func (w *Workspace) GenerateKey() (string, error) {
	if w.key != nil {
		return "", errors.New("workspace: key already exists")
	}
	_, prv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	return w.saveKey(prv)
}

// ImportKey installs an existing private key given as hex (the
// `revs keygen` "Private key" line) and returns the derived user id.
func (w *Workspace) ImportKey(hexKey string) (string, error) {
	if w.key != nil {
		return "", errors.New("workspace: key already exists")
	}
	key, err := parseKeyHex(hexKey)
	if err != nil {
		return "", err
	}
	return w.saveKey(key)
}

func parseKeyHex(s string) (ed25519.PrivateKey, error) {
	raw, err := hex.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("invalid hex private key: %w", err)
	}
	if len(raw) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("private key must be %d bytes (%d hex chars), got %d bytes",
			ed25519.PrivateKeySize, 2*ed25519.PrivateKeySize, len(raw))
	}
	return ed25519.PrivateKey(raw), nil
}

func (w *Workspace) saveKey(prv ed25519.PrivateKey) (string, error) {
	content := hex.EncodeToString(prv) + "\n"
	if err := writeFileAtomic(w.path(keyFile), []byte(content), 0o600); err != nil {
		return "", err
	}
	w.key = prv
	return w.UserID(), nil
}
