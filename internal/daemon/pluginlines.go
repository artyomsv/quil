package daemon

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/google/uuid"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/plugin"
	"github.com/artyomsv/quil/internal/update"
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
		pane.pluginLineTail, pane.pluginSent, pane.pluginOffering, pane.pluginOffered = nil, nil, nil, ""
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
// alone) still shows something true. A handler with offer_update also
// starts the latest-release lookup (offerLatestRelease), once per run.
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
		if eh.OfferUpdate && p.Update != nil && p.Update.GitHub != "" {
			d.offerLatestRelease(pane, paneID, p)
		}
	}
}

// updateOfferSource is what offerUpdate needs from the plugin, copied on
// the flush goroutine: the goroutines that finish an offer must never hold
// the plugin pointer — DetectAvailability and a plugin reload write
// Command.Cmd while the registry's pointer is out (the RD-029 escape), and
// a read off the flush goroutine would race them.
type updateOfferSource struct {
	name, displayName string
	binPath, binCmd   string
	managers          []plugin.UpdateManager
}

func newUpdateOfferSource(p *plugin.PanePlugin) updateOfferSource {
	return updateOfferSource{
		name: p.Name, displayName: p.DisplayName,
		binPath: p.Command.Path, binCmd: p.Command.Cmd,
		managers: p.Update.Managers,
	}
}

// matchUpdateNotice raises the update dialog when line is the plugin's
// [update] notice (offerUpdate). generation is the run the line came from.
func (d *Daemon) matchUpdateNotice(pane *Pane, paneID string, p *plugin.PanePlugin, line string, generation uint64) {
	if offered, ok := plugin.MatchUpdateNotice(p, line); ok {
		d.offerUpdate(pane, paneID, newUpdateOfferSource(p), offered, generation)
	}
}

