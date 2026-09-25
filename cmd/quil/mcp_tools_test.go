package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

// TestBuildTabMemSummaries_AggregatesPerTab covers the happy path: every
// pane in mem.Panes maps to a known tab in the embedded Tabs slice.
// goHeap / ptyRSS totals must equal the sum across all panes; per-tab
// counts and totals must respect the tabOrder from the Tabs argument.
func TestBuildTabMemSummaries_AggregatesPerTab(t *testing.T) {
	mem := ipc.MemoryReportRespPayload{
		Panes: []ipc.PaneMemInfo{
			{PaneID: "pane-aaa", TabID: "tab-1", GoHeapBytes: 1000, PTYRSSBytes: 0, TotalBytes: 1000},
			{PaneID: "pane-bbb", TabID: "tab-1", GoHeapBytes: 500, PTYRSSBytes: 2000, TotalBytes: 2500},
			{PaneID: "pane-ccc", TabID: "tab-2", GoHeapBytes: 100, PTYRSSBytes: 4000, TotalBytes: 4100},
		},
	}
	tabs := []ipc.TabInfo{
		{ID: "tab-1", Name: "Build", PaneCount: 2},
		{ID: "tab-2", Name: "Notes", PaneCount: 1},
	}

	goHeap, ptyRSS, summaries := buildTabMemSummaries(mem, tabs)

	if goHeap != 1600 {
		t.Errorf("goHeap = %d, want 1600", goHeap)
	}
	if ptyRSS != 6000 {
		t.Errorf("ptyRSS = %d, want 6000", ptyRSS)
	}
	if len(summaries) != 2 {
		t.Fatalf("summaries len = %d, want 2", len(summaries))
	}

	if summaries[0].TabID != "tab-1" || summaries[0].TabName != "Build" {
		t.Errorf("summaries[0] = %+v, want tab-1/Build", summaries[0])
	}
	if summaries[0].PaneCount != 2 || summaries[0].TotalBytes != 3500 {
		t.Errorf("summaries[0] count/total = %d/%d, want 2/3500", summaries[0].PaneCount, summaries[0].TotalBytes)
	}
	if summaries[1].TabID != "tab-2" || summaries[1].TotalBytes != 4100 {
		t.Errorf("summaries[1] = %+v, want tab-2 / 4100 bytes", summaries[1])
	}

	// TotalHuman is informative but stable for these inputs.
	if summaries[0].TotalHuman == "" || summaries[1].TotalHuman == "" {
		t.Errorf("TotalHuman should be non-empty for both summaries")
	}
}

// TestBuildTabMemSummaries_OrphanPaneFallback handles the case where a pane
// references a tab that isn't in the Tabs slice — typical when the tab was
// destroyed between the memreport tick and the Tabs snapshot. The orphan
// must still appear in summaries (no data lost) using the bare TabID as
// the display name.
func TestBuildTabMemSummaries_OrphanPaneFallback(t *testing.T) {
	mem := ipc.MemoryReportRespPayload{
		Panes: []ipc.PaneMemInfo{
			{PaneID: "pane-ghost", TabID: "tab-removed", GoHeapBytes: 100, TotalBytes: 100},
		},
	}
	tabs := []ipc.TabInfo{} // empty — the tab is gone

	_, _, summaries := buildTabMemSummaries(mem, tabs)

	if len(summaries) != 1 {
		t.Fatalf("summaries len = %d, want 1", len(summaries))
	}
	if summaries[0].TabID != "tab-removed" || summaries[0].TabName != "tab-removed" {
		t.Errorf("orphan summary = %+v, want TabID and TabName both 'tab-removed'", summaries[0])
	}
	if summaries[0].PaneCount != 1 || summaries[0].TotalBytes != 100 {
		t.Errorf("orphan count/total = %d/%d, want 1/100", summaries[0].PaneCount, summaries[0].TotalBytes)
	}
}

