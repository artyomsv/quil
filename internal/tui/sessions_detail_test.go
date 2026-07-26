package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/plugin"
)

const (
	detailRowA = "2db05609-f1d5-4576-b2a1-9e0c3a7f1188"
	detailRowB = "9aa1c3e7-2b40-4d19-8f6e-5c7d0e1a2b34"
)

func detailKeyModel(t *testing.T) (Model, *plugin.PanePlugin) {
	t.Helper()
	m, p := sessionKeyModel(t,
		sessionRow(detailRowA, "first session", ""),
		sessionRow(detailRowB, "second session", ""),
	)
	m.height = 60
	return m, p
}

// pressInfo sends the panel's toggle key through the real dispatcher, so a
// routing regression (the session case dropped from the kind switch) fails here.
func pressInfo(t *testing.T, m Model) (Model, tea.Cmd) {
	t.Helper()
	next, cmd := m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: 'i', Text: "i"})
	return next.(Model), cmd
}

// TestSessionDetail_ITogglesAndFetches covers the entry point: i opens the panel
// for the highlighted row and issues the read, and i again closes it.
func TestSessionDetail_ITogglesAndFetches(t *testing.T) {
	m, _ := detailKeyModel(t)
	m.sessionCursor = 1

	m, cmd := pressInfo(t, m)
	if !m.sessionDetail.open {
		t.Fatal("i must open the detail panel")
	}
	if m.sessionDetail.id != detailRowA {
		t.Errorf("sessionDetailID = %q, want the highlighted row's id", m.sessionDetail.id)
	}
	if m.sessionDetail.state != sessionScanning {
		t.Errorf("state = %v, want scanning", m.sessionDetail.state)
	}
	if cmd == nil {
		t.Error("want a command issuing the read")
	}

	m, _ = pressInfo(t, m)
	if m.sessionDetail.open {
		t.Error("a second i must close the panel")
	}
	if m.sessionDetail.id != "" || m.sessionDetail.state != sessionScanIdle {
		t.Error("closing must clear the panel's state, so reopening re-reads a transcript that has grown")
	}
}

// TestSessionDetail_NewSessionRowNeedsNoRead: row 0 has no transcript, so the
// panel explains what a fresh session is instead of issuing a doomed read.
func TestSessionDetail_NewSessionRowNeedsNoRead(t *testing.T) {
	m, _ := detailKeyModel(t)
	m.sessionCursor = 0

	m, cmd := pressInfo(t, m)
	if !m.sessionDetail.open {
		t.Fatal("i must open the panel even on the New session row")
	}
	if cmd != nil {
		t.Error("the New session row must not issue a transcript read")
	}
	if m.sessionDetail.id != "" {
		t.Errorf("sessionDetailID = %q, want empty", m.sessionDetail.id)
	}
	if got := m.renderSetupSessionField(true); !strings.Contains(got, "New session") {
		t.Errorf("panel must say what the row means:\n%s", got)
	}
}

// TestSessionDetail_MovingRefetches makes the panel a mode you browse in: the
// arrow keys move the cursor AND re-read, rather than leaving the panel
// describing a row the cursor has left.
func TestSessionDetail_MovingRefetches(t *testing.T) {
	m, _ := detailKeyModel(t)
	m.sessionCursor = 1
	m, _ = pressInfo(t, m)
	m.sessionDetail.state = sessionScanReady // pretend the first read landed

	next, cmd := m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: tea.KeyDown})
	m = next.(Model)
	if m.sessionCursor != 2 {
		t.Fatalf("cursor = %d, want 2", m.sessionCursor)
	}
	if m.sessionDetail.id != detailRowB {
		t.Errorf("sessionDetailID = %q, want the newly highlighted row", m.sessionDetail.id)
	}
	if m.sessionDetail.state != sessionScanning || cmd == nil {
		t.Error("moving with the panel open must issue a read for the new row")
	}
}

// TestSessionDetail_MoveWithPanelClosedIssuesNothing: browsing the list must not
// read a transcript per keypress.
func TestSessionDetail_MoveWithPanelClosedIssuesNothing(t *testing.T) {
	m, _ := detailKeyModel(t)
	m.sessionCursor = 1

	next, cmd := m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: tea.KeyDown})
	m = next.(Model)
	if cmd != nil {
		t.Error("moving with the panel closed must not issue a read")
	}
	if m.sessionDetail.id != "" {
		t.Error("no detail should be tracked while the panel is closed")
	}
}

// TestSessionDetail_EscClosesPanelNotDialog is the routing case: Esc is handled
// for the whole setup dialog before any field sees it, so without an explicit
// branch the first Esc would abandon pane creation entirely.
func TestSessionDetail_EscClosesPanelNotDialog(t *testing.T) {
	m, _ := detailKeyModel(t)
	m.sessionCursor = 1
	m, _ = pressInfo(t, m)

	next, _ := m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = next.(Model)
	if m.sessionDetail.open {
		t.Error("Esc must close the detail panel")
	}
	if m.dialog != dialogCreatePaneSetup {
		t.Errorf("dialog = %v, want to stay in the setup dialog", m.dialog)
	}

	// A second Esc, with the panel closed, leaves the dialog as it always did.
	next, _ = m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if next.(Model).dialog == dialogCreatePaneSetup {
		t.Error("Esc with the panel closed must still leave the setup dialog")
	}
}

