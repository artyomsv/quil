package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/artyomsv/quil/internal/plugin"
)

// envContains reports whether env holds the exact "KEY=value" entry.
func envContains(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}

// loadRegistryWithPlugin writes a single plugin TOML into a temp dir and loads
// it into a fresh registry, returning the registry. record controls the
// plugin's record_history opt-in.
func loadRegistryWithPlugin(t *testing.T, name string, record bool) *plugin.Registry {
	t.Helper()
	dir := t.TempDir()
	recordLine := ""
	if record {
		recordLine = "record_history = true\n"
	}
	toml := "[plugin]\n" +
		"name = \"" + name + "\"\n" +
		"display_name = \"" + name + "\"\n" +
		"category = \"test\"\n" +
		"schema_version = 1\n" +
		"[command]\n" +
		"cmd = \"echo\"\n" +
		recordLine
	if err := os.WriteFile(filepath.Join(dir, name+".toml"), []byte(toml), 0o600); err != nil {
		t.Fatalf("write plugin toml: %v", err)
	}
	reg := plugin.NewRegistry()
	if err := reg.LoadFromDir(dir); err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}
	if reg.Get(name) == nil {
		t.Fatalf("plugin %q not loaded", name)
	}
	return reg
}

// TestSpawnPane_SetsRecordHistoryEnv proves the gate env QUIL_RECORD_HISTORY=1
// reaches the PTY exactly when the plugin opts in via record_history. This is
// the seam that lets the Claude hook decide whether to append input history;
// a regression here silently disables capture for every new pane.
func TestSpawnPane_SetsRecordHistoryEnv(t *testing.T) {
	tests := []struct {
		name   string
		record bool
		want   bool
	}{
		{name: "record_history true sets env", record: true, want: true},
		{name: "record_history false omits env", record: false, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &Daemon{
				registry: loadRegistryWithPlugin(t, "histplugin", tt.record),
				session:  NewSessionManager(4096),
			}
			fake := &fakeSession{}
			pane := &Pane{ID: "test-pane", Type: "histplugin"}

			if err := d.spawnPane(pane, fake, false); err != nil {
				t.Fatalf("spawnPane: %v", err)
			}

			got := envContains(fake.env, "QUIL_RECORD_HISTORY=1")
			if got != tt.want {
				t.Fatalf("QUIL_RECORD_HISTORY present=%v, want %v (env=%v)", got, tt.want, fake.env)
			}
		})
	}
}
