package dmrs

import (
	"fmt"
	"os"
	"path/filepath"
)

// BlobStore is minimal content-addressed storage. Keys are sjsonl doc
// hashes, so blobs are immutable and identical uploads dedup for free.
type BlobStore interface {
	Put(hash string, data []byte) error
	Get(hash string) ([]byte, error)
}

// FileStore stores blobs on the local filesystem under
// <root>/<hash[:2]>/<hash>.
type FileStore struct {
	root string
}

func NewFileStore(root string) (*FileStore, error) {
	if err := os.MkdirAll(root, 0755); err != nil {
		return nil, err
	}

	return &FileStore{root: root}, nil
}

func (s *FileStore) path(hash string) (string, error) {
	if len(hash) < 2 || !isHexString(hash) {
		return "", fmt.Errorf("invalid blob hash %q", hash)
	}

	return filepath.Join(s.root, hash[:2], hash), nil
}

func (s *FileStore) Put(hash string, data []byte) error {
	path, err := s.path(hash)
	if err != nil {
		return err
	}

	if _, err := os.Stat(path); err == nil {
		return nil // content-addressed: already present means already identical
	}

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	return writeFileAtomic(path, data, 0644)
}

func (s *FileStore) Get(hash string) ([]byte, error) {
	path, err := s.path(hash)
	if err != nil {
		return nil, err
	}

	return os.ReadFile(path)
}

func isHexString(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// writeFileAtomic writes data to a temp file in the target directory and
// renames it into place, so partially written files are never visible.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	return os.Rename(tmp.Name(), path)
}
