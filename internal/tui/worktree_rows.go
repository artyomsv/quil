package tui

import (
	"fmt"
	"strings"
)

// The close dialog's worktree section: the worktree toggle, the files the
// removal destroys, and the two branch rows (local, remote). Kept apart from
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
	confirmRowRemote   = 2
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

// remoteRowReason is "" when the remote-branch row can be armed, else why not.
// The F1 switch comes first: while it is off the row says so and nothing else.
func (m Model) remoteRowReason() string {
	if !m.cfg.Worktree.RemoteBranchDelete {
		return "off in F1 → Settings"
	}
	if r := m.localRowReason(); r != "" {
		return r
	}
	if dest, ok := m.confirmDest(); ok && !m.destCanAdmin(dest) {
		return "needs a full token"
	}
	for _, w := range m.confirmDeletable() {
		if w.remote != "" {
			return ""
		}
	}
	for _, w := range m.confirmDeletable() {
		if w.remoteReason != "" {
			return w.remoteReason
		}
	}
	return "no remote branch"
}

// confirmDest is the destination of the pane or tab the confirm is about.
func (m Model) confirmDest() (string, bool) {
	switch m.confirmKind {
	case "pane":
		return m.destOfPane(m.confirmID), true
	case "tab":
		return m.destOfTab(m.confirmID), true
	case confirmKindAgentWT:
		return m.agentWT.dest, true
	}
	return "", false
}

// confirmBranchSelection is what an accept sends: the two flags as they take
// effect (an armed row that is no longer enabled counts as off) and the
// branch names on screen, which the daemon only CHECKS against what it reads.
func (m Model) confirmBranchSelection() (local, remote bool, confirmed []string) {
	local = m.confirmDeleteBranch && m.localRowReason() == ""
	remote = m.confirmDeleteRemote && m.remoteRowReason() == ""
	if !local && !remote {
		return false, false, nil
	}
	seen := map[string]bool{}
	for _, w := range m.confirmDeletable() {
		if !seen[w.branch] {
			seen[w.branch] = true
			confirmed = append(confirmed, w.branch)
		}
	}
	return local, remote, confirmed
}

// toggleConfirmRow flips the row under the cursor. A greyed row does nothing.
// Disarming the worktree row disarms the branch rows too: a branch cannot be
// deleted while its worktree still has it checked out.
func (m *Model) toggleConfirmRow() {
	switch m.confirmRow {
	case confirmRowWorktree:
		if m.agentWTBranchEntry() {
			return // a branch entry has no worktree row to toggle
		}
		m.confirmRemoveWorktree = !m.confirmRemoveWorktree
		if !m.confirmRemoveWorktree {
			m.confirmDeleteBranch, m.confirmDeleteRemote = false, false
		}
	case confirmRowLocal:
		if m.localRowReason() == "" {
			m.confirmDeleteBranch = !m.confirmDeleteBranch
		}
	case confirmRowRemote:
		if m.remoteRowReason() == "" {
			m.confirmDeleteRemote = !m.confirmDeleteRemote
		}
	}
}

// moveConfirmRow moves the cursor between the three rows.
func (m *Model) moveConfirmRow(delta int) {
	lowest := confirmRowWorktree
	if m.agentWTBranchEntry() {
		lowest = confirmRowLocal
	}
	m.confirmRow = max(lowest, min(confirmRowRemote, m.confirmRow+delta))
}

// focusedWorktree is the worktree whose files are listed.
func (m Model) focusedWorktree() (confirmWorktree, bool) {
	if len(m.confirmWorktrees) == 0 {
		return confirmWorktree{}, false
	}
	i := max(0, min(m.confirmFileFocus, min(len(m.confirmWorktrees), confirmWorktreeMaxRows)-1))
	return m.confirmWorktrees[i], true
}

// moveConfirmFocus moves the file list to the previous or next worktree.
func (m *Model) moveConfirmFocus(delta int) {
	n := min(len(m.confirmWorktrees), confirmWorktreeMaxRows)
	if n == 0 {
		return
	}
	m.confirmFileFocus = (m.confirmFileFocus + delta + n) % n
	m.confirmFileScroll = 0
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

// renderBranchRows draws the local and remote branch rows with their
// warnings. Each greyed row names why it cannot be armed.
func (m Model) renderBranchRows(inner int) string {
	deletable := m.confirmDeletable()
	var b strings.Builder

	localLabel := "Delete local branch"
	remoteLabel := "Delete remote branch"
	switch {
	case len(deletable) == 1:
		localLabel += " " + sanitizeRemoteText(deletable[0].branch)
		if r := deletable[0]; r.remote != "" {
			remoteLabel += " " + sanitizeRemoteText(r.remote+"/"+r.remoteBranch)
		}
	case len(deletable) > 1:
		localLabel = fmt.Sprintf("Delete local branches (%d)", len(deletable))
		remoteLabel = fmt.Sprintf("Delete remote branches (%d)", len(deletable))
	}

	b.WriteString("\n" + m.confirmCheckRow(confirmRowLocal, m.confirmDeleteBranch, localLabel, m.localRowReason(), inner))
	local, remote, _ := m.confirmBranchSelection()
	if local {
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

	b.WriteString("\n" + m.confirmCheckRow(confirmRowRemote, m.confirmDeleteRemote, remoteLabel, m.remoteRowReason(), inner))
	if remote {
		b.WriteString("\n      " + dialogWarn.Render(truncateToWidth("⚠ deletes it on the remote, for everyone", inner-6)))
		for _, w := range deletable {
			if w.prOpen && w.remote != "" {
				msg := fmt.Sprintf("⚠ PR #%d is open — deleting the branch closes it", w.prNumber)
				b.WriteString("\n      " + dialogWarn.Render(truncateToWidth(msg, inner-6)))
				break
			}
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
		text += " — " + reason
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

// remoteDeleteArmed reports that an accept would delete a remote branch —
// the one choice Enter alone must not confirm.
func (m Model) remoteDeleteArmed() bool {
	_, remote, _ := m.confirmBranchSelection()
	return remote
}
