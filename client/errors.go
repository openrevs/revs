package client

import (
	"errors"
	"fmt"
)

// ErrNotFound is returned when no document is held for a user.
var ErrNotFound = errors.New("client: no document for user")

// KeyChangedError means a manifest was signed by a key other than the
// pinned server key (or the configured key disagrees with the stored
// pin). This is never auto-accepted: resolving it means deciding the
// server identity really changed and starting from a fresh store, or
// pinning the new key explicitly.
type KeyChangedError struct {
	Pinned string
	Got    string
}

func (e *KeyChangedError) Error() string {
	return fmt.Sprintf("client: server key changed (pinned %s, got %s)", e.Pinned, e.Got)
}

// DivergedError means the local corpus no longer matches the state the
// server's patch chain assumes: a patch names a base document we don't
// hold, or the manifest chain doesn't extend ours. This is evidence of
// a fork, tampering, or local corruption — it is surfaced, never
// silently repaired. Recover explicitly with Client.Rebootstrap, which
// re-syncs from a snapshot while retaining existing blobs.
type DivergedError struct {
	Epoch    int
	UserID   string // empty for chain-level divergence
	Reason   string
	WantBase string // base hash the patch assumes, if applicable
	HaveBase string // hash we actually hold, if applicable
}

func (e *DivergedError) Error() string {
	msg := fmt.Sprintf("client: diverged at epoch %d: %s", e.Epoch, e.Reason)
	if e.UserID != "" {
		msg += fmt.Sprintf(" (user %s)", e.UserID)
	}
	if e.WantBase != "" || e.HaveBase != "" {
		msg += fmt.Sprintf(" (want base %s, have %s)", e.WantBase, e.HaveBase)
	}
	return msg
}

// StaleError means the server already holds a revision at least as new
// as the one we asserted — typically because the same user pushed from
// elsewhere. The conflicting revision is recorded, so a subsequent Push
// asserts past it; whether the content should first be merged is the
// application's decision, which is why this surfaces instead of
// auto-retrying.
type StaleError struct {
	UserID    string
	LatestRev int
	Reason    string
}

func (e *StaleError) Error() string {
	return fmt.Sprintf("client: push stale for user %s: server holds rev %d: %s", e.UserID, e.LatestRev, e.Reason)
}

// RejectedError means the server rejected the upload as invalid.
type RejectedError struct {
	Reason string
}

func (e *RejectedError) Error() string {
	return fmt.Sprintf("client: push rejected: %s", e.Reason)
}
