package daemon

import (
	"bytes"
	"net/url"
	"strings"
)

// The daemon follows a shell's directory itself, from the OSC 7 its prompt
// hooks print (internal/shellinit). It used to learn a `cd` only from a TUI's
// update_pane{cwd} — the TUI's emulator parses OSC 7 — so with no TUI
// attached (a browser alone, an MCP agent) Pane.CWD stayed the directory the
// pane was SPAWNED in, and every action resolved from it (a quick split, the
// overlay's repository, a delegated task) targeted the wrong folder. The
// browser's gateway refuses update_pane.cwd on purpose, so the page cannot
// report it either: the daemon reading its own PTY output is the one source
// every client shares.

var osc7Intro = []byte("\x1b]7;")

// lastOSC7 returns the payload of the LAST complete OSC 7 in data (BEL or
// ST terminated), "" for none. A sequence split across two flushes is
// skipped: the next prompt prints it again.
func lastOSC7(data []byte) string {
	for end := len(data); end > 0; {
		i := bytes.LastIndex(data[:end], osc7Intro)
		if i < 0 {
			return ""
		}
		rest := data[i+len(osc7Intro):]
		if j := bytes.IndexAny(rest, "\x07\x1b"); j >= 0 {
			if rest[j] == 0x07 || (j+1 < len(rest) && rest[j+1] == '\\') {
				return string(rest[:j])
			}
		}
		end = i
	}
	return ""
}

// osc7Path is internal/tui/pane.go parseOSC7Path, kept equal so the TUI's own
// report (still sent) agrees with this one instead of flipping the value
// back: file://host/path, with the leading slash of a Windows drive path
// ("/C:/x") dropped. Unlike the TUI it refuses what is not a file URI rather
// than taking the raw text as a path.
func osc7Path(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "file" {
		return ""
	}
	p := u.Path
	if len(p) >= 3 && p[0] == '/' && p[2] == ':' {
		p = p[1:]
	}
	return p
}

// validReportedCWD is handleUpdatePane's guard, shared: a UNC or device path
// (\\host\share, //host/share, \\?\...) that a crafted OSC 7 could inject is
// never stored, so it never reaches workspace.json or a restore's os.Stat.
func validReportedCWD(cwd string) bool {
	return cwd != "" && !strings.HasPrefix(cwd, `\\`) && !strings.HasPrefix(cwd, `//`)
}

// detectOSC7CWD stores the directory the pane's shell reported, and
// broadcasts once when it changed. A sandbox pane is skipped: what runs in it
// reports a path inside the container, and CWD is the HOST path.
func (d *Daemon) detectOSC7CWD(pane *Pane, data []byte) {
	if !bytes.Contains(data, osc7Intro) {
		return
	}
	cwd := osc7Path(lastOSC7(data))
	if !validReportedCWD(cwd) {
		return
	}
	pane.PluginMu.Lock()
	changed := pane.ContainerCWD == "" && pane.CWD != cwd
	if changed {
		pane.CWD = cwd
	}
	pane.PluginMu.Unlock()
	// Outside PluginMu: broadcastState takes every pane's lock.
	if changed {
		d.broadcastState()
		d.requestSnapshot()
	}
}
