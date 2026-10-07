package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/plugin"
)

// invalidNameModel is newBranchModel inside the setup dialog of a plugin
// that asks for a directory, with the worktree field focused and its listing
// already answered for the browsed directory.
func invalidNameModel(t *testing.T) (Model, *fakeSender, *plugin.PanePlugin) {
	t.Helper()
	dir := t.TempDir()
	toml := "[plugin]\nname = \"wt\"\ndisplay_name = \"WT\"\ncategory = \"tools\"\n\n[command]\ncmd = \"true\"\nprompts_cwd = true\n"
	if err := os.WriteFile(filepath.Join(dir, "wt.toml"), []byte(toml), 0o644); err != nil {
		t.Fatalf("write toml: %v", err)
	}
	r := plugin.NewRegistry()
	if err := r.LoadFromDir(dir); err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}
	m := newBranchModel(t)
	m.pluginRegistry = r
	m.selectedPlugin = "wt"
	m.dialog = dialogCreatePaneSetup
	m.worktrees.path = m.cwdBrowseDir // answered: focusing the field asks nothing
	sender := &fakeSender{}
	m.client = sender
	p := r.Get("wt")
	if p == nil {
		t.Fatal("setup: plugin wt did not load")
	}
	m.setupFieldCursor = m.setupFieldIndex(p, "worktree")
	if kind, _ := m.setupFieldKind(p, m.setupFieldCursor); kind != "worktree" {
		t.Fatalf("setup: field %d is %q, want worktree", m.setupFieldCursor, kind)
	}
	return m, sender, p
}

func typeText(t *testing.T, m Model, s string) Model {
	t.Helper()
	for _, r := range s {
		m = updateKey(t, m, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return m
}

func updateKey(t *testing.T, m Model, msg tea.KeyPressMsg) Model {
	t.Helper()
	next, _ := m.Update(msg)
	return next.(Model)
}

// A new branch named `bad..name` showed its error in the name field, lost it
// when focus moved to another field, and then Continue did nothing and said
// nothing (manual retest, PR #256). The error now stays on the field while
// the name is invalid, and a submit moves focus back to the open name field
// with the reason under it. Nothing is sent.
func TestWorktreeInvalidName_ErrorStaysAndSubmitReturnsToIt(t *testing.T) {
	m, sender, p := invalidNameModel(t)
	want := m.worktrees.validateNewBranch("bad..name")
	if want == "" {
		t.Fatal("setup: bad..name is valid")
	}

	m.worktreeCursor = worktreeRowIndex(t, m, worktreeNewRowPath)
	m = updateKey(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.worktreeNaming {
		t.Fatal("setup: Enter on the new-branch row did not open the name field")
	}
	m = typeText(t, m, "bad..name")
	m = updateKey(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.worktreeErr != want {
		t.Fatalf("worktreeErr = %q, want %q", m.worktreeErr, want)
	}

	// Focus moves on: the error is still drawn on the worktree row.
	m = updateKey(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	if kind, _ := m.setupFieldKind(p, m.setupFieldCursor); kind == "worktree" {
		t.Fatal("setup: Tab did not move focus off the worktree field")
	}
	if out := stripANSI(m.renderCreatePaneSetupDialog()); !strings.Contains(out, want) {
		t.Errorf("after Tab the dialog no longer shows %q:\n%s", want, out)
	}

	// Submit from Continue.
	m.setupFieldCursor = m.setupFieldIndex(p, "continue")
	m = updateKey(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if m.dialog != dialogCreatePaneSetup {
		t.Fatalf("the submit left the setup dialog (dialog %v, step %d)", m.dialog, m.createPaneStep)
	}
	if kind, _ := m.setupFieldKind(p, m.setupFieldCursor); kind != "worktree" {
		t.Errorf("focus after the refused submit is on %q, want the worktree field", kind)
	}
	if !m.worktreeNaming {
		t.Error("the name field is not open after the refused submit")
	}
	if out := stripANSI(m.renderCreatePaneSetupDialog()); !strings.Contains(out, want) {
		t.Errorf("after the refused submit the dialog does not show %q:\n%s", want, out)
	}
	for _, msg := range sender.sent {
		if msg.Type == ipc.MsgCreatePane || msg.Type == ipc.MsgCreateTab {
			t.Errorf("%s sent for an invalid branch name", msg.Type)
		}
	}
}

// A name typed and left without Enter is checked when focus leaves the
// field, so its error shows too; a valid one shows none.
func TestWorktreeInvalidName_CheckedWhenFocusLeaves(t *testing.T) {
	for _, tc := range []struct {
		name    string
		invalid bool
	}{{"bad..name", true}, {"feat/ok", false}} {
		m, _, _ := invalidNameModel(t)
		m.worktreeCursor = worktreeRowIndex(t, m, worktreeNewRowPath)
		m = updateKey(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
		m = typeText(t, m, tc.name)
		m = updateKey(t, m, tea.KeyPressMsg{Code: tea.KeyTab})

		out := stripANSI(m.renderCreatePaneSetupDialog())
		shown := strings.Contains(out, "branch name may not")
		if shown != tc.invalid {
			t.Errorf("%q: error shown = %v after Tab, want %v:\n%s", tc.name, shown, tc.invalid, out)
		}
	}
}
