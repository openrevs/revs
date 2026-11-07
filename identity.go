package dmrs

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const serverKeyFile = "server.key"

// loadOrCreateServerKey returns the server's ed25519 identity, generating
// it on first boot. The key file holds the hex-encoded private key in the
// data directory; the key is the server's identity (the TOFU pin and
// manifest signer), so backing up the data directory backs it up too.
func loadOrCreateServerKey(dataDir string) (ed25519.PrivateKey, error) {
	path := filepath.Join(dataDir, serverKeyFile)

	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		raw, err := hex.DecodeString(strings.TrimSpace(string(data)))
		if err != nil {
			return nil, fmt.Errorf("decoding server key %s: %w", path, err)
		}
		if len(raw) != ed25519.PrivateKeySize {
			return nil, fmt.Errorf("server key %s: unexpected length %d", path, len(raw))
		}
		return ed25519.PrivateKey(raw), nil

	case os.IsNotExist(err):
		_, prv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(dataDir, 0755); err != nil {
			return nil, err
		}
		if err := writeFileAtomic(path, []byte(hex.EncodeToString(prv)+"\n"), 0600); err != nil {
			return nil, err
		}
		return prv, nil

	default:
		return nil, err
	}
}
