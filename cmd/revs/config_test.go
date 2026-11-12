package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openrevs/revs"
)

// parseWith parses args against a parser whose config search path is a
// file containing configYAML.
func parseWith(t *testing.T, configYAML string, args ...string) *cli {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	var c cli
	parser, err := newParser(&c, cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parser.Parse(args); err != nil {
		t.Fatal(err)
	}
	return &c
}

func TestConfigDefaults(t *testing.T) {
	c := parseWith(t, "", "server")
	if c.Server.Listen != revs.DefaultServerListen {
		t.Errorf("listen = %q, want %q", c.Server.Listen, revs.DefaultServerListen)
	}
	if c.Server.CutInterval != revs.DefaultCutInterval {
		t.Errorf("cut interval = %v, want %v", c.Server.CutInterval, revs.DefaultCutInterval)
	}
	if c.Server.SnapshotEvery != revs.DefaultSnapshotEvery {
		t.Errorf("snapshot every = %d, want %d", c.Server.SnapshotEvery, revs.DefaultSnapshotEvery)
	}
	if c.Server.SnapshotChunkSize != revs.DefaultSnapshotChunkSize {
		t.Errorf("snapshot chunk size = %d, want %d", c.Server.SnapshotChunkSize, revs.DefaultSnapshotChunkSize)
	}
}

func TestConfigFileOverridesDefaults(t *testing.T) {
	dataDir := t.TempDir()
	cfg := fmt.Sprintf(`
server:
  data: %s
  listen: ":9999"
  cut-interval: 15m
  snapshot-every: 5
`, dataDir)
	c := parseWith(t, cfg, "server")
	if c.Server.DataDir != dataDir {
		t.Errorf("data dir = %q, want %q", c.Server.DataDir, dataDir)
	}
	if c.Server.Listen != ":9999" {
		t.Errorf("listen = %q, want :9999", c.Server.Listen)
	}
	if c.Server.CutInterval != 15*time.Minute {
		t.Errorf("cut interval = %v, want 15m", c.Server.CutInterval)
	}
	if c.Server.SnapshotEvery != 5 {
		t.Errorf("snapshot every = %d, want 5", c.Server.SnapshotEvery)
	}
	// Unset keys keep their defaults.
	if c.Server.SnapshotChunkSize != revs.DefaultSnapshotChunkSize {
		t.Errorf("snapshot chunk size = %d, want default", c.Server.SnapshotChunkSize)
	}
}

func TestCLIOverridesConfigFile(t *testing.T) {
	cfg := "server:\n  listen: \":9999\"\n  cut-interval: 15m\n"
	c := parseWith(t, cfg, "server", "--listen=:7777")
	if c.Server.Listen != ":7777" {
		t.Errorf("listen = %q, want CLI value :7777", c.Server.Listen)
	}
	if c.Server.CutInterval != 15*time.Minute {
		t.Errorf("cut interval = %v, want config value 15m", c.Server.CutInterval)
	}
}

func TestConfigSectionsAreIndependent(t *testing.T) {
	uiDir := t.TempDir()
	cfg := fmt.Sprintf(`
server:
  listen: ":9999"
ui:
  data: %s
  listen: 127.0.0.1:9998
`, uiDir)
	c := parseWith(t, cfg, "ui")
	if c.UI.DataDir != uiDir {
		t.Errorf("ui data dir = %q, want %q", c.UI.DataDir, uiDir)
	}
	if c.UI.Listen != "127.0.0.1:9998" {
		t.Errorf("ui listen = %q, want config value", c.UI.Listen)
	}
	// The server section must not leak into the client command.
	c = parseWith(t, cfg, "client")
	if c.Client.Listen != revs.DefaultClientListen {
		t.Errorf("client listen = %q, want default", c.Client.Listen)
	}
}

func TestConfigUnderscoreKeys(t *testing.T) {
	c := parseWith(t, "server:\n  cut_interval: 20m\n", "server")
	if c.Server.CutInterval != 20*time.Minute {
		t.Errorf("cut interval = %v, want 20m", c.Server.CutInterval)
	}
}

func TestConfigFlag(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "other.yaml")
	if err := os.WriteFile(cfgPath, []byte("server:\n  listen: \":8888\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var c cli
	// No default config paths: only --config supplies values.
	parser, err := newParser(&c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parser.Parse([]string{"server", "--config", cfgPath}); err != nil {
		t.Fatal(err)
	}
	if c.Server.Listen != ":8888" {
		t.Errorf("listen = %q, want :8888 from --config file", c.Server.Listen)
	}
}

func TestRevsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("REVS_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte("server:\n  listen: \":7070\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var c cli
	parser, err := newParser(&c, defaultConfigPaths()...)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parser.Parse([]string{"server"}); err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "server"); c.Server.DataDir != want {
		t.Errorf("server data dir = %q, want %q", c.Server.DataDir, want)
	}
	if c.Server.Listen != ":7070" {
		t.Errorf("listen = %q, want :7070 from $REVS_HOME/config.yaml", c.Server.Listen)
	}

	c = cli{}
	parser, err = newParser(&c, defaultConfigPaths()...)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parser.Parse([]string{"tui"}); err != nil {
		t.Fatal(err)
	}
	if c.TUI.DataDir != home {
		t.Errorf("tui data dir = %q, want %q", c.TUI.DataDir, home)
	}
}
