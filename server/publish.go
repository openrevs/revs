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
	"github.com/openrevs/revs/publication"
	jsonlines "github.com/openrevs/sjsonl-go"
)

// Publishing: see specs/publishing-v2.md in the website repo.

const (
	// ChannelMain is the server's primary feed: every user's latest
	// accepted document (publication.ChannelMain).
	ChannelMain = publication.ChannelMain
	// ChannelDirect holds only documents uploaded to this server
	// directly (publication.ChannelDirect).
	ChannelDirect = publication.ChannelDirect
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
	artifacts = append(artifacts, &domain.Artifact{Kind: publication.KindPatch, Start: cur, End: epoch, Parts: parts, Created: created})

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
		artifacts = append(artifacts, &domain.Artifact{Kind: publication.KindPatch, Start: start, End: epoch, Parts: parts, Created: created})
	}

	if epoch == 1 || (s.snapshotEvery > 0 && epoch%s.snapshotEvery == 0) {
		parts, err := s.writeSnapshot(dir, ch.name, epoch, members, created)
		if err != nil {
			return 0, err
		}
		artifacts = append(artifacts, &domain.Artifact{Kind: publication.KindSnapshot, Start: epoch, End: epoch, Parts: parts, Created: created})
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

// writePatch publishes patch-<start>-<end> and its manifest, taking a
// consumer from the channel state from (at start) to the state to (at
// end). Changed users get an sjsonl patch from their rev at start, or
// the full document when that is smaller; users new to the channel get
// the full document; users that left are listed as removals in the
// manifest.
func (s *Server) writePatch(dir, chName string, start, end int, from map[string]*domain.Document, to []*domain.Document, created time.Time) (int, error) {
	base := publication.PatchBase(start, end)
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
			lines = append(lines, publication.ManifestRemoval{Removed: id})
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

		entry := publication.ManifestEntry{ID: d.Userid, Rev: d.Rev, Hash: d.Hash}
		name, data := publication.EntryName{UserID: d.Userid, Rev: d.Rev}.String(), target
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
				name, data = publication.EntryName{UserID: d.Userid, BaseRev: prev.Rev, Rev: d.Rev}.String(), patch
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
	meta := publication.ManifestMeta{Manifest: publication.KindPatch, Channel: chName, Range: &rng}
	if err := s.writeManifest(path.Join(dir, publication.PatchManifestName(start, end)), meta, created, lines); err != nil {
		return 0, err
	}

	return parts, nil
}

// writeSnapshot publishes snapshot-<epoch> and its manifest: the full
// document of every user in the channel at that epoch.
func (s *Server) writeSnapshot(dir, chName string, epoch int, docs []*domain.Document, created time.Time) (int, error) {
	w, err := newArchiveWriter(s.pub, dir, publication.SnapshotBase(epoch), s.partSize, created)
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
		part, err := w.Add(publication.EntryName{UserID: d.Userid, Rev: d.Rev}.String(), data)
		if err != nil {
			return 0, err
		}
		lines = append(lines, publication.ManifestEntry{ID: d.Userid, Rev: d.Rev, Hash: d.Hash, Part: part})
	}

	parts, err := w.Close()
	if err != nil {
		return 0, err
	}

	meta := publication.ManifestMeta{Manifest: publication.KindSnapshot, Channel: chName, Epoch: &epoch}
	if err := s.writeManifest(path.Join(dir, publication.SnapshotManifestName(epoch)), meta, created, lines); err != nil {
		return 0, err
	}

	return parts, nil
}

// writeManifest signs a manifest with the server key and writes it,
// gzipped, to name in the publications (atomically, like the archives).
func (s *Server) writeManifest(name string, meta publication.ManifestMeta, created time.Time, lines []any) error {
	meta.ServerID = s.serverID()
	meta.Created = created.UTC().Format(time.RFC3339)
	meta.Format = publication.ManifestFormat

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

// writeIndex regenerates the publication index from the database.
func (s *Server) writeIndex(ctx context.Context) error {
	idx := publication.Index{
		Created:   now().Format(time.RFC3339),
		ServerID:  s.serverID(),
		ServerKey: keys.EncodePublic(s.key.Public().(ed25519.PublicKey)),
		Channels:  map[string]*publication.Channel{},
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

		ci := &publication.Channel{Epoch: epoch, Snapshots: []publication.Snapshot{}, Patches: []publication.Patch{}}
		for _, a := range arts {
			created := a.Created.UTC().Format(time.RFC3339)
			switch a.Kind {
			case publication.KindSnapshot:
				ci.Snapshots = append(ci.Snapshots, publication.Snapshot{Epoch: a.End, Parts: a.Parts, Created: created})
			case publication.KindPatch:
				ci.Patches = append(ci.Patches, publication.Patch{Range: [2]int{a.Start, a.End}, Parts: a.Parts, Created: created})
			}
		}
		slices.SortFunc(ci.Snapshots, func(a, b publication.Snapshot) int { return cmp.Compare(a.Epoch, b.Epoch) })
		// By end epoch; for the same end, finer ranges first.
		slices.SortFunc(ci.Patches, func(a, b publication.Patch) int {
			return cmp.Or(cmp.Compare(a.Range[1], b.Range[1]), cmp.Compare(b.Range[0], a.Range[0]))
		})

		idx.Channels[ch.name] = ci
	}

	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}

	return s.pub.WriteFile(publication.IndexName, append(data, '\n'))
}
