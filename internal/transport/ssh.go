package transport

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sync"
)

// DefaultRemoteCommand is what ssh runs on the far side. It is `quil`, not
// `quild`: the daemon-ensure logic (startDaemon, waitForDaemonReady,
// findDaemonBinary) lives in the TUI binary, and release archives ship both
// binaries together so quil is present wherever quild is.
const DefaultRemoteCommand = "quil --stdio"

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

// keepaliveSSHOptions make ssh itself notice a dead link and exit, which EOFs
// our pipes. There is no application-layer heartbeat: ipc.MsgHeartbeat is
// declared but never sent anywhere, so this is the only liveness check.
var keepaliveSSHOptions = []string{
	"ServerAliveInterval=15",
	"ServerAliveCountMax=3",
}

// SSHOptions tunes the ssh invocation.
type SSHOptions struct {
	// SSHPath overrides the ssh binary. Empty means "ssh" from PATH.
	SSHPath string

	// RemoteCommand overrides what runs on the far side. Empty means
	// DefaultRemoteCommand. A non-interactive login shell often lacks
	// ~/.local/bin, which is where scripts/install.sh puts quil, so an
	// absolute path is sometimes required.
	RemoteCommand string

	// Batch suppresses every interactive prompt. False for the first dial,
	// which happens before the TUI takes the terminal and must be able to
	// prompt for a host-key fingerprint or a key passphrase. True for
	// reconnects, which happen under raw mode where a prompt would garble or
	// deadlock the display — on Windows especially, since ssh reads CONIN$
	// directly rather than stdin.
	Batch bool
}

// sshArgs builds the ssh argument vector. Pure, so it is unit-testable without
// spawning anything.
func sshArgs(dest string, opts SSHOptions) []string {
	args := []string{"-T"}
	for _, o := range forcedSSHOptions {
		args = append(args, "-o", o)
	}
	for _, o := range keepaliveSSHOptions {
		args = append(args, "-o", o)
	}
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
			childIn.Close()
			parentWrite.Close()
			return nil, fmt.Errorf("create stdout pipe: %w", err)
		}

		cmd := exec.CommandContext(ctx, resolved, sshArgs(dest, opts)...)
		cmd.Stdin = childIn
		cmd.Stdout = childOut

		// ssh's own diagnostics ("Permission denied (publickey)", "Host key
		// verification failed") are better than anything we would write, so
		// keep them. On the first dial stderr goes to the terminal so prompts
		// are visible; on reconnect it is captured for the error message.
		var errBuf *lockedBuffer
		if opts.Batch {
			errBuf = &lockedBuffer{}
			cmd.Stderr = errBuf
		} else {
			cmd.Stderr = os.Stderr
		}

		if err := cmd.Start(); err != nil {
			childIn.Close()
			childOut.Close()
			parentRead.Close()
			parentWrite.Close()
			return nil, fmt.Errorf("start ssh to %s: %w", dest, err)
		}
		// The child holds its own descriptors now; drop the parent's copies or
		// EOF will never propagate.
		childIn.Close()
		childOut.Close()

		conn := newStdioConn(cmd, parentRead, parentWrite, dest)
		if errBuf != nil {
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
