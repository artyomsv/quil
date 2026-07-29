package tui

import (
	"log"
	"time"

	tea "charm.land/bubbletea/v2"
)

// linkLostMsg reports that the connection to the daemon died.
//
// It carries the client generation the reporting listen loop was reading.
// Model is a value type, so every tea.Cmd closure holds its own copy of the
// client it was created with: a loop from a superseded connection can still be
// parked in Receive when its replacement is already live, and will report that
// connection's death long after it stopped mattering. Comparing the stamped
// generation against the current one is what makes such a report discardable
// instead of a second reconnect.
type linkLostMsg struct {
	gen int
	err error
}

// reconnectState tracks an in-progress reconnect. The zero value means "not
// reconnecting", matching the ctxMenu/palette convention in this package: there
// is no separate open bool that could drift out of sync with the rest.
type reconnectState struct {
	active  bool
	attempt int       // 1-based; drives the backoff
	lastErr error     // shown in the banner
	nextAt  time.Time // when the next attempt fires, for the countdown
}

// RedialFunc dials a replacement connection after a drop.
//
// The dead client is passed in so the caller can close it: Client is
// deliberately just Send/Receive, so the TUI has no way to release the
// underlying ssh child itself, and cmd/quil is the only layer that knows the
// value is really an *ipc.Client.
type RedialFunc func(old Client) (Client, error)

// SetRedialFunc installs the reconnect dialer. Called by cmd/quil in remote
// mode only; a nil func disables reconnect, which is what local sessions get.
//
// A setter rather than a NewModel parameter for the same reason SetRemoteDest
// is one: NewModel's signature is already at five arguments.
func (m *Model) SetRedialFunc(f RedialFunc) { m.redialFn = f }

// canReconnect reports whether a dropped link should be retried rather than
// being fatal.
//
// Local sessions never reconnect: a dead local daemon means the panes died with
// it, so retrying would spin against something that is not coming back while
// hiding the loss. Remote mode without a dialer is equally fatal — there is
// nothing to retry with.
func (m Model) canReconnect() bool {
	return m.RemoteMode() && m.redialFn != nil
}

// beginReconnect enters the reconnecting state.
//
// The redial loop itself arrives in RD-011; this function is separate so the
// entry condition can be tested without a dialer in the tree.
func (m Model) beginReconnect(cause error) (tea.Model, tea.Cmd) {
	if m.reconnect.active {
		return m, nil // already reconnecting; one loop only
	}
	log.Printf("remote link lost, reconnecting: %v", cause)
	m.reconnect = reconnectState{active: true, lastErr: cause}
	// A drag in flight refers to coordinates and panes that the post-reattach
	// state may not have; drop it rather than resolve it later.
	m.clearDragState()
	return m, nil
}
