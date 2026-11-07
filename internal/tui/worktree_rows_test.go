package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// openLoadedConfirm opens the close dialog for one worktree pane and lands a
// status answer on its row: files, a deletable branch and, when remote is
// non-empty, a remote.
func openLoadedConfirm(t *testing.T, files int, remote string) Model {
	t.Helper()
	m := modelWithWorktreePane()
	m.cfg = config.Default()
	m = applyModel(m.openClosePaneConfirm())
	if len(m.confirmWorktrees) != 1 {
		t.Fatalf("confirmWorktrees = %+v, want one", m.confirmWorktrees)
	}
	w := &m.confirmWorktrees[0]
	w.loaded, w.changes = true, files
	for i := 0; i < files; i++ {
		w.files = append(w.files, ipc.WorktreeFile{Code: "??", Path: fmt.Sprintf("f%02d.txt", i)})
	}
	w.branch, w.branchDeletable, w.defaultBranch, w.unmerged = "feat/a", true, "master", 2
	if remote != "" {
		w.remote, w.remoteBranch = remote, "feat/a"
	} else {
		w.remoteReason = "no remote branch"
	}
	return m
}

func press(m Model, key string) Model { return applyModel(m.handleConfirmKey(keyPressFor(key))) }

// The list shows a page of files and PgDn moves to the next page.
//
// Fails if the file rows are not rendered, are not capped at the page, or if
// PgDn does not move the window.
func TestCloseConfirm_ListsFilesAndScrolls(t *testing.T) {
	m := openLoadedConfirm(t, 25, "")

	out := stripANSI(m.renderConfirmDialog())
	if !strings.Contains(out, "f00.txt") || !strings.Contains(out, "f09.txt") || strings.Contains(out, "f10.txt") {
		t.Fatalf("first page should hold f00..f09 only:\n%s", out)
	}
	if !strings.Contains(out, "1–10 of 25") {
		t.Errorf("no position line for a list longer than the page:\n%s", out)
	}

	m = press(m, "pgdown")
	out = stripANSI(m.renderConfirmDialog())
	if !strings.Contains(out, "f10.txt") || strings.Contains(out, "f09.txt") {
		t.Errorf("PgDn did not show the next page:\n%s", out)
	}
}

// A file name is remote text: an escape sequence or a bidi override in it
// must not reach the terminal.
func TestCloseConfirm_FileNameIsSanitizedAndBounded(t *testing.T) {
	m := openLoadedConfirm(t, 1, "")
	m.confirmWorktrees[0].files[0].Path = "\x1b[31mred‮evil" + strings.Repeat("x", 300)

	out := m.renderConfirmDialog()
	if strings.Contains(out, "\x1b[31mred") || strings.Contains(out, "‮") {
		t.Errorf("the file name reached the screen raw: %q", out)
	}
	budget := dialogWidth - dialogBoxChrome
	for _, line := range strings.Split(stripANSI(out), "\n") {
		if len([]rune(line)) > budget+2 {
			t.Errorf("line is wider than the box: %q", line)
		}
	}
}

// The cursor starts on the worktree row on every open, so space does what it
// always did.
func TestCloseConfirm_CursorStartsOnWorktreeRow(t *testing.T) {
	m := modelWithWorktreePane()
	m.confirmRow = confirmRowRemote // left over from a previous dialog
	m = applyModel(m.openClosePaneConfirm())
	if m.confirmRow != confirmRowWorktree {
		t.Errorf("confirmRow = %d on open, want %d", m.confirmRow, confirmRowWorktree)
	}
}

// A greyed row does nothing when toggled, and the branch rows are greyed until
// the worktree row is armed: a branch cannot go while its worktree stays.
func TestCloseConfirm_BranchRowsInertUntilWorktreeArmed(t *testing.T) {
	m := openLoadedConfirm(t, 1, "")

	m = press(m, "down")
	m = press(m, " ")
	if m.confirmDeleteBranch {
		t.Fatal("the local branch row armed while the worktree row was off")
	}

	m = press(m, "up")
	m = press(m, " ") // arm the worktree
	m = press(m, "down")
	m = press(m, " ")
	if !m.confirmDeleteBranch {
		t.Fatal("the local branch row did not arm once the worktree row was on")
	}

	m = press(m, "up")
	m = press(m, " ") // disarm the worktree
	if m.confirmDeleteBranch {
		t.Error("disarming the worktree left the branch row armed")
	}
}

