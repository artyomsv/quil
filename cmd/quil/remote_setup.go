package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"strings"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/remoteinstall"
	"github.com/artyomsv/quil/internal/transport"
	versionpkg "github.com/artyomsv/quil/internal/version"
)

// remoteSetupTimeout bounds the whole provisioning run: two or three ssh
// round trips plus a release download. Generous, because a slow link
// downloading ~15 MB is normal, but bounded because this runs before the TUI
// exists and there is nothing to interrupt it from.
const remoteSetupTimeout = 10 * time.Minute

// isReleaseFn is version.IsRelease, swappable so the dev-build refusal can be
// tested. Mirrors the existing seam pattern (stopDaemonForUpgradeFn).
var isReleaseFn = versionpkg.IsRelease

// recordedRemoteBinaryFn reads the path a previous run recorded for dest, or ""
// when none was. Swappable for tests.
//
// It replaces an older alreadyProvisionedFn that answered a bool. The bool was
// the bug: it collapsed "a path is recorded" into "quil is installed there",
// and those diverge every time a remote binary disappears behind Quil's back.
// The decision needs the path itself, so it can be compared with what the host
// actually reports. See healRemoteRecord.
var recordedRemoteBinaryFn = func(dest string) string {
	cfg, err := config.Load(config.ConfigPath())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	return cfg.RemoteBinary(dest)
}

// probeRemoteFn asks the host what quil it actually has. Swappable so the
// reconciliation truth table is testable without ssh.
var probeRemoteFn = func(dest string) (remoteinstall.Probe, error) {
	ctx, cancel := context.WithTimeout(context.Background(), remoteProbeTimeout)
	defer cancel()
	return remoteinstall.RunProbe(ctx, sshRunner{dest: dest})
}

// recordRemoteBinaryFn and clearRemoteBinaryFn are the config writes, swappable
// so a test can assert WHICH mutation a branch chose — the difference between
// healing a stale record and destroying a good one.
var (
	recordRemoteBinaryFn = recordRemoteBinary
	clearRemoteBinaryFn  = clearRemoteBinary
)

// remoteProbeTimeout bounds the reconciliation probe.
//
// It is one ssh round trip, but the budget is not sized for the network. This
// dial is deliberately non-batch — the record exists partly for hosts reached
// with a passphrase-only key and no agent, and forcing BatchMode here would
// make the reconciliation permanently unavailable to exactly those. So ssh may
// prompt for a host-key fingerprint or a passphrase, and the deadline has to
// cover a HUMAN reading and answering, not just a round trip.
const remoteProbeTimeout = 3 * time.Minute

// offerRemoteInstallFn is offerRemoteInstall, swappable for tests of the
// version gate.
var offerRemoteInstallFn = offerRemoteInstall

// errSetupAborted reports that the user declined at the confirmation prompt.
//
// A package-level sentinel matched with errors.Is, rather than a comparison on
// the message text: the callers need to tell "user said no" (silent, exit 1)
// from a real failure (print the cause), and a string comparison breaks the
// moment anything wraps it with %w.
var errSetupAborted = errors.New("aborted")

// setupOptions are the flags of `quil remote setup`.
type setupOptions struct {
	// FromDir pushes locally built binaries instead of a release. The only
	// path available to a dev build, which has no matching release to fetch.
	FromDir string

	// Version pins a release ("" means this TUI's own, or latest for a dev
	// build with an explicit pin).
	Version string

	// Yes skips the confirmation prompt. Mandatory for any caller that is not
	// the CLI: confirmRemoteInstall reads stdin, and under the TUI that is a
	// prompt nobody can answer.
	Yes bool

	// Out receives the progress narration. nil means os.Stderr, which is what
	// the CLI wants and what every existing caller got implicitly.
	//
	// It exists because stderr is the TERMINAL, and the TUI is drawing on it.
	// Reusing this function from a dialog printed "Downloading quil…" over the
	// dialog — the CLI's two unwritten dependencies are stdout for narration
	// and stdin for confirmation, and neither appears in the signature. Yes
	// covers the second; this covers the first.
	Out io.Writer

	// probe carries a host probe the caller already ran, so the attach path
	// costs one ssh round trip rather than two. nil means "probe here", which
	// is what the `quil remote setup` CLI passes.
	//
	// A pointer rather than a value because setupOptions is compared with != in
	// the flag-parsing tests, and Probe is comparable — but "not supplied" has
	// to stay distinguishable from a zero Probe. Not because a zero Probe is a
	// real answer: RunProbe cannot produce one, since ParseProbe rejects an
	// empty HOME outright and PlatformFor must succeed. It is that a zero Probe
	// reaching PlanTarget yields Dir = ".local/bin" — a RELATIVE remote install
	// path — so the value that must never be mistaken for an answer is exactly
	// the one a `setupOptions{}` literal would supply.
	probe *remoteinstall.Probe
}

