package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/logger"
	"github.com/artyomsv/quil/internal/plugin"
	"github.com/artyomsv/quil/internal/tui"
	versionpkg "github.com/artyomsv/quil/internal/version"
)

var (
	version         = "dev"
	buildDevMode    string // "true" to auto-enable dev mode (set via ldflags)
	buildLogLevel   string // overrides config log level, e.g. "debug" (set via ldflags)
	daemonBinary    string // daemon binary name, e.g. "quild-dev" (set via ldflags)
	buildUpdatesOff string // "true" to disable the self-update pipeline (set via ldflags; dev/debug builds only)
)

const (
	// daemonReadyTimeout bounds how long a client waits for a freshly-spawned
	// daemon to open its socket. It must comfortably exceed a heavy workspace
	// restore: the daemon spawns the active tab's panes AND every Eager-flagged
	// pane serially BEFORE it listens, so N eager AI panes cost ~N×spawn
	// latency (each claude --resume ≈ 200-300 ms). The old 2 s budget produced
	// false "daemon did not come up" failures whenever restore ran long. The
	// wait aborts early if the spawned PID dies, so a genuine crash is still
	// reported fast (see waitForDaemonReady).
	daemonReadyTimeout = 30 * time.Second
	daemonReadyPoll    = 100 * time.Millisecond
)

func main() {
	// Publish this binary's version to the shared version package so
	// subcommands (MCP bridge, handshake logic) and the TUI all read
	// from one place.
	versionpkg.SetCurrent(version)
	// Dev/debug builds never self-update — a staged release swap would strip
	// their build-mode ldflags (see internal/version.SetUpdatesEnabled).
	versionpkg.SetUpdatesEnabled(buildUpdatesOff != "true")

	// Build-time dev mode: if baked in via ldflags, auto-set QUIL_HOME
	// before anything else. The --dev flag and QUIL_HOME env var still
	// take precedence (they're checked first).
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

	// Check for --dev flag before anything else.
	// Sets QUIL_HOME to .quil/ next to the executable for isolated dev instances.
	for i, arg := range os.Args[1:] {
		if arg == "--dev" {
			exe, err := os.Executable()
			if err != nil {
				fmt.Fprintf(os.Stderr, "--dev: cannot determine executable path: %v\n", err)
				os.Exit(1)
			}
			devDir := filepath.Join(filepath.Dir(exe), ".quil")
			os.Setenv("QUIL_HOME", devDir)
			realIdx := i + 1 // i is relative to os.Args[1:]
			os.Args = append(os.Args[:realIdx], os.Args[realIdx+1:]...)
			break
		}
	}

	// Record the console mode before anything we spawn can disturb it. A
	// non-batch ssh dial reconfigures it and is killed rather than allowed to
	// restore it, which leaves every later diagnostic staircasing down the
	// screen — see consolemode_windows.go. No-op off Windows.
	saveConsoleMode()

	// --remote binds this TUI to a daemon on another host. Parsed before the
	// subcommand switch so the lifecycle guards are armed for everything below.
	if dest, rest, err := parseRemoteFlag(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	} else if dest != "" {
		remoteDest = dest
		os.Args = rest
	}

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "daemon":
			handleDaemon()
			return
		case "mcp":
			runMCP()
			return
		case "version":
			fmt.Println("quil v" + version)
			return
		case "remote":
			// `quil remote setup <dest>` — distinct from the `--remote <dest>`
			// FLAG, which parseRemoteFlag has already stripped from os.Args by
			// the time this switch runs.
			handleRemote()
			return
		case "restart":
			if remoteMode() {
				fmt.Fprintf(os.Stderr, "quil restart: not available with --remote (target: %s)\n", remoteDest)
				os.Exit(1)
			}
			// Recovery path for a hung/wedged daemon: stop with bounded
			// escalation, start fresh, then drop into the normal TUI.
			restartDaemonCmd()
			launchTUI()
			return
		case "--stdio":
			runStdio()
			return
		case "status":
			if remoteMode() {
				// runStatus resolves config.SocketPath(), daemonPID() and
				// config.PidPath() — all local. Without this it answers
				// confidently about THIS machine while naming none of it, and
				// --json emits {"running":true,...} with no field saying which
				// host replied. Refused rather than silently wrong; reading the
				// remote status over the transport is Phase 3 work.
				fmt.Fprintf(os.Stderr, "quil status: not available with --remote (target: %s)\n"+
					"Run it on the remote host instead:\n"+
					"    ssh %s quil status\n", remoteDest, remoteDest)
				os.Exit(1)
			}
			runStatus(os.Args[2:])
			return
		}
	}

	launchTUI()
}

