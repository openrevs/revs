package revs

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/openrevs/revs/domain"
	"github.com/openrevs/revs/util"
	jsonlines "github.com/openrevs/sjsonl-go"
	"log/slog"
)

const (
	kindPatch    = "patch"
	kindSnapshot = "snapshot"

	// manifestFormat gates artifact format evolution (compression scheme,
	// dictionaries, ...).
	manifestFormat = 1
)

// manifestMeta is the manifest's metadata line. Prev chains manifests
// together: for a patch manifest it is the previous epoch's manifest
// hash; for a snapshot manifest it is the hash of the patch manifest of
// the epoch the snapshot materializes, tying it into the same chain.
type manifestMeta struct {
	Epoch   int    `json:"epoch"`
	Kind    string `json:"kind"`
	Format  int    `json:"format"`
	Created string `json:"created"`
	Prev    string `json:"prev,omitempty"`
	// Chunks is the snapshot chunk table, in file order: entry offsets
	// are global across the chunk sequence, and each chunk is verifiable
	// standalone via the sha256 of its stored bytes. Absent on patch
	// manifests.
	Chunks []chunkInfo `json:"chunks,omitempty"`
}

// chunkInfo describes one stored snapshot chunk (snapshot.<i>).
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

// serverIndex is the unsigned discovery document at /v1/index.json.
// Everything it points to is signed, so tampering can only reference
// artifacts that fail verification or withhold newer epochs.
type serverIndex struct {
	CurrentEpoch        int    `json:"current_epoch"`
	OldestRetainedEpoch int    `json:"oldest_retained_epoch"`
	SnapshotEpochs      []int  `json:"snapshot_epochs"`
	ServerKey           string `json:"server_key"`
}

// Cut publishes a patch epoch covering every document accepted since the
// last cut, plus a snapshot when the cadence calls for one. It returns
// the epoch number cut, or 0 if nothing changed.
func (s *Server) Cut(ctx context.Context) (int, error) {
	s.cutMu.Lock()
	defer s.cutMu.Unlock()

	latest, err := s.dal.LatestDocuments(ctx)
	if err != nil {
		return 0, err
	}

	heads, err := s.dal.PublishedHeads(ctx)
	if err != nil {
		return 0, err
	}

	type change struct {
		doc     *domain.Document
		baseRev int
		hasBase bool
	}

	var changes []change
	for _, doc := range latest {
		rev, published := heads[doc.Userid]
		if published && rev == doc.Rev {
			continue
		}
		changes = append(changes, change{doc: doc, baseRev: rev, hasBase: published})
	}

	if len(changes) == 0 {
		return 0, nil
	}

	epoch := 1
	prevHash := ""
	if prev, err := s.dal.LatestPatchEpoch(ctx); err != nil {
		return 0, err
	} else if prev != nil {
		epoch = prev.Epoch + 1
		prevHash = prev.ManifestHash
	}

	// Concatenate entries into the decompressed stream the manifest
	// offsets address. Diffs run published rev -> latest accepted rev;
	// first-appearance users (and diffs that would be larger than the
	// document) ship the full doc.
	var stream bytes.Buffer
	entries := make([]manifestEntry, 0, len(changes))
	newHeads := make([]domain.PublishedHead, 0, len(changes))

	for _, ch := range changes {
		target, err := s.docs.Get(ch.doc.Hash)
		if err != nil {
			return 0, fmt.Errorf("loading document %s: %w", ch.doc.Hash, err)
		}

		entry := target
		if ch.hasBase {
			baseDoc, err := s.dal.Document(ctx, ch.doc.Userid, ch.baseRev)
			if err != nil {
				return 0, fmt.Errorf("loading published head for %s: %w", ch.doc.Userid, err)
			}
			base, err := s.docs.Get(baseDoc.Hash)
			if err != nil {
				return 0, fmt.Errorf("loading base document %s: %w", baseDoc.Hash, err)
			}
			patch, err := jsonlines.Diff(base, target)
			if err != nil {
				return 0, fmt.Errorf("diffing %s: %w", ch.doc.Userid, err)
			}
			if len(patch) < len(target) {
				entry = patch
			}
		}

		entries = append(entries, manifestEntry{
			ID:     ch.doc.Userid,
			Rev:    ch.doc.Rev,
			Hash:   ch.doc.Hash,
			Offset: stream.Len(),
			Size:   len(entry),
		})
		stream.Write(entry)
		newHeads = append(newHeads, domain.PublishedHead{Userid: ch.doc.Userid, Rev: ch.doc.Rev})
	}

	created := util.Now()

	manifest, manifestHash, err := s.signManifest(manifestMeta{
		Epoch:   epoch,
		Kind:    kindPatch,
		Format:  manifestFormat,
		Created: created.UTC().Format(time.RFC3339),
		Prev:    prevHash,
	}, entries)
	if err != nil {
		return 0, err
	}

	// Patches are consumed whole, so one solid gzip stream captures
	// cross-document redundancy.
	var compressed bytes.Buffer
	gw := gzip.NewWriter(&compressed)
	if _, err := gw.Write(stream.Bytes()); err != nil {
		return 0, err
	}
	if err := gw.Close(); err != nil {
		return 0, err
	}

	dir := filepath.Join(s.pubDir, "v1", "epochs", strconv.Itoa(epoch))
	if err := publishFiles(dir, map[string][]byte{
		"patch":    compressed.Bytes(),
		"manifest": manifest,
	}); err != nil {
		return 0, err
	}

	err = s.dal.RecordCut(ctx, &domain.Epoch{
		Epoch:        epoch,
		Kind:         kindPatch,
		ManifestHash: manifestHash,
		Created:      created,
	}, newHeads)
	if err != nil {
		return 0, err
	}

	if epoch == 1 || (s.snapshotEvery > 0 && epoch%s.snapshotEvery == 0) {
		if err := s.writeSnapshot(ctx, epoch, manifestHash); err != nil {
			return epoch, fmt.Errorf("writing snapshot at epoch %d: %w", epoch, err)
		}
	}

	if err := s.writeIndex(ctx); err != nil {
		return epoch, err
	}

	slog.Info("cut epoch", "epoch", epoch, "documents", len(entries))

	return epoch, nil
}