// out resolves the narration sink, defaulting to stderr so a zero
// setupOptions behaves exactly as it did before the field existed.
func (o setupOptions) out() io.Writer {
	if o.Out != nil {
		return o.Out
	}
	return os.Stderr
}

// sshRunner adapts transport.RunSSH to remoteinstall.Runner.
//
// Every ssh invocation in this file goes through it, so the hardening options
// and the destination check are shared with the dial path rather than
// reimplemented per call site.
type sshRunner struct {
	dest string
	opts transport.SSHOptions
}

func (r sshRunner) Run(ctx context.Context, command string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	return transport.RunSSH(ctx, r.dest, r.opts, command, stdin, stdout, stderr)
}

// runRemoteSetup provisions quil on dest.
//
// Sequence: probe the host, resolve a source, ask, stop any existing daemon,
// push, record the path. The probe comes first because everything after it
// depends on what the host actually is — including whether we can support it
// at all.
func runRemoteSetup(dest string, opts setupOptions) error {
	// Same rejection as parseRemoteFlag and transport.RunSSH. ssh parses a
	// leading '-' as an option, and -oProxyCommand= runs a command on THIS
	// machine. Checked here too because this is a separate entry point.
	if err := validateRemoteDest(dest); err != nil {
		return err
	}
	if dest == "" {
		return errors.New("remote setup requires a destination, e.g. quil remote setup gpu01")
	}

	ctx, cancel := context.WithTimeout(context.Background(), remoteSetupTimeout)
	defer cancel()

	// Batch=false: this runs before the TUI takes the terminal, so ssh may
	// still prompt for a host-key fingerprint or a key passphrase. stderr is
	// sanitized because it carries whatever the remote shell wrote.
	runner := sshRunner{
		dest: dest,
		opts: transport.SSHOptions{},
	}

	// Reuse the caller's probe when it has one. The attach path probes to
	// reconcile the recorded binary path (healRemoteRecord), and probing again
	// here would spend a second ssh round trip re-asking a question already
	// answered — on a launch the user is already waiting on.
	probe := remoteinstall.Probe{}
	if opts.probe != nil {
		probe = *opts.probe
	} else {
		fmt.Fprintf(opts.out(), "Checking %s…\n", dest)
		var err error
		probe, err = remoteinstall.RunProbe(ctx, runner)
		if err != nil {
			return err
		}
	}

	target := remoteinstall.PlanTarget(probe)

	// Resolve WHICH version before asking, but download it after. The version
	// is knowable from the flags and this binary alone, so pulling ~15 MB
	// before the user has agreed to anything wastes their bandwidth on a
	// question they may answer "no" to — and on a slow link it also spends the
	// deadline that the push still needs.
	planned, err := plannedVersion(opts, probe.Platform)
	if err != nil {
		return err
	}

	upgrade := probe.ExistingPath != ""
	if !opts.Yes && !confirmRemoteInstall(dest, probe, target, planned, upgrade) {
		return errSetupAborted
	}

	src, err := resolveSource(ctx, opts, probe.Platform)
	if err != nil {
		return err
	}

	// Stop the existing daemon before replacing its binary. Ordered before the
	// push so the new quild is not left racing an old daemon that still owns
	// every pane PTY.
	var stopWarning string
	if upgrade {
		fmt.Fprintf(opts.out(), "Stopping the remote daemon…\n")
		stopWarning, err = remoteinstall.StopRemoteDaemon(ctx, runner, probe.ExistingPath)
		if err != nil {
			return err
		}
	}

	fmt.Fprintf(opts.out(), "Installing to %s…\n", target.Dir)
	if err := remoteinstall.Push(ctx, runner, target, src); err != nil {
		return err
	}

	if err := recordRemoteBinaryFn(dest, target.BinaryPath()); err != nil {
		// The install succeeded; only the shortcut for next time did not.
		// Reporting it as a failure would be wrong, and silence would leave a
		// confusing "not found" on the next launch.
		fmt.Fprintf(opts.out(),
			"\nInstalled, but could not record the path in your config: %v\n"+
				"Attaching will still work if %s is on the remote's PATH.\n", err, target.BinaryPath())
		return nil
	}

	reportInstalled(opts.out(), dest, target, src, stopWarning)
	return nil
}

