package transport

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSSHArgs_PassesDestinationVerbatim(t *testing.T) {
	// A ~/.ssh/config Host alias must survive untouched — parsing it into
	// user/host/port and reassembling would discard HostName, Port, User and
	// ProxyJump, and a bastion hop is the common case for a cluster.
	for _, dest := range []string{"gpu01", "user@gpu01", "user@gpu01.example.com"} {
		args := sshArgs(dest, SSHOptions{})
		found := false
		for i, a := range args {
			if a == dest {
				found = true
				// The destination must be immediately followed by the remote
				// command and be the last option-free token before it.
				if i != len(args)-2 {
					t.Errorf("dest %q at index %d, want second-to-last of %v", dest, i, args)
				}
			}
		}
		if !found {
			t.Errorf("dest %q not present verbatim in %v", dest, args)
		}
	}
}

// TestSSHArgs_ForcesSecurityOptions asserts each forced option positionally —
// a ("-o", want) adjacent pair — rather than via a space-joined Contains
// check. Joining with spaces makes "-o ForwardAgent=no" (two argv elements,
// what OpenSSH expects) indistinguishable from "-o ForwardAgent=no" produced
// by a single malformed element (which OpenSSH would mis-parse): both join
// to the same string, so a refactor that broke the pairing would still pass.
func TestSSHArgs_ForcesSecurityOptions(t *testing.T) {
	args := sshArgs("gpu01", SSHOptions{})
	for _, want := range []string{
		"ForwardAgent=no",
		"ForwardX11=no",
		"ForwardX11Trusted=no",
		"PermitLocalCommand=no",
		"ClearAllForwardings=yes",
		"RequestTTY=no",
	} {
		found := false
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "-o" && args[i+1] == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing forced option pair (\"-o\", %q) in %v", want, args)
		}
	}
}

// TestSSHArgs_DoesNotForceHostKeyChecking pins a deliberate decision:
// StrictHostKeyChecking=accept-new is WEAKER than the default prompt, so the
// user's own policy is left alone.
func TestSSHArgs_DoesNotForceHostKeyChecking(t *testing.T) {
	joined := strings.Join(sshArgs("gpu01", SSHOptions{}), " ")
	if strings.Contains(joined, "StrictHostKeyChecking") {
		t.Errorf("StrictHostKeyChecking must not be forced, got %q", joined)
	}
}

// TestSSHArgs_DoesNotForceProxyOptions pins that bastion support survives.
func TestSSHArgs_DoesNotForceProxyOptions(t *testing.T) {
	joined := strings.Join(sshArgs("gpu01", SSHOptions{}), " ")
	for _, forbidden := range []string{"ProxyCommand", "ProxyJump", "ControlMaster", "IdentityFile"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("must not force %q, got %q", forbidden, joined)
		}
	}
}

func TestSSHArgs_NoTTY(t *testing.T) {
	// -T is mandatory: a PTY would apply CRLF translation and corrupt the
	// 4-byte big-endian length prefix on every frame.
	args := sshArgs("gpu01", SSHOptions{})
	hasT := false
	for _, a := range args {
		if a == "-T" {
			hasT = true
		}
	}
	if !hasT {
		t.Errorf("missing -T in %v", args)
	}
}

func TestSSHArgs_BatchMode(t *testing.T) {
	tests := []struct {
		name  string
		batch bool
		want  bool
	}{
		{"first dial is interactive so host-key and passphrase prompts work", false, false},
		{"reconnect is batch because no terminal is available", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			joined := strings.Join(sshArgs("gpu01", SSHOptions{Batch: tt.batch}), " ")
			got := strings.Contains(joined, "BatchMode=yes")
			if got != tt.want {
				t.Errorf("BatchMode present = %v, want %v (args: %q)", got, tt.want, joined)
			}
		})
	}
}

func TestSSHArgs_DefaultRemoteCommand(t *testing.T) {
	args := sshArgs("gpu01", SSHOptions{})
	if got := args[len(args)-1]; got != DefaultRemoteCommand {
		t.Errorf("remote command = %q, want %q", got, DefaultRemoteCommand)
	}
}

func TestSSHArgs_CustomRemoteCommand(t *testing.T) {
	// A non-interactive login shell often lacks ~/.local/bin, so an absolute
	// path must be usable.
	args := sshArgs("gpu01", SSHOptions{RemoteCommand: "/opt/bin/quil --stdio"})
	if got := args[len(args)-1]; got != "/opt/bin/quil --stdio" {
		t.Errorf("remote command = %q, want the override", got)
	}
}

func TestSSHArgs_KeepsOptionOrderStable(t *testing.T) {
	// Regression guard: the args builder is pure, so its output must be
	// deterministic for a given input.
	a := sshArgs("gpu01", SSHOptions{Batch: true})
	b := sshArgs("gpu01", SSHOptions{Batch: true})
	if !reflect.DeepEqual(a, b) {
		t.Errorf("sshArgs is not deterministic:\n%v\n%v", a, b)
	}
}

