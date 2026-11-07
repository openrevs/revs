package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	jsonlines "github.com/kalafut/sjsonl"
)

// Result summarizes one Sync call.
type Result struct {
	// FromEpoch and Epoch are the synced epoch before and after.
	FromEpoch int
	Epoch     int

	// Bootstrapped reports that the corpus was (re)built from a
	// snapshot rather than reached purely by chained patches, and why:
	// "fresh", "stale", or "rebootstrap".
	Bootstrapped    bool
	BootstrapReason string

	// Changed lists the users whose current document changed, sorted.
	Changed []string
}

// Sync brings the local corpus up to the server's current epoch: it
// applies chained patches in order, bootstrapping from a snapshot first
// when the store is fresh or too stale to catch up. Each applied epoch
// is committed before the next, so an interrupted Sync resumes where it
// left off.
//
// A DivergedError means the local corpus no longer matches the chain;
// recover explicitly with Rebootstrap. A KeyChangedError means the
// server is not signing with the pinned key.
func (c *Client) Sync(ctx context.Context) (*Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.syncLocked(ctx, "")
}

// Rebootstrap rebuilds the corpus from the newest snapshot, then syncs
// forward as usual. It is the explicit recovery from a DivergedError:
// heads are replaced wholesale, while existing blobs are retained as
// history. The key pin and push state are untouched.
func (c *Client) Rebootstrap(ctx context.Context) (*Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.syncLocked(ctx, "rebootstrap")
}

func (c *Client) syncLocked(ctx context.Context, force string) (*Result, error) {
	if err := c.ensureState(); err != nil {
		return nil, err
	}

	idx, err := c.fetchIndex(ctx)
	if err != nil {
		return nil, err
	}

	pin, err := c.resolvePin(idx)
	if err != nil {
		return nil, err
	}
	// Commit the pin before consuming anything signed by it: trust on
	// first use is a commitment, not a per-sync choice.
	if c.state.ServerKey != pin {
		c.state.ServerKey = pin
		if err := c.store.SaveState(c.state); err != nil {
			return nil, err
		}
	}

	res := &Result{FromEpoch: c.state.Epoch, Epoch: c.state.Epoch}

	if idx.CurrentEpoch == 0 {
		return res, nil // nothing published yet
	}

	changed := map[string]bool{}

	reason := force
	if reason == "" && c.state.Epoch < idx.CurrentEpoch && c.state.Epoch+1 < idx.OldestRetainedEpoch {
		// The patches between our epoch and the horizon are gone;
		// catching up by chain is impossible. Fresh and stale stores
		// bootstrap by design.
		if c.state.Epoch == 0 {
			reason = "fresh"
		} else {
			reason = "stale"
		}
	}

	if reason != "" {
		if err := c.bootstrap(ctx, idx, pin, changed); err != nil {
			return nil, err
		}
		res.Bootstrapped = true
		res.BootstrapReason = reason
	}

	for e := c.state.Epoch + 1; e <= idx.CurrentEpoch; e++ {
		if err := c.applyEpoch(ctx, e, pin, changed); err != nil {
			return nil, err
		}
		if err := c.store.SaveState(c.state); err != nil {
			return nil, err
		}
	}

	res.Epoch = c.state.Epoch
	res.Changed = slices.Sorted(maps.Keys(changed))

	return res, nil
}

func (c *Client) fetchIndex(ctx context.Context) (*serverIndex, error) {
	data, err := c.fetch(ctx, "/v1/index.json")
	if err != nil {
		return nil, err
	}

	var idx serverIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("client: index: %w", err)
	}

	return &idx, nil
}

// applyEpoch fetches and applies the patch for one epoch. It mutates
// state (heads, epoch, chain hash) only in memory; the caller commits.
func (c *Client) applyEpoch(ctx context.Context, epoch int, pin string, changed map[string]bool) error {
	manData, err := c.fetch(ctx, fmt.Sprintf("/v1/epochs/%d/manifest", epoch))
	if err != nil {
		return err
	}
	man, err := parseManifest(manData, pin)
	if err != nil {
		return err
	}
	if err := checkManifest(man, kindPatch, epoch); err != nil {
		return err
	}
	if man.meta.Prev != c.state.ManifestHash {
		return &DivergedError{
			Epoch:    epoch,
			Reason:   "manifest chain mismatch",
			WantBase: man.meta.Prev,
			HaveBase: c.state.ManifestHash,
		}
	}

	compressed, err := c.fetch(ctx, fmt.Sprintf("/v1/epochs/%d/patch", epoch))
	if err != nil {
		return err
	}
	// Patches are one solid gzip stream; manifest offsets address the
	// decompressed bytes.
	stream, err := gunzip(compressed)
	if err != nil {
		return fmt.Errorf("client: epoch %d patch: %w", epoch, err)
	}

	for _, e := range man.entries {
		if err := c.applyPatchEntry(epoch, e, stream); err != nil {
			return err
		}
		changed[e.ID] = true
	}

	c.state.Epoch = epoch
	c.state.ManifestHash = man.hash

	return nil
}

