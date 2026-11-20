package server

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/openrevs/revs/domain"
	"github.com/openrevs/revs/keys"
	jsonlines "github.com/openrevs/sjsonl-go"
)

// Publishing: see site/specs/publishing-v2.md.

const (
	kindPatch    = "patch"
	kindSnapshot = "snapshot"

	// ChannelMain is the server's primary feed: every user's latest
	// accepted document.
	ChannelMain = "main"
	// ChannelDirect holds only documents uploaded to this server
	// directly.
	ChannelDirect = "direct"

	// manifestFormat is the manifest format version.
	manifestFormat = 1
)

// channel is one publication channel: a name and the documents it
// should hold now.
type channel struct {
	name    string
	members func(ctx context.Context) ([]*domain.Document, error)
}

// AddChannel defines an extra channel, published alongside main and
// direct. It holds each user's latest accepted document for which
// include returns true; a user whose document stops passing is removed
// from the channel at the next cut. The name must be a plain path
// segment.
func (s *Server) AddChannel(name string, include func(*domain.Document) bool) error {
	if name == "" || strings.ContainsAny(name, `/\.`) {
		return fmt.Errorf("invalid channel name %q", name)
	}

	s.cutMu.Lock()
	defer s.cutMu.Unlock()

	for _, ch := range s.channels {
		if ch.name == name {
			return fmt.Errorf("channel %q already exists", name)
		}
	}
	s.channels = append(s.channels, channel{name: name, members: func(ctx context.Context) ([]*domain.Document, error) {
		docs, err := s.dal.LatestDocuments(ctx)
		if err != nil {
			return nil, err
		}
		return slices.DeleteFunc(docs, func(d *domain.Document) bool { return !include(d) }), nil
	}})

	return nil
}

func (s *Server) defaultChannels() []channel {
	return []channel{
		{name: ChannelMain, members: s.dal.LatestDocuments},
		{name: ChannelDirect, members: s.dal.LatestDirectDocuments},
	}
}

// publicationIndex is /v1/index.json.
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

// Manifest lines. A manifest is an sjsonl document signed with the
// server key; sjsonl sorts lines by hash, so consumers tell the kinds
// apart by their fields.

// manifestMeta is a manifest's one metadata line, naming the
// publication it describes.
type manifestMeta struct {
	Manifest string  `json:"manifest"` // kindSnapshot or kindPatch
	ServerID string  `json:"server_id"`
	Channel  string  `json:"channel"`
	Epoch    int     `json:"epoch,omitempty"` // snapshots
	Range    *[2]int `json:"range,omitempty"` // patches
	Created  string  `json:"created"`
	Format   int     `json:"format"`
}

// manifestEntry describes one archive entry: the document it yields,
// the base it patches (sjsonl patches only), and the part holding it.
type manifestEntry struct {
	ID       string `json:"id"`
	Rev      int    `json:"rev"`
	Hash     string `json:"hash"`
	BaseRev  int    `json:"base_rev,omitempty"`
	BaseHash string `json:"base_hash,omitempty"`
	Part     int    `json:"part"`
}

// manifestRemoval lists one user that left the channel in a patch's
// range.
type manifestRemoval struct {
	Removed string `json:"removed"`
}

func (s *Server) serverID() string {
	return keys.UserID(s.key.Public().(ed25519.PublicKey))
}

// Cut publishes a new epoch for every channel whose contents changed
// since its last epoch, and returns the epochs cut by channel name.
// Channels with no changes are left alone and absent from the result.
func (s *Server) Cut(ctx context.Context) (map[string]int, error) {
	s.cutMu.Lock()
	defer s.cutMu.Unlock()

	cut := map[string]int{}
	var errs []error
	for _, ch := range s.channels {
		epoch, err := s.cutChannel(ctx, ch)
		if err != nil {
			errs = append(errs, fmt.Errorf("channel %s: %w", ch.name, err))
			continue
		}
		if epoch > 0 {
			cut[ch.name] = epoch
		}
	}

	// The index is regenerated even if a channel failed, so the others'
	// new epochs are advertised.
	if len(cut) > 0 {
		if err := s.writeIndex(ctx); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return cut, fmt.Errorf("cut: %v", errs)
	}
	return cut, nil
}

