package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/remoteinstall"
	"github.com/artyomsv/quil/internal/transport"
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
