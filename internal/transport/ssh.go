package transport

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// DefaultRemoteCommand is what ssh runs on the far side. It is `quil`, not
// `quild`: the daemon-ensure logic (startDaemon, waitForDaemonReady,
// findDaemonBinary) lives in the TUI binary, and release archives ship both
// binaries together so quil is present wherever quild is.
const DefaultRemoteCommand = "quil --stdio"

// startCommand builds the ssh child process.
//
// A package var rather than a direct exec.Command call because SSH() passes
// ssh's own argument vector to the binary, so a test cannot point SSHPath at a
// helper and still control what that helper is asked to do. Mirrors the
// injectable-function-var pattern used throughout this codebase.
var startCommand = func(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}

// forcedSSHOptions are passed as -o flags ahead of the user's ssh_config.
// OpenSSH uses the FIRST obtained value for each parameter and processes
// command-line -o before any configuration file, so these win over a user's
// global settings.
//
// ClearAllForwardings=yes neutralises LocalForward, RemoteForward and
// DynamicForward in one option rather than enumerating each.
//
// Deliberately NOT forced: ProxyCommand and ProxyJump (bastion hops are a core
// requirement), IdentityFile, IdentityAgent, User, HostName, Port, Ciphers and
// KexAlgorithms (the user's crypto policy), ControlMaster, and
// StrictHostKeyChecking — accept-new is weaker than the default prompt, so the
// user's own host-key policy is left intact.
var forcedSSHOptions = []string{
	"ForwardAgent=no",
	"ForwardX11=no",
	"ForwardX11Trusted=no",
	"PermitLocalCommand=no",
	"ClearAllForwardings=yes",
	"RequestTTY=no",
}

// livenessSSHOptions bound how long the connection can be stuck, at both ends
// of its life.
//
// ServerAlive* make ssh itself notice a dead ESTABLISHED link and exit, which
// EOFs our pipes. There is no application-layer heartbeat — ipc.MsgHeartbeat is
// declared but never sent anywhere — so this is the only liveness check once
// the session is up.
//
// ConnectTimeout bounds the other end: the TCP handshake. Without it ssh
// inherits the OS connect timeout, which on a silently-dropped SYN (firewall,
// host down, wrong address) is minutes long, and the TUI has no way to cancel
// it — the dial runs before tea.NewProgram, so there is no UI to press Ctrl+C
// in and no ctx deadline on the call site. This is set rather than left to the
// user's ssh_config on purpose: OpenSSH takes the first obtained value and
// processes command-line -o first, so passing it here overrides a user's own
// ConnectTimeout. That is the deliberate trade — a bounded, diagnosable failure
// beats an unbounded hang — and SSHOptions.ConnectTimeout exists for the caller
// that needs a different bound.
var livenessSSHOptions = []string{
	"ServerAliveInterval=15",
	"ServerAliveCountMax=3",
}

// defaultConnectTimeout bounds the TCP handshake. Generous for a handshake
// (which is one round trip on any working link) while still failing fast enough
// that a wrong hostname is reported in seconds rather than minutes.
const defaultConnectTimeout = 15 * time.Second

// waitDelay bounds how long Wait may block on a stderr pipe that a surviving
// grandchild still holds open, after the child itself has been killed. Long
// enough that a healthy ssh's final diagnostics are not truncated, short enough
// that a wedged bastion helper cannot hang the TUI's exit.
const waitDelay = 5 * time.Second

// SSHOptions tunes the ssh invocation.
type SSHOptions struct {
	// SSHPath overrides the ssh binary. Empty means "ssh" from PATH.
	SSHPath string

	// RemoteCommand overrides what runs on the far side. Empty means
	// DefaultRemoteCommand. A non-interactive login shell often lacks
	// ~/.local/bin, which is where scripts/install.sh puts quil, so an
	// absolute path is sometimes required.
	RemoteCommand string

	// ConnectTimeout bounds the TCP handshake. Zero means
	// defaultConnectTimeout. Values below one second are rounded up to one:
	// ssh's ConnectTimeout is expressed in whole seconds, and a sub-second
	// value would truncate to 0, which OpenSSH reads as "no timeout" — the
	// exact opposite of what a caller passing a small value intends.
	ConnectTimeout time.Duration

	// Batch suppresses every interactive prompt. False for the first dial,
	// which happens before the TUI takes the terminal and must be able to
	// prompt for a host-key fingerprint or a key passphrase. True for
	// reconnects, which happen under raw mode where a prompt would garble or
	// deadlock the display — on Windows especially, since ssh reads CONIN$
	// directly rather than stdin.
	Batch bool
}

// connectTimeoutSecs converts a Duration to the whole seconds ssh expects,
// never returning 0 for a non-zero request — see SSHOptions.ConnectTimeout.
func connectTimeoutSecs(d time.Duration) int {
	if d <= 0 {
		d = defaultConnectTimeout
	}
	secs := int(d / time.Second)
	if secs < 1 {
		secs = 1
	}
	return secs
}

// sshArgs builds the ssh argument vector. Pure, so it is unit-testable without
// spawning anything.
func sshArgs(dest string, opts SSHOptions) []string {
	args := []string{"-T"}
	for _, o := range forcedSSHOptions {
		args = append(args, "-o", o)
	}
	for _, o := range livenessSSHOptions {
		args = append(args, "-o", o)
	}
	args = append(args, "-o", fmt.Sprintf("ConnectTimeout=%d", connectTimeoutSecs(opts.ConnectTimeout)))
	if opts.Batch {
		args = append(args, "-o", "BatchMode=yes")
	}
	cmd := opts.RemoteCommand
	if cmd == "" {
		cmd = DefaultRemoteCommand
	}
	// dest is passed VERBATIM — never parsed into user/host/port and
	// reassembled, or a ~/.ssh/config Host alias loses its HostName, Port,
	// User and ProxyJump.
	return append(args, dest, cmd)
}

