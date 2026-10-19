package tui

import (
	"log"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/textsafe"
)

// paneCopyPath picks what the context menu's copy row copies, and its label.
//
// WorktreePath first: it is the directory git created for this pane, fixed at
// creation, so it names the checkout even after the shell has cd'd deeper
// into it (CWD follows every cd via OSC 7). A pane with no Quil-made worktree
// gets its current directory instead — still the folder the user would
// otherwise walk to by hand.
//
// A path carrying a control character is refused, not cleaned: the daemon may
// be on a host the user does not control, a pasted newline runs the line in
// a shell, and a cleaned path is a different path.
func paneCopyPath(pane *PaneModel) (label, path string) {
	label, path = "Copy directory path", pane.CWD
	if pane.WorktreePath != "" {
		label, path = "Copy worktree path", pane.WorktreePath
	}
	if textsafe.HasStripped(path) {
		path = ""
	}
	return label, path
}

// pathCopiedMsg reports a copy-path clipboard write. A send result, not an
// IPC response, so its Update arm must not re-arm listenForMessages.
type pathCopiedMsg struct {
	path string
	err  error
}

// copyPanePath writes the pane's path (see paneCopyPath) to the clipboard.
// The path is read here, on the Update goroutine; only the write runs in the
// Cmd, since it can shell out (pbcopy, xclip).
func (m Model) copyPanePath(pane *PaneModel) tea.Cmd {
	_, path := paneCopyPath(pane)
	if path == "" {
		return nil
	}
	return func() tea.Msg {
		err := clipboardWriteText(path)
		if err != nil {
			log.Printf("copy path: clipboard write: %v", err)
		}
		return pathCopiedMsg{path: path, err: err}
	}
}
