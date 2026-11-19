package tui

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
)

// Every text copy (pane selection, notes editor, read-only viewer, the
// editors' Ctrl+C / Ctrl+X / Enter) goes through copyTextCmd, and its result
// comes back as textCopiedMsg, which flashes it in the status bar the way
// copy-path (pathCopiedMsg) already did. Before, a copy was silent: nothing
// said it had happened, or that the clipboard helper had failed.

// textCopiedMsg reports a text copy's clipboard write. A send result, not an
// IPC response, so its Update arm must not re-arm listenForMessages.
type textCopiedMsg struct {
	text string
	err  error
	// mark, when set, is the pane selection copy_on_select copied; Update
	// marks it copied (Selection.copied) only if the write succeeded.
	mark *selectionMark
	// seq and superseded order the result against other copies; see
	// copyTicket and Model.takeCopyResult.
	seq        uint64
	superseded bool
}

// Clipboard writes run in the order Update asked for them. Bubble Tea runs
// every Cmd on its own goroutine, so two copies made in quick succession (the
// word on a double-click, then the sentence on the third click; a selection,
// then a pane's path) could finish in either order, and a late older write
// would replace the newer text while the newer selection stayed highlighted
// and marked copied.
//
// Every clipboard write, text and path alike, takes a copyTicket when its Cmd
// is built. That happens on the Update goroutine, so ticket numbers follow
// input order. copyMu lets one write run at a time, without blocking Update:
// only the Cmd goroutines wait on it. lastCopySeq, guarded by copyMu, is the
// newest copy written or attempted; an older one that gets the lock after it
// is dropped rather than written late. Their results can still reach Update
// out of order, which Model.takeCopyResult handles. These are package state
// because the clipboard is one per process, like the counters in pane.go and
// watchdog.go.
var (
	copySeq     atomic.Uint64
	copyMu      sync.Mutex
	lastCopySeq uint64
)

// copyTicket is one clipboard write's place in input order.
type copyTicket struct{ seq uint64 }

// newCopyTicket must be called on the Update goroutine, where every copy
// site runs: the number it takes there is what keeps writes in input order.
func newCopyTicket() copyTicket { return copyTicket{seq: copySeq.Add(1)} }

// beforeCopyLock runs as a clipboard write starts waiting for its turn.
// Tests set it to know a write has reached the queue; production leaves it a
// no-op.
var beforeCopyLock = func() {}

// write runs in the Cmd. It writes text unless a newer copy has already run,
// in which case nothing is written and superseded is true.
func (k copyTicket) write(text string) (superseded bool, err error) {
	beforeCopyLock()
	copyMu.Lock()
	defer copyMu.Unlock()
	if k.seq < lastCopySeq {
		return true, nil
	}
	lastCopySeq = k.seq
	return false, clipboardWriteText(text)
}

// selectionMark names a selection and its extent at the moment its copy
// started. The extent is checked again before marking: a selection the
// keyboard changed while the write ran no longer matches the clipboard.
type selectionMark struct {
	sel            *Selection
	anchor, cursor SelectionAnchor
}

// apply marks the selection copied when it still spans what was copied, and
// reports whether it did. Called on the Update goroutine, which owns every
// Selection.
func (k *selectionMark) apply() bool {
	if k.sel.Anchor == k.anchor && k.sel.Cursor == k.cursor {
		k.sel.copied = true
		return true
	}
	return false
}

// takeCopyResult reports whether a copy result is the newest one: not
// superseded, and not older than a result Update has already applied, since
// Bubble Tea can deliver the results of two writes in either order. Anything
// else neither flashes nor marks: the clipboard holds newer text than it.
func (m *Model) takeCopyResult(seq uint64, superseded bool) bool {
	if superseded || seq < m.copyResultSeq {
		return false
	}
	m.copyResultSeq = seq
	return true
}

// clipboardChanged records a successful copy. The clipboard no longer holds
// the text of the selection marked copied before it, so that one is unmarked
// (right-click copies it again rather than dropping it); mark, if any, is
// the selection this copy holds.
func (m *Model) clipboardChanged(mark *selectionMark) {
	if m.copiedSel != nil {
		m.copiedSel.copied = false
		m.copiedSel = nil
	}
	if mark != nil && mark.apply() {
		m.copiedSel = mark.sel
	}
}

// copyTextCmd writes text to the clipboard off the Update goroutine (the
// write can shell out to a helper) and reports the outcome as textCopiedMsg.
// what names the copy's source in the log line. Empty text copies nothing.
func copyTextCmd(text, what string) tea.Cmd {
	return markingCopyCmd(text, what, nil)
}

// markingCopyCmd is copyTextCmd carrying mark back to Update with the result.
// It must be called on the Update goroutine (see newCopyTicket).
func markingCopyCmd(text, what string, mark *selectionMark) tea.Cmd {
	if text == "" {
		return nil
	}
	ticket := newCopyTicket()
	return func() tea.Msg {
		superseded, err := ticket.write(text)
		if err != nil {
			log.Printf("%s clipboard write: %v", what, err)
		}
		return textCopiedMsg{text: text, err: err, mark: mark, seq: ticket.seq, superseded: superseded}
	}
}

// copiedFlash is the status-bar text for a successful copy: the line count
// for a multi-line copy, else the character count, so a short drag that
// caught less than intended shows it. A single trailing newline is not
// counted, so "one line\n" reads as one line of 8 characters.
func copiedFlash(text string) string {
	text = strings.TrimSuffix(text, "\n")
	if n := strings.Count(text, "\n") + 1; n > 1 {
		return fmt.Sprintf("Copied %d lines", n)
	}
	if n := utf8.RuneCountInString(text); n != 1 {
		return fmt.Sprintf("Copied %d characters", n)
	}
	return "Copied 1 character"
}

// paneSelectionText returns the selected text of the pane the selection is
// in, or "". That is the selection's own pane, not the focused one: a drag
// in a pane that does not have focus selects there without moving focus.
func (m Model) paneSelectionText() string {
	if m.selection == nil {
		return ""
	}
	pane := m.activePaneByID(m.selection.PaneID)
	if pane == nil {
		return ""
	}
	return copyText(pane, m.selection, m.paneIsAgent(pane))
}

// copyOnSelectCmd copies a just-made mouse selection when copy_on_select is
// on, and is nil otherwise. The selection stays on screen: the copy is a
// side effect of making it, not an action that consumes it.
func (m Model) copyOnSelectCmd(text, what string) tea.Cmd {
	if !m.cfg.UI.CopyOnSelect {
		return nil
	}
	return copyTextCmd(text, what)
}

// copyPaneSelectionOnSelect is copyOnSelectCmd for the pane selection the
// mouse just made. The selection is marked copied (see Selection.copied)
// only when the write reports success, in Update: marking it here, before
// the write ran, let a failed copy make right-click drop a selection that
// never reached the clipboard.
func (m Model) copyPaneSelectionOnSelect() tea.Cmd {
	if m.selection == nil || !m.cfg.UI.CopyOnSelect {
		return nil
	}
	s := m.selection
	return markingCopyCmd(m.paneSelectionText(), "pane",
		&selectionMark{sel: s, anchor: s.Anchor, cursor: s.Cursor})
}

// paneMenuAllowed reports whether a right-click with no selection opens a
// context menu: not while a dialog, a rename edit or notes mode owns input.
func (m Model) paneMenuAllowed() bool {
	return m.dialog == dialogNone && !m.notesMode && !m.renaming && !m.renamingPane
}