// SSH returns a dialer that reaches a daemon on another host by running the
// remote command over ssh and speaking the IPC protocol across its stdio.
//
// No network port is opened on the remote host: the daemon keeps listening
// only on its local Unix socket, and the remote command proxies to it.
func SSH(dest string, opts SSHOptions) func(context.Context) (net.Conn, error) {
	return func(ctx context.Context) (net.Conn, error) {
		// dest lands after our -o flags, so ssh is still parsing options when
		// it reaches it: a leading '-' makes it an option, and
		// -oProxyCommand=... executes a local command before any network
		// traffic. cmd/quil rejects this at the flag too — duplicated because
		// this package is reachable by any caller and the failure is silent
		// local code execution, not a wrong hostname.
		if strings.HasPrefix(dest, "-") {
			return nil, fmt.Errorf("invalid ssh destination %q: must not begin with '-'", dest)
		}

		// Checked before anything else is touched. ctx no longer owns the child
		// (see the cmd construction below), so refusing to spawn is the only
		// place it can still apply — and a caller who has already given up
		// should not even pay for a PATH lookup.
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("dial %s: %w", dest, err)
		}

		sshPath := opts.SSHPath
		if sshPath == "" {
			sshPath = "ssh"
		}
		resolved, err := exec.LookPath(sshPath)
		if err != nil {
			return nil, fmt.Errorf("locate ssh binary %q: %w", sshPath, err)
		}

		// Own the pipes rather than using cmd.StdinPipe/StdoutPipe so both
		// ends are *os.File, which is what stdioConn needs.
		childIn, parentWrite, err := os.Pipe()
		if err != nil {
			return nil, fmt.Errorf("create stdin pipe: %w", err)
		}
		parentRead, childOut, err := os.Pipe()
		if err != nil {
			// Unwinding a half-built pipe pair: the returned error already says
			// what went wrong, and a Close failure on a descriptor we are
			// abandoning adds nothing a caller could act on.
			childIn.Close()
			parentWrite.Close()
			return nil, fmt.Errorf("create stdout pipe: %w", err)
		}

		// ctx bounds the DIAL, not the connection. exec.CommandContext would bind
		// the ssh child's lifetime to it, so a caller writing the ordinary
		// `ctx, cancel := context.WithTimeout(...)` with a deferred cancel would
		// kill the session at the moment the dial returned it. The conn owns the
		// child and releases it in Close (kill + reap).
		//
		// run.go's RunSSH deliberately keeps CommandContext: a one-shot remote
		// command IS a bounded operation, so there the two lifetimes coincide.
		cmd := startCommand(resolved, sshArgs(dest, opts)...)
		cmd.Stdin = childIn
		cmd.Stdout = childOut
		// Bound Wait. cmd.Stderr below is NOT an *os.File, so exec creates a
		// pipe and a copier goroutine for it, and Wait blocks until every
		// process holding that pipe's write end has exited. Killing ssh does not
		// cover a ProxyCommand or ProxyJump helper, which inherits stderr and
		// outlives its parent — and those are explicitly supported here (bastion
		// hops are a core requirement, which is why ProxyCommand is not forced
		// off). Without this, closing a bastion-routed connection would park the
		// TUI's exit path for as long as the helper lived.
		cmd.WaitDelay = waitDelay

		// ssh's own diagnostics ("Permission denied (publickey)", "Host key
		// verification failed") are better than anything we would write, so
		// keep them. On the first dial stderr goes to the terminal so prompts
		// are visible; on reconnect it is captured for the error message.
		var errBuf *lockedBuffer
		if opts.Batch {
			errBuf = &lockedBuffer{}
			cmd.Stderr = errBuf
		} else {
			// Sanitized, not raw. ssh multiplexes the REMOTE command's fd 2
			// onto its own stderr, and this fd stays attached for the whole
			// session — so without the filter a compromised remote host has an
			// unfiltered escape-sequence channel to the operator's terminal
			// (OSC 52 clipboard write, UI spoofing), bypassing every protection
			// the pane path applies to its output.
			//
			// ssh's own prompts are unaffected: host-key confirmation and
			// passphrase entry go to /dev/tty directly, not through this pipe.
			// Its diagnostics ("Could not resolve hostname …") are plain text
			// and pass through unchanged.
			cmd.Stderr = &terminalSanitizer{w: os.Stderr}
		}

		if err := cmd.Start(); err != nil {
			// Same as above: all four descriptors are being abandoned, so a
			// Close error is unactionable and would mask the real Start error.
			childIn.Close()
			childOut.Close()
			parentRead.Close()
			parentWrite.Close()
			return nil, fmt.Errorf("start ssh to %s: %w", dest, err)
		}
		// The child holds its own descriptors now; drop the parent's copies or
		// EOF will never propagate. Close errors are ignored because the child
		// already has what it needs and the conn below is fully usable either
		// way — failing the dial here would discard a working connection.
		childIn.Close()
		childOut.Close()

		conn := newStdioConn(cmd, parentRead, parentWrite, dest)
		if errBuf != nil {
			// Safe post-construction write: this goroutine is the only one
			// holding conn so far (it hasn't been returned yet), and pump()
			// — already running — never touches stderr.
			conn.stderr = errBuf
		}
		return conn, nil
	}
}

// lockedBuffer is a bytes.Buffer safe for the exec package's writer goroutine
// to fill while the dial path reads it on error.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
