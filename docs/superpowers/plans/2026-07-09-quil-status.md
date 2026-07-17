# `quil status` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a top-level `quil status` command that reports daemon liveness and workspace session metrics in a human-readable tree (default) or `--json`.

**Architecture:** A new `cmd/quil/status.go` connects to the daemon as a plain IPC client (no attach) and composes three existing request-response messages — `MsgVersionReq`, `MsgListPanesReq`, `MsgMemoryReportReq` — plus an optional `MsgGetNotificationsReq`. Pure functions build an in-memory `statusReport` and render it as text or JSON; the thin IPC glue stays untested per convention. Zero daemon-side changes.

**Tech Stack:** Go 1.25, existing `internal/ipc` client, `github.com/google/uuid` (already a dependency), stdlib `encoding/json` / `strings` / `time`.

## Global Constraints

- **Package:** all new code is `package main` in `cmd/quil/`.
- **Zero daemon changes:** do not modify `internal/daemon/` or `internal/ipc/protocol.go`. Only compose existing messages.
- **Reuse existing helpers:** `daemonPID()`, `parsePidData()`, `envDescription()` (all `cmd/quil/daemonctl.go`); `config.SocketPath()`, `config.PidPath()`, `config.QuilDir()`.
- **Go conventions:** `gofmt` mandatory, tabs, wrap errors with `%w`, acronyms uppercase (`ID`, `CWD`, `JSON`), no `init()`.
- **Testing:** table-driven white-box tests in `package main`, `TestFn_Scenario_Expected` naming. The IPC round-trip glue (`runStatus`, `statusRoundTrip`) is thin I/O — not unit-tested (per `cmd/*/main.go` convention); verify it manually against a dev daemon.
- **Git (user rules):** imperative subject ≤72 chars; **no AI/model/vendor attribution**, no `Co-Authored-By`; never commit the spec/plan docs or WIP. The per-task commits below are the meaningful units — do not add extra WIP commits.
- **Timeout:** `statusTimeout = 2 * time.Second` per round-trip.
- **Exit codes:** `0` healthy, `1` not running (dial failed), `2` running-but-wedged (a core round-trip timed out) or bad flags.

---

### Task 1: Formatters (`formatBytes`, `formatUptime`)

Pure, dependency-free helpers. Foundation for the renderers.

**Files:**
- Create: `cmd/quil/status.go` (formatters only for now)
- Test: `cmd/quil/status_test.go`

**Interfaces:**
- Produces: `formatBytes(n uint64) string`, `formatUptime(d time.Duration) string`

- [ ] **Step 1: Write the failing tests**

Create `cmd/quil/status_test.go`:

```go
package main

import (
	"testing"
	"time"
)

func TestFormatBytes_Boundaries(t *testing.T) {
	tests := []struct {
		name string
		in   uint64
		want string
	}{
		{"zero", 0, "0 B"},
		{"sub-kb", 512, "512 B"},
		{"one-kb", 1024, "1.0 KB"},
		{"kb-fraction", 1536, "1.5 KB"},
		{"one-mb", 1024 * 1024, "1.0 MB"},
		{"mb-fraction", 1024*1024*3 + 400*1024, "3.4 MB"},
		{"one-gb", 1024 * 1024 * 1024, "1.0 GB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatBytes(tt.in); got != tt.want {
				t.Errorf("formatBytes(%d) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestFormatUptime_Boundaries(t *testing.T) {
	tests := []struct {
		name string
		in   time.Duration
		want string
	}{
		{"under-a-minute", 30 * time.Second, "<1m"},
		{"minutes", 5 * time.Minute, "5m"},
		{"hours-minutes", 2*time.Hour + 13*time.Minute, "2h13m"},
		{"days-hours", 3*24*time.Hour + 4*time.Hour, "3d4h"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatUptime(tt.in); got != tt.want {
				t.Errorf("formatUptime(%v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `./scripts/dev.sh test 2>&1 | grep -E "status|formatBytes|formatUptime|undefined"`
Expected: FAIL — `undefined: formatBytes`, `undefined: formatUptime`.

- [ ] **Step 3: Write the formatters**

Create `cmd/quil/status.go`:

```go
package main

