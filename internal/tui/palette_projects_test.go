package tui

import (
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/config"
)

// paletteModelWithProjects builds a two-project model — one local, one remote —
// which is the shape every project row has to distinguish.
func paletteModelWithProjects(t *testing.T) Model {
	t.Helper()
	local := &ProjectModel{ID: "proj-local", Name: "quil"}
	local.tabs = []*TabModel{tabWithPane("tab-1", "pane-1")}
	remote := &ProjectModel{ID: "proj-remote", Name: "build", Dest: "gpu01"}
	remote.tabs = []*TabModel{tabWithPane("tab-2", "pane-2")}

	return Model{
		width: 120, height: 40,
		cfg:           config.Default(),
		client:        newFakeConn(),
		projects:      []*ProjectModel{local, remote},
		activeProject: 0,
	}
}

func paletteLabels(m Model) []string {
	var out []string
	for _, c := range m.buildPaletteCommands() {
		out = append(out, c.label)
	}
	return out
}

// TestPalette_OffersEveryProjectAction: the palette advertises itself as a
// launcher for "every action", and each row shows its shortcut, so it is also
// how the bindings get taught. Shipping projects without them there left the
// whole feature reachable only by a key you had to already know.
func TestPalette_OffersEveryProjectAction(t *testing.T) {
	m := paletteModelWithProjects(t)
	labels := strings.Join(paletteLabels(m), "\n")

	for _, want := range []string{
		"Switch to build@gpu01", // the other project, by displayName
		"New project",
		"Rename project",
		"Destroy project…",  // active project is local
		"Previous project",
		"Go to the agent waiting longest",
		"Show project sidebar",
	} {
		if !strings.Contains(labels, want) {
			t.Errorf("palette has no %q row.\ngot:\n%s", want, labels)
		}
	}
}

// TestPalette_SkipsTheActiveProject: a "Switch to" row for the project you are
// already on is a no-op that pushes real destinations down the list.
func TestPalette_SkipsTheActiveProject(t *testing.T) {
	m := paletteModelWithProjects(t)
	if labels := strings.Join(paletteLabels(m), "\n"); strings.Contains(labels, "Switch to quil") {
		t.Error("palette offers a Switch to row for the ACTIVE project")
	}
}

// TestPalette_RemoteProjectOffersDisconnectNotDestroy pins the same one-action
// rule the sidebar menu and the keybinding follow. Destroy cannot do what the
// user means on a remote project — the daemon bootstraps a replacement — so
// offering both is offering one that silently does the wrong thing.
func TestPalette_RemoteProjectOffersDisconnectNotDestroy(t *testing.T) {
	m := paletteModelWithProjects(t)
	m.activeProject = 1 // the remote one

	labels := strings.Join(paletteLabels(m), "\n")
	if !strings.Contains(labels, "Disconnect host…") {
		t.Errorf("no Disconnect row on a remote project.\ngot:\n%s", labels)
	}
	if strings.Contains(labels, "Destroy project…") {
		t.Error("palette offers BOTH Destroy and Disconnect on a remote project")
	}
}

// TestPalette_SidebarRowNamesTheAction: a toggle labelled with its current
// state leaves the user working out which way Enter moves it. Both of the
// palette's other toggles name the action, so this one must too.
func TestPalette_SidebarRowNamesTheAction(t *testing.T) {
	m := paletteModelWithProjects(t)
	m.sidebarOpen = true
	if labels := strings.Join(paletteLabels(m), "\n"); !strings.Contains(labels, "Hide project sidebar") {
		t.Error("with the sidebar open the row should offer to Hide it")
	}
	m.sidebarOpen = false
	if labels := strings.Join(paletteLabels(m), "\n"); !strings.Contains(labels, "Show project sidebar") {
		t.Error("with the sidebar closed the row should offer to Show it")
	}
}

// TestPalette_ProjectRowsCarryTheirShortcuts: each row shows its binding, which
// is what makes the palette teach the keymap as it is used. A project row with
// an empty detail column teaches nothing.
func TestPalette_ProjectRowsCarryTheirShortcuts(t *testing.T) {
	m := paletteModelWithProjects(t)
	m.initKeymap() // buildPaletteCommands dispatches through the keymap; NewModel builds it
	kb := m.cfg.Keybindings

	want := map[string]string{
		"New project":                     kb.NewProject,
		"Destroy project…":                kb.DestroyProject,
		"Previous project":                kb.ProjectToggle,
		"Go to the agent waiting longest": kb.AttentionQueue,
		"Show project sidebar":            kb.SidebarToggle,
	}
	for _, c := range m.buildPaletteCommands() {
		binding, tracked := want[c.label]
		if !tracked {
			continue
		}
		if c.detail != kbDisplay(binding) {
			t.Errorf("row %q shows detail %q, want the binding %q",
				c.label, c.detail, kbDisplay(binding))
		}
		delete(want, c.label)
	}
	for label := range want {
		t.Errorf("row %q never appeared in the palette", label)
	}
}
