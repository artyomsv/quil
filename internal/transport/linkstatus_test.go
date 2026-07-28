package transport

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// --- Established --------------------------------------------------------
//
// Established, not LinkErr, is what distinguishes "the link never came up"
// from "the peer answered badly". LinkErr goes non-nil only once the child has
// DIED, so on its own it misses every slow failure: a firewalled port or a
// dead IP leaves ssh alive and still connecting while the caller's much
// shorter handshake deadline expires.

func TestStdioConn_Established_FalseBeforeAnyBytesArrive(t *testing.T) {
	c, _, _ := pipePair(t)

	if c.Established() {
		t.Error("Established() = true on a conn that has received nothing")
	}
}

func TestStdioConn_Established_TrueAfterBytesArrive(t *testing.T) {
	c, feed, _ := pipePair(t)

	go func() { feed.Write([]byte("x")) }()
	buf := make([]byte, 1)
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("ReadFull: %v", err)
	}

	if !c.Established() {
		t.Error("Established() = false after a byte was delivered")
	}
}

// TestStdioConn_Established_StaysFalseWhileChildIsAliveButSilent reproduces the
// exact slow-failure shape: the pipe is open, nothing has failed, and nothing
// has arrived. LinkErr is nil here — which is precisely why it cannot be the
// discriminator on its own.
func TestStdioConn_Established_StaysFalseWhileChildIsAliveButSilent(t *testing.T) {
	c, _, _ := pipePair(t)

	if err := c.SetReadDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	buf := make([]byte, 8)
	if _, err := c.Read(buf); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("Read err = %v, want os.ErrDeadlineExceeded", err)
	}

	if got := c.LinkErr(); got != nil {
		t.Errorf("LinkErr() = %v on a live-but-silent link, want nil", got)
	}
	if c.Established() {
		t.Error("Established() = true though nothing was ever received")
	}
}

// TestStdioConn_Established_RemainsTrueAfterTheLinkDies pins monotonicity. The
// caller checks after a read has already failed, so a flag that reverted on
// death would report a peer that DID answer as never-connected.
func TestStdioConn_Established_RemainsTrueAfterTheLinkDies(t *testing.T) {
	c, feed, _ := pipePair(t)

	go func() { feed.Write([]byte("y")) }()
	buf := make([]byte, 1)
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("ReadFull: %v", err)
	}
	feed.Close()
	drainUntilError(t, c)

	if !c.Established() {
		t.Error("Established() reverted to false after the link died")
	}
}

// --- terminal sanitisation ----------------------------------------------

