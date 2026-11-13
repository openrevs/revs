package revs

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// archiveWriter writes a publication archive (a snapshot or a patch) as
// one or more parts, <dir>/<base>_<n>.tar.gz, numbered from 1. Each part
// is a complete gzipped ustar file holding only regular files, and parts
// split only at entry boundaries.
//
// Each part is written to a temporary file and renamed into place when
// it is complete, so no partially written part is ever visible. The
// part size is a target measured on the uncompressed tar stream (an
// upper bound on the compressed size); an entry larger than the target
// gets a part to itself.
type archiveWriter struct {
	dir      string
	base     string
	partSize int
	modTime  time.Time

	parts int

	// The open part, if any.
	tmp  *os.File
	gz   *gzip.Writer
	tw   *tar.Writer
	size int
}

func newArchiveWriter(dir, base string, partSize int, modTime time.Time) (*archiveWriter, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	return &archiveWriter{
		dir:      dir,
		base:     base,
		partSize: partSize,
		modTime:  modTime.Truncate(time.Second),
	}, nil
}

// tarSize is the space an entry of n bytes takes in a tar stream: a
// 512-byte header plus the data padded to a 512-byte block.
func tarSize(n int) int {
	return 512 + (n+511)/512*512
}

// Add appends one entry, starting a new part first if the open part
// would overflow the target size.
func (w *archiveWriter) Add(name string, data []byte) error {
	n := tarSize(len(data))
	if w.tw != nil && w.size > 0 && w.size+n > w.partSize {
		if err := w.closePart(); err != nil {
			return err
		}
	}
	if w.tw == nil {
		if err := w.openPart(); err != nil {
			return err
		}
	}

	hdr := &tar.Header{
		Typeflag: tar.TypeReg,
		Name:     name,
		Size:     int64(len(data)),
		Mode:     0644,
		ModTime:  w.modTime,
		Format:   tar.FormatUSTAR,
	}
	if err := w.tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("archive entry %s: %w", name, err)
	}
	if _, err := w.tw.Write(data); err != nil {
		return err
	}
	w.size += n

	return nil
}

// Close finishes the last part and returns the number of parts written.
// An archive with no entries is still published, as one empty part.
func (w *archiveWriter) Close() (int, error) {
	if w.tw == nil && w.parts == 0 {
		if err := w.openPart(); err != nil {
			return 0, err
		}
	}
	if w.tw != nil {
		if err := w.closePart(); err != nil {
			return 0, err
		}
	}
	return w.parts, nil
}

// Abort discards the open part, if any. Parts already renamed into place
// stay; they are unreferenced until an index lists them.
func (w *archiveWriter) Abort() {
	if w.tmp != nil {
		w.tmp.Close()
		os.Remove(w.tmp.Name())
		w.tmp, w.gz, w.tw = nil, nil, nil
	}
}

func (w *archiveWriter) openPart() error {
	tmp, err := os.CreateTemp(w.dir, ".tmp-*")
	if err != nil {
		return err
	}
	w.tmp = tmp
	w.gz = gzip.NewWriter(tmp)
	w.tw = tar.NewWriter(w.gz)
	w.size = 0
	w.parts++
	return nil
}

func (w *archiveWriter) closePart() error {
	err := w.tw.Close()
	if err == nil {
		err = w.gz.Close()
	}
	if err == nil {
		err = w.tmp.Chmod(0644)
	}
	if err != nil {
		w.Abort()
		return err
	}

	tmp := w.tmp
	w.tmp, w.gz, w.tw = nil, nil, nil
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}

	path := filepath.Join(w.dir, fmt.Sprintf("%s_%d.tar.gz", w.base, w.parts))
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return err
	}

	return nil
}
