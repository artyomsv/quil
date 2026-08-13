package tui

import (
	tea "charm.land/bubbletea/v2"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/notify"
)

// fakeNotifier records calls so the whole trigger path is testable on Linux,
// where no real toast mechanism exists.
type fakeNotifier struct {
	sent      []notify.Notification
	withdrawn []string
	err       error
}

func (f *fakeNotifier) Notify(n notify.Notification) error {
	f.sent = append(f.sent, n)
	return f.err
}

func (f *fakeNotifier) Withdraw(tag string) error {
	f.withdrawn = append(f.withdrawn, tag)
	return f.err
}

const (
	toastPaneID  = "pane-0a1b2c3d"
	toastPaneID2 = "pane-11111111"

	// Escapes, never literal characters. A literal U+202E or U+009B in Go
	// source is invisible in review and can be lost in transit — which would
	// silently reduce the leak assertions to strings.Contains(text, ""),
	// always true. Naming them also documents what each one is.
	bidiRLO = "‮" // right-to-left override: PRINTABLE, so no control filter catches it
	c1CSI   = "" // C1 CSI introducer, the reason internal/tui/oscfilter.go exists
)

// toastModel builds a Model with one project, one tab and one pane, wired to a
// fake notifier with desktop notifications enabled and the terminal blurred.
//
// Pane ids use the daemon's real shape ("pane-" + 8 hex) because the toast's
// launch URI round-trips through notify.ParseActivateURI, which enforces it.
func toastModel(t *testing.T) (*Model, *fakeNotifier, *PaneModel) {
	t.Helper()
	pane := &PaneModel{ID: toastPaneID, Name: "claude-code"}
	proj := &ProjectModel{ID: "proj-a", Name: "quil", tabs: []*TabModel{tabWith(pane)}}
	m := Model{projects: []*ProjectModel{proj}, activeProject: 0}

	f := &fakeNotifier{}
	m.SetDesktopNotifier(f, notify.Variant(false), 4321)
	// Written into m.cfg, the ONE authority — there is deliberately no cached
	// copy on the Model for a Settings toggle to fall out of step with.
	m.cfg.Notification.Desktop = config.DesktopConfig{
		Enabled: true, Blocked: true, Done: true,
		Cooldown: "30s",
	}
	m.termFocused = false
	return &m, f, pane
}

func TestRaiseAttentionToast_FiresOnBlockedRisingEdge(t *testing.T) {
	m, f, pane := toastModel(t)
	pane.blockedSince = time.Now()

	m.raiseAttentionToast(pane, m.projects[0], false, false)

	if len(f.sent) != 1 {
		t.Fatalf("sent %d toasts, want 1", len(f.sent))
	}
	if f.sent[0].Tag != pane.ID {
		t.Errorf("Tag = %q, want the pane id %q", f.sent[0].Tag, pane.ID)
	}
	if f.sent[0].Launch == "" {
		t.Fatal("Launch URI empty — the toast would not be clickable")
	}
	pid, paneID, err := notify.ParseActivateURI("quil", f.sent[0].Launch)
	if err != nil {
		t.Fatalf("toast Launch is not a parseable activation URI: %v", err)
	}
	if pid != 4321 || paneID != pane.ID {
		t.Errorf("Launch routes to pid=%d pane=%s, want 4321/%s", pid, paneID, pane.ID)
	}
	if !strings.Contains(f.sent[0].Title, "quil") || !strings.Contains(f.sent[0].Title, "claude-code") {
		t.Errorf("Title = %q, want it to name the project and pane", f.sent[0].Title)
	}
}

func TestRaiseAttentionToast_FiresOnDoneRisingEdge(t *testing.T) {
	m, f, pane := toastModel(t)
	pane.unseen = true

	m.raiseAttentionToast(pane, m.projects[0], false, false)

	if len(f.sent) != 1 {
		t.Fatalf("sent %d toasts, want 1", len(f.sent))
	}
	if !strings.Contains(f.sent[0].Body, "finished") {
		t.Errorf("Body = %q, want it to say the turn finished", f.sent[0].Body)
	}
}

func TestRaiseAttentionToast_SilentWhenAlreadyBlocked(t *testing.T) {
	m, f, pane := toastModel(t)
	pane.blockedSince = time.Now()

	m.raiseAttentionToast(pane, m.projects[0], true, false) // was ALREADY blocked

	if len(f.sent) != 0 {
		t.Errorf("sent %d toasts on a non-edge, want 0", len(f.sent))
	}
}