// TestBuildTabMemSummaries_NilTabsBackwardCompat verifies the pre-1.10 daemon
// case where MemoryReportRespPayload.Tabs is nil. The tool must still
// produce one summary per (orphan-treated) tab seen on a pane, falling
// back to TabID as the name. This is the safety net that lets the MCP
// bridge keep working during a rolling daemon upgrade.
func TestBuildTabMemSummaries_NilTabsBackwardCompat(t *testing.T) {
	mem := ipc.MemoryReportRespPayload{
		Panes: []ipc.PaneMemInfo{
			{PaneID: "p1", TabID: "tab-1", GoHeapBytes: 50, TotalBytes: 50},
			{PaneID: "p2", TabID: "tab-2", GoHeapBytes: 70, TotalBytes: 70},
		},
	}

	_, _, summaries := buildTabMemSummaries(mem, nil)

	gotIDs := make([]string, len(summaries))
	for i, s := range summaries {
		gotIDs[i] = s.TabID
		if s.TabName != s.TabID {
			t.Errorf("summary %s: name = %q, want fallback to TabID", s.TabID, s.TabName)
		}
	}
	want := []string{"tab-1", "tab-2"}
	if !reflect.DeepEqual(gotIDs, want) {
		t.Errorf("orphan order = %v, want %v", gotIDs, want)
	}
}

// TestBuildTabMemSummaries_EmptyMemKeepsTabsAsZeroRows verifies that tabs
// with no panes still appear in the summary list with PaneCount=0 — the
// memory dialog should show every tab even when it's empty.
func TestBuildTabMemSummaries_EmptyMemKeepsTabsAsZeroRows(t *testing.T) {
	mem := ipc.MemoryReportRespPayload{} // no panes
	tabs := []ipc.TabInfo{
		{ID: "tab-1", Name: "Build"},
		{ID: "tab-2", Name: "Notes"},
	}

	goHeap, ptyRSS, summaries := buildTabMemSummaries(mem, tabs)

	if goHeap != 0 || ptyRSS != 0 {
		t.Errorf("goHeap/ptyRSS = %d/%d, want 0/0", goHeap, ptyRSS)
	}
	if len(summaries) != 2 {
		t.Fatalf("summaries len = %d, want 2", len(summaries))
	}
	for _, s := range summaries {
		if s.PaneCount != 0 || s.TotalBytes != 0 {
			t.Errorf("empty tab %s: count/total = %d/%d, want 0/0", s.TabID, s.PaneCount, s.TotalBytes)
		}
	}
}

// TestSetActivePane_ClientFieldReachesThePayload: the tool's optional
// client input has to survive the trip onto the wire, or targeting one
// specific TUI (multi-client sync) silently degrades to the implicit
// most-recently-active target on every call.
func TestSetActivePane_ClientFieldReachesThePayload(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	local := newFakeIPCDaemon(t, "pane-local")
	session, _ := toolHarness(t, local, nil)

	if _, err := callTool(t, session, "set_active_pane", map[string]any{
		"pane_id": "pane-local", "client": "tui-B",
	}); err != nil {
		t.Fatalf("set_active_pane: %v", err)
	}

	// sendRaw only guarantees the frame is ENQUEUED by the time the tool call
	// returns; the actual write happens on the bridge's own sendLoop
	// goroutine (same reason mcp_hosts_test.go's waitUntil exists).
	var got *ipc.SetActivePanePayload
	waitUntil(func() bool {
		local.mu.Lock()
		defer local.mu.Unlock()
		for _, m := range local.received {
			if m.Type == ipc.MsgSetActivePane {
				var p ipc.SetActivePanePayload
				if err := m.DecodePayload(&p); err != nil {
					t.Fatal(err)
				}
				got = &p
			}
		}
		return got != nil
	}, 500*time.Millisecond)
	if got == nil {
		t.Fatal("no set_active_pane frame reached the daemon")
	}
	if got.Client != "tui-B" {
		t.Errorf("SetActivePanePayload.Client = %q, want %q", got.Client, "tui-B")
	}
}

