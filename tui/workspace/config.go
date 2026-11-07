package workspace

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
)

type Config struct {
	ServerURL string `json:"server_url,omitempty"`
}

func (w *Workspace) loadConfig() error {
	data, err := os.ReadFile(w.path(configFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(data, &w.cfg)
}

func (w *Workspace) saveConfig() error {
	data, err := json.MarshalIndent(w.cfg, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(w.path(configFile), append(data, '\n'), 0o644)
}
