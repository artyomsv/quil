package daemon

import (
	"encoding/json"
	"os"
	"sync"
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

func TestBuildWorkspaceState_RevStartsAtOneAndIncreases(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	d := New(config.Default())
	a := d.buildWorkspaceState()
	b := d.buildWorkspaceState()
	if a.Rev != 1 || b.Rev != 2 {
		t.Errorf("revs = %d, %d; want 1, 2", a.Rev, b.Rev)
	}
	if a.RunID == "" || a.RunID != b.RunID {
		t.Errorf("run ids %q, %q; want one non-empty id per daemon", a.RunID, b.RunID)
	}
}

func TestNew_RunIDDiffersPerDaemon(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	if New(config.Default()).runID == New(config.Default()).runID {
		t.Error("two daemons share a run id; a client could not tell a restart")
	}
}

// Review focus 2: the size master is read outside SnapshotState, so the rev
// must be taken after EVERY field, under one lock, or rev order and content
// order can disagree. Content order cannot be observed deterministically from
// outside, so this pins the half a test can see (every build gets its own
// rev, under -race); the lock-order argument in step 3 carries the rest.
func TestBuildWorkspaceState_ConcurrentBuilders_EachGetsItsOwnRev(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	d := New(config.Default())
	var mu sync.Mutex
	seen := map[uint64]int{} // rev -> clients count observed
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := d.buildWorkspaceState()
			mu.Lock()
			seen[s.Rev] = *s.Clients
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(seen) != 50 {
		t.Fatalf("got %d distinct revs from 50 builds; every build needs its own", len(seen))
	}
}

// TestSnapshot_WorkspaceJSON_HasNoRev mirrors Task 3's
// TestSnapshot_WorkspaceJSON_SameShapeAsBefore's disk-reading steps: rev and
// run_id are broadcast-only, numbered-frame concepts, and workspace.json is
// not a numbered frame — a restored daemon starts its OWN rev at 1 regardless
// of what was on disk.
func TestSnapshot_WorkspaceJSON_HasNoRev(t *testing.T) {
	d := richStateDaemon(t)
	d.snapshot()

	raw, err := os.ReadFile(config.WorkspacePath())
	if err != nil {
		t.Fatalf("read workspace.json: %v", err)
	}
	var onDisk map[string]any
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("unmarshal workspace.json: %v", err)
	}
	if _, ok := onDisk["rev"]; ok {
		t.Error("workspace.json carries \"rev\" — it is a broadcast-only field")
	}
	if _, ok := onDisk["run_id"]; ok {
		t.Error("workspace.json carries \"run_id\" — it is a broadcast-only field")
	}
}

func TestHandleMessage_StateReq_AnswersWorkspaceStateWithSameIDAndFreshRev(t *testing.T) {
	d, client := mcpTestDaemon(t)
	before := d.buildWorkspaceState().Rev
	resp := roundTrip(t, client, ipc.MsgStateReq, ipc.MsgWorkspaceState, struct{}{})
	ws := decodeInto[ipc.WorkspaceState](t, resp)
	if ws.Rev <= before {
		t.Errorf("state_req answer rev = %d, want > %d", ws.Rev, before)
	}
	if ws.RunID != d.runID {
		t.Errorf("run_id = %q, want %q", ws.RunID, d.runID)
	}
}

// TestHandleAttach_StateFrame_CarriesRev drives a real attach through the live
// server (as mcpTestDaemon does) and checks the very first workspace_state
// frame — the one handleAttach sends directly to the attaching conn, ahead of
// ghost replay — carries a positive rev and this daemon's run id.
func TestHandleAttach_StateFrame_CarriesRev(t *testing.T) {
	d, sock := overlayServerDaemonWithConfig(t, config.Default())
	client, err := ipc.NewClient(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	attach, err := ipc.NewMessage(ipc.MsgAttach, ipc.AttachPayload{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("build attach: %v", err)
	}
	if err := client.Send(attach); err != nil {
		t.Fatalf("send attach: %v", err)
	}

	for {
		msg, err := client.Receive()
		if err != nil {
			t.Fatalf("receive: %v", err)
		}
		if msg.Type != ipc.MsgWorkspaceState {
			continue
		}
		ws := decodeInto[ipc.WorkspaceState](t, msg)
		if ws.Rev == 0 {
			t.Error("attach state frame carries rev == 0; want > 0")
		}
		if ws.RunID != d.runID {
			t.Errorf("attach state frame run_id = %q, want %q", ws.RunID, d.runID)
		}
		return
	}
}