func handleDaemon() {
	if remoteMode() {
		fmt.Fprintf(os.Stderr, "quil daemon: not available with --remote (target: %s)\n"+
			"Manage the remote daemon over ssh, or drop --remote to manage the local one.\n", remoteDest)
		os.Exit(1)
	}

	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: quil daemon [start|stop|restart|status]")
		os.Exit(1)
	}

	switch os.Args[2] {
	case "start":
		pid := startDaemon(false)
		if pid != 0 && !waitForDaemonReady(config.SocketPath(), pid) {
			fmt.Fprintln(os.Stderr, "daemon did not come up — check the daemon log (see 'quil daemon status')")
			os.Exit(1)
		}
	case "stop":
		stopDaemon()
	case "restart":
		restartDaemonCmd()
	case "status":
		runStatus(os.Args[3:])
	default:
		fmt.Fprintf(os.Stderr, "unknown daemon command: %s\n", os.Args[2])
		os.Exit(1)
	}
}

func findDaemonBinary() string {
	name := "quild"
	if daemonBinary != "" {
		name = daemonBinary
	}

	// 1. Check PATH first
	if p, err := exec.LookPath(name); err == nil {
		return p
	}

	// 2. Check alongside the running executable
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

	// 3. Fallback — let OS try
	return name
}

// startDaemon spawns the background daemon, returning its PID so callers can
// watch it for early death while waiting for the socket (see
// waitForDaemonReady). Returns 0 only when a daemon was already listening (no
// process spawned) — that invariant lets callers treat pid==0 as "socket is
// already up". Spawn failures exit the process (os.Exit) rather than return 0.
func startDaemon(quiet bool) int {
	if remoteMode() {
		// Defense in depth: launchTUI never reaches here in remote mode, but
		// startDaemon spawns against config.SocketPath() and a future caller
		// that forgets would start a daemon on the wrong machine.
		fmt.Fprintln(os.Stderr, "internal error: startDaemon called while attached to a remote daemon")
		exitFn(1)
		// Unreachable in production: exitFn is os.Exit, which never returns.
		// The explicit return exists so a test double that DOES return (a
		// perfectly reasonable double to write) cannot fall through into the
		// spawn path below and start a real daemon against
		// config.SocketPath() — the LOCAL machine's, not the one the remote
		// session is attached to. -1 is not a real PID: 0 already means "a
		// daemon was already listening, nothing spawned" (see the doc comment
		// above), and asserting that here would be false — nothing was
		// checked. Callers' pid > 0 gates (waitForDaemonReady, processProbe)
		// treat -1 the same as "no process to watch", which is the true
		// state.
		return -1
	}
	sockPath := config.SocketPath()

	// Probe existing socket — if daemon is dead, clean up stale
	if client, err := ipc.NewClient(sockPath); err == nil {
		client.Close()
		if !quiet {
			fmt.Println("daemon already running")
		}
		return 0
	} else if _, statErr := os.Stat(sockPath); statErr == nil {
		// Socket exists but daemon isn't responding → stale
		os.Remove(sockPath)
	}

	quild := findDaemonBinary()
	// Pin the daemon's CWD to its data dir so newly-spawned panes don't
	// inherit the launcher's directory (which can vanish under the daemon).
	// Pre-create the directory because exec.Cmd.Start chdir's *before* the
	// daemon's own MkdirAll runs — without this, `quil daemon start` fails
	// with "chdir: no such file or directory" on first install.
	quilDir := config.QuilDir()
	if err := os.MkdirAll(quilDir, 0700); err != nil {
		fmt.Fprintf(os.Stderr, "failed to create data dir %q: %v\n", quilDir, err)
		exitFn(1)
		return -1 // unreachable in production; see the guard above for why
	}
	cmd := exec.Command(quild, "--background")
	cmd.Dir = quilDir
	cmd.Stdout = nil
	// Capture the daemon's stderr (runtime panics, SIGQUIT goroutine dumps)
	// instead of discarding it — stderr at /dev/null cost us the post-mortem
	// for the 2026-06-11 daemon wedge. The parent's handle is closed after
	// Start; the child keeps its own dup.
	cmd.Stderr = nil
	if f, ferr := os.OpenFile(filepath.Join(quilDir, "quild.stderr.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600); ferr == nil {
		cmd.Stderr = f
		defer f.Close()
	}
	cmd.SysProcAttr = daemonSysProcAttr()
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to start daemon: %v\n", err)
		exitFn(1)
		return -1 // unreachable in production; see the guard above for why
	}

	pid := cmd.Process.Pid
	cmd.Process.Release()
	if !quiet {
		fmt.Printf("daemon started (pid %d)\n", pid)
	}
	return pid
}