import (
	"fmt"
	"time"
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `./scripts/dev.sh test 2>&1 | grep -E "FormatBytes|FormatUptime|ok.*cmd/quil"`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/quil/status.go cmd/quil/status_test.go
git commit -m "feat(status): add byte and uptime formatters"
```

---

### Task 2: Status model, `mergePaneMemory`, `buildStatus`

The in-memory report and the pure join/aggregation logic.

**Files:**
- Modify: `cmd/quil/status.go` (add types + two functions)
- Test: `cmd/quil/status_test.go`

**Interfaces:**
- Consumes: `ipc.PaneInfo`, `ipc.PaneMemInfo`, `ipc.MemoryReportRespPayload`, `ipc.TabInfo` (from `internal/ipc/protocol.go`); `envMode()`, `config.QuilDir()`.
- Produces:
  - `type statusPane struct { ID, TabID, Name, Type, CWD string; Running, Pending, HasMem bool; MemBytes uint64 }`
  - `type statusTab struct { ID, Name string; Active bool; Panes []statusPane }`
  - `type statusTotals struct { Tabs, Panes, Running, Pending, PendingEvents int; MemoryBytes uint64; HasEvents bool }`
  - `type statusReport struct { Running, Responding, HasUptime bool; Pid int; Version, Environment, EnvDir string; StartedAt time.Time; Totals statusTotals; Tabs []statusTab }`
  - `mergePaneMemory(panes []ipc.PaneInfo, mem []ipc.PaneMemInfo) []statusPane`
  - `buildStatus(version string, pid int, panes []ipc.PaneInfo, mem ipc.MemoryReportRespPayload, events int, hasEvents bool, startedAt time.Time) statusReport`
  - `envMode() string`

- [ ] **Step 1: Write the failing tests**

Append to `cmd/quil/status_test.go`:

```go
func TestMergePaneMemory_MatchAndGaps(t *testing.T) {
	panes := []ipc.PaneInfo{
		{ID: "p1", TabID: "t1", Name: "shell", Type: "terminal", Running: true},
		{ID: "p2", TabID: "t1", Name: "notes", Type: "terminal", Pending: true},
	}
	mem := []ipc.PaneMemInfo{
		{PaneID: "p1", TotalBytes: 1200},
		{PaneID: "pX", TotalBytes: 999}, // stale — no matching pane, ignored
	}
	got := mergePaneMemory(panes, mem)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if !got[0].HasMem || got[0].MemBytes != 1200 {
		t.Errorf("p1 mem = (%v,%d), want (true,1200)", got[0].HasMem, got[0].MemBytes)
	}
	if got[1].HasMem {
		t.Errorf("p2 (pending) should have no memory entry")
	}
}

func TestBuildStatus_TotalsAndGrouping(t *testing.T) {
	panes := []ipc.PaneInfo{
		{ID: "p1", TabID: "t1", Name: "shell", Type: "terminal", Running: true},
		{ID: "p2", TabID: "t1", Name: "notes", Type: "terminal", Pending: true},
		{ID: "p3", TabID: "t2", Name: "claude", Type: "claude-code", Running: true},
	}
	mem := ipc.MemoryReportRespPayload{
		Panes: []ipc.PaneMemInfo{
			{PaneID: "p1", TotalBytes: 1000},
			{PaneID: "p3", TotalBytes: 2000},
		},
		Tabs: []ipc.TabInfo{
			{ID: "t1", Name: "Shell", Active: true},
			{ID: "t2", Name: "AI"},
		},
	}
	started := time.Now().Add(-2 * time.Hour)
	r := buildStatus("1.2.3", 42, panes, mem, 3, true, started)

	if !r.Running || !r.Responding {
		t.Fatalf("running/responding = %v/%v, want true/true", r.Running, r.Responding)
	}
	if r.Totals.Tabs != 2 || r.Totals.Panes != 3 {
		t.Errorf("tabs/panes = %d/%d, want 2/3", r.Totals.Tabs, r.Totals.Panes)
	}
	if r.Totals.Running != 2 || r.Totals.Pending != 1 {
		t.Errorf("running/pending = %d/%d, want 2/1", r.Totals.Running, r.Totals.Pending)
	}
	if r.Totals.MemoryBytes != 3000 {
		t.Errorf("memory = %d, want 3000", r.Totals.MemoryBytes)
	}
	if !r.Totals.HasEvents || r.Totals.PendingEvents != 3 {
		t.Errorf("events = (%v,%d), want (true,3)", r.Totals.HasEvents, r.Totals.PendingEvents)
	}
	if len(r.Tabs) != 2 || r.Tabs[0].Name != "Shell" || !r.Tabs[0].Active {
		t.Fatalf("tab[0] = %+v, want Shell/active", r.Tabs[0])
	}
	if len(r.Tabs[0].Panes) != 2 || len(r.Tabs[1].Panes) != 1 {
		t.Errorf("pane grouping = %d/%d, want 2/1", len(r.Tabs[0].Panes), len(r.Tabs[1].Panes))
	}
	if !r.HasUptime {
		t.Errorf("HasUptime = false, want true")
	}
}

func TestBuildStatus_NoUptimeWhenZeroTime(t *testing.T) {
	r := buildStatus("1.2.3", 42, nil, ipc.MemoryReportRespPayload{}, 0, false, time.Time{})
	if r.HasUptime {
		t.Errorf("HasUptime = true for zero StartedAt, want false")
	}
	if r.Totals.HasEvents {
		t.Errorf("HasEvents = true, want false")
	}
}
```

Add the import to the top of `status_test.go`:

```go
	"github.com/artyomsv/quil/internal/ipc"
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `./scripts/dev.sh test 2>&1 | grep -E "undefined|mergePaneMemory|buildStatus"`
Expected: FAIL — `undefined: mergePaneMemory`, `undefined: buildStatus`.

- [ ] **Step 3: Write the model and functions**

Add to `cmd/quil/status.go` (extend the import block to include `os`, `github.com/artyomsv/quil/internal/config`, `github.com/artyomsv/quil/internal/ipc`):

```go
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `./scripts/dev.sh test 2>&1 | grep -E "MergePaneMemory|BuildStatus|ok.*cmd/quil"`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/quil/status.go cmd/quil/status_test.go
git commit -m "feat(status): add report model and aggregation"
```

---

### Task 3: Renderers (`renderHuman`, `renderJSON`, `paneState`)

Turn a `statusReport` into text or JSON.

**Files:**
- Modify: `cmd/quil/status.go`
- Test: `cmd/quil/status_test.go`

**Interfaces:**
- Consumes: `statusReport`, `formatBytes`, `formatUptime`.
- Produces: `renderHuman(r statusReport, verbose bool) string`, `renderJSON(r statusReport) ([]byte, error)`, `paneState(p statusPane) string`.

- [ ] **Step 1: Write the failing tests**

Append to `cmd/quil/status_test.go` (add `"encoding/json"` and `"strings"` to imports):

```go
func TestRenderHuman_NotRunning(t *testing.T) {
	out := renderHuman(statusReport{Running: false}, false)
	if !strings.Contains(out, "not running") {
		t.Errorf("output = %q, want it to mention 'not running'", out)
	}
}

func TestRenderHuman_Wedged(t *testing.T) {
	out := renderHuman(statusReport{Running: true, Responding: false, Pid: 77}, false)
	if !strings.Contains(out, "not responding") || !strings.Contains(out, "77") {
		t.Errorf("output = %q, want 'not responding' and pid 77", out)
	}
}

func TestRenderHuman_HealthyTree(t *testing.T) {
	r := statusReport{
		Running: true, Responding: true, Pid: 42, Version: "1.2.3",
		Environment: "production", HasUptime: true, StartedAt: time.Now().Add(-time.Hour),
		Totals: statusTotals{Tabs: 1, Panes: 2, Running: 1, Pending: 1, MemoryBytes: 1024 * 1024, PendingEvents: 3, HasEvents: true},
		Tabs: []statusTab{{
			ID: "t1", Name: "Shell", Active: true,
			Panes: []statusPane{
				{Name: "shell", Type: "terminal", Running: true, HasMem: true, MemBytes: 1024 * 1024},
				{Name: "notes", Type: "terminal", Pending: true},
			},
		}},
	}
	out := renderHuman(r, false)
	for _, want := range []string{"running", "pid 42", "v1.2.3", "production", "1:Shell *", "shell", "pending", "—", "events 3 pending"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRenderHuman_VerboseShowsCWD(t *testing.T) {
	r := statusReport{
		Running: true, Responding: true,
		Tabs: []statusTab{{Name: "T", Panes: []statusPane{{Name: "p", Type: "terminal", Running: true, CWD: "/home/x/proj"}}}},
	}
	if strings.Contains(renderHuman(r, false), "/home/x/proj") {
		t.Errorf("non-verbose output should not contain CWD")
	}
	if !strings.Contains(renderHuman(r, true), "/home/x/proj") {
		t.Errorf("verbose output should contain CWD")
	}
}

func TestRenderJSON_OmitsAndIncludes(t *testing.T) {
	// Not running → just {"running":false}
	b, err := renderJSON(statusReport{Running: false})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["running"] != false {
		t.Errorf("running = %v, want false", m["running"])
	}
	if _, ok := m["responding"]; ok {
		t.Errorf("responding should be omitted when not running")
	}
	if _, ok := m["totals"]; ok {
		t.Errorf("totals should be omitted when not running")
	}

	// Healthy with events + uptime
	r := statusReport{
		Running: true, Responding: true, Pid: 5, Version: "9.9.9", Environment: "dev",
		HasUptime: true, StartedAt: time.Now().Add(-time.Minute),
		Totals: statusTotals{Tabs: 1, Panes: 1, Running: 1, PendingEvents: 2, HasEvents: true},
		Tabs:   []statusTab{{ID: "t", Name: "T", Active: true, Panes: []statusPane{{ID: "p", Name: "p", Type: "terminal", Running: true, HasMem: true, MemBytes: 10}}}},
	}
	b, err = renderJSON(r)
	if err != nil {
		t.Fatal(err)
	}
	m = nil
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["responding"] != true {
		t.Errorf("responding = %v, want true", m["responding"])
	}
	totals, ok := m["totals"].(map[string]any)
	if !ok {
		t.Fatalf("totals missing or wrong type: %v", m["totals"])
	}
	if totals["pending_events"].(float64) != 2 {
		t.Errorf("pending_events = %v, want 2", totals["pending_events"])
	}
	if _, ok := m["uptime_seconds"]; !ok {
		t.Errorf("uptime_seconds should be present when HasUptime")
	}
}

func TestRenderJSON_OmitsEventsWhenUnknown(t *testing.T) {
	r := statusReport{
		Running: true, Responding: true,
		Totals: statusTotals{Tabs: 0, Panes: 0, HasEvents: false},
	}
	b, _ := renderJSON(r)
	var m map[string]any
	json.Unmarshal(b, &m)
	totals := m["totals"].(map[string]any)
	if _, ok := totals["pending_events"]; ok {
		t.Errorf("pending_events should be omitted when HasEvents is false")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `./scripts/dev.sh test 2>&1 | grep -E "undefined|renderHuman|renderJSON"`
Expected: FAIL — `undefined: renderHuman`, `undefined: renderJSON`.

- [ ] **Step 3: Write the renderers**

Add to `cmd/quil/status.go` (extend imports with `"encoding/json"` and `"strings"`):

```go
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
	Running        bool         `json:"running"`
	Responding     *bool        `json:"responding,omitempty"`
	Pid            int          `json:"pid,omitempty"`
	Version        string       `json:"version,omitempty"`
	Environment    string       `json:"environment,omitempty"`
	EnvironmentDir string       `json:"environment_dir,omitempty"`
	UptimeSeconds  *int64       `json:"uptime_seconds,omitempty"`
	StartedAt      string       `json:"started_at,omitempty"`
	Totals         *jsonTotals  `json:"totals,omitempty"`
	Tabs           []jsonTab    `json:"tabs,omitempty"`
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `./scripts/dev.sh test 2>&1 | grep -E "RenderHuman|RenderJSON|ok.*cmd/quil"`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/quil/status.go cmd/quil/status_test.go
git commit -m "feat(status): add human and JSON renderers"
```

---

### Task 4: Command glue + `main.go` wiring

The IPC round-trip and command entry. Not unit-tested (thin I/O) — verified manually against a dev daemon.

**Files:**
- Modify: `cmd/quil/status.go` (add `runStatus`, `statusRoundTrip`, `emitStatus`, `statusTimeout`)
- Modify: `cmd/quil/main.go` (add top-level `case "status"`; route `daemon status` to `runStatus`; delete old `daemonStatus()`)
- Modify: `cmd/quil/daemonctl.go` (optional: `envDescription` reuses `envMode`)

**Interfaces:**
- Consumes: `ipc.NewClient`, `ipc.NewMessage`, `Client.Send/Receive/SetReadDeadline/Close`, `config.SocketPath/PidPath`, `daemonPID()`, `buildStatus`, `renderHuman`, `renderJSON`, `uuid.New`.
- Produces: `runStatus(args []string)`.

- [ ] **Step 1: Add the command glue to `status.go`**

Extend the import block with `"os"`, `"github.com/artyomsv/quil/internal/ipc"`, `"github.com/google/uuid"` (config/ipc already added in Task 2), then append:

```go
const statusTimeout = 2 * time.Second

// statusRoundTrip sends one request-response message on a fresh (non-attached)
// connection and decodes the matching reply into out. Replies are correlated
// by Message.ID; any interleaved broadcast frame is skipped. A single read
// deadline bounds the whole exchange.
func statusRoundTrip(c *ipc.Client, typ string, payload any, out any) error {
	id := uuid.New().String()
	msg, err := ipc.NewMessage(typ, payload)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", typ, err)
	}
	msg.ID = id
	if err := c.SetReadDeadline(time.Now().Add(statusTimeout)); err != nil {
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
			continue
		}
		if out == nil {
			return nil
		}
		return resp.DecodePayload(out)
	}
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
```

- [ ] **Step 2: Wire `main.go` and delete the old `daemonStatus`**

In `cmd/quil/main.go`, add a top-level case in the `switch os.Args[1]` block (after the `restart` case, around line 98-103):

```go
		case "status":
			runStatus(os.Args[2:])
			return
