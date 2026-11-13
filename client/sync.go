package client

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"

	jsonlines "github.com/openrevs/sjsonl-go"
)

// Result summarizes one Sync call.
type Result struct {
	// FromEpoch and Epoch are the channel epoch before and after.
	FromEpoch int
	Epoch     int

	// Bootstrapped reports that the corpus was (re)loaded from a
	// snapshot rather than reached purely by patches, and why: "fresh"
	// (nothing synced yet), "fallback" (no published patch could bring
	// the corpus forward), or "rebootstrap" (asked for).
	Bootstrapped    bool
	BootstrapReason string

	// Changed lists the users whose current document changed or who
	// left the channel, sorted.
	Changed []string
}

// Sync brings the local corpus up to the channel's current epoch.
//
// From its current epoch, it repeatedly applies the patch with the
// largest range starting there. A patch is applied only if the client
// holds the base of every sjsonl patch in it (any retained revision
// will do, not only the current one); otherwise the next smaller range
// is tried. When no patch starts at the current epoch, or none applies,
// it loads the newest snapshot and continues from that snapshot's
// epoch. A fresh corpus starts from the newest snapshot, if there is
// one.
//
// Each step is committed before the next, so an interrupted Sync
// resumes where it left off. A DivergedError means no published path
// leads forward from the local corpus; Rebootstrap starts over.
func (c *Client) Sync(ctx context.Context) (*Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.syncLocked(ctx, false)
}

// Rebootstrap discards the synced corpus and rebuilds it, from the
// newest snapshot if there is one, then syncs forward as usual. Blobs
// already stored are retained, and push state is untouched.
func (c *Client) Rebootstrap(ctx context.Context) (*Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.syncLocked(ctx, true)
}

func (c *Client) syncLocked(ctx context.Context, force bool) (*Result, error) {
	if err := c.ensureState(); err != nil {
		return nil, err
	}

	idx, err := c.fetchIndex(ctx)
	if err != nil {
		return nil, err
	}
	ch := idx.Channels[c.channel]
	if ch == nil {
		return nil, fmt.Errorf("client: server publishes no channel %q", c.channel)
	}

	res := &Result{FromEpoch: c.state.Epoch}
	changed := map[string]bool{}

	// A corpus synced from another channel is no base for this one.
	if c.state.Channel != c.channel && (c.state.Epoch > 0 || len(c.state.Heads) > 0) {
		force = true
	}

	if force {
		for id := range c.state.Heads {
			changed[id] = true
		}
		c.state.Heads = map[string]Head{}
		c.state.Epoch = 0
		res.BootstrapReason = "rebootstrap"
	}
	c.state.Channel = c.channel

	if ch.Epoch < c.state.Epoch {
		return nil, &DivergedError{
			Epoch:  c.state.Epoch,
			Reason: fmt.Sprintf("the server's channel %q is at epoch %d, behind the local corpus", c.channel, ch.Epoch),
		}
	}

	triedSnapshot := false
	if c.state.Epoch == 0 && len(ch.Snapshots) > 0 {
		if err := c.loadSnapshot(ctx, newestSnapshot(ch), changed); err != nil {
			return nil, err
		}
		triedSnapshot = true
		res.Bootstrapped = true
		if res.BootstrapReason == "" {
			res.BootstrapReason = "fresh"
		}
	} else if force {
		// Nothing to bootstrap from: the rebuilt corpus starts empty
		// and is reached by patches from epoch 0.
		if err := c.store.SaveState(c.state); err != nil {
			return nil, err
		}
		res.Bootstrapped = true
	}

	for c.state.Epoch < ch.Epoch {
		var missing *missingBaseError
		advanced := false
		for _, p := range patchesFrom(ch, c.state.Epoch) {
			err := c.applyPatch(ctx, p, changed)
			if errors.As(err, &missing) {
				continue // try a smaller range
			}
			if err != nil {
				return nil, err
			}
			if err := c.store.SaveState(c.state); err != nil {
				return nil, err
			}
			advanced = true
			break
		}
		if advanced {
			continue
		}

		if triedSnapshot || len(ch.Snapshots) == 0 {
			d := &DivergedError{Epoch: c.state.Epoch, Reason: "no published patch or snapshot brings the corpus forward"}
			if missing != nil {
				d.Reason = "patch base not held"
				d.UserID = missing.userID
				d.WantBase = missing.base
				d.HaveBase = c.state.Heads[missing.userID].Hash
			}
			return nil, d
		}
		if err := c.loadSnapshot(ctx, newestSnapshot(ch), changed); err != nil {
			return nil, err
		}
		triedSnapshot = true
		if !res.Bootstrapped {
			res.Bootstrapped = true
			res.BootstrapReason = "fallback"
		}
	}

	res.Epoch = c.state.Epoch
	res.Changed = slices.Sorted(maps.Keys(changed))

	return res, nil
}

