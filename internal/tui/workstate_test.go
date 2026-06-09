package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

func TestWorkEventKind(t *testing.T) {
	t.Parallel()
	tests := []struct {
		eventType string
		want      workTransition
	}{
		{"hook.claude.UserPromptSubmit", workStart},
		{"hook.opencode.chat.message", workStart},
		{"hook.claude.Stop", workStop},
		{"hook.claude.SessionEnd", workStop},
		{"hook.opencode.session.idle", workStop},
		{"hook.opencode.session.error", workStop},
		{"process_exit", workAbort},
		{"hook.claude.Notification", workNone},
		{"output_idle", workNone},
		{"", workNone},
	}
	for _, tt := range tests {
		t.Run(tt.eventType, func(t *testing.T) {
			t.Parallel()
			if got := workEventKind(tt.eventType); got != tt.want {
				t.Errorf("workEventKind(%q) = %v, want %v", tt.eventType, got, tt.want)
			}
		})
	}
}

// modelForWorkTest builds a Model with one tab holding one pane (id "p1").
func modelForWorkTest() Model {
	cfg := config.Default()
	tab := NewTabModel("tab-1", "test")
	pane := NewPaneModel("p1", 1024)
	tab.Root = NewLeaf(pane)
	tab.ActivePane = "p1"
	return Model{
		client:        &fakeSender{},
		tabs:          []*TabModel{tab},
		activeTab:     0,
		notifications: NewNotificationCenter(cfg.Notification.SidebarWidth, cfg.Notification.MaxEvents),
	}
}

func TestApplyWorkTransition_StartSetsWorking(t *testing.T) {
	t.Parallel()
	m := modelForWorkTest()
	m.applyWorkTransition("p1", "hook.claude.UserPromptSubmit")
	if !m.tabs[0].Root.Leaves()[0].working {
		t.Fatal("expected pane.working = true after start event")
	}
	if !m.anyPaneWorking() {
		t.Error("anyPaneWorking should be true")
	}
	if !m.tabHasWorkingPane(0) {
		t.Error("tabHasWorkingPane(0) should be true")
	}
}

func TestApplyWorkTransition_StopClearsAndFlashes(t *testing.T) {
	t.Parallel()
	m := modelForWorkTest()
	m.applyWorkTransition("p1", "hook.claude.UserPromptSubmit")
	cmd := m.applyWorkTransition("p1", "hook.claude.Stop")
	if m.tabs[0].Root.Leaves()[0].working {
		t.Error("pane.working should be false after stop")
	}
	if !m.tabFlashing(0) {
		t.Error("tab should be flashing after a genuine stop")
	}
	if cmd == nil {
		t.Error("stop transition should return a flash-expiry tick cmd")
	}
}

func TestApplyWorkTransition_AbortClearsWithoutFlash(t *testing.T) {
	t.Parallel()
	m := modelForWorkTest()
	m.applyWorkTransition("p1", "hook.claude.UserPromptSubmit")
	cmd := m.applyWorkTransition("p1", "process_exit")
	if m.tabs[0].Root.Leaves()[0].working {
		t.Error("pane.working should be false after process_exit")
	}
	if m.tabFlashing(0) {
		t.Error("process_exit must NOT flash the tab green")
	}
	if cmd != nil {
		t.Error("abort transition should not return a flash cmd")
	}
}

func TestApplyWorkTransition_StopWithoutPriorStart_NoFlash(t *testing.T) {
	t.Parallel()
	// A Stop with no in-progress turn (pane was already idle) must not flash.
	m := modelForWorkTest()
	cmd := m.applyWorkTransition("p1", "hook.claude.Stop")
	if m.tabFlashing(0) {
		t.Error("stop on an already-idle pane must not flash")
	}
	if cmd != nil {
		t.Error("no-op stop should not return a flash cmd")
	}
}

func TestApplyWorkTransition_UnknownPane_NoPanic(t *testing.T) {
	t.Parallel()
	m := modelForWorkTest()
	if cmd := m.applyWorkTransition("does-not-exist", "hook.claude.Stop"); cmd != nil {
		t.Error("unknown pane should be a no-op")
	}
}

