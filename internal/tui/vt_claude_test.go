package tui

import (
	"bytes"
	"testing"
	"time"
)

const testRingBufSize = 64 * 1024

// TestPaneModel_AppendOutput_DoesNotDeadlockOnVTQueries verifies that the
// VT emulator does not deadlock when a TUI app sends terminal-capability
// queries.
//
// Background: charmbracelet/x/vt answers CSI c (Primary Device Attributes)
// and similar queries by writing to an internal io.Pipe. Without a reader,
// that pipe's buffer (one chunk) fills and subsequent writes block inside
// vt.Write(). When Claude Code 2.1.110 sends "\x1b[c" during its startup,
// the entire TUI froze at AppendOutput() because there was no drain. This
// test feeds the problem sequences to a fresh PaneModel with a 2-second
// deadline — it must complete quickly.
func TestPaneModel_AppendOutput_DoesNotDeadlockOnVTQueries(t *testing.T) {
	cases := []struct {
		name string
		data []byte
	}{
		{"DA1 Primary Device Attributes", []byte("\x1b[c")},
		{"DA2 Secondary Device Attributes", []byte("\x1b[>c")},
		{"DSR cursor position report", []byte("\x1b[6n")},
		{"repeated DA1 bursts", bytes.Repeat([]byte("\x1b[c"), 20)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pane := NewPaneModel("test", testRingBufSize)
			pane.ResizeVT(120, 30)

			done := make(chan struct{})
			go func() {
				defer close(done)
				pane.AppendOutput(tc.data)
			}()

			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatalf("AppendOutput deadlocked on %s (VT response pipe not drained)", tc.name)
			}
		})
	}
}
