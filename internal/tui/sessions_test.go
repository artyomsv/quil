package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/plugin"
)

// claudeLikePlugin mirrors the shipped claude-code plugin's setup-dialog shape.
func claudeLikePlugin() *plugin.PanePlugin {
	return &plugin.PanePlugin{
		Name: "claude-code",
		Command: plugin.CommandConfig{
			PromptsCWD: true,
			Sessions:   "claude",
			Toggles:    []plugin.Toggle{{Name: "skip"}, {Name: "auto"}},
		},
	}
}

// TestSetupFieldKind_SessionAfterCWD pins the field order. The session listing
// is scoped to the directory chosen above it, so it must come after the CWD
// field — a session field the user reaches first would have nothing to list.
func TestSetupFieldKind_SessionAfterCWD(t *testing.T) {
	m := Model{}
	p := claudeLikePlugin()

	if got, want := m.setupFieldCount(p), 5; got != want {
		t.Fatalf("setupFieldCount = %d, want %d (cwd, session, 2 toggles, continue)", got, want)
	}
	wantKinds := []string{"cwd", "session", "toggle", "toggle", "continue"}
	for i, want := range wantKinds {
		if kind, _ := m.setupFieldKind(p, i); kind != want {
			t.Errorf("kind at index %d = %q, want %q", i, kind, want)
		}
	}
}

func TestSetupFieldKind_NoSessionFieldWhenNotDeclared(t *testing.T) {
	m := Model{}
	p := &plugin.PanePlugin{Command: plugin.CommandConfig{PromptsCWD: true}}

	if got, want := m.setupFieldCount(p), 2; got != want {
		t.Fatalf("setupFieldCount = %d, want %d", got, want)
	}
	for i, want := range []string{"cwd", "continue"} {
		if kind, _ := m.setupFieldKind(p, i); kind != want {
			t.Errorf("kind at index %d = %q, want %q", i, kind, want)
		}
	}
}

func sessionRow(id, title string, inUse string) ipc.ClaudeSessionInfo {
	return ipc.ClaudeSessionInfo{
		ID:          id,
		Title:       title,
		ModifiedMs:  time.Now().Add(-2 * time.Hour).UnixMilli(),
		InUsePaneID: inUse,
	}
}

func modelWithSessions(rows ...ipc.ClaudeSessionInfo) Model {
	return Model{
		sessionRows:    rows,
		sessionScanCWD: "/proj",
		cwdBrowseDir:   "/proj",
		sessionState:   sessionScanReady,
	}
}

// registryWithSessionsPlugin builds a registry holding one plugin that opts
// into the session picker. Render paths resolve the active plugin through the
// registry (setupDialogWidth), so render tests need a real one.
func registryWithSessionsPlugin(t *testing.T) *plugin.Registry {
	t.Helper()
	dir := t.TempDir()
	content := `[plugin]
name = "ai"

[command]
cmd = "ai"
prompts_cwd = true
sessions = "claude"
`
	if err := os.WriteFile(filepath.Join(dir, "ai.toml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write test toml: %v", err)
	}
	r := plugin.NewRegistry()
	if err := r.LoadFromDir(dir); err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}
	r.Get("ai").Available = true
	return r
}

// renderableSessionModel is modelWithSessions plus everything the render path
// needs (registry, selected plugin, width).
func renderableSessionModel(t *testing.T, rows ...ipc.ClaudeSessionInfo) Model {
	t.Helper()
	m := modelWithSessions(rows...)
	m.dialog = dialogCreatePaneSetup
	m.pluginRegistry = registryWithSessionsPlugin(t)
	m.selectedPlugin = "ai"
	m.width = 100
	return m
}

