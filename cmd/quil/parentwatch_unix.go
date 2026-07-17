//go:build !windows

package main

import (
	"log"
	"os"
	"time"
)

// parentWatchInterval paces the orphan poll. Package var so a future test
// harness can shrink it; 2 s keeps the check effectively free while still
// reaping an orphaned bridge promptly.
var parentWatchInterval = 2 * time.Second

// watchParentExit arms the MCP bridge's parent-death watchdog: when the
// process that spawned `quil mcp` exits, the bridge exits too.
//
// On Unix stdin EOF usually terminates the bridge already (server.Run
// returns when the pipe breaks), so this is belt-and-suspenders for the
// case where a leaked descendant still holds the pipe's write end. Orphan
// detection is a ppid poll: when the parent dies the bridge is reparented
// (to init/subreaper) and getppid changes.
//
// Assumption: the DIRECT parent is the bridge's owner. That holds for the
// MCP stdio spawn model — the AI client execs `quil mcp` itself, because
// it must own the stdio pipe ends. A launch behind an intermediate
// wrapper that exits by design would self-terminate here; don't do that.
func watchParentExit() {
	initial := os.Getppid()
	if initial <= 1 {
		// Already orphaned (or spawned by init): no live parent to watch.
		// Don't exit here — a deliberate detached launch stays governed by
		// stdin EOF, which is reliable on Unix.
		return
	}
	go func() {
		ticker := time.NewTicker(parentWatchInterval)
		defer ticker.Stop()
		for range ticker.C {
			if os.Getppid() != initial {
				log.Printf("mcp: parent %d exited (reparented), shutting down", initial)
				os.Exit(0)
			}
		}
	}()
}
