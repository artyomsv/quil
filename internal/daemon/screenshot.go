package daemon

import (
	"errors"
	"io"
	"log"
	"strings"
	"time"

	"github.com/charmbracelet/x/vt"
)

// screenshotDrainStop bounds the wait for the drain goroutine to see the
// closed pipe. Closing the pipe ends its Read at once, so this only matters if
// the library ever stops honouring io.Pipe's close contract.
const screenshotDrainStop = time.Second

// renderScreenshot replays raw into a throwaway emulator of width x height and
// returns the visible rows (trailing blanks trimmed) and the cursor.
//
// The emulator ANSWERS terminal queries — DA1 (`CSI c`), DSR (`CSI 6n`),
// DECRQM and others — by writing the reply into an internal io.Pipe, and a
// pipe write blocks until somebody reads it. Claude Code sends such queries on
// every start, so its ring buffer nearly always holds one. Without a reader,
// em.Write parks for good — and this runs on the requesting connection's
// dispatch goroutine, so that connection never had another request read:
// every later MCP call from the same bridge timed out (2026-11-11, production
// log: the last line from that conn was `ipc recv: screenshot_pane_req`).
// The TUI met the same trap first and drains the same way (drainVTResponses,
// internal/tui/pane.go).
//
// The pipe is closed through its WRITER, never em.Close: Emulator.Close writes
// an unsynchronised `closed` flag that the drain's Read checks, which is a
// data race, while closing an io.Pipe concurrently with a Read is part of the
// pipe's contract.
func renderScreenshot(raw []byte, width, height int) (string, int, int) {
	em := vt.NewSafeEmulator(width, height)
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 256)
		for {
			if _, err := em.Read(buf); err != nil {
				if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
					log.Printf("screenshot: VT drain exited unexpectedly: %v", err)
				}
				return
			}
		}
	}()

	_, _ = em.Write(raw) // writes to an in-memory grid; it cannot fail short of a closed emulator

	var lines []string
	for y := 0; y < height; y++ {
		var line strings.Builder
		for x := 0; x < width; x++ {
			cell := em.CellAt(x, y)
			if cell != nil && cell.Content != "" {
				line.WriteString(cell.Content)
			} else {
				line.WriteByte(' ')
			}
		}
		lines = append(lines, strings.TrimRight(line.String(), " "))
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	cursor := em.CursorPosition()

	// Every writer of the pipe ran inside em.Write above, so nothing writes
	// concurrently with this close.
	if pw, ok := em.InputPipe().(*io.PipeWriter); ok {
		_ = pw.CloseWithError(io.EOF) // CloseWithError on an io.PipeWriter always returns nil
	} else {
		// A library change behind our back: fall back to the emulator's own
		// close. Racy against the drain's Read, but bounded, and logged so the
		// change does not go unnoticed.
		log.Printf("screenshot: emulator input pipe is %T, not *io.PipeWriter — closing the emulator instead", em.InputPipe())
		_ = em.Close() // Close only reports the pipe's close result, which is nil
	}
	select {
	case <-done:
	case <-time.After(screenshotDrainStop):
		log.Printf("screenshot: VT drain did not stop within %v", screenshotDrainStop)
	}
	return strings.Join(lines, "\n"), cursor.X, cursor.Y
}
