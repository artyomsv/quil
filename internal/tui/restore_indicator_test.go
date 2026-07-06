package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/artyomsv/quil/internal/config"
)

func TestScreenBlank(t *testing.T) {
	t.Parallel()
	p := NewPaneModel("p", testRingBufSize)
	defer p.Dispose()

	if !p.screenBlank() {
		t.Error("fresh pane should be blank")
	}
	p.AppendOutput([]byte("x"))
	if p.screenBlank() {
		t.Error("pane with content should not be blank")
	}
	// claude-code's boot path: emit bytes, then clear the screen. The pane is
	// blank again — this is exactly the window the indicator must cover.
	p.AppendOutput([]byte("\x1b[2J\x1b[H"))
	if !p.screenBlank() {
		t.Error("cleared screen should be blank again")
	}
}

func TestRestoreSettled(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		content string // appended to the VT ("" = blank screen)
		elapsed time.Duration
		want    bool
	}{
		{"blank before cap stays", "", 5 * time.Second, false},
		{"content after min settles", "hello", 5 * time.Second, true},
		{"content before min waits", "hello", 1 * time.Second, false},
		{"safety cap settles while blank", "", 31 * time.Second, true},
		{"at safety cap settles", "", 30 * time.Second, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := NewPaneModel("p", testRingBufSize)
			defer p.Dispose()
			if tc.content != "" {
				p.AppendOutput([]byte(tc.content))
			}
			p.resumeStart = time.Now().Add(-tc.elapsed)
			if got := p.restoreSettled(); got != tc.want {
				t.Errorf("restoreSettled() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestShowRestoreIndicator(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		resuming  bool
		preparing bool
		pending   bool
		content   string
		want      bool
	}{
		{"resuming blank", true, false, false, "", true},
		{"preparing blank", false, true, false, "", true},
		{"pending blank", false, false, true, "", true},
		{"resuming with content", true, false, false, "hi", false},
		{"preparing with content", false, true, false, "hi", false},
		{"pending with content", false, false, true, "hi", false},
		{"idle blank", false, false, false, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := NewPaneModel("p", testRingBufSize)
			defer p.Dispose()
			p.resuming, p.preparing, p.Pending = tc.resuming, tc.preparing, tc.pending
			if tc.content != "" {
				p.AppendOutput([]byte(tc.content))
			}
			if got := p.showRestoreIndicator(); got != tc.want {
				t.Errorf("showRestoreIndicator() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSyncPaneMeta_PropagatesPending(t *testing.T) {
	t.Parallel()
	p := NewPaneModel("p", testRingBufSize)
	defer p.Dispose()
	p.Pending = true
	syncPaneMeta(p, &PaneInfo{ID: "p", Pending: false}, false, 0)
	if p.Pending {
		t.Error("syncPaneMeta should clear Pending when the daemon reports it spawned")
	}
	syncPaneMeta(p, &PaneInfo{ID: "p", Pending: true}, false, 0)
	if !p.Pending {
		t.Error("syncPaneMeta should set Pending when the daemon reports deferred")
	}
}

func TestRenderRestoreIndicator_Checklist(t *testing.T) {
	t.Parallel()
	p := &PaneModel{resuming: true, Type: "claude-code", SessionID: "8f2e1c00deadbeef", Pending: true}
	out := ansi.Strip(p.renderRestoreIndicator(48, 10))

	for _, want := range []string{"session loaded", "history via resume", "resuming claude · 8f2e1c00", "waiting for first output"} {
		if !strings.Contains(out, want) {
			t.Errorf("checklist missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "✓") {
		t.Errorf("checklist missing done marker:\n%s", out)
	}
	if !strings.ContainsAny(out, "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏") {
		t.Errorf("checklist missing active spinner:\n%s", out)
	}
}

func TestRenderRestoreIndicator_FallbackWhenTooSmall(t *testing.T) {
	t.Parallel()
	short := ansi.Strip((&PaneModel{resuming: true, Type: "claude-code"}).renderRestoreIndicator(48, 4))
	if !strings.Contains(short, "Rebuilding session") {
		t.Errorf("short pane should fall back to compact label:\n%s", short)
	}
	// 14 cols is narrower than the widest row ("waiting for first output"),
	// so the checklist can't fit → compact fallback.
	narrow := ansi.Strip((&PaneModel{preparing: true, Type: "terminal"}).renderRestoreIndicator(14, 10))
	if !strings.Contains(narrow, "Building new pane") {
		t.Errorf("narrow pane should fall back to compact label:\n%s", narrow)
	}
}

func TestRestoreContext_FallsBackToCWDBasename(t *testing.T) {
	t.Parallel()
	p := &PaneModel{Type: "ssh", CWD: "/home/user/prod-eu-1"}
	if got := p.restoreContext(); got != "ssh · prod-eu-1" {
		t.Errorf("restoreContext() = %q, want %q", got, "ssh · prod-eu-1")
	}
	empty := &PaneModel{}
	if got := empty.restoreContext(); got != "terminal" {
		t.Errorf("restoreContext() = %q, want %q", got, "terminal")
	}
}

func TestPaneView_ShowsIndicatorWhileResuming(t *testing.T) {
	t.Parallel()
	p := NewPaneModel("p-resume", testRingBufSize)
	defer p.Dispose()
	p.Width, p.Height = 40, 12
	p.Type, p.Name = "claude-code", "ai-01"
	p.resuming = true // fresh pane → blank screen
	if !strings.Contains(p.View(), "waiting for first output") {
		t.Errorf("resuming pane View() should show the indicator:\n%s", p.View())
	}
}

func TestPaneView_NoIndicatorOnceContentArrives(t *testing.T) {
	t.Parallel()
	p := NewPaneModel("p-content", testRingBufSize)
	defer p.Dispose()
	p.Width, p.Height = 40, 12
	p.resuming = true
	p.AppendOutput([]byte("restored session here"))
	if strings.Contains(p.View(), "waiting for first output") {
		t.Errorf("pane with content must not overlay the indicator:\n%s", p.View())
	}
}

// TestPaneView_IndicatorPersistsThroughBootClear is the regression test for the
// reported bug: claude-code (ghost_buffer=false) emits early bytes and clears
// the screen ~0.5s in, then spends 5-15s resuming before painting. The indicator
// must stay visible through that blank boot gap rather than vanishing on the
// first byte.
func TestPaneView_IndicatorPersistsThroughBootClear(t *testing.T) {
	t.Parallel()
	p := NewPaneModel("p-boot", testRingBufSize)
	defer p.Dispose()
	p.Width, p.Height = 40, 12
	p.Type = "claude-code"
	p.resuming = true
	p.AppendOutput([]byte("\x1b[2J\x1b[H")) // boot clear-screen — still blank
	if !strings.Contains(p.View(), "waiting for first output") {
		t.Errorf("indicator must persist through the blank boot gap:\n%s", p.View())
	}
}

func restoreTickModel(setup func(p *PaneModel)) (Model, *PaneModel) {
	p := NewPaneModel("p1", testRingBufSize)
	p.Width, p.Height = 40, 12
	p.resuming = true
	p.resumeStart = time.Now().Add(-5 * time.Second)
	if setup != nil {
		setup(p)
	}
	tab := NewTabModel("t1", "Test")
	tab.Root = &LayoutNode{Pane: p}
	tab.ActivePane = "p1"
	m := Model{
		cfg:           config.Default(),
		tabs:          []*TabModel{tab},
		activeTab:     0,
		width:         80,
		height:        24,
		notifications: NewNotificationCenter(30, 50),
	}
	m.resizeTabs()
	return m, p
}

func TestSpinnerTick_PersistsWhileBlank(t *testing.T) {
	m, p := restoreTickModel(nil) // 5s elapsed, blank screen
	m.Update(spinnerTickMsg{paneID: "p1", frame: 3})
	if !p.resuming {
		t.Error("resuming should persist while the screen is still blank")
	}
}

func TestSpinnerTick_ClearsAfterContent(t *testing.T) {
	m, p := restoreTickModel(func(p *PaneModel) { p.AppendOutput([]byte("done")) })
	m.Update(spinnerTickMsg{paneID: "p1", frame: 3})
	if p.resuming {
		t.Error("resuming should clear once visible content has painted after min display")
	}
}

func TestSpinnerTick_ClearsAtSafetyCap(t *testing.T) {
	m, p := restoreTickModel(func(p *PaneModel) {
		p.resumeStart = time.Now().Add(-31 * time.Second)
	})
	m.Update(spinnerTickMsg{paneID: "p1", frame: 3})
	if p.resuming {
		t.Error("resuming should clear at the safety cap even while blank")
	}
}

func TestSpinnerTick_KeepsRunningFlagWhileAnimating(t *testing.T) {
	// blank + resuming + 5s elapsed → chain continues, flag stays set.
	m, p := restoreTickModel(func(p *PaneModel) { p.spinnerTickRunning = true })
	m.Update(spinnerTickMsg{paneID: "p1", frame: 3})
	if !p.spinnerTickRunning {
		t.Error("spinnerTickRunning should stay set while the chain continues")
	}
}

func TestSpinnerTick_ClearsRunningFlagOnStop(t *testing.T) {
	// content painted after min display → chain stops, flag cleared so a future
	// re-arm can start a fresh single chain (no stacking / double-speed).
	m, p := restoreTickModel(func(p *PaneModel) {
		p.AppendOutput([]byte("done"))
		p.spinnerTickRunning = true
	})
	m.Update(spinnerTickMsg{paneID: "p1", frame: 3})
	if p.spinnerTickRunning {
		t.Error("spinnerTickRunning should be cleared when the chain stops")
	}
}

func TestSpinnerTick_PreparingPersistsAndClears(t *testing.T) {
	// preparing (new pane) follows the same lifecycle as resuming.
	m, p := restoreTickModel(func(p *PaneModel) {
		p.resuming = false
		p.preparing = true
	})
	m.Update(spinnerTickMsg{paneID: "p1", frame: 3})
	if !p.preparing {
		t.Error("preparing should persist while the screen is still blank")
	}
	p.AppendOutput([]byte("ready"))
	m.Update(spinnerTickMsg{paneID: "p1", frame: 4})
	if p.preparing {
		t.Error("preparing should clear once visible content has painted after min display")
	}
}

func TestSyncPaneMeta_PropagatesSessionAndHistory(t *testing.T) {
	t.Parallel()
	p := NewPaneModel("p", testRingBufSize)
	defer p.Dispose()
	syncPaneMeta(p, &PaneInfo{ID: "p", Type: "claude-code", SessionID: "abc123", HistoryLines: 42}, false, 0)
	if p.SessionID != "abc123" {
		t.Errorf("SessionID = %q, want abc123", p.SessionID)
	}
	if p.HistoryLines != 42 {
		t.Errorf("HistoryLines = %d, want 42", p.HistoryLines)
	}
}

func TestResumeLabel(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, typ, sid, want string
	}{
		{"claude with id", "claude-code", "8f2e1c00deadbeef", "resuming claude · 8f2e1c00"},
		{"claude no id", "claude-code", "", "resuming claude"},
		{"opencode with id", "opencode", "abcdef0123", "resuming opencode · abcdef01"},
		{"terminal", "terminal", "", "restarting shell"},
		{"empty type", "", "", "restarting shell"},
		{"ssh ignores id", "ssh", "ignored", "reconnecting ssh"},
		{"stripe", "stripe", "", "restarting stripe"},
		{"unknown", "weird", "", "starting weird"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resumeLabel(tc.typ, tc.sid); got != tc.want {
				t.Errorf("resumeLabel(%q,%q) = %q, want %q", tc.typ, tc.sid, got, tc.want)
			}
		})
	}
}

func TestRestoreSteps(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		pane *PaneModel
		want []restoreStep
	}{
		{"claude deferred restores via resume",
			&PaneModel{Type: "claude-code", SessionID: "8f2e1c00xx", HistoryLines: 0, Pending: true},
			[]restoreStep{{"session loaded", stepDone}, {"history via resume", stepDone}, {"resuming claude · 8f2e1c00", stepActive}, {"waiting for first output", stepPending}}},
		{"new claude pane has no session to resume",
			&PaneModel{Type: "claude-code", SessionID: "", HistoryLines: 0, Pending: false},
			[]restoreStep{{"session loaded", stepDone}, {"no saved history", stepNone}, {"resuming claude", stepDone}, {"waiting for first output", stepActive}}},
		{"opencode restores via resume",
			&PaneModel{Type: "opencode", SessionID: "abcdef0123", HistoryLines: 0, Pending: false},
			[]restoreStep{{"session loaded", stepDone}, {"history via resume", stepDone}, {"resuming opencode · abcdef01", stepDone}, {"waiting for first output", stepActive}}},
		{"terminal spawned with history",
			&PaneModel{Type: "terminal", HistoryLines: 412, Pending: false},
			[]restoreStep{{"session loaded", stepDone}, {"history restored (412 ln)", stepDone}, {"restarting shell", stepDone}, {"waiting for first output", stepActive}}},
		{"zero history",
			&PaneModel{Type: "terminal", HistoryLines: 0, Pending: false},
			[]restoreStep{{"session loaded", stepDone}, {"no saved history", stepNone}, {"restarting shell", stepDone}, {"waiting for first output", stepActive}}},
		{"negative history treated as none",
			&PaneModel{Type: "terminal", HistoryLines: -1, Pending: false},
			[]restoreStep{{"session loaded", stepDone}, {"no saved history", stepNone}, {"restarting shell", stepDone}, {"waiting for first output", stepActive}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			steps := tc.pane.restoreSteps()
			if len(steps) != len(tc.want) {
				t.Fatalf("got %d steps, want %d: %+v", len(steps), len(tc.want), steps)
			}
			active := 0
			for i := range tc.want {
				if steps[i] != tc.want[i] {
					t.Errorf("step %d = %+v, want %+v", i, steps[i], tc.want[i])
				}
				if steps[i].state == stepActive {
					active++
				}
			}
			if active != 1 {
				t.Errorf("want exactly one active row, got %d: %+v", active, steps)
			}
		})
	}
}
