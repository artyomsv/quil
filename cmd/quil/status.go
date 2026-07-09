package main

import (
	"fmt"
	"os"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// formatBytes renders a byte count as B / KB / MB / GB with one decimal.
func formatBytes(n uint64) string {
	const (
		kb = 1024
		mb = kb * 1024
		gb = mb * 1024
	)
	switch {
	case n >= gb:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(gb))
	case n >= mb:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(mb))
	case n >= kb:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(kb))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// formatUptime renders a duration as a coarse, human-friendly string.
func formatUptime(d time.Duration) string {
	if d < time.Minute {
		return "<1m"
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd%dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh%dm", hours, mins)
	default:
		return fmt.Sprintf("%dm", mins)
	}
}

type statusPane struct {
	ID       string
	TabID    string
	Name     string
	Type     string
	CWD      string
	Running  bool
	Pending  bool
	HasMem   bool
	MemBytes uint64
}

type statusTab struct {
	ID     string
	Name   string
	Active bool
	Panes  []statusPane
}

type statusTotals struct {
	Tabs          int
	Panes         int
	Running       int
	Pending       int
	PendingEvents int
	MemoryBytes   uint64
	HasEvents     bool
}

type statusReport struct {
	Running     bool
	Responding  bool
	HasUptime   bool
	Pid         int
	Version     string
	Environment string
	EnvDir      string
	StartedAt   time.Time
	Totals      statusTotals
	Tabs        []statusTab
}

// envMode reports "dev" when QUIL_HOME is set, else "production" — the same
// signal envDescription() uses for its combined label.
func envMode() string {
	if os.Getenv("QUIL_HOME") != "" {
		return "dev"
	}
	return "production"
}

// mergePaneMemory joins pane metadata with the memory collector's per-pane
// totals by pane ID. Panes with no memory entry (e.g. pending, not yet
// spawned) get HasMem=false; memory entries with no matching pane are dropped.
func mergePaneMemory(panes []ipc.PaneInfo, mem []ipc.PaneMemInfo) []statusPane {
	byID := make(map[string]uint64, len(mem))
	for _, pm := range mem {
		byID[pm.PaneID] = pm.TotalBytes
	}
	out := make([]statusPane, 0, len(panes))
	for _, p := range panes {
		sp := statusPane{
			ID:      p.ID,
			TabID:   p.TabID,
			Name:    p.Name,
			Type:    p.Type,
			CWD:     p.CWD,
			Running: p.Running,
			Pending: p.Pending,
		}
		if b, ok := byID[p.ID]; ok {
			sp.MemBytes = b
			sp.HasMem = true
		}
		out = append(out, sp)
	}
	return out
}

// buildStatus assembles the full in-memory report from the composed IPC
// responses. Tab order and metadata come from mem.Tabs; per-pane details from
// panes; memory is joined by pane ID. Totals are computed from the merged
// panes so the header numbers always match the rendered rows.
func buildStatus(version string, pid int, panes []ipc.PaneInfo, mem ipc.MemoryReportRespPayload, events int, hasEvents bool, startedAt time.Time) statusReport {
	merged := mergePaneMemory(panes, mem.Panes)

	byTab := make(map[string][]statusPane, len(mem.Tabs))
	for _, sp := range merged {
		byTab[sp.TabID] = append(byTab[sp.TabID], sp)
	}

	tabs := make([]statusTab, 0, len(mem.Tabs))
	for _, t := range mem.Tabs {
		tabs = append(tabs, statusTab{
			ID:     t.ID,
			Name:   t.Name,
			Active: t.Active,
			Panes:  byTab[t.ID],
		})
	}

	var totals statusTotals
	totals.Tabs = len(tabs)
	for _, sp := range merged {
		totals.Panes++
		switch {
		case sp.Pending:
			totals.Pending++
		case sp.Running:
			totals.Running++
		}
		totals.MemoryBytes += sp.MemBytes
	}
	totals.PendingEvents = events
	totals.HasEvents = hasEvents

	return statusReport{
		Running:     true,
		Responding:  true,
		HasUptime:   !startedAt.IsZero(),
		Pid:         pid,
		Version:     version,
		Environment: envMode(),
		EnvDir:      config.QuilDir(),
		StartedAt:   startedAt,
		Totals:      totals,
		Tabs:        tabs,
	}
}
