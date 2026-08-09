package tui

import (
	"errors"
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
	// dest names the connection that died, for a client holding several. Empty
	// is the local daemon — and also what every single-connection drop reports,
	// so the existing handling is unchanged.
	dest string
	err  error
}

// errLinkLost is the cause carried by a router-synthesised drop. The router's
// pump discards the transport error (Receive's contract is one message or one
// MsgLinkLost, and ipc.Message has nowhere to put an error), so the banner
// needs a non-nil cause of its own — beginReconnect renders msg.err.
var errLinkLost = errors.New("connection to the daemon was lost")

// reconnectState tracks an in-progress reconnect. The zero value means "not
// reconnecting", matching the ctxMenu/palette convention in this package: there
// is no separate open bool that could drift out of sync with the rest.
//
// One instance per DESTINATION, held in Model.links. The struct itself is
// unchanged by that: the flap window, the backoff ladder and the parked banner
// all still apply, now once per daemon rather than once per session.
type reconnectState struct {
	active  bool
	attempt int       // 1-based; drives the backoff
	lastErr error     // shown in the banner
	nextAt  time.Time // when the next attempt fires, for the countdown
	// gen retires this destination's own in-flight timers and dials, and it
	// lives HERE rather than on the Model because the thing it identifies is one
	// destination's connection, not the client as a whole.
	//
	// Model.clientGen used to serve both. That works while only one ladder can
	// climb: finishReconnect bumps the single counter, every stale closure
	// carries the old number and is dropped. With a router two ladders climb at
	// once, and one destination completing bumped the counter the OTHER
	// destination's already-armed redialTickMsg and in-flight redialResultMsg
	// were stamped with — so both were discarded, its `active` stayed true with
	// no timer left to clear it, and its banner stuck for the rest of the
	// session with nothing retrying behind it.
	//
	// clientGen still guards the single-connection listen loop, which is a
	// genuinely session-wide thing: there, finishReconnect swaps m.client
	// wholesale and the old loop must be retired. A router's r.in is never
	// swapped, so nothing there needs a session generation.
	gen int
	// lastUpAt and settledAttempt survive a successful reconnect so a flapping
	// link cannot restart the backoff ladder from scratch each time. See
	// beginReconnect.
	lastUpAt       time.Time
	settledAttempt int
	// parked stops the retry loop after a failure that cannot heal on its own —
	// a rejected key, a changed host key, an algorithm mismatch. The banner
	// stays up because the session is paused rather than over, and
	// reconnectResumeKey restarts the loop once the operator has fixed the
	// cause. Never set for a failure the classifier did not recognise.
	parked bool
}

// reconnectFlapWindow is how long a restored link must survive before the next
// drop counts as a fresh outage rather than a continuing one. Matches the
// backoff cap: a link that dies faster than we would have waited to retry it has
// not really recovered.
const reconnectFlapWindow = reconnectMaxDelay

