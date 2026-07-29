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

const (
	// reconnectBaseDelay is the nominal wait before the first attempt. Short
	// enough that a brief blip heals before the user reaches for the keyboard.
	reconnectBaseDelay = 500 * time.Millisecond
	// reconnectMaxDelay caps the backoff. Budgeted for Windows, where OpenSSH
	// has no ControlMaster and every attempt pays a full TCP and auth handshake.
	reconnectMaxDelay = 30 * time.Second
)

// reconnectDelay returns how long to wait before attempt n (1-based).
//
// Exponential from reconnectBaseDelay, capped at reconnectMaxDelay, then scaled
// by jitter into [50%, 100%] of that value — so attempt 1 is 250-500ms, not a
// flat 500ms. Jitter is a parameter rather than an internal rand call so the
// curve is deterministic under test; callers pass rand.Float64().
//
// Full jitter (scaling into [0, delay]) was not used: it puts real weight near
// zero, and a near-instant retry against a host that is still down is a hot
// loop with extra steps. Half jitter keeps the herd spread without that.
func reconnectDelay(attempt int, jitter float64) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := reconnectBaseDelay << (attempt - 1)
	// The d <= 0 half is load-bearing, and not for the reason it looks like.
	// This is a runtime shift on an int64, so it wraps rather than saturates:
	// from attempt 36 the product exceeds int64 and can come back NEGATIVE,
	// which "d > reconnectMaxDelay" does not catch — it would flow through to
	// tea.Tick, fire immediately, and become the hot loop the cap exists to
	// prevent. Exact zero only arrives at attempt 57, once the shift count
	// reaches 64. Both cases are covered here.
	// TestReconnectDelay_NeverDropsBelowFloorAtAnyAttempt pins this.
	if d > reconnectMaxDelay || d <= 0 {
		d = reconnectMaxDelay
	}
	if jitter < 0 {
		jitter = 0
	}
	if jitter > 1 {
		jitter = 1
	}
	return time.Duration(float64(d) * (0.5 + 0.5*jitter))
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
