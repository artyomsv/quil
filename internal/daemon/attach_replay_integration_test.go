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

// TestHandleAttach_ReplaysEventsOldestFirst pins the CALL SITE, not just the
// queue method. The TUI rebuilds each pane's work state by applying these
// events as ordered transitions, so replaying the queue in its stored
// newest-first order lands on the state implied by the oldest event: three
// claude panes parked on "Claude is waiting for your input" came back from a
// TUI restart spinning, with nothing running behind them (2026-08-03).
//
// The unit tests cover eventQueue.EventsOldestFirst. Only this one catches the
// handler being pointed back at Events().
func TestHandleAttach_ReplaysEventsOldestFirst(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("QUIL_HOME", tmp)

	d := New(config.Default())
	tab := d.session.CreateTab("Shell")
	pane, err := d.session.CreatePane(tab.ID, "/tmp")
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}

	// One pane's history, in the order it happened. Distinct titles so the
	// queue's (PaneID, Title) aggregation leaves all three standing.
	history := []string{"Resumed after AskUserQuestion", "Reply ready", "Claude is waiting for your input"}
	for i, title := range history {
		d.events.Push(PaneEvent{
			ID:        title,
			PaneID:    pane.ID,
			TabID:     tab.ID,
			Type:      "hook.claude.Test",
			Title:     title,
			Timestamp: time.Now().Add(time.Duration(i) * time.Second),
		})
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

	var seen []string
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	for len(seen) < len(history) {
		msg, err := ipc.ReadMessage(conn)
		if err != nil {
			t.Fatalf("read after %d/%d events: %v", len(seen), len(history), err)
		}
		if msg.Type != ipc.MsgPaneEvent {
			continue
		}
		var p ipc.PaneEventPayload
		if err := msg.DecodePayload(&p); err != nil {
			t.Fatalf("decode pane event: %v", err)
		}
		seen = append(seen, p.Title)
	}

	for i, want := range history {
		if seen[i] != want {
			t.Fatalf("replay order %v, want %v — a client rebuilding state from "+
				"this sequence ends on the wrong event", seen, history)
		}
	}
}

