package client

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/openrevs/revs/keys"
	"github.com/openrevs/revs/publication"
	"github.com/openrevs/revs/ratingset"
)

// The publication format: see the publication package and
// specs/publishing-v2.md in the website repo.

const (
	// DefaultChannel is the channel a client follows unless told
	// otherwise: the server's primary feed.
	DefaultChannel = publication.ChannelMain

	// maxEntrySize bounds a single archive entry. Servers cap documents
	// well below this; the bound only stops a hostile archive from
	// exhausting memory.
	maxEntrySize = 16 << 20
)

// readArchive streams one archive part, calling fn for each entry with
// its name and contents (bounded by maxEntrySize; fn may skip reading).
// It enforces the archive format: ustar, regular files only, and names
// that are plain file names (no directories, absolute paths or "..").
func readArchive(r io.Reader, fn func(name string, data io.Reader) error) error {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer zr.Close()

	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}

		if hdr.Typeflag != tar.TypeReg {
			return fmt.Errorf("archive entry %q is not a regular file", hdr.Name)
		}
		if hdr.Format != tar.FormatUSTAR {
			return fmt.Errorf("archive entry %q is not in ustar format (%v)", hdr.Name, hdr.Format)
		}
		if hdr.Name == "" || strings.ContainsAny(hdr.Name, `/\`) || hdr.Name == "." || hdr.Name == ".." {
			return fmt.Errorf("archive entry %q is not a plain file name", hdr.Name)
		}
		if hdr.Size > maxEntrySize {
			return fmt.Errorf("archive entry %q is %d bytes, over the %d byte limit", hdr.Name, hdr.Size, maxEntrySize)
		}

		if err := fn(hdr.Name, tr); err != nil {
			return err
		}
	}
}

// fetchArchive fetches one archive part and streams its entries to fn.
func (c *Client) fetchArchive(ctx context.Context, path string, fn func(name string, data io.Reader) error) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("client: GET %s: %s", path, resp.Status)
	}

	if err := readArchive(resp.Body, fn); err != nil {
		return fmt.Errorf("client: %s: %w", path, err)
	}
	return nil
}

// entryControl is the subset of an entry's sjsonl control line the
// client dispatches on: whether the entry is a full doc or a patch, and
// for patches which base it applies to and which document it yields.
type entryControl struct {
	SJSONL string `json:"sjsonl"`
	Base   string `json:"base"`
	Target string `json:"target"`
}

func peekControl(entry []byte) (entryControl, error) {
	first, _, _ := bytes.Cut(entry, []byte("\n"))

	var ctl entryControl
	if err := json.Unmarshal(first, &ctl); err != nil {
		return ctl, fmt.Errorf("client: entry control line: %w", err)
	}

	return ctl, nil
}

// verifiedDoc is a full document that passed verification.
type verifiedDoc struct {
	userID string
	rev    int
	hash   string
	data   []byte // canonical encoding
}

// verifyDoc checks a full signed document's identity binding (see
// ratingset.Verify) and returns its canonical encoding along with the
// asserted revision.
func verifyDoc(data []byte) (*verifiedDoc, error) {
	doc, id, err := ratingset.Verify(data)
	if err != nil {
		return nil, err
	}
	return &verifiedDoc{userID: id.ID, rev: id.Revision, hash: doc.Info().Hash, data: doc.JSON()}, nil
}

// docIdentity returns the user id and asserted revision of a stored
// (already verified) document, without verifying the signature.
func docIdentity(doc []byte) (string, int, error) {
	id, err := ratingset.ReadIdentity(doc)
	return id.ID, id.Revision, err
}

// UserID derives a user's id from their public key, matching the
// server's derivation. A document's user id must always equal the id
// derived from the key that signed it.
func UserID(pub ed25519.PublicKey) string {
	return keys.UserID(pub)
}

func userIDFromKey(pub ed25519.PublicKey) string {
	return UserID(pub)
}

// splitLines splits JSONL bytes, tolerating a single trailing newline.
func splitLines(data []byte) [][]byte {
	lines := bytes.Split(data, []byte("\n"))
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	return lines
}
