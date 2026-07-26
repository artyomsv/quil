package tui

import (
	"fmt"
	"log"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/claudesessions"
	"github.com/artyomsv/quil/internal/ipc"
)

// sessionScanTimeout is how long the setup dialog waits for a session listing
// before showing a diagnosable message. Mirrors the palette's search timeout:
// a wedged daemon — or a new TUI talking to an older daemon whose handleMessage
// has no case for claude_sessions_req and silently drops it — would otherwise
// leave the field on "Scanning…" forever.
const sessionScanTimeout = 3 * time.Second

// sessionListVisibleRows is the nominal height of the expanded session list.
// Matches browserVisibleRows so the dialog does not change height when the user
// tabs between the directory browser and the session picker.
const sessionListVisibleRows = browserVisibleRows

// sessionListMinRows is the floor the list shrinks to on a short terminal.
// Below this the picker stops being usable at all, and losing the [Continue]
// button is worse than a cramped list.
const sessionListMinRows = 4

// sessionVisibleRows is the actual window height for the current terminal.
// The setup dialog already spends ~12 rows on the directory browser plus the
// toggles and the Continue button, and renderDialog's lipgloss.Place does not
// clip — so on a short terminal a fixed-height second list pushes the button
// off-screen entirely. Shrink the list instead; it is the only element here
// that can give.
func (m Model) sessionVisibleRows() int {
	// Rows the rest of the dialog needs around this field, measured against the
	// shipped claude-code layout (title, CWD browser + hint, toggles, button,
	// borders and padding).
	const surroundingRows = 26
	avail := m.height - surroundingRows
	switch {
	case avail >= sessionListVisibleRows:
		return sessionListVisibleRows
	case avail < sessionListMinRows:
		return sessionListMinRows
	default:
		return avail
	}
}

// sessionScanState tracks the session field's request lifecycle.
type sessionScanState int

const (
	sessionScanIdle     sessionScanState = iota // nothing requested yet for this CWD
	sessionScanning                             // request issued, awaiting response
	sessionScanReady                            // rows are current
	sessionScanTimedOut                         // no response within sessionScanTimeout
	sessionScanFailed                           // daemon answered with an error
)

// claudeSessionsRespMsg carries a daemon session listing into Update.
type claudeSessionsRespMsg struct{ Resp ipc.ClaudeSessionsRespPayload }

// sessionScanTimeoutMsg fires sessionScanTimeout after a request was issued.
// cwd is the directory captured when the tick was scheduled, so a response for
// a different directory cannot be mistaken for this one.
type sessionScanTimeoutMsg struct{ cwd string }

// requestClaudeSessions fires MsgClaudeSessionsReq for cwd (fire-and-forget);
// the response arrives via listenForMessages → claudeSessionsRespMsg. Mirrors
// requestPaneSearch.
func (m Model) requestClaudeSessions(cwd string) tea.Cmd {
	return func() tea.Msg {
		if m.client == nil {
			return nil
		}
		msg, err := ipc.NewMessage(ipc.MsgClaudeSessionsReq, ipc.ClaudeSessionsReqPayload{CWD: cwd})
		if err != nil {
			log.Printf("requestClaudeSessions: marshal: %v", err)
			return nil
		}
		// Decorative, like requestPaneSearch's: respondTo unicasts to this conn
		// regardless, and staleness is resolved by the echoed CWD.
		msg.ID = fmt.Sprintf("sessions-%d", time.Now().UnixNano())
		if err := m.client.Send(msg); err != nil {
			log.Printf("requestClaudeSessions: send: %v", err)
		}
		return nil
	}
}

// sessionScanTimeoutCmd schedules the failure fallback for an issued request.
func sessionScanTimeoutCmd(cwd string) tea.Cmd {
	return tea.Tick(sessionScanTimeout, func(time.Time) tea.Msg {
		return sessionScanTimeoutMsg{cwd: cwd}
	})
}

