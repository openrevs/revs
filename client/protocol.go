package client

import (
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"github.com/openrevs/revs/keys"
	jsonlines "github.com/openrevs/sjsonl-go"
)

const (
	kindPatch    = "patch"
	kindSnapshot = "snapshot"

	// manifestFormat is the artifact format version this client speaks.
	manifestFormat = 1
)

// serverIndex is the unsigned discovery document at /v1/index.json.
type serverIndex struct {
	CurrentEpoch        int    `json:"current_epoch"`
	OldestRetainedEpoch int    `json:"oldest_retained_epoch"`
	SnapshotEpochs      []int  `json:"snapshot_epochs"`
	ServerKey           string `json:"server_key"`
}

type manifestMeta struct {
	Epoch   int    `json:"epoch"`
	Kind    string `json:"kind"`
	Format  int    `json:"format"`
	Created string `json:"created"`
	Prev    string `json:"prev"`
	// Chunks is the snapshot chunk table, in file order: entry offsets
	// are global across the chunk sequence. Absent on patch manifests.
	Chunks []chunkInfo `json:"chunks"`
}

// chunkInfo describes one stored snapshot chunk (snapshot.<i>): its
// size and the hex sha256 of its stored bytes.
type chunkInfo struct {
	Size int    `json:"size"`
	Hash string `json:"hash"`
}

// manifestEntry locates one user's entry inside an artifact. For patches,
// offset/size address the decompressed stream; for snapshots they address
// the stored (compressed) file.
type manifestEntry struct {
	ID     string `json:"id"`
	Rev    int    `json:"rev"`
	Hash   string `json:"hash"`
	Offset int    `json:"offset"`
	Size   int    `json:"size"`
}

type manifest struct {
	meta    manifestMeta
	entries []manifestEntry
	hash    string // sjsonl doc hash, the value chained by successor manifests
}

// parseManifest verifies a signed manifest against the pinned server key
// and decodes its lines. sjsonl hash-sorts lines, so the metadata line
// is identified by its fields, not its position.
func parseManifest(data []byte, pin string) (*manifest, error) {
	doc, err := jsonlines.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("client: manifest: %w", err)
	}

	info := doc.Info()
	if signer := keys.EncodePublic(info.SignerKey); signer != pin {
		return nil, &KeyChangedError{Pinned: pin, Got: signer}
	}

	m := &manifest{hash: info.Hash}
	seenMeta := false
	seenIDs := map[string]bool{}

	lines := splitLines(data)
	for _, line := range lines[1:] { // line 0 is the sjsonl control line
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(line, &probe); err != nil {
			return nil, fmt.Errorf("client: manifest line: %w", err)
		}

		switch {
		case probe["kind"] != nil:
			if seenMeta {
				return nil, fmt.Errorf("client: manifest has multiple metadata lines")
			}
			seenMeta = true
			if err := json.Unmarshal(line, &m.meta); err != nil {
				return nil, fmt.Errorf("client: manifest metadata: %w", err)
			}
		case probe["id"] != nil:
			var e manifestEntry
			if err := json.Unmarshal(line, &e); err != nil {
				return nil, fmt.Errorf("client: manifest entry: %w", err)
			}
			if seenIDs[e.ID] {
				return nil, fmt.Errorf("client: manifest has duplicate entry for user %s", e.ID)
			}
			seenIDs[e.ID] = true
			m.entries = append(m.entries, e)
		default:
			return nil, fmt.Errorf("client: unrecognized manifest line: %s", line)
		}
	}

	if !seenMeta {
		return nil, fmt.Errorf("client: manifest has no metadata line")
	}
	if m.meta.Format != manifestFormat {
		return nil, fmt.Errorf("client: unsupported manifest format %d (this client speaks %d)", m.meta.Format, manifestFormat)
	}

	return m, nil
}

// checkManifest validates the parts of a manifest the caller can predict.
func checkManifest(m *manifest, kind string, epoch int) error {
	if m.meta.Kind != kind {
		return fmt.Errorf("client: manifest kind is %q, want %q", m.meta.Kind, kind)
	}
	if m.meta.Epoch != epoch {
		return fmt.Errorf("client: manifest is for epoch %d, want %d", m.meta.Epoch, epoch)
	}
	return nil
}

// entryControl is the subset of an entry's sjsonl control line the
// client dispatches on. Entry boundaries come only from the manifest;
// the control line just says whether the entry is a full doc or a patch,
// and for patches which base it applies to.
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

// chunkStarts returns each chunk's global start offset, with the total
// size appended as a final element.
func chunkStarts(chunks []chunkInfo) []int {
	starts := make([]int, len(chunks)+1)
	for i, c := range chunks {
		starts[i+1] = starts[i] + c.Size
	}
	return starts
}

// entryChunk returns the index of the chunk wholly containing e. Chunks
// split only at member boundaries, so an entry spanning chunks (or
// falling outside the table) means a malformed manifest.
func entryChunk(starts []int, e manifestEntry) (int, error) {
	i := sort.Search(len(starts)-1, func(i int) bool { return starts[i+1] > e.Offset })
	if e.Offset < 0 || e.Size <= 0 || i >= len(starts)-1 || e.Offset+e.Size > starts[i+1] {
		return 0, fmt.Errorf("client: snapshot entry for user %s (offset %d, size %d) does not lie within one chunk",
			e.ID, e.Offset, e.Size)
	}
	return i, nil
}

// sliceEntry cuts one manifest entry out of an artifact byte stream.
func sliceEntry(stream []byte, e manifestEntry) ([]byte, error) {
	if e.Offset < 0 || e.Size <= 0 || e.Offset+e.Size > len(stream) {
		return nil, fmt.Errorf("client: entry for user %s (offset %d, size %d) outside artifact of %d bytes",
			e.ID, e.Offset, e.Size, len(stream))
	}

	return stream[e.Offset : e.Offset+e.Size], nil
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

func gunzip(data []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer zr.Close()

	return io.ReadAll(zr)
}
