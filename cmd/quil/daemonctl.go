package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// Escalation timeouts for stopping the daemon. Each tier is bounded so a
// wedged daemon (deadlocked IPC dispatch, stuck handler) can never park the
// CLI forever: IPC shutdown → SIGTERM (Unix) → SIGKILL.
const (
	ipcShutdownWait = 5 * time.Second
	sigtermWait     = 3 * time.Second
	sigkillWait     = 2 * time.Second
	stopPollEvery   = 100 * time.Millisecond
)

// envDescription names the data directory the daemon commands operate on,
// so users always see whether they're touching dev or production state.
// Mirrors the TUI's [dev] status-bar indicator (QUIL_HOME set → dev).
func envDescription() string {
	return fmt.Sprintf("%s (%s)", envMode(), config.QuilDir())
}

// daemonPID reads the daemon's PID file. Returns 0 when the file is
// missing or unparsable.
func daemonPID() int {
	data, err := os.ReadFile(config.PidPath())
	if err != nil {
		return 0
	}
	return parsePidData(data)
}

func parsePidData(data []byte) int {
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

// isQuildName reports whether a process command name belongs to a quil
// daemon binary (quild, quild-dev, quild-debug — optionally .exe, optionally
// a full path). Guards against PID reuse: a recycled PID from a stale PID
// file must never get signaled if it now belongs to an unrelated process.
func isQuildName(comm string) bool {
	// Normalize Windows separators so a full image path parses the same on
	// every platform (filepath.Base only splits on the host's separator).
	norm := strings.ReplaceAll(strings.TrimSpace(comm), `\`, "/")
	base := strings.ToLower(filepath.Base(norm))
	base = strings.TrimSuffix(base, ".exe")
	return base == "quild" || strings.HasPrefix(base, "quild-")
}

// stopDaemonEscalating stops the daemon for the current QUIL_HOME with
// bounded escalation. Returns wasRunning=false when nothing was listening
// and no live PID existed. A non-nil error means a daemon process may still
// be alive after all tiers.
func stopDaemonEscalating(verbose bool) (wasRunning bool, err error) {
	sockPath := config.SocketPath()
	pid := daemonPID()

	// Tier 1 — graceful IPC shutdown: daemon writes a final snapshot and
	// closes pane PTYs cleanly. This is the only graceful tier on Windows.
	if client, cerr := ipc.NewClient(sockPath); cerr == nil {
		wasRunning = true
		if msg, merr := ipc.NewMessage(ipc.MsgShutdown, nil); merr == nil {
			_ = client.Send(msg)
		}
		if verbose {
			fmt.Println("sent graceful shutdown...")
		}
		// Keep the conn open while waiting: Send only queues the frame for
		// the async sendLoop, and Close discards anything still queued —
		// closing immediately would race the flush and silently drop the
		// shutdown message (the historical flakiness of `quil daemon stop`).
		gone := waitDaemonGone(pid, sockPath, ipcShutdownWait)
		client.Close()
		if gone {
			cleanupDaemonFiles(sockPath)
			return true, nil
		}
		if verbose {
			fmt.Printf("daemon did not exit within %s (wedged?), escalating\n", ipcShutdownWait)
		}
	}

	if pid == 0 {
		if !wasRunning {
			// No socket, no PID — nothing to stop. Sweep leftovers anyway.
			cleanupDaemonFiles(sockPath)
			return false, nil
		}
		return true, fmt.Errorf("daemon is unresponsive and %s is missing — cannot escalate; find the quild process manually", config.PidPath())
	}

	alive, comm := processProbe(pid)
	if !alive {
		cleanupDaemonFiles(sockPath)
		return wasRunning, nil
	}
	if !isQuildName(comm) {
		// PID reuse: the recorded PID now belongs to something else. Never
		// signal it — just clear the stale bookkeeping.
		if verbose {
			fmt.Printf("pid %d is %q, not a quil daemon — clearing stale files\n", pid, comm)
		}
		cleanupDaemonFiles(sockPath)
		return wasRunning, nil
	}
	wasRunning = true

	// Tier 2 — SIGTERM (Unix only; no-op error on Windows). The daemon's
	// signal handler runs the same graceful Stop as IPC shutdown, but
	// bypasses a wedged IPC dispatch loop.
	if terr := signalTerm(pid); terr == nil {
		if verbose {
			fmt.Printf("sent SIGTERM to pid %d...\n", pid)
		}
		if waitProcessGone(pid, sigtermWait) {
			cleanupDaemonFiles(sockPath)
			return true, nil
		}
		if verbose {
			fmt.Printf("still alive after %s, escalating\n", sigtermWait)
		}
	}

	// Tier 3 — SIGKILL. State is safe: workspace.json is snapshotted every
	// 30s during normal operation, so a force-kill loses at most the last
	// few seconds of layout changes.
	if kerr := killProcess(pid); kerr != nil {
		return true, fmt.Errorf("force-kill pid %d: %w", pid, kerr)
	}
	if verbose {
		fmt.Printf("force-killed pid %d\n", pid)
	}
	if !waitProcessGone(pid, sigkillWait) {
		return true, fmt.Errorf("pid %d still alive after SIGKILL", pid)
	}
	cleanupDaemonFiles(sockPath)
	return true, nil
}

// waitDaemonGone polls until the daemon is confirmed dead: by PID when
// known, otherwise by the socket no longer accepting connections.
func waitDaemonGone(pid int, sockPath string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pid > 0 {
			if alive, _ := processProbe(pid); !alive {
				return true
			}
		} else {
			if client, err := ipc.NewClient(sockPath); err != nil {
				return true
			} else {
				client.Close()
			}
		}
		time.Sleep(stopPollEvery)
	}
	return false
}

func waitProcessGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if alive, _ := processProbe(pid); !alive {
			return true
		}
		time.Sleep(stopPollEvery)
	}
	return false
}

// cleanupDaemonFiles removes the socket and PID file left behind by a
// killed daemon (its own remove-on-exit defers never ran).
func cleanupDaemonFiles(sockPath string) {
	os.Remove(sockPath)
	os.Remove(config.PidPath())
}

// restartDaemonCmd stops (escalating) and starts the daemon, reporting the
// environment first so production vs dev is always explicit.
func restartDaemonCmd() {
	fmt.Printf("environment: %s\n", envDescription())
	wasRunning, err := stopDaemonEscalating(true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stop daemon: %v\n", err)
		os.Exit(1)
	}
	if wasRunning {
		fmt.Println("daemon stopped")
	} else {
		fmt.Println("daemon was not running")
	}
	pid := startDaemon(false)
	if !waitForDaemonReady(config.SocketPath(), pid) {
		fmt.Fprintln(os.Stderr, "daemon did not come up — check the daemon log (see 'quil daemon status')")
		os.Exit(1)
	}
	fmt.Println("daemon ready")
}

// waitForDaemonReady polls until a freshly-spawned daemon accepts a connection,
// up to daemonReadyTimeout. This tolerates a slow workspace restore (many eager
// panes spawn before the socket opens) that the old 2 s budget falsely reported
// as "daemon did not come up".
//
// When pid > 0, the spawned process is watched: if it dies before the socket
// opens, the wait returns false immediately instead of blocking for the full
// timeout — a genuine daemon crash is reported fast, while a healthy-but-slow
// start still succeeds. pid == 0 (daemon was already running, or spawn PID
// unknown) falls back to a plain timed poll.
func waitForDaemonReady(sockPath string, pid int) bool {
	return waitForDaemonReadyWithin(sockPath, pid, daemonReadyTimeout, daemonReadyPoll)
}

// waitForDaemonReadyWithin is waitForDaemonReady with the timing injected, so
// tests can exercise the ready / crash-abort / timeout branches without the
// 30 s production budget.
func waitForDaemonReadyWithin(sockPath string, pid int, timeout, poll time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if client, err := ipc.NewClient(sockPath); err == nil {
			client.Close()
			return true
		}
		if pid > 0 {
			// Discard comm: pid is our own just-spawned child, so identity is
			// certain by construction — only liveness matters here.
			if alive, _ := processProbe(pid); !alive {
				return false
			}
		}
		time.Sleep(poll)
	}
	return false
}