// cutChannel publishes the next epoch of one channel if its contents
// changed: the single-epoch patch, any compacted patches ending at the
// new epoch, and a snapshot when the cadence calls for one, each with
// its signed manifest. Archives and manifests are written before the
// epoch is committed to the database, so a crash leaves either no epoch
// or an epoch not yet in the index.
func (s *Server) cutChannel(ctx context.Context, ch channel) (int, error) {
	members, err := ch.members(ctx)
	if err != nil {
		return 0, err
	}

	cur, err := s.dal.LatestChannelEpoch(ctx, ch.name)
	if err != nil {
		return 0, err
	}
	prevDocs, err := s.dal.ChannelDocuments(ctx, ch.name, cur)
	if err != nil {
		return 0, err
	}
	prev := byUser(prevDocs)
	next := byUser(members)

	var changes []domain.ChannelChange
	for _, d := range members {
		if p, ok := prev[d.Userid]; !ok || p.Rev != d.Rev {
			rev := d.Rev
			changes = append(changes, domain.ChannelChange{Userid: d.Userid, Rev: &rev})
		}
	}
	for _, id := range slices.Sorted(maps.Keys(prev)) {
		if _, ok := next[id]; !ok {
			changes = append(changes, domain.ChannelChange{Userid: id})
		}
	}
	if len(changes) == 0 {
		return 0, nil
	}

	epoch := cur + 1
	created := now()
	dir := ch.name

	// Newest-first, so a mostly synced consumer's skippable entries
	// cluster at the end.
	slices.SortStableFunc(members, func(a, b *domain.Document) int {
		return cmp.Or(b.ReceivedTime.Compare(a.ReceivedTime), cmp.Compare(a.Userid, b.Userid))
	})

	var artifacts []*domain.Artifact

	parts, err := s.writePatch(dir, ch.name, cur, epoch, prev, members, created)
	if err != nil {
		return 0, err
	}
	artifacts = append(artifacts, &domain.Artifact{Kind: kindPatch, Start: cur, End: epoch, Parts: parts, Created: created})

	// Compacted patches cover aligned ranges [epoch-n, epoch], so a stale
	// consumer holding a boundary's state catches up in one step.
	for _, n := range s.compactEvery {
		if n <= 1 || epoch%n != 0 {
			continue
		}
		start := epoch - n
		startDocs, err := s.dal.ChannelDocuments(ctx, ch.name, start)
		if err != nil {
			return 0, err
		}
		parts, err := s.writePatch(dir, ch.name, start, epoch, byUser(startDocs), members, created)
		if err != nil {
			return 0, err
		}
		artifacts = append(artifacts, &domain.Artifact{Kind: kindPatch, Start: start, End: epoch, Parts: parts, Created: created})
	}

	if epoch == 1 || (s.snapshotEvery > 0 && epoch%s.snapshotEvery == 0) {
		parts, err := s.writeSnapshot(dir, ch.name, epoch, members, created)
		if err != nil {
			return 0, err
		}
		artifacts = append(artifacts, &domain.Artifact{Kind: kindSnapshot, Start: epoch, End: epoch, Parts: parts, Created: created})
	}

	if err := s.dal.RecordCut(ctx, ch.name, epoch, created, changes, artifacts); err != nil {
		return 0, err
	}

	slog.Info("cut epoch", "channel", ch.name, "epoch", epoch, "changes", len(changes), "artifacts", len(artifacts))

	return epoch, nil
}

func byUser(docs []*domain.Document) map[string]*domain.Document {
	m := make(map[string]*domain.Document, len(docs))
	for _, d := range docs {
		m[d.Userid] = d
	}
	return m
}

func docEntryName(userid string, rev int) string {
	return fmt.Sprintf("%s-%d.sjsonl", userid, rev)
}

func patchEntryName(userid string, base, target int) string {
	return fmt.Sprintf("%s-%d-%d.sjsonl", userid, base, target)
}

// writePatch publishes patch-<start>-<end> and its manifest, taking a
// consumer from the channel state from (at start) to the state to (at
// end). Changed users get an sjsonl patch from their rev at start, or
// the full document when that is smaller; users new to the channel get
// the full document; users that left are listed as removals in the
// manifest.
func (s *Server) writePatch(dir, chName string, start, end int, from map[string]*domain.Document, to []*domain.Document, created time.Time) (int, error) {
	base := fmt.Sprintf("patch-%d-%d", start, end)
	w, err := newArchiveWriter(s.pub, dir, base, s.partSize, created)
	if err != nil {
		return 0, err
	}
	defer w.Abort()

	var lines []any

	inTo := make(map[string]bool, len(to))
	for _, d := range to {
		inTo[d.Userid] = true
	}
	for _, id := range slices.Sorted(maps.Keys(from)) {
		if !inTo[id] {
			lines = append(lines, manifestRemoval{Removed: id})
		}
	}

	for _, d := range to {
		prev, had := from[d.Userid]
		if had && prev.Rev == d.Rev {
			continue
		}

		target, err := s.docs.Get(d.Hash)
		if err != nil {
			return 0, fmt.Errorf("loading document %s: %w", d.Hash, err)
		}

		entry := manifestEntry{ID: d.Userid, Rev: d.Rev, Hash: d.Hash}
		name, data := docEntryName(d.Userid, d.Rev), target
		if had {
			baseData, err := s.docs.Get(prev.Hash)
			if err != nil {
				return 0, fmt.Errorf("loading base document %s: %w", prev.Hash, err)
			}
			patch, err := jsonlines.Diff(baseData, target)
			if err != nil {
				return 0, fmt.Errorf("diffing %s: %w", d.Userid, err)
			}
			if len(patch) < len(target) {
				name, data = patchEntryName(d.Userid, prev.Rev, d.Rev), patch
				entry.BaseRev, entry.BaseHash = prev.Rev, prev.Hash
			}
		}

		entry.Part, err = w.Add(name, data)
		if err != nil {
			return 0, err
		}
		lines = append(lines, entry)
	}

	parts, err := w.Close()
	if err != nil {
		return 0, err
	}

	rng := [2]int{start, end}
	meta := manifestMeta{Manifest: kindPatch, Channel: chName, Range: &rng}
	if err := s.writeManifest(path.Join(dir, "patch-manifest-"+strings.TrimPrefix(base, "patch-")+".sjsonl.gz"), meta, created, lines); err != nil {
		return 0, err
	}

	return parts, nil
}

