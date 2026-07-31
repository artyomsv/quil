package tui

import (
	"fmt"
	"log"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/claudesessions"
	"github.com/artyomsv/quil/internal/ipc"
)

// historyDialogWidth is the input-history modal's box width. It is 25% wider
// than the 80-cell default the dialog family shares (Memory, About): a history
// row is prose, not a label, and the extra 20 cells are often what separates
// two prompts that open with the same clause. renderDialog still clamps it to
// the terminal.
const historyDialogWidth = 100

// historyRowPrefix is the width of the per-row cursor prefix ("› " / "  ").
// Subtracted from the inner width so a row can never exceed the box — the old
// layout budgeted the prefix out of the wrong number and every row soft-wrapped
// onto a second line, which is what made a long list unreadable.
const historyRowPrefix = 2

const (
	// historyMinRows is the floor on the list window: one row, never zero.
	//
	// It is 1 rather than a friendlier number because renderDialog's
	// lipgloss.Place does NOT clip — a box taller than the terminal is drawn
	// past the edge, and on this dialog what falls off is the footer. Any floor
	// above the height actually available manufactures exactly that overflow: a
	// 3-row floor forced a 10-row box onto terminals with 6-9 rows, each of
	// which had room for a shorter one. With a floor of 1 the box exceeds the
	// terminal only below historyChromeRows+1 rows, where the CHROME alone does
	// not fit and no choice of list height helps.
	historyMinRows = 1
	// historyChromeRows is every row the modal spends outside the list: the
	// rounded border (2), dialogBorder's Padding(1,2) top and bottom (2), the
	// title, the blank row above the footer, the footer, and one spare so the
	// centered box never sits flush against the terminal edge.
	historyChromeRows = 8
)

// historyState holds the live state of the input-history modal
// (dialogCommandHistory). It mirrors memoryDialogState's role for the Memory
// dialog: a snapshot of the daemon's response plus the cursor and a loading
// flag. supported is false when the active pane's plugin does not opt into
// history capture (Command.RecordHistory == false) — the modal then renders a
// short explanatory line instead of issuing an IPC request.
type historyState struct {
	paneID    string
	paneType  string
	supported bool
	loading   bool
	// timedOut is set when no response arrived within historyReqTimeout. Without
	// it, loading is cleared only by applyHistoryList, so a response that is
	// never delivered — dropped, or rejected by DecodePayload, which
	// listenForMessages answers with a bare listenContinueMsg — leaves the modal
	// showing "Loading…" forever with nothing to diagnose it by. Same failure
	// mode paletteSearchTimeout and sessionScanTimeout exist for.
	timedOut  bool
	entries   []ipc.HistoryEntryMeta
	cursor    int
	// scroll is the index of the first entry drawn. The list holds up to
	// panehistory.MaxEntries (200) rows against a window of ~30, so without it
	// everything past the fold rendered off-screen with no way to reach it.
	scroll int
}

// historyListMsg is the Bubble Tea message produced when the TUI receives
// MsgPaneHistoryResp from the daemon. Update applies it via applyHistoryList.
type historyListMsg struct {
	Resp ipc.PaneHistoryRespPayload
}

// historyEntryMsg is the Bubble Tea message produced when the TUI receives
// MsgPaneHistoryEntryResp — one entry's full text. Update opens it in the
// read-only viewer.
type historyEntryMsg struct {
	Resp ipc.PaneHistoryEntryRespPayload
}

// historyTimeoutMsg fires historyReqTimeout after a list request was issued. It
// is a LOCAL timer, so its Update branch must NOT re-arm listenForMessages —
// doing so would add a second concurrent reader on the IPC connection.
type historyTimeoutMsg struct{ paneID string }

// historyReqTimeout bounds the wait for MsgPaneHistoryResp. The daemon answers
// from a file read, so anything near this is a wedge rather than slow I/O.
const historyReqTimeout = 3 * time.Second

// openHistoryDialog transitions the Model into the input-history modal for the
// given pane. loading is true only when the pane type supports history (the
// caller pairs this with requestHistory); otherwise the modal renders the
// unsupported message immediately.
func (m Model) openHistoryDialog(paneID, paneType string, supported bool) Model {
	m.dialog = dialogCommandHistory
	m.history = historyState{
		paneID:    paneID,
		paneType:  paneType,
		supported: supported,
		loading:   supported,
		cursor:    0,
	}
	return m
}

