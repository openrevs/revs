// Package fsstore is a client.Store backed by the local filesystem:
// content-addressed blobs under <root>/docs/<hash[:2]>/<hash> (mirroring
// the server's own blob layout) plus an atomically rewritten
// <root>/state.json. It needs nothing beyond the standard library.
package fsstore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/openrevs/revs/client"
)

type Store struct {
	root string
}

// New opens (creating if needed) a store rooted at the given directory.
func New(root string) (*Store, error) {
	if err := os.MkdirAll(root, 0755); err != nil {
		return nil, err
	}

	return &Store{root: root}, nil
}

func (s *Store) blobPath(hash string) (string, error) {
	if len(hash) < 2 || !isLowerHex(hash) {
		return "", fmt.Errorf("fsstore: invalid blob hash %q", hash)
	}

	return filepath.Join(s.root, "docs", hash[:2], hash), nil
}

func (s *Store) PutBlob(hash string, data []byte) error {
	path, err := s.blobPath(hash)
	if err != nil {
		return err
	}

	if _, err := os.Stat(path); err == nil {
		return nil // content-addressed: already present means already identical
	}

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	return writeFileAtomic(path, data)
}

func (s *Store) GetBlob(hash string) ([]byte, error) {
	path, err := s.blobPath(hash)
	if err != nil {
		return nil, err
	}

	return os.ReadFile(path)
}

func (s *Store) LoadState() (*client.State, error) {
	data, err := os.ReadFile(filepath.Join(s.root, "state.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var state client.State
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("fsstore: corrupt state file: %w", err)
	}

	return &state, nil
}

func (s *Store) SaveState(state *client.State) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}

	return writeFileAtomic(filepath.Join(s.root, "state.json"), append(data, '\n'))
}

func isLowerHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// writeFileAtomic writes data to a temp file in the target directory and
// renames it into place, so partially written files are never visible.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	return os.Rename(tmp.Name(), path)
}
