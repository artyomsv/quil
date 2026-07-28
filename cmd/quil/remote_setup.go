package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
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

// remoteInstallAttempted records that this process already ran an install.
//
// It is the loop guard. A binary that will not execute makes the remote shell
// report 127 — the same status as "not installed" — so without it a launch
// would install, retry, see 127 again, and offer to install forever.
var remoteInstallAttempted bool

// offerRemoteInstallFn is offerRemoteInstall, swappable for tests of the
// version gate.
var offerRemoteInstallFn = offerRemoteInstall

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

	src, err := resolveSource(ctx, opts, probe.Platform)
	if err != nil {
		return err
	}

	upgrade := probe.ExistingPath != ""
	if !opts.Yes && !confirmRemoteInstall(dest, probe, target, src, upgrade) {
		return errors.New("aborted")
	}

	// Stop the existing daemon before replacing its binary. Ordered before the
	// push so the new quild is not left racing an old daemon that still owns
	// every pane PTY.
	if upgrade {
		fmt.Fprintf(os.Stderr, "Stopping the remote daemon…\n")
		if err := remoteinstall.StopRemoteDaemon(ctx, runner, probe.ExistingPath); err != nil {
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

	reportInstalled(dest, target, src)
	return nil
}

// resolveSource picks what to install.
func resolveSource(ctx context.Context, opts setupOptions, p remoteinstall.Platform) (remoteinstall.Source, error) {
	if opts.FromDir != "" {
		fmt.Fprintf(os.Stderr, "Packing binaries from %s…\n", opts.FromDir)
		return remoteinstall.PackDir(opts.FromDir, p)
	}

	version := opts.Version
	if version == "" {
		// A dev build has no matching release, and installing "latest" would
		// produce a remote daemon this TUI then refuses to attach to — turning
		// a missing binary into a version mismatch. Refuse and name both ways
		// out rather than guess.
		if !isReleaseFn() {
			return remoteinstall.Source{}, fmt.Errorf(
				"this is a development build (%s), which has no matching release to install.\n"+
					"  Use --from-dir <path> to push locally built binaries for %s,\n"+
					"  or --version <x.y.z> to install a published release",
				versionpkg.Current(), p)
		}
		version = versionpkg.Current()
	}

	fmt.Fprintf(os.Stderr, "Downloading quil %s for %s…\n", version, p)
	return remoteinstall.FetchRelease(ctx, version, p)
}

// confirmRemoteInstall prints what will happen and reads a y/N answer.
//
// Installing software on another machine is not something to do as a side
// effect, so the prompt names the host, the exact path, the version, and — for
// an upgrade — that the daemon stops and in-flight commands die with it.
func confirmRemoteInstall(dest string, probe remoteinstall.Probe, target remoteinstall.Target, src remoteinstall.Source, upgrade bool) bool {
	version := src.Version
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
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	cfg.SetRemoteBinary(dest, binary)
	if err := config.Save(path, cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	return nil
}

func reportInstalled(dest string, target remoteinstall.Target, src remoteinstall.Source) {
	fmt.Fprintf(os.Stderr, "\n  Installed %s on %s.\n\n", displayVersion(src), dest)
	fmt.Fprintf(os.Stderr, "    %s\n", target.BinaryPath())
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

	// The loop guard. Reaching here a second time means the binaries we just
	// installed do not execute on that host — almost always an architecture
	// the probe read one way and the loader another (a 64-bit-kernel Raspberry
	// Pi OS reports aarch64 while its userland is armhf).
	if remoteInstallAttempted {
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

	switch remedy {
	case remoteinstall.RemedyInstall:
		fmt.Fprintf(os.Stderr, "\n  Quil is not installed on %s.\n", dest)
	case remoteinstall.RemedyReinstall:
		fmt.Fprintf(os.Stderr, "\n  Quil is installed on %s but will not execute"+
			" (wrong architecture).\n", dest)
	}

	remoteInstallAttempted = true
	if err := runRemoteSetup(dest, setupOptions{}); err != nil {
		if err.Error() == "aborted" {
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

	var (
		opts setupOptions
		dest string
	)
	rest := args[1:]
	for i := 0; i < len(rest); i++ {
		switch arg := rest[i]; {
		case arg == "--yes" || arg == "-y":
			opts.Yes = true
		case arg == "--from-dir":
			if i+1 >= len(rest) {
				fmt.Fprintln(os.Stderr, "quil remote setup: --from-dir requires a path")
				os.Exit(1)
			}
			i++
			opts.FromDir = rest[i]
		case strings.HasPrefix(arg, "--from-dir="):
			opts.FromDir = strings.TrimPrefix(arg, "--from-dir=")
		case arg == "--version":
			if i+1 >= len(rest) {
				fmt.Fprintln(os.Stderr, "quil remote setup: --version requires a version")
				os.Exit(1)
			}
			i++
			opts.Version = rest[i]
		case strings.HasPrefix(arg, "--version="):
			opts.Version = strings.TrimPrefix(arg, "--version=")
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(os.Stderr, "quil remote setup: unknown flag %q\n", arg)
			os.Exit(1)
		default:
			if dest != "" {
				fmt.Fprintf(os.Stderr, "quil remote setup: unexpected argument %q\n", arg)
				os.Exit(1)
			}
			dest = arg
		}
	}

	if err := runRemoteSetup(dest, opts); err != nil {
		if err.Error() == "aborted" {
			fmt.Fprintln(os.Stderr, "Aborted — nothing was written to the remote host.")
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "quil remote setup: %v\n", err)
		os.Exit(1)
	}
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
