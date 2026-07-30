package main

import (
	"bytes"
	"context"
	"io"
	"log"
	"net"

	"errors"
	"fmt"
	"github.com/artyomsv/quil/internal/ipc"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/remoteinstall"
	"github.com/artyomsv/quil/internal/transport"
	"github.com/artyomsv/quil/internal/tui"
	versionpkg "github.com/artyomsv/quil/internal/version"
)

// remoteDest is empty for a local session and holds the --remote destination
// otherwise. Written once in main() before any daemon-lifecycle function can
// run, then read-only for the life of the process.
var remoteDest string

// remoteMode reports whether this TUI is attached to a daemon on another host.
func remoteMode() bool { return remoteDest != "" }

// errRemoteMode is returned by the local daemon-lifecycle functions when the
// session is attached to a remote daemon. They all resolve
// config.SocketPath()/config.PidPath() internally, so running them here would
// act on THIS machine's daemon — which is not the one the user is looking at.
var errRemoteMode = errors.New("not available while attached to a remote daemon")

// exitFn is os.Exit, swappable so the guards can be tested. Mirrors the
// existing seam pattern (stopDaemonForUpgradeFn, spawnDaemonForUpgradeFn).
var exitFn = os.Exit

// remoteLinkErrFn reports why the ssh transport died, or nil while it is
// healthy. Installed by launchTUI after a successful remote dial and read by
// the version gate; nil in a local session and in tests that never dial.
//
// It exists because a remote dial cannot fail for network reasons: exec.Cmd
// starts the ssh BINARY successfully long before ssh resolves the host or
// authenticates, so an unreachable host produces a live net.Conn to a process
// that is about to die. Without this, the version gate sees only "no version
// came back" and blames a version mismatch for what is really DNS, a missing
// key, or a rejected host key.
var remoteLinkErrFn func() error

// remoteLinkEstablishedFn reports whether the remote transport has ever
// delivered a byte. Installed alongside remoteLinkErrFn.
var remoteLinkEstablishedFn func() bool

// remoteExitCodeFn reports the ssh child's exit status, or -1 when it has not
// exited. Installed alongside remoteLinkErrFn.
//
// Meaningful only AFTER the connection is closed, because Close is what reaps
// the child. That is the opposite of remoteLinkErrFn, which Close can silently
// clear — so the two are read on either side of the same Close call.
var remoteExitCodeFn func() int

// remoteExitCode reports how the ssh child exited, or -1 when it has not, when
// no probe is installed, or in a local session.
func remoteExitCode() int {
	if remoteExitCodeFn == nil {
		return -1
	}
	return remoteExitCodeFn()
}

// remoteStderrRedirectFn moves ssh's diagnostics off the terminal. Installed by
// dialRemote when the transport supports it; nil in a local session.
var remoteStderrRedirectFn func(io.Writer)

// redirectRemoteStderr sends further ssh diagnostics to w, or discards them when
// w is nil. Safe to call in a local session, where it does nothing.
//
// Called once the TUI owns the screen. ssh holds its stderr for the whole
// session and multiplexes the remote command's fd 2 onto it, so a late
// diagnostic would otherwise land mid-render on a terminal Bubble Tea believes
// it controls. The dial itself must NOT be redirected — host-key and passphrase
// prompts have to be visible before that point.
func redirectRemoteStderr(w io.Writer) {
	if remoteStderrRedirectFn != nil {
		remoteStderrRedirectFn(w)
	}
}

// remoteSSHOptions builds the dial options for the configured destination.
//
// When `quil remote setup` has recorded an absolute path for this host, it
// becomes the remote command. That is what makes attaching work on a host where
// quil lives in ~/.local/bin: `ssh host quil --stdio` runs a non-interactive
// shell, which on Debian and Ubuntu returns from ~/.bashrc before reaching any
// PATH line, so the directory is invisible there.
//
// With nothing recorded the transport's default (`quil --stdio`) applies, which
// works when the remote's non-interactive PATH can already see it.
func remoteSSHOptions(cfg config.Config) transport.SSHOptions {
	var opts transport.SSHOptions
	if binary := cfg.RemoteBinary(remoteDest); binary != "" {
		opts.RemoteCommand = remoteinstall.ShellSingleQuote(binary) + " --stdio"
	}
	return opts
}

