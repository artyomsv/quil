package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

func TestBuildStatus_OrphanTabPaneExcludedFromTotals(t *testing.T) {
	// A pane whose TabID is absent from mem.Tabs (its tab destroyed between the
	// ListPanes and MemoryReport round-trips) must be excluded from totals, so
	// totals never exceed what the rendered tabs contain.
	panes := []ipc.PaneInfo{
		{ID: "p1", TabID: "t1", Name: "shell", Type: "terminal", Running: true},
		{ID: "orphan", TabID: "gone", Name: "x", Type: "terminal", Running: true},
	}
	mem := ipc.MemoryReportRespPayload{
		Panes: []ipc.PaneMemInfo{{PaneID: "p1", TotalBytes: 100}, {PaneID: "orphan", TotalBytes: 500}},
		Tabs:  []ipc.TabInfo{{ID: "t1", Name: "Shell", Active: true}},
	}
	r := buildStatus("v", 1, panes, mem, 0, false, time.Time{})

	if r.Totals.Panes != 1 || r.Totals.Running != 1 {
		t.Errorf("totals panes/running = %d/%d, want 1/1 (orphan excluded)", r.Totals.Panes, r.Totals.Running)
	}
	if r.Totals.MemoryBytes != 100 {
		t.Errorf("totals memory = %d, want 100 (orphan's 500 excluded)", r.Totals.MemoryBytes)
	}
	rendered := 0
	for _, tb := range r.Tabs {
		rendered += len(tb.Panes)
	}
	if rendered != r.Totals.Panes {
		t.Errorf("rendered panes %d != totals.Panes %d — totals must match the tree", rendered, r.Totals.Panes)
	}
}

func TestRenderJSON_UnknownMemoryIsNull(t *testing.T) {
	// A pane with no memory sample serializes memory_bytes as JSON null (not 0)
	// so a script can distinguish "unsampled" from a real zero-byte reading.
	r := statusReport{
		Running: true, Responding: true,
		Totals: statusTotals{Tabs: 1, Panes: 2},
		Tabs: []statusTab{{ID: "t", Name: "T", Panes: []statusPane{
			{ID: "p1", Name: "known", Type: "terminal", Running: true, HasMem: true, MemBytes: 123},
			{ID: "p2", Name: "pending", Type: "terminal", Pending: true, HasMem: false},
		}}},
	}
	b, err := renderJSON(r)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	panes := m["tabs"].([]any)[0].(map[string]any)["panes"].([]any)
	if got := panes[0].(map[string]any)["memory_bytes"]; got != float64(123) {
		t.Errorf("known pane memory_bytes = %v, want 123", got)
	}
	v, ok := panes[1].(map[string]any)["memory_bytes"]
	if !ok {
		t.Errorf("memory_bytes key should be present (null), not absent")
	}
	if v != nil {
		t.Errorf("unsampled pane memory_bytes = %v, want null", v)
	}
}

func TestBuildStatus_TracksUnsampledMemory(t *testing.T) {
	// Sampled panes contribute to MemoryBytes; unsampled ones are counted so
	// the aggregate can be flagged as incomplete.
	panes := []ipc.PaneInfo{
		{ID: "p1", TabID: "t1", Name: "a", Type: "terminal", Running: true},
		{ID: "p2", TabID: "t1", Name: "b", Type: "terminal", Running: true},
		{ID: "p3", TabID: "t1", Name: "c", Type: "terminal", Pending: true},
	}
	mem := ipc.MemoryReportRespPayload{
		Panes: []ipc.PaneMemInfo{{PaneID: "p1", TotalBytes: 100}}, // p2, p3 unsampled
		Tabs:  []ipc.TabInfo{{ID: "t1", Name: "T", Active: true}},
	}
	r := buildStatus("v", 1, panes, mem, 0, false, time.Time{})
	if r.Totals.MemoryBytes != 100 {
		t.Errorf("MemoryBytes = %d, want 100 (only p1 sampled)", r.Totals.MemoryBytes)
	}
	if r.Totals.MemUnsampled != 2 {
		t.Errorf("MemUnsampled = %d, want 2 (p2, p3)", r.Totals.MemUnsampled)
	}
}

func TestRenderJSON_MemoryCompleteSignal(t *testing.T) {
	base := func(unsampled int) statusReport {
		return statusReport{
			Running: true, Responding: true,
			Totals: statusTotals{Tabs: 1, Panes: 1, MemoryBytes: 10, MemUnsampled: unsampled},
			Tabs:   []statusTab{{ID: "t", Name: "T"}},
		}
	}
	parse := func(r statusReport) map[string]any {
		b, err := renderJSON(r)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		return m["totals"].(map[string]any)
	}
	if c := parse(base(0))["memory_complete"]; c != true {
		t.Errorf("memory_complete (all sampled) = %v, want true", c)
	}
	tot := parse(base(2))
	if c := tot["memory_complete"]; c != false {
		t.Errorf("memory_complete (2 unsampled) = %v, want false", c)
	}
	// The partial sum is still reported alongside the incomplete flag.
	if mb := tot["memory_bytes"]; mb != float64(10) {
		t.Errorf("memory_bytes = %v, want 10 (partial sum retained)", mb)
	}
}

func TestRenderHuman_UnsampledNote(t *testing.T) {
	r := statusReport{
		Running: true, Responding: true,
		Totals: statusTotals{Tabs: 1, Panes: 3, Running: 3, MemoryBytes: 1024 * 1024, MemUnsampled: 2},
	}
	out := renderHuman(r, false)
	if !strings.Contains(out, "(2 unsampled)") {
		t.Errorf("output should flag unsampled panes\n---\n%s", out)
	}
	// A complete total carries no note.
	r.Totals.MemUnsampled = 0
	if strings.Contains(renderHuman(r, false), "unsampled") {
		t.Errorf("complete total should not carry an unsampled note")
	}
}

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
