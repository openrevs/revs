package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/openrevs/revs/client"
)

// PullResult summarizes one pull. Head is the server's current
// document for this user (nil if none). When the head advanced past
// the draft's base and the draft was clean (or absent), the draft was
// fast-forwarded (Seeded); if it was dirty instead, DirtyConflict asks
// the UI to run the take-server/keep-mine prompt.
type PullResult struct {
	Sync          *client.Result
	Head          *client.Document
	HeadAdvanced  bool
	DirtyConflict bool
	Seeded        bool
}

// PushOutcome reports one push. Exactly one of Result and Stale is
// set: Stale means the server holds a newer revision (another device
// pushed) and the UI must resolve.
type PushOutcome struct {
	Result *client.PushResult
	Stale  *client.StaleError
}

// Pull syncs the corpus and fast-forwards a clean draft onto a newer
// head. A dirty draft is never touched — the conflict is surfaced.
func (w *Workspace) Pull(ctx context.Context) (*PullResult, error) {
	if w.client == nil {
		return nil, ErrNoServer
	}
	res, err := w.client.Sync(ctx)
	if err != nil {
		return nil, err
	}
	return w.afterSync(res)
}

// Rebootstrap recovers from a *client.DivergedError by re-downloading
// the corpus from a snapshot, then applies the same head handling as
// Pull.
func (w *Workspace) Rebootstrap(ctx context.Context) (*PullResult, error) {
	if w.client == nil {
		return nil, ErrNoServer
	}
	res, err := w.client.Rebootstrap(ctx)
	if err != nil {
		return nil, err
	}
	return w.afterSync(res)
}

func (w *Workspace) afterSync(res *client.Result) (*PullResult, error) {
	pr := &PullResult{Sync: res}

	doc, err := w.client.Document(w.UserID())
	if err != nil && !errors.Is(err, client.ErrNotFound) {
		return nil, err
	}
	pr.Head = doc

	w.meta.LastSync = time.Now()
	if err := w.saveMeta(); err != nil {
		return nil, err
	}

	if doc != nil && doc.Rev > w.meta.BaseRev {
		pr.HeadAdvanced = true
		if w.draft != nil && w.Dirty() {
			pr.DirtyConflict = true
		} else {
			if err := w.SeedFromHead(doc); err != nil {
				return nil, err
			}
			pr.Seeded = true
		}
	}
	return pr, nil
}

// Push uploads the draft. The client owns the revision discipline; on
// success (including "already up to date") the draft's base advances
// to what the server now holds.
func (w *Workspace) Push(ctx context.Context) (*PushOutcome, error) {
	if w.client == nil {
		return nil, ErrNoServer
	}
	if w.key == nil {
		return nil, ErrNoKey
	}
	if w.draft == nil {
		return nil, ErrNoDraft
	}

	res, err := w.client.Push(ctx, w.key, w.draft.content())
	var stale *client.StaleError
	if errors.As(err, &stale) {
		return &PushOutcome{Stale: stale}, nil
	}
	if err != nil {
		return nil, err
	}

	w.meta.BaseRev = res.Rev
	w.meta.BaseHash = res.Hash
	w.meta.LastPush = time.Now()
	if err := w.saveMeta(); err != nil {
		return nil, err
	}
	if err := w.normalizeUserRevision(res.Rev); err != nil {
		return nil, err
	}
	w.recomputeDirty()
	return &PushOutcome{Result: res}, nil
}

// ResolveKeepMine resolves a stale push by pushing again: the client
// recorded the server's revision from the 409 and now asserts past it,
// overwriting the other device's document.
func (w *Workspace) ResolveKeepMine(ctx context.Context) (*PushOutcome, error) {
	return w.Push(ctx)
}

// ResolveTakeServer resolves a conflict by discarding local edits in
// favor of the server: sync, then reseed the draft from the pulled
// head (a backup of the old draft is kept). staleRev is the revision
// the conflict reported; if the server hasn't cut that revision into
// an epoch yet the head will still be older, and the draft is left
// alone (Seeded stays false) so the UI can say "not yet published".
func (w *Workspace) ResolveTakeServer(ctx context.Context, staleRev int) (*PullResult, error) {
	if w.client == nil {
		return nil, ErrNoServer
	}
	res, err := w.client.Sync(ctx)
	if err != nil {
		return nil, err
	}

	pr := &PullResult{Sync: res}
	doc, err := w.client.Document(w.UserID())
	if err != nil && !errors.Is(err, client.ErrNotFound) {
		return nil, err
	}
	pr.Head = doc

	w.meta.LastSync = time.Now()
	if err := w.saveMeta(); err != nil {
		return nil, err
	}

	if doc == nil || doc.Rev < staleRev {
		return pr, nil
	}

	pr.HeadAdvanced = doc.Rev > w.meta.BaseRev
	if err := w.SeedFromHead(doc); err != nil {
		return nil, err
	}
	pr.Seeded = true
	return pr, nil
}

// normalizeUserRevision rewrites the draft's user line exactly the way
// client.Push rewrote it before signing (unmarshal to a map, set the
// revision, remarshal — compact, keys sorted), so the saved draft is
// byte-identical to the document the server just accepted and the
// dirty check reads clean.
func (w *Workspace) normalizeUserRevision(rev int) error {
	var m map[string]any
	if err := json.Unmarshal(w.draft.UserRaw, &m); err != nil {
		return err
	}
	m["revision"] = rev
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &w.draft.User); err != nil {
		return err
	}
	w.draft.UserRaw = raw
	return w.saveDraftFile()
}