// remoteLinkError reports a dead remote transport, or nil when the link is
// alive, still connecting, or unknown. Safe to call in a local session.
func remoteLinkError() error {
	if remoteLinkErrFn == nil {
		return nil
	}
	return remoteLinkErrFn()
}

// remoteLinkEstablished reports whether the far side has ever answered.
//
// Defaults to TRUE when no probe is installed, which is the safe direction: a
// missing probe means we cannot tell, and reporting "unreachable" on a link
// that is actually fine would break every session rather than mis-explain a
// broken one.
func remoteLinkEstablished() bool {
	if remoteLinkEstablishedFn == nil {
		return true
	}
	return remoteLinkEstablishedFn()
}

// reportRemoteLinkFailure explains an ssh channel that died before the daemon
// could answer. Written to stderr because it is the last thing the process does
// before exiting; the TUI never starts.
//
// The wording deliberately does not name a cause. By this point ssh has already
// printed the precise one — to the terminal on an interactive dial, or into
// linkErr on a batch dial — and guessing a second time would contradict it.
func reportRemoteLinkFailure(linkErr error) {
	// nil means the link never delivered a byte but ssh has not exited yet —
	// still resolving, still completing a TCP handshake, or waiting on an
	// unanswered port. There is no error to quote, and inventing one would
	// contradict whatever ssh prints a moment later.
	detail := "no response — the connection was still being established"
	if linkErr != nil {
		detail = linkErr.Error()
	}
	fmt.Fprintf(os.Stderr,
		"\n"+
			"  Cannot reach the Quil daemon on %s.\n"+
			"\n"+
			"    %s\n"+
			"\n"+
			"  The daemon never answered over this ssh connection, so the problem is\n"+
			"  reaching the host (hostname, network, key, or host key) rather than\n"+
			"  anything about Quil itself. Any ssh error printed above is the cause.\n"+
			"\n"+
			"  Check that this works, then try again:\n"+
			"\n"+
			"    ssh %s quil --stdio\n"+
			"\n"+
			"  It should print nothing at all and stay open until you interrupt it.\n"+
			"  \"command not found\" there means quil is not on the remote PATH.\n"+
			"\n",
		remoteDest, detail, remoteDest)
}

// parseRemoteFlag extracts --remote <dest> or --remote=<dest> from args,
// returning the destination and args with the flag removed. args includes
// argv[0]; it is returned unchanged when the flag is absent.
//
// The destination is never interpreted here — it goes to ssh verbatim so a
// ~/.ssh/config Host alias keeps its HostName, Port, User and ProxyJump.
func parseRemoteFlag(args []string) (dest string, rest []string, err error) {
	rest = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--remote":
			if i+1 >= len(args) || args[i+1] == "" {
				return "", nil, errors.New("--remote requires a destination, e.g. --remote gpu01")
			}
			dest = args[i+1]
			i++
		case strings.HasPrefix(arg, "--remote="):
			dest = strings.TrimPrefix(arg, "--remote=")
			if dest == "" {
				return "", nil, errors.New("--remote requires a destination, e.g. --remote gpu01")
			}
		default:
			rest = append(rest, arg)
		}
	}
	if err := validateRemoteDest(dest); err != nil {
		return "", nil, err
	}
	return dest, rest, nil
}

