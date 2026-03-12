package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/artyomsv/aethel/internal/config"
	"github.com/artyomsv/aethel/internal/daemon"
)

func main() {
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
	log.Println("aetheld starting...")
	fmt.Println("aetheld — starting daemon...")
	if err := d.Start(); err != nil {
		log.Printf("failed to start daemon: %v", err)
		fmt.Fprintf(os.Stderr, "failed to start daemon: %v\n", err)
		os.Exit(1)
	}

	// Write PID file after Start() ensures ~/.aethel/ exists
	writePIDFile()
	defer removePIDFile()

	log.Printf("aetheld ready (pid %d)", os.Getpid())
	fmt.Printf("aetheld ready (pid %d). Press Ctrl+C to stop.\n", os.Getpid())
	d.Wait()
}

func initLogging() *os.File {
	logDir := config.AethelDir()
	if logDir == "" {
		return nil
	}
	os.MkdirAll(logDir, 0700)
	f, err := os.OpenFile(filepath.Join(logDir, "aetheld.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil
	}
	log.SetOutput(f)
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	return f
}

func writePIDFile() {
	dir := config.AethelDir()
	if dir == "" {
		log.Println("warning: cannot determine aethel dir, skipping PID file")
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
