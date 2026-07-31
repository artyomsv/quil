package tui

import (
	"path/filepath"
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/plugin"
)

// TestSaveMigrationAndAdvance_ReloadsDaemon proves the schema-migration dialog
// notifies the daemon after rewriting a plugin file. The daemon loads plugins
// once at startup and keeps a stale in-memory copy; without an explicit
// MsgReloadPlugins it spawns panes with the OLD config (e.g. record_history
// still false) until it restarts — which is exactly how input-history capture
// silently broke for freshly created panes after the schema 5→6 bump.
func TestSaveMigrationAndAdvance_ReloadsDaemon(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir()) // keep config.PluginsDir() off the real home

	dir := t.TempDir()
	fp := filepath.Join(dir, "claude-code.toml")
	content := "[plugin]\n" +
		"name = \"claude-code\"\n" +
		"schema_version = 6\n" +
		"[command]\n" +
		"cmd = \"claude\"\n" +
		"record_history = true\n"

	fake := &fakeSender{}
	m := Model{
		client:         fake,
		pluginRegistry: plugin.NewRegistry(),
		migrationIdx:   0,
		migrationPlugins: []plugin.StalePlugin{{
			Name:        "claude-code",
			FilePath:    fp,
			DefaultData: []byte("[plugin]\nschema_version = 6\n"),
		}},
		migrationLeft: NewTextEditor(content, fp, 80, 24),
	}

	_, cmd := m.saveMigrationAndAdvance()
	runCmd(cmd) // unwraps tea.Batch and executes nested sends

	found := false
	for _, msg := range fake.sent {
		if msg.Type == ipc.MsgReloadPlugins {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("migration save must send MsgReloadPlugins so the daemon picks up the new config; sent %d msgs", len(fake.sent))
	}
}

// TestSaveMigrationAndAdvance_RemoteModeKeepsAdoptedAvailability pins the
// third DetectAvailability guard: the startup migration dialog resolves
// after the first attach, so a remote session has already adopted the
// daemon's availability answer by the time this runs. Calling
// DetectAvailability() unconditionally here discarded that answer with a
// detection pass over the wrong machine.
func TestSaveMigrationAndAdvance_RemoteModeKeepsAdoptedAvailability(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())

	dir := t.TempDir()
	fp := filepath.Join(dir, "claude-code.toml")
	content := "[plugin]\n" +
		"name = \"claude-code\"\n" +
		"schema_version = 6\n" +
		"[command]\n" +
		"cmd = \"claude\"\n" +
		"record_history = true\n"

	reg := plugin.NewRegistry()
	// Simulate an availability answer already adopted from the daemon over
	// IPC before this dialog resolved.
	reg.SetAvailability(map[string]bool{"terminal-wide": false})

	m := Model{
		client:         &fakeSender{},
		pluginRegistry: reg,
		migrationIdx:   0,
		migrationPlugins: []plugin.StalePlugin{{
			Name:        "claude-code",
			FilePath:    fp,
			DefaultData: []byte("[plugin]\nschema_version = 6\n"),
		}},
		migrationLeft: NewTextEditor(content, fp, 80, 24),
	}
	m.SetRemoteDest("gpu01")

	out, _ := m.saveMigrationAndAdvance()
	got := out.(Model)
	if got.pluginRegistry.Get("terminal-wide").Available {
		t.Error("Available = true — a local DetectAvailability pass ran in remote mode and clobbered the daemon's answer")
	}
}