// validateRemoteDest rejects a destination that ssh would read as an option
// rather than a host. Empty (no --remote given) is fine.
//
// The destination is appended second-to-last, after our -o flags, so ssh's
// argument parser is still in option-parsing mode when it reaches it. A leading
// '-' therefore makes it an OPTION, and `-oProxyCommand=...` runs an arbitrary
// command on THIS machine before any network traffic. That was verified against
// OpenSSH 10.2p1, where a single-token remote command executes the injected
// ProxyCommand; today the attack fails only incidentally, because
// DefaultRemoteCommand contains a space and so trips ssh's hostname validation
// first. SSHOptions.RemoteCommand exists to override exactly that, so the
// incidental guard is not one to rely on.
//
// Same mitigation git adopted for CVE-2017-1000117. Rejecting outright rather
// than rewriting to "./-x" or passing "--": a destination starting with '-' is
// never a real host, so there is nothing to preserve.
func validateRemoteDest(dest string) error {
	if strings.HasPrefix(dest, "-") {
		return fmt.Errorf("invalid --remote destination %q: must not begin with '-'", dest)
	}
	return nil
}

// remoteInstallRetry is set by the version gate when a remote install has just
// succeeded, telling launchTUI to re-dial instead of exiting.
//
// A flag rather than a return value because the gate's signature already
// carries "the client to use from here on", and the retry is a different kind
// of answer: there is no client yet, and the caller — not the gate — owns
// dialling.
var remoteInstallRetry bool

// dialRemote opens an ssh-backed client to remoteDest and installs the link
// seams the version gate reads.
//
// Split out of launchTUI so it can be called twice: once for the initial
// attach, and again after an install has removed the reason the first one
// failed. Each call re-installs the seams, so the gate never reads a previous
// connection's status.
func dialRemote(cfg config.Config) (*ipc.Client, error) {
	// Batch=false so this dial can prompt for a host-key fingerprint or a key
	// passphrase — it runs before tea.NewProgram takes the terminal.
	log.Printf("remote mode: dialing %s over ssh", remoteDest)

	client, link, err := dialRemoteTransport(context.Background(), cfg, false, nil)
	if link != nil {
		remoteLinkErrFn = link.LinkErr
		remoteLinkEstablishedFn = link.Established
		remoteExitCodeFn = link.ExitCode
	}
	return client, err
}

// dialRemoteTransport performs one ssh-backed dial and returns the client
// alongside the transport's link probe.
//
// This is the single dial implementation, shared by the startup dial and the
// reconnect loop, for the same reason transport.RunSSH shares sshArgs with the
// dialer: a second call site assembling its own options is free to drop a
// forced hardening flag or the leading-'-' destination check, and nothing would
// notice until it mattered.
//
// batch=false lets ssh prompt on the terminal, which is only safe before
// tea.NewProgram takes it. Every dial after that point must pass true.
//
// ctx bounds the DIAL only. Per the ipc.DialFunc contract the returned conn
// owns the ssh child and releases it on Close, so a caller may cancel this
// context the moment the dial returns without killing the session it opened.
func dialRemoteTransport(ctx context.Context, cfg config.Config, batch bool, stderrSink io.Writer) (*ipc.Client, transport.LinkStatus, error) {
	opts := remoteSSHOptions(cfg)
	opts.Batch = batch
	// Only consulted on a batch dial. The interactive dial's stderr belongs on
	// the terminal, where host-key and passphrase prompts have to be readable,
	// and moves to the log later via RedirectStderr.
	opts.StderrSink = stderrSink

	// Keep hold of the transport so callers can tell a dead ssh channel from a
	// daemon that answered badly. The dial only fails when ssh could not be
	// STARTED (binary missing, pipe exhaustion) — every network-level failure
	// survives it and surfaces later as a failed handshake.
	var link transport.LinkStatus
	dialSSH := transport.SSH(remoteDest, opts)
	client, err := ipc.NewClientWithDialer(ctx, func(c context.Context) (net.Conn, error) {
		conn, dialErr := dialSSH(c)
		if conn != nil {
			ls, ok := conn.(transport.LinkStatus)
			if !ok {
				// Not fatal, but it silently disables the only check that tells
				// "unreachable host" from "version mismatch", so it must not
				// pass unnoticed. A compile-time assertion covers *stdioConn
				// itself; this catches a future wrapper type.
				log.Printf("remote: transport %T does not report link status — link diagnosis disabled", conn)
			}
			link = ls
			// Only the interactive dial writes this: a batch dial captures
			// stderr into its own buffer and never reaches the terminal, so
			// there is nothing to redirect, and a reconnect runs on a
			// background goroutine where writing a package global would be a
			// race against nothing but is still not worth doing.
			if !batch {
				if r, ok := conn.(transport.StderrRedirector); ok {
					remoteStderrRedirectFn = r.RedirectStderr
				}
			}
		}
		return conn, dialErr
	})
	return client, link, err
}

