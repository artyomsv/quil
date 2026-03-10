package main

import (
	"fmt"
	"log"
	"os"

	"github.com/stukans/aethel/internal/config"
	"github.com/stukans/aethel/internal/daemon"
)

func main() {
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
	if err := d.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to start daemon: %v\n", err)
		os.Exit(1)
	}

	d.Wait()
}