func TestRaiseAttentionToast_Gates(t *testing.T) {
	tests := []struct {
		name  string
		setup func(m *Model, p *PaneModel)
		want  int
	}{
		// The fixture's pane IS the active pane of the active tab, so a focused
		// terminal means the user is looking straight at it.
		{"looking at this very pane", func(m *Model, p *PaneModel) { m.termFocused = true }, 0},
		{"focused, but this pane is not the active one", func(m *Model, p *PaneModel) {
			m.termFocused = true
			m.projects[0].tabs[0].ActivePane = "pane-99999999"
		}, 1},
		{"feature disabled", func(m *Model, p *PaneModel) {
			m.cfg.Notification.Desktop.Enabled = false
		}, 0},
		{"blocked kind off", func(m *Model, p *PaneModel) {
			m.cfg.Notification.Desktop.Blocked = false
		}, 0},
		{"no notifier installed", func(m *Model, p *PaneModel) { m.notifier = nil }, 0},
		{"muted pane", func(m *Model, p *PaneModel) { p.Muted = true }, 0},
		{"within cooldown", func(m *Model, p *PaneModel) {
			p.lastToastAt = time.Now().Add(-5 * time.Second)
		}, 0},
		{"past cooldown", func(m *Model, p *PaneModel) {
			p.lastToastAt = time.Now().Add(-31 * time.Second)
		}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, f, pane := toastModel(t)
			pane.blockedSince = time.Now()
			tt.setup(m, pane)

			m.raiseAttentionToast(pane, m.projects[0], false, false)

			if len(f.sent) != tt.want {
				t.Errorf("sent %d toasts, want %d", len(f.sent), tt.want)
			}
		})
	}
}

// The `done` kind has its own off switch, and it must suppress independently
// of `blocked`. The gate table above only covers the blocked side; without
// this, setting done=false could stop nothing and no test would notice.
func TestRaiseAttentionToast_DoneKindOffSuppresses(t *testing.T) {
	m, f, pane := toastModel(t)
	m.cfg.Notification.Desktop.Done = false
	pane.unseen = true

	m.raiseAttentionToast(pane, m.projects[0], false, false)

	if len(f.sent) != 0 {
		t.Errorf("sent %d toasts with done=false, want 0", len(f.sent))
	}
}

// A failing notifier must not leave a phantom outstanding entry: there is
// nothing in Action Center to withdraw, so recording it would have the sweep
// later issue a pointless round-trip for a toast that never existed.
func TestRaiseAttentionToast_FailedEmitRecordsNothing(t *testing.T) {
	m, f, pane := toastModel(t)
	f.err = errors.New("boom")
	pane.blockedSince = time.Now()

	m.raiseAttentionToast(pane, m.projects[0], false, false)

	if len(f.sent) != 1 {
		t.Fatalf("the emit should still be attempted once, got %d", len(f.sent))
	}
	if len(m.outstandingToasts) != 0 {
		t.Errorf("outstanding = %v, want empty after a failed emit", m.outstandingToasts)
	}
}

// The cooldown is per pane and SHARED by both kinds: a pane that blocks and
// then finishes five seconds later is one event to a human, not two.
func TestRaiseAttentionToast_CooldownIsSharedAcrossKinds(t *testing.T) {
	m, f, pane := toastModel(t)

	pane.blockedSince = time.Now()
	m.raiseAttentionToast(pane, m.projects[0], false, false)

	pane.blockedSince = time.Time{}
	pane.unseen = true
	m.raiseAttentionToast(pane, m.projects[0], true, false)

	if len(f.sent) != 1 {
		t.Errorf("sent %d toasts, want 1 — the cooldown is shared by blocked and done", len(f.sent))
	}
}

// Project and pane names arrive from a daemon the user may not control, so the
// toast is a remote-text render surface like every other.
func TestRaiseAttentionToast_SanitizesRemoteNames(t *testing.T) {
	m, f, pane := toastModel(t)
	m.projects[0].Name = "safe" + bidiRLO + "reversed"
	pane.Name = "pane" + c1CSI + "here"
	pane.blockedSince = time.Now()

	m.raiseAttentionToast(pane, m.projects[0], false, false)

	if len(f.sent) != 1 {
		t.Fatalf("sent %d toasts, want 1", len(f.sent))
	}
	text := f.sent[0].Title + f.sent[0].Body
	for _, bad := range []string{bidiRLO, c1CSI} {
		// Guard, not padding: an empty needle makes strings.Contains always
		// true, so a lost literal turns this whole assertion into noise. Both
		// forms have already been hit once while writing this file.
		if bad == "" {
			t.Fatal("empty needle in the leak table would make this assertion vacuous")
		}
		if strings.Contains(text, bad) {
			t.Errorf("toast text leaked %q: %q", bad, text)
		}
	}
	for _, keep := range []string{"safe", "reversed", "pane", "here"} {
		if !strings.Contains(text, keep) {
			t.Errorf("sanitising destroyed printable text %q: %q", keep, text)
		}
	}
}

