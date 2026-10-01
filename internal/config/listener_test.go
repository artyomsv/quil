package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListener_OffByDefaultAndRoundTrips(t *testing.T) {
	if Default().Listener.TCP != "" {
		t.Fatal("the TCP listener is on by default")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := Save(path, Default()); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || cfg.Listener.TCP != "" {
		t.Fatalf("Save/Load of the default turned the listener on: %q %v", cfg.Listener.TCP, err)
	}
	if err := os.WriteFile(path, []byte("[listener]\ntcp = \"127.0.0.1:7878\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if cfg, err = Load(path); err != nil || cfg.Listener.TCP != "127.0.0.1:7878" {
		t.Fatalf("Load = %q %v", cfg.Listener.TCP, err)
	}
}
