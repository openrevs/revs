package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"path"
	"time"

	"github.com/openrevs/revs/publication"
)

// archiveWriter writes a publication archive (a snapshot or a patch) as
// one or more parts, <dir>/<base>_<n>.tar.gz, numbered from 1. Each part
// is a complete gzipped ustar file holding only regular files, and parts
// split only at entry boundaries.
//
// Each part is built in memory and published when it is complete, so no
// partially written part is ever visible. The
// part size is a target measured on the uncompressed tar stream (an
// upper bound on the compressed size); an entry larger than the target
// gets a part to itself.
type archiveWriter struct {
	pub      Publications
	dir      string
	base     string
	partSize int
	modTime  time.Time

	parts int

	// The open part, if any.
	buf  *bytes.Buffer
	gz   *gzip.Writer
	tw   *tar.Writer
	size int
}

// newArchiveWriter writes parts into dir (slash-separated) of pub.
func newArchiveWriter(pub Publications, dir, base string, partSize int, modTime time.Time) (*archiveWriter, error) {
	return &archiveWriter{
		pub:      pub,
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
// would overflow the target size, and returns the part (numbered from 1)
// that holds it.
func (w *archiveWriter) Add(name string, data []byte) (int, error) {
	n := tarSize(len(data))
	if w.tw != nil && w.size > 0 && w.size+n > w.partSize {
		if err := w.closePart(); err != nil {
			return 0, err
		}
	}
	if w.tw == nil {
		if err := w.openPart(); err != nil {
			return 0, err
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
		return 0, fmt.Errorf("archive entry %s: %w", name, err)
	}
	if _, err := w.tw.Write(data); err != nil {
		return 0, err
	}
	w.size += n

	return w.parts, nil
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

// Abort discards the open part, if any. Parts already published stay;
// they are unreferenced until an index lists them.
func (w *archiveWriter) Abort() {
	w.buf, w.gz, w.tw = nil, nil, nil
}

func (w *archiveWriter) openPart() error {
	w.buf = new(bytes.Buffer)
	w.gz = gzip.NewWriter(w.buf)
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
	buf := w.buf
	w.Abort()
	if err != nil {
		return err
	}

	return w.pub.WriteFile(path.Join(w.dir, publication.PartName(w.base, w.parts)), buf.Bytes())
}
