package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/google/uuid"
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
// panes; memory is joined by pane ID. Totals are summed over the panes grouped
// into rendered tabs (not over all merged panes) so a pane whose tab was
// destroyed between the ListPanes and MemoryReport round-trips — its TabID
// absent from mem.Tabs — is excluded from BOTH the tree and the totals, and the
// two can never diverge.
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
	for _, t := range tabs {
		for _, sp := range t.Panes {
			totals.Panes++
			switch {
			case sp.Pending:
				totals.Pending++
			case sp.Running:
				totals.Running++
			}
			totals.MemoryBytes += sp.MemBytes
		}
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
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Running bool   `json:"running"`
	Pending bool   `json:"pending"`
	CWD     string `json:"cwd"`
	// MemoryBytes is a pointer so an unsampled pane (pending, or not yet seen
	// by the 5 s memreport collector) serializes as JSON null — distinct from a
	// real zero-byte sample. Mirrors the human renderer's "—".
	MemoryBytes *uint64 `json:"memory_bytes"`
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
			jp := jsonPane{
				ID: p.ID, Name: p.Name, Type: p.Type,
				Running: p.Running, Pending: p.Pending, CWD: p.CWD,
			}
			if p.HasMem {
				mb := p.MemBytes
				jp.MemoryBytes = &mb // else JSON null: unsampled, not real zero
			}
			jt.Panes = append(jt.Panes, jp)
		}
		jr.Tabs = append(jr.Tabs, jt)
	}

	return json.Marshal(jr)
}

const (
	// statusIdleTimeout bounds the wait for the NEXT frame, re-armed on every
	// frame received. A non-attached status conn still receives the daemon's
	// broadcasts (pane output, state updates); re-arming per frame means a
	// healthy-but-chatty daemon keeps the connection visibly alive and never
	// trips the wedge path — only genuine silence (no frames at all) does.
	statusIdleTimeout = 2 * time.Second
	// statusTotalBudget caps the whole exchange so a partial wedge — broadcasts
	// still flowing but our specific request never answered — can't loop
	// forever re-arming the idle deadline.
	statusTotalBudget = 8 * time.Second
)

// statusRoundTrip sends one request-response message on a fresh (non-attached)
// connection and decodes the matching reply into out. Replies are correlated
// by Message.ID; interleaved broadcast frames are skipped (and re-arm the idle
// deadline). The exchange is bounded by statusIdleTimeout per frame and
// statusTotalBudget overall.
func statusRoundTrip(c *ipc.Client, typ string, payload any, out any) error {
	id := uuid.New().String()
	msg, err := ipc.NewMessage(typ, payload)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", typ, err)
	}
	msg.ID = id
	overall := time.Now().Add(statusTotalBudget)
	if err := c.SetReadDeadline(earlier(time.Now().Add(statusIdleTimeout), overall)); err != nil {
		return err
	}
	if err := c.Send(msg); err != nil {
		return fmt.Errorf("send %s: %w", typ, err)
	}
	for {
		resp, err := c.Receive()
		if err != nil {
			return fmt.Errorf("receive %s: %w", typ, err)
		}
		if resp.ID != id {
			// A broadcast frame delivered to this non-attached conn — the
			// daemon is alive. Re-arm the idle deadline (bounded by the overall
			// budget) and keep looking for our reply.
			if err := c.SetReadDeadline(earlier(time.Now().Add(statusIdleTimeout), overall)); err != nil {
				return err
			}
			continue
		}
		if out == nil {
			return nil
		}
		return resp.DecodePayload(out)
	}
}

// earlier returns the earlier of two times — the effective read deadline is the
// idle window unless the overall budget would elapse first.
func earlier(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// emitStatus writes the report in the requested format and exits with code.
func emitStatus(r statusReport, jsonOut, verbose bool, code int) {
	if jsonOut {
		if b, err := renderJSON(r); err == nil {
			fmt.Println(string(b))
		}
	} else {
		fmt.Print(renderHuman(r, verbose))
	}
	os.Exit(code)
}

// runStatus is the entry point for `quil status` and `quil daemon status`.
func runStatus(args []string) {
	jsonOut, verbose := false, false
	for _, a := range args {
		switch a {
		case "--json":
			jsonOut = true
		case "-v", "--verbose":
			verbose = true
		default:
			fmt.Fprintf(os.Stderr, "unknown flag: %s\nusage: quil status [--json] [-v]\n", a)
			os.Exit(2)
		}
	}

	pid := daemonPID()

	client, err := ipc.NewClient(config.SocketPath())
	if err != nil {
		emitStatus(statusReport{Running: false}, jsonOut, verbose, 1)
	}
	defer client.Close()

	var ver ipc.VersionRespPayload
	if err := statusRoundTrip(client, ipc.MsgVersionReq, struct{}{}, &ver); err != nil {
		emitStatus(statusReport{Running: true, Responding: false, Pid: pid}, jsonOut, verbose, 2)
	}

	var panes ipc.ListPanesRespPayload
	if err := statusRoundTrip(client, ipc.MsgListPanesReq, struct{}{}, &panes); err != nil {
		emitStatus(statusReport{Running: true, Responding: false, Pid: pid, Version: ver.Version}, jsonOut, verbose, 2)
	}

	var mem ipc.MemoryReportRespPayload
	if err := statusRoundTrip(client, ipc.MsgMemoryReportReq, ipc.MemoryReportReqPayload{}, &mem); err != nil {
		emitStatus(statusReport{Running: true, Responding: false, Pid: pid, Version: ver.Version}, jsonOut, verbose, 2)
	}

	// Optional: pending notification count. Degrades to "unknown" on any error.
	events, hasEvents := 0, false
	var notif ipc.GetNotificationsRespPayload
	if err := statusRoundTrip(client, ipc.MsgGetNotificationsReq, struct{}{}, &notif); err == nil {
		events, hasEvents = len(notif.Events), true
	}

	var startedAt time.Time
	if fi, err := os.Stat(config.PidPath()); err == nil {
		startedAt = fi.ModTime()
	}

	report := buildStatus(ver.Version, pid, panes.Panes, mem, events, hasEvents, startedAt)
	emitStatus(report, jsonOut, verbose, 0)
}