func TestApplyClaudeSessionDetail_DropsStaleResponses(t *testing.T) {
	m, _ := detailKeyModel(t)
	m.sessionCursor = 1
	m, _ = pressInfo(t, m)

	tests := []struct {
		name string
		resp ipc.ClaudeSessionDetailRespPayload
	}{
		{"another session", ipc.ClaudeSessionDetailRespPayload{
			CWD: "/proj", SessionID: detailRowB, UserPrompts: 9}},
		{"another directory", ipc.ClaudeSessionDetailRespPayload{
			CWD: "/elsewhere", SessionID: detailRowA, UserPrompts: 9}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := m.applyClaudeSessionDetail(tt.resp)
			if got.sessionDetail.state != sessionScanning {
				t.Errorf("state = %v, want the panel still waiting for its own answer", got.sessionDetail.state)
			}
			if got.sessionDetail.data.UserPrompts != 0 {
				t.Error("a stale response overwrote the panel")
			}
		})
	}

	fresh := ipc.ClaudeSessionDetailRespPayload{CWD: "/proj", SessionID: detailRowA, UserPrompts: 47}
	got := m.applyClaudeSessionDetail(fresh)
	if got.sessionDetail.state != sessionScanReady || got.sessionDetail.data.UserPrompts != 47 {
		t.Errorf("the matching response was not applied: state=%v prompts=%d",
			got.sessionDetail.state, got.sessionDetail.data.UserPrompts)
	}
}

func TestApplyClaudeSessionDetail_ErrorSetsFailed(t *testing.T) {
	m, _ := detailKeyModel(t)
	m.sessionCursor = 1
	m, _ = pressInfo(t, m)

	got := m.applyClaudeSessionDetail(ipc.ClaudeSessionDetailRespPayload{
		CWD: "/proj", SessionID: detailRowA, Error: "could not read this session's transcript",
	})
	if got.sessionDetail.state != sessionScanFailed {
		t.Errorf("state = %v, want failed", got.sessionDetail.state)
	}
	if rendered := got.renderSetupSessionField(true); !strings.Contains(rendered, "could not read") {
		t.Errorf("the panel must show the reason:\n%s", rendered)
	}
}

// TestApplyClaudeSessionDetail_SanitizesPrompts: prompts are user-authored text
// that reaches the screen without passing through the VT emulator.
func TestApplyClaudeSessionDetail_SanitizesPrompts(t *testing.T) {
	m, _ := detailKeyModel(t)
	m.sessionCursor = 1
	m, _ = pressInfo(t, m)

	got := m.applyClaudeSessionDetail(ipc.ClaudeSessionDetailRespPayload{
		CWD: "/proj", SessionID: detailRowA,
		FirstPrompt: "red \x1b[31mtext",
		LastPrompt:  "safe\u202etxet",
	})
	if strings.ContainsRune(got.sessionDetail.data.FirstPrompt, '\x1b') {
		t.Errorf("escape survived sanitization: %q", got.sessionDetail.data.FirstPrompt)
	}
	if strings.ContainsRune(got.sessionDetail.data.LastPrompt, '\u202e') {
		t.Errorf("bidi override survived sanitization: %q", got.sessionDetail.data.LastPrompt)
	}
}

// TestSessionDetail_ClosedWhenDirectoryChanges: the panel describes a row the
// rescan is about to replace, and its response is matched on the CWD that just
// changed — it could never resolve.
func TestSessionDetail_ClosedWhenDirectoryChanges(t *testing.T) {
	m, _ := detailKeyModel(t)
	m.sessionCursor = 1
	m, _ = pressInfo(t, m)

	m.cwdBrowseDir = "/other"
	m.ensureSessionScan()
	if m.sessionDetail.open || m.sessionDetail.id != "" {
		t.Error("changing the directory must close the detail panel")
	}
}

// readyDetailModel is the panel in its loaded state, with prompts long enough
// to exercise wrapping and truncation.
func readyDetailModel(t *testing.T) Model {
	t.Helper()
	m, _ := detailKeyModel(t)
	m.sessionCursor = 1
	m, _ = pressInfo(t, m)
	m.sessionDetail.state = sessionScanReady
	m.sessionDetail.data = ipc.ClaudeSessionDetailRespPayload{
		CWD: "/proj", SessionID: detailRowA,
		FirstPrompt: strings.Repeat("a long first prompt that must wrap and then be cut ", 12),
		LastPrompt:  strings.Repeat("a long last prompt that must wrap and then be cut ", 12),
		UserPrompts: 47, SizeBytes: 1800000,
		StartedMs:  time.Now().Add(-48 * time.Hour).UnixMilli(),
		ModifiedMs: time.Now().UnixMilli(),
	}
	return m
}