// plannedVersion reports what will be installed, without fetching anything, so
// the consent prompt can name it. Returns "" for a --from-dir source, whose
// contents carry no version metadata.
func plannedVersion(opts setupOptions, p remoteinstall.Platform) (string, error) {
	if opts.FromDir != "" {
		return "", nil
	}
	if opts.Version != "" {
		return opts.Version, nil
	}
	// A dev build has no matching release, and installing "latest" would
	// produce a remote daemon this TUI then refuses to attach to — turning a
	// missing binary into a version mismatch. Refuse and name both ways out
	// rather than guess.
	if !isReleaseFn() {
		return "", fmt.Errorf(
			"this is a development build (%s), which has no matching release to install.\n"+
				"  Use --from-dir <path> to push locally built binaries for %s,\n"+
				"  or --version <x.y.z> to install a published release",
			versionpkg.Current(), p)
	}
	return versionpkg.Current(), nil
}

// resolveSource produces the bytes to push, after consent has been given.
func resolveSource(ctx context.Context, opts setupOptions, p remoteinstall.Platform) (remoteinstall.Source, error) {
	if opts.FromDir != "" {
		fmt.Fprintf(opts.out(), "Packing binaries from %s…\n", opts.FromDir)
		return remoteinstall.PackDir(opts.FromDir, p)
	}
	version, err := plannedVersion(opts, p)
	if err != nil {
		return remoteinstall.Source{}, err
	}
	fmt.Fprintf(opts.out(), "Downloading quil %s for %s…\n", version, p)
	return remoteinstall.FetchRelease(ctx, version, p)
}

