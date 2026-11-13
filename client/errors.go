package client

import (
	"errors"
	"fmt"
)

// ErrNotFound is returned when no document is held for a user.
var ErrNotFound = errors.New("client: no document for user")

// ErrBlobNotFound is wrapped by Store.GetBlob errors for blobs the
// store doesn't hold.
var ErrBlobNotFound = errors.New("client: blob not found")

// DivergedError means no published route leads forward from the local
// corpus: the channel has gone back in time, or a patch names a base
// document the store doesn't hold and no snapshot resolves it. That is
// evidence of a fork, tampering, or local corruption, so it is surfaced
// rather than repaired. Recover explicitly with Client.Rebootstrap.
type DivergedError struct {
	Epoch    int
	UserID   string // empty for channel-level divergence
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
