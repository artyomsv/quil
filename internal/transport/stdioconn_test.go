package transport

import (
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// pipePair builds a stdioConn wired to two os.Pipes with no child process,
// so the adapter can be tested without spawning anything.
func pipePair(t *testing.T) (c *stdioConn, feed *os.File, drain *os.File) {
	t.Helper()
	// Data the conn will READ arrives on inR; tests write to inW.
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	// Data the conn WRITES lands on outW; tests read from outR.
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	c = newStdioConn(exec.Command("nonexistent-by-design"), inR, outW, "test")
	t.Cleanup(func() { c.Close(); inW.Close(); outR.Close() })
	return c, inW, outR
}

func TestStdioConn_Read_ReturnsWrittenBytes(t *testing.T) {
	c, feed, _ := pipePair(t)

	go func() { feed.Write([]byte("hello")) }()

	buf := make([]byte, 5)
	n, err := io.ReadFull(c, buf)
	if err != nil {
		t.Fatalf("ReadFull: %v", err)
	}
	if got := string(buf[:n]); got != "hello" {
		t.Errorf("read %q, want %q", got, "hello")
	}
}

// TestStdioConn_Read_SplitsLargePayloadAcrossCalls pins that a held-over
// remainder is returned on the next Read rather than dropped — bufio.Reader
// calls Read with whatever capacity it has left.
func TestStdioConn_Read_SplitsLargePayloadAcrossCalls(t *testing.T) {
	c, feed, _ := pipePair(t)

	go func() { feed.Write([]byte("abcdef")) }()

	first := make([]byte, 2)
	if _, err := io.ReadFull(c, first); err != nil {
		t.Fatalf("first ReadFull: %v", err)
	}
	if string(first) != "ab" {
		t.Fatalf("first read %q, want %q", first, "ab")
	}

	rest := make([]byte, 4)
	if _, err := io.ReadFull(c, rest); err != nil {
		t.Fatalf("second ReadFull: %v", err)
	}
	if string(rest) != "cdef" {
		t.Errorf("second read %q, want %q", rest, "cdef")
	}
}

// TestStdioConn_ReadDeadline_TimesOutAsNetError is the load-bearing one:
// cmd/quil/handshake.go does errors.As(err, &netErr) && netErr.Timeout().
func TestStdioConn_ReadDeadline_TimesOutAsNetError(t *testing.T) {
	c, _, _ := pipePair(t)

	if err := c.SetReadDeadline(time.Now().Add(30 * time.Millisecond)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}

	buf := make([]byte, 4)
	_, err := c.Read(buf)
	if err == nil {
		t.Fatal("Read succeeded, want timeout")
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("err = %v, want a net.Error with Timeout() == true", err)
	}
}

// TestStdioConn_ReadDeadline_ZeroClearsIt pins that handshake.go's
// `defer client.SetReadDeadline(time.Time{})` actually re-enables blocking.
func TestStdioConn_ReadDeadline_ZeroClearsIt(t *testing.T) {
	c, feed, _ := pipePair(t)

	if err := c.SetReadDeadline(time.Now().Add(10 * time.Millisecond)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	time.Sleep(20 * time.Millisecond) // deadline is now in the past
	if err := c.SetReadDeadline(time.Time{}); err != nil {
		t.Fatalf("clear SetReadDeadline: %v", err)
	}

	go func() {
		time.Sleep(30 * time.Millisecond)
		feed.Write([]byte("late"))
	}()

	buf := make([]byte, 4)
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("ReadFull after clearing deadline: %v", err)
	}
	if string(buf) != "late" {
		t.Errorf("read %q, want %q", buf, "late")
	}
}

func TestStdioConn_Write_ReachesTheChild(t *testing.T) {
	c, _, drain := pipePair(t)

	go func() { c.Write([]byte("ping")) }()

	buf := make([]byte, 4)
	if _, err := io.ReadFull(drain, buf); err != nil {
		t.Fatalf("ReadFull: %v", err)
	}
	if string(buf) != "ping" {
		t.Errorf("child got %q, want %q", buf, "ping")
	}
}

func TestStdioConn_SetWriteDeadline_ReportsUnsupported(t *testing.T) {
	c, _, _ := pipePair(t)
	if err := c.SetWriteDeadline(time.Now().Add(time.Second)); !errors.Is(err, os.ErrNoDeadline) {
		t.Fatalf("err = %v, want os.ErrNoDeadline", err)
	}
}

func TestStdioConn_ReadAfterClose_ReturnsError(t *testing.T) {
	c, _, _ := pipePair(t)
	c.Close()

	if _, err := c.Read(make([]byte, 4)); err == nil {
		t.Fatal("Read after Close succeeded, want error")
	}
}

func TestStdioConn_Close_IsIdempotent(t *testing.T) {
	c, _, _ := pipePair(t)
	if err := c.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestStdioConn_Addrs_AreDescriptive(t *testing.T) {
	c, _, _ := pipePair(t)
	if got := c.RemoteAddr().Network(); got != "stdio" {
		t.Errorf("Network() = %q, want %q", got, "stdio")
	}
	if got := c.RemoteAddr().String(); got != "test" {
		t.Errorf("String() = %q, want %q", got, "test")
	}
}

// TestStdioConn_Close_UnblocksBlockedRead pins that a Read parked in the
// select (no data, no deadline) returns promptly once Close fires, via the
// <-c.done case, rather than hanging forever.
func TestStdioConn_Close_UnblocksBlockedRead(t *testing.T) {
	c, _, _ := pipePair(t)

	result := make(chan error, 1)
	go func() {
		_, err := c.Read(make([]byte, 4))
		result <- err
	}()

	time.Sleep(30 * time.Millisecond) // let Read park in the select
	c.Close()

	select {
	case err := <-result:
		if err == nil {
			t.Fatal("Read succeeded after Close, want an error")
		}
	case <-time.After(time.Second):
		t.Fatal("Read did not return after Close — blocked select never unblocked")
	}
}

// TestStdioConn_Close_UnblocksPumpBlockedOnSend pins that the pump goroutine
// does not leak when it is blocked handing a chunk to readCh (buffer full,
// nothing draining it) at the moment Close fires. The first chunk fills the
// buffered channel; the second is read by the pump but can't be delivered,
// parking it in the `case c.readCh <- chunk` / `case <-c.done` select. Close
// must free it via the done case so the pump's deferred close(readCh) runs.
func TestStdioConn_Close_UnblocksPumpBlockedOnSend(t *testing.T) {
	c, feed, _ := pipePair(t)

	go func() { feed.Write([]byte("first")) }()
	time.Sleep(30 * time.Millisecond) // pump reads "first", fills readCh (cap 1)

	go func() { feed.Write([]byte("second")) }()
	time.Sleep(30 * time.Millisecond) // pump reads "second", blocks trying to send it

	c.Close()

	// Drain whatever was buffered; the channel must eventually report closed
	// (proving the pump exited) rather than hang forever.
	drained := make(chan struct{})
	go func() {
		for {
			if _, ok := <-c.readCh; !ok {
				close(drained)
				return
			}
		}
	}()

	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("pump never exited after Close (readCh stayed open) — goroutine leak")
	}
}

// --- LinkErr -----------------------------------------------------------
//
// LinkErr is what lets a caller tell "the ssh channel died" apart from "the
// daemon answered badly". Both reach the version gate as a failed read, and
// without this distinction an unreachable host is reported as a version
// mismatch.

func TestStdioConn_LinkErr_NilWhileLinkIsHealthy(t *testing.T) {
	c, _, _ := pipePair(t)

	if err := c.LinkErr(); err != nil {
		t.Errorf("LinkErr() on a live conn = %v, want nil", err)
	}
}

// drainUntilError reads until the conn reports an error, which guarantees the
// pump has already recorded pumpErr (pump sets it before its deferred
// close(readCh) runs, and Read only observes the closed channel after that).
// Deterministic, so the test needs no sleep.
func drainUntilError(t *testing.T, c *stdioConn) error {
	t.Helper()
	buf := make([]byte, 64)
	for i := 0; i < 100; i++ {
		if _, err := c.Read(buf); err != nil {
			return err
		}
	}
	t.Fatal("conn never reported a read error")
	return nil
}

func TestStdioConn_LinkErr_ReportsFailureAfterChildStdoutCloses(t *testing.T) {
	c, feed, _ := pipePair(t)

	// Closing the far end is what an exiting ssh process does to our read pipe.
	feed.Close()
	drainUntilError(t, c)

	err := c.LinkErr()
	if err == nil {
		t.Fatal("LinkErr() after the pipe closed = nil, want an error")
	}
	if !strings.Contains(err.Error(), "test") {
		t.Errorf("LinkErr() = %q, want it to name the destination %q", err, "test")
	}
}

// TestStdioConn_LinkErr_PrefersCapturedStderr pins that ssh's own diagnosis
// wins over our generic pipe error. "Could not resolve hostname" tells the user
// what to fix; "read |0: EOF" does not.
func TestStdioConn_LinkErr_PrefersCapturedStderr(t *testing.T) {
	c, feed, _ := pipePair(t)

	const sshSays = "ssh: Could not resolve hostname gpu01: Name or service not known"
	c.stderr = &lockedBuffer{}
	c.stderr.Write([]byte(sshSays + "\n"))

	feed.Close()
	drainUntilError(t, c)

	err := c.LinkErr()
	if err == nil {
		t.Fatal("LinkErr() = nil, want an error")
	}
	if !strings.Contains(err.Error(), sshSays) {
		t.Errorf("LinkErr() = %q, want it to carry ssh's message %q", err, sshSays)
	}
	if strings.Contains(err.Error(), "EOF") {
		t.Errorf("LinkErr() = %q, want ssh's message INSTEAD of the raw pipe error", err)
	}
}

// TestStdioConn_LinkErr_SatisfiesLinkStatus guards the seam the version gate
// type-asserts on: a change to the method set here breaks that call site
// silently, because a failed assertion just leaves the check disabled.
func TestStdioConn_LinkErr_SatisfiesLinkStatus(t *testing.T) {
	c, _, _ := pipePair(t)

	var conn net.Conn = c
	if _, ok := conn.(LinkStatus); !ok {
		t.Error("a stdioConn behind a net.Conn no longer satisfies LinkStatus")
	}
}

// TestStdioConn_Close_ReturnsWhilePumpIsParkedInRead pins the descriptor
// ownership that keeps Close off the pump's in-flight read.
//
// Windows pipe handles are non-overlapped, so a parked ReadFile cannot be
// cancelled and internal/poll.FD.Close blocks until its reference drops. If
// Close ever closes c.r itself while the pump is inside Read, the two deadlock:
// the kill that would end the read sits BELOW the close that never returns.
// Without this test that shows up only as the whole package timing out at
// random, which reads as flakiness rather than as the bug it is.
func TestStdioConn_Close_ReturnsWhilePumpIsParkedInRead(t *testing.T) {
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	// inW stays open for the whole test so the pump's read genuinely parks
	// instead of taking an immediate EOF. Cleanup rather than defer: it must
	// outlive the Close under test, and closing it is what finally lets the
	// pump exit and release inR.
	t.Cleanup(func() { inW.Close(); outR.Close() })

	// A never-started command, matching pipePair: Process is nil, so Close
	// skips the kill and reaches the pipe closes with nothing to unpark it.
	c := newStdioConn(exec.Command("nonexistent-by-design"), inR, outW, "parked-read")

	// Let the pump reach its read. Without this the race is not set up and the
	// test would pass for the wrong reason.
	time.Sleep(300 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Close()
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close blocked while the pump was parked in Read — the read " +
			"descriptor is being closed from the wrong goroutine")
	}
}
