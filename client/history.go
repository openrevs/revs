package client

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"slices"
	"sync"

	jsonlines "github.com/openrevs/sjsonl-go"
)

// DefaultHistoryLimit is how many versions a History keeps per user
// unless WithHistoryLimit says otherwise.
const DefaultHistoryLimit = 100

// History is an author's local version history: the signed documents
// committed for a user, with the revision numbers they were assigned.
// It is the single authority for an author's revisions, so it should be
// shared by every Client the author pushes through. Servers can pull
// from each other, so a server may hold any of the author's versions,
// regardless of which server it was uploaded to; keeping them all lets
// Push build a patch against whichever one the server has.
//
// Only the newest version is stored in full. Each older one is stored
// as a signed sjsonl patch from its successor (or in full, when that is
// smaller), so a version is rebuilt by patching back from the newest.
// Because the patches are signed, a rebuilt version is verified against
// the author's signature and its hash.
//
// A History is safe for concurrent use. A HistoryStore must be owned by
// a single History.
type History struct {
	store HistoryStore
	limit int

	mu    sync.Mutex
	cache map[string]*VersionHistory
}

// HistoryStore persists a History, one user at a time.
type HistoryStore interface {
	// LoadHistory returns a user's history, or (nil, nil) if none has
	// been saved yet.
	LoadHistory(userID string) (*VersionHistory, error)
	// SaveHistory must replace the user's history atomically.
	SaveHistory(userID string, h *VersionHistory) error
}

// VersionHistory is one user's stored history.
type VersionHistory struct {
	// ForeignRev is the highest revision known for a document of the
	// user's that is not in this history: e.g. another device's push,
	// reported by a server. New versions are assigned a revision above
	// it, and content is only unchanged if the newest version is newer.
	ForeignRev int `json:"foreign_rev,omitempty"`
	// Versions are oldest first. The last is a full signed document; each
	// earlier one is a signed sjsonl patch from its successor, or a full
	// document when that is smaller.
	Versions []StoredVersion `json:"versions,omitempty"`
}

// StoredVersion is one version as stored.
type StoredVersion struct {
	Rev  int    `json:"rev"`
	Hash string `json:"hash"`
	Data []byte `json:"data"`
}

type HistoryOption func(*History)

// WithHistoryLimit sets how many versions are kept per user; the oldest
// are dropped beyond it. It must be at least 1.
func WithHistoryLimit(n int) HistoryOption {
	return func(h *History) { h.limit = max(n, 1) }
}

func NewHistory(store HistoryStore, opts ...HistoryOption) *History {
	h := &History{
		store: store,
		limit: DefaultHistoryLimit,
		cache: map[string]*VersionHistory{},
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// Commit makes content the user's newest version, signed with key, and
// returns it. If content is unchanged from the newest version, and no
// other document is known at that revision or above, that version is
// returned as is; otherwise the new version is assigned a revision above
// every known one.
//
// content is unsigned JSONL data lines, exactly one of which carries the
// user's "id" (which must match the id derived from key). Any revision
// already in the content is overwritten.
func (h *History) Commit(key ed25519.PrivateKey, content []byte) (*Document, error) {
	uid := userIDFromKey(key.Public().(ed25519.PublicKey))

	lines, idLine, contentID, err := parseContent(content)
	if err != nil {
		return nil, err
	}
	if contentID != uid {
		return nil, fmt.Errorf("client: content id %q does not match signing key (user %s)", contentID, uid)
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	vh, err := h.load(uid)
	if err != nil {
		return nil, err
	}

	base := vh.ForeignRev
	if latest := vh.latest(); latest != nil {
		base = max(base, latest.Rev)

		// Changed means changed with the revision held constant.
		if latest.Rev > vh.ForeignRev {
			_, candHash, err := signWithRev(lines, idLine, base, key)
			if err != nil {
				return nil, err
			}
			if candHash == latest.Hash {
				return &Document{UserID: uid, Rev: latest.Rev, Hash: latest.Hash, Data: latest.Data}, nil
			}
		}
	}

	rev := base + 1
	signed, hash, err := signWithRev(lines, idLine, rev, key)
	if err != nil {
		return nil, err
	}

	if err := h.append(uid, vh, StoredVersion{Rev: rev, Hash: hash, Data: signed}); err != nil {
		return nil, err
	}

	return &Document{UserID: uid, Rev: rev, Hash: hash, Data: signed}, nil
}

// Latest returns the user's newest version, or ErrNotFound.
func (h *History) Latest(userID string) (*Document, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	vh, err := h.load(userID)
	if err != nil {
		return nil, err
	}

	latest := vh.latest()
	if latest == nil {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, userID)
	}

	return &Document{UserID: userID, Rev: latest.Rev, Hash: latest.Hash, Data: latest.Data}, nil
}

// Version returns the user's version with the given doc hash, rebuilt
// from the stored patches, or ErrNotFound if it isn't retained.
func (h *History) Version(userID, hash string) (*Document, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	vh, err := h.load(userID)
	if err != nil {
		return nil, err
	}

	i := slices.IndexFunc(vh.Versions, func(v StoredVersion) bool { return v.Hash == hash })
	if i < 0 {
		return nil, fmt.Errorf("%w: %s version %s", ErrNotFound, userID, hash)
	}

	data, err := vh.rebuild(i)
	if err != nil {
		return nil, fmt.Errorf("client: rebuilding version %s of user %s: %w", hash, userID, err)
	}

	return &Document{UserID: userID, Rev: vh.Versions[i].Rev, Hash: hash, Data: data}, nil
}

// NoteRev records that a document of the user's exists elsewhere at
// rev (e.g. a server reported it), with the given doc hash if known. If
// it isn't a version in this history, the next version is assigned a
// revision above it, even if content is unchanged.
func (h *History) NoteRev(userID string, rev int, hash string) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	vh, err := h.load(userID)
	if err != nil {
		return err
	}
	if rev <= vh.ForeignRev || (hash != "" && slices.ContainsFunc(vh.Versions, func(v StoredVersion) bool { return v.Hash == hash })) {
		return nil
	}

	next := vh.clone()
	next.ForeignRev = rev
	return h.save(userID, next)
}

// adopt records a verified signed document of the user's obtained from
// elsewhere (e.g. pulled from a server) as the newest version, if it is
// newer than every retained one. That lets Commit recognize the
// author's content as unchanged when it matches.
func (h *History) adopt(doc *Document) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	vh, err := h.load(doc.UserID)
	if err != nil {
		return err
	}
	if latest := vh.latest(); latest != nil && doc.Rev <= latest.Rev {
		return nil
	}

	return h.append(doc.UserID, vh, StoredVersion{Rev: doc.Rev, Hash: doc.Hash, Data: doc.Data})
}