func TestSSH_MissingBinary_ReturnsError(t *testing.T) {
	dial := SSH("gpu01", SSHOptions{SSHPath: "definitely-not-a-real-ssh-binary"})
	if _, err := dial(context.Background()); err == nil {
		t.Fatal("dial with a bogus ssh path succeeded, want error")
	}
}

// A dialed connection must outlive the context that dialed it.
//
// exec.CommandContext binds the child's lifetime to ctx, so the ordinary
// reconnect shape — ctx, cancel := context.WithTimeout(...); defer cancel() —
// would kill each session at the moment the dial handed it back.
//
// The assertion is on the reaped channel, NOT on ExitCode: noExitCode is -1 and
// os/exec also reports -1 for a signalled process, so an exit-code comparison
// passes whether or not the cancel killed the child. reaped closes only when a
// status has actually been recorded.
func TestSSH_ConnSurvivesDialContextCancel(t *testing.T) {
	orig := startCommand
	t.Cleanup(func() { startCommand = orig })
	used := false
	startCommand = func(_ string, _ ...string) *exec.Cmd {
		used = true
		c := exec.Command(os.Args[0], "-test.run=TestHelperSleep")
		c.Env = append(os.Environ(), "QUIL_HELPER_SLEEP=1")
		return c
	}

	ctx, cancel := context.WithCancel(context.Background())
	conn, err := SSH("helper", SSHOptions{SSHPath: os.Args[0]})(ctx)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Without this the test is bypassable: reverting to exec.CommandContext
	// skips the seam entirely, the fake never runs, and any failure that
	// follows is incidental rather than a report about ctx ownership.
	if !used {
		t.Fatal("SSH did not build its child through startCommand; the ctx-ownership " +
			"assertion below is not exercising the production path")
	}

	sc, ok := conn.(*stdioConn)
	if !ok {
		t.Fatalf("conn is %T, want *stdioConn", conn)
	}

	cancel()
	time.Sleep(300 * time.Millisecond) // let a cancel-kill land, if one is coming

	select {
	case <-sc.reaped:
		t.Fatal("the ssh child was reaped after the dial context was cancelled; " +
			"a connection must own its own lifetime, or every reconnect attempt " +
			"kills the session it just established")
	default:
	}
}

// TestHelperSleep is a child process, not a test. It stays alive until killed.
func TestHelperSleep(t *testing.T) {
	if os.Getenv("QUIL_HELPER_SLEEP") == "" {
		t.Skip("helper process")
	}
	time.Sleep(30 * time.Second)
}

// An already-cancelled context must not leave a live child behind. ctx no longer
// owns the process, so refusing to spawn is the only place it can still apply.
func TestSSH_RefusesAlreadyCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	conn, err := SSH("gpu01", SSHOptions{})(ctx)
	if err == nil {
		conn.Close()
		t.Fatal("dial with a cancelled context succeeded, want error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want it to wrap context.Canceled", err)
	}
}

// TestSSHArgs_BoundsTheConnectHandshake pins that the TCP handshake is always
// bounded. Without it ssh inherits the OS connect timeout, which for a silently
// dropped SYN is minutes — and the dial runs before the TUI starts, so there is
// no UI to interrupt and no deadline on the call site.
func TestSSHArgs_BoundsTheConnectHandshake(t *testing.T) {
	args := sshArgs("gpu01", SSHOptions{})
	found := false
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-o" && strings.HasPrefix(args[i+1], "ConnectTimeout=") {
			found = true
			if args[i+1] == "ConnectTimeout=0" {
				t.Errorf("ConnectTimeout=0 means NO timeout to OpenSSH, got %q", args[i+1])
			}
		}
	}
	if !found {
		t.Errorf("no (\"-o\", \"ConnectTimeout=...\") pair in %v", args)
	}
}

