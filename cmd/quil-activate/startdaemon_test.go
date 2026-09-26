package main

import "testing"

func TestParseStartDaemonArgs(t *testing.T) {
	q, h, err := parseStartDaemonArgs([]string{"--quild", `E:\t\quild.exe`, "--home", `C:\u\.quil`})
	if err != nil || q != `E:\t\quild.exe` || h != `C:\u\.quil` {
		t.Fatalf("got %q %q %v", q, h, err)
	}
	for name, args := range map[string][]string{
		"missing home":   {"--quild", `E:\q.exe`},
		"missing quild":  {"--home", `C:\h`},
		"dangling flag":  {"--quild"},
		"unknown flag":   {"--quild", `E:\q.exe`, "--home", `C:\h`, "--evil", "x"},
		"relative quild": {"--quild", `quild.exe`, "--home", `C:\h`},
		"UNC home":       {"--quild", `E:\q.exe`, "--home", `\\srv\s`},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := parseStartDaemonArgs(args); err == nil {
				t.Errorf("accepted %q", args)
			}
		})
	}
}

// start-daemon dispatches only as argv[1]. A URI activation always starts with
// --scheme, and parseArgs stops at the first bare argument, so a URI-injected
// "start-daemon" lands at index 4 or later and is never dispatched.
func TestIsStartDaemon_OnlyAtArgvOne(t *testing.T) {
	if !isStartDaemon([]string{"quil-activate.exe", "start-daemon", "--quild", "x"}) {
		t.Error("argv[1] start-daemon not recognised")
	}
	if isStartDaemon([]string{"quil-activate.exe", "--scheme", "quil", "quil://x", "start-daemon"}) {
		t.Error("URI-borne start-daemon was recognised")
	}
	if isStartDaemon([]string{"quil-activate.exe"}) {
		t.Error("empty argv recognised")
	}
}