// TestSessionRowSelectable_BlocksInUse is the corruption guard: two claude
// processes attached to one transcript overwrite each other's history.
func TestSessionRowSelectable_BlocksInUse(t *testing.T) {
	m := modelWithSessions(
		sessionRow("free", "a free session", ""),
		sessionRow("busy", "already open", "pane-0000000a"),
	)

	if !m.sessionRowSelectable(0) {
		t.Error("row 0 (New session) must always be selectable")
	}
	if !m.sessionRowSelectable(1) {
		t.Error("a free session must be selectable")
	}
	if m.sessionRowSelectable(2) {
		t.Error("a session held by a live pane must NOT be selectable")
	}
}

func TestCommitSessionSelection(t *testing.T) {
	t.Run("row 0 clears the resume target", func(t *testing.T) {
		m := modelWithSessions(sessionRow("free", "x", ""))
		m.selectedSessionID = "stale"
		m.sessionCursor = 0
		if !m.commitSessionSelection() {
			t.Fatal("committing New session must succeed")
		}
		if m.selectedSessionID != "" {
			t.Errorf("selectedSessionID = %q, want empty for New session", m.selectedSessionID)
		}
	})

	t.Run("free row commits its id", func(t *testing.T) {
		m := modelWithSessions(sessionRow("free", "x", ""))
		m.sessionCursor = 1
		if !m.commitSessionSelection() {
			t.Fatal("committing a free session must succeed")
		}
		if m.selectedSessionID != "free" {
			t.Errorf("selectedSessionID = %q, want \"free\"", m.selectedSessionID)
		}
	})

	t.Run("blocked row is refused and leaves the previous choice", func(t *testing.T) {
		m := modelWithSessions(
			sessionRow("free", "x", ""),
			sessionRow("busy", "y", "pane-0000000a"),
		)
		m.selectedSessionID = "free"
		m.sessionCursor = 2
		if m.commitSessionSelection() {
			t.Fatal("committing an in-use session must be refused")
		}
		if m.selectedSessionID != "free" {
			t.Errorf("selectedSessionID = %q, want the previous choice preserved", m.selectedSessionID)
		}
	})
}

// TestApplyClaudeSessions_DropsStale guards the echoed-CWD staleness check: a
// listing for a directory the user has already navigated away from must not
// replace the rows they are looking at.
func TestApplyClaudeSessions_DropsStale(t *testing.T) {
	m := Model{sessionScanCWD: "/proj-b", sessionState: sessionScanning}

	m = m.applyClaudeSessions(ipc.ClaudeSessionsRespPayload{
		CWD:      "/proj-a", // the previous directory's scan, arriving late
		Sessions: []ipc.ClaudeSessionInfo{sessionRow("old", "from project A", "")},
	})

	if len(m.sessionRows) != 0 {
		t.Errorf("stale response populated %d rows, want 0", len(m.sessionRows))
	}
	if m.sessionState != sessionScanning {
		t.Errorf("state = %v, want still scanning (the in-flight request is unanswered)", m.sessionState)
	}
}

func TestApplyClaudeSessions_AcceptsCurrentAndClearsTimeout(t *testing.T) {
	m := Model{sessionScanCWD: "/proj", sessionState: sessionScanTimedOut}

	m = m.applyClaudeSessions(ipc.ClaudeSessionsRespPayload{
		CWD:      "/proj",
		Sessions: []ipc.ClaudeSessionInfo{sessionRow("s1", "hello", "")},
	})

	if m.sessionState != sessionScanReady {
		t.Errorf("state = %v, want ready — a late-but-current response must clear the timed-out state", m.sessionState)
	}
	if len(m.sessionRows) != 1 {
		t.Errorf("rows = %d, want 1", len(m.sessionRows))
	}
}

func TestApplyClaudeSessions_ErrorPath(t *testing.T) {
	m := Model{sessionScanCWD: "/proj", sessionState: sessionScanning}

	m = m.applyClaudeSessions(ipc.ClaudeSessionsRespPayload{
		CWD:   "/proj",
		Error: "could not read session history",
	})

	if m.sessionState != sessionScanFailed {
		t.Errorf("state = %v, want failed", m.sessionState)
	}
	if m.sessionError == "" {
		t.Error("sessionError must carry the daemon's message for the render branch")
	}
}

