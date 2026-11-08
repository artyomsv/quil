package tui

import (
	"fmt"
	"strings"

	"github.com/artyomsv/quil/internal/ipc"
)

// The close dialog's worktree section: the worktree toggle, the files the
// removal destroys, and the local branch row. Kept apart from
// dialog.go so the agent-worktree view can draw the same rows.

// confirmFileRowsMax is the file list's height when the terminal has room.
const confirmFileRowsMax = 10

// dialogVChrome is the rows renderDialog's box adds around its content: the
// two border rows and Padding(1, 2)'s two vertical cells.
const dialogVChrome = 4

// Row indices under the confirm dialog's cursor.
const (
	confirmRowWorktree = 0
	confirmRowLocal    = 1
)

// confirmDeletable reports the worktrees whose branch the dialog may offer to
// delete: facts LOADED, no error, and the daemon said deletable.
func (m Model) confirmDeletable() []confirmWorktree {
	var out []confirmWorktree
	for _, w := range m.confirmWorktrees {
		if w.loaded && w.err == "" && w.branchDeletable && w.branch != "" {
			out = append(out, w)
		}
	}
	return out
}

// localRowReason is "" when the local-branch row can be armed, else why not.
func (m Model) localRowReason() string {
	if !m.confirmRemoveWorktree {
		return "needs the worktree delete"
	}
	if len(m.confirmDeletable()) > 0 {
		return ""
	}
	for _, w := range m.confirmWorktrees {
		if !w.loaded {
			return "checking…"
		}
	}
	for _, w := range m.confirmWorktrees {
		if w.branchReason != "" {
			return w.branchReason
		}
	}
	return "no branch to delete"
}

// confirmBranchSelection is what an accept sends: the local flag as it takes
// effect (an armed row that is no longer enabled counts as off) and the
// branch names on screen, which the daemon only CHECKS against what it reads.
func (m Model) confirmBranchSelection() (local bool, confirmed []ipc.BranchConfirmation) {
	local = m.confirmDeleteBranch && m.localRowReason() == ""
	if !local {
		return false, nil
	}
	// One confirmation PER WORKTREE: the daemon matches each against that
	// worktree alone, so one row's branch cannot confirm another's.
	for _, w := range m.confirmDeletable() {
		confirmed = append(confirmed, ipc.BranchConfirmation{WorktreePath: w.path, Branch: w.branch})
	}
	return local, confirmed
}

// toggleConfirmRow flips the row under the cursor. A greyed row does nothing.
// Disarming the worktree row disarms the branch row too: a branch cannot be
// deleted while its worktree still has it checked out.
func (m *Model) toggleConfirmRow() {
	switch m.confirmRow {
	case confirmRowWorktree:
		if m.agentWTBranchEntry() {
			return // a branch entry has no worktree row to toggle
		}
		// Disarming always works; arming only while the files are known.
		if !m.confirmRemoveWorktree && m.worktreeRowReason() != "" {
			return
		}
		m.confirmRemoveWorktree = !m.confirmRemoveWorktree
		if !m.confirmRemoveWorktree {
			m.confirmDeleteBranch = false
		}
	case confirmRowLocal:
		if m.localRowReason() == "" {
			m.confirmDeleteBranch = !m.confirmDeleteBranch
		}
	}
}

// moveConfirmRow moves the cursor between the two rows.
func (m *Model) moveConfirmRow(delta int) {
	lowest := confirmRowWorktree
	if m.agentWTBranchEntry() {
		lowest = confirmRowLocal
	}
	m.confirmRow = max(lowest, min(confirmRowLocal, m.confirmRow+delta))
}

// focusedWorktree is the worktree whose files are listed.
func (m Model) focusedWorktree() (confirmWorktree, bool) {
	if len(m.confirmWorktrees) == 0 {
		return confirmWorktree{}, false
	}
	i := max(0, min(m.confirmFileFocus, len(m.confirmWorktrees)-1))
	return m.confirmWorktrees[i], true
}