// The blocked reason (the hook's tool name) is remote text too, and is the one
// field that is genuinely optional.
func TestRaiseAttentionToast_IncludesBlockedReasonWhenPresent(t *testing.T) {
	m, f, pane := toastModel(t)
	pane.blockedSince = time.Now()
	pane.blockedReason = "Bash"

	m.raiseAttentionToast(pane, m.projects[0], false, false)

	if len(f.sent) != 1 {
		t.Fatalf("sent %d toasts, want 1", len(f.sent))
	}
	if !strings.Contains(f.sent[0].Body, "Bash") {
		t.Errorf("Body = %q, want it to carry the blocked reason", f.sent[0].Body)
	}
}

// --------------------------------------------------------- the attention gate

// The rule is "you are not looking at THAT PANE" — not "you have left the
// terminal". An agent parking in another project while you work in this one is
// the case Quil exists for, and the original terminal-blur-only gate produced
// nothing for it.
func TestSuppression_OnlyForThePaneOnScreen(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())

	watched := &PaneModel{ID: toastPaneID, Name: "watched"}
	background := &PaneModel{ID: toastPaneID2, Name: "background"}
	projA := &ProjectModel{ID: "proj-a", Name: "a", tabs: []*TabModel{tabWith(watched)}}
	projB := &ProjectModel{ID: "proj-b", Name: "b", tabs: []*TabModel{tabWith(background)}}

	m := Model{projects: []*ProjectModel{projA, projB}, activeProject: 0}
	f := &fakeNotifier{}
	m.SetDesktopNotifier(f, notify.Variant(false), 1)
	m.cfg.Notification.Desktop = config.DesktopConfig{
		Enabled: true, Blocked: true, Done: true, Cooldown: "30s",
	}
	m.termFocused = true // user is IN Quil, looking at project A
	projA.tabs[0].ActivePane = watched.ID

	// The pane on screen: suppressed, the user can see it.
	watched.blockedSince = time.Now()
	m.raiseAttentionToast(watched, projA, false, false)
	if len(f.sent) != 0 {
		t.Errorf("toasted for the pane the user is looking at (%d sent)", len(f.sent))
	}

	// A pane in ANOTHER project: must toast — this is the whole feature.
	background.blockedSince = time.Now()
	m.raiseAttentionToast(background, projB, false, false)
	if len(f.sent) != 1 {
		t.Fatalf("sent %d toasts for a pane in another project, want 1", len(f.sent))
	}
	if !strings.Contains(f.sent[0].Title, "background") {
		t.Errorf("Title = %q, want the background pane", f.sent[0].Title)
	}
}

// A pane in another TAB of the same project is equally invisible.
func TestSuppression_OtherTabStillToasts(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())

	front := &PaneModel{ID: toastPaneID}
	back := &PaneModel{ID: toastPaneID2}
	proj := &ProjectModel{ID: "p", Name: "p", tabs: []*TabModel{tabWith(front), tabWith(back)}}
	proj.activeTab = 0
	proj.tabs[0].ActivePane = front.ID

	m := Model{projects: []*ProjectModel{proj}, activeProject: 0}
	f := &fakeNotifier{}
	m.SetDesktopNotifier(f, notify.Variant(false), 1)
	m.cfg.Notification.Desktop = config.DesktopConfig{Enabled: true, Blocked: true, Done: true, Cooldown: "30s"}
	m.termFocused = true

	back.blockedSince = time.Now()
	m.raiseAttentionToast(back, proj, false, false)

	if len(f.sent) != 1 {
		t.Errorf("sent %d toasts for a pane in a background tab, want 1", len(f.sent))
	}
}

