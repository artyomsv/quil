package tui

import (
	"fmt"
	"log"
	"math/rand"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
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

// freezeInput drops user input while a reconnect is in flight, reporting
// whether it consumed the message.
//
// Dropped rather than buffered, deliberately. Buffered keystrokes would be
// delivered into a live agent session minutes later, at a prompt that has moved
// on — a paste or a stray "y" landing on the wrong question is worse than a
// visible stall. This is a fail-closed choice.
//
// One choke point rather than a guard in each of the six input branches: a
// future input message type gets frozen by default here, whereas six scattered
// guards would silently let it through. Same reasoning as clearDragState.
//
// Ctrl+Q is the single exception. It is the only way out of a host that never
// comes back, and by definition the reconnect loop cannot end the session
// itself — it retries forever.
func (m Model) freezeInput(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if kbMatches(msg.String(), m.cfg.Keybindings.Quit) {
			return tea.Quit, true
		}
		return nil, true
	case tea.MouseClickMsg, tea.MouseWheelMsg, tea.MouseMotionMsg, tea.MouseReleaseMsg, tea.PasteMsg:
		// A wheel notch is forwarded to the PTY on tracking panes, so it is
		// input by another name and belongs in the freeze with the rest.
		return nil, true
	}
	return nil, false
}

// firstErrLine reduces a multi-line diagnostic to its first line.
//
// ssh errors routinely span several lines. The banner is a one-row overlay, so
// a raw multi-line message would paint over the rows beneath it rather than
// growing the box. Named to avoid colliding with the test-only firstLine.
func firstErrLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

// bannerSep separates the status core from the ssh diagnostic.
const bannerSep = " · "

// minBannerDetail is the narrowest diagnostic worth showing. Below this a
// truncated error is noise ("ss…") occupying space the core could use.
const minBannerDetail = 14

// bannerCandidates builds the banner's degradation ladder for the current phase,
// longest first. Every rung keeps ctrl+q — see renderReconnectBanner.
//
// There are two phases and they must not read the same. nextAt is in the future
// while the backoff waits, and in the past once the tick has fired and a dial is
// in flight — which against a host that is down lasts as long as the transport's
// ConnectTimeout, 15 s. Labelling both "Reconnecting" leaves no way to tell a
// wedged TUI from one patiently waiting, and the wording carries the state
// instead: the host is named as unreachable, with a countdown to the next try.
//
// The 1 s window poll (sizePollTick) already re-renders while the link is down,
// so the countdown advances without a ticker of its own.
//
// Composed per phase rather than as a shared prefix plus host, because the host
// sits in a different place in each — "Host unreachable … to gpu01" is not a
// sentence.
func (m Model) bannerCandidates() []string {
	host, attempt := m.remoteDest, m.reconnect.attempt
	if remain := time.Until(m.reconnect.nextAt); remain > 0 {
		// +1 so a sub-second remainder reads as "1s" rather than "0s".
		secs := int(remain.Seconds()) + 1
		return []string{
			fmt.Sprintf("%s unreachable — retry in %ds (attempt %d)%sctrl+q quits",
				host, secs, attempt, bannerSep),
			fmt.Sprintf("%s unreachable — retry in %ds%sctrl+q", host, secs, bannerSep),
			fmt.Sprintf("Unreachable — retry in %ds%sctrl+q", secs, bannerSep),
		}
	}
	return []string{
		fmt.Sprintf("Connecting to %s (attempt %d)%sctrl+q quits", host, attempt, bannerSep),
		fmt.Sprintf("Connecting to %s%sctrl+q", host, bannerSep),
		"Connecting" + bannerSep + "ctrl+q",
	}
}