// sshStderrBudget caps how much ssh stderr one session may write to the log.
//
// The sink is remote-influenced and lives as long as the session, and the
// rotating writer keeps only max_files archives — so an unbounded remote could
// roll the whole archive set and evict every other record of what happened.
// Disk is never at risk; local history is.
const sshStderrBudget = 256 << 10

// sshStderrLogger turns ssh's stderr into whole, quoted log records.
//
// Writing the bytes straight at the log is not safe even though they are
// sanitized first: terminalSanitizer deliberately PRESERVES \n, which is
// correct for its original terminal use and load-bearing here. Every other path
// that puts remote text in the log goes through log.Printf and slog quotes it
// inside msg="…"; bytes appended verbatim land at column 0 instead, so a remote
// could emit a line that reads exactly like a genuine slog record — and the F1
// log viewer renders it as one. A chunk not ending in \n would also glue the
// next real record onto the remote's line.
//
// So: accumulate, split on \n, and emit each line through %q.
type sshStderrLogger struct {
	mu      sync.Mutex
	dest    string
	partial []byte
	written int
	stopped bool
}

func (s *sshStderrLogger) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return len(p), nil
	}
	s.written += len(p)
	if s.written > sshStderrBudget {
		s.stopped = true
		s.partial = nil
		log.Printf("remote: ssh stderr suppressed after %d bytes from %s", s.written, s.dest)
		return len(p), nil
	}
	s.partial = append(s.partial, p...)
	for {
		i := bytes.IndexByte(s.partial, '\n')
		if i < 0 {
			break
		}
		// %q so a newline the sanitizer preserved cannot begin a new record.
		log.Printf("remote: ssh stderr: %q", string(s.partial[:i]))
		s.partial = s.partial[i+1:]
	}
	return len(p), nil
}

// redialTimeout bounds one reconnect attempt. Longer than the transport's own
// ConnectTimeout so authentication has room after the TCP connect succeeds.
const redialTimeout = 30 * time.Second

// linkVerifyTimeout bounds the post-dial liveness probe.
//
// Sized to sit just past the transport's ConnectTimeout (15 s): against a host
// that is down, ssh spends that long trying to connect and then exits, which is
// what turns the probe's read into an error. A shorter deadline would report
// "unreachable" while ssh was still legitimately connecting over a slow link.
const linkVerifyTimeout = 20 * time.Second

