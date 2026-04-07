package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/daemon"
)

var version = "dev" // overridden at build time via -ldflags "-X main.version=..."

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println("quild v" + version)
		return
	}

	background := len(os.Args) > 1 && os.Args[1] == "--background"

	logFile := initLogging()
	if logFile != nil {
		defer logFile.Close()
	}

	if background {
		devNull, err := os.Open(os.DevNull)
		if err != nil {
			log.Printf("warning: could not open %s: %v", os.DevNull, err)
		} else {
			os.Stdout = devNull
			os.Stderr = devNull
		}
	}

	cfg := config.Default()

	cfgPath := config.ConfigPath()
	if _, err := os.Stat(cfgPath); err == nil {
		loaded, err := config.Load(cfgPath)
		if err != nil {
			log.Printf("warning: failed to load config: %v", err)
		} else {
			cfg = loaded
		}
	}

	d := daemon.New(cfg)
	log.Printf("quild v%s starting...", version)
	fmt.Println("quild — starting daemon...")
	if err := d.Start(); err != nil {
		log.Printf("failed to start daemon: %v", err)
		fmt.Fprintf(os.Stderr, "failed to start daemon: %v\n", err)
		os.Exit(1)
	}

	// Write PID file after Start() ensures ~/.quil/ exists
	writePIDFile()
	defer removePIDFile()

	log.Printf("quild ready (pid %d)", os.Getpid())
	fmt.Printf("quild ready (pid %d). Press Ctrl+C to stop.\n", os.Getpid())
	d.Wait()
}

func initLogging() *os.File {
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
	log.SetOutput(f)
	log.SetFlags(log.LstdFlags | log.Lshortfile)
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