func TestConnectTimeoutSecs(t *testing.T) {
	tests := []struct {
		name string
		in   time.Duration
		want int
	}{
		{"zero uses the default", 0, int(defaultConnectTimeout / time.Second)},
		{"negative uses the default", -5 * time.Second, int(defaultConnectTimeout / time.Second)},
		{"whole seconds pass through", 30 * time.Second, 30},
		{"truncates to whole seconds", 2500 * time.Millisecond, 2},
		// A sub-second request must never become 0 — OpenSSH reads
		// ConnectTimeout=0 as "no timeout", inverting the caller's intent.
		{"sub-second rounds up to one", 400 * time.Millisecond, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := connectTimeoutSecs(tt.in); got != tt.want {
				t.Errorf("connectTimeoutSecs(%v) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

// TestSSHArgs_ConnectTimeoutHonoursTheCaller guards the seam Phase 2's
// reconnect path needs: a shorter bound for an unattended retry.
func TestSSHArgs_ConnectTimeoutHonoursTheCaller(t *testing.T) {
	args := sshArgs("gpu01", SSHOptions{ConnectTimeout: 3 * time.Second})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "ConnectTimeout=3") {
		t.Errorf("caller's ConnectTimeout not honoured in %q", joined)
	}
}

// TestLockedBuffer_Write_KeepsOnlyTheTailOnceCapped pins the bound on a buffer
// a remote host can fill.
//
// ssh multiplexes the REMOTE command's fd 2 onto its own stderr, and a
// successful batch reconnect holds its conn for the whole session — so without
// a cap this grows without limit under a writer the far side controls.
func TestLockedBuffer_Write_KeepsOnlyTheTailOnceCapped(t *testing.T) {
	var b lockedBuffer
	// Three full caps of distinguishable content. LinkErr wants the most recent
	// diagnostic, so the TAIL is what must survive.
	for i := 0; i < 3; i++ {
		chunk := []byte(strings.Repeat(string(rune('a'+i)), stderrBufCap))
		n, err := b.Write(chunk)
		if err != nil {
			t.Fatalf("Write: %v", err)
		}
		if n != len(chunk) {
			t.Fatalf("Write returned %d, want %d — a short count makes io.Copy retry forever", n, len(chunk))
		}
	}

	got := b.String()
	// 2× is the real bound, not 1×: the trim fires at twice the cap so the
	// per-write cost amortizes. Asserting the tighter bound would pass here by
	// luck of this write pattern and fail on another.
	if len(got) > 2*stderrBufCap {
		t.Errorf("buffer grew to %d bytes, want at most %d", len(got), 2*stderrBufCap)
	}
	if strings.Contains(got, "a") {
		t.Error("oldest content survived; the trim must drop from the front")
	}
	if !strings.Contains(got, "c") {
		t.Error("newest content was dropped; the trim must keep the tail")
	}
}

// TestLockedBuffer_Write_StaysBoundedUnderManySmallWrites is the shape a remote
// actually produces, and the one the amortized trim is tuned for. A trim that
// never fired would grow without limit here.
func TestLockedBuffer_Write_StaysBoundedUnderManySmallWrites(t *testing.T) {
	var b lockedBuffer
	chunk := []byte("x")
	for i := 0; i < 5*stderrBufCap; i++ {
		if _, err := b.Write(chunk); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if got := b.Len(); got > 2*stderrBufCap {
		t.Errorf("buffer grew to %d bytes under %d one-byte writes, want at most %d",
			got, 5*stderrBufCap, 2*stderrBufCap)
	}
}

// TestSSH_BatchStderrSink_ReceivesSanitizedOutput pins that a batch dial's
// diagnostics reach the log sink with control sequences already filtered.
//
// A re-exec helper rather than `sh -c`: this package's test binary is run
// NATIVELY on Windows, where there is no sh. Mirrors TestHelperSleep, including
// SSHPath: os.Args[0] so exec.LookPath is deterministic instead of finding a
// real ssh on PATH.
func TestSSH_BatchStderrSink_ReceivesSanitizedOutput(t *testing.T) {
	orig := startCommand
	t.Cleanup(func() { startCommand = orig })
	used := false
	startCommand = func(_ string, _ ...string) *exec.Cmd {
		used = true
		c := exec.Command(os.Args[0], "-test.run=TestHelperStderr")
		c.Env = append(os.Environ(), "QUIL_HELPER_STDERR=1")
		return c
	}

	// lockedBuffer, not bytes.Buffer: exec's copier goroutine writes this while
	// the poll below reads it, and -race would flag the unsynchronised access.
	var sink lockedBuffer
	conn, err := SSH("helper", SSHOptions{
		SSHPath:    os.Args[0],
		Batch:      true,
		StderrSink: &sink,
	})(context.Background())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if !used {
		t.Fatal("SSH did not build its child through startCommand; the assertion below is not exercising the production path")
	}

	// Poll rather than sleep: the copier goroutine is asynchronous, so a fixed
	// sleep is either flaky or slow.
	var got string
	for i := 0; i < 150; i++ {
		if got = sink.String(); strings.Contains(got, "warned") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if !strings.Contains(got, "warned") {
		t.Fatalf("plain diagnostic text never reached the sink: %q", got)
	}
	if strings.Contains(got, "\x1b") {
		t.Errorf("an escape byte reached the sink unsanitized: %q", got)
	}
}

// TestHelperStderr is a child process, not a test. It writes one
// hostile-looking diagnostic to stderr and exits.
func TestHelperStderr(t *testing.T) {
	if os.Getenv("QUIL_HELPER_STDERR") == "" {
		t.Skip("helper process")
	}
	// OSC 52 is a clipboard write — the concrete capability a compromised remote
	// gains if this stream reaches a renderer unfiltered.
	fmt.Fprint(os.Stderr, "\x1b]52;c;cGF5bG9hZA==\x07warned\n")
}
