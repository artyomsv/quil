//go:build integration

package daemon

import (
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/panehistory"
	"github.com/artyomsv/quil/internal/persist"
)

// TestStop_ClosesShutdownChannel verifies that calling Stop() — even without
// going through MsgShutdown or a signal — closes d.shutdown so that every
// long-running goroutine that selects on it (idleChecker, memReport ctx
// bridge, sendGhostChunked) wakes up and exits.
//
// Regression test for techdebt/daemon/2-2-stop-does-not-close-shutdown.md.
func TestStop_ClosesShutdownChannel(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())

	d := New(config.Default())
	if err := d.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	select {
	case <-d.shutdown:
		t.Fatal("shutdown channel closed before Stop()")
	default:
	}

	d.Stop()

	select {
	case <-d.shutdown:
	default:
		t.Fatal("shutdown channel still open after Stop()")
	}

	done := make(chan struct{})
	go func() {
		d.collectorWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("collectorWG did not drain within 2s — Run(ctx) leaked")
	}
}

// TestStop_Idempotent verifies that calling Stop() twice is safe — the second
// call must not panic on a closed channel, and the channel must remain closed.
func TestStop_Idempotent(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())

	d := New(config.Default())
	if err := d.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	d.Stop()
	d.Stop()

	select {
	case <-d.shutdown:
	default:
		t.Fatal("shutdown channel reopened after second Stop()")
	}
}

// TestHandleMemoryReportReq_TabsPopulated boots a daemon with a known set of
// tabs + panes and verifies that MsgMemoryReportReq returns a response whose
// embedded Tabs slice matches the live session: per-tab pane counts, the
// active flag, and the name/color round-trip. This is the only path that
// exercises the new Tabs field added to MemoryReportRespPayload — without
// it, future changes to handleMemoryReportReq could regress silently.
func TestHandleMemoryReportReq_TabsPopulated(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("QUIL_HOME", tmp)

	d := New(config.Default())
	tab1 := d.session.CreateTab("Build")
	tab1.Color = "blue"
	if _, err := d.session.CreatePane(tab1.ID, "/tmp"); err != nil {
		t.Fatalf("CreatePane tab1: %v", err)
	}
	if _, err := d.session.CreatePane(tab1.ID, "/tmp"); err != nil {
		t.Fatalf("CreatePane tab1: %v", err)
	}
	tab2 := d.session.CreateTab("Notes")
	if _, err := d.session.CreatePane(tab2.ID, "/tmp"); err != nil {
		t.Fatalf("CreatePane tab2: %v", err)
	}

	if err := d.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer d.Stop()

	sockPath := filepath.Join(tmp, "quild.sock")
	conn := dialDaemon(t, sockPath)
	defer conn.Close()

	payload, _ := json.Marshal(ipc.MemoryReportReqPayload{})
	req := &ipc.Message{Type: ipc.MsgMemoryReportReq, ID: "tabs1", Payload: payload}
	if err := ipc.WriteMessage(conn, req); err != nil {
		t.Fatalf("write: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp, err := ipc.ReadMessage(conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var out ipc.MemoryReportRespPayload
	if err := json.Unmarshal(resp.Payload, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(out.Tabs) != 2 {
		t.Fatalf("Tabs len = %d, want 2", len(out.Tabs))
	}
	byID := map[string]ipc.TabInfo{}
	for _, ti := range out.Tabs {
		byID[ti.ID] = ti
	}
	gotTab1, ok := byID[tab1.ID]
	if !ok {
		t.Fatalf("tab1 (%s) missing from Tabs", tab1.ID)
	}
	if gotTab1.Name != "Build" || gotTab1.Color != "blue" || gotTab1.PaneCount != 2 || !gotTab1.Active {
		t.Errorf("tab1 mismatch: %+v", gotTab1)
	}
	gotTab2 := byID[tab2.ID]
	if gotTab2.PaneCount != 1 {
		t.Errorf("tab2 PaneCount = %d, want 1", gotTab2.PaneCount)
	}
	if gotTab2.Active {
		t.Errorf("tab2 marked Active — only the first-created tab should be")
	}
}

// TestSnapshot_PaneSetConsistentAcrossWorkspaceAndBuffers verifies that the
// snapshot() refactor (single SnapshotState reused for both halves) produces
// identical pane sets in workspace.json and the persisted buffers directory.
// Before the fix the two halves called SnapshotState independently and a
// pane create/destroy slipping between them produced an off-by-one mismatch
// — exactly the "snapshot pane count oscillation" tech-debt bug.
func TestSnapshot_PaneSetConsistentAcrossWorkspaceAndBuffers(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("QUIL_HOME", tmp)

	d := New(config.Default())
	tab := d.session.CreateTab("Shell")
	for i := 0; i < 3; i++ {
		pane, err := d.session.CreatePane(tab.ID, "/tmp")
		if err != nil {
			t.Fatalf("CreatePane: %v", err)
		}
		// Seed each pane's ring buffer so the buffer-flush half has
		// something to write — otherwise empty buffers are skipped and we
		// can't observe the consistency property on disk.
		pane.OutputBuf.Write(bytes.Repeat([]byte("x"), 32))
	}

	d.snapshot()

	state, err := persist.Load(config.WorkspacePath())
	if err != nil {
		t.Fatalf("Load workspace: %v", err)
	}
	rawPanes, _ := state["panes"].([]any)
	wantIDs := map[string]bool{}
	for _, p := range rawPanes {
		pm, _ := p.(map[string]any)
		id, _ := pm["id"].(string)
		if id != "" {
			wantIDs[id] = true
		}
	}
	if len(wantIDs) != 3 {
		t.Fatalf("workspace.json: %d panes, want 3", len(wantIDs))
	}

	bufIDs := listBufferIDs(t, config.BufferDir())
	if len(bufIDs) != 3 {
		t.Fatalf("buffer dir: %d panes, want 3 — workspace and buffers disagree", len(bufIDs))
	}
	for id := range bufIDs {
		if !wantIDs[id] {
			t.Errorf("buffer dir has pane %s that is not in workspace.json", id)
		}
	}
	for id := range wantIDs {
		if _, ok := bufIDs[id]; !ok {
			t.Errorf("workspace.json has pane %s with no persisted buffer", id)
		}
	}
}

// listBufferIDs scans the buffer directory and returns a set of pane IDs
// (stripped of the .bin suffix) for which a persisted ghost buffer exists.
func listBufferIDs(t *testing.T, dir string) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read buffer dir %s: %v", dir, err)
	}
	out := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".bin") {
			continue
		}
		out[strings.TrimSuffix(e.Name(), ".bin")] = true
	}
	return out
}

