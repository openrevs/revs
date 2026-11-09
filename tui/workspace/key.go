package workspace

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/openrevs/revs/keys"
)

func (w *Workspace) loadKey() error {
	data, err := os.ReadFile(w.path(keyFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	key, err := keys.ParsePrivate(string(data))
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

// ImportKey installs an existing base64 private key (the `revs keygen`
// "Private key" line) and returns the derived user id.
func (w *Workspace) ImportKey(encoded string) (string, error) {
	if w.key != nil {
		return "", errors.New("workspace: key already exists")
	}
	key, err := keys.ParsePrivate(encoded)
	if err != nil {
		return "", err
	}
	return w.saveKey(key)
}

func (w *Workspace) saveKey(prv ed25519.PrivateKey) (string, error) {
	content := keys.EncodePrivate(prv) + "\n"
	if err := writeFileAtomic(w.path(keyFile), []byte(content), 0o600); err != nil {
		return "", err
	}
	w.key = prv
	return w.UserID(), nil
}