// Switching tab or project away from a waiting pane must surface it, exactly
// like leaving the app does.
func TestUpdate_SwitchingAwayRaisesDeferredToast(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())

	a := &PaneModel{ID: toastPaneID}
	b := &PaneModel{ID: toastPaneID2}
	proj := &ProjectModel{ID: "p", Name: "p", tabs: []*TabModel{tabWith(a), tabWith(b)}}
	proj.activeTab = 0
	proj.tabs[0].ActivePane = a.ID
	proj.tabs[1].ActivePane = b.ID

	m := Model{
		client:        newFakeConn(),
		projects:      []*ProjectModel{proj},
		activeProject: 0,
		notifications: NewNotificationCenter(30, 200),
	}
	f := &fakeNotifier{}
	m.SetDesktopNotifier(f, notify.Variant(false), 1)
	m.cfg.Notification.Desktop = config.DesktopConfig{Enabled: true, Blocked: true, Done: true, Cooldown: "30s"}
	m.termFocused = true
	m.lastFocusedPaneID = a.ID

	// Pane A parks while the user is looking straight at it: correctly silent.
	updated, _ := m.Update(paneEventMsg{PaneID: a.ID, Type: "hook.claude.PermissionRequest"})
	m = updated.(Model)
	if len(f.sent) != 0 {
		t.Fatalf("sent %d toasts for the focused pane, want 0", len(f.sent))
	}

	// User switches to the other tab — pane A is now out of sight.
	proj.activeTab = 1
	updated, _ = m.Update(workSpinnerTickMsg{})
	_ = updated

	if len(f.sent) != 1 {
		t.Errorf("sent %d toasts after switching away, want 1", len(f.sent))
	}
}

// ------------------------------------------------------- deferred (on blur)

// The sequence a real user produced, from the daemon log: the agent finished
// while the pane was FOCUSED (so no unseen mark, correctly), the user then
// alt-tabbed away, and the agent sat waiting. Before this, no toast ever fired
// — the rising edge had already been suppressed and nothing brought it back.
func TestUpdate_BlurRaisesToastForAlreadyWaitingPane(t *testing.T) {
	m, f, pane := wiredToastModel(t)
	m.termFocused = true // user is looking at Quil

	// Agent parks while the user watches: edge fires, gate suppresses it.
	updated, _ := m.Update(paneEventMsg{PaneID: pane.ID, Type: "hook.claude.PermissionRequest"})
	m = updated.(Model)
	if len(f.sent) != 0 {
		t.Fatalf("sent %d toasts while focused, want 0", len(f.sent))
	}
	if pane.blockedSince.IsZero() {
		t.Fatal("setup assumption broken: the pane should be blocked")
	}

	// User leaves.
	updated, _ = m.Update(tea.BlurMsg{})
	got := updated.(Model)

	if len(f.sent) != 1 {
		t.Fatalf("sent %d toasts on blur, want 1 — leaving must surface a pane already waiting", len(f.sent))
	}
	if !strings.Contains(f.sent[0].Body, "Waiting") {
		t.Errorf("Body = %q, want the blocked headline", f.sent[0].Body)
	}
	if _, ok := got.outstandingToasts[pane.ID]; !ok {
		t.Error("the deferred toast must be recorded so the sweep can withdraw it")
	}
}

// Alt-tabbing repeatedly must not produce a toast per switch.
func TestUpdate_BlurDoesNotRetoastWhileOneIsShowing(t *testing.T) {
	m, f, pane := wiredToastModel(t)
	m.termFocused = true
	updated, _ := m.Update(paneEventMsg{PaneID: pane.ID, Type: "hook.claude.PermissionRequest"})
	m = updated.(Model)

	for i := 0; i < 3; i++ {
		updated, _ = m.Update(tea.BlurMsg{})
		m = updated.(Model)
		updated, _ = m.Update(tea.FocusMsg{})
		m = updated.(Model)
	}

	if len(f.sent) != 1 {
		t.Errorf("sent %d toasts across three blur/focus cycles, want 1", len(f.sent))
	}
}

// Nothing needing attention means nothing to say.
func TestUpdate_BlurWithNothingWaitingIsSilent(t *testing.T) {
	m, f, _ := wiredToastModel(t)
	m.termFocused = true

	updated, _ := m.Update(tea.BlurMsg{})
	_ = updated

	if len(f.sent) != 0 {
		t.Errorf("sent %d toasts on blur with no pane waiting, want 0", len(f.sent))
	}
}

// A finished turn the user watched should also surface on leaving — but only
// if it actually left an unseen mark.
func TestBlur_RaisesForUnseenPane(t *testing.T) {
	m, f, pane := toastModel(t)
	m.termFocused = false // the user has left the terminal
	pane.unseen = true

	m.raiseDeferredToasts()

	if len(f.sent) != 1 {
		t.Fatalf("sent %d toasts, want 1 for an unseen pane", len(f.sent))
	}
	if !strings.Contains(f.sent[0].Body, "finished") {
		t.Errorf("Body = %q, want the finished headline", f.sent[0].Body)
	}
}