// writeSnapshot publishes snapshot-<epoch> and its manifest: the full
// document of every user in the channel at that epoch.
func (s *Server) writeSnapshot(dir, chName string, epoch int, docs []*domain.Document, created time.Time) (int, error) {
	w, err := newArchiveWriter(s.pub, dir, fmt.Sprintf("snapshot-%d", epoch), s.partSize, created)
	if err != nil {
		return 0, err
	}
	defer w.Abort()

	lines := make([]any, 0, len(docs))
	for _, d := range docs {
		data, err := s.docs.Get(d.Hash)
		if err != nil {
			return 0, fmt.Errorf("loading document %s: %w", d.Hash, err)
		}
		part, err := w.Add(docEntryName(d.Userid, d.Rev), data)
		if err != nil {
			return 0, err
		}
		lines = append(lines, manifestEntry{ID: d.Userid, Rev: d.Rev, Hash: d.Hash, Part: part})
	}

	parts, err := w.Close()
	if err != nil {
		return 0, err
	}

	meta := manifestMeta{Manifest: kindSnapshot, Channel: chName, Epoch: epoch}
	if err := s.writeManifest(path.Join(dir, fmt.Sprintf("snapshot-manifest-%d.sjsonl.gz", epoch)), meta, created, lines); err != nil {
		return 0, err
	}

	return parts, nil
}

// writeManifest signs a manifest with the server key and writes it,
// gzipped, to name in the publications (atomically, like the archives).
func (s *Server) writeManifest(name string, meta manifestMeta, created time.Time, lines []any) error {
	meta.ServerID = s.serverID()
	meta.Created = created.UTC().Format(time.RFC3339)
	meta.Format = manifestFormat

	var buf bytes.Buffer
	for _, line := range append([]any{meta}, lines...) {
		data, err := json.Marshal(line)
		if err != nil {
			return err
		}
		buf.Write(data)
		buf.WriteByte('\n')
	}

	signed, _, err := jsonlines.Sign(buf.Bytes(), s.key)
	if err != nil {
		return fmt.Errorf("signing manifest: %w", err)
	}

	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write(signed); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}

	return s.pub.WriteFile(name, gz.Bytes())
}

// writeIndex regenerates /v1/index.json from the database.
func (s *Server) writeIndex(ctx context.Context) error {
	idx := publicationIndex{
		Created:   now().Format(time.RFC3339),
		ServerID:  s.serverID(),
		ServerKey: keys.EncodePublic(s.key.Public().(ed25519.PublicKey)),
		Channels:  map[string]*channelIndex{},
	}

	for _, ch := range s.channels {
		epoch, err := s.dal.LatestChannelEpoch(ctx, ch.name)
		if err != nil {
			return err
		}
		arts, err := s.dal.ChannelArtifacts(ctx, ch.name)
		if err != nil {
			return err
		}

		ci := &channelIndex{Epoch: epoch, Snapshots: []snapshotInfo{}, Patches: []patchInfo{}}
		for _, a := range arts {
			created := a.Created.UTC().Format(time.RFC3339)
			switch a.Kind {
			case kindSnapshot:
				ci.Snapshots = append(ci.Snapshots, snapshotInfo{Epoch: a.End, Parts: a.Parts, Created: created})
			case kindPatch:
				ci.Patches = append(ci.Patches, patchInfo{Range: [2]int{a.Start, a.End}, Parts: a.Parts, Created: created})
			}
		}
		slices.SortFunc(ci.Snapshots, func(a, b snapshotInfo) int { return cmp.Compare(a.Epoch, b.Epoch) })
		// By end epoch; for the same end, finer ranges first.
		slices.SortFunc(ci.Patches, func(a, b patchInfo) int {
			return cmp.Or(cmp.Compare(a.Range[1], b.Range[1]), cmp.Compare(b.Range[0], a.Range[0]))
		})

		idx.Channels[ch.name] = ci
	}

	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}

	return s.pub.WriteFile("index.json", append(data, '\n'))
}
