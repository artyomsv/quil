package transport

import (
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
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
