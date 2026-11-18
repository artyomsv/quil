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
}

// copyTextCmd writes text to the clipboard off the Update goroutine (the
// write can shell out to a helper) and reports the outcome as textCopiedMsg.
// what names the copy's source in the log line. Empty text copies nothing.
func copyTextCmd(text, what string) tea.Cmd {
	if text == "" {
		return nil
	}
	return func() tea.Msg {
		err := clipboardWriteText(text)
		if err != nil {
			log.Printf("%s clipboard write: %v", what, err)
		}
		return textCopiedMsg{text: text, err: err}
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
// mouse just made, and marks it copied (see Selection.copied). The mark is
// written through the m.selection pointer, which the returned Model shares,
// so the value receiver does not lose it.
func (m Model) copyPaneSelectionOnSelect() tea.Cmd {
	if m.selection == nil {
		return nil
	}
	cmd := m.copyOnSelectCmd(m.paneSelectionText(), "pane")
	if cmd != nil {
		m.selection.copied = true
	}
	return cmd
}

// paneMenuAllowed reports whether a right-click with no selection opens a
// context menu: not while a dialog, a rename edit or notes mode owns input.
func (m Model) paneMenuAllowed() bool {
	return m.dialog == dialogNone && !m.notesMode && !m.renaming && !m.renamingPane
}
