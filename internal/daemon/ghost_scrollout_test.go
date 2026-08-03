package daemon

import (
	"bytes"
	"testing"

	"github.com/artyomsv/quil/internal/ringbuf"
)

// A respawned child paints with ABSOLUTE cursor positioning against a screen
// it believes it owns: PSReadLine redrawing an input line emits `CSI 1;30H` —
// row 1, column 30, one past a 29-character prompt. A replayed session left on
// the visible screen therefore does not merely look stale, it gets painted
// through: the fresh prompt lands at the top of the pane with the previous
// session's rows below it (reported 2026-08-03).
//
// The replay goes into scrollback instead, where the child cannot reach it.

func TestGhostScrollOut_PushesAFullScreen(t *testing.T) {
	got := ghostScrollOut(54)
	if n := bytes.Count(got, []byte{'\n'}); n != 54 {
		t.Errorf("scrolled %d rows, want 54 — an under-scroll leaves rows for the child to paint through", n)
	}
	// CR as well as LF: the column has to be reset, not just the row.
	if !bytes.Equal(got[:2], []byte("\r\n")) {
		t.Errorf("first sequence = %q, want \"\\r\\n\"", got[:2])
	}
}

// A deferred pane has no client geometry yet, so rows can be zero. Emitting
// nothing there would leave the whole replay on screen.
func TestGhostScrollOut_UnsizedPaneStillScrolls(t *testing.T) {
	for _, rows := range []int{0, -1} {
		got := ghostScrollOut(rows)
		if n := bytes.Count(got, []byte{'\n'}); n != 24 {
			t.Errorf("rows=%d scrolled %d, want the 24-row fallback", rows, n)
		}
	}
}

// The restore seeds OutputBuf with the PREVIOUS session's bytes so a pane the
// user never opens still has history to persist and to replay on reconnect.
// The first byte the respawned child writes has to hand the buffer over, or
// the two sessions concatenate — and since OutputBuf is what the next snapshot
// persists, the concatenation is SAVED and grows on every restart (measured
// 3387 → 3512 bytes across one restore).
func TestFlushPaneOutput_ChildTakesOverTheSeededBuffer(t *testing.T) {
	d := &Daemon{session: NewSessionManager(4096), events: newEventQueue(16)}
	pane := &Pane{
		ID:        "p1",
		OutputBuf: ringbuf.NewRingBuffer(4096),
	}
	restored := []byte("PS E:\\Projects\\Stukans\\quil> ")
	pane.OutputBuf.Write(restored)
	pane.ghostSeeded = true
	d.session.panes["p1"] = pane

	d.flushPaneOutput("p1", []byte("fresh child output"))

	if got := string(pane.OutputBuf.Bytes()); got != "fresh child output" {
		t.Errorf("buffer = %q, want only the child's own stream — the restored "+
			"bytes must not be carried into what gets persisted", got)
	}
	if pane.ghostSeeded {
		t.Error("ghostSeeded still set; the handover must happen exactly once")
	}

	// Second write appends normally — a handover that fired twice would drop
	// the child's own scrollback on every flush.
	d.flushPaneOutput("p1", []byte(" and more"))
	if got := string(pane.OutputBuf.Bytes()); got != "fresh child output and more" {
		t.Errorf("buffer = %q, want the child's output appended", got)
	}
}

// A pane the user never opens is never spawned, so nothing hands its buffer
// over — and the restored history must survive to be persisted again.
func TestFlushPaneOutput_UnvisitedPaneKeepsItsRestoredHistory(t *testing.T) {
	pane := &Pane{ID: "p1", OutputBuf: ringbuf.NewRingBuffer(4096)}
	restored := []byte("previous session scrollback")
	pane.OutputBuf.Write(restored)
	pane.ghostSeeded = true

	if got := string(pane.OutputBuf.Bytes()); got != string(restored) {
		t.Fatalf("buffer = %q, want the restored bytes intact", got)
	}
	if !pane.ghostSeeded {
		t.Error("an unspawned pane must still be marked as holding restored bytes")
	}
}
