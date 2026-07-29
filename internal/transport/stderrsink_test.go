package transport

import (
	"bytes"
	"io"
	"sync"
	"testing"
)

func TestSwitchWriter_RoutesToCurrentSink(t *testing.T) {
	var first, second bytes.Buffer
	sw := &switchWriter{w: &first}

	if _, err := sw.Write([]byte("before")); err != nil {
		t.Fatalf("write: %v", err)
	}
	sw.Set(&second)
	if _, err := sw.Write([]byte("after")); err != nil {
		t.Fatalf("write: %v", err)
	}

	if got := first.String(); got != "before" {
		t.Errorf("first sink = %q, want %q", got, "before")
	}
	if got := second.String(); got != "after" {
		t.Errorf("second sink = %q, want %q", got, "after")
	}
}

// A nil sink discards rather than panicking or erroring: exec's copier goroutine
// outlives the TUI's log file, and an error there would only be reported to the
// writer that just went away.
func TestSwitchWriter_NilSinkDiscards(t *testing.T) {
	sw := &switchWriter{}

	n, err := sw.Write([]byte("dropped"))
	if err != nil {
		t.Fatalf("write to nil sink: %v", err)
	}
	if n != len("dropped") {
		t.Errorf("n = %d, want %d — a short write would make the copier retry forever", n, len("dropped"))
	}
}

// exec's copier goroutine writes while the TUI swaps the sink. Meaningful under
// -race, harmless without it.
func TestSwitchWriter_ConcurrentSetAndWrite(t *testing.T) {
	sw := &switchWriter{w: io.Discard}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			sw.Write([]byte("x"))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			sw.Set(io.Discard)
		}
	}()
	wg.Wait()
}

func TestStdioConn_SatisfiesStderrRedirector(t *testing.T) {
	var _ StderrRedirector = (*stdioConn)(nil)
}

// A batch dial captured stderr into a buffer and never reached the terminal, so
// there is nothing to redirect. It must not panic.
func TestStdioConn_RedirectStderr_NoopWhenCaptured(t *testing.T) {
	c := &stdioConn{}
	c.RedirectStderr(io.Discard)
	c.RedirectStderr(nil)
}

// The redirect must actually move the bytes: writes before the swap reach the
// terminal sink, writes after reach the log sink.
func TestStdioConn_RedirectStderr_MovesSubsequentWrites(t *testing.T) {
	var term, logSink bytes.Buffer
	sw := &switchWriter{w: &term}
	c := &stdioConn{termErr: sw}

	sanitizer := &terminalSanitizer{w: sw}
	sanitizer.Write([]byte("host key prompt\n"))

	c.RedirectStderr(&logSink)
	sanitizer.Write([]byte("packet_write_wait: Broken pipe\n"))

	if !bytes.Contains(term.Bytes(), []byte("host key prompt")) {
		t.Errorf("pre-swap write missed the terminal sink: %q", term.String())
	}
	if bytes.Contains(term.Bytes(), []byte("Broken pipe")) {
		t.Errorf("post-swap write still reached the terminal: %q", term.String())
	}
	if !bytes.Contains(logSink.Bytes(), []byte("Broken pipe")) {
		t.Errorf("post-swap write missed the log sink: %q", logSink.String())
	}
}