func TestBlur_RespectsMuteAndDisabled(t *testing.T) {
	t.Run("muted pane", func(t *testing.T) {
		m, f, pane := toastModel(t)
		pane.blockedSince = time.Now()
		pane.Muted = true
		m.raiseDeferredToasts()
		if len(f.sent) != 0 {
			t.Errorf("sent %d toasts for a muted pane, want 0", len(f.sent))
		}
	})
	t.Run("feature disabled", func(t *testing.T) {
		m, f, pane := toastModel(t)
		pane.blockedSince = time.Now()
		m.cfg.Notification.Desktop.Enabled = false
		m.raiseDeferredToasts()
		if len(f.sent) != 0 {
			t.Errorf("sent %d toasts while disabled, want 0", len(f.sent))
		}
	})
}

// ---------------------------------------------------------------- withdrawal

// The rising edge has a single choke point. The falling edge does NOT —
// blockedSince and unseen are cleared in at least four places outside
// applyWorkTransition, so withdrawal is a sweep rather than a mirror of the
// emit. The typing path is the one that matters: approving a Bash/Edit/Write
// prompt fires no hook at all, so an edge-based withdrawal would leave the
// toast standing for the pane's whole remaining turn.
func TestSweepOutstandingToasts_WithdrawsWhenAttentionEnds(t *testing.T) {
	tests := []struct {
		name  string
		clear func(m *Model, p *PaneModel)
	}{
		{"user typed an answer", func(m *Model, p *PaneModel) { p.answerBlockedByInput() }},
		{"pane focused, unseen cleared", func(m *Model, p *PaneModel) {
			p.blockedSince = time.Time{}
			p.unseen = false
		}},
		{"clear attention menu row", func(m *Model, p *PaneModel) {
			p.blockedSince = time.Time{}
			p.blockedReason = ""
			p.unseen = false
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, f, pane := toastModel(t)
			pane.blockedSince = time.Now()
			m.raiseAttentionToast(pane, m.projects[0], false, false)
			if len(f.sent) != 1 {
				t.Fatalf("setup: sent %d toasts, want 1", len(f.sent))
			}

			tt.clear(m, pane)
			m.sweepOutstandingToasts()

			if len(f.withdrawn) != 1 || f.withdrawn[0] != pane.ID {
				t.Errorf("withdrawn = %v, want [%s]", f.withdrawn, pane.ID)
			}
			if _, still := m.outstandingToasts[pane.ID]; still {
				t.Error("swept tag must be dropped from the outstanding set")
			}
		})
	}
}

func TestSweepOutstandingToasts_KeepsLiveToasts(t *testing.T) {
	m, f, pane := toastModel(t)
	pane.blockedSince = time.Now()
	m.raiseAttentionToast(pane, m.projects[0], false, false)

	m.sweepOutstandingToasts()

	if len(f.withdrawn) != 0 {
		t.Errorf("withdrew %v while the pane is still blocked", f.withdrawn)
	}
}

// A destroyed pane cannot clear its own flags, so the sweep must treat "gone"
// as a reason to withdraw.
func TestSweepOutstandingToasts_WithdrawsForDestroyedPane(t *testing.T) {
	m, f, pane := toastModel(t)
	pane.blockedSince = time.Now()
	m.raiseAttentionToast(pane, m.projects[0], false, false)

	m.outstandingToasts["pane-deadbeef"] = toastBlocked
	m.sweepOutstandingToasts()

	found := false
	for _, tag := range f.withdrawn {
		if tag == "pane-deadbeef" {
			found = true
		}
	}
	if !found {
		t.Errorf("withdrawn = %v, want it to include the vanished pane", f.withdrawn)
	}
}

func TestSweepOutstandingToasts_NoOpWhenEmpty(t *testing.T) {
	m, f, _ := toastModel(t)
	m.sweepOutstandingToasts()
	if len(f.withdrawn) != 0 {
		t.Errorf("withdrew %v from an empty set", f.withdrawn)
	}
}

// ------------------------------------------------------------------- wiring

