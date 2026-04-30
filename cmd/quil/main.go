package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
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
	version       = "dev"
	buildDevMode  string // "true" to auto-enable dev mode (set via ldflags)
	buildLogLevel string // overrides config log level, e.g. "debug" (set via ldflags)
	daemonBinary  string // daemon binary name, e.g. "quild-dev" (set via ldflags)
)

const (
	daemonStartRetries  = 20
	daemonRetryInterval = 100 * time.Millisecond
)

func main() {
	// Publish this binary's version to the shared version package so
	// subcommands (MCP bridge, handshake logic) and the TUI all read
	// from one place.
	versionpkg.SetCurrent(version)

	// Build-time dev mode: if baked in via ldflags, auto-set QUIL_HOME
	// before anything else. The --dev flag and QUIL_HOME env var still
	// take precedence (they're checked first).
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
		}
	}

	launchTUI()
}

func handleDaemon() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: quil daemon [start|stop|status]")
		os.Exit(1)
	}

	switch os.Args[2] {
	case "start":
		startDaemon(false)
	case "stop":
		stopDaemon()
	case "status":
		daemonStatus()
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

func startDaemon(quiet bool) {
	sockPath := config.SocketPath()

	// Probe existing socket — if daemon is dead, clean up stale
	if client, err := ipc.NewClient(sockPath); err == nil {
		client.Close()
		if !quiet {
			fmt.Println("daemon already running")
		}
		return
	} else if _, statErr := os.Stat(sockPath); statErr == nil {
		// Socket exists but daemon isn't responding → stale
		os.Remove(sockPath)
	}

	quild := findDaemonBinary()
	cmd := exec.Command(quild, "--background")
	cmd.Dir = config.QuilDir() // daemon CWD = ~/.quil/ (not caller's random directory)
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = daemonSysProcAttr()
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to start daemon: %v\n", err)
		os.Exit(1)
	}

	pid := cmd.Process.Pid
	cmd.Process.Release()
	if !quiet {
		fmt.Printf("daemon started (pid %d)\n", pid)
	}
}

func stopDaemon() {
	sockPath := config.SocketPath()
	client, err := ipc.NewClient(sockPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "daemon not running")
		os.Exit(1)
	}
	defer client.Close()

	msg, err := ipc.NewMessage(ipc.MsgShutdown, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create shutdown message: %v\n", err)
		os.Exit(1)
	}
	if err := client.Send(msg); err != nil {
		fmt.Fprintf(os.Stderr, "failed to send shutdown: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("daemon stopped")
}

func daemonStatus() {
	sockPath := config.SocketPath()
	client, err := ipc.NewClient(sockPath)
	if err != nil {
		fmt.Println("daemon not running")
		os.Exit(1)
	}
	client.Close()

	if pidData, err := os.ReadFile(config.PidPath()); err == nil {
		fmt.Printf("daemon running (pid %s)\n", strings.TrimSpace(string(pidData)))
	} else {
		fmt.Println("daemon running")
	}
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
	logPath := filepath.Join(logDir, "quil.log")
	logLevel := cfg.Logging.Level
	if buildLogLevel != "" {
		logLevel = buildLogLevel
	}
	logFile, err := os.OpenFile(logPath, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err == nil && logFile != nil {
		logger.Init(logLevel, logFile)
		defer logFile.Close()
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

	sockPath := config.SocketPath()
	log.Printf("config loaded, AutoStart=%v", cfg.Daemon.AutoStart)

	// Try connecting; auto-start if needed

	client, err := ipc.NewClient(sockPath)
	if err != nil && cfg.Daemon.AutoStart {
		log.Printf("daemon not reachable, auto-starting...")
		startDaemon(true) // quiet — no stdout during TUI launch
		for i := 0; i < daemonStartRetries; i++ {
			time.Sleep(daemonRetryInterval)
			client, err = ipc.NewClient(sockPath)
			if err == nil {
				break
			}
		}
	}
	if err != nil {
		log.Printf("cannot connect to daemon: %v", err)
		fmt.Fprintf(os.Stderr, "cannot connect to daemon: %v\nRun 'quil daemon start' first.\n", err)
		os.Exit(1)
	}
	log.Print("connected to daemon")

	// Version gate: compare TUI and daemon versions before attaching.
	// gateVersionCheck either returns the same client (match / skipped),
	// returns a NEW client connected to a freshly-spawned daemon (after
	// user-confirmed upgrade restart), or exits the process outright
	// (TUI older than daemon — blocking dialog path).
	client = gateVersionCheck(client, sockPath)
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
		saveWindowSize(m)
		if m.ConfigChanged() {
			if err := config.Save(config.ConfigPath(), m.Config()); err != nil {
				log.Printf("save config: %v", err)
			} else {
				log.Print("config saved (disclaimer preference updated)")
			}
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
