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
	"strconv"
	"strings"

	"github.com/openrevs/revs/keys"
	jsonlines "github.com/openrevs/sjsonl-go"
)

// The publication format: see specs/publishing-v2.md in the website
// repo.

const (
	// DefaultChannel is the channel a client follows unless told
	// otherwise: the server's primary feed.
	DefaultChannel = "main"

	// maxEntrySize bounds a single archive entry. Servers cap documents
	// well below this; the bound only stops a hostile archive from
	// exhausting memory.
	maxEntrySize = 16 << 20
)

// publicationIndex is the server's /v1/index.json. It is unsigned: it
// advertises the server key, which is pinned on first use, and
// everything it points to is verified (manifests against the pinned
// server key, documents against their authors' signatures).
type publicationIndex struct {
	Created   string                   `json:"created"`
	ServerID  string                   `json:"server_id"`
	ServerKey string                   `json:"server_key"`
	Channels  map[string]*channelIndex `json:"channels"`
}

type channelIndex struct {
	Epoch     int            `json:"epoch"`
	Snapshots []snapshotInfo `json:"snapshots"`
	Patches   []patchInfo    `json:"patches"`
}

type snapshotInfo struct {
	Epoch   int    `json:"epoch"`
	Parts   int    `json:"parts"`
	Created string `json:"created"`
}

type patchInfo struct {
	Range   [2]int `json:"range"`
	Parts   int    `json:"parts"`
	Created string `json:"created"`
}

// entryName is a parsed archive entry name: <user_id>-<rev>.sjsonl for
// a full document, <user_id>-<base_rev>-<rev>.sjsonl for a patch. Names
// are advisory (they let a consumer skip entries it already has), but
// it is an error if they disagree with the contents.
type entryName struct {
	userID  string
	isPatch bool
	baseRev int
	rev     int
}

func parseEntryName(name string) (entryName, error) {
	var en entryName

	stem, ok := strings.CutSuffix(name, ".sjsonl")
	if !ok {
		return en, fmt.Errorf("client: unexpected archive entry %q", name)
	}
	fields := strings.Split(stem, "-")
	if len(fields) != 2 && len(fields) != 3 {
		return en, fmt.Errorf("client: malformed archive entry name %q", name)
	}
	if !isUserID(fields[0]) {
		return en, fmt.Errorf("client: archive entry %q has an invalid user ID", name)
	}
	en.userID = fields[0]

	revs := make([]int, 0, 2)
	for _, f := range fields[1:] {
		n, err := strconv.Atoi(f)
		if err != nil || n < 1 || strconv.Itoa(n) != f {
			return en, fmt.Errorf("client: archive entry %q has an invalid revision", name)
		}
		revs = append(revs, n)
	}
	if len(revs) == 2 {
		en.isPatch = true
		en.baseRev = revs[0]
		if revs[0] >= revs[1] {
			return en, fmt.Errorf("client: archive entry %q patches backwards", name)
		}
	}
	en.rev = revs[len(revs)-1]

	return en, nil
}

// isUserID reports whether s looks like a user id: lowercase hex.
func isUserID(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

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

// verifyDoc checks a full signed document: valid author signature, and
// an id line whose id matches the signing key. It returns the canonical
// encoding along with the asserted revision.
func verifyDoc(data []byte) (*verifiedDoc, error) {
	doc, err := jsonlines.Parse(data)
	if err != nil {
		return nil, err
	}
	info := doc.Info()
	canonical := doc.JSON()

	id, rev, err := docIdentity(canonical)
	if err != nil {
		return nil, err
	}
	if signer := userIDFromKey(info.SignerKey); signer != id {
		return nil, fmt.Errorf("document for user %s is signed by %s", id, signer)
	}

	return &verifiedDoc{userID: id, rev: rev, hash: info.Hash, data: canonical}, nil
}

// docIdentity returns the user id and asserted revision from a
// document's id line, without verifying the signature.
func docIdentity(doc []byte) (string, int, error) {
	lines := splitLines(doc)
	if len(lines) == 0 {
		return "", 0, errors.New("empty document")
	}

	found := false
	var id string
	var rev int
	for _, line := range lines[1:] { // line 0 is the sjsonl control line
		var probe struct {
			ID       *string `json:"id"`
			Revision *int    `json:"revision"`
		}
		if err := json.Unmarshal(line, &probe); err != nil {
			return "", 0, err
		}
		if probe.ID == nil {
			continue
		}
		if found {
			return "", 0, errors.New("document has multiple id lines")
		}
		found = true
		id = *probe.ID
		if probe.Revision != nil {
			rev = *probe.Revision
		}
	}
	if !found {
		return "", 0, errors.New("document has no id line")
	}
	if rev < 1 {
		return "", 0, fmt.Errorf("document for user %s has no valid revision", id)
	}

	return id, rev, nil
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
