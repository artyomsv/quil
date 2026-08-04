package tui

import (
	"path/filepath"
	"testing"

	"github.com/artyomsv/quil/internal/config"
)

// TestPersistDestination_KeepsTheRecordedRemoteBinary is the regression for a
// silent config revert that made a successful install look like a failed one.
//
// The runtime-connect flow is: dial fails "no quil there" → install → the
// install records [remote.hosts.<dest>].binary, the absolute path that makes
// attaching work on a host whose non-interactive PATH cannot see ~/.local/bin
// → retry dial succeeds → adoptDest records the destination.
//
// That last step used to save the Model's LAUNCH-TIME config struct whole.
// config.Save serialises everything, so it reverted the binary path written
// seconds earlier — and the next launch dialled bare `quil`, got 127, and
// offered the install again. Forever, since each install ends by erasing its
// own result.
func TestPersistDestination_KeepsTheRecordedRemoteBinary(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("QUIL_HOME", dir)
	path := config.ConfigPath()

	// The launch-time snapshot the Model holds: no remote hosts, because the
	// install has not happened yet when the TUI starts.
	launch := config.Default()
	if err := config.Save(path, launch); err != nil {
		t.Fatal(err)
	}

	// The install, writing behind the Model's back exactly as runRemoteSetup
	// does — through a read-modify-write of the file, not the Model's copy.
	if err := config.Mutate(path, func(c *config.Config) {
		c.SetRemoteBinary("gpu01", "/home/artyom/.local/bin/quil")
	}); err != nil {
		t.Fatal(err)
	}

	m := &Model{cfg: launch}
	m.persistDestination("gpu01")

	got, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if bin := got.RemoteBinary("gpu01"); bin != "/home/artyom/.local/bin/quil" {
		t.Errorf("recorded remote binary = %q after persistDestination, want it "+
			"untouched.\nSaving the launch-time snapshot whole reverts every key "+
			"written since — the next attach dials bare quil, gets 127, and "+
			"offers the install it just finished.", bin)
	}
	if len(got.Destinations) != 1 || got.Destinations[0].Dest != "gpu01" {
		t.Errorf("Destinations = %v, want the host recorded", got.Destinations)
	}
}

// TestForgetDestination_KeepsTheRecordedRemoteBinary: disconnecting a host
// removes it from the attach list; it must not also forget where quil lives on
// it, or reconnecting later re-runs the whole install dance.
func TestForgetDestination_KeepsTheRecordedRemoteBinary(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("QUIL_HOME", dir)
	path := config.ConfigPath()

	launch := config.Default()
	launch.Destinations = []config.Destination{{Dest: "gpu01"}, {Dest: "build02"}}
	if err := config.Save(path, launch); err != nil {
		t.Fatal(err)
	}
	if err := config.Mutate(path, func(c *config.Config) {
		c.SetRemoteBinary("gpu01", "/opt/quil/bin/quil")
	}); err != nil {
		t.Fatal(err)
	}

	m := &Model{cfg: launch}
	m.forgetDestination("build02")

	got, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if bin := got.RemoteBinary("gpu01"); bin != "/opt/quil/bin/quil" {
		t.Errorf("recorded remote binary for the KEPT host = %q, want it "+
			"untouched by disconnecting a different one", bin)
	}
	if len(got.Destinations) != 1 || got.Destinations[0].Dest != "gpu01" {
		t.Errorf("Destinations = %v, want only gpu01 left", got.Destinations)
	}
}

// TestMutate_ReadsFromDiskNotFromTheCaller pins the property the two tests
// above depend on, so a future "simplify" back to load-less Save is caught
// here rather than in the install flow six steps away.
func TestMutate_ReadsFromDiskNotFromTheCaller(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("QUIL_HOME", dir)
	path := filepath.Join(dir, "config.toml")

	if err := config.Mutate(path, func(c *config.Config) {
		c.SetRemoteBinary("host", "/first")
	}); err != nil {
		t.Fatal(err)
	}
	if err := config.Mutate(path, func(c *config.Config) {
		c.Destinations = append(c.Destinations, config.Destination{Dest: "host"})
	}); err != nil {
		t.Fatal(err)
	}

	got, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.RemoteBinary("host") != "/first" {
		t.Errorf("second Mutate lost the first's write: RemoteBinary = %q",
			got.RemoteBinary("host"))
	}
	if len(got.Destinations) != 1 {
		t.Errorf("Destinations = %v, want the second Mutate's write", got.Destinations)
	}
}
