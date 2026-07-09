package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
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

// paneState is the one-word lifecycle label shown per pane row.
func paneState(p statusPane) string {
	switch {
	case p.Pending:
		return "pending"
	case p.Running:
		return "running"
	default:
		return "stopped"
	}
}

// renderHuman renders the report as the terminal-facing text block. verbose
// adds each pane's CWD on an indented continuation line.
func renderHuman(r statusReport, verbose bool) string {
	var b strings.Builder

	if !r.Running {
		b.WriteString("quil ○ not running\n")
		return b.String()
	}
	if !r.Responding {
		if r.Pid > 0 {
			fmt.Fprintf(&b, "quil ⚠ running but not responding (pid %d)\n", r.Pid)
		} else {
			b.WriteString("quil ⚠ running but not responding\n")
		}
		return b.String()
	}

	fmt.Fprintf(&b, "quil ● running    pid %d    v%s    %s", r.Pid, r.Version, r.Environment)
	if r.HasUptime {
		fmt.Fprintf(&b, "    uptime ~%s", formatUptime(time.Since(r.StartedAt)))
	}
	b.WriteString("\n\n")

	fmt.Fprintf(&b, "tabs %d    panes %d (%d running · %d pending)    mem %s",
		r.Totals.Tabs, r.Totals.Panes, r.Totals.Running, r.Totals.Pending, formatBytes(r.Totals.MemoryBytes))
	if r.Totals.HasEvents {
		fmt.Fprintf(&b, "    events %d pending", r.Totals.PendingEvents)
	}
	b.WriteString("\n")

	for i, t := range r.Tabs {
		marker := ""
		if t.Active {
			marker = " *"
		}
		fmt.Fprintf(&b, "\n%d:%s%s\n", i+1, t.Name, marker)
		for j, p := range t.Panes {
			branch := "├"
			if j == len(t.Panes)-1 {
				branch = "└"
			}
			memStr := "—"
			if p.HasMem {
				memStr = formatBytes(p.MemBytes)
			}
			fmt.Fprintf(&b, "  %s %-12s %-12s %-9s %s\n", branch, p.Name, p.Type, paneState(p), memStr)
			if verbose && p.CWD != "" {
				fmt.Fprintf(&b, "  │   %s\n", p.CWD)
			}
		}
	}
	return b.String()
}

// JSON wire shapes. Pointers / omitempty control conditional presence:
//   - not running        → {"running":false}
//   - running but wedged  → {"running":true,"responding":false,...}
//   - healthy             → full object
type jsonTotals struct {
	Tabs          int    `json:"tabs"`
	Panes         int    `json:"panes"`
	Running       int    `json:"running"`
	Pending       int    `json:"pending"`
	MemoryBytes   uint64 `json:"memory_bytes"`
	PendingEvents *int   `json:"pending_events,omitempty"`
}

type jsonPane struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Running     bool   `json:"running"`
	Pending     bool   `json:"pending"`
	CWD         string `json:"cwd"`
	MemoryBytes uint64 `json:"memory_bytes"`
}

type jsonTab struct {
	ID     string     `json:"id"`
	Name   string     `json:"name"`
	Active bool       `json:"active"`
	Panes  []jsonPane `json:"panes"`
}

type jsonReport struct {
	Running        bool        `json:"running"`
	Responding     *bool       `json:"responding,omitempty"`
	Pid            int         `json:"pid,omitempty"`
	Version        string      `json:"version,omitempty"`
	Environment    string      `json:"environment,omitempty"`
	EnvironmentDir string      `json:"environment_dir,omitempty"`
	UptimeSeconds  *int64      `json:"uptime_seconds,omitempty"`
	StartedAt      string      `json:"started_at,omitempty"`
	Totals         *jsonTotals `json:"totals,omitempty"`
	Tabs           []jsonTab   `json:"tabs,omitempty"`
}

// renderJSON marshals the report. Marshalling never fails for these types, but
// the error is returned for caller symmetry.
func renderJSON(r statusReport) ([]byte, error) {
	jr := jsonReport{Running: r.Running}
	if !r.Running {
		return json.Marshal(jr)
	}

	responding := r.Responding
	jr.Responding = &responding
	jr.Pid = r.Pid
	jr.Version = r.Version

	if !r.Responding {
		return json.Marshal(jr)
	}

	jr.Environment = r.Environment
	if r.Environment == "dev" {
		jr.EnvironmentDir = r.EnvDir
	}
	if r.HasUptime {
		secs := int64(time.Since(r.StartedAt).Seconds())
		jr.UptimeSeconds = &secs
		jr.StartedAt = r.StartedAt.UTC().Format(time.RFC3339)
	}

	totals := jsonTotals{
		Tabs:        r.Totals.Tabs,
		Panes:       r.Totals.Panes,
		Running:     r.Totals.Running,
		Pending:     r.Totals.Pending,
		MemoryBytes: r.Totals.MemoryBytes,
	}
	if r.Totals.HasEvents {
		ev := r.Totals.PendingEvents
		totals.PendingEvents = &ev
	}
	jr.Totals = &totals

	for _, t := range r.Tabs {
		jt := jsonTab{ID: t.ID, Name: t.Name, Active: t.Active}
		for _, p := range t.Panes {
			jt.Panes = append(jt.Panes, jsonPane{
				ID: p.ID, Name: p.Name, Type: p.Type,
				Running: p.Running, Pending: p.Pending,
				CWD: p.CWD, MemoryBytes: p.MemBytes,
			})
		}
		jr.Tabs = append(jr.Tabs, jt)
	}

	return json.Marshal(jr)
}