// These are the tests that fail if the PRODUCTION CALL SITES are deleted.
//
// Every test above calls raiseAttentionToast / sweepOutstandingToasts directly.
// A code review proved by mutation that removing BOTH call sites — the one in
// applyWorkTransition and the one in Update — left the entire suite green while
// no toast could ever fire or be withdrawn. A direct-call test is not merely
// weaker than a call-site test; it passes on the working code AND on a mutant
// where the production path is severed, which makes green mean nothing.
//
// So these drive real messages through Update and assert on the fake notifier.

// wiredToastModel is toastModel plus the bits Update needs to run.
func wiredToastModel(t *testing.T) (Model, *fakeNotifier, *PaneModel) {
	t.Helper()
	t.Setenv("QUIL_HOME", t.TempDir())

	pane := &PaneModel{ID: toastPaneID, Name: "claude-code"}
	proj := &ProjectModel{ID: "proj-a", Name: "quil", tabs: []*TabModel{tabWith(pane)}}
	m := Model{
		client:        newFakeConn(),
		projects:      []*ProjectModel{proj},
		activeProject: 0,
		// The paneEventMsg arm records into the sidebar before it ever reaches
		// the work transition, so a nil centre panics before the toast logic
		// runs at all.
		notifications: NewNotificationCenter(30, 200),
	}
	f := &fakeNotifier{}
	m.SetDesktopNotifier(f, notify.Variant(false), 4321)
	m.cfg.Notification.Desktop = config.DesktopConfig{
		Enabled: true, Blocked: true, Done: true,
		Cooldown: "30s",
	}
	m.termFocused = false
	return m, f, pane
}

// A permission prompt arriving as a real hook event must produce a toast
// WITHOUT any test calling raiseAttentionToast. Deleting the call site in
// applyWorkTransition fails exactly here.
func TestUpdate_PermissionRequestRaisesToast(t *testing.T) {
	m, f, pane := wiredToastModel(t)

	updated, _ := m.Update(paneEventMsg{
		PaneID: pane.ID,
		Type:   "hook.claude.PermissionRequest",
		Data:   map[string]string{"tool": "Bash"},
	})
	got := updated.(Model)

	if len(f.sent) != 1 {
		t.Fatalf("sent %d toasts through Update, want 1", len(f.sent))
	}
	if !strings.Contains(f.sent[0].Body, "Bash") {
		t.Errorf("Body = %q, want the tool name from the hook payload", f.sent[0].Body)
	}
	if _, ok := got.outstandingToasts[pane.ID]; !ok {
		t.Error("a raised toast must be recorded for the sweep to withdraw later")
	}
}

// A finished turn on an unfocused pane must toast through Update too — the
// other half of the edge detection, and a different switch arm.
func TestUpdate_FinishedTurnRaisesToast(t *testing.T) {
	m, f, pane := wiredToastModel(t)
	// Make the pane the non-focused one so workStop marks it unseen.
	m.projects[0].tabs[0].ActivePane = "pane-99999999"

	updated, _ := m.Update(paneEventMsg{PaneID: pane.ID, Type: "hook.claude.UserPromptSubmit"})
	m = updated.(Model)
	updated, _ = m.Update(paneEventMsg{PaneID: pane.ID, Type: "hook.claude.Stop"})

	if len(f.sent) != 1 {
		t.Fatalf("sent %d toasts, want 1 for a completed turn", len(f.sent))
	}
	if !strings.Contains(f.sent[0].Body, "finished") {
		t.Errorf("Body = %q, want the finished-turn headline", f.sent[0].Body)
	}
}

// The commonest sequence there is, and the one every other test in this file
// stepped around: ask the agent something, then leave the terminal WITHOUT
// switching pane first. The pane stays the active pane of the active tab, so
// the only thing separating "seen" from "missed" is that the window is behind
// Chrome.
//
// Reported from real use twice ("asked a question to ai pane and switched to
// another window", "tab is marked as finished but I got no toast"). It failed
// because `unseen` — which the Done toast is derived from — was computed from
// the three-part on-screen test ALONE, so a backgrounded terminal still counted
// as the user watching. TestUpdate_FinishedTurnRaisesToast passes only because
// it moves ActivePane away first, which is exactly the case that already worked.
func TestUpdate_FinishedTurnWhileTerminalBlurredRaisesToast(t *testing.T) {
	m, f, pane := wiredToastModel(t)
	m.termFocused = true // the user is in Quil, in this very pane

	updated, _ := m.Update(paneEventMsg{PaneID: pane.ID, Type: "hook.claude.UserPromptSubmit"})
	m = updated.(Model)
	// They alt-tab away. The pane is still ActivePane — nothing moved.
	updated, _ = m.Update(tea.BlurMsg{})
	m = updated.(Model)
	updated, _ = m.Update(paneEventMsg{PaneID: pane.ID, Type: "hook.claude.Stop"})
	got := updated.(Model)

	if len(f.sent) != 1 {
		t.Fatalf("sent %d toasts for a turn that finished while the terminal was in the background, want 1", len(f.sent))
	}
	if !strings.Contains(f.sent[0].Body, "finished") {
		t.Errorf("Body = %q, want the finished-turn headline", f.sent[0].Body)
	}
	if !pane.unseen {
		t.Error("the pane must carry the unseen mark: the user was not looking at the screen")
	}
	if _, ok := got.outstandingToasts[pane.ID]; !ok {
		t.Error("a raised toast must be recorded for the sweep to withdraw later")
	}
}

