//go:build windows

package main

import (
	"errors"
	"log"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// watchParentExit arms the MCP bridge's parent-death watchdog: when the
// process that spawned `quil mcp` (the AI client — claude.exe or an IDE)
// exits for ANY reason, the bridge exits too.
//
// Why stdin EOF is not enough on Windows: the MCP client spawns several
// stdio servers concurrently, and concurrently-spawned siblings inherit
// each other's pipe handles (spawn-time handle inheritance races). When the
// client dies — pane killed, session restarted, crash — a sibling still
// holds the write end of this bridge's stdin pipe, so the blocking read
// inside server.Run never sees EOF and the process leaks forever (observed
// in production: 20 bridges accumulated over a week, in same-second spawn
// pairs). The parent process HANDLE is signaled on exit regardless of who
// holds which pipe, so waiting on it is the reliable lifetime signal.
func watchParentExit() {
	ppid := os.Getppid()
	if ppid <= 0 {
		return // no recorded parent — nothing to watch, rely on stdin EOF
	}
	h, err := windows.OpenProcess(
		windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION,
		false, uint32(ppid))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			// The PID no longer exists: the recorded parent died before we
			// could open it — this bridge was orphaned at birth. Exit
			// before serving anything.
			log.Printf("mcp: parent %d already gone at startup, exiting", ppid)
			os.Exit(0)
		}
		// Access denied or another open failure: the parent may well be
		// alive. Exiting would kill a healthy bridge, and there is no
		// handle to wait on — abandon the watchdog and leave lifetime to
		// stdin EOF (the pre-watchdog behavior).
		log.Printf("mcp: cannot open parent %d (%v) — stdin EOF governs lifetime", ppid, err)
		return
	}

	// PID-reuse guard: os.Getppid returns the PID recorded at creation. If
	// the original parent died before OpenProcess above, the PID may now
	// belong to an unrelated process that could run for weeks — exactly the
	// leak this watchdog exists to prevent. The impostor is detectable by
	// its creation time: a real parent cannot be younger than its child.
	var pCreate, pExit, pKernel, pUser windows.Filetime
	var sCreate, sExit, sKernel, sUser windows.Filetime
	errParent := windows.GetProcessTimes(h, &pCreate, &pExit, &pKernel, &pUser)
	errSelf := windows.GetProcessTimes(windows.CurrentProcess(), &sCreate, &sExit, &sKernel, &sUser)
	if errParent != nil || errSelf != nil {
		// Identity unverifiable: waiting on an unverified handle risks the
		// exact leak this watchdog prevents (an impostor may never exit),
		// while exiting risks killing a bridge whose parent is alive.
		// Abandon the watchdog — stdin EOF governs, as before the feature.
		// CloseHandle error deliberately ignored: we are leaving the
		// watchdog path and the OS reclaims the handle at process exit.
		_ = windows.CloseHandle(h)
		log.Printf("mcp: cannot verify parent %d identity (parent times: %v, self times: %v) — stdin EOF governs lifetime",
			ppid, errParent, errSelf)
		return
	}
	parentCreated := time.Unix(0, pCreate.Nanoseconds())
	selfCreated := time.Unix(0, sCreate.Nanoseconds())
	if !parentHandleTrustworthy(parentCreated, selfCreated) {
		// CloseHandle error deliberately ignored: os.Exit follows
		// immediately and the OS reclaims the handle either way.
		_ = windows.CloseHandle(h)
		log.Printf("mcp: parent pid %d was reused by a newer process, original parent is gone — exiting", ppid)
		os.Exit(0)
	}

	go func() {
		// Blocks until the parent's process object is signaled (it exited).
		// The wait result is deliberately ignored: after a verified-parent
		// INFINITE wait the outcomes are "signaled" or "wait failed", and
		// for a helper process whose only purpose is to serve its parent,
		// both mean the same thing — stop serving. Erring toward exit is
		// the safe direction.
		windows.WaitForSingleObject(h, windows.INFINITE)
		log.Printf("mcp: parent %d exited, shutting down", ppid)
		os.Exit(0)
	}()
}
