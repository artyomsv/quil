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
	// The whole premise is "you are not looking at Quil". RequireBlur is the
	// escape hatch for a terminal that never reports focus at all.
	if cfg.RequireBlur && m.termFocused {
		return
	}

	nowBlocked := !pane.blockedSince.IsZero()
	var kind toastKind
	var headline string
	switch {
	case cfg.Blocked && nowBlocked && !wasBlocked:
		kind, headline = toastBlocked, glyphBlocked+" Waiting for your input"
	case cfg.Done && pane.unseen && !wasUnseen:
		kind, headline = toastDone, glyphDone+" Turn finished"
	default:
		return
	}

	now := time.Now()
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
	if nowBlocked && pane.blockedReason != "" {
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
