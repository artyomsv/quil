package tui

import (
	"sort"

	"github.com/artyomsv/quil/internal/ipc"
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
