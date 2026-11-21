// Package fsstore is a client.Store backed by the local filesystem:
// content-addressed blobs under <root>/docs/<hash[:2]>/<hash> (mirroring
// the server's own blob layout) plus an atomically rewritten
// <root>/state.json, and an author's version history per user under
// <root>/history/<userid>.json. It needs nothing beyond the standard
// library.
package fsstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
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

// PutBlobs stores each blob as its own file.
func (s *Store) PutBlobs(blobs []client.Blob) error {
	for _, b := range blobs {
		if err := s.PutBlob(b.Hash, b.Data); err != nil {
			return err
		}
	}
	return nil
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

	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("fsstore: blob %s: %w", hash, client.ErrBlobNotFound)
	}
	return data, err
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

// UpdateState applies a change to the state file. The file holds the
// whole state, so this rewrites it (atomically) however small the
// change; sqlitestore writes just what changed.
func (s *Store) UpdateState(ch client.StateChange) error {
	state, err := s.LoadState()
	if err != nil {
		return err
	}
	if state == nil {
		state = &client.State{}
	}
	state.Apply(ch)

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}

	return writeFileAtomic(filepath.Join(s.root, "state.json"), append(data, '\n'))
}

func (s *Store) historyPath(userID string) (string, error) {
	if userID == "" || !isLowerHex(userID) {
		return "", fmt.Errorf("fsstore: invalid user ID %q", userID)
	}

	return filepath.Join(s.root, "history", userID+".json"), nil
}

func (s *Store) LoadHistory(userID string) (*client.VersionHistory, error) {
	path, err := s.historyPath(userID)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var h client.VersionHistory
	if err := json.Unmarshal(data, &h); err != nil {
		return nil, fmt.Errorf("fsstore: corrupt history file: %w", err)
	}

	return &h, nil
}

func (s *Store) SaveHistory(userID string, h *client.VersionHistory) error {
	path, err := s.historyPath(userID)
	if err != nil {
		return err
	}

	data, err := json.Marshal(h)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	return writeFileAtomic(path, append(data, '\n'))
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
