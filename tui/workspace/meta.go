package workspace

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"time"
)

// Meta records which published head the draft derives from. BaseRev
// and BaseHash are the rev/doc-hash of the last head this draft was
// seeded from or successfully pushed as; BaseRev 0 means the draft has
// never been synced.
type Meta struct {
	BaseRev  int       `json:"base_rev"`
	BaseHash string    `json:"base_hash,omitempty"`
	LastSync time.Time `json:"last_sync"`
	LastPush time.Time `json:"last_push"`
}

func (w *Workspace) loadMeta() error {
	data, err := os.ReadFile(w.path(metaFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &w.meta); err != nil {
		return err
	}
	w.metaFound = true
	return nil
}

func (w *Workspace) saveMeta() error {
	data, err := json.MarshalIndent(w.meta, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(w.path(metaFile), append(data, '\n'), 0o644); err != nil {
		return err
	}
	w.metaFound = true
	return nil
}
