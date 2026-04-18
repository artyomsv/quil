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
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	versionpkg "github.com/artyomsv/quil/internal/version"
)

// reconnectRetryAttempts bounds how long we wait for the respawned
// daemon to open its socket after a graceful restart.
const reconnectRetryAttempts = 50

// restartDaemonWaitInterval paces the polling loops that watch the
// socket disappear (after MsgShutdown) and reappear (after respawn).
const restartDaemonWaitInterval = 100 * time.Millisecond

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
func gateVersionCheck(client *ipc.Client, sockPath string) *ipc.Client {
	res := versionHandshake(client)

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
		os.Exit(0)
		return nil

	default:
		// Either TUI > daemon, or the daemon timed out / returned an
		// unparseable version (treated as "pre-versioning daemon", same
		// handling: offer to restart).
		if !promptRestartDaemon(versionpkg.Current(), res.DaemonVersion, res.DaemonUnknown) {
			fmt.Fprintln(os.Stderr, "Aborted — daemon left running at the older version.")
			client.Close()
			os.Exit(0)
		}
		newClient, err := restartDaemonForUpgrade(client, sockPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Daemon restart failed: %v\n", err)
			os.Exit(1)
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
			os.Exit(1)
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

// restartDaemonForUpgrade gracefully stops the current daemon, spawns
// a fresh one from the TUI's own install directory, and returns a
// connected client to the new daemon. The old client is closed.
func restartDaemonForUpgrade(oldClient *ipc.Client, sockPath string) (*ipc.Client, error) {
	// 1. Send MsgShutdown so the old daemon exits cleanly (defers in its
	//    main() run: PID file removal, log close, snapshot flush).
	shutdown, err := ipc.NewMessage(ipc.MsgShutdown, nil)
	if err != nil {
		return nil, fmt.Errorf("build shutdown msg: %w", err)
	}
	if err := oldClient.Send(shutdown); err != nil {
		// Non-fatal — the daemon may already be crashing. Proceed to
		// the wait-for-socket-gone step either way.
		log.Printf("restart: send MsgShutdown: %v", err)
	}
	oldClient.Close()

	// 2. Wait for the socket file to go away, bounded. If the daemon
	//    hangs, remove stale socket/PID files so the respawn path
	//    doesn't refuse to claim them.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sockPath); os.IsNotExist(err) {
			break
		}
		time.Sleep(restartDaemonWaitInterval)
	}
	if _, err := os.Stat(sockPath); err == nil {
		log.Printf("restart: socket %s still present after shutdown — removing", sockPath)
		os.Remove(sockPath)
	}
	// Stale PID file removal is best-effort; the daemon's normal boot
	// path also cleans these up, so a failure here isn't blocking.
	if pidPath := config.PidPath(); pidPath != "" {
		if _, err := os.Stat(pidPath); err == nil {
			os.Remove(pidPath)
		}
	}

	// 3. Spawn a fresh daemon. Prefer the executable-adjacent binary
	//    over PATH so a stale `quild` earlier on PATH doesn't shadow
	//    the bundled one the user just upgraded to.
	binary := findDaemonBinaryForUpgrade()
	log.Printf("restart: spawning %s", binary)
	cmd := exec.Command(binary, "--background")
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = daemonSysProcAttr()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("spawn daemon %q: %w", binary, err)
	}
	if cmd.Process != nil {
		cmd.Process.Release()
	}

	// 4. Wait for the new daemon's socket and reconnect.
	var newClient *ipc.Client
	for i := 0; i < reconnectRetryAttempts; i++ {
		time.Sleep(restartDaemonWaitInterval)
		c, err := ipc.NewClient(sockPath)
		if err == nil {
			newClient = c
			break
		}
	}
	if newClient == nil {
		return nil, fmt.Errorf("daemon did not open socket %s within %s",
			sockPath, time.Duration(reconnectRetryAttempts)*restartDaemonWaitInterval)
	}
	return newClient, nil
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
