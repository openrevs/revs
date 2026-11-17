package server

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"

	"github.com/openrevs/revs/keys"
)

const serverKeyFile = "server.key"

// LoadOrCreateKey returns the server's ed25519 identity from dataDir,
// generating it on first boot. The key file holds the base64-encoded private key in the
// data directory; the key is the server's identity (the TOFU pin and
// manifest signer), so backing up the data directory backs it up too.
func LoadOrCreateKey(dataDir string) (ed25519.PrivateKey, error) {
	path := filepath.Join(dataDir, serverKeyFile)

	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		prv, err := keys.ParsePrivate(string(data))
		if err != nil {
			return nil, fmt.Errorf("server key %s: %w", path, err)
		}
		return prv, nil

	case os.IsNotExist(err):
		_, prv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(dataDir, 0755); err != nil {
			return nil, err
		}
		if err := writeFileAtomic(path, []byte(keys.EncodePrivate(prv)+"\n"), 0600); err != nil {
			return nil, err
		}
		return prv, nil

	default:
		return nil, err
	}
}