// offerUpdate raises the update dialog for offered — a version the tool
// printed in its notice, or the latest release GitHub named — unless the
// pane has left run (a restart since the offer was found), a fix is armed
// or running, the run has raised it already, or the user declined it. run is
// the PTY generation (Pane.ptyGen) the offer was found in. The offered
// version is recorded on the pane (Pane.pluginOffered) as the frame leaves:
// it is the only version the daemon accepts back from a client's Update or
// Not now for this pane.
//
// The run check, the dedupe and the record are committed AT DELIVERY, in one
// PluginMu span after the binary's path is resolved, never before the
// resolution starts: resolving can take up to the target timeout, and in
// that window the pane can restart — silently, or into a fix whose output
// is never matched — and offer a version for its new run; a stale
// completion committed up front would then broadcast over the newer dialog
// and leave pluginOffered naming the wrong run's version. The check before
// the goroutine is only a cheap skip. run 0 (the PTY-less onPaneExit path)
// means the current run. Safe off the flush goroutine: src is a copy.
func (d *Daemon) offerUpdate(pane *Pane, paneID string, src updateOfferSource, offered string, run uint64) {
	// Not now is kept daemon-side so every client agrees and it survives a
	// restart. Read first, outside PluginMu: the store loads from disk on its
	// first use and must not do so under the pane's lock.
	if d.pluginUpdateSkips().Suppressed(src.name, offered) {
		return
	}
	key := "u:" + offered
	// superseded reports, under PluginMu, whether this offer is no longer the
	// pane's: the INSTALLED PTY is a later child than run (pane.ptyGen — not
	// pluginLineGen, which moves only when the replacement prints plugin
	// output, so a silent restart would leave it behind), a fix is armed or
	// running (its own output is never matched, so a dialog for it would be
	// one nothing can act on), or the version was raised already.
	superseded := func() bool {
		return (run != 0 && pane.ptyGen != run) || pane.FixLabel != "" || len(pane.fixArgv) > 0 || pane.pluginSent[key]
	}
	// The cheap pre-check: superseded already, or the same version's
	// resolution is in flight for this run — no second resolver for it. The
	// in-flight mark is cleared when the goroutine commits or drops.
	pane.PluginMu.Lock()
	inFlight, marked := pane.pluginOffering[key]
	skip := superseded() || (marked && inFlight == run)
	if !skip {
		if pane.pluginOffering == nil {
			pane.pluginOffering = make(map[string]uint64)
		}
		pane.pluginOffering[key] = run
	}
	pane.PluginMu.Unlock()
	if skip {
		return
	}
	// The binary's path is a PATH walk and a symlink resolution: filesystem
	// calls, which never run on the output-flush goroutine — a PATH entry on
	// a dead mount would stall this pane's output. The frame leaves from a
	// goroutine once the path is known, or without it when it is not. The
	// configuration and the resolver are read HERE, on the calling goroutine,
	// so a test that sets them is ordered with every read.
	resolve, timeout := resolvePluginBinaryFn, d.pluginUpdateCfg().targetTimeout
	go func() {
		target := pluginBinaryTargetWithin(src.binPath, src.binCmd, timeout, resolve)
		// The manager that owns the binary, by the same match the update's
		// argv is chosen with (updateArgv), so the dialog's question names
		// what will really happen: "Update with brew?" rather than "Replace
		// <path>?" for a file a download would never touch.
		manager := ""
		if m := matchUpdateManager(src.managers, target); m != nil {
			manager = m.Name
		}
		// Commit: still this run, still unsent — then it is sent and recorded.
		// The in-flight mark goes only when it is still THIS run's: the new
		// run may have marked the same version after a restart, and that mark
		// is its resolver's, not ours.
		pane.PluginMu.Lock()
		if marked, ok := pane.pluginOffering[key]; ok && marked == run {
			delete(pane.pluginOffering, key)
		}
		if superseded() {
			pane.PluginMu.Unlock()
			return
		}
		if pane.pluginSent == nil {
			pane.pluginSent = make(map[string]bool)
		}
		pane.pluginSent[key] = true
		installed := pane.PluginState["version"]
		pane.pluginOffered = offered
		pane.PluginMu.Unlock()
		message := fmt.Sprintf("%s %s is available.", src.displayName, offered)
		if installed != "" {
			message = fmt.Sprintf("%s %s is available; %s is installed.", src.displayName, offered, installed)
		}
		d.sendPluginError(pane, ipc.PluginErrorPayload{
			PaneID:  paneID,
			Title:   "Update available: " + src.displayName,
			Message: message,
			Plugin:  src.name,
			Update:  &ipc.PluginUpdateInfo{Installed: installed, Offered: offered, Target: target, Manager: manager},
		})
	}()
}

// pluginUpdateConfig is the update dialog machinery's configuration: the
// GitHub API host offer_update asks and the two timeouts. One per Daemon,
// set by New (defaultPluginUpdateConfig) and read through pluginUpdateCfg —
// never a package variable, so two daemons in one process (or two tests)
// cannot configure each other. A test sets the fields on its own daemon
// before it drives it.
type pluginUpdateConfig struct {
	// releaseBaseURL is the GitHub API host; "" is update.DefaultBaseURL.
	releaseBaseURL string
	// targetTimeout bounds resolving the binary's path for one update
	// dialog, and a fix program's PATH lookup. Short, like
	// spawnDirProbeTimeout: the dialog is useful without the path (the
	// update request resolves it again), and the user is waiting for the
	// dialog, not the path.
	targetTimeout time.Duration
	// latestTimeout bounds one GitHub latest-release lookup for an
	// offer_update handler. The user is reading the handler's dialog
	// meanwhile; an answer later than this would arrive as an update frame
	// merging into a dialog they have long closed.
	latestTimeout time.Duration
}

const (
	defaultPluginTargetTimeout  = 2 * time.Second
	defaultLatestReleaseTimeout = 10 * time.Second
)

func defaultPluginUpdateConfig() pluginUpdateConfig {
	return pluginUpdateConfig{
		targetTimeout: defaultPluginTargetTimeout,
		latestTimeout: defaultLatestReleaseTimeout,
	}
}

