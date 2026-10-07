package tui

import (
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/plugin"
)

// A standard token may not start a program by raw arguments: no overlay pane
// and no plugin instance. The daemon refuses both, but the TUI sends them
// id-less, so without a client-side refusal the user saw nothing happen and
// nothing say why. Each entry point is driven through Update against all three
// rights levels; full is the control that proves the path is reachable.

// rawArgsRegistry holds the two overlay tools, installed, and a plugin started
// by instance (form fields, like ssh).
func rawArgsRegistry(t *testing.T) *plugin.Registry {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"lazygit.toml": "[plugin]\nname = \"lazygit\"\ndisplay_name = \"Lazygit\"\ncategory = \"tools\"\n[command]\ncmd = \"lazygit\"\n",
		"hunk.toml":    "[plugin]\nname = \"hunk\"\ndisplay_name = \"hunk\"\ncategory = \"tools\"\n[command]\ncmd = \"hunk\"\nargs = [\"diff\"]\n",
		"remotex.toml": "[plugin]\nname = \"remotex\"\ndisplay_name = \"Remotex\"\ncategory = \"remote\"\n" +
			"[command]\ncmd = \"ssh\"\narg_template = [\"{user}@{host}\"]\n" +
			"[[command.form_fields]]\nname = \"name\"\nlabel = \"Name\"\nrequired = true\n" +
			"[[command.form_fields]]\nname = \"host\"\nlabel = \"Host\"\nrequired = true\n" +
			"[[command.form_fields]]\nname = \"user\"\nlabel = \"User\"\nrequired = true\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	r := plugin.NewRegistry()
	if err := r.LoadFromDir(dir); err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}
	for _, name := range []string{"lazygit", "hunk", "remotex"} {
		p := r.Get(name)
		if p == nil {
			t.Fatalf("setup: plugin %s did not load", name)
		}
		p.Available = true
	}
	return r
}

// rawArgsModel is readOnlyModel with that registry, the active pane in a
// repository, and a window.
func rawArgsModel(t *testing.T, rights string) (Model, *fakeConn) {
	t.Helper()
	t.Setenv("QUIL_HOME", t.TempDir()) // the instance form and recent dirs persist
	m, conn := readOnlyModel(t, rights)
	m.pluginRegistry = rawArgsRegistry(t)
	m.projects[0].tabs[0].Root.Pane.CWD = "/repo"
	m = roUpdate(t, m, tea.WindowSizeMsg{Width: 172, Height: 48})
	clearSent(conn)
	return m, conn
}

// sentOverlayCreate reports whether conn carried a create_pane for an overlay.
func sentOverlayCreate(t *testing.T, conn *fakeConn) bool {
	t.Helper()
	conn.mu.Lock()
	defer conn.mu.Unlock()
	for _, msg := range conn.sent {
		if msg.Type != ipc.MsgCreatePane {
			continue
		}
		var p ipc.CreatePanePayload
		if err := msg.DecodePayload(&p); err != nil {
			t.Fatalf("decode create_pane: %v", err)
		}
		if p.Overlay {
			return true
		}
	}
	return false
}

// answerRepos delivers the daemon's git-discovery answer for the request in
// flight, as the listener would.
func answerRepos(t *testing.T, m Model, repos ...string) Model {
	t.Helper()
	if m.repoScan.gen == "" {
		t.Fatal("setup: no git discovery in flight")
	}
	return roUpdate(t, m, gitReposMsg{
		Resp: ipc.GitReposRespPayload{CWD: m.repoScan.cwd, Repos: repos},
		Gen:  m.repoScan.gen,
	})
}

var altG = tea.KeyPressMsg{Code: 'g', Mod: tea.ModAlt}