// TestSubmitSetupDialog_DropsResumeAfterCWDChange covers the path the field's
// own focus handler cannot: pick a session, Shift+Tab back to the browser, move
// to another project, then press Continue without re-focusing the session
// field. Sending the stale id would attach the new pane to a conversation from
// a different codebase.
func TestSubmitSetupDialog_DropsResumeAfterCWDChange(t *testing.T) {
	p := claudeLikePlugin()
	m := Model{
		selectedSessionID: "sess-from-project-a",
		sessionScanCWD:    "/proj-a",
		cwdBrowseDir:      "/proj-b", // user moved the browser after choosing
		toggleStates:      make([]bool, len(p.Command.Toggles)),
	}

	next, _ := m.submitSetupDialog(p)
	got := next.(Model)
	if got.selectedSessionID != "" {
		t.Errorf("selectedSessionID = %q, want it dropped when the CWD no longer matches the listing", got.selectedSessionID)
	}
	if got.selectedCWD != "/proj-b" {
		t.Errorf("selectedCWD = %q, want /proj-b", got.selectedCWD)
	}
}

func TestSubmitSetupDialog_KeepsResumeForMatchingCWD(t *testing.T) {
	p := claudeLikePlugin()
	m := Model{
		selectedSessionID: "sess-1",
		sessionScanCWD:    "/proj",
		cwdBrowseDir:      "/proj",
		toggleStates:      make([]bool, len(p.Command.Toggles)),
	}

	next, _ := m.submitSetupDialog(p)
	if got := next.(Model).selectedSessionID; got != "sess-1" {
		t.Errorf("selectedSessionID = %q, want it preserved when the CWD matches", got)
	}
}

func TestResetSessionSelection(t *testing.T) {
	m := modelWithSessions(sessionRow("s1", "x", ""))
	m.selectedSessionID = "s1"
	m.sessionCursor = 1
	m.sessionScroll = 3

	m.resetSessionSelection()

	if m.selectedSessionID != "" || m.sessionRows != nil || m.sessionScanCWD != "" ||
		m.sessionCursor != 0 || m.sessionScroll != 0 || m.sessionState != sessionScanIdle {
		t.Errorf("resetSessionSelection left state behind: %+v", struct {
			id     string
			rows   int
			cwd    string
			cursor int
			scroll int
			state  sessionScanState
		}{m.selectedSessionID, len(m.sessionRows), m.sessionScanCWD, m.sessionCursor, m.sessionScroll, m.sessionState})
	}
}

func TestEnsureSessionScan_SkipsWhenRowsAreCurrent(t *testing.T) {
	m := modelWithSessions(sessionRow("s1", "x", ""))
	if cmd := m.ensureSessionScan(); cmd != nil {
		t.Error("re-focusing the field with rows already loaded for this CWD must not rescan")
	}
}

func TestEnsureSessionScan_RescanClearsSelection(t *testing.T) {
	m := modelWithSessions(sessionRow("s1", "x", ""))
	m.selectedSessionID = "s1"
	m.cwdBrowseDir = "/other-proj" // browser moved

	cmd := m.ensureSessionScan()

	if cmd == nil {
		t.Fatal("a different CWD must trigger a rescan")
	}
	if m.selectedSessionID != "" {
		t.Errorf("selectedSessionID = %q, want cleared — the session belongs to the previous project", m.selectedSessionID)
	}
	if m.sessionState != sessionScanning {
		t.Errorf("state = %v, want scanning", m.sessionState)
	}
}