// ensureSessionScan issues a session listing for the currently highlighted
// directory if the rows on hand do not already belong to it. Returns nil when
// the existing rows are still valid, so re-focusing the field costs nothing.
//
// Called when the session field gains focus rather than when the dialog opens:
// creating a pane with a fresh session — the overwhelmingly common case — then
// performs no session I/O at all.
func (m *Model) ensureSessionScan() tea.Cmd {
	cwd := m.cwdBrowseDir
	if cwd == "" {
		// No directory resolved yet (browser failed to load, or the plugin does
		// not prompt for one). Nothing to scan against.
		m.sessionState = sessionScanIdle
		m.sessionRows = nil
		return nil
	}
	// Re-focusing is a no-op only while a scan is in flight or its rows are
	// already on hand. A timed-out or failed scan MUST be retryable: the field
	// tells the user the daemon may not be running, which invites exactly the
	// retry that treating those states as "settled" would silently refuse,
	// leaving the message up until they navigate away and back.
	if m.sessionScanCWD == cwd &&
		(m.sessionState == sessionScanning || m.sessionState == sessionScanReady) {
		return nil
	}
	// Rescanning means the directory changed, which invalidates any session
	// already picked: it belongs to the previous project.
	m.sessionScanCWD = cwd
	m.sessionState = sessionScanning
	m.sessionRows = nil
	m.sessionCursor = 0
	m.sessionScroll = 0
	m.selectedSessionID = ""
	// The panel describes a row that is about to be replaced, and its response
	// is matched on the CWD that just changed — it could never resolve.
	m.closeSessionDetail()
	return tea.Batch(m.requestClaudeSessions(cwd), sessionScanTimeoutCmd(cwd))
}

// resetSessionSelection discards the rows and the committed choice. Called when
// the working directory changes: a session recorded under another project is
// not a meaningful resume target, and silently carrying the selection across
// would spawn a pane attached to a conversation from a different codebase.
func (m *Model) resetSessionSelection() {
	m.sessionRows = nil
	m.sessionCursor = 0
	m.sessionScroll = 0
	m.sessionScanCWD = ""
	m.sessionState = sessionScanIdle
	m.selectedSessionID = ""
	m.closeSessionDetail()
}

// applyClaudeSessions stores a fresh listing. Responses whose echoed CWD is not
// the directory currently highlighted are dropped — the user moved on while the
// scan was in flight. A response for the current directory is accepted even
// after the timeout fired, and clears the timed-out state.
func (m Model) applyClaudeSessions(resp ipc.ClaudeSessionsRespPayload) Model {
	if resp.CWD != m.sessionScanCWD {
		return m
	}
	if resp.Error != "" {
		m.sessionState = sessionScanFailed
		m.sessionError = resp.Error
		m.sessionRows = nil
		m.sessionCursor = 0
		m.sessionScroll = 0
		m.sessionTruncated = false
		return m
	}
	// Re-sanitize titles on arrival. The daemon already strips control and
	// format characters, so this is defence in depth rather than a fix — but
	// these strings originate in user-authored prompt text and are written
	// straight to the screen, unlike every other daemon-sourced string in the
	// TUI, which passes through the VT emulator. Doing it here rather than in
	// the render path costs one pass per response instead of one per frame.
	rows := make([]ipc.ClaudeSessionInfo, len(resp.Sessions))
	copy(rows, resp.Sessions)
	for i := range rows {
		rows[i].Title = claudesessions.SanitizeTitle(rows[i].Title)
	}

	m.sessionRows = rows
	m.sessionTruncated = resp.Truncated
	m.sessionState = sessionScanReady
	m.sessionError = ""
	m.sessionCursor = 0
	m.sessionScroll = 0
	return m
}

// sessionDetailPanel is the picker's info panel, following the same shape as
// ctxMenuState and paletteState: one struct on the Model whose zero value is
// "closed", so nothing else has to be reset when it goes away.
//
// It replaces the list in place rather than compositing over it, so it needs no
// overlay plumbing and cannot desynchronize from the row it describes: id names
// that row, and a response for any other id is dropped as stale.
type sessionDetailPanel struct {
	open  bool                               // panel is showing instead of the list
	id    string                             // session it describes ("" = the New session row)
	state sessionScanState                   // request lifecycle
	data  ipc.ClaudeSessionDetailRespPayload // last accepted read
}