// requestHistory issues MsgPaneHistoryReq to the daemon as a fire-and-forget
// send, paired with the timeout tick that bounds the wait. The corresponding
// MsgPaneHistoryResp is dispatched by listenForMessages → historyListMsg →
// Update. Mirrors refreshMemory.
func (m Model) requestHistory(paneID string) tea.Cmd {
	send := func() tea.Msg {
		if m.client == nil {
			return nil
		}
		msg, err := ipc.NewMessage(ipc.MsgPaneHistoryReq, ipc.PaneHistoryReqPayload{PaneID: paneID})
		if err != nil {
			log.Printf("requestHistory: marshal: %v", err)
			return nil
		}
		msg.ID = fmt.Sprintf("hist-%d", time.Now().UnixNano())
		if err := m.client.Send(msg); err != nil {
			log.Printf("requestHistory: send: %v", err)
		}
		return nil
	}
	return tea.Batch(send, tea.Tick(historyReqTimeout, func(time.Time) tea.Msg {
		return historyTimeoutMsg{paneID: paneID}
	}))
}

// applyHistoryTimeout marks the list as timed out, but only if it is still
// waiting for the pane the tick was armed for. A response that arrived first
// cleared loading, and a tick from a previous pane's request is stale.
func (m Model) applyHistoryTimeout(paneID string) Model {
	if m.dialog != dialogCommandHistory || paneID != m.history.paneID || !m.history.loading {
		return m
	}
	m.history.loading = false
	m.history.timedOut = true
	return m
}

// requestHistoryEntry issues MsgPaneHistoryEntryReq for one entry's full text,
// looked up by its TsMs id. The response arrives as historyEntryMsg.
func (m Model) requestHistoryEntry(paneID string, tsMs int64) tea.Cmd {
	return func() tea.Msg {
		if m.client == nil {
			return nil
		}
		msg, err := ipc.NewMessage(ipc.MsgPaneHistoryEntryReq, ipc.PaneHistoryEntryReqPayload{PaneID: paneID, TsMs: tsMs})
		if err != nil {
			log.Printf("requestHistoryEntry: marshal: %v", err)
			return nil
		}
		msg.ID = fmt.Sprintf("histentry-%d", time.Now().UnixNano())
		if err := m.client.Send(msg); err != nil {
			log.Printf("requestHistoryEntry: send: %v", err)
		}
		return nil
	}
}

// applyHistoryList stores a fresh preview list and clamps the cursor. Stale
// responses (a different pane than the one the modal is showing) are ignored —
// the same guard pattern applyMemoryReport uses against the active dialog.
func (m Model) applyHistoryList(resp ipc.PaneHistoryRespPayload) Model {
	if resp.PaneID != m.history.paneID {
		return m
	}
	// Re-sanitize on arrival even though the daemon already ran PreviewLine.
	// Under `quil --remote <host>` the daemon that produced these rows is on a
	// machine the user may not control, and the version handshake only proves
	// the binaries match — not that the peer is honest. A row reaches the
	// terminal without passing through the VT emulator, and truncateToWidth
	// measures an escape sequence as zero cells, so it would survive truncation
	// intact. Same both-sides-of-the-socket rule sessions.go applies to session
	// titles, using the same sanitizer; its 240-rune cap is far above any row
	// width, so it costs nothing here.
	for i := range resp.Entries {
		resp.Entries[i].Preview = claudesessions.SanitizeTitle(resp.Entries[i].Preview)
	}
	m.history.entries = resp.Entries
	m.history.loading = false
	// A response that beat the retry recovers the modal on its own.
	m.history.timedOut = false
	if m.history.cursor >= len(m.history.entries) {
		m.history.cursor = len(m.history.entries) - 1
	}
	if m.history.cursor < 0 {
		m.history.cursor = 0
	}
	m.syncHistoryScroll()
	return m
}

// historyInnerWidth is the usable content width inside the modal's border for
// the current terminal size. Kept in lockstep with renderDialog's clamp, and
// subtracting dialogBoxChrome (not the border alone) is the whole point: the
// previous budget was two cells too generous, so every row wrapped.
func (m Model) historyInnerWidth() int {
	return dialogInnerWidth(m.width, historyDialogWidth)
}

// historyVisibleRows is how many entries fit in the list for the current
// terminal height. Same shape as sessionVisibleRows: the list is the only
// element that can give, so it absorbs a short terminal rather than pushing the
// footer off-screen.
func (m Model) historyVisibleRows() int {
	if avail := m.height - historyChromeRows; avail > historyMinRows {
		return avail
	}
	return historyMinRows
}

