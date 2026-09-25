package daemon

import apty "github.com/artyomsv/quil/internal/pty"

type terminalSize struct{ cols, rows int }

// newPaneSession gives a new child useful dimensions before its first paint.
// MCP can create panes in hidden tabs, so waiting for a TUI resize lets a child
// build its first screen for the default 80x24 terminal.
func (d *Daemon) newPaneSession(pane *Pane) apty.Session {
	cols, rows := d.initialPaneSize()
	for _, sibling := range d.session.Panes(pane.CurrentTabID()) {
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
	// A new announcement (see Pane.sizeSeq): the spawn size reaches followers
	// only through the broadcast, and a restart reuses the Pane, so it must
	// outrank every size announced for the previous child.
	pane.sizeSeq++
	pane.colsSeq = pane.sizeSeq
	pane.PluginMu.Unlock()
	return newSessionFn(cols, rows)
}

// initialPaneSize is the size a new pane starts at when no sibling in its tab
// has one. It is the size master's window, because the master is the client
// that will size the pane next; the last client to attach may be a follower
// whose window says nothing about the PTYs. With no master it falls back to
// the stored attach size, and then to 80x24.
func (d *Daemon) initialPaneSize() (cols, rows int) {
	if c := d.masterConn(); c != nil {
		// The RAW geometry. A master is paintable by construction, but the
		// record can change between the two lookups, so it is checked again.
		if rec, ok := d.clientByConn(c); ok && eligible(&rec) {
			return rec.cols, rec.rows
		}
	}
	// Older or console-less clients can attach at 1x1. Do not inherit that
	// unusable geometry and permanently reflow the child's first screen.
	if size := d.clientSize.Load(); size != nil && !degenerateSize(size.cols, size.rows) {
		return size.cols, size.rows
	}
	return 80, 24
}
