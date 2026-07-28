package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/remoteinstall"
	versionpkg "github.com/artyomsv/quil/internal/version"
)

// releasesURL is shown to users running an older TUI against a newer
// daemon. Kept in one place so future URL changes don't need to hunt
// through the prompt text.
const releasesURL = "https://github.com/artyomsv/quil/releases"

// gateVersionCheck runs the version handshake against the daemon the
// caller has already connected to. If versions match it returns the
// same client unchanged. On mismatch or pre-versioning daemon it either
// exits the process (client-is-older path) or orchestrates a graceful
// daemon restart and returns a new client connected to the freshly
// spawned daemon.
//
// Returns the client the caller should use from here on, or exits.
func gateVersionCheck(client *ipc.Client) *ipc.Client {
	res := versionHandshake(client)

	// Checked BEFORE the switch, not inside the mismatch arm, for two reasons.
	//
	// A dead transport invalidates every branch below it — there is no daemon
	// version to compare, no upgrade to offer, and nothing to attach to. And
	// the switch's first arm returns early for any non-release build, so a
	// check placed further down never runs on a dev binary at all: the gate
	// would hand back a client whose connection is closed and the TUI would
	// launch against it, showing a blank screen with no diagnosis. Since
	// .claude/rules/dev-environment.md mandates dev builds for work on this
	// repo, that is the path exercised most.
	//
	// Ordering note: this must also precede any client.Close() below. Close
	// unblocks pump via <-done, which can return WITHOUT ever setting pumpErr,
	// so LinkErr() would go nil and the misdiagnosis would return.
	if remoteMode() && !remoteLinkEstablished() {
		// Read BEFORE Close, per the ordering note above.
		linkErr := remoteLinkError()
		client.Close()
		// Read AFTER Close: Close kills and reaps the ssh child, which is what
		// makes its exit status final. Reading it earlier races a child that is
		// still exiting and would report "still running" for one that already
		// failed. The mirror image of LinkErr's requirement, deliberately so.
		remedy := remoteinstall.ClassifyExit(remoteExitCode(), remoteLinkEstablished())
		if offerRemoteInstallFn(remoteDest, remedy) {
			// The binaries are in place, but this process is holding a dead
			// connection and half-built state, and the config entry the install
			// just wrote is not loaded here. Relaunching is cleaner than
			// re-dialing in place, and costs one command on first setup only.
			fmt.Fprintf(os.Stderr,
				"  Run `quil --remote %s` again to attach.\n\n", remoteDest)
			exitFn(0)
			return nil
		}
		reportRemoteLinkFailure(linkErr)
		exitFn(1)
		// exitFn is a swappable var, so the compiler cannot know it does not
		// return; without this the guard would fall through into the switch.
		return nil
	}

	switch {
	case res.ClientSkipped:
		log.Printf("version gate: skipped (non-release TUI)")
		return client

	case res.Matched:
		log.Printf("version gate: match — TUI %s == daemon %s", versionpkg.Current(), res.DaemonVersion)
		return client

	case res.Cmp < 0 && !res.DaemonUnknown:
		// TUI is older than the running daemon. Blocking path: we
		// refuse to attach, print actionable instructions, exit.
		client.Close()
		promptUpgradeClient(versionpkg.Current(), res.DaemonVersion)
		exitFn(0)
		return nil

	default:
		if remoteMode() {
			// Reaching here means the link DID deliver bytes (the check above
			// the switch would have exited otherwise), so a daemon really did
			// answer and a version verdict is the right one.

			// The restart path below manages the LOCAL daemon and is guarded
			// against remote mode, so there is nothing to offer here — say what
			// is wrong and how to fix it instead of prompting for an action
			// that cannot run.
			reported := res.DaemonVersion
			if reported == "" {
				reported = "unknown"
			}
			client.Close()
			fmt.Fprintf(os.Stderr,
				"\n"+
					"  Version mismatch with the remote daemon.\n"+
					"\n"+
					"    this TUI:            %s\n"+
					"    daemon at %s: %s\n",
				versionpkg.Current(), remoteDest, reported)

			// Offer to upgrade rather than advise. The advice this replaced —
			// `ssh <host> 'quil daemon restart'` — could not work: restarting
			// the same binary reports the same version.
			if offerRemoteInstallFn(remoteDest, remoteinstall.RemedyUpgrade) {
				fmt.Fprintf(os.Stderr,
					"  Run `quil --remote %s` again to attach.\n\n", remoteDest)
				exitFn(0)
				return nil
			}
			fmt.Fprintf(os.Stderr,
				"\n"+
					"  Upgrade one of them so both run the same version, then try again.\n"+
					"  To upgrade the remote daemon:\n"+
					"    quil remote setup %s\n"+
					"\n",
				remoteDest)
			exitFn(1)
			return nil
		}
		// Either TUI > daemon, or the daemon timed out / returned an
		// unparseable version (treated as "pre-versioning daemon", same
		// handling: offer to restart).
		// A staged-update respawn already got user consent at the apply
		// prompt — asking again here would double-prompt every update.
		if !updateRestartPreapproved() && !promptRestartDaemon(versionpkg.Current(), res.DaemonVersion, res.DaemonUnknown) {
			fmt.Fprintln(os.Stderr, "Aborted — daemon left running at the older version.")
			client.Close()
			exitFn(0)
			return nil
		}
		// The stop path dials its own connection, so hand the socket back
		// before it runs rather than leaving a second client attached to a
		// daemon we are about to shut down.
		client.Close()
		newClient, err := restartDaemonForUpgrade()
		if err != nil {
			// Every later launch hits this same wall — the old daemon is
			// still there and still the wrong version — so a bare one-liner
			// would strand the user with a TUI that refuses to start and no
			// idea why. Log it too: "quil won't start" is otherwise a report
			// with nothing behind it in quil.log.
			log.Printf("version gate: daemon restart failed: %v", err)
			fmt.Fprintf(os.Stderr,
				"\n"+
					"  Daemon restart failed: %v\n"+
					"\n"+
					"  The running daemon is a different version and could not be stopped,\n"+
					"  so Quil will not start a second one beside it — two daemons would\n"+
					"  fight over the same workspace and duplicate every pane.\n"+
					"\n"+
					"  Recover with:\n"+
					"    quil daemon status    # which daemon is running, and its pid\n"+
					"    quil daemon stop      # stop it (escalates to a force kill)\n"+
					"\n"+
					"  If it still will not stop, end the quild process from your task\n"+
					"  manager, then run quil again. Panes respawn from the saved\n"+
					"  workspace; in-flight shell commands are lost.\n"+
					"\n",
				err)
			exitFn(1)
			return nil
		}
		// Verify the freshly spawned daemon is actually the expected
		// version. If PATH has an older quild shadowing the bundled
		// binary, the restart would loop; bail out with a pointer.
		verify := versionHandshake(newClient)
		if !verify.Matched && !verify.ClientSkipped {
			newClient.Close()
			fmt.Fprintf(os.Stderr,
				"Restarted daemon still reports version %q (TUI is %q).\n"+
					"Another quild binary on PATH may be shadowing the bundled one.\n"+
					"Locate the quild alongside this quil executable and ensure it's\n"+
					"the same version, or remove the stale quild from PATH.\n",
				verify.DaemonVersion, versionpkg.Current(),
			)
			exitFn(1)
			return nil
		}
		log.Printf("version gate: reconnected to daemon %s after restart", verify.DaemonVersion)
		return newClient
	}
}