// applyPatchEntry applies one manifest entry of a patch artifact:
// full docs replace, sjsonl patches apply against the held base.
func (c *Client) applyPatchEntry(epoch int, e manifestEntry, stream []byte) error {
	seg, err := sliceEntry(stream, e)
	if err != nil {
		return err
	}

	ctl, err := peekControl(seg)
	if err != nil {
		return err
	}

	head, held := c.state.Heads[e.ID]
	if held && e.Rev <= head.Rev {
		return fmt.Errorf("client: epoch %d publishes rev %d for user %s, but rev %d is already held",
			epoch, e.Rev, e.ID, head.Rev)
	}

	var doc []byte
	switch ctl.SJSONL {
	case "doc":
		doc = seg
	case "patch":
		if !held {
			return &DivergedError{Epoch: epoch, UserID: e.ID, Reason: "patch for a user with no local document"}
		}
		if ctl.Base != head.Hash {
			return &DivergedError{
				Epoch: epoch, UserID: e.ID, Reason: "patch base mismatch",
				WantBase: ctl.Base, HaveBase: head.Hash,
			}
		}
		base, err := c.store.GetBlob(head.Hash)
		if err != nil {
			return fmt.Errorf("client: loading base document %s: %w", head.Hash, err)
		}
		doc, _, err = jsonlines.Patch(base, seg)
		if err != nil {
			return fmt.Errorf("client: applying patch for user %s: %w", e.ID, err)
		}
	default:
		return fmt.Errorf("client: entry for user %s has unknown sjsonl type %q", e.ID, ctl.SJSONL)
	}

	return c.ingestDoc(e, doc)
}

// ingestDoc verifies a full document against its signed manifest entry
// and stores it. This is the trust boundary on the pull side: author
// signature valid, doc hash exactly what the manifest promised, and the
// signer's derived id matching the entry. (sjsonl's Patch does not yet
// verify the result's signature itself, so re-parsing here is what
// makes patched documents trustworthy.)
func (c *Client) ingestDoc(e manifestEntry, data []byte) error {
	doc, err := jsonlines.Parse(data)
	if err != nil {
		return fmt.Errorf("client: document for user %s: %w", e.ID, err)
	}

	info := doc.Info()
	if info.Hash != e.Hash {
		return fmt.Errorf("client: document for user %s hashes to %s, manifest says %s", e.ID, info.Hash, e.Hash)
	}
	if id := userIDFromKey(info.SignerKey); id != e.ID {
		return fmt.Errorf("client: document signed by %s but manifest entry is for %s", id, e.ID)
	}

	// Store the canonical encoding, matching the server's blobs.
	if err := c.store.PutBlob(info.Hash, doc.JSON()); err != nil {
		return err
	}

	c.state.Heads[e.ID] = Head{Rev: e.Rev, Hash: e.Hash}

	return nil
}

// bootstrap replaces the corpus from the newest snapshot and commits.
// Existing blobs are never deleted; heads are replaced wholesale.
func (c *Client) bootstrap(ctx context.Context, idx *serverIndex, pin string, changed map[string]bool) error {
	if len(idx.SnapshotEpochs) == 0 {
		return errNoSnapshot
	}
	epoch := slices.Max(idx.SnapshotEpochs)

	manData, err := c.fetch(ctx, fmt.Sprintf("/v1/snapshots/%d/manifest", epoch))
	if err != nil {
		return err
	}
	man, err := parseManifest(manData, pin)
	if err != nil {
		return err
	}
	if err := checkManifest(man, kindSnapshot, epoch); err != nil {
		return err
	}

	// Snapshot entries are individually gzipped members packed into
	// chunk files; manifest offsets are global across the chunk
	// sequence. Group entries by containing chunk so each chunk is
	// fetched once and released before the next.
	starts := chunkStarts(man.meta.Chunks)
	byChunk := make([][]manifestEntry, len(man.meta.Chunks))
	for _, e := range man.entries {
		i, err := entryChunk(starts, e)
		if err != nil {
			return err
		}
		byChunk[i] = append(byChunk[i], e)
	}

	heads := make(map[string]Head, len(man.entries))
	oldHeads := c.state.Heads
	c.state.Heads = heads

	for i, ents := range byChunk {
		if len(ents) == 0 {
			continue
		}
		chunk, err := c.fetchChunk(ctx, epoch, i, man.meta.Chunks[i])
		if err != nil {
			c.state.Heads = oldHeads
			return err
		}
		for _, e := range ents {
			// In bounds by construction: entryChunk proved containment
			// and fetchChunk proved the chunk is table-sized.
			member := chunk[e.Offset-starts[i] : e.Offset-starts[i]+e.Size]
			doc, err := gunzip(member)
			if err != nil {
				c.state.Heads = oldHeads
				return fmt.Errorf("client: snapshot entry for user %s: %w", e.ID, err)
			}
			if err := c.ingestDoc(e, doc); err != nil {
				c.state.Heads = oldHeads
				return err
			}
		}
	}

	for id, h := range heads {
		if old, ok := oldHeads[id]; !ok || old != h {
			changed[id] = true
		}
	}
	for id := range oldHeads {
		if _, ok := heads[id]; !ok {
			changed[id] = true
		}
	}

	// A snapshot materializes the corpus at an existing epoch; its
	// manifest's prev is that epoch's patch-manifest hash, which is
	// exactly what the next patch in the chain will name.
	c.state.Epoch = epoch
	c.state.ManifestHash = man.meta.Prev

	return c.store.SaveState(c.state)
}

// fetchChunk fetches snapshot chunk i and verifies it against the
// signed manifest's chunk table before any of its members are used.
func (c *Client) fetchChunk(ctx context.Context, epoch, i int, want chunkInfo) ([]byte, error) {
	data, err := c.fetch(ctx, fmt.Sprintf("/v1/snapshots/%d/snapshot.%d", epoch, i))
	if err != nil {
		return nil, err
	}
	if len(data) != want.Size {
		return nil, fmt.Errorf("client: snapshot chunk %d is %d bytes, manifest says %d", i, len(data), want.Size)
	}
	if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != want.Hash {
		return nil, fmt.Errorf("client: snapshot chunk %d does not match its manifest hash", i)
	}
	return data, nil
}
