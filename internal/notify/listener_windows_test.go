//go:build windows

package notify

import (
	"os"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// writeRawToPipe bypasses SendActivate's own validation so the LISTENER's guard
// is what is under test. The pipe is reachable by any same-user process, so it
// cannot rely on its callers being well behaved.
func writeRawToPipe(t *testing.T, pid int, payload string) {
	t.Helper()
	// Uses the same dial helper SendActivate does, so this exercises the
	// listener's VALIDATION rather than re-racing the connection. A raw
	// CreateFile here failed on the transient busy window and looked like a
	// listener bug.
	h, err := dialActivatePipe(pid)
	if err != nil {
		t.Fatalf("dialling pipe: %v", err)
	}
	defer windows.CloseHandle(h)
	b := []byte(payload)
	if len(b) == 0 {
		b = []byte{0}
	}
	var n uint32
	if err := windows.WriteFile(h, b, &n, nil); err != nil {
		t.Fatalf("writing to pipe: %v", err)
	}
}

func TestListenSendActivate_RoundTrip(t *testing.T) {
	pid := os.Getpid()
	got := make(chan string, 1)

	srv, err := Listen(pid, func(paneID string) { got <- paneID })
	if err != nil {
		t.Fatalf("Listen() = %v", err)
	}
	defer srv.Close()

	if err := SendActivate(pid, "pane-0a1b2c3d"); err != nil {
		t.Fatalf("SendActivate() = %v", err)
	}
	select {
	case id := <-got:
		if id != "pane-0a1b2c3d" {
			t.Errorf("received %q, want pane-0a1b2c3d", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("activation never reached the listener")
	}
}

// A malformed frame must be dropped WITHOUT killing the accept loop: one bad
// local caller must not disable activation for the whole session. The
// well-formed id at the end is what proves the listener is still alive rather
// than merely quiet — a test that only asserted silence would pass against a
// listener that died on the first bad frame.
func TestListen_RejectsMalformedPaneIDsAndKeepsServing(t *testing.T) {
	pid := os.Getpid() + 1 // distinct pipe from the round-trip test
	got := make(chan string, 4)

	srv, err := Listen(pid, func(paneID string) { got <- paneID })
	if err != nil {
		t.Fatalf("Listen() = %v", err)
	}
	defer srv.Close()

	for _, bad := range []string{"", "../../etc/passwd", "pane-BADBEEF", "pane-0a1b2c3d4", "pane-0a1b2c3g"} {
		writeRawToPipe(t, pid, bad)
	}
	if err := SendActivate(pid, "pane-11111111"); err != nil {
		t.Fatalf("SendActivate() after bad frames = %v", err)
	}
	select {
	case id := <-got:
		if id != "pane-11111111" {
			t.Errorf("first delivered id = %q, want the only well-formed one", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("listener stopped serving after a malformed frame")
	}
}

// Clicking a toast whose TUI has since exited must not pop an error window, so
// the caller needs to be able to tell "nobody home" apart from a real failure.
func TestSendActivate_ReportsMissingListener(t *testing.T) {
	if err := SendActivate(999999, "pane-0a1b2c3d"); err == nil {
		t.Error("SendActivate() to a pid with no listener = nil, want an error the caller can swallow")
	}
}

func TestSendActivate_RefusesMalformedPaneID(t *testing.T) {
	if err := SendActivate(os.Getpid(), "../../etc"); err == nil {
		t.Error("SendActivate() accepted a malformed pane id")
	}
}

// Close must be safe to call twice — cmd/quil defers it and a caller may also
// close explicitly.
func TestPipeListener_CloseIsIdempotent(t *testing.T) {
	srv, err := Listen(os.Getpid()+2, func(string) {})
	if err != nil {
		t.Fatalf("Listen() = %v", err)
	}
	if err := srv.Close(); err != nil {
		t.Errorf("first Close() = %v", err)
	}
	if err := srv.Close(); err != nil {
		t.Errorf("second Close() = %v", err)
	}
}
