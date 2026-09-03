package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/artyomsv/quil/internal/config"
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

	// A REAL plugins dir with a real TOML in it. Without this, PluginsDir()
	// does not exist, LoadFromDir takes its os.IsNotExist early-out, and the
	// reload this test reasons about never runs at all — the test then passes
	// against any implementation.
	if err := os.MkdirAll(config.PluginsDir(), 0o700); err != nil {
		t.Fatalf("mkdir plugins dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(config.PluginsDir(), "claude-code.toml"), []byte(content), 0o600); err != nil {
		t.Fatalf("write plugin toml: %v", err)
	}

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
	m.asRemote("gpu01")
	// The marker is "terminal", NOT "terminal-wide": LoadFromDir's
	// prune-on-reload exempts only "terminal" (registry.go), so a real reload
	// deletes "terminal-wide" outright. Same hazard documented at
	// dialog_test.go's sibling test.
	m.applyPluginList("gpu01", ipc.PluginListRespPayload{
		Plugins: []ipc.PluginInfo{{Name: "terminal", Available: false}},
	})

	out, cmd := m.saveMigrationAndAdvance()
	got := out.(Model)
	if got.pluginAvailableFor("gpu01", "terminal") {
		t.Error(`"gpu01" reads available — a local detection pass ran over the daemon's answer`)
	}
	if !got.pluginAvailableFor("", "terminal") {
		t.Error("local reads unavailable — the migration reloaded the registry and nothing re-detected")
	}

	// Re-detecting locally is only half the contract. The reload changed a
	// plugin DEFINITION, and the remote daemon is still holding whatever it
	// last reported about the old one; without a re-ask that stays stale for
	// the rest of the session.
	runCmd(cmd)
	var asked bool
	for _, sent := range fake.sent {
		if sent.Type == ipc.MsgPluginListReq {
			asked = true
		}
	}
	if !asked {
		t.Errorf("no %s sent after the migration reload — availability is never repopulated",
			ipc.MsgPluginListReq)
	}
}
