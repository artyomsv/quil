package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/artyomsv/quil/internal/config"
)

func TestSandboxImageFile_RoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "sandbox-image.json")
	if got := LoadSandboxImage(p); got != "" {
		t.Errorf("missing file loaded %q", got)
	}
	if err := SaveSandboxImage(p, "quil-sandbox:latest"); err != nil {
		t.Fatalf("save: %v", err)
	}
	if got := LoadSandboxImage(p); got != "quil-sandbox:latest" {
		t.Errorf("loaded %q, want quil-sandbox:latest", got)
	}
}

// A bad file costs a pre-fill, never the dialog.
func TestLoadSandboxImage_MalformedIsEmpty(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sandbox-image.json")
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := LoadSandboxImage(p); got != "" {
		t.Errorf("malformed file loaded %q", got)
	}
}

func memStore(m *Model) map[string]string {
	mem := map[string]string{}
	m.SetSandboxImageStore(func(d string) string { return mem[d] }, func(d, i string) { mem[d] = i })
	return mem
}

// Pre-fill: the host's last image, else default_image, else empty.
func TestSandboxImageDefault_LastUsedThenConfig(t *testing.T) {
	t.Parallel()
	var m Model
	m.cfg = config.Default()
	m.cfg.Sandbox.DefaultImage = "cfg:1"
	if got := m.sandboxImageDefault("hostA"); got != "cfg:1" {
		t.Errorf("no store: %q, want the config default", got)
	}
	mem := memStore(&m)
	mem["hostA"] = "last:2"
	if got := m.sandboxImageDefault("hostA"); got != "last:2" {
		t.Errorf("hostA: %q, want last:2", got)
	}
	if got := m.sandboxImageDefault("hostB"); got != "cfg:1" {
		t.Errorf("hostB saw hostA's image: %q", got)
	}
}

// Opening the dialog pre-fills from the destination's memory.
func TestResetSandboxField_PrefillsTheRememberedImage(t *testing.T) {
	t.Parallel()
	var m Model
	m.cfg = config.Default()
	mem := memStore(&m)
	mem["gpu"] = "quil-sandbox:latest"
	m.resetSandboxField("gpu")
	if m.sandboxImage != "quil-sandbox:latest" {
		t.Errorf("image = %q, want the remembered one", m.sandboxImage)
	}
	m.resetSandboxField("")
	if m.sandboxImage != "" {
		t.Errorf("local dest pre-filled %q from another host's memory", m.sandboxImage)
	}
}

// Submitting a sandbox pane files the image under the dialog's PINNED
// destination — through the real submit path, not the helper — because the
// image is pulled by THAT host's docker.
func TestCreatePaneSubmit_RemembersTheImageForThePinnedDest(t *testing.T) {
	for _, dest := range []string{"", "user@gpu"} {
		t.Run("dest="+dest, func(t *testing.T) {
			m := newTabModel(t)
			m.createPaneTarget = paneTargetNewTab
			m.dialog = dialogCreatePane
			m.selectedPlugin = "claude-code"
			m.createPaneDest = dest
			m.sandboxOn, m.sandboxImage = true, "quil-sandbox:latest"
			mem := memStore(&m)

			m.handleCreatePaneSplit()

			if mem[dest] != "quil-sandbox:latest" {
				t.Errorf("remembered = %v, want the image under %q", mem, dest)
			}
			for d := range mem {
				if d != dest {
					t.Errorf("image filed under %q, not the pinned dest %q", d, dest)
				}
			}
		})
	}
}

// A plain (non-sandbox) create remembers nothing.
func TestCreatePaneSubmit_NoSandboxRemembersNothing(t *testing.T) {
	m := newTabModel(t)
	m.createPaneTarget = paneTargetNewTab
	m.dialog = dialogCreatePane
	m.selectedPlugin = "claude-code"
	m.sandboxImage = "quil-sandbox:latest" // pre-filled, but the switch is off
	mem := memStore(&m)
	m.handleCreatePaneSplit()
	if len(mem) != 0 {
		t.Errorf("a non-sandbox create remembered %v", mem)
	}
}

// A create the client refuses before sending (here: a new-branch worktree
// whose repository root is not known yet) created nothing, so the image must
// not be remembered as used on that host.
func TestCreatePaneSubmit_RefusedCreateRemembersNothing(t *testing.T) {
	m := newTabModel(t)
	m.createPaneTarget = paneTargetNewTab
	m.dialog = dialogCreatePane
	m.selectedPlugin = "claude-code"
	m.worktreeNewBranch = "feat/x" // with no worktrees.root: refused locally
	m.sandboxOn, m.sandboxImage = true, "quil-sandbox:latest"
	mem := memStore(&m)

	m.handleCreatePaneSplit()

	if len(mem) != 0 {
		t.Errorf("a refused create remembered %v", mem)
	}
}

// The split and replace forms send from their own closures; both remember the
// image once the create actually leaves.
func TestCreatePaneSubmit_SplitAndReplaceRememberTheImage(t *testing.T) {
	for name, cursor := range map[string]int{"split": 0, "replace": 2} {
		t.Run(name, func(t *testing.T) {
			m := newBranchModel(t)
			m.client = &fakeSender{}
			m.selectedPlugin = "claude-code"
			m.selectedCWD = "/repo"
			m.dialogCursor = cursor
			m.sandboxOn, m.sandboxImage = true, "quil-sandbox:latest"
			mem := memStore(&m)

			_, cmd := m.handleCreatePaneSplit()
			runCmd(cmd)

			if mem[""] != "quil-sandbox:latest" {
				t.Errorf("remembered = %v, want the image under the local dest", mem)
			}
		})
	}
}
