package daemon

import (
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

// redrawKick exists because plugins with ghost_buffer = false get no replay on
// attach, so a reconnecting client is sent nothing for a pane whose process is
// alive and mid-conversation. The rectangle stays blank until the child writes
// something, and an alternate-screen app has no reason to do that unprompted.
//
// These tests pin the two halves that matter: that the kick produces a real
// size CHANGE (a same-size resize is swallowed and repaints nothing), and that
// it declines in every state where there is nothing safe to signal.

func TestRedrawKick_JigglesThenRestoresTheSize(t *testing.T) {
	fake := &fakeSession{}

	redrawKick("pane-1", "claude-code", fake, 120, 40)

	// A single Resize to the size the PTY already has is a no-op the kernel
	// never turns into SIGWINCH — the child would not repaint and the whole
	// call would be silently useless. The jiggle is what makes it a change.
	if len(fake.resizes) != 2 {
		t.Fatalf("resizes = %v, want exactly 2 (jiggle then restore)", fake.resizes)
	}
	if got, want := fake.resizes[0], [2]uint16{40, 119}; got != want {
		t.Errorf("jiggle resize = %v, want %v (one column narrower)", got, want)
	}
	// Ending on the real size matters as much as the jiggle: leaving the child
	// one column narrow would make every pane render wrong until the next
	// genuine resize.
	if got, want := fake.resizes[1], [2]uint16{40, 120}; got != want {
		t.Errorf("restore resize = %v, want %v (the pane's true size)", got, want)
	}
}

func TestRedrawKick_DeclinesWhenThereIsNothingToSignal(t *testing.T) {
	tests := []struct {
		name       string
		nilPTY     bool
		cols, rows int
		why        string
	}{
		{
			name:   "no PTY",
			nilPTY: true, cols: 120, rows: 40,
			why: "a Pending lazy-restore pane has no child to signal",
		},
		{
			name: "size never recorded",
			cols: 0, rows: 0,
			why: "no client has sized this pane, so there is no correct size to restore to",
		},
		{
			name: "width recorded but height not",
			cols: 120, rows: 0,
			why: "a half-known size would resize the child to zero rows",
		},
		{
			name: "single column",
			cols: 1, rows: 40,
			why: "cols-1 would be 0; resizeKick skips the jiggle, so only the restore lands",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeSession{}
			var pty *fakeSession
			if !tt.nilPTY {
				pty = fake
			}

			// Must not panic on a nil PTY, which is the whole point of the
			// running check at the call site.
			if pty == nil {
				redrawKick("pane-1", "claude-code", nil, tt.cols, tt.rows)
			} else {
				redrawKick("pane-1", "claude-code", pty, tt.cols, tt.rows)
			}

			if tt.cols == 1 {
				// Degenerate but valid: resizeKick guards cols > 1 for the
				// jiggle, so exactly one Resize lands. Pinned so a future
				// change to that guard is visible here.
				if len(fake.resizes) != 1 {
					t.Errorf("resizes = %v, want 1 (restore only, no jiggle at width 1)", fake.resizes)
				}
				return
			}
			if len(fake.resizes) != 0 {
				t.Errorf("resizes = %v, want none — %s", fake.resizes, tt.why)
			}
		})
	}
}

// TestPaneSize_ConcurrentResizeAndRead is a race-detector regression guard.
//
// pane.Cols/Rows used to be written just OUTSIDE handleResizePane's PluginMu
// span, and were documented on the struct as "immutable once set". They are
// not: handleResizePane rewrites them on every genuine resize from the
// resizing conn's dispatch goroutine, while handleAttach (a different conn),
// the PTY output goroutine's resizeKick, and snapshot() all read them. Three
// live data races, invisible because no test ran a resize concurrently with a
// read — `go test -race` only reports races it actually executes.
//
// This fails under -race if the write moves back out of the lock, or if a new
// reader takes the field without it.
func TestPaneSize_ConcurrentResizeAndRead(t *testing.T) {
	d := &Daemon{session: NewSessionManager(4096)}
	pane := &Pane{ID: "p1", PTY: &fakeSession{}}
	d.session.panes["p1"] = pane

	const iterations = 200

	// Built on the test goroutine, not inside the writer below: resizeMsg
	// calls t.Fatalf, and testing.T's failure methods are only valid from the
	// goroutine running the test. Alternating sizes so the same-size guard
	// never short-circuits the write — a guard hit would make this vacuous.
	msgs := make([]*ipc.Message, iterations)
	for i := range msgs {
		msgs[i] = resizeMsg(t, "p1", uint16(100+i%2), 40)
	}

	done := make(chan struct{})

	// Writer: the resizing client's dispatch goroutine.
	go func() {
		defer close(done)
		for _, m := range msgs {
			d.handleResizePane(m)
		}
	}()

	// Reader: stands in for handleAttach / resizeKick / snapshot, all of which
	// read the pair the same way.
	for i := 0; i < iterations; i++ {
		pane.PluginMu.Lock()
		cols, rows := pane.Cols, pane.Rows
		pane.PluginMu.Unlock()
		if cols != 0 && (cols < 100 || cols > 101 || rows != 40) {
			t.Fatalf("torn read: %dx%d", cols, rows)
		}
	}
	<-done
}