// Alt+G on a tab with no overlay: refused at the key for read-only and
// standard, each with its own reason, before asking the daemon anything; full
// asks, and the answer creates the overlay.
func TestNoRawArgs_OverlayKeyRefused(t *testing.T) {
	for _, tc := range []struct{ rights, flash string }{
		{ipc.RightsReadOnly, readOnlyFlash},
		{ipc.RightsStandard, noRawArgsFlash},
		{ipc.RightsFull, ""},
	} {
		t.Run(tc.rights, func(t *testing.T) {
			full := tc.rights == ipc.RightsFull
			m, conn := rawArgsModel(t, tc.rights)
			m = roUpdate(t, m, altG)
			if asked := sentType(conn, ipc.MsgGitReposReq); asked != full {
				t.Fatalf("git_repos_req sent = %v on rights %q", asked, tc.rights)
			}
			if m.flashText != tc.flash {
				t.Fatalf("flash = %q, want %q", m.flashText, tc.flash)
			}
			if full {
				m = answerRepos(t, m, "/repo")
			}
			if created := sentOverlayCreate(t, conn); created != full {
				t.Fatalf("overlay create sent = %v on rights %q", created, tc.rights)
			}
		})
	}
}

// A standard token may still show the overlay its tab already runs — that
// creates nothing — but a repository the overlay is not on would REPLACE it,
// and that is refused with the reason. Full replaces.
func TestNoRawArgs_ExistingOverlayShownNotReplaced(t *testing.T) {
	for _, tc := range []struct {
		rights, repo string
		shown        bool
		created      bool
		flash        string
	}{
		{ipc.RightsStandard, "/repo", true, false, ""},
		{ipc.RightsStandard, "/other", false, false, noRawArgsFlash},
		{ipc.RightsFull, "/other", false, true, ""},
	} {
		t.Run(tc.rights+tc.repo, func(t *testing.T) {
			m, conn := rawArgsModel(t, tc.rights)
			ov := NewPaneModel("ov-1", testRingBufSize)
			t.Cleanup(ov.Dispose)
			ov.Type, ov.CWD = overlayPluginLazygit, "/repo"
			tab := m.projects[0].tabs[0]
			tab.overlayPane, tab.overlayVisible = ov, false

			m = roUpdate(t, m, altG)
			if !sentType(conn, ipc.MsgGitReposReq) {
				t.Fatal("setup: discovery not asked with an overlay to show")
			}
			m = answerRepos(t, m, tc.repo)
			tab = m.projects[0].tabs[0]
			if tab.overlayVisible != tc.shown {
				t.Fatalf("overlay shown = %v, want %v", tab.overlayVisible, tc.shown)
			}
			if created := sentOverlayCreate(t, conn); created != tc.created {
				t.Fatalf("overlay create sent = %v, want %v", created, tc.created)
			}
			if m.flashText != tc.flash {
				t.Fatalf("flash = %q, want %q", m.flashText, tc.flash)
			}
		})
	}
}

// The palette and pane-menu rows for both overlay tools are greyed for
// read-only and standard, live for full — the binaries are installed in all
// three, so the grey is the rights alone.
func TestNoRawArgs_OverlayRowsGreyed(t *testing.T) {
	for _, rights := range adminRightsCases {
		t.Run(rights, func(t *testing.T) {
			full := rights == ipc.RightsFull
			m, _ := rawArgsModel(t, rights)

			m = roUpdate(t, m, tea.KeyPressMsg{Code: 'p', Mod: tea.ModAlt | tea.ModShift})
			if m.dialog != dialogCommandPalette {
				t.Fatal("setup: the palette did not open")
			}
			found := 0
			for _, c := range m.paletteDisplay() {
				if c.action == palActLazygit || c.action == palActHunk {
					found++
					if c.enabled != full {
						t.Errorf("palette %q enabled = %v on rights %q", c.label, c.enabled, rights)
					}
				}
			}
			if found != 2 {
				t.Fatalf("setup: %d overlay palette rows, want 2", found)
			}
			m = roUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})

			m = roUpdate(t, m, tea.KeyPressMsg{Code: 'a', Mod: tea.ModAlt})
			assertCtxRows(t, m.ctxMenu.items, full, ctxActLazygit, ctxActHunk)
		})
	}
}