```

In `handleDaemon()` (around line 127), change the `status` case body:

```go
	case "status":
		runStatus(os.Args[3:])
```

Delete the now-unused `daemonStatus()` function (main.go:233-247).

- [ ] **Step 3: DRY the environment helper (optional but preferred)**

In `cmd/quil/daemonctl.go`, rewrite `envDescription` to reuse `envMode`:

```go
func envDescription() string {
	return fmt.Sprintf("%s (%s)", envMode(), config.QuilDir())
}
```

- [ ] **Step 4: Build and vet**

Run: `./scripts/dev.sh build && ./scripts/dev.sh vet`
Expected: builds all 6 binaries; vet clean. (Fixes any unused-import errors from the deleted `daemonStatus` — e.g. `strings` may no longer be needed in main.go; remove it if vet flags it.)

- [ ] **Step 5: Manual verification against the dev daemon**

Per `.claude/rules/dev-environment.md`, use the dev instance only. With a dev daemon running (launch `./scripts/quil-dev.ps1`, confirm `[dev]` in the status bar, create a couple of tabs/panes), from a separate shell with `QUIL_HOME` pointed at the project `.quil/`:

```bash
QUIL_HOME=$(pwd)/.quil ./quil-dev.exe status
QUIL_HOME=$(pwd)/.quil ./quil-dev.exe status --json
QUIL_HOME=$(pwd)/.quil ./quil-dev.exe status -v
```

Expected: healthy header with `production`→`dev (…/.quil)`, tab/pane tree, `mem`, and (if any events) `events N pending`; `--json` parses via `jq .`; exit code 0 (`echo $?`).

Then stop the dev daemon and re-run: expect `quil ○ not running` and exit code 1.

**Do NOT** run `quil status` without `--dev`/`QUIL_HOME` — that targets the production daemon (read-only, but out of scope for dev per the isolation rule).

- [ ] **Step 6: Run the full test + race suite**

Run: `./scripts/dev.sh test && ./scripts/dev.sh test-race 2>&1 | tail -5`
Expected: all pass.

- [ ] **Step 7: Commit**

```bash
git add cmd/quil/status.go cmd/quil/main.go cmd/quil/daemonctl.go
git commit -m "feat(status): add quil status command and wiring"
```

---

### Task 5: Documentation

**Files:**
- Modify: `docs/troubleshooting.md` (log/diagnostic section) and/or `docs/mcp.md`/CLI reference — add `quil status`
- Modify: `docs/roadmap.md` (mark the observability item done)

- [ ] **Step 1: Document the command**

Add a short section to `docs/troubleshooting.md` (it already covers "daemon won't start" / log locations — `quil status` is the natural first diagnostic):

```markdown
## Checking daemon + session status

