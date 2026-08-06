package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

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

// TestSetupFieldKind_SessionAfterCWDAndToggles pins the field order. Two
// constraints meet here: the listing is scoped to the directory chosen above it,
// so the session field must come after CWD — a session field the user reaches
// first would have nothing to list — and it is the only field that expands, so
// it sits last, below the fixed-height toggle rows it would otherwise displace.
func TestSetupFieldKind_SessionAfterCWDAndToggles(t *testing.T) {
	m := Model{}
	p := claudeLikePlugin()

	if got, want := m.setupFieldCount(p), 6; got != want {
		t.Fatalf("setupFieldCount = %d, want %d (cwd, 2 toggles, worktree, session, continue)", got, want)
	}
	wantKinds := []string{"cwd", "toggle", "toggle", "worktree", "session", "continue"}
	for i, want := range wantKinds {
		if kind, _ := m.setupFieldKind(p, i); kind != want {
			t.Errorf("kind at index %d = %q, want %q", i, kind, want)
		}
	}
	// The toggle index must keep tracking the slice position, not the cursor.
	for cursor, wantIdx := range map[int]int{1: 0, 2: 1} {
		if _, got := m.setupFieldKind(p, cursor); got != wantIdx {
			t.Errorf("toggleIdx at cursor %d = %d, want %d", cursor, got, wantIdx)
		}
	}
}

func TestSetupFieldKind_NoSessionFieldWhenNotDeclared(t *testing.T) {
	m := Model{}
	p := &plugin.PanePlugin{Command: plugin.CommandConfig{PromptsCWD: true}}

	if got, want := m.setupFieldCount(p), 3; got != want {
		t.Fatalf("setupFieldCount = %d, want %d", got, want)
	}
	for i, want := range []string{"cwd", "worktree", "continue"} {
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
	m.setupFieldCursor = 1 // on the worktree field, one Tab from session (cwd, worktree, session, continue)

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

// sessionKeyModel puts the cursor on the session field with rows loaded, ready
// to be driven through the real key dispatcher.
func sessionKeyModel(t *testing.T, rows ...ipc.ClaudeSessionInfo) (Model, *plugin.PanePlugin) {
	t.Helper()
	m := renderableSessionModel(t, rows...)
	p := m.pluginRegistry.Get("ai")
	m.setupFieldCursor = 2 // cwd = 0, worktree = 1, session = 2
	if kind, _ := m.setupFieldKind(p, m.setupFieldCursor); kind != "session" {
		t.Fatalf("cursor is on %q, want the session field", kind)
	}
	return m, p
}

// The following mirror the kube field's TestSetupKubeKey_* set: they drive
// handleCreatePaneSetupKey rather than calling the movement helpers directly,
// so a routing regression (the session case dropped from the kind switch) is
// caught rather than passing on the helpers alone.

func TestSetupSessionKey_DownMovesAndUpClampsAtTop(t *testing.T) {
	m, _ := sessionKeyModel(t, sessionRow("s1", "one", ""), sessionRow("s2", "two", ""))

	next, _ := m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if got := next.(Model).sessionCursor; got != 1 {
		t.Errorf("cursor after down = %d, want 1", got)
	}

	m = next.(Model)
	next, _ = m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: tea.KeyUp})
	if got := next.(Model).sessionCursor; got != 0 {
		t.Errorf("cursor after up = %d, want 0", got)
	}

	// Already at the top: up must clamp, not wrap onto the last row.
	m = next.(Model)
	next, _ = m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: tea.KeyUp})
	if got := next.(Model).sessionCursor; got != 0 {
		t.Errorf("cursor after up at top = %d, want it clamped to 0", got)
	}
}

func TestSetupSessionKey_DownClampsAtBottom(t *testing.T) {
	m, _ := sessionKeyModel(t, sessionRow("s1", "one", ""))
	m.sessionCursor = 1 // last row (0 = New session, 1 = s1)

	next, _ := m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if got := next.(Model).sessionCursor; got != 1 {
		t.Errorf("cursor after down at bottom = %d, want it clamped to 1", got)
	}
}

func TestSetupSessionKey_HomeEndAndPaging(t *testing.T) {
	rows := make([]ipc.ClaudeSessionInfo, 40)
	for i := range rows {
		rows[i] = sessionRow("s", "row", "")
	}
	m, _ := sessionKeyModel(t, rows...)
	last := m.sessionRowCount() - 1

	next, _ := m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: tea.KeyEnd})
	if got := next.(Model).sessionCursor; got != last {
		t.Errorf("cursor after End = %d, want %d", got, last)
	}

	m = next.(Model)
	next, _ = m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: tea.KeyHome})
	if got := next.(Model).sessionCursor; got != 0 {
		t.Errorf("cursor after Home = %d, want 0", got)
	}

	m = next.(Model)
	next, _ = m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if got := next.(Model).sessionCursor; got != m.sessionVisibleRows() {
		t.Errorf("cursor after PgDn = %d, want one page (%d)", got, m.sessionVisibleRows())
	}
}

