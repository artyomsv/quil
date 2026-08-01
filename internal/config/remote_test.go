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

func TestConfig_ClearRemoteBinary_RemovesOnlyThatHost(t *testing.T) {
	var cfg Config
	cfg.SetRemoteBinary("gpu01", "/home/a/.local/bin/quil")
	cfg.SetRemoteBinary("other", "/usr/local/bin/quil")

	cfg.ClearRemoteBinary("gpu01")

	if got := cfg.RemoteBinary("gpu01"); got != "" {
		t.Errorf("RemoteBinary(gpu01) = %q, want empty after clear", got)
	}
	if got := cfg.RemoteBinary("other"); got != "/usr/local/bin/quil" {
		t.Errorf("clearing one host disturbed another: %q", got)
	}
}

// The zero value has a nil Hosts map, which is what a config predating the
// [remote] section loads as. Clearing must be a no-op there rather than a
// panic: this runs on the failure path, where a crash would replace a
// diagnosable error with no diagnosis at all.
func TestConfig_ClearRemoteBinary_NilMapAndAbsentKey(t *testing.T) {
	var cfg Config
	cfg.ClearRemoteBinary("never-seen")

	cfg.SetRemoteBinary("gpu01", "/p/quil")
	cfg.ClearRemoteBinary("never-seen")
	if got := cfg.RemoteBinary("gpu01"); got != "/p/quil" {
		t.Errorf("clearing an absent key disturbed a present one: %q", got)
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