// With the F1 switch off the remote row says so and cannot be armed.
func TestCloseConfirm_RemoteRowGreyedWhenSettingOff(t *testing.T) {
	m := openLoadedConfirm(t, 1, "origin")
	m = press(m, " ")
	m = press(m, "down")
	m = press(m, "down")
	m = press(m, " ")

	if m.confirmDeleteRemote {
		t.Error("the remote row armed with the setting off")
	}
	if out := stripANSI(m.renderConfirmDialog()); !strings.Contains(out, "off in F1") {
		t.Errorf("the remote row does not say why it is off:\n%s", out)
	}
}

// Enter does not accept a remote delete; `y` does, and the payload carries the
// flag and the branch the dialog showed.
func TestCloseConfirm_EnterDoesNotAcceptWithRemoteArmed(t *testing.T) {
	m := openLoadedConfirm(t, 1, "origin")
	m.cfg.Worktree.RemoteBranchDelete = true
	m = press(m, " ")
	m = press(m, "down")
	m = press(m, "down")
	m = press(m, " ")
	if !m.confirmDeleteRemote {
		t.Fatal("the remote row did not arm with the setting on")
	}

	fake := &fakeSender{}
	m.client = fake
	m = press(m, "enter")
	if !m.confirmDeleteRemote || len(m.confirmWorktrees) == 0 {
		t.Fatal("a refused Enter dropped the arm or closed the dialog state")
	}
	if len(fake.sent) != 0 {
		t.Fatalf("Enter sent %d messages with a remote delete armed", len(fake.sent))
	}

	_, cmd := m.handleConfirmKey(keyPressFor("y"))
	if cmd != nil {
		cmd()
	}
	var p ipc.DestroyPanePayload
	if !decodeSent(t, fake, ipc.MsgDestroyPane, &p) {
		t.Fatal("y sent no destroy_pane")
	}
	if !p.RemoveWorktree || !p.DeleteRemoteBranch || p.DeleteBranch ||
		len(p.ConfirmedBranches) != 1 || p.ConfirmedBranches[0] != "feat/a" {
		t.Errorf("payload = %+v, want worktree + remote delete of feat/a only", p)
	}
}

// Every new field is cleared on the way out, like the worktree toggle.
func TestCloseConfirm_ResetOnEveryExit(t *testing.T) {
	m := openLoadedConfirm(t, 25, "")
	m = press(m, " ")
	m = press(m, "down")
	m = press(m, " ")
	m = press(m, "pgdown")

	m = press(m, "esc")

	if m.confirmDeleteBranch || m.confirmDeleteRemote || m.confirmRow != 0 || m.confirmFileScroll != 0 || m.confirmFileFocus != 0 {
		t.Errorf("esc left state: branch=%v remote=%v row=%d scroll=%d focus=%d",
			m.confirmDeleteBranch, m.confirmDeleteRemote, m.confirmRow, m.confirmFileScroll, m.confirmFileFocus)
	}
}

// The box is never taller than the terminal, at a small and an ordinary size,
// with three worktrees full of files and every row armed. lipgloss.Place does
// not clip, so an overflow pushes the footer off screen.
func TestCloseConfirm_FitsTerminal(t *testing.T) {
	for _, height := range []int{24, 30} {
		t.Run(fmt.Sprint(height), func(t *testing.T) {
			var panes []*PaneModel
			for i := 0; i < 3; i++ {
				panes = append(panes, worktreePane(fmt.Sprintf("pane-%d", i), fmt.Sprintf("/w/feat-%d", i), fmt.Sprintf("feat/%d", i)))
			}
			panes[0].PRNumber, panes[0].PRStatus = 12, "open"
			m := modelWithPanes(panes...)
			m.cfg = config.Default()
			m.cfg.Worktree.RemoteBranchDelete = true
			m.width, m.height = 100, height
			m = applyModel(m.openCloseTabConfirm())
			for i := range m.confirmWorktrees {
				w := &m.confirmWorktrees[i]
				w.loaded, w.changes = true, 30
				for j := 0; j < 30; j++ {
					w.files = append(w.files, ipc.WorktreeFile{Code: " M", Path: fmt.Sprintf("dir/f%02d.go", j)})
				}
				w.branch, w.branchDeletable, w.unmerged = fmt.Sprintf("feat/%d", i), true, 3
				w.remote, w.remoteBranch = "origin", w.branch
			}
			m.confirmRemoveWorktree, m.confirmDeleteBranch, m.confirmDeleteRemote = true, true, true

			lines := strings.Count(m.renderConfirmDialog(), "\n") + 1
			if box := lines + dialogVChrome; box > height {
				t.Errorf("box is %d rows in a %d-row terminal:\n%s", box, height, stripANSI(m.renderConfirmDialog()))
			}
		})
	}
}
