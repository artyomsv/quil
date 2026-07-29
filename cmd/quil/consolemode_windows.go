//go:build windows

package main

import (
	"log"
	"os"
	"syscall"
	"unsafe"
)

// Console-mode save/restore for the terminal this process was launched in.
//
// Why this exists: a non-batch ssh dial spawns OpenSSH's ssh.exe, which puts the
// local console into VT passthrough — including DISABLE_NEWLINE_AUTO_RETURN, so
// a bare "\n" moves down a row WITHOUT returning to column 0. ssh restores the
// mode when it exits normally, but stdioConn.Close kills it with
// TerminateProcess, and a terminated process runs no cleanup. Everything printed
// afterwards then staircases one indent further right per line.
//
// It only bites the diagnostic paths, because a SUCCESSFUL dial hands the
// terminal to Bubble Tea, which sets and restores its own modes. That makes it
// worse rather than better: the version-mismatch report, the remote-install
// offer, and the "cannot reach the daemon" explanation are all printed exactly
// when the user most needs to read them.
//
// Restoring the saved mode is preferred over writing "\r\n" at each print site.
// It fixes the cause once instead of every current and future caller, and it
// keeps CR bytes out of stderr when that is redirected to a file.
//
// kernel32 is declared in window_windows.go — same package, same build tag.
var (
	getConsoleMode = kernel32.NewProc("GetConsoleMode")
	setConsoleMode = kernel32.NewProc("SetConsoleMode")
)

// savedMode is one handle's console mode as it was at startup. ok is false when
// the handle is not a console (piped, redirected, or running under a harness),
// in which case there is no mode to restore and nothing to fix.
type savedMode struct {
	h    syscall.Handle
	mode uint32
	ok   bool
}

var savedConsoleModes []savedMode

// saveConsoleMode records the console modes as they were before anything this
// process spawns can disturb them. Called once, early in main.
//
// stdin is included because ssh reconfigures input as well as output, and a
// console left with VT input enabled changes how a subsequent [y/N] read behaves.
func saveConsoleMode() {
	savedConsoleModes = nil
	for _, f := range []*os.File{os.Stdout, os.Stderr, os.Stdin} {
		h := syscall.Handle(f.Fd())
		var mode uint32
		r, _, _ := getConsoleMode.Call(uintptr(h), uintptr(unsafe.Pointer(&mode)))
		savedConsoleModes = append(savedConsoleModes, savedMode{h: h, mode: mode, ok: r != 0})
	}
}

// restoreConsoleMode puts the console back the way saveConsoleMode found it.
//
// Called before printing a diagnostic that follows an ssh dial. Idempotent, and
// a no-op for any handle that was not a console. Failures are logged rather than
// surfaced: the caller is already reporting a real problem, and "could not
// restore the console" stacked on top of it would bury the actual message.
func restoreConsoleMode() {
	for _, s := range savedConsoleModes {
		if !s.ok {
			continue
		}
		if r, _, err := setConsoleMode.Call(uintptr(s.h), uintptr(s.mode)); r == 0 {
			log.Printf("restore console mode on handle %v: %v", s.h, err)
		}
	}
}