// promptUpgradeClient tells the user their TUI is too old and exits.
// No confirmation — this path is blocking by design.
func promptUpgradeClient(tuiVer, daemonVer string) {
	fmt.Fprintf(os.Stderr,
		"\n"+
			"  Quil needs an update.\n"+
			"\n"+
			"    TUI version:    %s\n"+
			"    Daemon version: %s\n"+
			"\n"+
			"  Please download %s (or newer) from:\n"+
			"    %s\n"+
			"\n"+
			"  The TUI refuses to attach to a newer daemon to avoid undefined behaviour.\n"+
			"  Your workspace, panes, and notes are safe — the running daemon is untouched.\n"+
			"\n",
		tuiVer, daemonVer, daemonVer, releasesURL,
	)
}

// promptRestartDaemon asks the user whether to restart the daemon to
// match the TUI's version. Returns true only on an explicit "y" / "yes".
// An empty response (just Enter) defaults to no — mismatches should
// never be resolved accidentally.
func promptRestartDaemon(tuiVer, daemonVer string, unknown bool) bool {
	daemonLabel := daemonVer
	if unknown || daemonLabel == "" {
		daemonLabel = "unknown (pre-versioning daemon)"
	}
	fmt.Fprintf(os.Stderr,
		"\n"+
			"  Daemon restart required.\n"+
			"\n"+
			"    TUI version:    %s\n"+
			"    Daemon version: %s\n"+
			"\n"+
			"  Continue to restart the daemon to %s. This will respawn all panes;\n"+
			"  your tabs, layouts, working directories, and notes are preserved via\n"+
			"  the workspace snapshot. In-flight commands in shells will be killed.\n"+
			"\n"+
			"  Restart daemon now? [y/N] ",
		tuiVer, daemonLabel, tuiVer,
	)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		log.Printf("prompt read: %v", err)
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

// Side effects of restartDaemonForUpgrade, swappable so the abort-before-spawn
// ordering can be tested without real processes.
var (
	stopDaemonForUpgradeFn  = stopDaemonEscalating
	spawnDaemonForUpgradeFn = spawnDaemonForUpgrade
)

// restartDaemonForUpgrade stops the current daemon, spawns a fresh one from
// the TUI's own install directory, and returns a connected client to it.
//
// The stop goes through the same escalating path as `quil daemon stop`
// (IPC shutdown → SIGTERM → SIGKILL, with a PID-reuse guard) and an upgrade
// ABORTS when it fails. The earlier version sent a bare MsgShutdown, waited
// 5 s, and — if the daemon was still there — deleted its socket and PID file
// and spawned a replacement anyway. That daemon kept running: detached, still
// owning every pane PTY, and now untrackable, since the PID file the stop
// path reads had just been erased. The replacement then restored the same
// workspace snapshot into a duplicate set of panes, so each update could
// leave behind another daemon, another copy of every pane, and another
// `claude --resume` on an already-resumed session id. Refusing to spawn a
// second daemon is the only safe response to a stop that could not be
// confirmed.
// The socket is resolved here rather than accepted as a parameter: the stop
// path reads config.SocketPath()/config.PidPath() internally, so a caller
// passing a different socket would stop one daemon and then wait on another.
func restartDaemonForUpgrade() (*ipc.Client, error) {
	if remoteMode() {
		return nil, errRemoteMode
	}
	sockPath := config.SocketPath()
	// verbose=true: this runs in the foreground before tea.NewProgram takes
	// the terminal, and the escalation can spend 5+3+2 s across its tiers.
	// Silent, that reads as a hang right after the user answered the prompt;
	// the tier lines ("did not exit within 5s (wedged?), escalating") are
	// exactly what explains the wait.
	if _, err := stopDaemonForUpgradeFn(true); err != nil {
		return nil, fmt.Errorf("stop the running daemon: %w", err)
	}

	pid, err := spawnDaemonForUpgradeFn()
	if err != nil {
		return nil, err
	}

	// Wait for the new daemon's socket and reconnect. Shares the crash-aware
	// readiness wait used by every other spawn path — tolerates a slow
	// restore, aborts early if the spawned daemon dies.
	if !waitForDaemonReady(sockPath, pid) {
		return nil, fmt.Errorf("daemon did not open socket %s within %s",
			sockPath, daemonReadyTimeout)
	}
	newClient, err := ipc.NewClient(sockPath)
	if err != nil {
		return nil, fmt.Errorf("reconnect after restart: %w", err)
	}
	return newClient, nil
}

// spawnDaemonForUpgrade starts a detached daemon and returns its pid. Prefers
// the executable-adjacent binary over PATH so a stale `quild` earlier on PATH
// doesn't shadow the bundled one the user just upgraded to.
func spawnDaemonForUpgrade() (int, error) {
	binary := findDaemonBinaryForUpgrade()
	log.Printf("restart: spawning %s", binary)
	cmd := exec.Command(binary, "--background")
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = daemonSysProcAttr()
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("spawn daemon %q: %w", binary, err)
	}
	// Start succeeded, so cmd.Process is set. Returning 0 here instead would
	// silently downgrade waitForDaemonReady to a blind 30 s poll with no
	// crash detection.
	pid := cmd.Process.Pid
	cmd.Process.Release()
	return pid, nil
}

// findDaemonBinaryForUpgrade is the upgrade-path analogue of
// findDaemonBinary: it prefers the binary alongside the running TUI
// executable over any `quild` on PATH. During an upgrade, the TUI's
// adjacent daemon is almost always the matching version (shipped in
// the same release archive); an older `quild` on PATH would cause a
// post-restart mismatch loop.
func findDaemonBinaryForUpgrade() string {
	name := "quild"
	if daemonBinary != "" {
		name = daemonBinary
	}

	// Executable-adjacent first.
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidate := filepath.Join(dir, name)
		if runtime.GOOS == "windows" {
			candidate += ".exe"
		}
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}

	// PATH fallback.
	if p, err := exec.LookPath(name); err == nil {
		return p
	}

	// Last resort — let the OS try. Same behaviour as findDaemonBinary.
	return name
}
