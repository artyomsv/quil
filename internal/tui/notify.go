package tui

import (
	"runtime"
	"time"

	"github.com/artyomsv/quil/internal/logger"
	"github.com/artyomsv/quil/internal/notify"
)

// desktopNotifier is defined HERE, at the consumer, per the project's Go
// conventions — internal/notify returns a concrete implementation and this
// package names only the two methods it uses, which is also what makes the
// whole trigger path testable with a recording fake on Linux.
type desktopNotifier interface {
	Notify(notify.Notification) error
	Withdraw(tag string) error
}

// ActivatePaneMsg asks the TUI to focus a pane by id.
//
// Exported because cmd/quil constructs it from the activation listener and
// delivers it with Program.Send. It carries a pane id and nothing else: the
// path from a registry-reachable URI to this message must not be able to name
// a command, an argument or a filesystem path.
type ActivatePaneMsg struct{ PaneID string }

// toastKind records WHICH attention state raised a live toast.
//
// Tracking the kind rather than mere presence is a correctness requirement, not
// bookkeeping. The two states are independent: a pane can be `unseen` from a
// finished turn AND later `blocked` by a background subagent's permission
// prompt. Answering that prompt clears blockedSince ONLY — answerBlockedByInput
// deliberately does not touch unseen — so a sweep that kept the toast while
// EITHER state held would leave "▲ Waiting for your input" standing in Action
// Center until the user happened to focus that pane. That is precisely the
// surface-that-accumulates-lies this feature's Withdraw exists to prevent.
type toastKind int

const (
	toastBlocked toastKind = iota
	toastDone
)

// live reports whether the state that raised a toast of this kind still holds.
func (k toastKind) live(p *PaneModel) bool {
	switch k {
	case toastBlocked:
		return !p.blockedSince.IsZero()
	case toastDone:
		return p.unseen
	}
	return false
}

// SetDesktopNotifier installs the notifier and the identity the activation URI
// must carry.
//
// An explicit setter rather than a NewModel parameter, matching SetRecentCWDs:
// roughly 46 tests build a Model directly, and threading a platform-dependent
// value through the constructor would make every one of them care about it.
//
// opts is passed in rather than derived from m.devMode — that field is
// QUIL_HOME != "", which is true for a prod binary under a custom QUIL_HOME
// too, and would then name the dev AUMID that `quil notify setup` never wrote.
// Called even when n is nil, so opts is always populated: the Settings row
// reports registration state, which it cannot look up without the variant.
func (m *Model) SetDesktopNotifier(n desktopNotifier, opts notify.Options, pid int) {
	m.notifier = n
	m.notifyOpts = opts
	m.notifyPID = pid
}

// desktopNotifyState describes what the Settings row should say.
//
// A separate type rather than a bool because with `enabled` defaulting true,
// "on but not registered" is the DEFAULT state on a fresh Windows install
// rather than an edge case — and rendering that as a plain "on" would claim
// toasts are working when no toast can be displayed at all. Same rule the
// Sidebar width setter states: never show a value the system is not using.
type desktopNotifyState int

const (
	desktopOff desktopNotifyState = iota
	desktopOn
	desktopNeedsSetup
	desktopUnsupported
)

func (s desktopNotifyState) label() string {
	switch s {
	case desktopOn:
		return "on"
	case desktopNeedsSetup:
		return "on (run notify setup)"
	case desktopUnsupported:
		return "unsupported"
	default:
		return "off"
	}
}

// desktopState resolves what the row shows.
//
// Order matters: disabled outranks everything, because a user who turned it off
// does not need to be told about registration. Unsupported outranks
// needs-setup for the same reason — there is nothing to set up.
//
// Registration is read from whether a notifier was INSTALLED, never by probing
// the filesystem and registry here. This function is reached from
// settingsFields()[].get, which renderSettingsDialog calls for every row on
// every frame — so a probe would run os.Stat + two registry reads per frame,
// hundreds per second while a pane streams output. Worse, on a domain-joined
// machine with folder redirection %APPDATA% is a UNC path, so an unreachable
// share would freeze the whole TUI behind an SMB timeout on the Update
// goroutine — the exact class remote-dialogs.md builds maxBlockingFSCalls for.
//
// notify.New returns ErrNotRegistered when the artifacts are absent, so a
// non-nil notifier IS the registration answer, already resolved once at launch.
// The cost is that running `quil notify setup` from another window does not
// update this row until relaunch — which is honest rather than merely
// convenient: this process genuinely has no notifier until then.
func (m *Model) desktopState() desktopNotifyState {
	if !m.cfg.Notification.Desktop.Enabled {
		return desktopOff
	}
	if runtime.GOOS != "windows" {
		return desktopUnsupported
	}
	if m.notifier == nil {
		return desktopNeedsSetup
	}
	return desktopOn
}

