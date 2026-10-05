package tui

import (
	"os"
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/instances"
)

// The web gateway writes instances.json too. A TUI save must start from the
// file on disk, never from the copy read at start, or it erases what a
// browser added after the TUI opened.
func TestAddInstance_KeepsAnExternalWriteAfterStart(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	path := config.InstancesPath()
	m := newModelForTest([]string{"a"}, 0)
	m.instanceStore = LoadInstances(path) // what NewModel read: empty

	external := instances.Store{"ssh": {{ID: "web1", Name: "from-browser"}}}
	if err := instances.Save(path, external); err != nil {
		t.Fatal(err)
	}
	if !m.addInstance("ssh", SavedInstance{ID: "tui1", Name: "from-tui"}) {
		t.Fatal("addInstance refused a readable file")
	}
	got, err := instances.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got["ssh"]) != 2 || got["ssh"][0].ID != "web1" || got["ssh"][1].ID != "tui1" {
		t.Fatalf("file = %+v, want web1 then tui1", got["ssh"])
	}
	if len(m.instanceStore["ssh"]) != 2 {
		t.Fatalf("in-memory list not refreshed: %+v", m.instanceStore["ssh"])
	}

	// A delete must not revive an entry the browser deleted meanwhile.
	if err := instances.Save(path, instances.Store{"ssh": {{ID: "tui1", Name: "from-tui"}}}); err != nil {
		t.Fatal(err)
	}
	if !m.deleteInstance("ssh", "tui1") {
		t.Fatal("deleteInstance refused a readable file")
	}
	got, _ = instances.Load(path)
	if len(got["ssh"]) != 0 {
		t.Fatalf("file after delete = %+v, want empty (web1 was deleted elsewhere)", got["ssh"])
	}
}

// A corrupt file must not be replaced by the in-memory list: refuse with a
// flash and leave the bytes alone.
func TestAddInstance_CorruptFileIsRefusedAndUnchanged(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	path := config.InstancesPath()
	corrupt := []byte("{not json")
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	m := newModelForTest([]string{"a"}, 0)
	if m.addInstance("ssh", SavedInstance{ID: "x", Name: "x"}) {
		t.Fatal("addInstance saved over a corrupt file")
	}
	if m.deleteInstance("ssh", "x") {
		t.Fatal("deleteInstance saved over a corrupt file")
	}
	if m.flashText != instancesUnreadableFlash {
		t.Fatalf("flash = %q, want %q", m.flashText, instancesUnreadableFlash)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != string(corrupt) {
		t.Fatalf("file changed: %q %v", data, err)
	}
}

// Opening Ctrl+N re-reads the list, so a browser-added instance is offered.
func TestOpenCreatePaneDialog_ReloadsInstances(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	m := newModelForTest([]string{"a"}, 0)
	if err := instances.Save(config.InstancesPath(), instances.Store{"ssh": {{ID: "web1", Name: "b"}}}); err != nil {
		t.Fatal(err)
	}
	out, _ := m.openCreatePaneDialog()
	if got := out.(Model).instanceStore["ssh"]; len(got) != 1 || got[0].ID != "web1" {
		t.Fatalf("instanceStore[ssh] = %+v after opening Ctrl+N", got)
	}
}
