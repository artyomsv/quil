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
	versionpkg "github.com/artyomsv/quil/internal/version"
)

var (
	version       = "dev" // overridden at build time via -ldflags "-X main.version=..."
	buildDevMode  string  // "true" to auto-enable dev mode (set via ldflags)
	buildLogLevel string  // overrides config log level, e.g. "debug" (set via ldflags)
)

func main() {
	// Publish this binary's version to the shared version package so the
	// IPC MsgVersionReq handler can report it back to connecting clients.
	versionpkg.SetCurrent(version)

	// Build-time dev mode: auto-set QUIL_HOME before anything else.
	if buildDevMode == "true" && os.Getenv("QUIL_HOME") == "" {
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintf(os.Stderr, "dev build: cannot determine executable path: %v\n", err)
			os.Exit(1)
		}
		os.Setenv("QUIL_HOME", filepath.Join(filepath.Dir(exe), ".quil"))
	}

	// Native Claude hook fast-path. Registered via --settings as the command
	// Claude runs per hook event (see internal/claudehook). It must NOT start
	// the daemon — it reads the hook JSON on stdin, writes the session-id file
	// / spool line under $QUIL_HOME, and exits 0 so Claude is never blocked.
	// Replacing the per-event PowerShell/sh script with this native subcommand
	// cuts hook latency from ~1-4 s (shell cold start) to tens of ms.
	if len(os.Args) > 1 && os.Args[1] == "claude-hook" {
		runClaudeHook()
		return
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