// raiseAttentionToast fires a desktop toast on a RISING attention edge.
//
// Called from the single derivation point in applyWorkTransition, deriving the
// edge from the before/after pair rather than hooking each write of
// blockedSince and unseen. There are three such writes today (workstate.go
// lines 350, 384 and 421); a fourth added later would silently emit nothing if
// this were wired per write site. The spinner beside it already works this way
// for the same reason.
func (m *Model) raiseAttentionToast(pane *PaneModel, proj *ProjectModel, wasBlocked, wasUnseen bool) {
	// Read from m.cfg, never from a cached copy — see the Model field comment.
	// This is also what makes the F1 -> Settings toggle take effect immediately.
	cfg := m.cfg.Notification.Desktop
	if m.notifier == nil || !cfg.Enabled || pane == nil || proj == nil {
		return
	}
	// A muted pane is muted everywhere. The sidebar already honours this and a
	// toast is louder than a sidebar row, so honouring it here is the minimum.
	if pane.Muted {
		return
	}
	if m.suppressedByAttention(pane.ID) {
		return
	}

	nowBlocked := !pane.blockedSince.IsZero()
	var kind toastKind
	switch {
	case cfg.Blocked && nowBlocked && !wasBlocked:
		kind = toastBlocked
	case cfg.Done && pane.unseen && !wasUnseen:
		kind = toastDone
	default:
		return
	}
	m.emitToast(pane, proj, kind)
}

// suppressedByAttention reports whether the user is demonstrably already
// looking at this pane, in which case a toast tells them nothing.
//
// "Looking at it" means the terminal has focus AND the pane is the active pane
// of the active tab of the active project. Anything else — another tab,
// another project, another application — is a pane the user cannot see, and
// those are exactly the ones worth a toast. Quil's whole premise is agents
// running in projects you are not currently looking at.
//
// An earlier version gated on the terminal being unfocused alone, which made
// the feature useless for its main case: an agent parking in project B while
// you work in project A produced nothing at all. That was wrong, and it was
// wrong in the direction that quietly removes most of the value.
//
// RequireBlur remains as an opt-in for the stricter behaviour — never toast
// while the terminal has focus at all — for anyone who finds cross-tab toasts
// noisy. It now defaults false, so the default matches what the sidebar
// already does.
func (m *Model) suppressedByAttention(paneID string) bool {
	if !m.termFocused {
		return false // the whole app is in the background
	}
	if m.cfg.Notification.Desktop.RequireBlur {
		return true // opt-in: focused terminal suppresses everything
	}
	return m.paneOnScreenFocused(paneID)
}

// paneOnScreenFocused reports whether this pane is the one the user is
// actually looking at right now.
//
// Same three-part test applyWorkTransition uses to decide whether a completed
// turn counts as `unseen`, so the toast and the sidebar cannot disagree about
// what "you saw it" means.
func (m *Model) paneOnScreenFocused(paneID string) bool {
	pane, proj, tabIdx := m.findPaneAndTab(paneID)
	if pane == nil || proj == nil || proj != m.cur() {
		return false
	}
	if tabIdx != proj.activeTab || tabIdx >= len(proj.tabs) {
		return false
	}
	return proj.tabs[tabIdx].ActivePane == paneID
}

// focusedPaneID is the pane the user is looking at, or "" when the terminal is
// in the background. Used only to notice that it changed.
func (m *Model) focusedPaneID() string {
	if !m.termFocused {
		return ""
	}
	tab := m.activeTabModel()
	if tab == nil {
		return ""
	}
	return tab.ActivePane
}

// raiseDeferredToasts fires for panes that ALREADY need attention, at the
// moment the user stops looking at them.
//
// Without this the feature only works when the user happens to be away at the
// exact instant an agent parks. The common real sequence is the opposite: you
// watch the agent finish, then leave — and because the rising edge fired while
// you were focused, it was suppressed and never comes back. Observed directly:
// Stop at 21:32:54 while the pane was focused, user leaves, agent idle-waiting
// at 21:33:54, no toast ever. "Notify me when I'm away" has to mean when I
// LEAVE, not only if I was already gone.
//
// Deliberately state-based rather than edge-based: it asks what needs attention
// NOW. outstandingToasts skips anything already showing, and the per-pane
// cooldown stops alt-tabbing from producing a burst per switch.
func (m *Model) raiseDeferredToasts() {
	cfg := m.cfg.Notification.Desktop
	if m.notifier == nil || !cfg.Enabled {
		return
	}
	for _, proj := range m.projects {
		for _, tab := range proj.tabs {
			if tab.Root == nil {
				continue
			}
			for _, pane := range tab.Leaves() {
				if _, showing := m.outstandingToasts[pane.ID]; showing {
					continue
				}
				if m.suppressedByAttention(pane.ID) {
					continue
				}
				switch {
				case cfg.Blocked && !pane.blockedSince.IsZero():
					m.emitToast(pane, proj, toastBlocked)
				case cfg.Done && pane.unseen:
					m.emitToast(pane, proj, toastDone)
				}
			}
		}
	}
}

