//go:build !windows

package main

import (
	"fmt"
	"log"
	"os"
)

func restoreWindowSizePlatform(ws *windowState) {
	// Xterm resize sequence: ESC[8;rows;colst
	// Supported by iTerm2, GNOME Terminal, most modern Unix terminals.
	fmt.Fprintf(os.Stdout, "\x1b[8;%d;%dt", ws.Rows, ws.Cols)
	log.Printf("restored window size: %dx%d (xterm sequence)", ws.Cols, ws.Rows)
}

func saveWindowSizePlatform(ws *windowState) {
	// No pixel dimensions needed on Unix — xterm sequence uses cols/rows.
}