// closeTUIReceived polls the fake daemon for the LATEST close_tui frame it
// received, up to 500ms. sendRaw only guarantees the frame is ENQUEUED by the
// time the tool call returns — the actual socket write happens on the
// bridge's own sendLoop goroutine, same as mcp_hosts_test.go's waitUntil
// exists for.
func closeTUIReceived(t *testing.T, f *fakeIPCDaemon) *ipc.CloseTUIPayload {
	t.Helper()
	var got *ipc.CloseTUIPayload
	waitUntil(func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, m := range f.received {
			if m.Type == ipc.MsgCloseTUI {
				var p ipc.CloseTUIPayload
				if err := m.DecodePayload(&p); err != nil {
					t.Fatal(err)
				}
				got = &p
			}
		}
		return got != nil
	}, 500*time.Millisecond)
	return got
}

// TestCloseTUI_ClientFieldReachesThePayload is the close_tui half of the
// same wiring check.
func TestCloseTUI_ClientFieldReachesThePayload(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	local := newFakeIPCDaemon(t, "pane-local")
	session, _ := toolHarness(t, local, nil)

	if _, err := callTool(t, session, "close_tui", map[string]any{"client": "tui-A"}); err != nil {
		t.Fatalf("close_tui: %v", err)
	}

	got := closeTUIReceived(t, local)
	if got == nil {
		t.Fatal("no close_tui frame reached the daemon")
	}
	if got.Client != "tui-A" {
		t.Errorf("CloseTUIPayload.Client = %q, want %q", got.Client, "tui-A")
	}
}

// TestCloseTUI_NoClientSendsAnEmptyPayload: an older-style call with no
// client argument must still reach the daemon as a valid (empty) payload —
// the historical broadcast-to-every-TUI shape a headless daemon and every
// existing caller depend on.
func TestCloseTUI_NoClientSendsAnEmptyPayload(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	local := newFakeIPCDaemon(t, "pane-local")
	session, _ := toolHarness(t, local, nil)

	if _, err := callTool(t, session, "close_tui", map[string]any{}); err != nil {
		t.Fatalf("close_tui: %v", err)
	}

	got := closeTUIReceived(t, local)
	if got == nil {
		t.Fatal("no close_tui frame reached the daemon")
	}
	if got.Client != "" {
		t.Errorf("CloseTUIPayload.Client = %q, want empty", got.Client)
	}
}

// TestListClients_ReturnsTheDaemonsList exercises the new list_clients tool
// end to end: it must gate on the daemon advertising list_clients_req (or a
// version at least listClientsMinVersion), and decode the daemon's answer.
func TestListClients_ReturnsTheDaemonsList(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	local := newFakeIPCDaemonVersion(t, "pane-local", listClientsMinVersion)
	session, _ := toolHarness(t, local, nil)

	text, err := callTool(t, session, "list_clients", map[string]any{})
	if err != nil {
		t.Fatalf("list_clients: %v", err)
	}
	var out []struct {
		ipc.ClientInfo
		Host string `json:"host"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("decode: %v\n%s", err, text)
	}
	if len(out) != 1 || out[0].Client != "tui-pane-local" || !out[0].Master {
		t.Fatalf("list_clients result = %+v", out)
	}
}

// TestListClients_RefusedBelowItsOwnFloor: list_clients_req is new with
// multi-client sync, so a daemon that neither advertises it nor clears
// listClientsMinVersion must be refused with a named error — not a silent
// drop and a timeout (the daemon simply ignores an unknown request type).
func TestListClients_RefusedBelowItsOwnFloor(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	local := newFakeIPCDaemonRequests(t, "pane-local", "9.9.9", ipc.MsgCreateTabReq)
	session, _ := toolHarness(t, local, nil)

	_, err := callTool(t, session, "list_clients", map[string]any{})
	if err == nil || !strings.Contains(err.Error(), ipc.MsgListClientsReq) {
		t.Fatalf("expected a refusal naming %s, got %v", ipc.MsgListClientsReq, err)
	}
	if !local.sawNo(ipc.MsgListClientsReq) {
		t.Fatal("refused request reached the daemon")
	}
}
