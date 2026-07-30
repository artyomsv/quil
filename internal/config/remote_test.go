package config

import (
	"path/filepath"
	"testing"
)

func TestConfig_SetRemoteBinary_InitialisesMap(t *testing.T) {
	var cfg Config // zero value: Hosts is nil
	cfg.SetRemoteBinary("gpu01", "/home/a/.local/bin/quil")
	if got := cfg.RemoteBinary("gpu01"); got != "/home/a/.local/bin/quil" {
		t.Errorf("RemoteBinary = %q", got)
	}
}

func TestConfig_RemoteBinary_UnknownDestination(t *testing.T) {
	var cfg Config
	if got := cfg.RemoteBinary("never-seen"); got != "" {
		t.Errorf("RemoteBinary = %q, want empty for an unrecorded destination", got)
	}
}

func TestConfig_SetRemoteBinary_Overwrites(t *testing.T) {
	var cfg Config
	cfg.SetRemoteBinary("gpu01", "/usr/local/bin/quil")
	cfg.SetRemoteBinary("gpu01", "/home/a/.local/bin/quil")
	if n := len(cfg.Remote.Hosts); n != 1 {
		t.Errorf("Hosts has %d entries, want 1", n)
	}
	if got := cfg.RemoteBinary("gpu01"); got != "/home/a/.local/bin/quil" {
		t.Errorf("RemoteBinary = %q, want the newer path", got)
	}
}

// A destination is an arbitrary user string — an ssh_config Host alias can
// carry dots, dashes and an @ — so it has to survive a TOML key round trip.
func TestConfig_RemoteHosts_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	cfg := Default()
	cfg.SetRemoteBinary("gpu-01.lan", "/opt/bin/quil")
	cfg.SetRemoteBinary("dev@203.0.113.5", "/usr/local/bin/quil")

	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.RemoteBinary("gpu-01.lan") != "/opt/bin/quil" {
		t.Errorf("round trip lost the alias entry: %+v", got.Remote)
	}
	if got.RemoteBinary("dev@203.0.113.5") != "/usr/local/bin/quil" {
		t.Errorf("round trip lost the user@host entry: %+v", got.Remote)
	}
}

// A config written before this section existed must still load.
func TestConfig_LoadWithoutRemoteSection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := Save(path, Default()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.RemoteBinary("anything") != "" {
		t.Error("expected no recorded remote binaries")
	}
}
