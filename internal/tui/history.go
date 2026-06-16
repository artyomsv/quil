package tui

import (
	"fmt"
	"log"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
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
	return m
}

// renderCommandHistory produces the modal body string. The outer dialogBorder
// wrapping + centering is applied by the common render dispatch (renderDialog),
// exactly as for renderMemoryDialog.
func (m Model) renderCommandHistory() string {
	var b strings.Builder
	b.WriteString(dialogTitle.Render("Input history · " + m.history.paneType))
	b.WriteByte('\n')

	switch {
	case m.history.loading:
		b.WriteString("Loading…\n")
		b.WriteByte('\n')
		b.WriteString(dialogSubtle.Render("Esc close"))
		return b.String()
	case !m.history.supported:
		b.WriteString("No input history for this pane type.\n")
		b.WriteByte('\n')
		b.WriteString(dialogSubtle.Render("Esc close"))
		return b.String()
	case len(m.history.entries) == 0:
		b.WriteString("No input history yet.\n")
		b.WriteByte('\n')
		b.WriteString(dialogSubtle.Render("Esc close"))
		return b.String()
	}

	// Inner width matches the Memory dialog's box (width 80, padding 1,2 →
	// 4 cells of horizontal padding inside the border). Leave room for the
	// "› " / "  " cursor prefix.
	const innerWidth = 80 - 4
	const previewWidth = innerWidth - 2

	for i, entry := range m.history.entries {
		if i > 0 {
			b.WriteByte('\n')
		}
		for j, line := range entry.Preview {
			prefix := "  "
			if i == m.history.cursor && j == 0 {
				prefix = "› "
			}
			style := dialogNormal
			if i == m.history.cursor {
				style = dialogSelected
			}
			b.WriteString(style.Render(prefix + truncateHistory(line, previewWidth)))
			b.WriteByte('\n')
		}
	}

	b.WriteByte('\n')
	b.WriteString(dialogSubtle.Render("↑↓ nav · Enter open · Esc close"))
	return b.String()
}

// truncateHistory shortens s to at most n runes, appending "…" if truncated.
// Rune-aware so multi-byte UTF-8 (CJK, emoji) is not sliced mid-rune. Mirrors
// truncateMem in memory.go.
func truncateHistory(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