func TestTabFlashing_Expired(t *testing.T) {
	t.Parallel()
	m := modelForWorkTest()
	m.tabs[0].flashUntil = time.Now().Add(-time.Second) // already past
	if m.tabFlashing(0) {
		t.Error("expired flashUntil should report not flashing")
	}
	m.tabs[0].flashUntil = time.Time{} // zero value
	if m.tabFlashing(0) {
		t.Error("zero flashUntil should report not flashing")
	}
}

func TestUpdate_PaneEvent_StartBeginsTicking(t *testing.T) {
	t.Parallel()
	m := modelForWorkTest()
	start := paneEventMsg(ipc.PaneEventPayload{
		ID:     "e1",
		PaneID: "p1",
		Type:   "hook.claude.UserPromptSubmit",
		Title:  "Working on: x",
	})
	next, _ := m.Update(start)
	nm := next.(Model)
	if !nm.anyPaneWorking() {
		t.Fatal("pane should be working after UserPromptSubmit")
	}
	if !nm.workTickRunning {
		t.Error("work spinner tick loop should have started")
	}
}

func TestUpdate_WorkSpinnerTick_AdvancesAndStops(t *testing.T) {
	t.Parallel()
	m := modelForWorkTest()
	// Pane working → tick should advance the frame and keep running.
	m.tabs[0].Root.Leaves()[0].working = true
	m.workTickRunning = true
	next, cmd := m.Update(workSpinnerTickMsg{})
	nm := next.(Model)
	if nm.workSpinnerFrame != 1 {
		t.Errorf("frame = %d, want 1", nm.workSpinnerFrame)
	}
	if nm.tabs[0].Root.Leaves()[0].workFrame != 1 {
		t.Errorf("pane.workFrame = %d, want 1 (mirrored)", nm.tabs[0].Root.Leaves()[0].workFrame)
	}
	if cmd == nil {
		t.Error("tick should reschedule while a pane is working")
	}

	// No pane working → tick stops.
	m2 := modelForWorkTest()
	m2.workTickRunning = true
	next2, cmd2 := m2.Update(workSpinnerTickMsg{})
	if next2.(Model).workTickRunning {
		t.Error("tick loop should stop when no pane is working")
	}
	if cmd2 != nil {
		t.Error("stopped tick must not reschedule")
	}
}

func TestTabLabel_ShowsSpinnerWhenWorking(t *testing.T) {
	t.Parallel()
	m := modelForWorkTest()
	m.tabs[0].Name = "Build"
	m.workSpinnerFrame = 0 // spinnerFrames[0] == "⠋"

	// Not working: no spinner glyph.
	if got := m.tabLabel(0); strings.ContainsAny(got, "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏") {
		t.Errorf("idle tab label %q should not contain a spinner", got)
	}

	// Working: leading spinner frame present.
	m.tabs[0].Root.Leaves()[0].working = true
	got := m.tabLabel(0)
	if !strings.Contains(got, "⠋") {
		t.Errorf("working tab label %q should contain frame ⠋", got)
	}
}

func TestTabStyle_FlashOverridesInactive(t *testing.T) {
	t.Parallel()
	m := modelForWorkTest()
	// Add a second tab so we can flash a non-active one.
	tab2 := NewTabModel("tab-2", "second")
	tab2.Root = NewLeaf(NewPaneModel("p2", 1024))
	m.tabs = append(m.tabs, tab2)
	m.activeTab = 0

	// lipgloss.Style is uncomparable (contains a slice), so assert on the
	// rendered 256-color background SGR: flash=48;5;28, active=48;5;57.

	// Inactive tab flashing → green flash background.
	m.tabs[1].flashUntil = time.Now().Add(time.Hour)
	if !strings.Contains(m.tabStyle(1).Render("x"), "48;5;28") {
		t.Error("flashing inactive tab should render with green background (48;5;28)")
	}

	// Active tab never flashes, even if flashUntil is set.
	m.tabs[0].flashUntil = time.Now().Add(time.Hour)
	if strings.Contains(m.tabStyle(0).Render("x"), "48;5;28") {
		t.Error("active tab must never use the green flash background")
	}
	if !strings.Contains(m.tabStyle(0).Render("x"), "48;5;57") {
		t.Error("active tab without custom color should use activeTabStyle (48;5;57)")
	}
}
