package tui

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/memreport"
)

// memRowKind distinguishes the three kinds of visible rows in the Memory
// dialog: the grand-total header, a tab subtotal, or a pane detail.
type memRowKind int

const (
	memRowTotal memRowKind = iota
	memRowTab
	memRowPane
)

// memoryRow is one visible line in the Memory dialog tree view.
type memoryRow struct {
	kind   memRowKind
	tabID  string
	paneID string
	label  string
	goHeap uint64
	ptyRSS uint64
	tui    uint64 // filled in by the renderer, not by the tree builder
	total  uint64
}

// memoryTabNode groups panes by TabID for the tree. Panes are sorted by
// TotalBytes desc; tabs themselves are sorted by cumulative total desc.
type memoryTabNode struct {
	id       string
	name     string
	total    uint64
	panes    []ipc.PaneMemInfo
	expanded bool
}

// memoryTree is the collapsible tree rendered by the Memory dialog.
type memoryTree struct {
	snapshot ipc.MemoryReportRespPayload
	tabs     []*memoryTabNode
}

// buildMemoryTree groups the response's panes by TabID. tabOrder + tabNames
// come from the live Model so tabs render with their user-visible names.
// Tabs missing from tabOrder (e.g. a pane whose tab was just destroyed) are
// appended; the whole list is then sorted by total desc so the biggest
// consumer is shown first.
func buildMemoryTree(resp ipc.MemoryReportRespPayload, tabOrder []string, tabNames map[string]string) *memoryTree {
	byTab := make(map[string]*memoryTabNode)
	for _, p := range resp.Panes {
		node, ok := byTab[p.TabID]
		if !ok {
			node = &memoryTabNode{id: p.TabID, name: tabNames[p.TabID]}
			if node.name == "" {
				node.name = p.TabID
			}
			byTab[p.TabID] = node
		}
		node.panes = append(node.panes, p)
		node.total += p.TotalBytes
	}
	for _, node := range byTab {
		sort.Slice(node.panes, func(i, j int) bool {
			return node.panes[i].TotalBytes > node.panes[j].TotalBytes
		})
	}

	t := &memoryTree{snapshot: resp}
	seen := make(map[string]bool, len(byTab))
	for _, id := range tabOrder {
		if node, ok := byTab[id]; ok {
			t.tabs = append(t.tabs, node)
			seen[id] = true
		}
	}
	for id, node := range byTab {
		if !seen[id] {
			t.tabs = append(t.tabs, node)
		}
	}
	sort.SliceStable(t.tabs, func(i, j int) bool {
		return t.tabs[i].total > t.tabs[j].total
	})
	return t
}

// flatten walks the tree and emits a row per visible line: the grand-total
// row first, then each tab row, then — for expanded tabs — each pane row.
// The caller uses the returned slice for cursor arithmetic.
func (t *memoryTree) flatten() []memoryRow {
	rows := make([]memoryRow, 0, 1+len(t.tabs))
	rows = append(rows, memoryRow{
		kind:  memRowTotal,
		label: "Total",
		total: t.snapshot.Total,
	})
	for _, tab := range t.tabs {
		rows = append(rows, memoryRow{
			kind:  memRowTab,
			tabID: tab.id,
			label: tab.name,
			total: tab.total,
		})
		if tab.expanded {
			for _, p := range tab.panes {
				rows = append(rows, memoryRow{
					kind:   memRowPane,
					tabID:  tab.id,
					paneID: p.PaneID,
					label:  p.PaneID, // renderer may substitute a pane name
					goHeap: p.GoHeapBytes,
					ptyRSS: p.PTYRSSBytes,
					total:  p.TotalBytes,
				})
			}
		}
	}
	return rows
}

// toggleAt flips the expanded state of the tab at visible-row index i. A
// no-op if i does not refer to a tab row (e.g. grand-total or pane).
func (t *memoryTree) toggleAt(i int) {
	rows := t.flatten()
	if i < 0 || i >= len(rows) || rows[i].kind != memRowTab {
		return
	}
	for _, tab := range t.tabs {
		if tab.id == rows[i].tabID {
			tab.expanded = !tab.expanded
			return
		}
	}
}