func stopDaemon() {
	fmt.Printf("environment: %s\n", envDescription())
	wasRunning, err := stopDaemonEscalating(true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stop daemon: %v\n", err)
		os.Exit(1)
	}
	if !wasRunning {
		fmt.Fprintln(os.Stderr, "daemon not running")
		os.Exit(1)
	}
	fmt.Println("daemon stopped")
}

func launchTUI() {
	// Load config first so we know what log level to use.
	cfg := config.Default()
	if cfgPath := config.ConfigPath(); fileExists(cfgPath) {
		if loaded, err := config.Load(cfgPath); err == nil {
			cfg = loaded
		}
	}

	// Open the log file and route both slog and stdlib log through it at
	// the configured level. We open the file directly (not via tea.LogToFile)
	// so we can drive the slog handler ourselves; tea.LogToFile would set up
	// stdlib log with its own prefix and skip slog entirely.
	logDir := config.QuilDir()
	if logDir != "" {
		os.MkdirAll(logDir, 0700)
	}
	logLevel := cfg.Logging.Level
	if buildLogLevel != "" {
		logLevel = buildLogLevel
	}
	// Held beyond this block so ssh's stderr can be pointed at the same rotating
	// handle once the TUI owns the screen. Reopening the file separately would
	// put two writers on one rotating log, which is what breaks rotation.
	var logSink io.Writer
	if logDir != "" {
		logWriter, err := logger.NewRotatingWriter(logDir, "quil.log", int64(cfg.Logging.MaxSizeMB)<<20, cfg.Logging.MaxFiles)
		if err == nil && logWriter != nil {
			logger.Init(logLevel, logWriter)
			logSink = logWriter
			defer logWriter.Close()
		}
	}

	// Panic recovery — write to log before crashing
	defer func() {
		if r := recover(); r != nil {
			msg := fmt.Sprintf("PANIC: %v\n%s", r, debug.Stack())
			log.Print(msg)
			fmt.Fprintf(os.Stderr, "%s\n", msg)
			os.Exit(1)
		}
	}()

	// Staged auto-update: apply before touching the daemon. On success the
	// new binary was respawned and has already run the whole session — this
	// process was just a wrapper. On decline/failure, fall through to a
	// normal launch; cleanup only runs when nothing is being applied.
	if maybeApplyStagedUpdate(false) {
		return
	}
	cleanupAppliedUpdate()

	sockPath := config.SocketPath()
	log.Printf("config loaded, AutoStart=%v", cfg.Daemon.AutoStart)

	var client *ipc.Client
	var err error
	spawnedButNotReady := false

	if remoteMode() {
		// No local daemon is involved: `quil --stdio` on the far side ensures
		// the remote one. Batch=false so this first dial can prompt for a
		// host-key fingerprint or key passphrase — it runs before tea.NewProgram
		// takes the terminal.
		client, err = dialRemote(cfg)
		if err != nil {
			log.Printf("cannot connect to remote daemon %s: %v", remoteDest, err)
			fmt.Fprintf(os.Stderr, "cannot connect to %s: %v\n"+
				"Check that you can run: ssh %s quil --stdio\n", remoteDest, err, remoteDest)
			os.Exit(1)
		}
	} else {
		// Try connecting; auto-start if needed
		client, err = ipc.NewClient(sockPath)
		if err != nil && cfg.Daemon.AutoStart {
			log.Printf("daemon not reachable, auto-starting...")
			pid := startDaemon(true) // quiet — no stdout during TUI launch
			if waitForDaemonReady(sockPath, pid) {
				client, err = ipc.NewClient(sockPath)
			} else {
				spawnedButNotReady = true
			}
		}
	}
	if err != nil {
		if spawnedButNotReady {
			// We DID spawn a daemon; it just never opened its socket (crashed
			// during restore, or exceeded the readiness budget). Point the user
			// at the daemon log rather than "run daemon start" — they can't fix
			// this by starting it again.
			log.Printf("auto-started daemon did not come up within %s", daemonReadyTimeout)
			fmt.Fprintf(os.Stderr, "daemon was started but did not come up within %s — check the daemon log (see 'quil daemon status')\n", daemonReadyTimeout)
		} else {
			log.Printf("cannot connect to daemon: %v", err)
			fmt.Fprintf(os.Stderr, "cannot connect to daemon: %v\nRun 'quil daemon start' first.\n", err)
		}
		os.Exit(1)
	}
	log.Print("connected to daemon")

	// Version gate: compare TUI and daemon versions before attaching.
	// gateVersionCheck either returns the same client (match / skipped),
	// returns a NEW client connected to a freshly-spawned daemon (after
	// user-confirmed upgrade restart), or exits the process outright
	// (TUI older than daemon — blocking dialog path).
	client = gateVersionCheck(client)

	// The reason this launch failed is gone — either an install just succeeded,
	// or healRemoteRecord found quil at a path other than the one we dialed and
	// corrected the record. Re-dial rather than making the user retype the
	// command they already ran; attaching was the whole point of it.
	//
	// The config is reloaded first: it was read before either of those wrote to
	// it, so the recorded binary path is not in this process's copy, and that
	// path is what the next dial must use as the remote command.
	//
	// Bounded to one attempt by construction rather than by a counter. Both
	// paths leave a binary at the recorded path, so a far side that still
	// cannot run quil makes healRemoteRecord's probe report ExistingPath equal
	// to the record — the wrong-architecture arm, which refuses rather than
	// offering again.
	if client == nil && remoteInstallRetry {
		remoteInstallRetry = false
		if reloaded, loadErr := config.Load(config.ConfigPath()); loadErr == nil {
			cfg = reloaded
		}
		log.Printf("remote: install succeeded, re-dialing %s", remoteDest)
		fmt.Fprintf(os.Stderr, "  Attaching…\n\n")
		client, err = dialRemote(cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cannot connect to %s after installing: %v\n", remoteDest, err)
			os.Exit(1)
		}
		client = gateVersionCheck(client)
	}
	defer client.Close()

	// Ensure default plugins exist and detect stale ones needing migration
	stalePlugins, ensureErr := plugin.EnsureDefaultPlugins(config.PluginsDir())
	if ensureErr != nil {
		log.Printf("warning: ensure default plugins: %v", ensureErr)
	}

	// Initialize plugin registry for the TUI (detection runs in the TUI process
	// so the dialog knows which plugins are available)
	reg := plugin.NewRegistry()
	if err := reg.LoadFromDir(config.PluginsDir()); err != nil {
		log.Printf("warning: load plugins: %v", err)
	}
	reg.DetectAvailability()
	if len(stalePlugins) > 0 {
		log.Printf("detected %d stale plugin(s) needing migration", len(stalePlugins))
	}

	// Restore window size from previous session
	restoreWindowSize()

	model := tui.NewModel(client, cfg, version, reg, stalePlugins)
	// Drives the [remote <host>] status-bar indicator and suppresses the
	// update controls, which are wired to local disk and would target the
	// wrong machine. Empty for a local session.
	model.SetRemoteDest(remoteDest)
	if remoteDest != "" {
		model.SetRecentCWDs(tui.LoadRecentCWDs(config.RecentCWDsPath(remoteDest)))
	}

	// Only remote sessions reconnect. A local daemon that dies takes its panes
	// with it, so there is nothing to reattach to and retrying would hide the
	// loss; leaving redialFn nil is what makes that path stay fatal.
	if remoteMode() {
		model.SetRedialFunc(redialRemote(cfg))
		// The Model cannot close a connection itself — tui.Client is only
		// Send/Receive. Without this, the `defer client.Close()` above releases
		// the STARTUP client, which after a reconnect is already dead, while the
		// live ssh child is only reachable through the Model and outlives us.
		model.SetClientCloser(func(c tui.Client) {
			if ic, ok := c.(*ipc.Client); ok && ic != nil {
				ic.Close()
			}
		})
	}

	// ssh keeps its stderr for the whole session and multiplexes the remote
	// command's fd 2 onto it, so from here on a diagnostic would land mid-render
	// on a screen Bubble Tea owns. Every prompt that needs a terminal already
	// happened during the dial.
	//
	// Only when there is somewhere to put them. logSink is nil if the log could
	// not be opened at all, and redirecting to nil discards — which would delete
	// the only remaining explanation of a failure in a session that already
	// cannot log. Leaving them on the terminal there is the lesser harm: a
	// cosmetic corruption in an already-degraded setup beats silence.
	if logSink != nil {
		redirectRemoteStderr(logSink)
	}

	p := tea.NewProgram(model)
	finalModel, err := p.Run()
	if err != nil {
		log.Printf("TUI error: %v", err)
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// Save window size and config changes for next launch
	if m, ok := finalModel.(tui.Model); ok {
		m.FlushNotes()
		// Release the connection the Model actually holds. The deferred
		// client.Close() above only knows about the startup client, so after a
		// reconnect it closes a corpse and leaves the live ssh child running.
		// No-op in local mode, where no closer is installed.
		m.CloseClient()
		saveWindowSize(m)
		if m.ConfigChanged() {
			if err := config.Save(config.ConfigPath(), m.Config()); err != nil {
				log.Printf("save config: %v", err)
			} else {
				log.Print("config saved (disclaimer preference updated)")
			}
		}

		// About → Update now / notice → Update now: the confirm dialog
		// already asked, so apply pre-confirmed. The respawned TUI runs a
		// fresh session; this process waits as a wrapper.
		// The TUI suppresses every route to this flag in remote mode, since the
		// banner it would come from describes the REMOTE daemon's staging dir
		// while the apply reads the LOCAL one. Re-checked here rather than
		// trusted: this is the branch that swaps binaries on disk, and a
		// belt-and-braces guard on it costs one comparison.
		if m.ApplyUpdateRequested() && !remoteMode() {
			if maybeApplyStagedUpdate(true) {
				return
			}
			// The user explicitly confirmed "Update now" — silently falling
			// through to a normal exit would look like the confirm did
			// nothing. maybeApplyStagedUpdate already logged the specific
			// failure (verification, swap, or missing staged files); this is
			// the user-facing summary line.
			fmt.Fprintln(os.Stderr, "update was confirmed but could not be applied (staged files missing or failed verification) — run quil again or use About → Update now to re-download.")
		}
	}
	log.Print("TUI exited normally")
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// windowState is persisted to ~/.quil/window.json.
type windowState struct {
	Cols        int  `json:"cols"`
	Rows        int  `json:"rows"`
	PixelWidth  int  `json:"pixel_width,omitempty"`
	PixelHeight int  `json:"pixel_height,omitempty"`
	Maximized   bool `json:"maximized,omitempty"`
}

func loadWindowState() *windowState {
	data, err := os.ReadFile(config.WindowStatePath())
	if err != nil {
		return nil
	}
	var ws windowState
	if err := json.Unmarshal(data, &ws); err != nil {
		return nil
	}
	if ws.Cols <= 0 || ws.Rows <= 0 {
		return nil
	}
	return &ws
}

func restoreWindowSize() {
	ws := loadWindowState()
	if ws == nil {
		return
	}
	restoreWindowSizePlatform(ws)
}

func saveWindowSize(m tui.Model) {
	cols, rows := m.WindowSize()
	// Sanity check: don't save absurd dimensions (minimized, broken state)
	if cols < 40 || rows < 10 || cols > 1000 || rows > 1000 {
		return
	}
	ws := windowState{Cols: cols, Rows: rows}
	// Carry forward the previous session's pixel/maximized state; the platform
	// layer overwrites it only when a real (visible) console window exists, so
	// a ConPTY-hosted session (Windows Terminal) never wipes conhost geometry.
	if prev := loadWindowState(); prev != nil {
		ws.PixelWidth = prev.PixelWidth
		ws.PixelHeight = prev.PixelHeight
		ws.Maximized = prev.Maximized
	}
	// Get pixel dimensions for Windows MoveWindow
	saveWindowSizePlatform(&ws)
	data, err := json.Marshal(ws)
	if err != nil {
		return
	}
	if err := os.WriteFile(config.WindowStatePath(), data, 0600); err != nil {
		log.Printf("save window state: %v", err)
		return
	}
	log.Printf("saved window size: %dx%d (pixels: %dx%d)", ws.Cols, ws.Rows, ws.PixelWidth, ws.PixelHeight)
}