func (c *Client) fetchIndex(ctx context.Context) (*publicationIndex, error) {
	data, err := c.fetch(ctx, "/v1/index.json")
	if err != nil {
		return nil, err
	}

	var idx publicationIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("client: index: %w", err)
	}

	return &idx, nil
}

func newestSnapshot(ch *channelIndex) snapshotInfo {
	return slices.MaxFunc(ch.Snapshots, func(a, b snapshotInfo) int { return cmp.Compare(a.Epoch, b.Epoch) })
}

// patchesFrom returns the usable patches starting at epoch, largest
// range first.
func patchesFrom(ch *channelIndex, epoch int) []patchInfo {
	var ps []patchInfo
	for _, p := range ch.Patches {
		if p.Range[0] == epoch && p.Range[1] > epoch && p.Range[1] <= ch.Epoch && p.Parts > 0 {
			ps = append(ps, p)
		}
	}
	slices.SortFunc(ps, func(a, b patchInfo) int { return cmp.Compare(b.Range[1], a.Range[1]) })
	return ps
}

// missingBaseError means a patch entry's base document isn't held, so
// the patch can't be applied; the caller tries another route.
type missingBaseError struct {
	userID string
	base   string
}

func (e *missingBaseError) Error() string {
	return fmt.Sprintf("client: patch base %s for user %s not held", e.base, e.userID)
}

// applyPatch fetches and applies one patch, all parts. It changes state
// (heads, epoch) only in memory and only once the whole patch applied;
// the caller commits.
func (c *Client) applyPatch(ctx context.Context, p patchInfo, changed map[string]bool) error {
	start, end := p.Range[0], p.Range[1]
	heads := maps.Clone(c.state.Heads)
	seen := map[string]bool{}
	var removed []string
	sawRemoved := false

	for part := 1; part <= p.Parts; part++ {
		path := fmt.Sprintf("/v1/%s/patch-%d-%d_%d.tar.gz", c.channel, start, end, part)
		err := c.fetchArchive(ctx, path, func(name string, r io.Reader) error {
			if name == removedFile {
				if part != 1 || sawRemoved {
					return fmt.Errorf("%s outside part 1 or repeated", removedFile)
				}
				sawRemoved = true
				data, err := io.ReadAll(r)
				if err != nil {
					return err
				}
				if err := json.Unmarshal(data, &removed); err != nil {
					return fmt.Errorf("%s: %w", removedFile, err)
				}
				for _, id := range removed {
					if !isUserID(id) {
						return fmt.Errorf("%s lists invalid user id %q", removedFile, id)
					}
				}
				return nil
			}

			en, err := parseEntryName(name)
			if err != nil {
				return err
			}
			if seen[en.userID] {
				return fmt.Errorf("multiple entries for user %s", en.userID)
			}
			seen[en.userID] = true

			if h, ok := heads[en.userID]; ok && h.Rev == en.rev {
				return nil // already held
			}

			data, err := io.ReadAll(r)
			if err != nil {
				return err
			}
			h, err := c.applyEntry(en, data)
			if err != nil {
				return err
			}
			heads[en.userID] = h
			return nil
		})
		if err != nil {
			var missing *missingBaseError
			if errors.As(err, &missing) {
				return missing
			}
			return err
		}
	}

	for _, id := range removed {
		if seen[id] {
			return fmt.Errorf("client: patch %d-%d both removes and updates user %s", start, end, id)
		}
		if _, ok := heads[id]; ok {
			delete(heads, id)
			changed[id] = true
		}
	}
	for id := range seen {
		if heads[id] != c.state.Heads[id] {
			changed[id] = true
		}
	}

	c.state.Heads = heads
	c.state.Epoch = end

	return nil
}

