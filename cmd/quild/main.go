package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/daemon"
	"github.com/artyomsv/quil/internal/logger"
	apty "github.com/artyomsv/quil/internal/pty"
	versionpkg "github.com/artyomsv/quil/internal/version"
)

var (
	version         = "dev" // overridden at build time via -ldflags "-X main.version=..."
	buildDevMode    string  // "true" to auto-enable dev mode (set via ldflags)
	buildLogLevel   string  // overrides config log level, e.g. "debug" (set via ldflags)
	buildUpdatesOff string  // "true" to disable the self-update pipeline (set via ldflags; dev/debug builds only)
)

func main() {
	// Publish this binary's version to the shared version package so the
	// IPC MsgVersionReq handler can report it back to connecting clients.
	versionpkg.SetCurrent(version)
	// Dev/debug builds never self-update — a staged release swap would strip
	// their build-mode ldflags (see internal/version.SetUpdatesEnabled).
	versionpkg.SetUpdatesEnabled(buildUpdatesOff != "true")

	// Native Claude hook fast-path. Registered via --settings as the command
	// Claude runs per hook event (see internal/claudehook). It must NOT start
	// the daemon — it reads the hook JSON on stdin, writes the session-id file
	// / spool line under the dir the spawning daemon designated, and exits 0
	// so Claude is never blocked. Replacing the per-event PowerShell/sh script
	// with this native subcommand cuts hook latency from ~1-4 s (shell cold
	// start) to tens of ms.
	//
	// MUST run before the dev-mode env blocks below: the hook writes to
	// whatever dir the spawning daemon set via QUIL_HOOK_HOME (or legacy
	// QUIL_HOME during the upgrade window) — the dev belt would unset an
	// inherited prod QUIL_HOME and re-point the hook at the dev dir, breaking
	// session-id tracking for a dev hook binary running inside a production
	// pane. A hook invocation without pane env is a no-op anyway (RunHook
	// returns early on empty QUIL_PANE_ID), so the dev-default gate is
	// irrelevant here.
	if len(os.Args) > 1 && os.Args[1] == "claude-hook" {
		runClaudeHook()
		return
	}

	// Build-time dev mode: auto-set QUIL_HOME before everything except the
	// claude-hook fast path above (which must honor the spawning daemon's dir).
	if buildDevMode == "true" && config.IsDefaultQuilDir(os.Getenv("QUIL_HOME")) {
		// Inherited from a production pane env (pre-rename daemon) or a
		// stray export. A dev build pointed at production ~/.quil violates
		// the isolation rule — ignore it and fall through to the
		// project-local default.
		fmt.Fprintln(os.Stderr, "dev build: ignoring inherited QUIL_HOME pointing at production ~/.quil")
		os.Unsetenv("QUIL_HOME")
	}
	if buildDevMode == "true" && os.Getenv("QUIL_HOME") == "" {
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintf(os.Stderr, "dev build: cannot determine executable path: %v\n", err)
			os.Exit(1)
		}
		os.Setenv("QUIL_HOME", filepath.Join(filepath.Dir(exe), ".quil"))
	}

	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println("quild v" + version)
		return
	}

	background := len(os.Args) > 1 && os.Args[1] == "--background"

	// Load config FIRST so we know what log level to use. Errors during
	// config load go to stderr because the logger isn't set up yet.
	cfg := config.Default()
	cfgPath := config.ConfigPath()
	if _, err := os.Stat(cfgPath); err == nil {
		loaded, err := config.Load(cfgPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to load config: %v\n", err)
		} else {
			cfg = loaded
		}
	}

	logLevel := cfg.Logging.Level
	if buildLogLevel != "" {
		logLevel = buildLogLevel
	}
	closer := initLogging(logLevel, cfg.Logging.MaxSizeMB, cfg.Logging.MaxFiles)
	if closer != nil {
		defer closer.Close()
	}

	// Extract the bundled ConPTY host (Windows only; no-op elsewhere) so panes
	// spawn through the newer OpenConsole instead of the OS conhost. Non-fatal:
	// on failure the PTY layer falls back to the inbox ConPTY.
	if err := apty.PrepareBundledConPTY(config.QuilDir()); err != nil {
		log.Printf("conpty: bundled host unavailable (%v); using inbox", err)
	}

	// Single-instance guard. If a HEALTHY daemon is already serving the socket
	// this spawn is redundant — a double auto-start, or a fresh spawn racing a
	// previous daemon that is slow to exit. Exit cleanly instead of proceeding:
	// Server.Start() would os.Remove the live socket and re-listen, which
	// orphans the running daemon (it keeps its panes alive headless, holds the
	// log file open — breaking rotation — and leaks memory).
	//
	// The check is a real MsgVersionReq/Resp handshake, not a bare dial: a
	// wedged daemon or a foreign process squatting the path would accept a
	// connection but can't serve clients, and deferring to it would wrongly
	// refuse a legitimate startup. A stale/wedged/foreign socket is left for
	// Server.Start to reclaim. (Residual: two daemons that both fail the probe
	// before either listens can still race — availability-only, same-UID.)
	if daemonAlreadyHealthy(config.SocketPath()) {
		log.Printf("a healthy quild is already serving %s; exiting (no orphan spawn)", config.SocketPath())
		if !background {
			fmt.Println("quild already running")
		}
		return
	}

	d := daemon.New(cfg)
	log.Printf("quild v%s starting...", version)
	if !background {
		fmt.Println("quild — starting daemon...")
	}
	if err := d.Start(); err != nil {
		log.Printf("failed to start daemon: %v", err)
		if !background {
			fmt.Fprintf(os.Stderr, "failed to start daemon: %v\n", err)
		}
		os.Exit(1)
	}

	// Write PID file after Start() ensures ~/.quil/ exists
	writePIDFile()
	defer removePIDFile()

	log.Printf("quild ready (pid %d)", os.Getpid())
	if !background {
		fmt.Printf("quild ready (pid %d). Press Ctrl+C to stop.\n", os.Getpid())
	}
	d.Wait()
}

func initLogging(level string, maxSizeMB, maxFiles int) io.Closer {
	logDir := config.QuilDir()
	if logDir == "" {
		return nil
	}
	w, err := logger.NewRotatingWriter(logDir, "quild.log", int64(maxSizeMB)<<20, maxFiles)
	if err != nil {
		return nil
	}
	// logger.Init replaces the stdlib log output too, so existing log.Printf
	// call sites bridge through slog at info level and respect the configured
	// level. New code should call logger.Debug/Info/Warn/Error explicitly.
	logger.Init(level, w)
	return w
}

func writePIDFile() {
	dir := config.QuilDir()
	if dir == "" {
		log.Println("warning: cannot determine quil dir, skipping PID file")
		return
	}
	path := config.PidPath()
	if err := os.WriteFile(path, []byte(fmt.Sprintf("%d", os.Getpid())), 0600); err != nil {
		log.Printf("warning: failed to write PID file: %v", err)
	}
}

func removePIDFile() {
	os.Remove(config.PidPath())
}
