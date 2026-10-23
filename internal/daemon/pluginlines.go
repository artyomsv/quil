package daemon

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/google/uuid"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/plugin"
)

// The error handlers match every flushed chunk, ANSI-stripped, as they
// always did: a full-screen tool (codex, opencode, claude-code) redraws its
// error with cursor moves and no LF, and the last line a tool prints before
// it exits often has no newline either — a handler that waited for one would
// never see them. The [update] notice alone matches COMPLETE lines: the 2 ms
// coalescer (streamPTYOutput) cuts a chunk wherever the child's write
// happened to end, so "please update to: v1.55.1" can arrive as "…v1." and
// "55.1\r\n", and the version — the one thing the notice is read for — would
// come out as "1.5" or be missed altogether. The unfinished end of each
// flush is carried into the next within one PTY run, the osc7Tail pattern,
// and matched once more when the run ends (flushPluginTail) so a notice
// printed right before exit with no newline is not lost.

// maxPluginLineTail bounds the carried fragment, like maxOSC7Tail: a
// progress bar that redraws for minutes with no newline must not grow it
// without limit. A notice is far shorter. A longer fragment is dropped, not
// truncated, so a line can never be matched from its middle.
const maxPluginLineTail = 4096

// resetPluginRun forgets the previous PTY run's carried tail, dedupe set and
// offered version when generation is a new run. A restart never joins a
// replacement child to the last one's output, and the new child is free to
// raise its own dialogs. Idempotent within a run.
func resetPluginRun(pane *Pane, generation uint64) {
	pane.PluginMu.Lock()
	if pane.pluginLineGen != generation {
		pane.pluginLineTail, pane.pluginSent, pane.pluginOffered = nil, nil, ""
		pane.pluginLineGen = generation
	}
	pane.PluginMu.Unlock()
}

// pluginLines returns data's complete lines, ANSI-stripped and without their
// line ending, with the previous flush's unfinished line (same PTY run)
// joined in front of the first; the new unfinished end is carried into the
// next flush. A run change drops the old tail (resetPluginRun).
func (d *Daemon) pluginLines(pane *Pane, data []byte, generation uint64) []string {
	resetPluginRun(pane, generation)
	pane.PluginMu.Lock()
	carried := pane.pluginLineTail
	pane.pluginLineTail = nil
	pane.PluginMu.Unlock()

	if len(carried) > 0 {
		data = append(append([]byte(nil), carried...), data...)
	}
	lines, tail := splitCompleteLines(data)
	if len(tail) > maxPluginLineTail {
		tail = nil
	}

	pane.PluginMu.Lock()
	pane.pluginLineTail = tail
	pane.PluginMu.Unlock()
	return lines
}

// takePluginTail returns the carried tail of generation's run as one
// ANSI-stripped line and forgets it. generation 0 (the PTY-less onPaneExit
// path) takes whatever is held.
func takePluginTail(pane *Pane, generation uint64) string {
	pane.PluginMu.Lock()
	tail := pane.pluginLineTail
	if generation != 0 && pane.pluginLineGen != generation {
		tail = nil
	}
	pane.pluginLineTail = nil
	pane.PluginMu.Unlock()
	return string(stripPluginText(bytes.TrimSuffix(tail, []byte{'\r'})))
}

// c1CSI is the 8-bit CSI introducer, the one byte of the C1 range that
// starts a sequence on its own (ESC [ in one byte). internal/tui/oscfilter.go
// exists because a C1 byte reaches the emulator unparsed; here it would
// reach the regexes unstripped, so a colour run written the 8-bit way would
// hide the version in its middle from the notice pattern.
const c1CSI = 0x9b

// stripPluginText removes ANSI sequences from text (8-bit CSI included) and
// returns text itself when there is nothing to strip.
func stripPluginText(text []byte) []byte {
	if bytes.IndexByte(text, 0x1b) < 0 && bytes.IndexByte(text, c1CSI) < 0 {
		return text
	}
	return []byte(ansi.Strip(string(sevenBitCSI(text))))
}

// splitCompleteLines cuts data at every LF, strips ANSI sequences and a
// trailing CR from each complete line (PTY output is CRLF), skips the empty
// ones, and returns the bytes after the last LF as the tail — a copy, since
// data is the flush's own buffer.
func splitCompleteLines(data []byte) (lines []string, tail []byte) {
	for {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			break
		}
		line := stripPluginText(bytes.TrimSuffix(data[:i], []byte{'\r'}))
		data = data[i+1:]
		if len(line) > 0 {
			lines = append(lines, string(line))
		}
	}
	if len(data) > 0 {
		tail = append([]byte(nil), data...)
	}
	return lines, tail
}