// canOpenOverlay's other branch: a standard token whose tab already runs the
// lazygit overlay may show it, so the lazygit rows are live while the hunk
// rows — which would create an overlay — stay grey. Without the overlayRuns
// branch both rows are grey, as TestNoRawArgs_OverlayRowsGreyed shows for a
// tab with no overlay.
func TestNoRawArgs_RunningOverlayKeepsItsRowsLive(t *testing.T) {
	m, _ := rawArgsModel(t, ipc.RightsStandard)
	ov := NewPaneModel("ov-1", testRingBufSize)
	t.Cleanup(ov.Dispose)
	ov.Type, ov.CWD = overlayPluginLazygit, "/repo"
	tab := m.projects[0].tabs[0]
	tab.overlayPane, tab.overlayVisible = ov, false

	m = roUpdate(t, m, tea.KeyPressMsg{Code: 'p', Mod: tea.ModAlt | tea.ModShift})
	if m.dialog != dialogCommandPalette {
		t.Fatal("setup: the palette did not open")
	}
	found := 0
	for _, c := range m.paletteDisplay() {
		switch c.action {
		case palActLazygit:
			found++
			if !c.enabled {
				t.Errorf("palette %q is grey with that overlay running", c.label)
			}
		case palActHunk:
			found++
			if c.enabled {
				t.Errorf("palette %q is live; it would create an overlay", c.label)
			}
		}
	}
	if found != 2 {
		t.Fatalf("setup: %d overlay palette rows, want 2", found)
	}
	m = roUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})

	m = roUpdate(t, m, tea.KeyPressMsg{Code: 'a', Mod: tea.ModAlt})
	assertCtxRows(t, m.ctxMenu.items, true, ctxActLazygit)
	assertCtxRows(t, m.ctxMenu.items, false, ctxActHunk)
}

// openCreatePaneOn opens Ctrl+N and walks to the named plugin of category
// key, pressing Enter on it.
func openCreatePaneOn(t *testing.T, m Model, category, name string) Model {
	t.Helper()
	m = roUpdate(t, m, tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	if m.dialog != dialogCreatePane {
		t.Fatalf("setup: Ctrl+N left dialog = %v", m.dialog)
	}
	cats := m.createPaneCategories()
	ci := -1
	for i, c := range cats {
		if c.key == category {
			ci = i
		}
	}
	if ci < 0 {
		t.Fatalf("setup: no %q category", category)
	}
	m.dialogCursor = ci
	m = roUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	pi := -1
	for i, p := range cats[ci].plugins {
		if p.Name == name {
			pi = i
		}
	}
	if pi < 0 {
		t.Fatalf("setup: no %q plugin", name)
	}
	m.dialogCursor = pi
	return roUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
}

// Choosing a plugin started by instance: standard is refused before the form,
// with the dialog closed so the flash is on screen; full reaches the form.
func TestNoRawArgs_InstancePluginRefused(t *testing.T) {
	for _, rights := range []string{ipc.RightsStandard, ipc.RightsFull} {
		t.Run(rights, func(t *testing.T) {
			full := rights == ipc.RightsFull
			m, _ := rawArgsModel(t, rights)
			m = openCreatePaneOn(t, m, "remote", "remotex")
			if onForm := m.dialog == dialogInstanceForm; onForm != full {
				t.Fatalf("instance form opened = %v on rights %q (dialog %v)", onForm, rights, m.dialog)
			}
			if !full {
				if m.dialog != dialogNone {
					t.Fatalf("refused dialog left open: %v", m.dialog)
				}
				if m.flashText != noRawArgsFlash {
					t.Fatalf("flash = %q, want the raw-arguments refusal", m.flashText)
				}
			}
		})
	}
}

// The submit refuses on its own, for an instance reached some other way (a
// saved instance chosen before a reconnect lowered the token, say): standard
// sends no create_pane; full sends it with the instance's arguments.
func TestNoRawArgs_InstanceSubmitRefused(t *testing.T) {
	for _, rights := range []string{ipc.RightsStandard, ipc.RightsFull} {
		t.Run(rights, func(t *testing.T) {
			full := rights == ipc.RightsFull
			m, conn := rawArgsModel(t, rights)
			m.dialog, m.createPaneStep, m.dialogCursor = dialogCreatePane, 3, 0
			m.createPaneDest = roDest
			m.selectedPlugin = "remotex"
			m.selectedInstanceName = "box"
			m.selectedInstanceArgs = []string{"u@h"}
			m = roUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
			if sent := sentType(conn, ipc.MsgCreatePane); sent != full {
				t.Fatalf("create_pane sent = %v on rights %q", sent, rights)
			}
			if refused := m.flashText == noRawArgsFlash; refused == full {
				t.Fatalf("raw-arguments flash = %v on rights %q (flash %q)", refused, rights, m.flashText)
			}
		})
	}
}
