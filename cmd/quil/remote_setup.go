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

// alreadyProvisionedFn reports whether a previous run already installed quil on
// dest — i.e. whether a binary path was recorded for it. Swappable for tests.
//
// This is the loop guard, and it has to be PERSISTENT rather than a package
// bool. A binary that will not execute makes the remote shell report 127, the
// same status as "not installed", so the loop it guards against is: install →
// exit → user re-runs → 127 again → offer again. Every iteration is a separate
// process, so process-local state would never see the second one. A recorded
// config entry survives exactly as long as the condition it describes.
var alreadyProvisionedFn = func(dest string) bool {
	cfg, err := config.Load(config.ConfigPath())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false
	}
	return cfg.RemoteBinary(dest) != ""
}

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

	// Yes skips the confirmation prompt. For scripted provisioning only.
	Yes bool
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

	fmt.Fprintf(os.Stderr, "Checking %s…\n", dest)
	probe, err := remoteinstall.RunProbe(ctx, runner)
	if err != nil {
		return err
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
		fmt.Fprintf(os.Stderr, "Stopping the remote daemon…\n")
		stopWarning, err = remoteinstall.StopRemoteDaemon(ctx, runner, probe.ExistingPath)
		if err != nil {
			return err
		}
	}

	fmt.Fprintf(os.Stderr, "Installing to %s…\n", target.Dir)
	if err := remoteinstall.Push(ctx, runner, target, src); err != nil {
		return err
	}

	if err := recordRemoteBinary(dest, target.BinaryPath()); err != nil {
		// The install succeeded; only the shortcut for next time did not.
		// Reporting it as a failure would be wrong, and silence would leave a
		// confusing "not found" on the next launch.
		fmt.Fprintf(os.Stderr,
			"\nInstalled, but could not record the path in your config: %v\n"+
				"Attaching will still work if %s is on the remote's PATH.\n", err, target.BinaryPath())
		return nil
	}

	reportInstalled(dest, target, src, stopWarning)
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
		fmt.Fprintf(os.Stderr, "Packing binaries from %s…\n", opts.FromDir)
		return remoteinstall.PackDir(opts.FromDir, p)
	}
	version, err := plannedVersion(opts, p)
	if err != nil {
		return remoteinstall.Source{}, err
	}
	fmt.Fprintf(os.Stderr, "Downloading quil %s for %s…\n", version, p)
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

// recordRemoteBinary saves the resolved absolute path so later launches skip
// the remote's PATH entirely.
func recordRemoteBinary(dest, binary string) error {
	path := config.ConfigPath()
	cfg, err := config.Load(path)
	// A missing config.toml is the NORMAL first-run state, not a failure —
	// config.Load surfaces DecodeFile's fs.ErrNotExist verbatim. Treating it as
	// one would break exactly the case this feature exists for: a fresh machine
	// provisioning its first remote, where failing to record the path leaves the
	// next launch failing identically to before the install.
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("load config: %w", err)
	}
	cfg.SetRemoteBinary(dest, binary)
	if err := config.Save(path, cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	return nil
}

func reportInstalled(dest string, target remoteinstall.Target, src remoteinstall.Source, stopWarning string) {
	fmt.Fprintf(os.Stderr, "\n  Installed %s on %s.\n\n", displayVersion(src), dest)
	fmt.Fprintf(os.Stderr, "    %s\n", target.BinaryPath())
	if stopWarning != "" {
		// The new binaries are on disk, but a daemon that refused to stop keeps
		// running the OLD ones — renaming over a running executable leaves the
		// process on its original inode. Say so, or the next attach reports a
		// version mismatch the user believes they already fixed.
		fmt.Fprintf(os.Stderr,
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
		fmt.Fprintf(os.Stderr, "\n  %s is still present and is what a bare `ssh %s quil` finds.\n",
			target.Shadowed, dest)
		fmt.Fprintf(os.Stderr, "  Quil itself uses the absolute path above, recorded in your config.\n")
	}
	fmt.Fprintf(os.Stderr, "\n  Attach with:  quil --remote %s\n\n", dest)
}

func displayVersion(src remoteinstall.Source) string {
	if src.Version == "" {
		return "locally built binaries"
	}
	return "quil " + src.Version
}

// offerRemoteInstall handles a launch that failed because the far side has no
// usable quil. Returns true when an install succeeded.
func offerRemoteInstall(dest string, remedy remoteinstall.Remedy) bool {
	if remedy == remoteinstall.RemedyNone {
		return false
	}

	// The loop guard. A recorded path plus a shell that still cannot run the
	// binary means a previous install landed and does not execute — almost
	// always an architecture the probe read one way and the loader another (a
	// 64-bit-kernel Raspberry Pi OS reports aarch64 while its userland is
	// armhf). An upgrade is exempt: quil ran over there, so the binary is fine
	// and a recorded path proves nothing about it.
	if remedy != remoteinstall.RemedyUpgrade && alreadyProvisionedFn(dest) {
		fmt.Fprintf(os.Stderr,
			"\n"+
				"  Quil was installed on %s, but will not run there.\n"+
				"\n"+
				"  The remote shell reports the binary as missing or not executable\n"+
				"  even though it was just written, which almost always means it was\n"+
				"  built for a different architecture than the host actually runs.\n"+
				"\n"+
				"  Check what the host really is:\n"+
				"    ssh %s 'uname -sm; file ~/.local/bin/quil'\n"+
				"\n",
			dest, dest)
		return false
	}

	// Every line here is printed BEFORE the probe runs, so it may only say what
	// the exit code actually proves. Exit 127 means the remote SHELL could not
	// find quil — not that the host lacks it: a binary in ~/.local/bin is
	// invisible to a non-interactive shell, which is the very problem this
	// feature exists to solve. Asserting "not installed" here contradicted the
	// probe's own summary one line later ("currently installed: …") on every
	// hand-installed host.
	switch remedy {
	case remoteinstall.RemedyInstall:
		fmt.Fprintf(os.Stderr, "\n  Quil could not be started on %s.\n", dest)
	case remoteinstall.RemedyReinstall:
		fmt.Fprintf(os.Stderr, "\n  Quil is present on %s but will not execute"+
			" (wrong architecture).\n", dest)
	case remoteinstall.RemedyUpgrade:
		// Says nothing: the caller has already printed both versions, and the
		// probe is about to print the installed path.
	}

	if err := runRemoteSetup(dest, setupOptions{}); err != nil {
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