// findTab returns the tab node matching id, or nil. Used by the renderer
// to derive the expand/collapse indicator for a tab row.
func (t *memoryTree) findTab(id string) *memoryTabNode {
	for _, tab := range t.tabs {
		if tab.id == id {
			return tab
		}
	}
	return nil
}

// memoryDialogState holds the live state of the Memory dialog.
type memoryDialogState struct {
	tree    *memoryTree
	cursor  int
	loading bool
}

// memoryReportMsg is the Bubble Tea message produced when the TUI receives
// MsgMemoryReportResp. Task 14 will emit it; Task 13 defines the type.
type memoryReportMsg struct {
	Resp ipc.MemoryReportRespPayload
}

// memoryTickMsg drives the 5-second refresh cadence for the Memory dialog
// and status-bar total.
type memoryTickMsg struct{}

// memoryTickInterval is how often the TUI asks the daemon for a fresh
// memory snapshot.
const memoryTickInterval = 5 * time.Second

// memoryTickCmd schedules the next memoryTickMsg.
func memoryTickCmd() tea.Cmd {
	return tea.Tick(memoryTickInterval, func(time.Time) tea.Msg {
		return memoryTickMsg{}
	})
}

// openMemoryDialog transitions the Model into the Memory dialog and marks
// the snapshot as loading. Task 14 will issue the IPC request; for now we
// just show a loading state until applyMemoryReport is called with data.
func (m Model) openMemoryDialog() Model {
	m.dialog = dialogMemory
	m.mem.loading = true
	m.mem.cursor = 0
	m.mem.tree = nil
	m.pendingMemoryReport = true
	return m
}

// refreshMemory issues MsgMemoryReportReq to the daemon as a fire-and-
// forget send. The corresponding MsgMemoryReportResp is dispatched by
// listenForMessages → memoryReportMsg → Update.
func (m Model) refreshMemory() tea.Cmd {
	return func() tea.Msg {
		if m.client == nil {
			return nil
		}
		msg, err := ipc.NewMessage(ipc.MsgMemoryReportReq, ipc.MemoryReportReqPayload{})
		if err != nil {
			log.Printf("refreshMemory: marshal: %v", err)
			return nil
		}
		msg.ID = fmt.Sprintf("mem-%d", time.Now().UnixNano())
		if err := m.client.Send(msg); err != nil {
			log.Printf("refreshMemory: send: %v", err)
		}
		return nil
	}
}

// applyMemoryReport rebuilds the tree from a fresh response and clamps the
// cursor into the new row count.
func (m Model) applyMemoryReport(resp ipc.MemoryReportRespPayload) Model {
	order, names := m.tabOrderAndNames()
	stored := resp
	m.lastMemResp = &stored
	if m.dialog == dialogMemory {
		m.mem.tree = buildMemoryTree(resp, order, names)
		m.mem.loading = false
		rows := m.mem.tree.flatten()
		if m.mem.cursor >= len(rows) {
			m.mem.cursor = len(rows) - 1
		}
		if m.mem.cursor < 0 {
			m.mem.cursor = 0
		}
	}
	return m
}

// tabOrderAndNames extracts the current tab order and name map from the
// Model so the tree builder can render tab headers.
func (m Model) tabOrderAndNames() ([]string, map[string]string) {
	order := make([]string, 0, len(m.tabs))
	names := make(map[string]string, len(m.tabs))
	for _, t := range m.tabs {
		if t == nil {
			continue
		}
		order = append(order, t.ID)
		names[t.ID] = t.Name
	}
	return order, names
}

// tuiLocalMem returns an estimate of TUI-side memory attributable to a
// pane. Today that's the notes editor buffer when the pane has an open
// notes editor. VT grid state is not counted — the emulator owns that and
// no public accessor exists on the pane model.
func (m Model) tuiLocalMem(paneID string) uint64 {
	if m.notesEditor != nil && m.notesEditor.PaneID() == paneID {
		return m.notesEditor.ApproxBytes()
	}
	return 0
}