// append saves v as the user's newest version, compressing the
// previous newest into a patch from v. Callers hold h.mu.
func (h *History) append(uid string, vh *VersionHistory, v StoredVersion) error {
	next := vh.clone()

	if n := len(next.Versions); n > 0 {
		prev := next.Versions[n-1]
		patch, err := jsonlines.Diff(v.Data, prev.Data)
		if err != nil {
			return fmt.Errorf("client: compressing version %d of user %s: %w", prev.Rev, uid, err)
		}
		if len(patch) < len(prev.Data) {
			prev.Data = patch
			next.Versions[n-1] = prev
		}
	}

	next.Versions = append(next.Versions, v)
	if over := len(next.Versions) - h.limit; over > 0 {
		next.Versions = slices.Delete(next.Versions, 0, over)
	}

	return h.save(uid, next)
}

// Reload discards cached histories, so the next call reads them from
// the store. Call it before using a store that another process (with
// its own History) may have written, while holding a lock that keeps
// that process out.
func (h *History) Reload() {
	h.mu.Lock()
	defer h.mu.Unlock()
	clear(h.cache)
}

// load returns the user's cached history, loading it on first use.
// Callers hold h.mu and must not mutate the result.
func (h *History) load(uid string) (*VersionHistory, error) {
	if vh, ok := h.cache[uid]; ok {
		return vh, nil
	}

	vh, err := h.store.LoadHistory(uid)
	if err != nil {
		return nil, err
	}
	if vh == nil {
		vh = &VersionHistory{}
	}

	h.cache[uid] = vh
	return vh, nil
}

// save persists vh and then caches it. Callers hold h.mu.
func (h *History) save(uid string, vh *VersionHistory) error {
	if err := h.store.SaveHistory(uid, vh); err != nil {
		return err
	}

	h.cache[uid] = vh
	return nil
}

func (vh *VersionHistory) latest() *StoredVersion {
	if len(vh.Versions) == 0 {
		return nil
	}
	return &vh.Versions[len(vh.Versions)-1]
}

func (vh *VersionHistory) clone() *VersionHistory {
	return &VersionHistory{ForeignRev: vh.ForeignRev, Versions: slices.Clone(vh.Versions)}
}

// rebuild returns the full signed document of version i: starting from
// the nearest version at or after i that is stored in full, it applies
// each patch back to i.
func (vh *VersionHistory) rebuild(i int) ([]byte, error) {
	k := i
	for ; k < len(vh.Versions); k++ {
		kind, err := sjsonlKind(vh.Versions[k].Data)
		if err != nil {
			return nil, err
		}
		if kind == "doc" {
			break
		}
	}
	if k == len(vh.Versions) {
		return nil, errors.New("no full document stored")
	}

	doc := vh.Versions[k].Data
	for j := k - 1; j >= i; j-- {
		data := vh.Versions[j].Data
		kind, err := sjsonlKind(data)
		if err != nil {
			return nil, err
		}
		if kind == "doc" {
			doc = data
			continue
		}
		if doc, _, err = jsonlines.Patch(doc, data); err != nil {
			return nil, err
		}
	}

	return doc, nil
}

// sjsonlKind returns the sjsonl type ("doc" or "patch") of an encoding.
func sjsonlKind(data []byte) (string, error) {
	ctl, err := peekControl(data)
	if err != nil {
		return "", err
	}
	return ctl.SJSONL, nil
}