// claudeSessionDetailRespMsg carries one session's deep read into Update.
type claudeSessionDetailRespMsg struct {
	Resp ipc.ClaudeSessionDetailRespPayload
}

// sessionDetailTimeoutMsg fires sessionScanTimeout after a detail request. id is
// the session captured when the tick was scheduled, so a response for a row the
// user has since moved off cannot be mistaken for this one.
type sessionDetailTimeoutMsg struct{ id string }

// requestClaudeSessionDetail fires MsgClaudeSessionDetailReq (fire-and-forget);
// the response arrives via listenForMessages. Mirrors requestClaudeSessions.
func (m Model) requestClaudeSessionDetail(cwd, sessionID string) tea.Cmd {
	return func() tea.Msg {
		if m.client == nil {
			return nil
		}
		msg, err := ipc.NewMessage(ipc.MsgClaudeSessionDetailReq,
			ipc.ClaudeSessionDetailReqPayload{CWD: cwd, SessionID: sessionID})
		if err != nil {
			log.Printf("requestClaudeSessionDetail: marshal: %v", err)
			return nil
		}
		msg.ID = fmt.Sprintf("session-detail-%d", time.Now().UnixNano())
		if err := m.client.Send(msg); err != nil {
			log.Printf("requestClaudeSessionDetail: send: %v", err)
		}
		return nil
	}
}

// ensureSessionDetail requests the deep read for the highlighted row, unless the
// panel already holds it. Returns nil when there is nothing to fetch — the "New
// session" row has no transcript, and re-reading a row already on screen would
// re-stream a multi-MB file for no new information.
//
// Unlike ensureSessionScan this does NOT treat a failure as settled either: the
// panel says the read failed, which invites the retry that pressing i again is.
func (m *Model) ensureSessionDetail() tea.Cmd {
	s := m.sessionRowAt(m.sessionCursor)
	if s == nil {
		// The "New session" row. Keep the panel open (it explains what a fresh
		// session means) but there is nothing to read.
		m.sessionDetail = sessionDetailPanel{open: true}
		return nil
	}
	if m.sessionDetail.id == s.ID &&
		(m.sessionDetail.state == sessionScanning || m.sessionDetail.state == sessionScanReady) {
		return nil
	}
	m.sessionDetail = sessionDetailPanel{open: true, id: s.ID, state: sessionScanning}
	return tea.Batch(
		m.requestClaudeSessionDetail(m.sessionScanCWD, s.ID),
		tea.Tick(sessionScanTimeout, func(time.Time) tea.Msg {
			return sessionDetailTimeoutMsg{id: s.ID}
		}),
	)
}

// closeSessionDetail returns the field to the list. The fetched detail is
// discarded rather than cached: transcripts grow while you look at them, and a
// stale turn count presented as current is worse than reading again.
func (m *Model) closeSessionDetail() {
	m.sessionDetail = sessionDetailPanel{}
}

// applyClaudeSessionDetail stores a deep read. Responses are matched on BOTH
// echoed keys: the session the panel is waiting on, and the directory the rows
// were listed for. The user can keep moving the cursor while a read is in
// flight, so an unmatched response is a stale answer to a row they left.
func (m Model) applyClaudeSessionDetail(resp ipc.ClaudeSessionDetailRespPayload) Model {
	if resp.SessionID != m.sessionDetail.id || resp.CWD != m.sessionScanCWD {
		return m
	}
	// Re-sanitize on arrival for the same reason applyClaudeSessions does: these
	// are user-authored prompt strings rendered straight to the screen, never
	// through the VT emulator.
	resp.FirstPrompt = claudesessions.SanitizePrompt(resp.FirstPrompt)
	resp.LastPrompt = claudesessions.SanitizePrompt(resp.LastPrompt)

	m.sessionDetail.data = resp
	if resp.Error != "" {
		m.sessionDetail.state = sessionScanFailed
		return m
	}
	m.sessionDetail.state = sessionScanReady
	return m
}

// sessionRowCount is the number of rows the session list renders: the always-
// present "New session" row plus one per discovered session.
func (m Model) sessionRowCount() int {
	return len(m.sessionRows) + 1
}

