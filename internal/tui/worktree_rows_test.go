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

func confirmPress(m Model, key string) Model { return applyModel(m.handleConfirmKey(keyPressFor(key))) }

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

	m = confirmPress(m, "pgdown")
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
	m.confirmRow = confirmRowLocal // left over from a previous dialog
	m = applyModel(m.openClosePaneConfirm())
	if m.confirmRow != confirmRowWorktree {
		t.Errorf("confirmRow = %d on open, want %d", m.confirmRow, confirmRowWorktree)
	}
}

// A greyed row does nothing when toggled, and the branch rows are greyed until
// the worktree row is armed: a branch cannot go while its worktree stays.
func TestCloseConfirm_BranchRowsInertUntilWorktreeArmed(t *testing.T) {
	m := openLoadedConfirm(t, 1, "")

	m = confirmPress(m, "down")
	m = confirmPress(m, " ")
	if m.confirmDeleteBranch {
		t.Fatal("the local branch row armed while the worktree row was off")
	}

	m = confirmPress(m, "up")
	m = confirmPress(m, " ") // arm the worktree
	m = confirmPress(m, "down")
	m = confirmPress(m, " ")
	if !m.confirmDeleteBranch {
		t.Fatal("the local branch row did not arm once the worktree row was on")
	}

	m = confirmPress(m, "up")
	m = confirmPress(m, " ") // disarm the worktree
	if m.confirmDeleteBranch {
		t.Error("disarming the worktree left the branch row armed")
	}
}

// The dialog never offers a remote branch delete — Quil deletes local
// branches only — and Enter accepts a local delete with the branch the
// dialog showed.
func TestCloseConfirm_LocalDeleteOnlyAndEnterAccepts(t *testing.T) {
	m := openLoadedConfirm(t, 1, "origin")
	if out := strings.ToLower(stripANSI(m.renderConfirmDialog())); strings.Contains(out, "remote") {
		t.Fatalf("the dialog offers something remote:\n%s", out)
	}
	m = confirmPress(m, " ")
	m = confirmPress(m, "down")
	m = confirmPress(m, " ")
	m = confirmPress(m, "down") // no third row: the cursor stays
	if m.confirmRow != confirmRowLocal {
		t.Fatalf("confirmRow = %d, want the local row (the last one)", m.confirmRow)
	}

	fake := &fakeSender{}
	m.client = fake
	_, cmd := m.handleConfirmKey(keyPressFor("enter"))
	if cmd != nil {
		cmd()
	}
	var p ipc.DestroyPanePayload
	if !decodeSent(t, fake, ipc.MsgDestroyPane, &p) {
		t.Fatal("Enter sent no destroy_pane")
	}
	if !p.RemoveWorktree || !p.DeleteBranch || len(p.Confirmations) != 1 ||
		p.Confirmations[0].Branch != "feat/a" || p.Confirmations[0].WorktreePath != "/w/feat-a" {
		t.Errorf("payload = %+v, want worktree + local delete of feat/a", p)
	}
}

// The worktree is never removed before its file list is on screen: Enter
// with the worktree ticked waits while the row still says "checking…".
//
// Fails if the accept branch sends before every row has LOADED.
func TestCloseConfirm_EnterWaitsForTheFileList(t *testing.T) {
	m := modelWithWorktreePane()
	m.cfg = config.Default()
	m = applyModel(m.openClosePaneConfirm())
	m = confirmPress(m, " ") // tick the worktree before the status arrives
	fake := &fakeSender{}
	m.client = fake

	m = confirmPress(m, "enter")
	if len(fake.sent) != 0 || m.dialog != dialogConfirm || !m.confirmRemoveWorktree {
		t.Fatalf("Enter acted before the file list loaded (sent %d)", len(fake.sent))
	}

	m.confirmWorktrees[0].loaded = true
	_, cmd := m.handleConfirmKey(keyPressFor("enter"))
	if cmd != nil {
		cmd()
	}
	var p ipc.DestroyPanePayload
	if !decodeSent(t, fake, ipc.MsgDestroyPane, &p) || !p.RemoveWorktree {
		t.Errorf("Enter after the list loaded did not send the removal: %+v", p)
	}
}

// Every new field is cleared on the way out, like the worktree toggle.
func TestCloseConfirm_ResetOnEveryExit(t *testing.T) {
	m := openLoadedConfirm(t, 25, "")
	m = confirmPress(m, " ")
	m = confirmPress(m, "down")
	m = confirmPress(m, " ")
	m = confirmPress(m, "pgdown")

	m = confirmPress(m, "esc")

	if m.confirmDeleteBranch || m.confirmRow != 0 || m.confirmFileScroll != 0 || m.confirmFileFocus != 0 {
		t.Errorf("esc left state: branch=%v row=%d scroll=%d focus=%d",
			m.confirmDeleteBranch, m.confirmRow, m.confirmFileScroll, m.confirmFileFocus)
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
			m.confirmRemoveWorktree, m.confirmDeleteBranch = true, true

			lines := strings.Count(m.renderConfirmDialog(), "\n") + 1
			if box := lines + dialogVChrome; box > height {
				t.Errorf("box is %d rows in a %d-row terminal:\n%s", box, height, stripANSI(m.renderConfirmDialog()))
			}
		})
	}
}

// A greyed row's reason can be git's own error text: it is sanitised.
func TestCloseConfirm_BranchReasonIsSanitized(t *testing.T) {
	m := openLoadedConfirm(t, 1, "")
	w := &m.confirmWorktrees[0]
	w.branchDeletable, w.branchReason = false, "could not read the branch: \x1b]52;c;aGk=\x07boom"

	out := m.renderConfirmDialog()
	if strings.Contains(out, "\x1b]52") {
		t.Errorf("the branch reason reached the screen raw: %q", out)
	}
}