`quil status` reports whether the daemon is running and prints a live snapshot
of the workspace:

```
quil status            # human-readable tree
quil status -v         # also show each pane's working directory
quil status --json     # machine-readable, for scripts/CI
```

Exit codes: `0` healthy, `1` daemon not running, `2` daemon running but not
responding (wedged). `quil daemon status` is an alias.

Add `--dev` (or set `QUIL_HOME`) to inspect a dev instance instead of production.
```

- [ ] **Step 2: Mark the roadmap item**

In `docs/roadmap.md`, under M5 → Remaining, change the observability line to reflect `quil status` shipping (move it to Completed or annotate), leaving `session metrics` covered:

```markdown
- **Observability — `quil status`** — top-level command (alias `quil daemon
  status`) reporting daemon liveness, pid, version, environment, approximate
  uptime, and per-tab/pane session metrics (state + memory), with `--json` for
  scripting. Exit codes distinguish healthy / not-running / wedged.
```

- [ ] **Step 3: Commit**

```bash
git add docs/troubleshooting.md docs/roadmap.md
git commit -m "docs: document quil status command"
```

---

## Self-Review

**Spec coverage:**
- Command surface (`quil status`, `--json`, `-v`, `daemon status` alias, `--dev`) → Task 4. ✓
- Data flow (round-trip helper, 3 composed reqs + optional events, pidfile uptime, env label) → Task 4 + `buildStatus`/`envMode` in Task 2. ✓
- Human output (header, totals, per-tab tree, `*` marker, `—` for pending, `-v` CWD) → Task 3. ✓
- JSON output (conditional omission of responding/totals/uptime/events, dir on dev) → Task 3. ✓
- Error/edge (not running=1, wedged=2, optional-segment degrade) → Task 4 (`runStatus`), Task 3 (renderers). ✓
- Testability split (`mergePaneMemory`, `buildStatus`, renderers, formatters) → Tasks 1-3. ✓
- Files-touched table → matches Tasks 1-5. ✓
- Eager-marker deviation (dropped) → reflected: no `Eager` field anywhere in the model/JSON. ✓

**Placeholder scan:** No TBD/TODO; every code step shows complete code; every test step shows real assertions. ✓

**Type consistency:** `statusReport`/`statusTab`/`statusPane`/`statusTotals` field names are identical across Tasks 2-4. `buildStatus` signature matches its call in `runStatus`. `mergePaneMemory` returns `[]statusPane` consumed by `buildStatus`. `renderHuman(r, verbose)` / `renderJSON(r)` signatures match `emitStatus` calls. `MemoryReportReqPayload{}` (empty struct) and `struct{}{}` payloads match the message contract. ✓