// dialDaemon waits up to 2 s for the daemon's Unix socket to become
// connectable and returns the open connection.
func dialDaemon(t *testing.T, sockPath string) net.Conn {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.Dial("unix", sockPath)
		if err == nil {
			return c
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("socket %s never became connectable", sockPath)
	return nil
}

// TestHandlePaneHistoryReq_ReturnsPreviewsNewestFirst seeds two history
// entries for a pane, then verifies MsgPaneHistoryReq returns their previews
// newest-first with multiline text yielding multiple preview lines. Exercises
// the full IPC round-trip through handlePaneHistoryReq.
func TestHandlePaneHistoryReq_ReturnsPreviewsNewestFirst(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("QUIL_HOME", tmp)

	const paneID = "pane-a1b2c3d4"
	dir := config.QuilDir()
	if err := panehistory.Append(dir, paneID, panehistory.Entry{TsMs: 1, Text: "first prompt"}); err != nil {
		t.Fatalf("Append 1: %v", err)
	}
	if err := panehistory.Append(dir, paneID, panehistory.Entry{TsMs: 2, Text: "second\nmultiline"}); err != nil {
		t.Fatalf("Append 2: %v", err)
	}

	d := New(config.Default())
	if err := d.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer d.Stop()

	sockPath := filepath.Join(tmp, "quild.sock")
	conn := dialDaemon(t, sockPath)
	defer conn.Close()

	payload, _ := json.Marshal(ipc.PaneHistoryReqPayload{PaneID: paneID})
	req := &ipc.Message{Type: ipc.MsgPaneHistoryReq, ID: "hist1", Payload: payload}
	if err := ipc.WriteMessage(conn, req); err != nil {
		t.Fatalf("write: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp, err := ipc.ReadMessage(conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var out ipc.PaneHistoryRespPayload
	if err := json.Unmarshal(resp.Payload, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if out.PaneID != paneID {
		t.Errorf("PaneID = %q, want %q", out.PaneID, paneID)
	}
	if len(out.Entries) != 2 {
		t.Fatalf("Entries len = %d, want 2", len(out.Entries))
	}
	if out.Entries[0].TsMs != 2 {
		t.Errorf("Entries[0].TsMs = %d, want 2 (newest first)", out.Entries[0].TsMs)
	}
	if len(out.Entries[0].Preview) != 2 {
		t.Errorf("Entries[0].Preview len = %d, want 2 (multiline)", len(out.Entries[0].Preview))
	}
	if out.Entries[1].TsMs != 1 {
		t.Errorf("Entries[1].TsMs = %d, want 1", out.Entries[1].TsMs)
	}
}

// TestHandlePaneHistoryEntryReq_FetchesFullTextByTsMs seeds two entries and
// verifies MsgPaneHistoryEntryReq returns the full text of the requested entry
// (Found=true), and that an unknown TsMs yields Found=false with empty text.
func TestHandlePaneHistoryEntryReq_FetchesFullTextByTsMs(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("QUIL_HOME", tmp)

	const paneID = "pane-a1b2c3d4"
	dir := config.QuilDir()
	if err := panehistory.Append(dir, paneID, panehistory.Entry{TsMs: 1, Text: "first prompt"}); err != nil {
		t.Fatalf("Append 1: %v", err)
	}
	if err := panehistory.Append(dir, paneID, panehistory.Entry{TsMs: 2, Text: "second\nmultiline body"}); err != nil {
		t.Fatalf("Append 2: %v", err)
	}

	d := New(config.Default())
	if err := d.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer d.Stop()

	sockPath := filepath.Join(tmp, "quild.sock")
	conn := dialDaemon(t, sockPath)
	defer conn.Close()

	fetch := func(ts int64) ipc.PaneHistoryEntryRespPayload {
		t.Helper()
		payload, _ := json.Marshal(ipc.PaneHistoryEntryReqPayload{PaneID: paneID, TsMs: ts})
		req := &ipc.Message{Type: ipc.MsgPaneHistoryEntryReq, ID: "he", Payload: payload}
		if err := ipc.WriteMessage(conn, req); err != nil {
			t.Fatalf("write: %v", err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		resp, err := ipc.ReadMessage(conn)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var out ipc.PaneHistoryEntryRespPayload
		if err := json.Unmarshal(resp.Payload, &out); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return out
	}

	found := fetch(2)
	if !found.Found || found.Text != "second\nmultiline body" {
		t.Fatalf("fetch(2): Found=%v Text=%q, want true / full body", found.Found, found.Text)
	}

	missing := fetch(999)
	if missing.Found || missing.Text != "" {
		t.Fatalf("fetch(999): Found=%v Text=%q, want false / empty", missing.Found, missing.Text)
	}
}

// TestCleanupPaneArtifacts_RemovesHistory verifies cleanupPaneArtifacts deletes
// the pane's history file along with the other per-pane artifacts.
func TestCleanupPaneArtifacts_RemovesHistory(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("QUIL_HOME", tmp)

	const paneID = "pane-deadbeef"
	dir := config.QuilDir()
	if err := panehistory.Append(dir, paneID, panehistory.Entry{TsMs: 1, Text: "only prompt"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	histPath := panehistory.Path(dir, paneID)
	if _, err := os.Stat(histPath); err != nil {
		t.Fatalf("history file not created: %v", err)
	}

	d := New(config.Default())
	d.cleanupPaneArtifacts(paneID)

	if _, err := os.Stat(histPath); !os.IsNotExist(err) {
		t.Fatalf("history file still present after cleanup: err = %v", err)
	}
}
