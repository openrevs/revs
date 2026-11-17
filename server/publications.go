package server

import (
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing/fstest"
)

// Publications is where a server writes its published static file tree
// (index.json and a directory of archives and manifests per channel)
// and serves it from, under /v1/.
// Names are slash-separated and relative to the v1 root.
type Publications interface {
	// WriteFile publishes data at name, replacing any previous file.
	// A partially written file must never be observable.
	WriteFile(name string, data []byte) error
	// FS serves the published tree.
	FS() http.FileSystem
}

// DirPublications keeps publications in a directory: what the server
// uses in production, where a CDN or reverse proxy may serve the same
// files.
type DirPublications struct {
	Root string
}

func (d DirPublications) WriteFile(name string, data []byte) error {
	p := filepath.Join(d.Root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return err
	}
	return writeFileAtomic(p, data, 0644)
}

func (d DirPublications) FS() http.FileSystem { return http.Dir(d.Root) }

// MemPublications keeps publications in memory, for tests. The zero
// value is ready to use.
type MemPublications struct {
	mu    sync.RWMutex
	files map[string][]byte
}

func (m *MemPublications) WriteFile(name string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.files == nil {
		m.files = map[string][]byte{}
	}
	m.files[path.Clean(strings.TrimPrefix(name, "/"))] = append([]byte(nil), data...)
	return nil
}

func (m *MemPublications) FS() http.FileSystem { return http.FS(memPubFS{m}) }

// ReadFile returns a published file, for tests inspecting publications
// directly.
func (m *MemPublications) ReadFile(name string) ([]byte, error) {
	return fs.ReadFile(memPubFS{m}, name)
}

type memPubFS struct{ m *MemPublications }

// Open serves from a snapshot of the tree, so a file being replaced
// mid-read is seen either whole before or whole after.
func (f memPubFS) Open(name string) (fs.File, error) {
	f.m.mu.RLock()
	snap := make(fstest.MapFS, len(f.m.files))
	for k, v := range f.m.files {
		snap[k] = &fstest.MapFile{Data: v, Mode: 0644}
	}
	f.m.mu.RUnlock()

	return snap.Open(name)
}