// TestHandleCreatePaneSetupKey_TabToSessionFieldScans covers the one wiring
// that makes the whole feature reachable: tabbing onto the Session field has to
// fire the scan. Every other session test drives ensureSessionScan directly, so
// nothing else would notice if moveSetupCursor stopped propagating the mutation
// (a silent failure — the field would sit on its initial state forever).
func TestHandleCreatePaneSetupKey_TabToSessionFieldScans(t *testing.T) {
	m := renderableSessionModel(t)
	m.sessionScanCWD = "" // nothing scanned yet
	m.sessionState = sessionScanIdle
	m.setupFieldCursor = 0 // on the CWD field

	next, cmd := m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: tea.KeyTab})
	got := next.(Model)

	if kind, _ := got.setupFieldKind(got.pluginRegistry.Get("ai"), got.setupFieldCursor); kind != "session" {
		t.Fatalf("after Tab the focused field is %q, want \"session\"", kind)
	}
	if got.sessionState != sessionScanning {
		t.Errorf("sessionState = %v, want scanning — the mutation must land on the returned Model", got.sessionState)
	}
	if got.sessionScanCWD != "/proj" {
		t.Errorf("sessionScanCWD = %q, want /proj", got.sessionScanCWD)
	}
	if cmd == nil {
		t.Error("want a command issuing the request")
	}
}

// TestEnsureSessionScan_RetriesAfterFailure: the timed-out message tells the
// user the daemon may not be running, which invites a retry — refusing it would
// leave the field stuck until they navigate away and back.
func TestEnsureSessionScan_RetriesAfterFailure(t *testing.T) {
	for _, state := range []sessionScanState{sessionScanTimedOut, sessionScanFailed} {
		m := modelWithSessions()
		m.sessionState = state
		if cmd := m.ensureSessionScan(); cmd == nil {
			t.Errorf("state %v: re-focusing must retry the scan", state)
		}
		if m.sessionState != sessionScanning {
			t.Errorf("state %v: sessionState = %v, want scanning", state, m.sessionState)
		}
	}
}

func TestEnsureSessionScan_NoCWD_NoRequest(t *testing.T) {
	m := Model{cwdBrowseDir: ""}
	if cmd := m.ensureSessionScan(); cmd != nil {
		t.Error("no directory resolved means nothing to scan against")
	}
}

func TestAdjustSessionScroll(t *testing.T) {
	m := Model{height: 60} // tall enough for the full window
	if got := m.sessionVisibleRows(); got != sessionListVisibleRows {
		t.Fatalf("sessionVisibleRows = %d, want the full %d on a tall terminal", got, sessionListVisibleRows)
	}

	m.sessionCursor = sessionListVisibleRows + 2
	m.adjustSessionScroll()
	if want := m.sessionCursor - sessionListVisibleRows + 1; m.sessionScroll != want {
		t.Errorf("scroll = %d, want %d after moving past the window bottom", m.sessionScroll, want)
	}

	m.sessionCursor = 0
	m.adjustSessionScroll()
	if m.sessionScroll != 0 {
		t.Errorf("scroll = %d, want 0 after returning to the top", m.sessionScroll)
	}
}

// TestSessionVisibleRows_ShrinksOnShortTerminal: renderDialog places the box
// without clipping, so a fixed-height session list on a short terminal pushes
// the toggles and the [Continue] button off-screen. The list is the only part
// that can give, so it shrinks — down to a floor, below which the picker stops
// being usable at all.
func TestSessionVisibleRows_ShrinksOnShortTerminal(t *testing.T) {
	tests := []struct {
		name   string
		height int
		want   int
	}{
		{"tall terminal keeps the full window", 60, sessionListVisibleRows},
		{"exactly enough keeps the full window", 26 + sessionListVisibleRows, sessionListVisibleRows},
		{"short terminal shrinks", 34, 8},
		{"very short clamps to the floor", 20, sessionListMinRows},
		{"unset height clamps to the floor", 0, sessionListMinRows},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := Model{height: tt.height}
			if got := m.sessionVisibleRows(); got != tt.want {
				t.Errorf("sessionVisibleRows(height=%d) = %d, want %d", tt.height, got, tt.want)
			}
		})
	}
}

