//go:build integration

package daemon

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// TestHandleAttach_SlowClientGhostReplay_NotDisconnected reproduces the
// production attach kick-loop: panes with full output buffers generate far
// more critical ghost-replay frames than the per-conn critical queue holds
// (sendBufSize). A client that is briefly busy — not reading for ~500 ms,
// exactly like a TUI applying a 13-tab workspace state — must NOT be dropped
// by the critical-overflow defense, and must eventually receive every ghost
// byte of every pane.
//
// Before the fix the daemon logged "ipc: dropping slow client (critical send
// buffer overflow)" mid-replay and force-closed the connection on every
// attach, locking production out permanently.
func TestHandleAttach_SlowClientGhostReplay_NotDisconnected(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("QUIL_HOME", tmp)

	d := New(config.Default())
	tab := d.session.CreateTab("Shell")

	// 5 panes x 256000 bytes = 160 8KB replay chunks — 2.5x the critical
	// queue, comfortably past kernel-buffer slack on any platform.
	const paneCount = 5
	want := map[string]int{}
	wantTotal := 0
	for i := 0; i < paneCount; i++ {
		pane, err := d.session.CreatePane(tab.ID, "/tmp")
		if err != nil {
			t.Fatalf("CreatePane: %v", err)
		}
		pane.OutputBuf.Write(bytes.Repeat([]byte{'g'}, 256000))
		n := len(pane.OutputBuf.Bytes())
		want[pane.ID] = n
		wantTotal += n
	}
	// Guard: the scenario must actually exceed the critical queue, or this
	// test silently stops covering the overflow path.
	if wantTotal/(8*1024) <= 64 {
		t.Fatalf("test setup too small: %d bytes yields <= 64 replay chunks", wantTotal)
	}

	if err := d.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer d.Stop()

	conn := dialDaemon(t, filepath.Join(tmp, "quild.sock"))
	defer conn.Close()

	attach, err := ipc.NewMessage(ipc.MsgAttach, ipc.AttachPayload{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("NewMessage attach: %v", err)
	}
	if err := ipc.WriteMessage(conn, attach); err != nil {
		t.Fatalf("write attach: %v", err)
	}

	// Simulate the busy TUI: read NOTHING while the daemon performs the
	// replay. The kernel socket buffer fills, the daemon's sendLoop blocks,
	// and the replay must apply backpressure instead of overflow-closing.
	time.Sleep(500 * time.Millisecond)

	got := map[string]int{}
	total := 0
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	for total < wantTotal {
		msg, err := ipc.ReadMessage(conn)
		if err != nil {
			t.Fatalf("client disconnected mid-replay after %d/%d ghost bytes: %v", total, wantTotal, err)
		}
		if msg.Type != ipc.MsgPaneOutput {
			continue
		}
		var p ipc.PaneOutputPayload
		if err := msg.DecodePayload(&p); err != nil {
			t.Fatalf("decode pane output: %v", err)
		}
		if !p.Ghost {
			continue
		}
		got[p.PaneID] += len(p.Data)
		total += len(p.Data)
	}

	for id, n := range want {
		if got[id] != n {
			t.Errorf("pane %s: received %d ghost bytes, want %d", id, got[id], n)
		}
	}
}