// Raising the toast is only half the job: it has to still be there a moment
// later. The sweep withdraws any toast whose pane no longer needs attention, so
// an acknowledgement that fires while the user is away would pull the toast
// back out of Action Center within one tick of raising it — and "toast appeared
// and vanished before I could click it" is reported as "no toast".
//
// This is the interaction between the two halves of the fix, which neither
// single-site test can see: the raise is synchronous inside the Stop, the
// withdrawal happens on the NEXT message, and the 1 s size poll guarantees one.
func TestUpdate_ToastSurvivesLaterUpdatesWhileTheUserIsAway(t *testing.T) {
	m, f, pane := wiredToastModel(t)
	m.termFocused = true

	updated, _ := m.Update(paneEventMsg{PaneID: pane.ID, Type: "hook.claude.UserPromptSubmit"})
	m = updated.(Model)
	updated, _ = m.Update(tea.BlurMsg{})
	m = updated.(Model)
	updated, _ = m.Update(paneEventMsg{PaneID: pane.ID, Type: "hook.claude.Stop"})
	m = updated.(Model)
	if len(f.sent) != 1 {
		t.Fatalf("setup: sent %d toasts, want 1", len(f.sent))
	}

	// Time passes while the user reads the news. Every one of these reaches
	// ackFocusedPane and then sweepOutstandingToasts.
	for i := 0; i < 5; i++ {
		updated, _ = m.Update(workSpinnerTickMsg{})
		m = updated.(Model)
	}

	if len(f.withdrawn) != 0 {
		t.Errorf("withdrew %v while the user was still away — the toast must stand until they act", f.withdrawn)
	}

	// Returning to the terminal IS the acknowledgement, and now it must go.
	updated, _ = m.Update(tea.FocusMsg{})
	m = updated.(Model)
	updated, _ = m.Update(workSpinnerTickMsg{})
	m = updated.(Model)

	if len(f.withdrawn) != 1 {
		t.Fatalf("withdrew %d toasts after the user came back, want 1", len(f.withdrawn))
	}
	if _, still := m.outstandingToasts[pane.ID]; still {
		t.Error("a withdrawn toast must not stay on the outstanding list")
	}
}

// The other half of the same rule. ackFocusedPane runs at the top of EVERY
// Update, so if it acknowledges while the terminal is in the background it
// clears the mark within one tick — the toast is raised and then swept away
// before the user can see it, which looks identical to no toast at all.
func TestAckFocusedPane_DoesNotAcknowledgeWhileTerminalIsBlurred(t *testing.T) {
	m, _, pane := wiredToastModel(t)
	m.termFocused = false
	pane.unseen = true

	m.ackFocusedPane()

	if !pane.unseen {
		t.Error("a pane cannot be acknowledged by a window the user is not looking at")
	}

	// Coming back IS the acknowledgement.
	m.termFocused = true
	m.ackFocusedPane()
	if pane.unseen {
		t.Error("returning to the terminal must acknowledge the focused pane")
	}
}

// The sweep must run from Update, not only when a test calls it. Deleting the
// call at the top of Update fails exactly here.
func TestUpdate_SweepWithdrawsAfterTheUserAnswers(t *testing.T) {
	m, f, pane := wiredToastModel(t)

	updated, _ := m.Update(paneEventMsg{PaneID: pane.ID, Type: "hook.claude.PermissionRequest"})
	m = updated.(Model)
	if len(f.sent) != 1 {
		t.Fatalf("setup: sent %d toasts, want 1", len(f.sent))
	}

	// The real clear path for an answered prompt: typing into the pane. It
	// fires no hook at all, which is why withdrawal is a sweep.
	pane.answerBlockedByInput()

	updated, _ = m.Update(workSpinnerTickMsg{})
	got := updated.(Model)

	if len(f.withdrawn) != 1 || f.withdrawn[0] != pane.ID {
		t.Errorf("withdrawn = %v, want [%s] after the prompt was answered", f.withdrawn, pane.ID)
	}
	if len(got.outstandingToasts) != 0 {
		t.Errorf("outstanding = %v, want empty", got.outstandingToasts)
	}
}