// applyEntry turns one patch entry into a verified, stored document:
// full docs stand alone, sjsonl patches apply to the base they name,
// which may be any document the store holds.
func (c *Client) applyEntry(en entryName, data []byte) (Head, error) {
	ctl, err := peekControl(data)
	if err != nil {
		return Head{}, err
	}

	switch {
	case ctl.SJSONL == "doc" && !en.isPatch:
		return c.ingestDoc(en, data)

	case ctl.SJSONL == "patch" && en.isPatch:
		base, err := c.store.GetBlob(ctl.Base)
		if errors.Is(err, ErrBlobNotFound) {
			return Head{}, &missingBaseError{userID: en.userID, base: ctl.Base}
		}
		if err != nil {
			return Head{}, fmt.Errorf("client: loading base document %s: %w", ctl.Base, err)
		}
		if id, rev, err := docIdentity(base); err != nil || id != en.userID || rev != en.baseRev {
			return Head{}, fmt.Errorf("client: patch for user %s rev %d applies to a document that isn't that revision",
				en.userID, en.baseRev)
		}
		doc, _, err := jsonlines.Patch(base, data)
		if err != nil {
			return Head{}, fmt.Errorf("client: applying patch for user %s: %w", en.userID, err)
		}
		h, err := c.ingestDoc(en, doc)
		if err != nil {
			return Head{}, err
		}
		if ctl.Target != "" && ctl.Target != h.Hash {
			return Head{}, fmt.Errorf("client: patch for user %s yields %s, header says %s", en.userID, h.Hash, ctl.Target)
		}
		return h, nil

	default:
		return Head{}, fmt.Errorf("client: entry for user %s is an sjsonl %q, but its name says otherwise", en.userID, ctl.SJSONL)
	}
}

// ingestDoc verifies a full document and stores it. This is the trust
// boundary on the pull side: the author's signature must be valid, and
// the user id and revision in the entry name must match the contents.
// (sjsonl's Patch does not verify the result's signature itself, so
// re-parsing here is what makes patched documents trustworthy.)
func (c *Client) ingestDoc(en entryName, data []byte) (Head, error) {
	doc, err := verifyDoc(data)
	if err != nil {
		return Head{}, fmt.Errorf("client: document for user %s: %w", en.userID, err)
	}
	if doc.userID != en.userID || doc.rev != en.rev {
		return Head{}, fmt.Errorf("client: entry named for user %s rev %d holds user %s rev %d",
			en.userID, en.rev, doc.userID, doc.rev)
	}

	if err := c.store.PutBlob(doc.hash, doc.data); err != nil {
		return Head{}, err
	}

	return Head{Rev: doc.rev, Hash: doc.hash}, nil
}

// loadSnapshot replaces the corpus with a snapshot's and commits.
// Entries for revisions already held are skipped without parsing.
func (c *Client) loadSnapshot(ctx context.Context, snap snapshotInfo, changed map[string]bool) error {
	heads := make(map[string]Head)

	for part := 1; part <= snap.Parts; part++ {
		path := fmt.Sprintf("/v1/%s/snapshot-%d_%d.tar.gz", c.channel, snap.Epoch, part)
		err := c.fetchArchive(ctx, path, func(name string, r io.Reader) error {
			en, err := parseEntryName(name)
			if err != nil {
				return err
			}
			if en.isPatch {
				return fmt.Errorf("snapshot holds a patch entry %q", name)
			}
			if _, dup := heads[en.userID]; dup {
				return fmt.Errorf("multiple entries for user %s", en.userID)
			}

			// Skip revisions already held, unless the document itself
			// has gone missing: a snapshot is also how a corpus
			// repairs itself.
			if h, ok := c.state.Heads[en.userID]; ok && h.Rev == en.rev {
				if _, err := c.store.GetBlob(h.Hash); err == nil {
					heads[en.userID] = h
					return nil
				}
			}

			data, err := io.ReadAll(r)
			if err != nil {
				return err
			}
			if ctl, err := peekControl(data); err != nil {
				return err
			} else if ctl.SJSONL != "doc" {
				return fmt.Errorf("snapshot entry %q is an sjsonl %q", name, ctl.SJSONL)
			}
			h, err := c.ingestDoc(en, data)
			if err != nil {
				return err
			}
			heads[en.userID] = h
			return nil
		})
		if err != nil {
			return err
		}
	}

	for id, h := range heads {
		if old, ok := c.state.Heads[id]; !ok || old != h {
			changed[id] = true
		}
	}
	for id := range c.state.Heads {
		if _, ok := heads[id]; !ok {
			changed[id] = true
		}
	}

	c.state.Heads = heads
	c.state.Epoch = snap.Epoch

	return c.store.SaveState(c.state)
}