func TestSetupSessionKey_EnterOnFreeRowCommitsAndSubmits(t *testing.T) {
	m, _ := sessionKeyModel(t, sessionRow("s1", "resumable", ""))
	m.sessionCursor = 1

	next, _ := m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := next.(Model)

	if got.selectedSessionID != "s1" {
		t.Errorf("selectedSessionID = %q, want s1", got.selectedSessionID)
	}
	if got.createPaneStep != 3 {
		t.Errorf("createPaneStep = %d, want 3 (submitted through to split selection)", got.createPaneStep)
	}
}

// TestSetupSessionKey_EnterOnBlockedRowIsRefused is the guard's UI half: Enter
// on a session another live pane holds must neither commit nor advance the
// dialog, while the cursor stays put so the footer can keep explaining why.
func TestSetupSessionKey_EnterOnBlockedRowIsRefused(t *testing.T) {
	m, _ := sessionKeyModel(t,
		sessionRow("free", "free one", ""),
		sessionRow("busy", "held elsewhere", "pane-0000000a"),
	)
	m.sessionCursor = 2 // the blocked row

	next, _ := m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := next.(Model)

	if got.selectedSessionID != "" {
		t.Errorf("selectedSessionID = %q, want empty — the row is blocked", got.selectedSessionID)
	}
	if got.createPaneStep == 3 {
		t.Error("dialog advanced past a refused selection")
	}
	if got.sessionCursor != 2 {
		t.Errorf("cursor moved to %d; it must stay on the blocked row so the footer explains it", got.sessionCursor)
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
//
// The session field now shares its budget with the worktree list (Task 5), so
// "exactly enough" and "short terminal shrinks" also have to clear the
// worktree list's own maxed-out share (worktreeListVisibleRows, 6 rows) before
// the session list sees any room to grow past ITS floor — not just
// setupChromeRows on its own.
func TestSessionVisibleRows_ShrinksOnShortTerminal(t *testing.T) {
	tests := []struct {
		name   string
		height int
		want   int
	}{
		{"tall terminal keeps the full window", 60, sessionListVisibleRows},
		{"exactly enough keeps the full window", setupChromeRows + worktreeListVisibleRows + sessionListVisibleRows, sessionListVisibleRows},
		{"short terminal shrinks", setupChromeRows + worktreeListVisibleRows + 8, 8},
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

// longTitleSessionModel is a model whose session titles all overflow the row
// budget — the shape that exposed the wrap.
func longTitleSessionModel(t *testing.T) Model {
	t.Helper()
	m := renderableSessionModel(t,
		sessionRow("s1", "there is a strange deployments restart happening in the homelab cluster", ""),
		sessionRow("s2", "we need to create a new gateway for our company web portal service", ""),
		sessionRow("s3", "test me ai project, ingestion mailbox scheduled processing rewrite", ""),
	)
	m.createPaneStep = 2
	m.setupFieldCursor = 2 // the session field (0 = CWD browser, 1 = worktree)
	m.height = 60
	return m
}

// TestRenderCreatePaneSetup_SessionRowsDoNotWrap is the end-to-end guard for
// the defect: rows were truncated to the box width minus padding but NOT minus
// the border, which lipgloss counts inside Style.Width. Every row then sat two
// cells over the wrap limit, and reflow dropped its last word onto a line of
// its own at column 0, shredding the list.
//
// Asserts against the boxed render rather than the raw content, so it fails on
// any future accounting drift regardless of which constant caused it.
func TestRenderCreatePaneSetup_SessionRowsDoNotWrap(t *testing.T) {
	m := longTitleSessionModel(t)

	content := m.renderCreatePaneSetupDialog()
	for _, line := range strings.Split(content, "\n") {
		if w := lipgloss.Width(line); w > m.setupTextWidth() {
			t.Errorf("line width %d exceeds the box text area %d (wraps):\n%q",
				w, m.setupTextWidth(), line)
		}
	}

	// Line-count equality is what actually proves nothing wrapped: the box adds
	// exactly two border rows and Padding(1,2)'s two blank rows.
	const boxRows = 4
	want := len(strings.Split(content, "\n")) + boxRows
	got := len(strings.Split(dialogBorder.Width(m.setupDialogWidth()).Render(content), "\n"))
	if got != want {
		t.Errorf("boxed render is %d lines, want %d — %d line(s) wrapped", got, want, got-want)
	}
}

// TestRenderSetupSessionField_BlockedMarkerSurvivesLongTitle guards the second
// half of the fix: appending the marker and truncating the whole row dropped
// "[open in …]" off exactly the rows long enough to need it, leaving a blocked
// row visually identical to a selectable one until Enter refused it.
func TestRenderSetupSessionField_BlockedMarkerSurvivesLongTitle(t *testing.T) {
	m := renderableSessionModel(t, sessionRow("busy",
		"a very long earlier prompt that on its own already fills the whole row budget and then some",
		"pane-0000000a"))
	m.height = 60

	got := m.renderSetupSessionField(true)
	if !strings.Contains(got, "open in") {
		t.Errorf("the in-use marker must survive a title that fills the row:\n%s", got)
	}
	for _, line := range strings.Split(got, "\n") {
		if w := lipgloss.Width(line); w > m.setupTextWidth() {
			t.Errorf("marked row width %d exceeds the text area %d:\n%q", w, m.setupTextWidth(), line)
		}
	}
}