// REGRESSION: the sweep must check the state that RAISED each toast, not "any
// attention state".
//
// A pane can be unseen from an earlier finished turn AND then blocked by a
// background subagent's prompt. answerBlockedByInput clears blockedSince only —
// it deliberately never touches unseen — so a sweep gated on
// (blocked || unseen) kept the "Waiting for your input" toast standing in
// Action Center until the user happened to focus that pane. Confirmed by
// review probe before toastKind existed.
func TestSweep_AnsweredPromptWithdrawsEvenWhilePaneStaysUnseen(t *testing.T) {
	m, f, pane := toastModel(t)

	// Finished turn the user never looked at, then a prompt on top of it.
	pane.unseen = true
	pane.blockedSince = time.Now()
	m.raiseAttentionToast(pane, m.projects[0], false, true)
	if len(f.sent) != 1 {
		t.Fatalf("setup: sent %d toasts, want the blocked one", len(f.sent))
	}
	if !strings.Contains(f.sent[0].Body, "Waiting") {
		t.Fatalf("setup: expected the blocked toast, got %q", f.sent[0].Body)
	}

	pane.answerBlockedByInput() // clears blockedSince, leaves unseen set
	if !pane.unseen {
		t.Fatal("setup assumption broken: answerBlockedByInput must not clear unseen")
	}
	m.sweepOutstandingToasts()

	if len(f.withdrawn) != 1 {
		t.Errorf("withdrawn = %v, want the blocked toast gone even though the pane is still unseen",
			f.withdrawn)
	}
}

// ---------------------------------------------------------------- activation

// The activation message must route through jumpToPane rather than setting the
// active pane by hand. That function performs notes-mode teardown before the
// tab moves, the sidebarScroll reset ordering, syncActiveDest and
// notifyTabSwitch for lazily-restored tabs — roughly fifty lines of
// constraints, each of which was a bug first.
func TestActivatePaneMsg_JumpsToTheNamedPane(t *testing.T) {
	target := &PaneModel{ID: toastPaneID2}
	m := Model{
		client: newFakeConn(),
		projects: []*ProjectModel{
			{ID: "proj-a", tabs: []*TabModel{tabWith(&PaneModel{ID: toastPaneID})}},
			{ID: "proj-b", tabs: []*TabModel{tabWith(target)}},
		},
		activeProject: 0,
	}

	updated, _ := m.Update(ActivatePaneMsg{PaneID: target.ID})
	got := updated.(Model)

	if got.activeProject != 1 {
		t.Errorf("activeProject = %d, want 1 — the jump must cross projects", got.activeProject)
	}
	tab := got.activeTabModel()
	if tab == nil || tab.ActivePane != target.ID {
		t.Errorf("active pane = %v, want %s", tab, target.ID)
	}
}

func TestActivatePaneMsg_IgnoresUnknownPane(t *testing.T) {
	m, _, _ := toastModel(t)
	m.client = newFakeConn()
	before := m.activeProject

	updated, _ := m.Update(ActivatePaneMsg{PaneID: "pane-deadbeef"})
	got := updated.(Model)

	if got.activeProject != before {
		t.Errorf("activeProject moved to %d for an unknown pane", got.activeProject)
	}
}

// A malformed id must be refused at this boundary too. It has already passed
// ParseActivateURI and the pipe listener, but this is where focus actually
// moves, and jumpToPane has five other callers that owe it nothing.
func TestActivatePaneMsg_RefusesMalformedPaneID(t *testing.T) {
	m, _, _ := toastModel(t)
	m.client = newFakeConn()

	for _, bad := range []string{"", "../../etc", "pane-BADBEEF", "pane-0a1b2c3d4"} {
		updated, cmd := m.Update(ActivatePaneMsg{PaneID: bad})
		if cmd != nil {
			t.Errorf("ActivatePaneMsg(%q) returned a command, want nil", bad)
		}
		m2 := updated.(Model)
		if m2.activeProject != m.activeProject {
			t.Errorf("ActivatePaneMsg(%q) moved the active project", bad)
		}
	}
}