// historyWindow resolves the [start, end) slice of entries to draw. Pure, and
// deliberately re-derives the scroll origin from the cursor rather than
// trusting the stored one: render must not depend on Update having run first,
// since a WindowSizeMsg can change the row budget between them.
func historyWindow(total, cursor, scroll, visible int) (start, end int) {
	if total <= 0 || visible <= 0 {
		return 0, 0
	}
	if visible > total {
		visible = total
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor > total-1 {
		cursor = total - 1
	}
	// Keep the cursor inside the window, then pull the window back inside the
	// list. The order matters: a shrunken list can leave a stored scroll past
	// the last valid origin, which would draw blank rows under the final entry.
	if cursor < scroll {
		scroll = cursor
	}
	if cursor >= scroll+visible {
		scroll = cursor - visible + 1
	}
	if scroll > total-visible {
		scroll = total - visible
	}
	if scroll < 0 {
		scroll = 0
	}
	return scroll, scroll + visible
}

// syncHistoryScroll stores the window origin historyWindow would pick. Called
// after every cursor move and whenever the list is replaced.
func (m *Model) syncHistoryScroll() {
	m.history.scroll, _ = historyWindow(
		len(m.history.entries), m.history.cursor, m.history.scroll, m.historyVisibleRows())
}

// scrollHistoryList moves the cursor by one wheel step. The cursor moves rather
// than the window alone so Enter still opens what the highlight is on — a list
// whose highlight scrolled out of sight would make the wheel and the keyboard
// disagree about what is selected.
func (m *Model) scrollHistoryList(button tea.MouseButton) {
	lines := m.cfg.UI.MouseScrollLines
	if lines < 1 {
		lines = 3
	}
	switch button {
	case tea.MouseWheelUp:
		m.history.cursor -= lines
	case tea.MouseWheelDown:
		m.history.cursor += lines
	default:
		return // horizontal wheel
	}
	if last := len(m.history.entries) - 1; m.history.cursor > last {
		m.history.cursor = last
	}
	if m.history.cursor < 0 {
		m.history.cursor = 0
	}
	m.syncHistoryScroll()
}

// renderCommandHistory produces the modal body string. The outer dialogBorder
// wrapping + centering is applied by the common render dispatch (renderDialog),
// exactly as for renderMemoryDialog. Exactly one row is drawn per entry — the
// daemon flattens each prompt to a single line (panehistory.PreviewLine) and
// the row is truncated to the box, so the list stays scannable however long the
// prompts were.
func (m Model) renderCommandHistory() string {
	inner := m.historyInnerWidth()

	var b strings.Builder
	b.WriteString(dialogTitle.Render(truncateToWidth("Input history · "+m.history.paneType, inner)))
	b.WriteByte('\n')

	footer := func(hint string) string {
		b.WriteByte('\n')
		b.WriteString(dialogSubtle.Render(truncateToWidth(hint, inner)))
		return b.String()
	}

	// These go through truncateToWidth like every other line: at minTermWidth
	// (40) the box clamps to 38 and inner to 32, which is shorter than the
	// unsupported-pane message — and an untruncated line reflows onto a second
	// row, eating one of the chrome rows the height budget assumes.
	status := func(text, hint string) string {
		b.WriteString(truncateToWidth(text, inner))
		b.WriteByte('\n')
		return footer(hint)
	}
	switch {
	case m.history.loading:
		return status("Loading…", "Esc close")
	case m.history.timedOut:
		return status("No response from the daemon.", "r retry · Esc close")
	case !m.history.supported:
		return status("No input history for this pane type.", "Esc close")
	case len(m.history.entries) == 0:
		return status("No input history yet.", "Esc close")
	}

	visible := m.historyVisibleRows()
	start, end := historyWindow(len(m.history.entries), m.history.cursor, m.history.scroll, visible)
	textW := inner - historyRowPrefix

	for i := start; i < end; i++ {
		prefix := "  "
		style := dialogNormal
		if i == m.history.cursor {
			prefix = "› "
			style = dialogSelected
		}
		b.WriteString(style.Render(prefix + truncateToWidth(m.history.entries[i].Preview, textW)))
		b.WriteByte('\n')
	}

	hint := "↑↓ nav · Enter open · Esc close"
	// Only claim a position when there is something off-screen — on a list that
	// fits entirely, a counter is noise.
	if end-start < len(m.history.entries) {
		hint += fmt.Sprintf(" · %d-%d/%d", start+1, end, len(m.history.entries))
	}
	return footer(hint)
}
