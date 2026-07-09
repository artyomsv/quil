package main

import (
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
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
