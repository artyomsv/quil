package daemon

import (
	"path/filepath"
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/notify"
	"github.com/artyomsv/quil/internal/opencodehook"
	"github.com/artyomsv/quil/internal/panehistory"
)

// ipc.IsQuilHomeEntry is a hand-kept list of what quil writes at the top of
// QUIL_HOME, and ProtectDir refuses a folder holding anything else. A new
// file added to config without a line in that list would refuse a fresh
// home on Windows and stop its TCP listener, so every path helper is
// checked against it here.
func TestQuilHomeEntriesKnownToProtectDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QUIL_HOME", home)
	paths := []string{
		config.ConfigPath(), config.SocketPath(), config.PidPath(),
		config.WorkspacePath(), config.BufferDir(), config.PluginsDir(),
		config.PasteDir(), config.WindowStatePath(), config.InstancesPath(),
		config.ProjectGroupsPath(), config.NotesDir(), config.NotesConflictsDir(),
		config.SharedImportPath(), config.EventsDir(), config.SessionsDir(),
		config.UpdateDir(), config.TemplatesPath(), config.BindingsPath(),
		config.RecentCWDsPath(""), config.RecentCWDsPath("user@host"),
		config.SandboxImagePath(""), config.SandboxImagePath("user@host"),
		config.RemoteProjectsPath(""), config.RemoteProjectsPath("user@host"),
		config.MCPLogDir(config.MCPConfig{}),
		filepath.Join(home, auditFile), filepath.Join(home, "tokens.json"),
		filepath.Join(home, "quild.log"), filepath.Join(home, "quil.log"),
		filepath.Join(home, "quild.stderr.log"), filepath.Join(home, "quild.lock"),
		filepath.Join(home, "web.log"), filepath.Join(home, notify.ActivateLogName),
		sandboxRoot(home), panehistory.Dir(home),
		filepath.Dir(opencodehook.ScriptPath(home)),
		filepath.Join(home, "shellinit"), filepath.Join(home, "claudehook"),
		filepath.Join(home, "codexhook"),
	}
	for _, p := range paths {
		if filepath.Dir(p) != home {
			t.Errorf("%s is not at the top of QUIL_HOME; check it by its top-level folder instead", p)
			continue
		}
		if name := filepath.Base(p); !ipc.IsQuilHomeEntry(name) {
			t.Errorf("ipc.IsQuilHomeEntry(%q) = false: add it to quilHomeNames or quilHomeStems in internal/ipc/acl.go", name)
		}
	}
}