// verifyRemoteLink proves the far side is a live daemon speaking the Quil
// protocol, and is what makes a reconnect attempt honest.
//
// Without it, an attempt "succeeds" the moment ssh's BINARY starts. exec.Cmd
// .Start returns long before ssh has resolved the host or authenticated, so a
// dial cannot fail for network reasons — the same property that forces the
// version gate to consult LinkStatus before blaming a version mismatch. Against
// an unreachable host that produced a reconnect loop which reported success
// every few hundred milliseconds, cleared the banner, reset every pane for a
// replay that never came, and left the panes blank; because each false success
// zeroed the reconnect state, the attempt counter never climbed past 1 and the
// backoff never engaged.
//
// Frames other than the response are discarded. The connection has not attached
// yet, so there is no state to lose: a broadcast that arrives in this window is
// superseded by the full state the attach itself triggers.
//
// The daemon's version is logged rather than compared. A mid-session upgrade of
// the remote is the only way it can differ, and there is no good recovery for
// that here — the terminal belongs to Bubble Tea, so nothing can be prompted —
// but it must at least be diagnosable from the log.
func verifyRemoteLink(client *ipc.Client, timeout time.Duration) error {
	req, err := ipc.NewMessage(ipc.MsgVersionReq, struct{}{})
	if err != nil {
		return fmt.Errorf("build version probe: %w", err)
	}
	req.ID = probeRequestID
	if err := client.Send(req); err != nil {
		return fmt.Errorf("send version probe: %w", err)
	}
	// The deadline and an explicit wall-clock bound, because the deadline alone
	// does not bound this LOOP. It only fires when a read actually blocks, and
	// neither of the two layers below here is guaranteed to block: stdioConn.Read
	// hands back leftover bytes from `held` before consulting the deadline, and
	// ipc.Conn.Receive reads through a bufio.Reader, so a frame that is already
	// buffered costs no read at all. A daemon that never answers MsgVersionReq
	// while any pane is producing output would `continue` this loop on its
	// broadcast stream forever — the attempt would never finish, and the banner
	// would sit on "Connecting" with no further attempt ever scheduled.
	until := time.Now().Add(timeout)
	if err := client.SetReadDeadline(until); err != nil {
		// Logged, not fatal. Returning here would make a transport that cannot
		// set deadlines (SetWriteDeadline's ErrNoDeadline has a precedent)
		// break every reconnect permanently, when the wall-clock check below is
		// sufficient on its own.
		log.Printf("remote: probe deadline not set (%v) — relying on the wall-clock bound", err)
	}
	defer client.SetReadDeadline(time.Time{})

	for {
		if !time.Now().Before(until) {
			return fmt.Errorf("await version response: %w", os.ErrDeadlineExceeded)
		}
		msg, err := client.Receive()
		if err != nil {
			return fmt.Errorf("await version response: %w", err)
		}
		// Matched on the ID we sent, not merely on the type: the daemon
		// responds to the requesting conn when Message.ID is set, so anything
		// carrying a different ID answers somebody else's request and is not
		// evidence that OUR probe was seen.
		if msg.Type != ipc.MsgVersionResp || msg.ID != probeRequestID {
			continue
		}
		var resp ipc.VersionRespPayload
		if err := msg.DecodePayload(&resp); err != nil {
			// The frame is the right type and ID, so the daemon answered — the
			// link is proven even if the payload is unreadable. Not fatal.
			log.Printf("remote: link verified, version payload undecodable: %v", err)
			return nil
		}
		// A mismatch can only mean the remote was upgraded mid-session, since
		// gateVersionCheck matched the versions at launch. There is no good
		// recovery from here — Bubble Tea owns the terminal, so nothing can be
		// prompted, and refusing would end a session whose panes are healthy —
		// but it must be loud enough to find afterwards, because a protocol the
		// local TUI does not fully speak fails SILENTLY: an unhandled message
		// type is dropped with no error anywhere.
		if cur := versionpkg.Current(); resp.Version != "" && resp.Version != cur {
			log.Printf("remote: WARNING link verified but daemon version %q != this TUI %q "+
				"— the remote was upgraded mid-session; restart the TUI to re-gate",
				resp.Version, cur)
		} else {
			log.Printf("remote: link verified, daemon version %q", resp.Version)
		}
		return nil
	}
}

// probeRequestID correlates the liveness probe with its response. A literal
// rather than a fresh id per attempt: only one probe is ever in flight on a
// given conn, and a stable value keeps the log greppable.
const probeRequestID = "reconnect-version-probe"