// TestHandleAttach_SkipsGhostReplayWhenTheChildRepaints pins the CALL SITE of
// the ghost-source decision.
//
// A pane restored with a session-resume strategy has its whole transcript
// painted back by the respawned child, so replaying Quil's saved copy as well
// puts the conversation in the grid twice — and the child starts writing
// wherever the replay left the cursor, so the join is corrupted, not merely
// duplicated (reported 2026-08-02 and 2026-08-03).
//
// The terminal pane in the same attach is the control: a shell reprints none
// of its scrollback, so dropping ITS replay would lose the user's history
// outright. Both panes are checked in one attach so a fix that skips
// everything cannot pass.
func TestHandleAttach_SkipsGhostReplayWhenTheChildRepaints(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("QUIL_HOME", tmp)

	d := New(config.Default())
	tab := d.session.CreateTab("Shell")

	mkPane := func(typ string) *Pane {
		t.Helper()
		pane, err := d.session.CreatePane(tab.ID, "/tmp")
		if err != nil {
			t.Fatalf("CreatePane: %v", err)
		}
		pane.PluginMu.Lock()
		pane.Type = typ
		// GhostSnap non-nil is what "first attach after a restore" means: the
		// buffer came off disk and the child is about to be respawned.
		pane.GhostSnap = bytes.Repeat([]byte{'g'}, 4096)
		pane.PluginMu.Unlock()
		return pane
	}
	agent := mkPane("claude-code") // strategy = preassign_id
	shell := mkPane("terminal")    // strategy = cwd_only

	if err := d.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer d.Stop()

	// Guard: the daemon must actually resolve both plugins, or this test
	// degrades into asserting that two unknown types behave alike.
	if p := d.registry.Get("claude-code"); p == nil || !restoresOwnHistory(p) {
		t.Fatal("setup: claude-code must resolve to a session-resuming strategy")
	}
	if p := d.registry.Get("terminal"); p == nil || restoresOwnHistory(p) {
		t.Fatal("setup: terminal must NOT resolve to a session-resuming strategy")
	}

	conn := dialDaemon(t, filepath.Join(tmp, "quild.sock"))
	defer conn.Close()

	attach, err := ipc.NewMessage(ipc.MsgAttach, ipc.AttachPayload{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("NewMessage attach: %v", err)
	}
	if err := ipc.WriteMessage(conn, attach); err != nil {
		t.Fatalf("write attach: %v", err)
	}

	// Read until the shell's replay has fully arrived. The agent's frames, if
	// the daemon wrongly sent any, precede it in the same pane loop — so by the
	// time the shell's last byte lands, an agent frame would already be here.
	ghost := map[string]int{}
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	for ghost[shell.ID] < 4096 {
		msg, err := ipc.ReadMessage(conn)
		if err != nil {
			t.Fatalf("read after %v: %v", ghost, err)
		}
		if msg.Type != ipc.MsgPaneOutput {
			continue
		}
		var p ipc.PaneOutputPayload
		if err := msg.DecodePayload(&p); err != nil {
			t.Fatalf("decode pane output: %v", err)
		}
		if p.Ghost {
			ghost[p.PaneID] += len(p.Data)
		}
	}

	if n := ghost[agent.ID]; n != 0 {
		t.Errorf("claude-code pane received %d ghost bytes; the respawned child "+
			"repaints this transcript itself, so replaying it doubles the "+
			"conversation and corrupts the join", n)
	}
	if n := ghost[shell.ID]; n != 4096 {
		t.Errorf("terminal pane received %d ghost bytes, want 4096 — a shell "+
			"reprints none of its scrollback, so this replay is the only history", n)
	}
}

// TestHandleAttach_ReplaysTheChildsOwnStreamAfterARestore: the skip above is
// for the PREVIOUS session's bytes, which the respawned child repaints itself.
// Once that child has written, OutputBuf holds ITS stream (flushPaneOutput
// hands the buffer over on the first byte), and replaying that reproduces the
// screen the child already has — the same bytes a reattach replays. Sending
// nothing and relying on the redraw kick instead left the pane BLACK whenever
// the child sat on a screen that ignores Ctrl+L: claude's first-run setup,
// its login prompt, a startup error (2026-09-04, the proctoring pane sat on
// the theme picker behind an empty rectangle for two hours).
//
// Three panes in one attach so a fix cannot pass by replaying everything:
// a self-restoring child that has painted (replay ITS bytes, none of the old
// ones), one that has not (still nothing — the old bytes would double the
// conversation), and a shell as the barrier and control.
func TestHandleAttach_ReplaysTheChildsOwnStreamAfterARestore(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("QUIL_HOME", tmp)

	d := New(config.Default())
	tab := d.session.CreateTab("Shell")

	// restored leaves a pane exactly as restoreWorkspace does: the previous
	// session's bytes seeded into OutputBuf, the snapshot copy in GhostSnap.
	old := bytes.Repeat([]byte{'g'}, 4096)
	restored := func(typ string) *Pane {
		t.Helper()
		pane, err := d.session.CreatePane(tab.ID, "/tmp")
		if err != nil {
			t.Fatalf("CreatePane: %v", err)
		}
		pane.PluginMu.Lock()
		pane.Type = typ
		pane.OutputBuf.Write(old)
		pane.ghostSeeded = true
		pane.GhostSnap = append([]byte(nil), old...)
		pane.PluginMu.Unlock()
		return pane
	}

	painted := restored("claude-code")
	// The respawned child wrote — through the real handover seam, not by
	// poking the buffer, so the test fails if the handover ever stops
	// resetting it.
	own := bytes.Repeat([]byte{'c'}, 2048)
	d.flushPaneOutput(painted.ID, own)

	waiting := restored("claude-code")
	shell := restored("terminal") // last: the barrier, and the control

	if err := d.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer d.Stop()

	if p := d.registry.Get("claude-code"); p == nil || !restoresOwnHistory(p) {
		t.Fatal("setup: claude-code must resolve to a session-resuming strategy")
	}

	conn := dialDaemon(t, filepath.Join(tmp, "quild.sock"))
	defer conn.Close()

	attach, err := ipc.NewMessage(ipc.MsgAttach, ipc.AttachPayload{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("NewMessage attach: %v", err)
	}
	if err := ipc.WriteMessage(conn, attach); err != nil {
		t.Fatalf("write attach: %v", err)
	}

	// Panes replay in creation order; the shell's last byte proves every
	// earlier pane's frames — or their absence — are already here.
	got := map[string][]byte{}
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	for len(got[shell.ID]) < 4096 {
		msg, err := ipc.ReadMessage(conn)
		if err != nil {
			t.Fatalf("read after %d/%d/%d bytes: %v", len(got[painted.ID]), len(got[waiting.ID]), len(got[shell.ID]), err)
		}
		if msg.Type != ipc.MsgPaneOutput {
			continue
		}
		var p ipc.PaneOutputPayload
		if err := msg.DecodePayload(&p); err != nil {
			t.Fatalf("decode pane output: %v", err)
		}
		if p.Ghost {
			got[p.PaneID] = append(got[p.PaneID], p.Data...)
		}
	}

	if !bytes.Equal(got[painted.ID], own) {
		t.Errorf("painted claude-code pane received %d ghost bytes (%d of the old session's), want exactly its child's %d — "+
			"the screen it drew is the only thing that shows what it is waiting for",
			len(got[painted.ID]), bytes.Count(got[painted.ID], []byte{'g'}), len(own))
	}
	if n := len(got[waiting.ID]); n != 0 {
		t.Errorf("waiting claude-code pane received %d ghost bytes; its child has not painted, "+
			"so the only bytes on hand are the previous session's, which it will repaint itself", n)
	}
	if n := len(got[shell.ID]); n != 4096 {
		t.Errorf("terminal pane received %d ghost bytes, want 4096", n)
	}
}