// sevenBitCSI rewrites every 8-bit CSI in line — U+009B as the terminal
// receives it in UTF-8 (C2 9B), or a bare 0x9B byte that is not part of a
// character — to the 7-bit ESC [ the stripper is certain to recognise. It
// walks runes so the 9B that ends "ś" (C5 9B) or "ٛ" (D9 9B) is left alone:
// a byte-wise replace would corrupt every Polish or Arabic error message.
// Returns line itself when there is nothing to rewrite.
func sevenBitCSI(line []byte) []byte {
	var out []byte
	for i := 0; i < len(line); {
		r, n := utf8.DecodeRune(line[i:])
		if r == c1CSI || (r == utf8.RuneError && n == 1 && line[i] == c1CSI) {
			if out == nil {
				out = append(make([]byte, 0, len(line)+8), line[:i]...)
			}
			out = append(out, 0x1b, '[')
		} else if out != nil {
			out = append(out, line[i:i+n]...)
		}
		i += n
	}
	if out == nil {
		return line
	}
	return out
}

// pluginSentOnce records key as raised in the pane's current PTY run and
// reports whether it was new. A handler is keyed by title and pattern, an
// update notice by the offered version: a tool that prints the same error on
// every retry — or redraws it on every frame — raises ONE dialog per run,
// and a notice for a newer version than the one already raised is new again.
func pluginSentOnce(pane *Pane, key string) bool {
	pane.PluginMu.Lock()
	defer pane.PluginMu.Unlock()
	if pane.pluginSent[key] {
		return false
	}
	if pane.pluginSent == nil {
		pane.pluginSent = make(map[string]bool)
	}
	pane.pluginSent[key] = true
	return true
}

// pluginFixRunning reports whether the pane's current child is a plugin fix
// (Pane.FixLabel). Nothing is matched while one runs: the fix child's own
// output — `stripe login` printing the very error it fixes, an updater
// quoting the notice — would open dialogs whose buttons the daemon then
// refuses with "a fix is already running".
func pluginFixRunning(pane *Pane) bool {
	pane.PluginMu.Lock()
	defer pane.PluginMu.Unlock()
	return pane.FixLabel != ""
}

// matchPluginHandlers raises a plugin_error for every dialog handler the
// chunk matches, once per run each. One chunk can carry two things to act
// on, and each gets its own frame so an older TUI (which shows the last one
// alone) still shows something true.
func (d *Daemon) matchPluginHandlers(pane *Pane, paneID string, p *plugin.PanePlugin, chunk string) {
	for _, eh := range plugin.MatchErrors(p, chunk) {
		if eh.Action != "dialog" {
			continue
		}
		if !pluginSentOnce(pane, "h:"+eh.Title+"\n"+eh.Pattern) {
			continue
		}
		var fixes []ipc.PluginFixInfo
		for _, f := range eh.Fixes {
			fixes = append(fixes, ipc.PluginFixInfo{Name: f.Name, Label: f.Label})
		}
		d.sendPluginError(pane, ipc.PluginErrorPayload{
			PaneID:  paneID,
			Title:   eh.Title,
			Message: plugin.ExpandMessage(eh.Message, pane.InstanceArgs),
			Plugin:  p.Name,
			Fixes:   fixes,
		})
	}
}

// matchUpdateNotice raises the update dialog when line is the plugin's
// [update] notice for a version this run has not raised yet and the user has
// not declined. The offered version is recorded on the pane
// (Pane.pluginOffered) as the frame leaves: it is the only version the
// daemon accepts back from a client's Update or Not now for this pane.
func (d *Daemon) matchUpdateNotice(pane *Pane, paneID string, p *plugin.PanePlugin, line string) {
	offered, ok := plugin.MatchUpdateNotice(p, line)
	if !ok || !pluginSentOnce(pane, "u:"+offered) {
		return
	}
	// Not now is kept daemon-side so every client agrees and it survives a
	// restart; checked after the run's dedupe so a suppressed notice costs
	// the lookup once per run, not once per line.
	if d.pluginUpdateSkips().Suppressed(p.Name, offered) {
		return
	}
	pane.PluginMu.Lock()
	installed := pane.PluginState["version"]
	pane.pluginOffered = offered
	pane.PluginMu.Unlock()
	message := fmt.Sprintf("%s %s is available.", p.DisplayName, offered)
	if installed != "" {
		message = fmt.Sprintf("%s %s is available; %s is installed.", p.DisplayName, offered, installed)
	}
	payload := ipc.PluginErrorPayload{
		PaneID:  paneID,
		Title:   "Update available: " + p.DisplayName,
		Message: message,
		Plugin:  p.Name,
		Update:  &ipc.PluginUpdateInfo{Installed: installed, Offered: offered},
	}
	// The binary's path is a PATH walk and a symlink resolution: filesystem
	// calls, which never run on the output-flush goroutine — a PATH entry on
	// a dead mount would stall this pane's output. The frame leaves from a
	// goroutine once the path is known, or without it when it is not. The
	// goroutine gets COPIES of the two strings and the managers slice, never
	// the plugin pointer: DetectAvailability and a plugin reload write
	// Command.Cmd while the registry's pointer is out (the RD-029 escape),
	// and a read off this goroutine would race them. The resolver and the
	// timeout are read HERE too, on the calling goroutine, so a test that
	// swaps them is ordered with every read (the goroutine would race the
	// test's restore).
	binPath, binCmd := p.Command.Path, p.Command.Cmd
	managers := p.Update.Managers
	resolve, timeout := resolvePluginBinaryFn, pluginTargetTimeout
	go func() {
		target := pluginBinaryTargetWithin(binPath, binCmd, timeout, resolve)
		payload.Update.Target = target
		// The manager that owns the binary, by the same match the update's
		// argv is chosen with (updateArgv), so the dialog's question names
		// what will really happen: "Update with brew?" rather than "Replace
		// <path>?" for a file a download would never touch.
		if m := matchUpdateManager(managers, target); m != nil {
			payload.Update.Manager = m.Name
		}
		d.sendPluginError(pane, payload)
	}()
}

