package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/artyomsv/aethel/internal/config"
	"github.com/artyomsv/aethel/internal/ipc"
	"github.com/artyomsv/aethel/internal/tui"
)

var version = "dev"

func main() {
	// Check for --dev flag before anything else.
	// Sets AETHEL_HOME to .aethel/ next to the executable for isolated dev instances.
	for i, arg := range os.Args[1:] {
		if arg == "--dev" {
			exe, err := os.Executable()
			if err != nil {
				fmt.Fprintf(os.Stderr, "--dev: cannot determine executable path: %v\n", err)
				os.Exit(1)
			}
			devDir := filepath.Join(filepath.Dir(exe), ".aethel")
			os.Setenv("AETHEL_HOME", devDir)
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
		case "version":
			fmt.Println("aethel v" + version)
			return
		}
	}

	launchTUI()
}

func handleDaemon() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: aethel daemon [start|stop|status]")
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
	// 1. Check PATH first
	if p, err := exec.LookPath("aetheld"); err == nil {
		return p
	}

	// 2. Check alongside the running executable
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidate := filepath.Join(dir, "aetheld")
		if runtime.GOOS == "windows" {
			candidate += ".exe"
		}
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}

	// 3. Fallback — let OS try
	return "aetheld"
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

	aetheld := findDaemonBinary()
	cmd := exec.Command(aetheld, "--background")
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
	// Set up logging early
	logDir := config.AethelDir()
	if logDir != "" {
		os.MkdirAll(logDir, 0700)
	}
	logPath := filepath.Join(logDir, "aethel.log")
	logFile, err := tea.LogToFile(logPath, "aethel")
	if err == nil && logFile != nil {
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

	cfg := config.Default()
	if cfgPath := config.ConfigPath(); fileExists(cfgPath) {
		if loaded, err := config.Load(cfgPath); err == nil {
			cfg = loaded
		}
	}
	log.Printf("config loaded, AutoStart=%v", cfg.Daemon.AutoStart)

	// Try connecting; auto-start if needed
	const (
		daemonStartRetries  = 20
		daemonRetryInterval = 100 * time.Millisecond
	)

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
		fmt.Fprintf(os.Stderr, "cannot connect to daemon: %v\nRun 'aethel daemon start' first.\n", err)
		os.Exit(1)
	}
	defer client.Close()
	log.Print("connected to daemon")

	model := tui.NewModel(client, cfg, version)
	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		log.Printf("TUI error: %v", err)
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	log.Print("TUI exited normally")
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
