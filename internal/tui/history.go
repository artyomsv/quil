package tui

import (
	"fmt"
	"log"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

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
	// historyMinRows is the floor on the list window. Below this the modal
	// stops being usable, and renderDialog's lipgloss.Place does not clip, so
	// something has to give on a very short terminal — we let the box overflow
	// rather than render zero rows.
	historyMinRows = 3
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
// send. The corresponding MsgPaneHistoryResp is dispatched by
// listenForMessages → historyListMsg → Update. Mirrors refreshMemory.
func (m Model) requestHistory(paneID string) tea.Cmd {
	return func() tea.Msg {
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
	m.history.entries = resp.Entries
	m.history.loading = false
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
	boxW := historyDialogWidth
	if m.width > 2 && boxW > m.width-2 {
		boxW = m.width - 2
	}
	inner := boxW - dialogBoxChrome
	if inner < 1 {
		inner = 1
	}
	return inner
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

	switch {
	case m.history.loading:
		b.WriteString("Loading…\n")
		return footer("Esc close")
	case !m.history.supported:
		b.WriteString("No input history for this pane type.\n")
		return footer("Esc close")
	case len(m.history.entries) == 0:
		b.WriteString("No input history yet.\n")
		return footer("Esc close")
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