// renderReconnectBanner draws the reconnect status as a single row.
//
// Drawn by View as a compositor overlay, so it reserves no layout height and
// cannot resize a pane — the same reason the notification sidebar is one.
//
// Two rules drive the layout, and both were arrived at by looking at the
// rendered output rather than by satisfying an assertion:
//
//   - The exit hint survives to the narrowest width. The core degrades through
//     a ladder of progressively shorter forms, and every rung keeps "ctrl+q".
//     Truncating one long string instead cut it to "ctr…" at 40 columns, which
//     is the width the TUI's own minimum allows — so it was reachable, not
//     theoretical, and it strands the user on a host that never returns.
//   - The diagnostic is TRUNCATED to the space left, never dropped for not
//     fitting whole. A real ssh error runs past 50 characters, so an
//     all-or-nothing fit check hid it at every width below ~110 — including 80.
//     Capturing ssh's own words in batch mode is pointless if they never render.
func (m Model) renderReconnectBanner(width int) string {
	if !m.reconnect.active || width <= 0 {
		return ""
	}

	// Longest first; the first one that fits wins. Every rung keeps ctrl+q.
	candidates := m.bannerCandidates()
	core := candidates[len(candidates)-1]
	for _, c := range candidates {
		if lipgloss.Width(c) <= width {
			core = c
			break
		}
	}

	if m.reconnect.lastErr != nil {
		if detail := firstErrLine(m.reconnect.lastErr.Error()); detail != "" {
			room := width - lipgloss.Width(core) - lipgloss.Width(bannerSep)
			if room >= minBannerDetail {
				core += bannerSep + truncateToWidth(detail, room)
			}
		}
	}
	return reconnectBannerStyle.Width(width).Render(truncateToWidth(core, width))
}

// resetForReattach clears everything the daemon is about to replay into this
// pane.
//
// handleAttach replays the whole output buffer as ghost chunks on EVERY attach,
// and handlePaneOutput appends unconditionally, so a reconnect without this
// doubles the pane's scrollback — and the one after that triples it.
//
// Terminal panes are NOT exempt. The rule that terminal panes skip ResetVT
// protects RESTORED content against a respawned shell's init output; here
// nothing respawns and the content is about to arrive again, so applying that
// rule would be the bug rather than the safeguard.
//
// ResetVT already clears rawBuf and the mirrored mouse-mode flags, so this only
// adds the scroll position and the ghost/live latch. The daemon re-broadcasts
// its own MouseModes on reattach, so dropping the local mirror loses nothing.
func (p *PaneModel) resetForReattach() {
	p.ResetVT()
	p.ResetScroll()
	// Let the ghost→live transition and its settle repaints fire again; the
	// replay that follows is ghost output, exactly as on a first attach.
	p.liveOutputSeen = false
}

// eachClientPane calls fn for every pane this client holds.
//
// Every tab, not just the active one — a single attach replays background tabs
// too — plus each tab's overlay pane, which is a live daemon pane replayed like
// any other but deliberately kept OUTSIDE the layout tree, so a Leaves()-only
// walk misses it.
//
// Shared by both reattach resets so the two enumerations cannot drift: a pane
// class added to one and forgotten in the other is precisely the bug that would
// survive review, because each reset looks complete on its own.
func (m *Model) eachClientPane(fn func(*PaneModel)) {
	for _, tab := range m.tabs {
		if tab == nil {
			continue
		}
		for _, p := range tab.Leaves() {
			if p != nil {
				fn(p)
			}
		}
		if tab.overlayPane != nil {
			fn(tab.overlayPane)
		}
	}
}

// resetPanesForReattach resets every pane that the coming attach will replay.
func (m *Model) resetPanesForReattach() {
	m.eachClientPane((*PaneModel).resetForReattach)
	// Selection is Model-level, and anchors to row/column coordinates inside
	// content that has just been discarded. Keeping it would highlight whatever
	// happens to land in those cells after replay.
	m.selection = nil
}