// TestRenderSessionDetail_FitsTheBox holds the panel to the same width contract
// the list rows have — it is the widest content in the dialog while open.
func TestRenderSessionDetail_FitsTheBox(t *testing.T) {
	m := readyDetailModel(t)

	out := m.renderSetupSessionField(true)
	for _, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > m.setupTextWidth() {
			t.Errorf("panel line width %d exceeds the text area %d:\n%q", w, m.setupTextWidth(), line)
		}
	}
	if !strings.Contains(out, "47 typed") {
		t.Errorf("panel must show the prompt count:\n%s", out)
	}
}

// TestRenderSessionDetail_FitsTheListsHeight: the panel replaces the list, so
// it must not push the dialog past the height a full list already needs.
//
// Compared against a FULL list, not whatever the test fixture happens to hold:
// a two-session list is six rows, and requiring the panel to fit that would
// leave it no room for prompt text. The guarantee that matters is that the
// dialog's maximum height is unchanged.
func TestRenderSessionDetail_FitsTheListsHeight(t *testing.T) {
	rows := make([]ipc.ClaudeSessionInfo, 0, 40)
	for i := 0; i < 40; i++ {
		rows = append(rows, sessionRow(detailRowA, "a session", ""))
	}
	full, _ := sessionKeyModel(t, rows...)
	full.height = 60
	if full.sessionRowCount() <= full.sessionVisibleRows() {
		t.Fatalf("fixture must fill the window: %d rows, %d visible",
			full.sessionRowCount(), full.sessionVisibleRows())
	}
	listRows := strings.Count(full.renderSetupSessionField(true), "\n")

	panelRows := strings.Count(readyDetailModel(t).renderSetupSessionField(true), "\n")
	if panelRows > listRows {
		t.Errorf("panel is %d rows against a full list's %d — opening it grows the dialog", panelRows, listRows)
	}
}

// TestRenderSessionDetail_ShowsBothPrompts: the last prompt is the reason the
// panel exists (it appears nowhere else), and the first is capped rather than
// dropped because it is already the row's title.
func TestRenderSessionDetail_ShowsBothPrompts(t *testing.T) {
	m := readyDetailModel(t)
	m.sessionDetail.data.FirstPrompt = "the very first thing I asked"
	m.sessionDetail.data.LastPrompt = "where I actually left off"

	out := m.renderSetupSessionField(true)
	if !strings.Contains(out, "the very first thing I asked") {
		t.Errorf("first prompt missing:\n%s", out)
	}
	if !strings.Contains(out, "where I actually left off") {
		t.Errorf("last prompt missing:\n%s", out)
	}
}

func TestRenderSessionDetail_NoPromptsRecorded(t *testing.T) {
	m := readyDetailModel(t)
	m.sessionDetail.data.FirstPrompt = ""
	m.sessionDetail.data.LastPrompt = ""

	if out := m.renderSetupSessionField(true); !strings.Contains(out, "no typed prompt") {
		t.Errorf("a transcript with no typed prompt needs an honest empty state:\n%s", out)
	}
}

func TestWrapToLines(t *testing.T) {
	if got := wrapToLines("", 20, 3); got != nil {
		t.Errorf("empty text = %v, want nil", got)
	}
	if got := wrapToLines("text", 20, 0); got != nil {
		t.Errorf("zero rows = %v, want nil", got)
	}

	got := wrapToLines(strings.Repeat("word ", 100), 20, 3)
	if len(got) != 3 {
		t.Fatalf("got %d lines, want 3", len(got))
	}
	if !strings.HasSuffix(got[2], "…") {
		t.Errorf("a cut block must end with an ellipsis, got %q", got[2])
	}
	for _, ln := range got {
		if w := lipgloss.Width(ln); w > 20 {
			t.Errorf("line %q is %d cells, over the 20 requested", ln, w)
		}
		if strings.HasSuffix(ln, " ") {
			t.Errorf("line %q keeps lipgloss's padding, which would push rows to the wrap limit", ln)
		}
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{0, "—"},
		{-1, "—"},
		{512, "512 B"},
		{1500, "2 KB"},
		{1_800_000, "1.8 MB"},
		{2_500_000_000, "2.5 GB"},
	}
	for _, tt := range tests {
		if got := formatBytes(tt.in); got != tt.want {
			t.Errorf("formatBytes(%d) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestAbsoluteTime_ZeroIsDash(t *testing.T) {
	if got := absoluteTime(0); got != "—" {
		t.Errorf("absoluteTime(0) = %q, want an em dash", got)
	}
	if got := absoluteTime(time.Now().UnixMilli()); got == "—" {
		t.Error("a real timestamp must render")
	}
}