// writeSnapshot materializes the full corpus at an existing epoch. Each
// entry is individually gzipped; members are concatenated and packed
// into chunk files (snapshot.<i>), splitting only at member boundaries,
// so each chunk gunzips standalone while range-capable consumers pluck
// members using the manifest's global offsets. Entries are ordered
// newest-first so a mostly-synced peer's range requests cluster in the
// lowest-numbered chunks.
func (s *Server) writeSnapshot(ctx context.Context, epoch int, chainHash string) error {
	docs, err := s.dal.PublishedDocuments(ctx)
	if err != nil {
		return err
	}

	var chunks [][]byte
	var cur []byte
	offset := 0
	entries := make([]manifestEntry, 0, len(docs))

	for _, doc := range docs {
		data, err := s.docs.Get(doc.Hash)
		if err != nil {
			return fmt.Errorf("loading document %s: %w", doc.Hash, err)
		}

		var member bytes.Buffer
		gw := gzip.NewWriter(&member)
		if _, err := gw.Write(data); err != nil {
			return err
		}
		if err := gw.Close(); err != nil {
			return err
		}

		// The target size is advisory: a member larger than the target
		// gets a chunk to itself rather than spanning a boundary.
		if len(cur) > 0 && len(cur)+member.Len() > s.snapshotChunkSize {
			chunks = append(chunks, cur)
			cur = nil
		}

		entries = append(entries, manifestEntry{
			ID:     doc.Userid,
			Rev:    doc.Rev,
			Hash:   doc.Hash,
			Offset: offset,
			Size:   member.Len(),
		})
		offset += member.Len()
		cur = append(cur, member.Bytes()...)
	}
	if len(cur) > 0 {
		chunks = append(chunks, cur)
	}

	table := make([]chunkInfo, len(chunks))
	for i, c := range chunks {
		sum := sha256.Sum256(c)
		table[i] = chunkInfo{Size: len(c), Hash: hex.EncodeToString(sum[:])}
	}

	created := util.Now()

	manifest, manifestHash, err := s.signManifest(manifestMeta{
		Epoch:   epoch,
		Kind:    kindSnapshot,
		Format:  manifestFormat,
		Created: created.UTC().Format(time.RFC3339),
		Prev:    chainHash,
		Chunks:  table,
	}, entries)
	if err != nil {
		return err
	}

	files := map[string][]byte{"manifest": manifest}
	for i, c := range chunks {
		files["snapshot."+strconv.Itoa(i)] = c
	}

	dir := filepath.Join(s.pubDir, "v1", "snapshots", strconv.Itoa(epoch))
	if err := publishFiles(dir, files); err != nil {
		return err
	}

	err = s.dal.AddEpoch(ctx, &domain.Epoch{
		Epoch:        epoch,
		Kind:         kindSnapshot,
		ManifestHash: manifestHash,
		Created:      created,
	})
	if err != nil {
		return err
	}

	slog.Info("wrote snapshot", "epoch", epoch, "documents", len(entries), "chunks", len(chunks))

	return nil
}

// signManifest builds an sjsonl manifest document signed with the server
// key and returns its encoding and doc hash. Note that sjsonl hash-sorts
// lines, so manifest lines are unordered; consumers identify the
// metadata line by its fields and key entries by user id.
func (s *Server) signManifest(meta manifestMeta, entries []manifestEntry) ([]byte, string, error) {
	var buf bytes.Buffer

	metaLine, err := json.Marshal(meta)
	if err != nil {
		return nil, "", err
	}
	buf.Write(metaLine)
	buf.WriteByte('\n')

	for _, e := range entries {
		line, err := json.Marshal(e)
		if err != nil {
			return nil, "", err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}

	signed, info, err := jsonlines.Sign(buf.Bytes(), s.key)
	if err != nil {
		return nil, "", err
	}

	return signed, info.Hash, nil
}

// writeIndex regenerates /v1/index.json from the DB.
func (s *Server) writeIndex(ctx context.Context) error {
	epochs, err := s.dal.Epochs(ctx)
	if err != nil {
		return err
	}

	idx := serverIndex{
		SnapshotEpochs: []int{},
		ServerKey:      hex.EncodeToString(s.key.Public().(ed25519.PublicKey)),
	}

	for _, e := range epochs {
		switch e.Kind {
		case kindPatch:
			if idx.OldestRetainedEpoch == 0 || e.Epoch < idx.OldestRetainedEpoch {
				idx.OldestRetainedEpoch = e.Epoch
			}
			if e.Epoch > idx.CurrentEpoch {
				idx.CurrentEpoch = e.Epoch
			}
		case kindSnapshot:
			idx.SnapshotEpochs = append(idx.SnapshotEpochs, e.Epoch)
		}
	}

	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}

	return writeFileAtomic(filepath.Join(s.pubDir, "v1", "index.json"), append(data, '\n'), 0644)
}

// publishFiles writes a set of files into dir via temp-and-rename, so no
// partially written publication is ever observable.
func publishFiles(dir string, files map[string][]byte) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	for name, data := range files {
		if err := writeFileAtomic(filepath.Join(dir, name), data, 0644); err != nil {
			return err
		}
	}

	return nil
}
