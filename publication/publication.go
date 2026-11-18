// Package publication is the revs publication format
// (specs/publishing-v2.md in the website repo): the index, manifest
// lines, and file and archive entry names a server publishes and a
// client reads. It is shared by both halves so they can't drift apart,
// and it does no I/O.
package publication

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/openrevs/revs/keys"
)

const (
	// ChannelMain is a server's primary feed: every user's latest
	// accepted document. Clients follow it unless told otherwise.
	ChannelMain = "main"
	// ChannelDirect holds only the documents uploaded to a server
	// directly.
	ChannelDirect = "direct"

	// ManifestFormat is the manifest format version.
	ManifestFormat = 1

	// The publication kinds, as a manifest's metadata line names them.
	KindSnapshot = "snapshot"
	KindPatch    = "patch"

	// IndexName is the index's file name, at the publication root
	// (served as /v1/index.json).
	IndexName = "index.json"
)

// Index is the publication index. It is unsigned: it advertises the
// server key, which consumers pin on first use, and everything it
// points to is verified (manifests against the pinned server key,
// documents against their authors' signatures).
type Index struct {
	Created   string              `json:"created"`
	ServerID  string              `json:"server_id"`
	ServerKey string              `json:"server_key"`
	Channels  map[string]*Channel `json:"channels"`
}

// Channel is one channel's entry in the index: its current epoch and
// the snapshots and patches it publishes.
type Channel struct {
	Epoch     int        `json:"epoch"`
	Snapshots []Snapshot `json:"snapshots"`
	Patches   []Patch    `json:"patches"`
}

// Snapshot describes a published snapshot: every document in the
// channel at Epoch, in Parts archive parts.
type Snapshot struct {
	Epoch   int    `json:"epoch"`
	Parts   int    `json:"parts"`
	Created string `json:"created"`
}

// Patch describes a published patch taking a consumer from the channel
// at Range[0] to the channel at Range[1], in Parts archive parts.
type Patch struct {
	Range   [2]int `json:"range"`
	Parts   int    `json:"parts"`
	Created string `json:"created"`
}

// Manifest lines. A manifest is an sjsonl document signed with the
// server key; sjsonl sorts lines by hash, so consumers tell the kinds
// apart by their fields: "manifest" for the metadata line, "id" for
// entries, "removed" for removals.

// ManifestMeta is a manifest's one metadata line, naming the
// publication it describes. Snapshots set Epoch, patches set Range.
type ManifestMeta struct {
	Manifest string  `json:"manifest"` // KindSnapshot or KindPatch
	ServerID string  `json:"server_id"`
	Channel  string  `json:"channel"`
	Epoch    *int    `json:"epoch,omitempty"`
	Range    *[2]int `json:"range,omitempty"`
	Created  string  `json:"created"`
	Format   int     `json:"format"`
}

// ManifestEntry describes one archive entry: the document it yields,
// the base it patches (sjsonl patches only), and the part holding it.
type ManifestEntry struct {
	ID       string `json:"id"`
	Rev      int    `json:"rev"`
	Hash     string `json:"hash"`
	BaseRev  int    `json:"base_rev,omitempty"`
	BaseHash string `json:"base_hash,omitempty"`
	Part     int    `json:"part"`
}

// IsPatch reports whether the entry is an sjsonl patch rather than a
// full document.
func (e ManifestEntry) IsPatch() bool { return e.BaseHash != "" }

// ManifestRemoval lists one user that left the channel in a patch's
// range.
type ManifestRemoval struct {
	Removed string `json:"removed"`
}

// File names, relative to a channel's directory (itself named after the
// channel, under the publication root). An archive is published in one
// or more parts, <base>_<n>.tar.gz, numbered from 1.

// SnapshotBase is the base name of snapshot epoch's archive parts.
func SnapshotBase(epoch int) string { return fmt.Sprintf("snapshot-%d", epoch) }

// PatchBase is the base name of the patch start→end's archive parts.
func PatchBase(start, end int) string { return fmt.Sprintf("patch-%d-%d", start, end) }

// PartName is the file name of an archive's part n.
func PartName(base string, n int) string { return fmt.Sprintf("%s_%d.tar.gz", base, n) }

// SnapshotManifestName is the file name of snapshot epoch's manifest.
func SnapshotManifestName(epoch int) string {
	return fmt.Sprintf("snapshot-manifest-%d.sjsonl.gz", epoch)
}

// PatchManifestName is the file name of the patch start→end's manifest.
func PatchManifestName(start, end int) string {
	return fmt.Sprintf("patch-manifest-%d-%d.sjsonl.gz", start, end)
}

// EntryName is an archive entry's name: <user_id>-<rev>.sjsonl for a
// full document, <user_id>-<base_rev>-<rev>.sjsonl for a patch. Names
// are advisory (they let a consumer skip entries it already has), but
// it is an error if they disagree with the contents.
type EntryName struct {
	UserID  string
	BaseRev int // 0 for a full document
	Rev     int
}

// IsPatch reports whether the entry is an sjsonl patch.
func (n EntryName) IsPatch() bool { return n.BaseRev > 0 }

func (n EntryName) String() string {
	if n.IsPatch() {
		return fmt.Sprintf("%s-%d-%d.sjsonl", n.UserID, n.BaseRev, n.Rev)
	}
	return fmt.Sprintf("%s-%d.sjsonl", n.UserID, n.Rev)
}

// ParseEntryName parses and checks an archive entry name. Revisions
// must be positive and canonically written, and a patch must go
// forward.
func ParseEntryName(name string) (EntryName, error) {
	var en EntryName

	stem, ok := strings.CutSuffix(name, ".sjsonl")
	if !ok {
		return en, fmt.Errorf("unexpected archive entry %q", name)
	}
	fields := strings.Split(stem, "-")
	if len(fields) != 2 && len(fields) != 3 {
		return en, fmt.Errorf("malformed archive entry name %q", name)
	}
	if !keys.ValidUserID(fields[0]) {
		return en, fmt.Errorf("archive entry %q has an invalid user ID", name)
	}
	en.UserID = fields[0]

	revs := make([]int, 0, 2)
	for _, f := range fields[1:] {
		n, err := strconv.Atoi(f)
		if err != nil || n < 1 || strconv.Itoa(n) != f {
			return en, fmt.Errorf("archive entry %q has an invalid revision", name)
		}
		revs = append(revs, n)
	}
	if len(revs) == 2 {
		if revs[0] >= revs[1] {
			return en, fmt.Errorf("archive entry %q patches backwards", name)
		}
		en.BaseRev = revs[0]
	}
	en.Rev = revs[len(revs)-1]

	return en, nil
}
