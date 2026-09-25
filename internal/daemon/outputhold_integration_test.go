//go:build integration

package daemon

import (
	"bytes"
	"encoding/binary"
	"sync/atomic"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

// Spec 9.1.6: a client attaching while a pane is writing receives the pane's
// replay and its live output as ONE stream — every byte once, in order.
//
// Only frames after the attach's workspace_state count: a conn receives live
// output from the moment it connects, and those earlier frames are for panes
// the client does not know yet.
func TestHold_HeldConnReceivesReplayThenLiveExactlyOnce(t *testing.T) {
	h := newHoldHarness(t)
	tab := h.d.session.CreateTab("T")
	p := h.pane(tab.ID, "terminal")

	// 2000 chunks x 64 bytes stays well inside the ring buffer, so the replay
	// starts at counter 0. Paced, so the live stream never outruns a reading
	// client: a full droppable queue would drop frames by design.
	const chunks = 2000
	var written atomic.Int64
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for i := 0; i < chunks; i++ {
			h.d.flushPaneOutput(p.ID, counterBytes(i))
			written.Store(int64(i + 1))
			if i%4 == 0 {
				time.Sleep(200 * time.Microsecond)
			}
		}
	}()

	client, conn := h.dial("B")
	type result struct {
		data []byte
		err  string
	}
	res := make(chan result, 1)
	go func() {
		var out []byte
		attached := false
		last := uint64(chunks*8 - 1)
		_ = client.SetReadDeadline(time.Now().Add(20 * time.Second))
		for {
			m, err := client.Receive()
			if err != nil {
				res <- result{out, "read: " + err.Error()}
				return
			}
			switch m.Type {
			case ipc.MsgWorkspaceState:
				attached = true
			case ipc.MsgPaneOutput:
				if !attached {
					continue
				}
				var f ipc.PaneOutputPayload
				if err := m.DecodePayload(&f); err != nil {
					res <- result{out, "decode: " + err.Error()}
					return
				}
				if f.PaneID != p.ID {
					continue
				}
				if len(f.Data) == 0 {
					res <- result{out, "empty pane_output frame"}
					return
				}
				out = append(out, f.Data...)
				if len(out) >= 8 && binary.LittleEndian.Uint64(out[len(out)-8:]) == last {
					res <- result{out, ""}
					return
				}
			}
		}
	}()

	waitUntil(t, "writer mid-stream", func() bool { return written.Load() >= chunks/4 })
	sendClientMsg(t, client, ipc.MsgAttach, ipc.AttachPayload{ClientID: "B", Cols: 80, Rows: 24})
	if written.Load() >= chunks {
		t.Fatal("setup: the writer finished before the attach; the test covers nothing")
	}
	<-writerDone

	r := <-res
	if r.err != "" {
		t.Fatalf("after %d bytes: %s", len(r.data), r.err)
	}
	if n := conn.Dropped(); n != 0 {
		t.Fatalf("setup: %d live frames dropped by a full queue; the writer is paced too fast", n)
	}
	checkCounterRun(t, r.data, 0, uint64(chunks*8-1))
}