func TestSessionSummaryLine(t *testing.T) {
	t.Run("no selection reads as New session", func(t *testing.T) {
		m := modelWithSessions(sessionRow("s1", "some prompt", ""))
		if got := m.sessionSummaryLine(); got != "New session" {
			t.Errorf("summary = %q, want \"New session\"", got)
		}
	})

	t.Run("selection shows its row label", func(t *testing.T) {
		m := modelWithSessions(sessionRow("s1", "some prompt", ""))
		m.selectedSessionID = "s1"
		if got := m.sessionSummaryLine(); !strings.Contains(got, "some prompt") {
			t.Errorf("summary = %q, want it to contain the session title", got)
		}
	})
}

func TestSessionRowLabel_TitlelessFallsBackToID(t *testing.T) {
	got := sessionRowLabel(sessionRow("2db05609-f1d5-4576", "", ""))
	if !strings.Contains(got, "2db05609") {
		t.Errorf("label = %q, want the short id when no title was recorded", got)
	}
}

func TestRelativeAge(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name string
		ms   int64
		want string
	}{
		{"zero is unknown", 0, "?"},
		{"seconds", now.Add(-30 * time.Second).UnixMilli(), "now"},
		{"minutes", now.Add(-5 * time.Minute).UnixMilli(), "5m ago"},
		{"hours", now.Add(-3 * time.Hour).UnixMilli(), "3h ago"},
		{"days", now.Add(-50 * time.Hour).UnixMilli(), "2d ago"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := relativeAge(tt.ms); got != tt.want {
				t.Errorf("relativeAge = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestRenderSetupSessionField_CollapsedVsExpanded pins the height contract:
// unfocused the field is one line plus a blank, so a dialog that already
// carries a 12-row browser does not grow for a field most panes never touch.
func TestRenderSetupSessionField_CollapsedVsExpanded(t *testing.T) {
	m := renderableSessionModel(t,
		sessionRow("s1", "first prompt", ""),
		sessionRow("s2", "second prompt", ""),
	)

	collapsed := m.renderSetupSessionField(false)
	if n := strings.Count(collapsed, "\n"); n > 2 {
		t.Errorf("collapsed field rendered %d lines, want at most 2", n)
	}
	if !strings.Contains(collapsed, "New session") {
		t.Errorf("collapsed field must show the current value:\n%s", collapsed)
	}

	expanded := m.renderSetupSessionField(true)
	if !strings.Contains(expanded, "first prompt") || !strings.Contains(expanded, "second prompt") {
		t.Errorf("expanded field must list the sessions:\n%s", expanded)
	}
}

func TestRenderSetupSessionField_StatesRenderDiagnostically(t *testing.T) {
	tests := []struct {
		name   string
		state  sessionScanState
		errMsg string
		want   string
	}{
		{"scanning", sessionScanning, "", "Scanning"},
		{"timed out", sessionScanTimedOut, "", "Timed out"},
		{"failed", sessionScanFailed, "could not read session history", "could not read session history"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := renderableSessionModel(t)
			m.sessionState = tt.state
			m.sessionError = tt.errMsg
			got := m.renderSetupSessionField(true)
			if !strings.Contains(got, tt.want) {
				t.Errorf("render = %q, want it to contain %q", got, tt.want)
			}
		})
	}
}

func TestRenderSetupSessionField_MarksInUseRow(t *testing.T) {
	m := renderableSessionModel(t, sessionRow("busy", "held elsewhere", "pane-0000000a"))
	m.sessionCursor = 1

	got := m.renderSetupSessionField(true)
	if !strings.Contains(got, "open in") {
		t.Errorf("an in-use row must be marked:\n%s", got)
	}
	if !strings.Contains(got, "Already open in another pane") {
		t.Errorf("the footer must explain why the row is blocked while the cursor rests on it:\n%s", got)
	}
}

func TestRenderSetupSessionField_EmptyListing(t *testing.T) {
	m := renderableSessionModel(t)
	got := m.renderSetupSessionField(true)
	if !strings.Contains(got, "no earlier sessions") {
		t.Errorf("an empty listing needs an honest empty state:\n%s", got)
	}
}