// TestSanitizeForTerminal pins that anything a terminal would ACT on is
// removed. ssh multiplexes the remote command's fd 2 onto its own stderr, so
// this text is attacker-controlled whenever the remote host is, and it ends up
// printed to a local terminal.
func TestSanitizeForTerminal(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain text survives", "Could not resolve hostname gpu01", "Could not resolve hostname gpu01"},
		{"newline and tab survive", "line1\n\tline2", "line1\n\tline2"},
		{"ESC introducer dropped", "red\x1b[31mtext", "red[31mtext"},
		{"OSC 52 clipboard write defanged", "\x1b]52;c;cGF5bG9hZA==\x07ok", "]52;c;cGF5bG9hZA==ok"},
		{"bare CR dropped", "over\rwrite", "overwrite"},
		{"DEL dropped", "a\x7fb", "ab"},
		{"C1 CSI dropped", "a31mb", "a31mb"},
		{"NUL dropped", "a\x00b", "ab"},
		{"unicode preserved", "café ✓", "café ✓"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeForTerminal(tt.in); got != tt.want {
				t.Errorf("sanitizeForTerminal(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestSanitizeForTerminal_DefangsRawC1Byte covers the other spelling of the C1
// CSI introducer: a bare 0x9b byte inside an otherwise-UTF-8 stream. That is
// what a terminal actually acts on, and raw C1 bytes in UTF-8 have bitten this
// project before (see the OSC window-title filter note in .claude/CLAUDE.md).
//
// It is neutralised by a different mechanism than the rune-range check:
// strings.Map decodes the invalid byte to U+FFFD and re-encodes it, so the 0x9b
// never reaches the terminal. The assertion is "the byte is gone" rather than
// "the character is dropped", which pins the property that matters without
// over-specifying how it is achieved.
func TestSanitizeForTerminal_DefangsRawC1Byte(t *testing.T) {
	got := sanitizeForTerminal("a\x9b31mb")

	if strings.ContainsRune(got, 0x9b) {
		t.Errorf("sanitizeForTerminal kept a raw C1 CSI byte: %q", got)
	}
	if !strings.HasPrefix(got, "a") || !strings.HasSuffix(got, "31mb") {
		t.Errorf("sanitizeForTerminal(%q) = %q, want surrounding text intact", "a\x9b31mb", got)
	}
}

func TestStdioConn_LinkErr_SanitizesRemoteControlledStderr(t *testing.T) {
	c, feed, _ := pipePair(t)

	c.stderr = &lockedBuffer{}
	c.stderr.Write([]byte("remote said: \x1b]52;c;cGF5bG9hZA==\x07\x1b[2J\n"))

	feed.Close()
	drainUntilError(t, c)

	msg := c.LinkErr().Error()
	for _, forbidden := range []string{"\x1b", "\x07"} {
		if strings.Contains(msg, forbidden) {
			t.Errorf("LinkErr() = %q still contains control byte %q", msg, forbidden)
		}
	}
	if !strings.Contains(msg, "remote said:") {
		t.Errorf("LinkErr() = %q dropped the readable text along with the controls", msg)
	}
}

// --- Read/Close contract ------------------------------------------------

// TestStdioConn_ReadAfterClose_ReturnsErrorEvenWithHeldRemainder covers the
// branch the plain read-after-close test cannot reach: with bytes pending in
// `held`, the fast path used to hand them back after Close.
func TestStdioConn_ReadAfterClose_ReturnsErrorEvenWithHeldRemainder(t *testing.T) {
	c, feed, _ := pipePair(t)

	go func() { feed.Write([]byte("0123456789")) }()
	small := make([]byte, 4)
	if _, err := c.Read(small); err != nil { // leaves 6 bytes in held
		t.Fatalf("first Read: %v", err)
	}

	c.Close()

	if _, err := c.Read(small); !errors.Is(err, net.ErrClosed) {
		t.Errorf("Read after Close = %v, want net.ErrClosed", err)
	}
}

// --- destination validation ---------------------------------------------

// TestSSH_RejectsOptionShapedDestination guards an argument-injection path. The
// destination is appended after our -o flags, so ssh is still parsing options
// when it reaches it: a leading '-' makes it an option, and -oProxyCommand=
// executes an arbitrary LOCAL command before any network traffic (confirmed
// against OpenSSH 10.2p1). Rejected here as well as at the CLI flag, because
// this package is reachable by any caller.
func TestSSH_RejectsOptionShapedDestination(t *testing.T) {
	for _, dest := range []string{"-oProxyCommand=/tmp/x.sh", "-oPermitLocalCommand=yes", "-v"} {
		conn, err := SSH(dest, SSHOptions{})(context.Background())
		if err == nil {
			conn.Close()
			t.Errorf("SSH(%q) succeeded, want rejection", dest)
			continue
		}
		if !strings.Contains(err.Error(), "must not begin with '-'") {
			t.Errorf("SSH(%q) err = %v, want a leading-dash rejection", dest, err)
		}
	}
}

// TestTruncateForMessage_BoundsRemoteText pins that a hostile or merely chatty
// remote host cannot push unbounded text into one error line. The buffer this
// draws from is filled by the far side and has no size limit of its own.
func TestTruncateForMessage_BoundsRemoteText(t *testing.T) {
	short := "ssh: Could not resolve hostname gpu01"
	if got := truncateForMessage(short); got != short {
		t.Errorf("truncateForMessage shortened a short message: %q", got)
	}

	long := strings.Repeat("A", maxStderrInMessage*3)
	got := truncateForMessage(long)
	if len(got) > maxStderrInMessage+len("…[truncated]") {
		t.Errorf("truncated length %d exceeds the cap", len(got))
	}
	if !strings.HasSuffix(got, "…[truncated]") {
		t.Errorf("truncated message lacks its marker: ...%q", got[max(0, len(got)-20):])
	}
}

// TestTruncateForMessage_CutsOnARuneBoundary — truncation must not manufacture
// invalid UTF-8, which is exactly the class of bug the C1 handling above exists
// to deal with.
func TestTruncateForMessage_CutsOnARuneBoundary(t *testing.T) {
	// Multi-byte runes straddling the cut point.
	got := truncateForMessage(strings.Repeat("é", maxStderrInMessage))
	if !utf8.ValidString(got) {
		t.Errorf("truncateForMessage produced invalid UTF-8: %q", got)
	}
}

// --- interactive stderr sanitisation -------------------------------------

// TestTerminalSanitizer_StripsControlsFromAStream guards the one unfiltered
// escape path from the remote host to the local terminal. ssh multiplexes the
// remote command's fd 2 onto its own stderr, and on an interactive dial that fd
// stays attached to the terminal for the whole session — so a compromised
// remote could otherwise write OSC 52 (clipboard) or CSI directly to the
// operator's screen, bypassing everything the pane path filters.
func TestTerminalSanitizer_StripsControlsFromAStream(t *testing.T) {
	var out strings.Builder
	s := &terminalSanitizer{w: &out}

	in := []byte("ssh: connect failed\n\x1b]52;c;cGF5bG9hZA==\x07\x1b[2Jspoofed\ttext\r\n")
	n, err := s.Write(in)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	// Short counts make io.Copy and exec's writer goroutine treat filtering as
	// a write error, so the full input length must be reported as consumed.
	if n != len(in) {
		t.Errorf("Write returned n=%d, want %d (the caller's full length)", n, len(in))
	}

	got := out.String()
	for _, forbidden := range []string{"\x1b", "\x07", "\r"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("output %q still contains control byte %q", got, forbidden)
		}
	}
	// Readable diagnostics must survive — they are the reason stderr is shown
	// at all, and ssh explains failures better than Quil can.
	if !strings.Contains(got, "ssh: connect failed") {
		t.Errorf("output %q dropped ssh's own message", got)
	}
	if !strings.Contains(got, "spoofed\ttext") {
		t.Errorf("output %q dropped tab or text, which must be preserved", got)
	}
}

// TestTerminalSanitizer_PreservesUTF8AcrossWrites pins why this filters at BYTE
// level rather than reusing the rune-based sanitizeForTerminal: every byte it
// drops is < 0x80, so a multi-byte rune split across two Writes survives. A
// rune-level filter would mangle the boundary.
func TestTerminalSanitizer_PreservesUTF8AcrossWrites(t *testing.T) {
	var out strings.Builder
	s := &terminalSanitizer{w: &out}

	// "é" is C3 A9 — split deliberately across the two calls.
	if _, err := s.Write([]byte{0xC3}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := s.Write([]byte{0xA9}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if got := out.String(); got != "é" {
		t.Errorf("output = %q, want %q — a split rune was corrupted", got, "é")
	}
}