// resetWorkStateForReattach zeroes in-flight execution state on every pane.
//
// applyWorkTransition has no dedup, so replayed SubagentStart events would
// re-increment counters that already reflect them and wedge the spinner until
// SessionEnd. Filtering the replay by seen event id was the alternative;
// zeroing wins because work state is already documented as non-persistent —
// panes start idle after a daemon restart and the next hook event corrects
// them — so this is the existing contract rather than a new compromise.
//
// Two things are deliberately preserved:
//
//   - unseen and pinnedAttention. Both are user-facing marks about work the
//     user has not looked at, not in-flight execution. unseen is the only
//     signal that a background pane finished something while the link was
//     down, which is often exactly why the user is reconnecting.
//   - m.workTickRunning. The spinner loop is self-stopping: the tick already
//     in flight observes !anyPaneWorking() and clears the flag itself.
//     Clearing it here while that tick is still scheduled would let the next
//     hook event start a SECOND loop beside it, and the spinner would animate
//     at double rate for the rest of the session.
func (m *Model) resetWorkStateForReattach() {
	m.eachClientPane(func(p *PaneModel) {
		p.working = false
		p.turnActive = false
		p.subagents = 0
	})
}

// redialTickMsg fires when the backoff for one attempt has elapsed.
type redialTickMsg struct {
	gen     int
	attempt int
}

// redialResultMsg carries one attempt's outcome.
type redialResultMsg struct {
	gen    int
	client Client
	err    error
}

// beginReconnect enters the reconnecting state and arms the first attempt.
func (m Model) beginReconnect(cause error) (tea.Model, tea.Cmd) {
	if m.reconnect.active {
		return m, nil // already reconnecting; one loop only
	}
	log.Printf("remote link lost, reconnecting: %v", cause)
	m.reconnect = reconnectState{active: true, lastErr: cause}
	// A drag in flight refers to coordinates and panes that the post-reattach
	// state may not have; drop it rather than resolve it later.
	m.clearDragState()
	return m.scheduleRedial()
}

// scheduleRedial arms the next attempt's timer.
//
// Every attempt is logged. Only the first drop and the eventual success used to
// be, which is exactly backwards: a reconnect that succeeds needs no diagnosis,
// while one that never does left the log silent after a single "link lost" line.
// The failure cause is logged with it, since that is what the user is looking at
// in the banner and what they will quote when reporting it.
func (m Model) scheduleRedial() (tea.Model, tea.Cmd) {
	m.reconnect.attempt++
	delay := reconnectDelay(m.reconnect.attempt, rand.Float64())
	m.reconnect.nextAt = time.Now().Add(delay)
	log.Printf("remote: reconnect attempt %d in %v (last error: %v)",
		m.reconnect.attempt, delay.Round(time.Millisecond), m.reconnect.lastErr)
	gen, attempt := m.clientGen, m.reconnect.attempt
	return m, tea.Tick(delay, func(time.Time) tea.Msg {
		return redialTickMsg{gen: gen, attempt: attempt}
	})
}

// redialCmd performs one dial off the Update goroutine.
//
// The dead client is handed to the dialer rather than closed here: Client is
// only Send/Receive, so this package cannot release the underlying ssh child,
// and cmd/quil is the layer that knows the value is really an *ipc.Client.
func (m Model) redialCmd() tea.Cmd {
	gen, dial, old := m.clientGen, m.redialFn, m.client
	return func() tea.Msg {
		c, err := dial(old)
		return redialResultMsg{gen: gen, client: c, err: err}
	}
}

// finishReconnect swaps in the new client and re-attaches.
//
// The generation bump is what retires every closure still holding the dead
// client: their linkLostMsg and redialResultMsg all carry the old number and
// are dropped on arrival. It must happen BEFORE listenForMessages is built,
// since that closure stamps its reports with whatever it captures.
//
// m.attached is deliberately NOT cleared. It gates the first-WindowSizeMsg
// attach path, so clearing it would make the next resize attach a SECOND time —
// and the daemon replays the whole output buffer on every attach, so that is a
// doubled scrollback rather than a redundant no-op.
func (m Model) finishReconnect(c Client) (tea.Model, tea.Cmd) {
	log.Printf("remote link restored after %d attempt(s)", m.reconnect.attempt)
	m.client = c
	m.clientGen++
	m.reconnect = reconnectState{}

	// Before the attach that triggers replay, not after: the daemon starts
	// sending the moment it processes MsgAttach, and a reset arriving late would
	// wipe the replay it was meant to make room for.
	m.resetPanesForReattach()
	m.resetWorkStateForReattach()

	return m, tea.Batch(m.attachToDaemon(), m.listenForMessages())
}