// flushPluginTail matches the [update] notice once against the unfinished
// line a PTY run left behind when it ends — a tool that prints the notice
// and exits without a newline — then forgets it. Handlers need no such pass:
// they saw the tail's bytes in the chunk that carried them.
func (d *Daemon) flushPluginTail(pane *Pane, generation uint64) {
	if pane.Type == "" || pane.Type == "terminal" {
		return
	}
	p := d.registry.Get(pane.Type)
	if p == nil || p.Update == nil {
		return
	}
	line := takePluginTail(pane, generation)
	if line == "" || pluginFixRunning(pane) {
		return
	}
	d.matchUpdateNotice(pane, pane.ID, p, line)
}

// sendPluginError puts one card for the dialog in the notification sidebar
// and broadcasts the dialog. The card first: the TUI holds a dialog for a
// pane that is not on screen until the user switches to it, and the card is
// how they learn to — and a client that has the frame can then rely on the
// card being queued. emitEvent applies the pane's mute itself — a muted pane
// gets no card, and the dialog broadcast is unaffected.
func (d *Daemon) sendPluginError(pane *Pane, payload ipc.PluginErrorPayload) {
	pane.PluginMu.Lock()
	tabID := pane.CurrentTabID()
	name := pane.Name
	pane.PluginMu.Unlock()
	d.emitEvent(withExcerpt(PaneEvent{
		ID: uuid.New().String(), PaneID: payload.PaneID, TabID: tabID,
		PaneName: name, Type: "plugin_attention",
		Title: payload.Title, Message: payload.Message, Severity: "warning",
		Timestamp: time.Now(), Data: map[string]string{"plugin": payload.Plugin},
	}, paneOutputExcerpt(pane, 3)))

	msg, _ := ipc.NewMessage(ipc.MsgPluginError, payload)
	d.broadcast(msg)
}

// pluginTargetTimeout bounds resolving the binary's path for one update
// dialog. Short, like spawnDirProbeTimeout: the dialog is useful without the
// path (the update request resolves it again), and the user is waiting for
// the dialog, not the path. A var so a test can shorten it.
var pluginTargetTimeout = 2 * time.Second

// resolvePluginBinaryFn is pluginBinaryTarget; a test replaces it with one
// that never answers.
var resolvePluginBinaryFn = pluginBinaryTarget

// pluginBinaryTargetWithin is resolve (pluginBinaryTarget, as the caller
// read it from resolvePluginBinaryFn on its own goroutine) under the
// process-wide blocking-FS budget (browse.go): the walk runs on a worker
// that keeps its permit for as long as the syscalls really take, and the
// caller gets "" when no permit is free or the worker has not answered
// within d. The worker touches no package variable, so a test's restore of
// one can never race it.
func pluginBinaryTargetWithin(path, cmd string, d time.Duration, resolve func(path, cmd string) string) string {
	if !claimBlockingFSCall() {
		return ""
	}
	ch := make(chan string, 1)
	go func() {
		defer releaseBlockingFSCall()
		ch <- resolve(path, cmd)
	}()
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case target := <-ch:
		return target
	case <-timer.C:
		return ""
	}
}

// pluginBinaryTarget is the symlink-resolved path of the binary the pane's
// plugin runs, as this daemon resolved it: the explicit [command] path when
// one is set, else the PATH lookup DetectAvailability made (or the location
// its fallback search wrote back into Cmd). Takes the two strings, not the
// plugin, so it can run off the flush goroutine. Empty when it cannot be
// found — the dialog then has no path to confirm and the update request
// decides.
func pluginBinaryTarget(path, cmd string) string {
	bin := path
	if bin == "" {
		bin = cmd
	}
	if bin == "" {
		return ""
	}
	found, err := exec.LookPath(bin)
	if err != nil {
		return ""
	}
	if abs, err := filepath.Abs(found); err == nil {
		found = abs
	}
	if resolved, err := filepath.EvalSymlinks(found); err == nil {
		found = resolved
	}
	return found
}