// redialRemote builds the Model's reconnect dialer.
//
// Batch is TRUE here, unlike the startup dial. By reconnect time Bubble Tea
// owns the terminal in raw mode, so ssh must not prompt for a host key or a
// passphrase: there is nowhere for the prompt to be read from, and it would
// hang the attempt until the timeout killed it. A host whose key is unknown at
// reconnect time therefore fails fast with a captured error, which is honest.
//
// The dial runs under WithTimeout with a deferred cancel. That is only safe
// because transport.SSH builds its child with exec.Command rather than
// exec.CommandContext (RD-001) — otherwise this cancel would kill the ssh child
// of every attempt at the moment it succeeded.
//
// Every reconnect's ssh diagnostics go to the log through sshStderrLogger. The
// startup dial routes them through RedirectStderr instead, but that seam is a
// no-op on a batch dial — stderr is captured into a buffer and never reaches a
// terminal — so without this a flapping link becomes undiagnosable after its
// first reconnect, which is exactly when the diagnosis is wanted.
func redialRemote(cfg config.Config) tui.RedialFunc {
	// One framer for the whole session, so its byte budget cannot be reset by
	// reconnecting.
	stderrSink := &sshStderrLogger{dest: remoteDest}
	return func(old tui.Client) (tui.Client, error) {
		// Release the dead connection's ssh child. The TUI cannot do this: its
		// Client is only Send/Receive, and this is the layer that knows better.
		if c, ok := old.(*ipc.Client); ok && c != nil {
			c.Close()
		}

		ctx, cancel := context.WithTimeout(context.Background(), redialTimeout)
		defer cancel()

		client, link, err := dialRemoteTransport(ctx, cfg, true, stderrSink)
		if err != nil {
			// No LinkErr fallback here, deliberately: transport.SSH returns a
			// nil conn on failure and `link` is only assigned when the dialer
			// saw one, so on this path it is always nil. A dial can only fail
			// at spawn level — ssh's binary missing, pipes exhausted — and err
			// already names that.
			return nil, err
		}
		if client == nil {
			// Defensive: returning a nil *ipc.Client into the interface would
			// produce a non-nil tui.Client holding a nil pointer, and the next
			// Receive would panic rather than reconnect.
			return nil, errors.New("ssh dial returned no connection")
		}

		// The dial proves only that ssh started. Confirm a daemon actually
		// answers before reporting the link restored — see verifyRemoteLink.
		if verr := verifyRemoteLink(client, linkVerifyTimeout); verr != nil {
			// LinkErr is read BEFORE Close, matching the ordering the version
			// gate documents and pins. Close unblocks the transport's pump via
			// <-done, and that return path can complete WITHOUT ever setting
			// pumpErr — so a read afterwards can come back nil and lose ssh's
			// own words, leaving the banner showing "await version response:
			// i/o timeout", which names nothing the user can act on. That
			// generic message is the exact outcome this fallback exists to
			// avoid, so reading it in the wrong order defeats the purpose.
			cause := verr
			if link != nil {
				if le := link.LinkErr(); le != nil {
					cause = le
				}
			}
			client.Close()
			// Classified HERE rather than in the dial-failure branch above.
			// That branch cannot see an authentication failure: transport.SSH
			// returns a nil conn on failure so link is always nil there, and a
			// dial only fails at spawn level, because exec.Cmd.Start succeeds
			// as soon as the ssh BINARY launches. Every auth and network
			// failure arrives here instead.
			//
			// !Established() is the gate, and it is load-bearing rather than a
			// refinement. ClassifyLinkFailure reads ssh's stderr, but ssh
			// multiplexes the REMOTE command's fd 2 onto that same stream — so
			// the text is remote-influenced, and "permission denied" is one of
			// the most common strings any Unix shell emits. Without this gate an
			// ordinary ~/.bashrc touching an unreadable path would park the
			// user's session permanently, and a compromised remote could do it
			// deliberately. Established() counts only bytes the PUMP read, and
			// the pump reads stdout; stderr never reaches it. So a link that
			// delivered a byte proves the remote command ran, which proves ssh
			// authenticated, which means an auth or host-key marker in that
			// stream cannot be ssh's own. Same override shape as
			// remoteinstall.ClassifyExit's `established` parameter.
			established := link != nil && link.Established()
			if !established && transport.ClassifyLinkFailure(cause.Error()) == transport.LinkFailurePermanent {
				// cause FIRST, sentinel second. %w works in any position since
				// Go 1.20 and errors.Is is unaffected — but the banner renders
				// this string, and leading with the sentinel would spend ~35
				// cells on boilerplate before reaching ssh's own words, on a row
				// that already fights for width.
				return nil, fmt.Errorf("%v: %w", cause, tui.ErrLinkPermanent)
			}
			return nil, cause
		}
		return client, nil
	}
}