// emitToast is the single place a toast is built, rate-limited and sent.
//
// Shared by the rising-edge path and the on-blur sweep so the two cannot
// disagree about muting, the cooldown, sanitising, or what gets recorded for
// withdrawal. It does NOT check termFocused: the edge path checks it before
// deciding, and the blur path is by definition unfocused.
func (m *Model) emitToast(pane *PaneModel, proj *ProjectModel, kind toastKind) {
	if m.notifier == nil || pane == nil || proj == nil {
		return
	}
	// A muted pane is muted everywhere. The sidebar already honours this and a
	// toast is louder than a sidebar row.
	if pane.Muted {
		return
	}

	headline := glyphDone + " Turn finished"
	if kind == toastBlocked {
		headline = glyphBlocked + " Waiting for your input"
	}

	now := time.Now()
	cfg := m.cfg.Notification.Desktop
	if !pane.lastToastAt.IsZero() && now.Sub(pane.lastToastAt) < cfg.CooldownDuration() {
		return
	}
	pane.lastToastAt = now

	// Sanitised HERE rather than inside internal/notify, because this package's
	// standing rule is that remote text is cleaned at RENDER and never in
	// state — a project name round-trips back to the daemon on rename, so
	// stripping it in state would rewrite a name the user never edited. The
	// toast is one more render surface. internal/notify still escapes and
	// bounds what it is handed; the two passes cover different threats.
	title := sanitizeRemoteText(proj.Name)
	if pane.Name != "" {
		title += " · " + sanitizeRemoteText(pane.Name)
	}
	body := headline
	if kind == toastBlocked && pane.blockedReason != "" {
		body = headline + " (" + sanitizeRemoteText(pane.blockedReason) + ")"
	}

	n := notify.Notification{
		Title:  title,
		Body:   body,
		Tag:    pane.ID,
		Launch: notify.BuildActivateURI(m.notifyOpts.Scheme, m.notifyPID, pane.ID),
	}
	if err := m.notifier.Notify(n); err != nil {
		// Logged, never retried and never surfaced. The sidebar event has
		// already landed, so the user is not missing the information — and a
		// retried toast is worse than a missing one.
		//
		// Returning BEFORE recording it: a toast that was never queued has
		// nothing to withdraw, and recording it anyway would have the sweep
		// later issue a pointless Withdraw for a tag Action Center never saw.
		logger.Debug("notify: toast failed for pane %s: %v", pane.ID, err)
		return
	}

	// Recorded WITH its kind, not as a bare presence marker. The sweep has to
	// know which condition raised this toast — see sweepOutstandingToasts.
	if m.outstandingToasts == nil {
		m.outstandingToasts = make(map[string]toastKind, 1)
	}
	m.outstandingToasts[pane.ID] = kind
}

// sweepOutstandingToasts withdraws toasts whose pane no longer needs attention.
//
// A sweep rather than a hook on each clear site, because the falling edge has
// no choke point the way the rising one does. blockedSince and unseen are
// cleared in at least four places outside applyWorkTransition:
//
//   - PaneModel.answerBlockedByInput — the user typed an answer
//   - Model.ackFocusedPane          — the user focused the pane
//   - ctxmenu.go's "Clear attention" row
//   - pane destruction, which has no transition at all
//
// The first is both the commonest and the worst for an edge-based design:
// approving a Bash/Edit/Write prompt fires NO hook (promptToolMatcher covers
// only AskUserQuestion and ExitPlanMode), so applyWorkTransition is never
// reached and the toast would stand until the turn's Stop, minutes away.
//
// The sweep also covers a fifth clear path added later without anyone
// remembering this file, which a set of hooks cannot.
func (m *Model) sweepOutstandingToasts() {
	// The overwhelmingly common state, and this runs on every Update including
	// the 100 ms spinner tick — so the empty case must cost nothing.
	if len(m.outstandingToasts) == 0 || m.notifier == nil {
		return
	}

	// ONE pass over the workspace, not one findPaneAndTab per outstanding tag.
	// That helper walks every project × tab × layout tree, and this runs on
	// every Update including every paneOutputMsg — so the per-tag form was
	// O(outstanding × panes) precisely when panes are blocked, i.e. exactly
	// when other panes are streaming output.
	live := make(map[string]*PaneModel, len(m.outstandingToasts))
	for _, proj := range m.projects {
		for _, tab := range proj.tabs {
			if tab.Root == nil {
				continue
			}
			for _, pane := range tab.Leaves() {
				if _, watched := m.outstandingToasts[pane.ID]; watched {
					live[pane.ID] = pane
				}
			}
		}
	}

	for paneID, kind := range m.outstandingToasts {
		// Checked against the condition that RAISED it, never against "any
		// attention state" — see toastKind.
		//
		// A vanished pane cannot clear its own flags, so "gone" withdraws too.
		if pane := live[paneID]; pane != nil && kind.live(pane) {
			continue
		}
		if err := m.notifier.Withdraw(paneID); err != nil {
			logger.Debug("notify: withdraw failed for pane %s: %v", paneID, err)
		}
		// Deleting during range is spec-legal in Go and is what keeps this a
		// single pass.
		delete(m.outstandingToasts, paneID)
	}
}