// confirmRemoteInstall prints what will happen and reads a y/N answer.
//
// Installing software on another machine is not something to do as a side
// effect, so the prompt names the host, the exact path, the version, and — for
// an upgrade — that the daemon stops and in-flight commands die with it.
func confirmRemoteInstall(dest string, probe remoteinstall.Probe, target remoteinstall.Target, plannedVer string, upgrade bool) bool {
	version := plannedVer
	if version == "" {
		version = "locally built binaries"
	}

	fmt.Fprintf(os.Stderr, "\n")
	if upgrade {
		fmt.Fprintf(os.Stderr, "  Upgrade Quil on %s\n\n", dest)
		fmt.Fprintf(os.Stderr, "    currently installed:  %s\n", probe.ExistingPath)
	} else {
		fmt.Fprintf(os.Stderr, "  Install Quil on %s\n\n", dest)
	}
	fmt.Fprintf(os.Stderr, "    remote platform:      %s\n", probe.Platform)
	fmt.Fprintf(os.Stderr, "    install to:           %s/{quil,quild}\n", target.Dir)
	fmt.Fprintf(os.Stderr, "    version:              %s\n", version)
	fmt.Fprintf(os.Stderr, "    source:               downloaded and checksum-verified here,\n")
	fmt.Fprintf(os.Stderr, "                          pushed over this ssh connection\n")
	if target.Shadowed != "" {
		fmt.Fprintf(os.Stderr, "\n    note: %s is not writable by you, so the new install\n", target.Shadowed)
		fmt.Fprintf(os.Stderr, "          goes to %s and shadows it.\n", target.Dir)
	}
	if upgrade {
		fmt.Fprintf(os.Stderr, "\n    The remote daemon will be stopped. Panes respawn from the saved\n")
		fmt.Fprintf(os.Stderr, "    workspace; commands running in shells there are killed.\n")
	}
	fmt.Fprintf(os.Stderr, "\n  Continue? [y/N] ")

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		log.Printf("remote setup: prompt read: %v", err)
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

// mutateConfig applies fn to the config on disk and writes it back.
//
// A missing config.toml is the NORMAL first-run state, not a failure —
// config.Load surfaces DecodeFile's fs.ErrNotExist verbatim. Treating it as one
// would break exactly the case this feature exists for: a fresh machine
// provisioning its first remote, where failing to record the path leaves the
// next launch failing identically to before the install.
//
// Shared by the record and clear paths so that tolerance is stated once. Two
// copies would be free to drift, and the clear path runs on the failure branch
// where a spurious error is least likely to be noticed.
func mutateConfig(fn func(*config.Config)) error {
	path := config.ConfigPath()
	cfg, err := config.Load(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("load config: %w", err)
	}
	fn(&cfg)
	if err := config.Save(path, cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	return nil
}

// recordRemoteBinary saves the resolved absolute path so later launches skip
// the remote's PATH entirely.
func recordRemoteBinary(dest, binary string) error {
	return mutateConfig(func(c *config.Config) { c.SetRemoteBinary(dest, binary) })
}

// clearRemoteBinary forgets dest's recorded path.
//
// Only ever called after the host probe ANSWERED and reported no quil — see
// healRemoteRecord for why a probe error must never reach here.
func clearRemoteBinary(dest string) error {
	return mutateConfig(func(c *config.Config) { c.ClearRemoteBinary(dest) })
}

func reportInstalled(out io.Writer, dest string, target remoteinstall.Target, src remoteinstall.Source, stopWarning string) {
	fmt.Fprintf(out, "\n  Installed %s on %s.\n\n", displayVersion(src), dest)
	fmt.Fprintf(out, "    %s\n", target.BinaryPath())
	if stopWarning != "" {
		// The new binaries are on disk, but a daemon that refused to stop keeps
		// running the OLD ones — renaming over a running executable leaves the
		// process on its original inode. Say so, or the next attach reports a
		// version mismatch the user believes they already fixed.
		fmt.Fprintf(out,
			"\n  Warning: the remote daemon did not confirm shutdown:\n"+
				"    %s\n"+
				"  The new binaries are installed, but a daemon still running keeps\n"+
				"  serving the old version. If attaching reports a version mismatch:\n"+
				"    ssh %s %s\n",
			stopWarning, dest,
			// Quoted, so a path with an apostrophe yields a command the user can
			// actually paste rather than one that breaks on the shell.
			remoteinstall.ShellSingleQuote(remoteinstall.DaemonStopCommand(target.BinaryPath())))
	}
	if target.Shadowed != "" {
		fmt.Fprintf(out, "\n  %s is still present and is what a bare `ssh %s quil` finds.\n",
			target.Shadowed, dest)
		fmt.Fprintf(out, "  Quil itself uses the absolute path above, recorded in your config.\n")
	}
	fmt.Fprintf(out, "\n  Attach with:  quil --remote %s\n\n", dest)
}

func displayVersion(src remoteinstall.Source) string {
	if src.Version == "" {
		return "locally built binaries"
	}
	return "quil " + src.Version
}

// healRemoteRecord reconciles the recorded binary path with what the host
// actually has, and is the loop guard.
//
// The guard used to read the CONFIG: a recorded path meant "we already
// installed here", so a second 127 had to be a binary that will not execute —
// almost always an architecture the probe read one way and the loader another
// (a 64-bit-kernel Raspberry Pi OS reports aarch64 while its userland is
// armhf). The persistence was right; the fact was not. A recorded path proves
// an install landed ONCE and says nothing about now, so every way a remote
// binary can vanish — image rebuilt, home wiped, ~/.local/bin cleaned, binary
// moved by an admin, OS reinstalled on the same hostname — produced an
// architecture theory for a host whose arch was always correct, refused to
// offer the install, and left editing config.toml by hand as the only way out.
//
// The discriminator already existed and already ran: Probe.ExistingPath is ""
// when the host has none, and the probe checks absolute paths BEFORE
// `command -v` precisely because ~/.local/bin is invisible to a
// non-interactive shell. runRemoteSetup was calling RunProbe fourteen lines
// into the function this branch decides whether to skip, so the fact was
// fetched one branch too late.
//
// Returns done=true when the caller must stop and use retry as its result;
// done=false means "carry on and offer an install". The probe is handed back so
// the install can reuse it rather than spending a second ssh round trip; it is
// nil when the probe failed, which is exactly when runRemoteSetup should run
// its own and report the real error.
func healRemoteRecord(dest string) (probed *remoteinstall.Probe, done, retry bool) {
	recorded := recordedRemoteBinaryFn(dest)

	// Announce it. runRemoteSetup prints this before its own probe, but that
	// branch is skipped whenever a probe is handed to it — which is every path
	// that reaches here. Without it the terminal is silent for up to
	// remoteProbeTimeout right after a failed launch, and ssh may be waiting
	// behind that silence for a passphrase the user cannot see it asking for.
	fmt.Fprintf(os.Stderr, "Checking %s…\n", dest)

	probe, err := probeRemoteFn(dest)
	if err != nil {
		// POSITIVE EVIDENCE ONLY. A probe that failed is not evidence the
		// binary is gone — the host may be down, the key rejected, the link
		// flapping. Clearing here would silently downgrade a working host to a
		// bare `quil` on the non-interactive PATH, which is invisible on Debian
		// and Ubuntu and is the exact failure the recorded path exists to
		// prevent. Fall through and let runRemoteSetup's own probe report the
		// real error, which is more useful than anything this branch could say.
		log.Printf("remote: probe failed while reconciling %s: %v", dest, err)
		return nil, false, false
	}

	switch {
	case probe.ExistingPath == "":
		// The host has no quil at all. Any record is known-false; keeping it
		// means the next launch runs the same missing path and fails
		// identically. Offer the install this branch used to suppress.
		if recorded != "" {
			log.Printf("remote: %s has no quil; clearing the recorded path %q", dest, recorded)
			if err := clearRemoteBinaryFn(dest); err != nil {
				// Not fatal: the install below records a fresh path anyway, and
				// refusing to install because a config write failed would be a
				// worse outcome than the stale line it leaves behind.
				fmt.Fprintf(os.Stderr, "\n  Could not clear the stale path in your config: %v\n", err)
			}
		}
		return &probe, false, false

	case probe.ExistingPath == recorded:
		// We ran exactly the path the host reports, and it still would not
		// execute. This is the genuine wrong-architecture case, and the only
		// one that must not loop: "offer forever" requires a path that exists,
		// which becomes true only after a successful write.
		//
		// That termination proof rests on a fact in ANOTHER package, so it is
		// written down here rather than left to be rediscovered. The probe's
		// search order (internal/remoteinstall/scripts/remote-probe.sh) is
		// $HOME/.local/bin/quil, then /usr/local/bin/quil, then `command -v`;
		// PlanTarget only ever installs to path.Dir(ExistingPath) or to
		// $HOME/.local/bin. Both are what the NEXT probe reports first —
		// including the shadowed case, because ~/.local/bin has priority in that
		// loop. So `recorded == probe.ExistingPath` is guaranteed after any
		// install this tool performs. Reorder that shell loop and this guard
		// breaks with every Go test still green.
		reportRemoteBinaryWontRun(dest, probe.ExistingPath)
		return &probe, true, false

	default:
		// quil is present somewhere other than where we looked — an admin moved
		// it, or the host was installed by hand and never recorded. Correct the
		// record and re-dial rather than reinstalling something already there.
		//
		// Adopt ONLY a directory the probe reports as `rw`: writable by us AND
		// not writable by group or other. This is the same gate PlanTarget
		// applies before choosing an in-place upgrade, and it has to be repeated
		// here because recording a path is what makes it the ssh remote command
		// executed on every later attach with no further prompt. Adopting a
		// shared /opt/bin or a group-writable /usr/local/bin (the Homebrew
		// default on multi-admin macOS) would let another local user on that
		// host replace the binary and run as us. The probe finds such a binary
		// by ABSOLUTE path precisely because the non-interactive PATH cannot see
		// it — so a bare `ssh host quil --stdio` would never have run it, and
		// recording it is what grants the reach.
		//
		// Declining to adopt is not a dead end: it falls through to the install
		// offer, where PlanTarget routes to ~/.local/bin and reports the
		// shadowing. That is exactly what happened before this reconciliation
		// existed, so the degradation is to the previous behaviour rather than
		// to a new failure mode.
		if !probe.ExistingDirWritable {
			log.Printf("remote: %s has quil at %q but its directory is not exclusively writable; "+
				"not adopting it, offering an install instead", dest, probe.ExistingPath)
			return &probe, false, false
		}
		log.Printf("remote: %s has quil at %q, not %q; correcting the record",
			dest, probe.ExistingPath, recorded)
		if err := recordRemoteBinaryFn(dest, probe.ExistingPath); err != nil {
			// Without the record the retry would dial the same wrong path and
			// land right back here, so this one does have to stop. The correct
			// path IS known at this point, so hand it over rather than leaving
			// the user with a bare error — reportRemoteBinaryWontRun sets the
			// bar for this file.
			fmt.Fprintf(os.Stderr,
				"\n  Found quil at %s on %s, but could not record it: %v\n"+
					"\n  Fix the config write, or add this to %s:\n"+
					"    [remote.hosts.%q]\n"+
					"      binary = %q\n\n",
				probe.ExistingPath, dest, err, config.ConfigPath(), dest, probe.ExistingPath)
			return &probe, true, false
		}
		fmt.Fprintf(os.Stderr, "\n  Found quil at %s on %s. Reconnecting…\n\n", probe.ExistingPath, dest)
		return &probe, true, true
	}
}

// reportRemoteBinaryWontRun explains a binary the host has and cannot execute.
//
// It names the path the probe actually found rather than guessing at one. The
// older text asserted the binary "was just written" and pointed at
// ~/.local/bin/quil unconditionally, which sent a user whose install had simply
// been deleted to run `uname -sm` — and it came back correct, because the
// architecture was never the problem.
//
// The suggested command is ShellSingleQuote'd, following the same rule as the
// stop-daemon hint below. `path` is REMOTE-REPORTED and reaches checkRemotePath
// only for control characters and a leading slash — an apostrophe passes, and
// this message tells the user to paste the line into their own shell. Naive
// interpolation inside single quotes lets a crafted path close the quote and
// append a command that runs LOCALLY. It also simply breaks on legitimate
// input: this package's own tests use /home/o'brien/bin/quil.
func reportRemoteBinaryWontRun(dest, path string) {
	fmt.Fprintf(os.Stderr,
		"\n"+
			"  Quil is installed on %s at %s, but will not run there.\n"+
			"\n"+
			"  The remote shell finds the file and cannot execute it, which\n"+
			"  almost always means it was built for a different architecture\n"+
			"  than the host actually runs.\n"+
			"\n"+
			"  Check what the host really is:\n"+
			"    ssh %s %s\n"+
			"\n",
		dest, path, dest, remoteinstall.ShellSingleQuote("uname -sm; file "+path))
}

// offerRemoteInstall handles a launch that failed because the far side has no
// usable quil.
//
// Returns true when the reason the launch failed is GONE and the caller should
// re-dial — which is no longer the same as "an install succeeded". Reconciling
// a stale record can resolve it without installing anything, when the host had
// quil somewhere other than where we looked.
func offerRemoteInstall(dest string, remedy remoteinstall.Remedy) bool {
	if remedy == remoteinstall.RemedyNone {
		return false
	}

	// The loop guard, and it asks the HOST rather than the config.
	//
	// An upgrade is exempt: quil ran over there, so the binary is fine and no
	// probe result could change the decision — probing would spend an ssh round
	// trip to re-answer a settled question.
	var probe *remoteinstall.Probe
	if remedy != remoteinstall.RemedyUpgrade {
		p, done, retry := healRemoteRecord(dest)
		if done {
			return retry
		}
		probe = p
	}

	// These lines used to be printed BEFORE any probe, so they could only say
	// what the exit code proved — exit 127 means the remote SHELL could not find
	// quil, not that the host lacks it, since a binary in ~/.local/bin is
	// invisible to a non-interactive shell. Asserting "not installed" there
	// contradicted the probe's own summary one line later on every hand-installed
	// host.
	//
	// The probe now runs first for these remedies, so `probe` is the better
	// source and the exit code is only a fallback for RemedyUpgrade (which skips
	// reconciliation) and for a probe that failed.
	//
	// RemedyReinstall specifically must NOT be trusted on its own. It is exit
	// 126 — the shell found something and could not exec it — but the probe's
	// discovery test is `[ -x "$p" ]`, so a binary whose execute bit was removed
	// yields 126 from the shell and "" from the probe. Announcing "present but
	// will not execute" there contradicts the probe that just reported the host
	// has no quil, which is the same class of error this block was rewritten to
	// remove.
	switch {
	case probe != nil && probe.ExistingPath == "":
		fmt.Fprintf(os.Stderr, "\n  Quil is not installed on %s.\n", dest)
	case remedy == remoteinstall.RemedyReinstall:
		fmt.Fprintf(os.Stderr, "\n  Quil is present on %s but will not execute"+
			" (wrong architecture).\n", dest)
	case remedy == remoteinstall.RemedyInstall:
		fmt.Fprintf(os.Stderr, "\n  Quil could not be started on %s.\n", dest)
	case remedy == remoteinstall.RemedyUpgrade:
		// Says nothing: the caller has already printed both versions, and the
		// probe is about to print the installed path.
	}

	if err := runRemoteSetup(dest, setupOptions{probe: probe}); err != nil {
		if errors.Is(err, errSetupAborted) {
			return false
		}
		fmt.Fprintf(os.Stderr, "\n  Install failed: %v\n", err)
		return false
	}
	return true
}

// handleRemote dispatches `quil remote <subcommand>`.
func handleRemote() {
	args := os.Args[2:]
	if len(args) == 0 || args[0] != "setup" {
		printRemoteUsage()
		os.Exit(1)
	}

	// `quil --remote X remote setup Y` names two different hosts in one
	// command and cannot mean anything coherent.
	if remoteMode() {
		fmt.Fprintf(os.Stderr, "quil remote setup: cannot be combined with --remote (%s)\n", remoteDest)
		os.Exit(1)
	}

	opts, dest, showUsage, err := parseRemoteArgs(args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "quil remote setup: %v\n", err)
		os.Exit(1)
	}
	if showUsage {
		printRemoteUsage()
		return
	}

	if err := runRemoteSetup(dest, opts); err != nil {
		if errors.Is(err, errSetupAborted) {
			fmt.Fprintln(os.Stderr, "Aborted — nothing was written to the remote host.")
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "quil remote setup: %v\n", err)
		os.Exit(1)
	}
}

// parseRemoteArgs parses the flags of `quil remote setup`.
//
// Pure and returning errors rather than calling os.Exit, so the whole flag
// surface is testable — the same reason plannedVersion was split out of
// resolveSource.
func parseRemoteArgs(args []string) (opts setupOptions, dest string, showUsage bool, err error) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		// takeValue consumes the value of a flag given in either form.
		takeValue := func(flag string) (string, error) {
			if v, ok := strings.CutPrefix(arg, flag+"="); ok {
				if v == "" {
					return "", fmt.Errorf("%s requires a value", flag)
				}
				return v, nil
			}
			if i+1 >= len(args) || args[i+1] == "" {
				return "", fmt.Errorf("%s requires a value", flag)
			}
			i++
			return args[i], nil
		}

		switch {
		case arg == "--help" || arg == "-h":
			return setupOptions{}, "", true, nil
		case arg == "--yes" || arg == "-y":
			opts.Yes = true
		case arg == "--from-dir" || strings.HasPrefix(arg, "--from-dir="):
			if opts.FromDir, err = takeValue("--from-dir"); err != nil {
				return setupOptions{}, "", false, err
			}
		case arg == "--version" || strings.HasPrefix(arg, "--version="):
			if opts.Version, err = takeValue("--version"); err != nil {
				return setupOptions{}, "", false, err
			}
		case strings.HasPrefix(arg, "-"):
			return setupOptions{}, "", false, fmt.Errorf("unknown flag %q", arg)
		default:
			if dest != "" {
				return setupOptions{}, "", false, fmt.Errorf("unexpected argument %q (destination is already %q)", arg, dest)
			}
			dest = arg
		}
	}

	// --from-dir wins over --version in resolveSource, so accepting both would
	// silently ignore one of them. Say so instead.
	if opts.FromDir != "" && opts.Version != "" {
		return setupOptions{}, "", false, errors.New("--from-dir and --version are mutually exclusive")
	}
	return opts, dest, false, nil
}

func printRemoteUsage() {
	fmt.Fprint(os.Stderr, `usage: quil remote setup <destination> [flags]

Installs or upgrades Quil on another host over ssh. The archive is
downloaded and checksum-verified on THIS machine, then pushed over the
ssh connection — the remote host needs no route to GitHub.

Flags:
  --from-dir <path>   Push quil and quild from a local directory instead
                      of downloading a release. Required for dev builds.
  --version <x.y.z>   Install a specific release instead of this TUI's.
  -y, --yes           Skip the confirmation prompt.

Supported remote platforms: linux/amd64, linux/arm64, darwin/amd64,
darwin/arm64.
`)
}