// A conn receives live output from the moment it connects. Frames still queued
// for it when it attaches — a busy client with a full socket — must not be
// written after the attach's state and replay: their bytes are already in the
// replay, and the must-deliver queue is drained first, so they would land as
// stale duplicates in the middle of it.
func TestHold_LiveFramesQueuedBeforeAttachStayBeforeTheState(t *testing.T) {
	h := newHoldHarness(t)
	tab := h.d.session.CreateTab("T")
	p := h.pane(tab.ID, "terminal")
	client, conn := h.dial("B")

	// Nobody reads: the socket fills and live frames back up in the conn's
	// droppable queue.
	const perChunk = 1024 // counters per 8 KiB chunk
	next := uint64(0)
	flush := func() {
		h.d.flushPaneOutput(p.ID, counterRun(next, perChunk))
		next += perChunk
	}
	for conn.Dropped() == 0 && next < 1024*perChunk {
		flush()
	}
	if conn.Dropped() == 0 {
		t.Fatal("setup: the droppable queue never filled")
	}

	// The live chunk flushed once the attach is done; the read stops at it.
	last := next + perChunk - 1
	sendClientMsg(t, client, ipc.MsgAttach, ipc.AttachPayload{ClientID: "B", Cols: 80, Rows: 24})
	go func() {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if held, _, _ := h.holdOf(conn); !held && h.d.clientCount() == 1 {
				break
			}
			time.Sleep(2 * time.Millisecond)
		}
		flush()
	}()
	// Give the attach time to start before draining the backlog.
	time.Sleep(50 * time.Millisecond)

	var out []byte
	attached := false
	_ = client.SetReadDeadline(time.Now().Add(15 * time.Second))
	for {
		m, err := client.Receive()
		if err != nil {
			t.Fatalf("read after %d bytes: %v", len(out), err)
		}
		if m.Type == ipc.MsgWorkspaceState {
			attached = true
			continue
		}
		if m.Type != ipc.MsgPaneOutput || !attached {
			continue
		}
		var f ipc.PaneOutputPayload
		if err := m.DecodePayload(&f); err != nil {
			t.Fatalf("decode: %v", err)
		}
		out = append(out, f.Data...)
		if len(f.Data) >= 8 && binary.LittleEndian.Uint64(f.Data[len(f.Data)-8:]) == last && !f.Ghost {
			break
		}
	}
	// The ring keeps whole counters (its size and every chunk are multiples
	// of 8 bytes), so the replay starts on one.
	checkCounterRun(t, out, binary.LittleEndian.Uint64(out), last)
}

// A ghostsnap replay is a PREVIOUS session's bytes, so it records no end: every
// byte the new child wrote during the hold arrives after the replay and its
// scroll-out. Recording an end there would cut them.
func TestHold_GhostSnapReplayKeepsHeldBytes(t *testing.T) {
	h := newHoldHarness(t)
	tab := h.d.session.CreateTab("T")
	// 1 MiB of replay ahead of the ghostsnap pane, with the client not
	// reading, parks the attach before it reaches that pane.
	for i := 0; i < 4; i++ {
		x := h.pane(tab.ID, "terminal")
		x.OutputBuf.Write(bytes.Repeat([]byte{'x'}, 256000))
	}
	g := h.pane(tab.ID, "terminal")
	g.PluginMu.Lock()
	g.GhostSnap = []byte("SNAP")
	g.PluginMu.Unlock()

	client, conn := h.dial("B")
	sendClientMsg(t, client, ipc.MsgAttach, ipc.AttachPayload{ClientID: "B", Cols: 80, Rows: 24})
	waitUntil(t, "attach hold", func() bool { held, _, _ := h.holdOf(conn); return held })
	h.d.flushPaneOutput(g.ID, []byte("held-1"))
	h.d.flushPaneOutput(g.ID, []byte("held-2"))
	go func() {
		deadline := time.Now().Add(10 * time.Second)
		for h.holdCount() != 0 && time.Now().Before(deadline) {
			time.Sleep(2 * time.Millisecond)
		}
		h.d.flushPaneOutput(g.ID, []byte("tail"))
	}()

	frames := readOutput(t, client, isChunk(g.ID, "tail"))
	var ghost, live []byte
	for _, f := range frames {
		if f.PaneID != g.ID {
			continue
		}
		if f.Ghost {
			if len(live) > 0 {
				t.Fatal("a ghost frame arrived after live output")
			}
			ghost = append(ghost, f.Data...)
			continue
		}
		live = append(live, f.Data...)
	}
	_, rows := paneSize(g)
	if want := append([]byte("SNAP"), ghostScrollOut(rows)...); !bytes.Equal(ghost, want) {
		t.Errorf("ghost = %q, want %q", ghost, want)
	}
	if got, want := string(live), "held-1held-2tail"; got != want {
		t.Errorf("live = %q, want %q", got, want)
	}
}
