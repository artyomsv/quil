package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/daemon"
	"github.com/artyomsv/quil/internal/logger"
)

var (
	version       = "dev" // overridden at build time via -ldflags "-X main.version=..."
	buildDevMode  string  // "true" to auto-enable dev mode (set via ldflags)
	buildLogLevel string  // overrides config log level, e.g. "debug" (set via ldflags)
)

func main() {
	// Build-time dev mode: auto-set QUIL_HOME before anything else.
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
	logFile := initLogging(logLevel)
	if logFile != nil {
		defer logFile.Close()
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

func initLogging(level string) *os.File {
	logDir := config.QuilDir()
	if logDir == "" {
		return nil
	}
	os.MkdirAll(logDir, 0700)
	f, err := os.OpenFile(filepath.Join(logDir, "quild.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil
	}
	// logger.Init replaces the stdlib log output too, so existing log.Printf
	// call sites bridge through slog at info level and respect the configured
	// level. New code should call logger.Debug/Info/Warn/Error explicitly.
	logger.Init(level, f)
	return f
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
