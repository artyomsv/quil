package daemon

import apty "github.com/artyomsv/quil/internal/pty"

type terminalSize struct{ cols, rows int }

// newPaneSession gives a new child useful dimensions before its first paint.
// MCP can create panes in hidden tabs, so waiting for a TUI resize lets a child
// build its first screen for the default 80x24 terminal.
func (d *Daemon) newPaneSession(pane *Pane) apty.Session {
	cols, rows := 80, 24
	// Older or console-less clients can attach at 1x1. Do not inherit that
	// unusable geometry and permanently reflow the child's first screen.
	if size := d.clientSize.Load(); size != nil && !degenerateSize(size.cols, size.rows) {
		cols, rows = size.cols, size.rows
	}
	for _, sibling := range d.session.Panes(pane.TabID) {
		if sibling.ID == pane.ID {
			continue
		}
		sibling.PluginMu.Lock()
		c, r, overlay := sibling.Cols, sibling.Rows, sibling.Overlay
		sibling.PluginMu.Unlock()
		// Apply the same guard to sibling dimensions as to the attached client.
		// A stored 1x1 size must not override a usable fallback.
		if c > 0 && r > 0 && !degenerateSize(c, r) && !overlay {
			cols, rows = c, r
			break
		}
	}
	pane.PluginMu.Lock()
	pane.Cols, pane.Rows = cols, rows
	pane.PluginMu.Unlock()
	return newSessionFn(cols, rows)
}