// Two worktrees: each confirmation names ITS worktree, so one row's branch
// cannot confirm the other's on the daemon.
func TestCloseConfirm_ConfirmationsArePerWorktree(t *testing.T) {
	m := modelWithPanes(worktreePane("pane-a", "/w/feat-a", "feat/a"), worktreePane("pane-b", "/w/feat-b", "feat/b"))
	m.cfg = config.Default()
	m = applyModel(m.openCloseTabConfirm())
	for i, b := range []string{"feat/a", "feat/b"} {
		w := &m.confirmWorktrees[i]
		w.loaded, w.branch, w.branchDeletable = true, b, true
	}
	m = confirmPress(m, " ")
	m = confirmPress(m, "down")
	m = confirmPress(m, " ")

	_, got := m.confirmBranchSelection()
	if len(got) != 2 || got[0].WorktreePath != m.confirmWorktrees[0].path || got[0].Branch != "feat/a" ||
		got[1].WorktreePath != m.confirmWorktrees[1].path || got[1].Branch != "feat/b" {
		t.Errorf("confirmations = %+v, want one per worktree, tied to its path", got)
	}
}

// A pane whose shell left its worktree shows another checkout's PR: that PR
// is not claimed for the worktree being deleted.
func TestCollectConfirmWorktrees_NoPRWhenShellLeftTheWorktree(t *testing.T) {
	p := worktreePane("pane-a", "/w/feat-b", "feat/b")
	p.WorktreePath = "/w/feat-a"
	p.PRNumber, p.PRStatus = 7, "open"

	got := collectConfirmWorktrees([]*PaneModel{p})

	if len(got) != 1 || got[0].prOpen || got[0].prNumber != 0 {
		t.Errorf("row = %+v, want no PR claimed for a pane that left its worktree", got)
	}
}

// Six worktrees, every row armed and every warning on: the box still fits a
// 24-row terminal, because the worktree list gives way.
func TestCloseConfirm_SixWorktreesFitTerminal(t *testing.T) {
	m := sixWorktreeConfirm(t)

	out := m.renderConfirmDialog()
	if box := strings.Count(out, "\n") + 1 + dialogVChrome; box > m.height {
		t.Errorf("box is %d rows in a %d-row terminal:\n%s", box, m.height, stripANSI(out))
	}
	if !strings.Contains(stripANSI(out), "Esc") {
		t.Error("the footer is not drawn")
	}
}

// sixWorktreeConfirm opens the close dialog for a tab of six loaded
// worktrees, every row armed and every warning on, in a 24-row terminal.
func sixWorktreeConfirm(t *testing.T) Model {
	t.Helper()
	var panes []*PaneModel
	for i := 0; i < 6; i++ {
		panes = append(panes, worktreePane(fmt.Sprintf("pane-%d", i), fmt.Sprintf("/w/feat-%d", i), fmt.Sprintf("feat/%d", i)))
	}
	panes[0].PRNumber, panes[0].PRStatus = 12, "open"
	m := modelWithPanes(panes...)
	m.cfg = config.Default()
	m.width, m.height = 100, 24
	m = applyModel(m.openCloseTabConfirm())
	for i := range m.confirmWorktrees {
		w := &m.confirmWorktrees[i]
		w.loaded, w.changes = true, 30
		for j := 0; j < 30; j++ {
			w.files = append(w.files, ipc.WorktreeFile{Code: " M", Path: fmt.Sprintf("f%02d", j)})
		}
		w.branch, w.branchDeletable, w.unmerged = fmt.Sprintf("feat/%d", i), true, 3
		w.remote, w.remoteBranch = "origin", w.branch
	}
	m.confirmRemoveWorktree, m.confirmDeleteBranch = true, true
	return m
}

// Every worktree the close removes stays inspectable: → walks past the rows
// a short terminal draws, the drawn window follows, and the box still fits.
//
// Fails if the focus wraps at the drawn rows, or the window does not follow.
func TestCloseConfirm_ArrowsReachHiddenWorktrees(t *testing.T) {
	m := sixWorktreeConfirm(t)
	if cap := m.confirmRowCap(); cap >= 6 {
		t.Fatalf("cap = %d, want fewer than 6 rows for this test to mean anything", cap)
	}

	for i := 0; i < 5; i++ {
		m = confirmPress(m, "right")
	}

	if w, _ := m.focusedWorktree(); w.path != "/w/feat-5" {
		t.Fatalf("focus = %q after 5 rights, want the last worktree /w/feat-5", w.path)
	}
	out := stripANSI(m.renderConfirmDialog())
	focused := false
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "▸ ") && strings.Contains(line, "5") {
			focused = true
		}
	}
	if !focused {
		t.Errorf("the last worktree is not drawn as focused:\n%s", out)
	}
	if !strings.Contains(out, "more not shown") {
		t.Errorf("hidden worktrees are not counted:\n%s", out)
	}
	if box := strings.Count(out, "\n") + 1 + dialogVChrome; box > m.height {
		t.Errorf("box is %d rows in a %d-row terminal:\n%s", box, m.height, out)
	}

	m = confirmPress(m, "right")
	if w, _ := m.focusedWorktree(); w.path != "/w/feat-0" {
		t.Errorf("focus = %q after wrapping, want /w/feat-0", w.path)
	}
}
