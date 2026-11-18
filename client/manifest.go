package client

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/openrevs/revs/keys"
	jsonlines "github.com/openrevs/sjsonl-go"
)

const (
	// manifestFormat is the manifest format version this client reads.
	manifestFormat = 1

	// maxManifestSize bounds a decompressed manifest. A snapshot
	// manifest lists every user in the channel at roughly 200 bytes
	// each, so this allows for a few million users while stopping a
	// hostile gzip bomb.
	maxManifestSize = 1 << 30
)

// manifest is a verified snapshot or patch manifest: signed by the
// pinned server key and checked against the publication it was fetched
// for.
type manifest struct {
	// entries holds one entry per document in the archive, by user id.
	entries map[string]manifestEntry
	// removed lists the users a patch removes from the channel.
	removed []string
}

type manifestEntry struct {
	ID       string `json:"id"`
	Rev      int    `json:"rev"`
	Hash     string `json:"hash"`
	BaseRev  int    `json:"base_rev,omitempty"`
	BaseHash string `json:"base_hash,omitempty"`
	Part     int    `json:"part"`
}

func (e manifestEntry) isPatch() bool { return e.BaseHash != "" }

// manifestWant is what a manifest must describe: the publication the
// client asked for. A validly signed manifest for anything else is
// rejected, so it can't be passed off as a different publication.
type manifestWant struct {
	kind     string // "snapshot" or "patch"
	serverID string
	channel  string
	epoch    int    // snapshots
	rng      [2]int // patches
	parts    int
}

// fetchManifest fetches, verifies and parses one manifest. pin is the
// server's pinned public key (base64); the manifest must be signed by
// it.
func (c *Client) fetchManifest(ctx context.Context, path, pin string, want manifestWant) (*manifest, error) {
	gz, err := c.fetch(ctx, path)
	if err != nil {
		return nil, err
	}
	m, err := parseManifest(gz, pin, want)
	if err != nil {
		return nil, fmt.Errorf("client: %s: %w", path, err)
	}
	return m, nil
}

func parseManifest(gz []byte, pin string, want manifestWant) (*manifest, error) {
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(zr, maxManifestSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxManifestSize {
		return nil, fmt.Errorf("manifest is over the %d byte limit", maxManifestSize)
	}

	doc, err := jsonlines.Parse(data)
	if err != nil {
		return nil, err
	}
	if got := keys.EncodePublic(doc.Info().SignerKey); got != pin {
		return nil, &KeyChangedError{Pinned: pin, Got: got}
	}

	m := &manifest{entries: map[string]manifestEntry{}}
	removed := map[string]bool{}
	sawMeta := false

	for _, line := range splitLines(doc.JSON())[1:] { // line 0 is the sjsonl control line
		var probe struct {
			Manifest *string `json:"manifest"`
			ID       *string `json:"id"`
			Removed  *string `json:"removed"`
		}
		if err := json.Unmarshal(line, &probe); err != nil {
			return nil, err
		}

		kinds := 0
		for _, set := range []bool{probe.Manifest != nil, probe.ID != nil, probe.Removed != nil} {
			if set {
				kinds++
			}
		}
		if kinds != 1 {
			return nil, fmt.Errorf("manifest line is not exactly one of metadata, entry or removal: %s", line)
		}

		switch {
		case probe.Manifest != nil:
			if sawMeta {
				return nil, errors.New("manifest has multiple metadata lines")
			}
			sawMeta = true
			if err := checkManifestMeta(line, want); err != nil {
				return nil, err
			}

		case probe.ID != nil:
			var e manifestEntry
			if err := json.Unmarshal(line, &e); err != nil {
				return nil, err
			}
			if err := checkManifestEntry(e, want); err != nil {
				return nil, err
			}
			if _, dup := m.entries[e.ID]; dup {
				return nil, fmt.Errorf("manifest lists user %s twice", e.ID)
			}
			m.entries[e.ID] = e

		case probe.Removed != nil:
			id := *probe.Removed
			if want.kind != "patch" {
				return nil, errors.New("snapshot manifest lists a removal")
			}
			if !isUserID(id) {
				return nil, fmt.Errorf("manifest removes invalid user ID %q", id)
			}
			if removed[id] {
				return nil, fmt.Errorf("manifest removes user %s twice", id)
			}
			removed[id] = true
			m.removed = append(m.removed, id)
		}
	}
	if !sawMeta {
		return nil, errors.New("manifest has no metadata line")
	}
	for _, id := range m.removed {
		if _, ok := m.entries[id]; ok {
			return nil, fmt.Errorf("manifest both removes and updates user %s", id)
		}
	}

	return m, nil
}

func checkManifestMeta(line []byte, want manifestWant) error {
	var meta struct {
		Manifest string  `json:"manifest"`
		ServerID string  `json:"server_id"`
		Channel  string  `json:"channel"`
		Epoch    *int    `json:"epoch"`
		Range    *[2]int `json:"range"`
		Format   int     `json:"format"`
	}
	if err := json.Unmarshal(line, &meta); err != nil {
		return err
	}

	if meta.Format != manifestFormat {
		return fmt.Errorf("unsupported manifest format %d", meta.Format)
	}
	if meta.Manifest != want.kind {
		return fmt.Errorf("manifest describes a %q, want a %q", meta.Manifest, want.kind)
	}
	if meta.ServerID != want.serverID {
		return fmt.Errorf("manifest is for server %s, want %s", meta.ServerID, want.serverID)
	}
	if meta.Channel != want.channel {
		return fmt.Errorf("manifest is for channel %q, want %q", meta.Channel, want.channel)
	}

	switch want.kind {
	case "snapshot":
		if meta.Epoch == nil || *meta.Epoch != want.epoch || meta.Range != nil {
			return fmt.Errorf("manifest does not describe snapshot %d", want.epoch)
		}
	case "patch":
		if meta.Range == nil || *meta.Range != want.rng || meta.Epoch != nil {
			return fmt.Errorf("manifest does not describe patch %d-%d", want.rng[0], want.rng[1])
		}
	}

	return nil
}

func checkManifestEntry(e manifestEntry, want manifestWant) error {
	if !isUserID(e.ID) {
		return fmt.Errorf("manifest entry has invalid user ID %q", e.ID)
	}
	if e.Rev < 1 || e.Hash == "" {
		return fmt.Errorf("manifest entry for user %s has no valid rev or hash", e.ID)
	}
	if e.Part < 1 || e.Part > want.parts {
		return fmt.Errorf("manifest entry for user %s is in part %d of %d", e.ID, e.Part, want.parts)
	}
	if (e.BaseRev == 0) != (e.BaseHash == "") {
		return fmt.Errorf("manifest entry for user %s has a partial base", e.ID)
	}
	if e.isPatch() {
		if want.kind != "patch" {
			return fmt.Errorf("snapshot manifest entry for user %s has a base", e.ID)
		}
		if e.BaseRev < 1 || e.BaseRev >= e.Rev {
			return fmt.Errorf("manifest entry for user %s patches rev %d to %d", e.ID, e.BaseRev, e.Rev)
		}
	}
	return nil
}