// confirmRowCap is how many worktree rows the dialog draws: at most
// confirmWorktreeMaxRows, and fewer when the terminal is short. Every other
// line of the box (branch rows, warnings, footer) must stay on screen, and
// lipgloss.Place does not clip, so the worktree list gives way first. It is
// measured like confirmFileRows: one probe render with one row and no files.
func (m Model) confirmRowCap() int {
	n := min(len(m.confirmWorktrees), confirmWorktreeMaxRows)
	if m.confirmRowCapSet {
		return min(n, m.confirmRowCapFixed)
	}
	if m.height <= 0 || n <= 1 {
		return n
	}
	probe := m
	probe.confirmRowCapSet, probe.confirmRowCapFixed = true, 1
	probe.confirmFileRowsSet, probe.confirmFileRowsFixed = true, 0
	used := strings.Count(probe.renderConfirmDialog(), "\n") + 1
	// Each further row costs one line; one line stays for a file row.
	spare := m.height - dialogVChrome - used - 1
	return max(1, min(n, 1+spare))
}

// moveConfirmFocus moves the file list to the previous or next worktree. It
// walks EVERY worktree the delete covers, not only the rows drawn: a short
// terminal draws fewer, and each one the close removes must stay inspectable.
// The drawn window follows the focus (confirmWindow).
func (m *Model) moveConfirmFocus(delta int) {
	n := len(m.confirmWorktrees)
	if n == 0 {
		return
	}
	m.confirmFileFocus = (m.confirmFileFocus + delta + n) % n
	m.confirmFileScroll = 0
}

// confirmWindow is the slice of worktrees drawn: confirmRowCap rows, placed
// so the focused worktree is one of them.
func (m Model) confirmWindow() (start, end int) {
	n, rows := len(m.confirmWorktrees), m.confirmRowCap()
	focus := max(0, min(m.confirmFileFocus, n-1))
	start = max(0, min(focus-rows+1, n-rows))
	return start, min(n, start+rows)
}

// scrollConfirmFiles moves the file list by one page.
func (m *Model) scrollConfirmFiles(pages int) {
	w, ok := m.focusedWorktree()
	if !ok {
		return
	}
	rows := m.confirmFileRows()
	last := max(0, len(w.files)-rows)
	m.confirmFileScroll = max(0, min(last, m.confirmFileScroll+pages*rows))
}

// confirmFileRows is how many file rows fit: the box must never be taller than
// the terminal, because lipgloss.Place does not clip and the footer — with the
// keys it documents — would be pushed off screen. It renders the dialog once
// with no file rows to measure everything else, which keeps the budget exact
// whatever the rest of the box holds.
func (m Model) confirmFileRows() int {
	if m.confirmFileRowsSet {
		return m.confirmFileRowsFixed
	}
	if m.height <= 0 {
		return confirmFileRowsMax
	}
	probe := m
	probe.confirmRowCapSet, probe.confirmRowCapFixed = true, m.confirmRowCap()
	probe.confirmFileRowsSet, probe.confirmFileRowsFixed = true, 0
	used := strings.Count(probe.renderConfirmDialog(), "\n") + 1
	return max(1, min(confirmFileRowsMax, m.height-dialogVChrome-used))
}

// renderConfirmFiles draws the focused worktree's file list: rows lines, then
// a position line when the list does not fit. inner is the content width.
func (m Model) renderConfirmFiles(w confirmWorktree, inner int) string {
	if !w.loaded || w.err != "" || len(w.files) == 0 {
		return ""
	}
	rows := m.confirmFileRows()
	const indent = "        "
	budget := max(1, inner-len(indent))
	start := max(0, min(m.confirmFileScroll, max(0, len(w.files)-rows)))
	end := min(len(w.files), start+rows)
	var b strings.Builder
	for _, f := range w.files[start:end] {
		code := sanitizeRemoteText(f.Code)
		path := elideMiddle(sanitizeRemoteText(f.Path), max(1, budget-len(code)-1))
		b.WriteString("\n" + indent + dialogSubtle.Render(truncateToWidth(code+" "+path, budget)))
	}
	if len(w.files) > rows || w.filesTruncated {
		hint := fmt.Sprintf("%d–%d of %d · PgUp/PgDn", start+1, end, w.changes)
		if w.filesTruncated {
			hint = fmt.Sprintf("%d–%d of %d (first %d listed) · PgUp/PgDn", start+1, end, w.changes, len(w.files))
		}
		b.WriteString("\n" + indent + dialogSubtle.Render(truncateToWidth(hint, budget)))
	}
	return b.String()
}

