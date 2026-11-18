package tui

import (
	"fmt"
	"log"
	"strings"
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
}

// selectionMark names a selection and its extent at the moment its copy
// started. The extent is checked again before marking: a selection the
// keyboard changed while the write ran no longer matches the clipboard.
type selectionMark struct {
	sel            *Selection
	anchor, cursor SelectionAnchor
}

// apply marks the selection copied when it still spans what was copied.
// Called on the Update goroutine, which owns every Selection.
func (k *selectionMark) apply() {
	if k.sel.Anchor == k.anchor && k.sel.Cursor == k.cursor {
		k.sel.copied = true
	}
}

// copyTextCmd writes text to the clipboard off the Update goroutine (the
// write can shell out to a helper) and reports the outcome as textCopiedMsg.
// what names the copy's source in the log line. Empty text copies nothing.
func copyTextCmd(text, what string) tea.Cmd {
	return markingCopyCmd(text, what, nil)
}

// markingCopyCmd is copyTextCmd carrying mark back to Update with the result.
func markingCopyCmd(text, what string, mark *selectionMark) tea.Cmd {
	if text == "" {
		return nil
	}
	return func() tea.Msg {
		err := clipboardWriteText(text)
		if err != nil {
			log.Printf("%s clipboard write: %v", what, err)
		}
		return textCopiedMsg{text: text, err: err, mark: mark}
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
