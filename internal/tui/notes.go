package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/artyomsv/aethel/internal/persist"
)

// notesDebounceWindow is how long the notes editor waits after the last edit
// before auto-saving (in addition to auto-save on exit and explicit Ctrl+S).
const notesDebounceWindow = 30 * time.Second

// notesTickInterval is how often the model polls the notes editor to check
// whether the debounce window has elapsed.
const notesTickInterval = 5 * time.Second

// notesTickMsg triggers periodic debounce checks while notes mode is active.
type notesTickMsg struct{}

// notesAction tells the surrounding model what to do after the editor has
// processed a key. Currently the only "outside the editor" action is to
// exit notes mode entirely (e.g., the user pressed esc).
type notesAction int

const (
	notesActionNone notesAction = iota
	notesActionExit
)

// NotesEditor is a plain-text notes editor bound to a pane. It wraps the
// generic TextEditor and provides its own save path (bypassing TextEditor's
// TOML validation) and a debounced auto-save policy.
type NotesEditor struct {
	editor      *TextEditor
	notesDir    string
	paneID      string
	paneName    string
	dirty       bool
	lastEditAt  time.Time
	lastSavedAt time.Time
	saveErr     string
}

// NewNotesEditor loads the notes file for paneID (creating the editor even
// when the file does not yet exist) and returns an editor positioned at the
// top of the document.
func NewNotesEditor(notesDir, paneID, paneName string, viewW, viewH int) (*NotesEditor, error) {
	if paneID == "" {
		return nil, fmt.Errorf("pane ID is required")
	}
	content, err := persist.LoadNotes(notesDir, paneID)
	if err != nil {
		return nil, fmt.Errorf("load notes: %w", err)
	}
	ed := NewTextEditor(content, "", viewW, viewH)
	ed.Highlight = HighlightPlain
	return &NotesEditor{
		editor:   ed,
		notesDir: notesDir,
		paneID:   paneID,
		paneName: paneName,
	}, nil
}

// Resize updates the editor's viewport dimensions.
func (n *NotesEditor) Resize(w, h int) {
	if n == nil {
		return
	}
	n.editor.ViewWidth = w
	n.editor.ViewHeight = h
	n.editor.ensureCursorVisible()
}

// Dirty reports whether there are unsaved edits.
func (n *NotesEditor) Dirty() bool {
	if n == nil {
		return false
	}
	return n.dirty
}

// PaneID returns the pane this editor is bound to.
func (n *NotesEditor) PaneID() string {
	if n == nil {
		return ""
	}
	return n.paneID
}

// Content returns the current editor buffer as a single string.
func (n *NotesEditor) Content() string {
	if n == nil {
		return ""
	}
	return n.editor.Content()
}

// Save writes the current content to disk and clears the dirty flag on both
// the wrapper and the inner TextEditor. Safe to call when not dirty — it is
// a no-op in that case. Ensures the saved file ends with a newline so it
// behaves like a normal POSIX text file.
func (n *NotesEditor) Save() error {
	if n == nil || !n.dirty {
		return nil
	}
	content := n.editor.Content()
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if err := persist.SaveNotes(n.notesDir, n.paneID, content); err != nil {
		n.saveErr = err.Error()
		return err
	}
	n.dirty = false
	// Keep the wrapped editor's dirty flag in sync. Without this, every
	// non-mutating key (cursor moves) re-marks the wrapper dirty because the
	// inner editor still reports Dirty == true, leading to a save loop and
	// a flickering "[notes*]" status indicator after every save.
	n.editor.Dirty = false
	n.lastSavedAt = time.Now()
	n.saveErr = ""
	return nil
}

// HandleKey processes a key press. Returns:
//   - action: what the outer model should do (`notesActionNone` to keep
//     editing, `notesActionExit` to leave notes mode)
//   - cmd: an optional tea command (e.g., for async paste)
//
// ctrl+s and esc are intercepted before being passed to the TextEditor so the
// editor's TOML-specific Save() and its close-on-esc behaviour do not fire.
func (n *NotesEditor) HandleKey(key string) (notesAction, tea.Cmd) {
	if n == nil {
		return notesActionNone, nil
	}

	switch key {
	case "ctrl+s":
		_ = n.Save() // error is captured in n.saveErr and rendered in the footer
		return notesActionNone, nil
	case "esc":
		// Clear active selection first, exit notes mode only on a second press.
		if n.editor.Sel != nil {
			n.editor.Sel = nil
			return notesActionNone, nil
		}
		return notesActionExit, nil
	}

	_, _, cmd := n.editor.HandleKey(key)
	if n.editor.Dirty {
		n.dirty = true
		n.lastEditAt = time.Now()
	}
	return notesActionNone, cmd
}

// HandlePaste applies pasted content at the cursor position.
func (n *NotesEditor) HandlePaste(text string) {
	if n == nil || text == "" {
		return
	}
	n.editor.InsertMultiLine(text)
	n.dirty = true
	n.lastEditAt = time.Now()
}

// MaybeAutoSave saves when the debounce window has elapsed since the last edit.
// No-op if the editor is clean or the user is still actively editing.
func (n *NotesEditor) MaybeAutoSave() {
	if n == nil || !n.dirty {
		return
	}
	if time.Since(n.lastEditAt) < notesDebounceWindow {
		return
	}
	_ = n.Save() // failures are recorded in n.saveErr and shown in the footer
}

// Close flushes pending unsaved changes to disk and returns any save error.
func (n *NotesEditor) Close() error {
	if n == nil {
		return nil
	}
	return n.Save()
}

// SaveErr returns the most recent save error, if any.
func (n *NotesEditor) SaveErr() string {
	if n == nil {
		return ""
	}
	return n.saveErr
}

// View renders the notes editor inside a bordered box of the given size.
// The box includes a header with the pane name + dirty indicator and a
// footer with quick-reference hints.
func (n *NotesEditor) View(width, height int) string {
	if n == nil || width < 8 || height < 5 {
		return ""
	}

	// Reserve 2 cols / 4 rows for border + header/footer
	innerW := width - 2
	innerH := height - 4
	if innerW < 4 || innerH < 1 {
		return ""
	}
	n.Resize(innerW, innerH)

	header := n.headerLine(innerW)
	body := n.editor.Render()
	footer := n.footerLine(innerW)

	content := header + "\n" + body
	// The editor render may already end with a newline; ensure footer is on its own line.
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += footer

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("63")).
		Width(width).
		Height(height).
		Render(content)
}

func (n *NotesEditor) headerLine(width int) string {
	title := n.paneName
	if title == "" {
		title = n.paneID
	}
	dirtyMark := ""
	if n.dirty {
		dirtyMark = " *"
	}
	raw := " notes: " + title + " " + dirtyMark
	if lipgloss.Width(raw) > width {
		raw = truncateRunes(raw, width)
	}
	return lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("230")).
		Render(raw)
}

func (n *NotesEditor) footerLine(width int) string {
	hint := "Ctrl+S save  Esc exit  Alt+E toggle"
	if n.saveErr != "" {
		hint = "save err: " + n.saveErr
	} else if !n.lastSavedAt.IsZero() {
		hint = fmt.Sprintf("saved %s ago  Ctrl+S  Esc  Alt+E", relativeTime(n.lastSavedAt))
	}
	hint = truncateRunes(hint, width)
	return lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Render(hint)
}