// renderBranchRows draws the local branch row with its warning. A greyed row
// names why it cannot be armed. Quil never deletes a branch on a remote.
func (m Model) renderBranchRows(inner int) string {
	deletable := m.confirmDeletable()
	var b strings.Builder

	localLabel := "Delete local branch"
	switch {
	case len(deletable) == 1:
		localLabel += " " + sanitizeRemoteText(deletable[0].branch)
	case len(deletable) > 1:
		localLabel = fmt.Sprintf("Delete local branches (%d)", len(deletable))
	}

	b.WriteString("\n" + m.confirmCheckRow(confirmRowLocal, m.confirmDeleteBranch, localLabel, m.localRowReason(), inner))
	if local, _ := m.confirmBranchSelection(); local {
		unmerged, def := 0, ""
		for _, w := range deletable {
			unmerged += w.unmerged
			if def == "" {
				def = w.defaultBranch
			}
		}
		if unmerged > 0 {
			msg := fmt.Sprintf("⚠ %d commits only on this branch will be lost", unmerged)
			if def != "" && len(deletable) == 1 {
				msg = fmt.Sprintf("⚠ %d commits not on %s will be lost", unmerged, sanitizeRemoteText(def))
			}
			b.WriteString("\n      " + dialogWarn.Render(truncateToWidth(msg, inner-6)))
		}
	}
	return b.String()
}

// confirmCheckRow draws one checkbox row: the cursor mark, the box, the label,
// and — when the row cannot be armed — the reason, greyed.
func (m Model) confirmCheckRow(row int, armed bool, label, reason string, inner int) string {
	mark := "  "
	if m.confirmRow == row {
		mark = "> "
	}
	box := "[ ]"
	if armed && reason == "" {
		box = "[x]"
	}
	text := box + " " + label
	style := dialogNormal
	switch {
	case reason != "":
		// The reason can be git's own error text from the daemon: remote
		// text, sanitised like every other.
		text += " — " + sanitizeRemoteText(reason)
		style = dialogSubtle
	case armed:
		style = dialogWarn
	}
	return mark + style.Render(truncateToWidth(text, inner-len(mark)))
}

// worktreeChangesShort is the one-word state of a collapsed worktree row. The
// same four states renderWorktreeChanges keeps apart, so "could not check"
// never reads as clean here either.
func worktreeChangesShort(w confirmWorktree) string {
	switch {
	case !w.loaded:
		return "checking…"
	case w.err != "":
		return "⚠ not checked"
	case w.changes == 0:
		return "clean"
	case w.changes == 1:
		return "⚠ 1 file"
	default:
		return fmt.Sprintf("⚠ %d files", w.changes)
	}
}

// confirmWorktreesLoaded reports that every worktree row has its answer (or
// its timeout reason).
func (m Model) confirmWorktreesLoaded() bool {
	for _, w := range m.confirmWorktrees {
		if !w.loaded {
			return false
		}
	}
	return true
}

// confirmWorktreesInspected reports that every worktree's files were READ:
// loaded, and without an error. A timeout or a refusal loads a row with a
// reason, which says the files are unknown — not that there are none.
func (m Model) confirmWorktreesInspected() bool {
	for _, w := range m.confirmWorktrees {
		if !w.loaded || w.err != "" {
			return false
		}
	}
	return true
}

// worktreeRowReason is why the worktree row cannot be armed, or "". A
// worktree whose files could not be read is never deleted: the dialog cannot
// show what would be lost. Closing still works, keeping the worktree.
func (m Model) worktreeRowReason() string {
	if m.agentWTBranchEntry() {
		return ""
	}
	for _, w := range m.confirmWorktrees {
		if w.loaded && w.err != "" {
			return "kept: files not read"
		}
	}
	return ""
}

// disarmUnreadWorktree unticks the worktree row (and the branch rows that
// depend on it) once a check FAILED: a tick given while the files were
// loading is not consent to lose files nobody was shown, and a box drawn
// empty must not stay set underneath. Enter then closes and keeps it.
func (m *Model) disarmUnreadWorktree() {
	if m.worktreeRowReason() == "" {
		return
	}
	m.confirmRemoveWorktree = false
	m.confirmDeleteBranch = false
}