// sessionRowAt returns the session at a cursor row, or nil for row 0 ("New
// session") and for any out-of-range index.
func (m Model) sessionRowAt(row int) *ipc.ClaudeSessionInfo {
	if row <= 0 || row-1 >= len(m.sessionRows) {
		return nil
	}
	return &m.sessionRows[row-1]
}

// sessionRowSelectable reports whether Enter may commit the row. Row 0 is
// always selectable; a session another live pane already holds is not — two
// claude processes on one transcript overwrite each other's history.
//
// Note this blocks Enter but does NOT skip the cursor, matching how the Ctrl+N
// plugin list treats an uninstalled binary: landing on the row is what lets the
// footer explain why it is blocked.
func (m Model) sessionRowSelectable(row int) bool {
	s := m.sessionRowAt(row)
	return s == nil || s.InUsePaneID == ""
}

// commitSessionSelection records the highlighted row as the pane's resume
// target. Reports false when the row is blocked, leaving the previous choice
// untouched.
func (m *Model) commitSessionSelection() bool {
	if !m.sessionRowSelectable(m.sessionCursor) {
		return false
	}
	if s := m.sessionRowAt(m.sessionCursor); s != nil {
		m.selectedSessionID = s.ID
	} else {
		m.selectedSessionID = ""
	}
	return true
}

// adjustSessionScroll keeps the cursor inside the visible window. Mirrors
// adjustBrowseScroll.
func (m *Model) adjustSessionScroll() {
	visible := m.sessionVisibleRows()
	if m.sessionCursor < m.sessionScroll {
		m.sessionScroll = m.sessionCursor
	}
	if m.sessionCursor >= m.sessionScroll+visible {
		m.sessionScroll = m.sessionCursor - visible + 1
	}
}

// sessionSummaryLine is the one-line value shown while the field is unfocused:
// the committed choice, or "New session".
func (m Model) sessionSummaryLine() string {
	if m.selectedSessionID == "" {
		return "New session"
	}
	for _, s := range m.sessionRows {
		if s.ID == m.selectedSessionID {
			return sessionRowLabel(s)
		}
	}
	// Selected before the rows were replaced (should not happen, since changing
	// the directory clears the selection) — fall back to the raw id.
	return "Resume " + shortSessionID(m.selectedSessionID)
}

// sessionRowLabel renders one session as "<age>   <title>", falling back to the
// short id when the transcript held no typed prompt to title it with.
func sessionRowLabel(s ipc.ClaudeSessionInfo) string {
	title := s.Title
	if title == "" {
		title = "(no prompt recorded) " + shortSessionID(s.ID)
	}
	return fmt.Sprintf("%-7s %s", relativeAge(s.ModifiedMs), title)
}

// shortSessionID trims a session UUID to its leading segment for display.
func shortSessionID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// absoluteTime renders a unix-millisecond timestamp for the detail panel, where
// the exact moment matters (relativeAge answers "recent?", this answers "which
// afternoon was that?"). Local time, since every other timestamp the user sees
// in this TUI is theirs.
func absoluteTime(ms int64) string {
	if ms <= 0 {
		return "—"
	}
	return time.UnixMilli(ms).Local().Format("2006-01-02 15:04")
}

// formatBytes renders a byte count compactly. Deliberately decimal (KB = 1000):
// this sits next to a file the user can check in their own file manager, which
// on both Windows and macOS reports decimal.
func formatBytes(n int64) string {
	switch {
	case n <= 0:
		return "—"
	case n < 1000:
		return fmt.Sprintf("%d B", n)
	case n < 1000*1000:
		return fmt.Sprintf("%.0f KB", float64(n)/1000)
	case n < 1000*1000*1000:
		return fmt.Sprintf("%.1f MB", float64(n)/(1000*1000))
	default:
		return fmt.Sprintf("%.1f GB", float64(n)/(1000*1000*1000))
	}
}

// relativeAge renders a unix-millisecond timestamp as a compact age such as
// "2h ago". Deliberately coarse — the list is scanned, not read.
func relativeAge(ms int64) string {
	if ms <= 0 {
		return "?"
	}
	d := time.Since(time.UnixMilli(ms))
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%dy ago", int(d.Hours()/24/365))
	}
}
