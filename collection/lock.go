package collection

import (
	"context"
	"os"
	"path/filepath"
	"time"
)

// Several processes (say, `revs ui` and `revs tui`) may share a data
// directory. Plain edits are single SQLite statements or transactions
// and need nothing more, but publishing reads and writes state that the
// client package caches in memory: each server's sync state, and the
// author's version history, which assigns revisions. syncLocked runs
// such operations one at a time across processes, and reloads those
// caches first so each starts from what the other process last wrote.

// lockPoll is how often syncLocked retries a held lock.
const lockPoll = 100 * time.Millisecond

// syncLocked runs fn holding the data directory's sync lock, after
// discarding cached sync state. It waits for another process's
// operation to finish, or for ctx to end.
func (c *Collection) syncLocked(ctx context.Context, fn func() error) error {
	// Within this process, one operation at a time too: the lock is
	// per open file, and the caches are shared.
	select {
	case c.syncMu <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-c.syncMu }()

	f, err := os.OpenFile(filepath.Join(c.dataDir, "sync.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	for {
		ok, err := tryLock(f)
		if err != nil {
			return err
		}
		if ok {
			break
		}
		select {
		case <-time.After(lockPoll):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	defer unlock(f)

	c.versions.Reload()
	c.remotesMu.Lock()
	for _, r := range c.remotes {
		r.client.Reload()
	}
	c.remotesMu.Unlock()

	return fn()
}
