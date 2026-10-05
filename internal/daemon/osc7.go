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

// maxOSC7Tail bounds the carried fragment: a directory path is far shorter,
// and an introducer never closed (binary output that happens to contain the
// bytes) must not grow without limit.
const maxOSC7Tail = 4096

// scanOSC7 returns the payload of the LAST complete OSC 7 in data (BEL or
// ST terminated; "" for none) and the unfinished end of data to carry into
// the next flush: an OSC 7 still waiting for its terminator after the last
// complete one, or a trailing prefix of the introducer itself. A carry over
// maxOSC7Tail is dropped.
func scanOSC7(data []byte) (payload string, tail []byte) {
	tailFrom := -1
	for end := len(data); end > 0; {
		i := bytes.LastIndex(data[:end], osc7Intro)
		if i < 0 {
			break
		}
		rest := data[i+len(osc7Intro):]
		j := bytes.IndexAny(rest, "\x07\x1b")
		switch {
		case j >= 0 && (rest[j] == 0x07 || (j+1 < len(rest) && rest[j+1] == '\\')):
			payload = string(rest[:j])
		case j < 0 || j+1 == len(rest):
			// No terminator yet, or an ESC that may be the first byte of
			// ST: the report continues in the next flush. Only the LAST
			// introducer can be unfinished, so this runs at most once.
			if tailFrom < 0 && payload == "" {
				tailFrom = i
			}
			end = i
			continue
		}
		if payload != "" {
			break
		}
		end = i
	}
	if tailFrom < 0 {
		// The introducer itself may be cut: "\x1b", "\x1b]" or "\x1b]7".
		for n := len(osc7Intro) - 1; n > 0; n-- {
			if bytes.HasSuffix(data, osc7Intro[:n]) {
				tailFrom = len(data) - n
				break
			}
		}
	}
	if tailFrom >= 0 && len(data)-tailFrom <= maxOSC7Tail {
		tail = append([]byte(nil), data[tailFrom:]...)
	}
	return payload, tail
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
// broadcasts once when it changed. A report split across flushes is joined
// through the pane's carried tail, within one PTY run (generation). A
// sandbox pane is skipped: what runs in it reports a path inside the
// container, and CWD is the HOST path.
func (d *Daemon) detectOSC7CWD(pane *Pane, data []byte, generation uint64) {
	pane.PluginMu.Lock()
	carried := pane.osc7Tail
	if pane.osc7TailGen != generation {
		carried = nil
	}
	pane.osc7Tail, pane.osc7TailGen = nil, generation
	pane.PluginMu.Unlock()

	if len(carried) > 0 {
		data = append(append([]byte(nil), carried...), data...)
	} else if bytes.IndexByte(data, 0x1b) < 0 {
		return
	}
	payload, tail := scanOSC7(data)
	cwd := osc7Path(payload)

	pane.PluginMu.Lock()
	pane.osc7Tail = tail
	changed := validReportedCWD(cwd) && pane.ContainerCWD == "" && pane.CWD != cwd
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