// handleMemoryDialogKey processes a key press while the Memory dialog is
// open. Cursor navigation, expand/collapse, refresh, and close.
func (m Model) handleMemoryDialogKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// When loading or no tree, only Esc works.
	if m.mem.tree == nil {
		if msg.String() == "esc" {
			m.dialog = dialogNone
		}
		return m, nil
	}
	rows := m.mem.tree.flatten()
	switch msg.String() {
	case "esc":
		m.dialog = dialogNone
	case "up", "k":
		if m.mem.cursor > 0 {
			m.mem.cursor--
		}
	case "down", "j":
		if m.mem.cursor < len(rows)-1 {
			m.mem.cursor++
		}
	case "enter", " ", "right", "l":
		if m.mem.cursor < len(rows) && rows[m.mem.cursor].kind == memRowTab {
			m.mem.tree.toggleAt(m.mem.cursor)
		}
	case "left", "h":
		if m.mem.cursor < len(rows) {
			row := rows[m.mem.cursor]
			if row.kind == memRowPane {
				for i := m.mem.cursor - 1; i >= 0; i-- {
					if rows[i].tabID == row.tabID && rows[i].kind == memRowTab {
						m.mem.cursor = i
						return m, nil
					}
				}
			} else if row.kind == memRowTab {
				m.mem.tree.toggleAt(m.mem.cursor)
			}
		}
	case "r", "R":
		m.mem.loading = true
		m.pendingMemoryReport = true
		return m, m.refreshMemory()
	}
	return m, nil
}

// renderMemoryDialog produces the dialog body string. The outer
// dialogBorder wrapping is applied by the common render dispatch.
func (m Model) renderMemoryDialog() string {
	var b strings.Builder
	b.WriteString(dialogTitle.Render("Memory"))
	b.WriteByte('\n')

	if m.mem.tree == nil || m.mem.loading {
		b.WriteString("Loading snapshot...\n")
		b.WriteByte('\n')
		b.WriteString(dialogSubtle.Render("Esc close"))
		return b.String()
	}

	rows := m.mem.tree.flatten()
	for i, row := range rows {
		line := renderMemoryRow(m, row, i == m.mem.cursor)
		b.WriteString(line)
		b.WriteByte('\n')
	}

	b.WriteByte('\n')
	b.WriteString(dialogSubtle.Render("r refresh · enter/←→ expand · esc close"))
	b.WriteByte('\n')
	b.WriteString(dialogSubtle.Render("PTY RSS is OS-reported; not comparable across platforms."))
	return b.String()
}

// renderMemoryRow formats one row of the Memory dialog. Extracted from
// renderMemoryDialog to keep the loop readable.
func renderMemoryRow(m Model, row memoryRow, selected bool) string {
	style := dialogNormal
	if selected {
		style = dialogSelected
	}

	switch row.kind {
	case memRowTotal:
		return style.Render(fmt.Sprintf("  Total                                 %12s",
			memreport.HumanBytes(row.total)))
	case memRowTab:
		indicator := "▶"
		if t := m.mem.tree.findTab(row.tabID); t != nil && t.expanded {
			indicator = "▼"
		}
		return style.Render(fmt.Sprintf("%s %-36s %12s",
			indicator, truncateMem(row.label, 36), memreport.HumanBytes(row.total)))
	case memRowPane:
		tui := m.tuiLocalMem(row.paneID)
		total := row.total + tui
		return style.Render(fmt.Sprintf("    %-20s heap %8s  pty %8s  tui %8s  total %8s",
			truncateMem(row.label, 20),
			memreport.HumanBytes(row.goHeap),
			memreport.HumanBytes(row.ptyRSS),
			memreport.HumanBytes(tui),
			memreport.HumanBytes(total)))
	}
	return ""
}

// truncateMem shortens s to at most n runes, appending "…" if truncated.
// Used for pane/tab labels in the memory dialog. Rune-aware so multi-byte
// UTF-8 names (CJK, emoji) are not sliced mid-rune.
func truncateMem(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