const (
	// reconnectBaseDelay is the nominal wait before the first attempt. Short
	// enough that a brief blip heals before the user reaches for the keyboard.
	reconnectBaseDelay = 500 * time.Millisecond
	// reconnectMaxDelay caps the backoff while a drop still looks transient.
	// Budgeted for Windows, where OpenSSH has no ControlMaster and every
	// attempt pays a full TCP and auth handshake.
	reconnectMaxDelay = 30 * time.Second
	// reconnectDecayAfter is how many attempts stay on the fast curve before
	// the loop assumes the failure is not transient. Four spans roughly four
	// seconds, which covers the case this is tuned for: a host rebooting, where
	// a real reconnect landed on attempt 3.
	reconnectDecayAfter = 4
	// reconnectSlowMaxDelay is the plateau for a sustained failure.
	//
	// This exists for a reason that is not politeness. Every attempt is a fresh
	// ssh with BatchMode=yes, so every attempt is a full authentication — and
	// there is a realistic case where authentication can never succeed while
	// the link itself is fine: the startup dial runs NON-batch and may prompt
	// for a key passphrase, while every reconnect runs batch and cannot. An
	// operator with a passphrase-protected key and no agent authenticates once
	// interactively, then every reconnect fails `publickey` permanently. A
	// changed host key and a dead agent socket behave the same way.
	//
	// On the 30 s cap that is ~120 failed authentications an hour from the
	// operator's own address; a default fail2ban sshd jail (5 failures /
	// 10 min) bans them, and the recidive jail escalates it across every
	// service on the host. A laptop left with the banner up overnight locks its
	// owner out. Five minutes brings it to ~15/hour.
	//
	// Classification now closes the rest of the gap: a failure that
	// transport.ClassifyLinkFailure calls permanent parks the loop outright
	// instead of retrying it (see ErrLinkPermanent). This decay still governs
	// everything it cannot classify, which is the default — exit code 255
	// covers both a permanent denial and a transient timeout, so an unmatched
	// message stays transient on purpose.
	reconnectSlowMaxDelay = 5 * time.Minute
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
	// which a cap test alone does not catch — it would flow through to tea.Tick,
	// fire immediately, and become the hot loop the cap exists to prevent. Exact
	// zero arrives at attempt 57, where the shift count is 56 — 500ms is
	// 1953125 × 2^8 and 1953125 is odd, so the product is only a multiple of
	// 2^64 once 8 + shift reaches 64. Both cases are covered here.
	// TestReconnectDelay_NeverDropsBelowFloorAtAnyAttempt pins this.
	//
	// A positive wrap cannot slip through either: any nonzero wrapped value is a
	// multiple of 2^(8+shift) ≥ 2^43 ns, three orders of magnitude above the
	// cap, so it is caught by the first clause. The two together are exactly
	// sufficient.
	ceiling := reconnectMaxDelay
	if attempt > reconnectDecayAfter {
		ceiling = reconnectSlowMaxDelay
	}
	if d > ceiling || d <= 0 {
		d = ceiling
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

// SetRedialFunc installs the reconnect dialer for ONE destination. Called by
// cmd/quil in remote mode only; a destination with no func never reconnects,
// which is what local sessions get.
//
// dest is the ROUTING destination, not the ssh host name. A single-connection
// remote session routes everything unstamped, so its key is "" — the same key
// its link loss, its projects and its freeze all carry. Only a client holding
// several daemons keys by host.
//
// A setter rather than a NewModel parameter for the same reason SetRemoteDest
// is one: NewModel's signature is already at five arguments.
func (m *Model) SetRedialFunc(dest string, f RedialFunc) {
	if m.redialFns == nil {
		m.redialFns = map[string]RedialFunc{}
	}
	m.redialFns[dest] = f
}

// linkFor returns the reconnect state for one destination, creating it on first
// use. reconnectState itself is unchanged — the flap window, backoff ladder and
// parked handling all still apply, now once per daemon.
//
// Pointer receiver AND pointer result, both load-bearing. Model is a value type
// that Bubble Tea copies on every Update, so the map is what carries a mutation
// back: writing through the returned pointer reaches the entry every copy
// shares. The receiver has to be a pointer for the same reason in reverse — the
// lazy map creation must land on a Model that is returned, so only a caller that
// hands its copy back may reach this. Every read-only path uses linkOf.
func (m *Model) linkFor(dest string) *reconnectState {
	if m.links == nil {
		m.links = map[string]*reconnectState{}
	}
	ls, ok := m.links[dest]
	if !ok {
		ls = &reconnectState{}
		m.links[dest] = ls
	}
	return ls
}

// linkOf reports one destination's reconnect state, or the zero value for a
// destination that has never dropped.
//
// The read-only counterpart to linkFor, and it exists rather than being an
// inlined map read for two reasons: rendering a frame must not create state (a
// value-receiver View would lose the map assignment anyway, so the entry would
// be allocated and thrown away once per frame), and a nil map is the normal
// state of a session that has never lost a link.
func (m Model) linkOf(dest string) reconnectState {
	if ls := m.links[dest]; ls != nil {
		return *ls
	}
	return reconnectState{}
}

// linkHost names the daemon a message about dest is about.
//
// The destination IS the ssh host. It used to fall back to a session-wide
// remoteDest for an empty one, which was correct only while a single
// connection's "" WAS the remote — with a local daemon beside `--remote gpu01`,
// a loss on the LOCAL daemon rendered a banner naming gpu01, i.e. the one
// machine that had not failed.
//
// "" therefore names the local daemon explicitly. Callers that want "is this
// remote at all" must ask remoteModeFor, not test this for emptiness.
//
// A configured [[destinations]] name wins over the ssh destination, because
// that is what the name is for: an ssh destination is routinely user@10.0.0.4,
// and a one-row banner during an outage is exactly where a readable label earns
// its keep.
func (m Model) linkHost(dest string) string {
	if dest == "" {
		return "the local daemon"
	}
	for _, d := range m.cfg.Destinations {
		if d.Dest == dest {
			return d.Label()
		}
	}
	return dest
}

// connFor returns the connection carrying dest, for handing the DEAD one to the
// dialer — cmd/quil closes it there, and this package cannot.
//
// A router answers with the dead conn itself: a pump retires only its LIVENESS
// registration and leaves the record, precisely so the redial has something to
// release. Nil remains possible — a destination unreachable at launch never had
// a conn — and redialRemote tolerates it.
func (m Model) connFor(dest string) Client {
	if r, ok := m.client.(*Router); ok {
		return r.Conn(dest)
	}
	return m.client
}

// SetClientCloser installs the way to release a connection.
//
// Needed because Client is deliberately only Send/Receive, so this package
// cannot close the ssh child behind one. Two callers need it: discarding a
// late-arriving reconnect, and releasing the LIVE connection on exit — which
// `cmd/quil`'s own `defer client.Close()` cannot do, because it captured the
// startup client and after a reconnect the live one only exists on the Model.
// This repo has already paid for leaked child processes on Windows once.
func (m *Model) SetClientCloser(f func(Client)) { m.closeClientFn = f }

// closeClient releases c, if a closer was installed. Safe with a nil closer
// (local sessions never install one) and a nil client.
// clientFlushTimeout bounds how long releasing a connection waits for its
// already-queued frames to reach the socket. Short: this runs on the exit path,
// where a wedged peer must not out-wait the user's patience.
const clientFlushTimeout = 1 * time.Second

// flusher is the optional capability a Client has when its queued frames can be
// waited on. Narrow and optional so the Client interface stays two-method and
// every test fake keeps compiling.
type flusher interface{ Flush(time.Duration) bool }

// closeClient FLUSHES before releasing, and the order is load-bearing. Send is
// non-blocking: it hands the frame to the connection's send loop, which Close
// then stops without writing what is left. Closing straight after a send
// therefore discards frames the caller was told were accepted — for the TUI
// exit path that is the user's final keystrokes, the same loss the input queue
// blocks to avoid, one layer further down. Flush is bounded, so an unresponsive
// peer costs a short wait rather than a hung exit.
func (m Model) closeClient(c Client) {
	if m.closeClientFn == nil || c == nil {
		return
	}
	if f, ok := c.(flusher); ok {
		if !f.Flush(clientFlushTimeout) {
			log.Printf("close: queued frames did not reach the socket within %s", clientFlushTimeout)
		}
	}
	m.closeClientFn(c)
}

// CloseClient releases every connection the Model currently holds. Called by
// cmd/quil on exit, after the Bubble Tea program has returned.
//
// A router is unwrapped rather than handed over whole, and that is the fix for
// a real leak rather than a tidiness: closeClientFn type-asserts to *ipc.Client,
// so passing the *Router simply missed and exit closed NOTHING — every ssh
// child and every remote `quil --stdio` outlived the client, on top of the
// per-reconnect leak retire used to cause. cmd/quil's own `defer client.Close()`
// cannot cover this either: it captured the startup conn of ONE destination.
func (m Model) CloseClient() {
	if r, ok := m.client.(*Router); ok {
		for _, c := range r.Conns() {
			m.closeClient(c)
		}
		return
	}
	m.closeClient(m.client)
}

// canReconnect reports whether a dropped link to dest should be retried rather
// than being fatal.
//
// redialFns[dest] != nil is the WHOLE test, deliberately with no RemoteMode
// conjunct. A local destination never gets a dialer installed, so the nil check
// alone already keeps a dead local daemon fatal — retrying would spin against
// something that is not coming back while hiding the loss.
//
// The conjunct is not merely redundant, it is wrong, and the multi-daemon
// router is what made it so: RemoteMode() answers for the ACTIVE PROJECT, which
// is a different question entirely once a client holds several daemons. A
// background remote host dropping while a LOCAL project is on screen would read
// false and turn a perfectly reconnectable drop fatal.
//
// Note this is now the ONLY thing separating the two cases. Every destination
// is keyed by its ssh host — `quil --remote <host>` included, since the router
// keys its connection that way too — so there is no "" special case left to
// lean on.
func (m Model) canReconnect(dest string) bool {
	return m.redialFns[dest] != nil
}

// freezeInput drops user input while a reconnect is in flight, reporting
// whether it consumed the message.
//
// Dropped rather than buffered, deliberately. Buffered keystrokes would be
// delivered into a live agent session minutes later, at a prompt that has moved
// on — a paste or a stray "y" landing on the wrong question is worse than a
// visible stall. This is a fail-closed choice.
//
// One choke point rather than a guard in each of the six input branches: every
// input decision is made in one place, whereas six scattered guards drift.
//
// Note what the choke point does NOT give: it matches on message TYPE, so a new
// input-bearing message is covered only once it is LISTED here, not by default.
// clipboardPastedMsg proved that — it was added as the delayed half of a paste
// and rode straight through the freeze until it was named. Anything added later
// that can reach a PTY belongs in this function.
//
// Ctrl+Q is the single exception. It is the only way out of a host that never
// comes back, and by definition the reconnect loop cannot end the session
// itself — it retries forever.
//
// Scoped to the ACTIVE destination, which is the whole point of a per-daemon
// link table: input goes to the pane the user is typing into, so only that
// daemon being down can justify dropping it. A background project's daemon
// dropping must not freeze typing into a local pane. The gate lives here rather
// than only at the call site so the choke point stays correct standalone.
func (m Model) freezeInput(msg tea.Msg) (tea.Cmd, bool) {
	// A delayed paste is gated by ITS OWN destination, ahead of the active-dest
	// check, because it is the one input bound to a pane rather than to "wherever
	// the user is typing". The clipboard read can outlive a project switch, so
	// the two can disagree — and the active-dest gate is wrong in both
	// directions when they do: it discards a paste headed for a healthy daemon,
	// and releases one headed for a daemon that is reconnecting.
	//
	// destOfPane falls back to the active dest for a pane it cannot find, which
	// is the right default here: a pane that vanished mid-read has nowhere to
	// deliver to, and sendClipboardToPaneID drops it on arrival anyway.
	if p, ok := msg.(clipboardPastedMsg); ok {
		return nil, m.linkOf(m.destOfPane(p.paneID)).active
	}
	if !m.linkOf(m.activeDest()).active {
		return nil, false
	}
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if isFreezeEscape(msg.String(), m.cfg.Keybindings.Quit) {
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

// freezeEscapeKeys are always honoured during a freeze, whatever the config says.
//
// The configured quit binding is checked first and normally does the work. These
// exist because kbMatches returns false for an EMPTY configured string, so a
// config with `quit = ""` would leave a frozen session with no key out at all:
// every other binding is swallowed, and Bubble Tea v2 delivers ctrl+c as an
// ordinary KeyPressMsg, so that is swallowed too. The only recourse would be
// killing the process from another terminal — an unrecoverable UI reached by
// editing one config value, which is too sharp an edge to leave in place.
var freezeEscapeKeys = []string{"ctrl+q", "ctrl+c"}

// ErrLinkPermanent marks a dial failure that an identical retry cannot fix — a
// rejected key, a changed host key, an algorithm mismatch.
//
// The dialer wraps it and the loop tests with errors.Is. A sentinel rather than
// a bool on redialResultMsg so the classification travels WITH the error it
// describes, and cannot be dropped by a future call site that forgets to copy a
// field.
var ErrLinkPermanent = errors.New("remote link failure is permanent")

// resumeReconnect leaves the parked state and arms a fresh attempt.
//
// The attempt counter is deliberately NOT reset. The operator resuming does not
// make the earlier failures un-happen, and restarting at the base delay would
// undo the rate decay that keeps a still-broken key under a fail2ban threshold.
//
// Acts on the ACTIVE destination, matching the banner: the key is pressed at a
// banner naming one host, and resuming a daemon whose state the user cannot see
// would be an invisible authentication attempt.
func (m Model) resumeReconnect() (tea.Model, tea.Cmd) {
	dest := m.activeDest()
	// A destination can be parked with no dialer behind it: a multi-daemon
	// session parks a link it cannot reconnect rather than quitting the whole
	// client over it. Resuming that would reach redialCmd with a nil RedialFunc
	// and panic. The banner does not offer the key there either, but the guard
	// belongs at the action, not only at its affordance.
	if !m.canReconnect(dest) {
		return m, nil
	}
	ls := m.linkFor(dest)
	log.Printf("remote: resuming a parked reconnect to %s at attempt %d", m.linkHost(dest), ls.attempt)
	ls.parked = false
	ls.lastErr = nil
	// scheduleRedial returns (tea.Model, tea.Cmd) and carries the mutated copy
	// forward — Model is a value type, so returning m here instead would drop
	// whatever it set.
	return m.scheduleRedial(dest)
}

// isFreezeEscape reports whether a key should end a frozen session.
func isFreezeEscape(key, configuredQuit string) bool {
	if kbMatches(key, configuredQuit) {
		return true
	}
	for _, k := range freezeEscapeKeys {
		if key == k {
			return true
		}
	}
	return false
}

// firstErrLine reduces a multi-line diagnostic to a single banner-safe line.
//
// ssh errors routinely span several lines. The banner is a one-row overlay, so a
// raw multi-line message would paint over the rows beneath it rather than
// growing the box. Named to avoid colliding with the test-only firstLine.
//
// Tabs become spaces, and that is not cosmetic. The transport's sanitizer
// deliberately PRESERVES \t (correct for multi-line log output), while
// lipgloss.Width measures a tab as ZERO cells — so a remote emitting a few
// hundred tabs to its stderr produces a string that passes both the width
// budget here and the overlay's own width gate, then expands across many screen
// rows once the terminal renders it, displacing the frame. Remote-controlled
// text reaching a fixed-width row has to be measured the way it will be drawn.
func firstErrLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(strings.ReplaceAll(s, "\t", " "))
}

// bannerSep separates the status core from the ssh diagnostic.
// reconnectResumeKey restarts a parked loop.
//
// Checked in Update ahead of the freezeInput choke point, because freezeInput
// has a value receiver and returns (tea.Cmd, bool) — it can neither clear the
// parked state nor hand back a mutated Model. It is also not a freeze ESCAPE:
// isFreezeEscape means "this key ends the session", which resuming is not.
const reconnectResumeKey = "r"

// bannerResumeHint is the parked banner's resume affordance.
//
// A named constant because the banner test and the bannerCandidates invariant
// test must assert the SAME literal. Asserting the bare key letter cannot fail:
// every existing rung already contains an "r" — "ctrl+q", "unreachable",
// "Connecting" — so the assertion would pass with no hint rendered at all.
const bannerResumeHint = "r retries"

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
//
// Renders the ACTIVE project's destination. The banner is one row over the tab
// bar and the user is looking at one project, so it reports the daemon behind
// what is on screen; a background daemon's outage shows up in that project's own
// panes when the user switches to it.
func (m Model) bannerCandidates() []string {
	dest := m.activeDest()
	ls := m.linkOf(dest)
	host, attempt := m.linkHost(dest), ls.attempt
	// Checked BEFORE both phases. nextAt is stale-past while parked, so the
	// countdown branch below falls through to "Connecting" — the single most
	// misleading string available, since nothing is connecting and nothing will
	// until the operator acts.
	if ls.parked {
		// A parked link with no dialer cannot be resumed at all — that is a
		// multi-daemon session keeping itself alive around a daemon it can never
		// reach again, rather than an operator-fixable ssh failure. Offering
		// "r retries" there would be a key that does nothing, which is worse
		// than no affordance: it reads as "still trying".
		if !m.canReconnect(dest) {
			return []string{
				fmt.Sprintf("%s is gone — its panes are lost%sctrl+q quits", host, bannerSep),
				fmt.Sprintf("%s is gone%sctrl+q", host, bannerSep),
				"Daemon gone" + bannerSep + "ctrl+q",
			}
		}
		return []string{
			fmt.Sprintf("%s unreachable — stopped, %s%sctrl+q quits", host, bannerResumeHint, bannerSep),
			fmt.Sprintf("Stopped — %s%sctrl+q", bannerResumeHint, bannerSep),
			bannerResumeHint + bannerSep + "ctrl+q",
		}
	}
	if remain := time.Until(ls.nextAt); remain > 0 {
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
	ls := m.linkOf(m.activeDest())
	if !ls.active || width <= 0 {
		return ""
	}

	// Longest first; the first one that fits wins. Every rung keeps ctrl+q.
	candidates := m.bannerCandidates()
	if len(candidates) == 0 {
		// Unreachable today (both branches return three rungs), but this is the
		// render path — an index panic here takes the whole frame down at the
		// worst possible moment, during an outage.
		return ""
	}
	core := candidates[len(candidates)-1]
	for _, c := range candidates {
		if lipgloss.Width(c) <= width {
			core = c
			break
		}
	}

	if ls.lastErr != nil {
		if detail := firstErrLine(ls.lastErr.Error()); detail != "" {
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

// eachClientPane calls fn for every pane ONE daemon holds.
//
// Every tab of every project on that destination, not just the active one — a
// single attach replays background tabs too — plus each tab's overlay pane,
// which is a live daemon pane replayed like any other but deliberately kept
// OUTSIDE the layout tree, so a Leaves()-only walk misses it.
//
// Scoped by dest because only the reconnected daemon replays: sweeping every
// project would arm resets no chunk ever consumes and, worse, zero the work
// counters of daemons that never dropped — clearing a live spinner on a machine
// that is still running the turn, corrected only by its next hook event.
//
// Shared by both reattach resets so the two enumerations cannot drift: a pane
// class added to one and forgotten in the other is precisely the bug that would
// survive review, because each reset looks complete on its own.
func (m *Model) eachClientPane(dest string, fn func(*PaneModel)) {
	for _, proj := range m.projects {
		if proj == nil || proj.Dest != dest {
			continue
		}
		for _, tab := range proj.tabs {
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
}

// armReattachReset marks every pane to be reset when — and only when — the
// daemon's replay for it actually arrives. The reset itself happens in the
// PaneOutputMsg handler on the first `Ghost` chunk.
//
// Deferred rather than predicted, and that is the whole point. Resetting exists
// to stop a replay DOUBLING a pane's scrollback, so it is worth paying only
// where a replay comes; `handleAttach` sends one only for plugins with
// `ghost_buffer = true`, and wiping a pane that gets nothing leaves a blank
// rectangle in front of a live process — indefinitely, in a background tab.
//
// The obvious implementation is to ask the plugin registry which types replay.
// That is wrong here, in a way worth writing down: the registry the Model holds
// is loaded from `config.PluginsDir()` — **this** machine's plugins — while
// `handleAttach` decides from the DAEMON's registry. In remote mode those are
// different machines and can disagree arbitrarily; even locally the TUI reloads
// its own registry when a plugin TOML is saved, ahead of the daemon processing
// the reload. Either way a mismatch corrupts the reattach in both directions:
// clear a pane nothing repaints, or preserve one that is replayed and double it.
// Reconciling the two registries is RD-023's job (Phase 3).
//
// Waiting for the replay removes the guess entirely. The daemon's action is the
// signal, so no agreement about plugin config is needed.
//
// Panes that never receive a replay keep their content and stay armed, which is
// harmless: only an attach replay ever sets `Ghost`, so the flag can only be
// consumed by the thing it is waiting for. claude-code is one of those — it gets
// a `redraw_key` kick instead, and repaints over its existing grid exactly as it
// did before reconnect existed.
func (m *Model) armReattachReset(dest string) {
	m.eachClientPane(dest, func(p *PaneModel) {
		p.reattachReset = true
		// Forget that this pane has been sized. The suppression in diffResizes
		// describes a daemon-side guard (appliedCols/appliedRows) that a PTY
		// reinstall zeroes, so carrying it across an outage would withhold the
		// one resize repaintAfterResize needs to bring a restored pane back.
		// delete on a nil map is a no-op.
		delete(m.sizedOnce, p.ID)
	})
	// Selection is Model-level and anchors to row/column coordinates that any
	// replay invalidates. Dropped now rather than armed: there is no per-pane
	// chunk to hang it off, and a selection surviving an outage is worth nothing.
	//
	// Only when it belongs to the daemon that reconnected. destOfPane falls back
	// to the active dest for a pane the Model has not seen, so a selection on an
	// unknown pane is dropped exactly when the active daemon is the one
	// reconnecting — which is the case its content is about to be replaced in.
	if m.selection != nil && m.destOfPane(m.selection.PaneID) == dest {
		m.selection = nil
	}
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
func (m *Model) resetWorkStateForReattach(dest string) {
	m.eachClientPane(dest, func(p *PaneModel) {
		p.working = false
		p.turnActive = false
		clear(p.subagents)
		p.subagentsOverflow = false
	})
}

// redialTickMsg fires when the backoff for one attempt has elapsed.
//
// dest rides along with the generation because a client holding several daemons
// can have two ladders climbing at once: attempt 3 for one host is attempt 1 for
// another, so matching on the counter alone would let one destination's tick
// start the other's dial.
type redialTickMsg struct {
	gen     int
	dest    string
	attempt int
}

// redialResultMsg carries one attempt's outcome, for the destination it dialled.
type redialResultMsg struct {
	gen    int
	dest   string
	client Client
	err    error
}

// handleLinkLost marks ONE destination as reconnecting.
//
// It deliberately does not move the active project: the client stays put and
// renders the parked project with its last content. Being teleported into a
// different daemon's work is worse than stale work honestly labelled.
func (m *Model) handleLinkLost(dest string, err error) {
	ls := m.linkFor(dest)
	if ls.active {
		// One ladder per destination. Re-entering would reset attempt and lose the
		// flap carry, which is the guard beginReconnect's early return also serves
		// — this one keeps the state half correct for a direct caller.
		return
	}
	// Carry the attempt count forward when the link barely survived. Clearing it
	// on every success means a remote daemon that accepts, verifies, attaches and
	// then dies restarts the ladder at 500 ms every time — a crash-looping daemon
	// gets a fresh ssh roughly twice a second forever, with the counter never
	// passing 1. That is the same signature as the false-success bug
	// verifyRemoteLink was added to fix, reached by a different route, and the
	// 30 s cap only protects against it if the counter survives.
	carried := 0
	if !ls.lastUpAt.IsZero() && time.Since(ls.lastUpAt) < reconnectFlapWindow {
		carried = ls.settledAttempt
		log.Printf("remote: link to %s lasted %v (<%v) — resuming backoff at attempt %d rather than restarting it",
			m.linkHost(dest), time.Since(ls.lastUpAt).Round(time.Millisecond), reconnectFlapWindow, carried+1)
	}
	// gen carries forward across the drop as well. It identifies this
	// destination's connection, and a drop does not make the timers armed for the
	// PREVIOUS one current again — resetting it here would let a redial result
	// still in flight from before the outage be accepted as this outage's.
	*ls = reconnectState{gen: ls.gen, active: true, lastErr: err, attempt: carried}

	// The transient UI below belongs to the project on screen, so it is torn down
	// only when the daemon that dropped is the one the user is looking at. A
	// background host dying must not close the palette someone is typing into.
	if dest != m.activeDest() {
		return
	}
	// A drag in flight refers to coordinates and panes that the post-reattach
	// state may not have; drop it rather than resolve it later.
	m.clearDragState()
	// Transient pickers are worthless after an outage and their input is frozen,
	// so leaving them open strands the user in a UI that ignores Esc. Dialogs
	// proper are deliberately left alone — they may hold unsaved work.
	m.closeCtxMenu()
	if m.dialog == dialogCommandPalette {
		m.dialog = dialogNone
	}
}

// beginReconnect enters the reconnecting state for ONE destination and arms its
// first attempt. Every other destination is untouched — that is the whole point
// of a per-daemon link table.
func (m Model) beginReconnect(dest string, cause error) (tea.Model, tea.Cmd) {
	if m.linkOf(dest).active {
		return m, nil // already reconnecting; one loop per destination
	}
	log.Printf("remote link to %s lost, reconnecting: %v", m.linkHost(dest), cause)
	m.handleLinkLost(dest, cause)
	return m.scheduleRedial(dest)
}

// scheduleRedial arms the next attempt's timer.
//
// Every attempt is logged. Only the first drop and the eventual success used to
// be, which is exactly backwards: a reconnect that succeeds needs no diagnosis,
// while one that never does left the log silent after a single "link lost" line.
// The failure cause is logged with it, since that is what the user is looking at
// in the banner and what they will quote when reporting it.
func (m Model) scheduleRedial(dest string) (tea.Model, tea.Cmd) {
	ls := m.linkFor(dest)
	ls.attempt++
	delay := reconnectDelay(ls.attempt, rand.Float64())
	ls.nextAt = time.Now().Add(delay)
	log.Printf("remote: reconnect attempt %d to %s in %v (last error: %v)",
		ls.attempt, m.linkHost(dest), delay.Round(time.Millisecond), ls.lastErr)
	gen, attempt := ls.gen, ls.attempt
	return m, tea.Tick(delay, func(time.Time) tea.Msg {
		return redialTickMsg{gen: gen, dest: dest, attempt: attempt}
	})
}

// redialCmd performs one dial off the Update goroutine.
//
// The dead client is handed to the dialer rather than closed here: Client is
// only Send/Receive, so this package cannot release the underlying ssh child,
// and cmd/quil is the layer that knows the value is really an *ipc.Client.
//
// The dialer is looked up per destination and the dead conn resolved per
// destination too — handing over m.client would give a multi-daemon client's
// router to a dialer that expects one connection.
func (m Model) redialCmd(dest string) tea.Cmd {
	gen, dial, old := m.linkOf(dest).gen, m.redialFns[dest], m.connFor(dest)
	return func() tea.Msg {
		c, err := dial(old)
		return redialResultMsg{gen: gen, dest: dest, client: c, err: err}
	}
}

// finishReconnect installs the new connection for ONE destination and
// re-attaches to it.
//
// The generation bump is what retires every closure still holding the dead
// connection: that destination's redialTickMsg and redialResultMsg all carry
// the old number and are dropped on arrival. It is per-destination (see
// reconnectState.gen), so completing one ladder no longer discards another's
// armed timer.
//
// With a router there is no single client to swap: the fresh conn replaces ONE
// entry, and the pump Add starts is what makes that daemon's messages flow
// again. Remove cannot interrupt a pump still parked inside Receive — Client is
// only Send/Receive — but a dead pump has already retired its own liveness
// registration, which is what makes the Add land rather than early-return.
//
// The listen loop is re-armed ONLY on the single-connection path, and the
// asymmetry is load-bearing. There the loop died with the client and clientGen
// must bump to retire it. A router's loop never died: the drop arrived as DATA
// on r.in and Update's linkLostMsg branch already re-armed the one reader.
// Arming a second here would add a goroutine per reconnect, each racing the
// others for the same channel — unbounded over a flapping session.
//
// m.attached is SET rather than cleared. A destination that reconnects has been
// attached again right here, and the flag is what stops the next WindowSizeMsg
// attaching it a second time — the daemon replays its whole output buffer on
// every attach, so that is a doubled scrollback rather than a redundant no-op.
// Setting it also covers the destination that was unreachable at LAUNCH and so
// never carried the flag at all.
func (m Model) finishReconnect(dest string, c Client) (tea.Model, tea.Cmd) {
	ls := m.linkFor(dest)
	log.Printf("remote link to %s restored after %d attempt(s)", m.linkHost(dest), ls.attempt)
	isRouter := false
	if r, ok := m.client.(*Router); ok {
		isRouter = true
		r.Remove(dest)
		r.Add(dest, c)
	} else {
		m.client = c // single-daemon path, unchanged
		m.clientGen++
	}
	if m.attached == nil {
		m.attached = map[string]bool{}
	}
	m.attached[dest] = true
	// Not the zero value: lastUpAt and settledAttempt carry forward so a link
	// that dies again within reconnectFlapWindow resumes the backoff instead of
	// restarting it, and gen carries forward because it is what identifies this
	// destination's superseded timers. Everything else is cleared.
	*ls = reconnectState{
		gen:            ls.gen + 1,
		lastUpAt:       time.Now(),
		settledAttempt: ls.attempt,
	}

	// Before the attach that triggers replay, not after: the daemon starts
	// sending the moment it processes MsgAttach, and a reset arriving late would
	// wipe the replay it was meant to make room for. Scoped to dest — only this
	// daemon is about to replay.
	m.armReattachReset(dest)
	m.resetWorkStateForReattach(dest)

	if isRouter {
		return m, m.attachToDest(dest)
	}
	return m, tea.Batch(m.attachToDest(dest), m.listenForMessages())
}