// pluginUpdateCfg is d.pluginUpdate with every zero field read as its
// default, so a hand-built daemon (the tests' &Daemon{}) behaves like one
// New made.
func (d *Daemon) pluginUpdateCfg() pluginUpdateConfig {
	c := d.pluginUpdate
	if c.targetTimeout <= 0 {
		c.targetTimeout = defaultPluginTargetTimeout
	}
	if c.latestTimeout <= 0 {
		c.latestTimeout = defaultLatestReleaseTimeout
	}
	return c
}

// latestReleaseCacheTTL is how long one repository's latest version answers
// every pane's lookup. The unauthenticated GitHub API allows 60 requests an
// hour per address, shared with Quil's own update check; a pane that
// restarts in a loop on the too-old error would spend them all on one
// answer. Only successes are cached — a failure is retried on the next run.
const latestReleaseCacheTTL = 10 * time.Minute

// latestTagCache is the per-repository cache of GitHub latest versions
// (latestReleaseCacheTTL). The zero value is ready.
type latestTagCache struct {
	mu      sync.Mutex
	entries map[string]latestTagEntry
}

type latestTagEntry struct {
	version string
	at      time.Time
}

// get returns the cached version for repo when it is younger than the TTL.
func (c *latestTagCache) get(repo string, now time.Time) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[repo]
	if !ok || now.Sub(e.at) >= latestReleaseCacheTTL {
		return "", false
	}
	return e.version, true
}

func (c *latestTagCache) put(repo, version string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]latestTagEntry)
	}
	c.entries[repo] = latestTagEntry{version: version, at: now}
}

// offerLatestRelease asks GitHub for the latest release of the plugin's
// [update] repository and offers its version (offerUpdate), for a handler
// with offer_update: the tool is too old to print its own notice — a Stripe
// CLI that fails on a flag newer CLIs require exits before it says a newer
// version exists — so the "too old" dialog would have no Update button for
// exactly the users who need it. ONE lookup per pane and run (the "gh:"
// dedupe key; the handler's own key already gives once per handler), served
// from the per-repository cache when it can be, else on a goroutine bounded
// by the configured latest timeout. A failed lookup is logged and offers nothing: the
// handler's Close-only dialog stays. An answer that arrives after the run
// ended is dropped by offerUpdate's run check — the dialog it would merge
// into describes a child that is gone.
func (d *Daemon) offerLatestRelease(pane *Pane, paneID string, p *plugin.PanePlugin) {
	if !pluginSentOnce(pane, "gh:latest") {
		return
	}
	// The installed PTY's generation, which offerUpdate validates the
	// answer against at delivery (a restart bumps it whether or not the
	// replacement prints anything).
	pane.PluginMu.Lock()
	run := pane.ptyGen
	pane.PluginMu.Unlock()
	// Everything the goroutine needs, copied here: the plugin pointer stays
	// behind (see updateOfferSource) and the configuration is read on this
	// goroutine, so a test that set it is ordered before the read.
	src := newUpdateOfferSource(p)
	repo, cfg := p.Update.GitHub, d.pluginUpdateCfg()
	if version, ok := d.latestTags.get(repo, time.Now()); ok {
		d.offerUpdate(pane, paneID, src, version, run)
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), cfg.latestTimeout)
		defer cancel()
		checker := &update.Checker{Repo: repo, BaseURL: cfg.releaseBaseURL}
		rel, err := checker.Latest(ctx)
		if err != nil {
			log.Printf("pane %s: %s latest release lookup failed, no update offered: %v", paneID, src.name, err)
			return
		}
		// The tag becomes a release TAG and an asset NAME on the update argv
		// (updateArgv): the same shape check a client's version gets.
		version, err := pluginVersionArg(rel.TagName)
		if err != nil {
			log.Printf("pane %s: %s latest release tag %q unusable, no update offered: %v", paneID, src.name, rel.TagName, err)
			return
		}
		d.latestTags.put(repo, version, time.Now())
		d.offerUpdate(pane, paneID, src, version, run)
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
	d.matchUpdateNotice(pane, pane.ID, p, line, generation)
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

// resolvePluginBinaryFn is pluginBinaryTarget; a test replaces it with one
// that never answers. (The timeout it runs under is
// pluginUpdateConfig.targetTimeout.)
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
