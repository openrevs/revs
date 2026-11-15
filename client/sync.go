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

	// Equivocations lists users the server published at a revision the
	// client already held with a different document: the author signed
	// two documents with the same rev. The client follows the channel
	// (it now holds the published one); resolving the fork is up to the
	// caller.
	Equivocations []Equivocation
}

// Equivocation is two different signed documents for one user and rev.
type Equivocation struct {
	UserID    string
	Rev       int
	Held      string // hash of the document held before
	Published string // hash of the document the manifest lists
}

// Sync brings the local corpus up to the channel's current epoch.
//
// Every snapshot and patch is read through its manifest, signed by the
// server key (pinned on first use, see WithServerKey): the manifest
// says which documents a publication yields, so the client downloads
// only the archive parts holding documents it lacks, and checks every
// document it produces against the manifest's hash.
//
// From its current epoch, it repeatedly applies the patch with the
// largest range starting there. A patch is applied only if the client
// holds the base of every sjsonl patch in it it still needs (any
// retained revision will do, not only the current one), which the
// manifest shows before any archive is fetched; otherwise the next
// smaller range is tried. When no patch starts at the current epoch, or none applies,
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
	pin, err := c.resolvePin(idx)
	if err != nil {
		return nil, err
	}
	ch := idx.Channels[c.channel]
	if ch == nil {
		return nil, fmt.Errorf("client: server publishes no channel %q", c.channel)
	}
	pub := publisher{pin: pin, serverID: idx.ServerID}
	if c.state.ServerKey != pin {
		c.state.ServerKey = pin
		if err := c.store.SaveState(c.state); err != nil {
			return nil, err
		}
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
		if err := c.loadSnapshot(ctx, pub, newestSnapshot(ch), changed, res); err != nil {
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
			err := c.applyPatch(ctx, pub, p, changed, res)
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
		if err := c.loadSnapshot(ctx, pub, newestSnapshot(ch), changed, res); err != nil {
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

// publisher identifies the server whose manifests the client accepts:
// the pinned key that must sign them and the id they must name.
type publisher struct {
	pin      string
	serverID string
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

// holds reports whether the store holds the document a manifest entry
// yields. A blob found by hash that isn't the current head is checked
// to be that user's document at that rev, so a manifest can't pass off
// one user's document as another's.
func (c *Client) holds(e manifestEntry) (bool, error) {
	data, err := c.store.GetBlob(e.Hash)
	if errors.Is(err, ErrBlobNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if h, ok := c.state.Heads[e.ID]; ok && h.Hash == e.Hash && h.Rev == e.Rev {
		return true, nil
	}
	if id, rev, err := docIdentity(data); err != nil || id != e.ID || rev != e.Rev {
		return false, fmt.Errorf("client: manifest lists %s as user %s rev %d, but it isn't", e.Hash, e.ID, e.Rev)
	}
	return true, nil
}

// noteEquivocation records a manifest entry that contradicts the
// document held for the same user and rev.
func (c *Client) noteEquivocation(e manifestEntry, res *Result) {
	if h, ok := c.state.Heads[e.ID]; ok && h.Rev == e.Rev && h.Hash != e.Hash {
		res.Equivocations = append(res.Equivocations, Equivocation{UserID: e.ID, Rev: e.Rev, Held: h.Hash, Published: e.Hash})
	}
}

// plan sorts a manifest's entries into those already held, whose heads
// it sets, and those to fetch, returned by user id along with the parts
// that hold them. For patches it fails with a missingBaseError, before
// anything is downloaded, if a needed entry's base isn't held.
func (c *Client) plan(m *manifest, heads map[string]Head, res *Result) (map[string]manifestEntry, []int, error) {
	need := map[string]manifestEntry{}
	parts := map[int]bool{}

	for _, id := range slices.Sorted(maps.Keys(m.entries)) {
		e := m.entries[id]
		c.noteEquivocation(e, res)

		held, err := c.holds(e)
		if err != nil {
			return nil, nil, err
		}
		if held {
			heads[id] = Head{Rev: e.Rev, Hash: e.Hash}
			continue
		}

		if e.isPatch() {
			if _, err := c.store.GetBlob(e.BaseHash); errors.Is(err, ErrBlobNotFound) {
				return nil, nil, &missingBaseError{userID: id, base: e.BaseHash}
			} else if err != nil {
				return nil, nil, err
			}
		}
		need[id] = e
		parts[e.Part] = true
	}

	return need, slices.Sorted(maps.Keys(parts)), nil
}

// fetchEntries downloads the given parts of an archive and turns each
// needed entry into a verified, stored document, setting its head.
// Every entry must be listed in the manifest, in the part the manifest
// says, and every needed entry must turn up.
func (c *Client) fetchEntries(ctx context.Context, prefix string, m *manifest, need map[string]manifestEntry, parts []int, heads map[string]Head) error {
	got := map[string]bool{}

	for _, part := range parts {
		seen := map[string]bool{}
		path := fmt.Sprintf("%s_%d.tar.gz", prefix, part)
		err := c.fetchArchive(ctx, path, func(name string, r io.Reader) error {
			en, err := parseEntryName(name)
			if err != nil {
				return err
			}
			if seen[en.userID] {
				return fmt.Errorf("multiple entries for user %s", en.userID)
			}
			seen[en.userID] = true

			e, ok := m.entries[en.userID]
			if !ok {
				return fmt.Errorf("entry %q is not in the manifest", name)
			}
			if e.Part != part || e.Rev != en.rev || e.isPatch() != en.isPatch || e.BaseRev != en.baseRev {
				return fmt.Errorf("entry %q does not match its manifest entry", name)
			}
			if _, ok := need[en.userID]; !ok {
				return nil // already held
			}

			data, err := io.ReadAll(r)
			if err != nil {
				return err
			}
			h, err := c.applyEntry(en, e, data)
			if err != nil {
				return err
			}
			heads[en.userID] = h
			got[en.userID] = true
			return nil
		})
		if err != nil {
			return err
		}
	}

	for _, id := range slices.Sorted(maps.Keys(need)) {
		if !got[id] {
			return fmt.Errorf("client: %s: manifest lists user %s in part %d, but the archive lacks it", prefix, id, need[id].Part)
		}
	}

	return nil
}

// applyPatch applies one patch as its manifest directs: entries already
// held are taken as is, and only the parts holding the rest are
// downloaded (none, if the client holds everything). It changes state
// (heads, epoch) only in memory and only once the whole patch applied;
// the caller commits.
func (c *Client) applyPatch(ctx context.Context, pub publisher, p patchInfo, changed map[string]bool, res *Result) error {
	start, end := p.Range[0], p.Range[1]
	prefix := fmt.Sprintf("/v1/%s/patch-%d-%d", c.channel, start, end)

	m, err := c.fetchManifest(ctx, fmt.Sprintf("/v1/%s/patch-manifest-%d-%d.sjsonl.gz", c.channel, start, end), pub.pin,
		manifestWant{kind: "patch", serverID: pub.serverID, channel: c.channel, rng: p.Range, parts: p.Parts})
	if err != nil {
		return err
	}

	heads := maps.Clone(c.state.Heads)
	need, parts, err := c.plan(m, heads, res)
	if err != nil {
		return err
	}
	if err := c.fetchEntries(ctx, prefix, m, need, parts, heads); err != nil {
		var missing *missingBaseError
		if errors.As(err, &missing) {
			return missing
		}
		return err
	}

	for _, id := range m.removed {
		delete(heads, id)
	}
	for id := range m.entries {
		if heads[id] != c.state.Heads[id] {
			changed[id] = true
		}
	}
	for _, id := range m.removed {
		if _, ok := c.state.Heads[id]; ok {
			changed[id] = true
		}
	}

	c.state.Heads = heads
	c.state.Epoch = end

	return nil
}

// applyEntry turns one archive entry into a verified, stored document:
// full docs stand alone, sjsonl patches apply to the base they name,
// which may be any document the store holds. The result must be the
// document the manifest entry e lists.
func (c *Client) applyEntry(en entryName, e manifestEntry, data []byte) (Head, error) {
	ctl, err := peekControl(data)
	if err != nil {
		return Head{}, err
	}

	var h Head
	switch {
	case ctl.SJSONL == "doc" && !en.isPatch:
		h, err = c.ingestDoc(en, data)
		if err != nil {
			return Head{}, err
		}

	case ctl.SJSONL == "patch" && en.isPatch:
		if ctl.Base != e.BaseHash {
			return Head{}, fmt.Errorf("client: patch for user %s applies to %s, manifest says %s", en.userID, ctl.Base, e.BaseHash)
		}
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
		h, err = c.ingestDoc(en, doc)
		if err != nil {
			return Head{}, err
		}
		if ctl.Target != "" && ctl.Target != h.Hash {
			return Head{}, fmt.Errorf("client: patch for user %s yields %s, header says %s", en.userID, h.Hash, ctl.Target)
		}

	default:
		return Head{}, fmt.Errorf("client: entry for user %s is an sjsonl %q, but its name says otherwise", en.userID, ctl.SJSONL)
	}

	if h.Hash != e.Hash {
		return Head{}, fmt.Errorf("client: entry for user %s yields %s, manifest says %s", en.userID, h.Hash, e.Hash)
	}
	return h, nil
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

// loadSnapshot replaces the corpus with a snapshot's and commits. Its
// manifest lists every document in the channel at that epoch; only the
// parts holding documents the store lacks are downloaded, which is also
// how a snapshot repairs a corpus missing a document.
func (c *Client) loadSnapshot(ctx context.Context, pub publisher, snap snapshotInfo, changed map[string]bool, res *Result) error {
	m, err := c.fetchManifest(ctx, fmt.Sprintf("/v1/%s/snapshot-manifest-%d.sjsonl.gz", c.channel, snap.Epoch), pub.pin,
		manifestWant{kind: "snapshot", serverID: pub.serverID, channel: c.channel, epoch: snap.Epoch, parts: snap.Parts})
	if err != nil {
		return err
	}

	heads := make(map[string]Head, len(m.entries))
	need, parts, err := c.plan(m, heads, res)
	if err != nil {
		return err
	}
	prefix := fmt.Sprintf("/v1/%s/snapshot-%d", c.channel, snap.Epoch)
	if err := c.fetchEntries(ctx, prefix, m, need, parts, heads); err != nil {
		return err
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
