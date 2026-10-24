package daemon

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/plugin"
)

// The error handlers are matched on every ANSI-stripped chunk; the [update]
// notice on complete lines joined across the 2 ms coalescer's flushes, and
// once more on the run's unfinished tail when the child exits. Each dialog
// is raised once per PTY run. Driven through applyPluginHandlers — the
// flush's own call — with a dialed client reading the broadcasts, since the
// defect this guards against is in what reaches the wire.

const fixmeNotice = "A newer version of the Stripe CLI is available, please update to: v1.55.1"

// fixmePluginTOML is a stripe-shaped plugin: one dialog handler with a fix,
// one log-only handler, and a notice-only [update] table (the download
// fields are all-or-nothing and not under test here).
const fixmePluginTOML = `
[plugin]
name = "fixme"
display_name = "Fix Me"
category = "tools"
schema_version = 1

[command]
cmd = "stripe"
#PATH#
args = ["listen"]

[persistence]
strategy = "rerun"

[[error_handlers]]
pattern = 'api_key_expired|Expired API Key'
title = "Login expired"
message = "Log in again."
action = "dialog"

  [[error_handlers.fixes]]
  name = "login"
  label = "Log in"
  run = ["stripe", "login"]

[[error_handlers]]
pattern = 'quietly noted'
title = "Logged only"
message = "never a dialog"
action = "log"

[update]
notice = 'newer version of the Stripe CLI is available, please update to: v?(\d+\.\d+\.\d+)'
`

// registerFixmePlugin loads fixmePluginTOML into d's registry through
// LoadFromDir, so it passes the same validation a shipped plugin does. A
// non-empty binPath becomes the [command] path override.
func registerFixmePlugin(t *testing.T, d *Daemon, binPath string) *plugin.PanePlugin {
	t.Helper()
	return registerFixmeTOML(t, d, fixmePluginTOML, binPath)
}

// registerFixmeTOML loads toml — fixmePluginTOML or a variant of it, with
// its #PATH# marker — as the fixme plugin.
func registerFixmeTOML(t *testing.T, d *Daemon, toml, binPath string) *plugin.PanePlugin {
	t.Helper()
	dir := t.TempDir()
	if binPath != "" {
		toml = strings.Replace(toml, "#PATH#", "path = '"+filepath.ToSlash(binPath)+"'", 1)
	}
	if err := os.WriteFile(filepath.Join(dir, "fixme.toml"), []byte(toml), 0o644); err != nil {
		t.Fatalf("write fixme.toml: %v", err)
	}
	if err := d.registry.LoadFromDir(dir); err != nil {
		t.Fatalf("load test plugins: %v", err)
	}
	p := d.registry.Get("fixme")
	if p == nil {
		t.Fatal("fixme plugin did not load")
	}
	return p
}

// parkedLookup is a lookup stub that answers only once release() is called,
// standing in for a PATH entry on a dead mount. The production code copies
// the stub out of its package variable on the calling goroutine before the
// worker starts, so the worker never reads the variable a cleanup restores;
// restore still waits for the stub to come back, so no worker outlives the
// test that parked it.
type parkedLookup struct {
	release  chan struct{}
	returned chan struct{}
	once     sync.Once
}

func newParkedLookup() *parkedLookup {
	return &parkedLookup{release: make(chan struct{}), returned: make(chan struct{})}
}

// park is the stub's body: block until released, then mark the return.
func (l *parkedLookup) park() {
	<-l.release
	l.once.Do(func() { close(l.returned) })
}

// restore releases the stub, waits for it to come back, then runs put —
// the caller's reassignment of the package variables.
func (l *parkedLookup) restore(t *testing.T, put func()) {
	t.Helper()
	close(l.release)
	select {
	case <-l.returned:
	case <-time.After(3 * time.Second):
		t.Error("the parked lookup was never called, or never returned")
	}
	put()
}

// currentGen is the pane's PTY run, the generation its flushes carry.
func currentGen(pane *Pane) uint64 {
	pane.PluginMu.Lock()
	defer pane.PluginMu.Unlock()
	return pane.ptyGen
}

// newFixmePane creates a pane of type fixme in a fresh tab. It has no PTY;
// its generation is set to 1, the run the tests flush as, because an update
// offer is validated against the INSTALLED generation (pane.ptyGen) at
// delivery and a pane at generation 0 would drop every offer made for run 1.
func newFixmePane(t *testing.T, d *Daemon) *Pane {
	t.Helper()
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, "")
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	pane.PluginMu.Lock()
	pane.Type = "fixme"
	pane.ptyGen = 1
	pane.PluginMu.Unlock()
	return pane
}

// dialBroadcastReader starts d's IPC server and returns a client that
// receives its broadcasts. It returns only once a reply has come back on
// the conn: the server adds a conn to its broadcast list in acceptLoop,
// after the dial itself returns, so a broadcast sent right after NewClient
// can miss a conn that is not registered yet.
func dialBroadcastReader(t *testing.T, d *Daemon) *ipc.Client {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "s.sock")
	d.server = ipc.NewServer(sock, d.handleMessage, d.onClientDisconnect)
	if err := d.server.Start(); err != nil {
		t.Fatalf("start IPC server: %v", err)
	}
	t.Cleanup(func() { d.server.Stop() })
	broadcastSocks.Store(d, sock)
	return dialBroadcastSock(t, sock)
}

// broadcastSocks remembers the socket dialBroadcastReader started for each
// daemon, so a test can dial a SECOND reader (quietReader) on it.
var broadcastSocks sync.Map

// dialBroadcastSock dials sock and returns once the conn is registered for
// broadcasts (see dialBroadcastReader).
func dialBroadcastSock(t *testing.T, sock string) *ipc.Client {
	t.Helper()
	c, err := ipc.NewClient(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	sendWithID(t, c, ipc.MsgStateReq, "registered", struct{}{})
	readUntilID(t, c, ipc.MsgWorkspaceState, "registered", 3*time.Second)
	return c
}

// quietReader is a throwaway second conn for a "nothing more arrived" check:
// readFor must be the LAST read on a conn (a deadline that expires mid-frame
// leaves the stream unusable), so a test that reads on after the check
// spends this conn on it and keeps its main one. Dial it BEFORE the flush
// under test — it sees only broadcasts sent after it registered.
func quietReader(t *testing.T, d *Daemon) *ipc.Client {
	t.Helper()
	sock, ok := broadcastSocks.Load(d)
	if !ok {
		t.Fatal("quietReader before dialBroadcastReader")
	}
	return dialBroadcastSock(t, sock.(string))
}

// readPluginErrors reads c until n plugin_error frames arrived and returns
// their payloads in arrival order.
func readPluginErrors(t *testing.T, c *ipc.Client, n int) []ipc.PluginErrorPayload {
	t.Helper()
	var got []ipc.PluginErrorPayload
	readUntil(t, c, "plugin_error frames", func(m *ipc.Message) bool {
		if m.Type != ipc.MsgPluginError {
			return false
		}
		var p ipc.PluginErrorPayload
		if err := m.DecodePayload(&p); err != nil {
			t.Fatalf("decode plugin_error: %v", err)
		}
		got = append(got, p)
		return len(got) == n
	})
	return got
}

// pluginErrorsWithin reads everything c receives within d and returns the
// plugin_error payloads among it. The last read on c.
func pluginErrorsWithin(t *testing.T, c *ipc.Client, d time.Duration) []ipc.PluginErrorPayload {
	t.Helper()
	var got []ipc.PluginErrorPayload
	for _, m := range readFor(c, d) {
		if m.Type != ipc.MsgPluginError {
			continue
		}
		var p ipc.PluginErrorPayload
		if err := m.DecodePayload(&p); err != nil {
			t.Fatalf("decode plugin_error: %v", err)
		}
		got = append(got, p)
	}
	return got
}

func TestPluginLines_SplitAcrossFlushes(t *testing.T) {
	d := newTestDaemon(t)
	p := registerFixmePlugin(t, d, "")
	pane := newFixmePane(t, d)

	cut := strings.Index(fixmeNotice, "55.1")
	if lines := d.pluginLines(pane, []byte(fixmeNotice[:cut]), 1); len(lines) != 0 {
		t.Fatalf("an unfinished line was returned: %q", lines)
	}
	lines := d.pluginLines(pane, []byte(fixmeNotice[cut:]+"\r\n"), 1)
	if len(lines) != 1 || lines[0] != fixmeNotice {
		t.Fatalf("lines = %q, want the joined notice", lines)
	}
	if v, ok := plugin.MatchUpdateNotice(p, lines[0]); !ok || v != "1.55.1" {
		t.Fatalf("MatchUpdateNotice = %q, %v; want 1.55.1 from the joined line", v, ok)
	}
	// Every cut of the whole line joins back together.
	for at := 1; at < len(fixmeNotice); at++ {
		d.pluginLines(pane, []byte(fixmeNotice[:at]), 2)
		if lines := d.pluginLines(pane, []byte(fixmeNotice[at:]+"\r\n"), 2); len(lines) != 1 || lines[0] != fixmeNotice {
			t.Fatalf("cut at %d: lines = %q", at, lines)
		}
	}
}

func TestPluginLines_ANSIStripped(t *testing.T) {
	d := newTestDaemon(t)
	registerFixmePlugin(t, d, "")
	pane := newFixmePane(t, d)

	in := "\x1b[31mFATAL\x1b[0m \x1b[1mExpired API Key\x1b[0m provided\r\n\x1b]0;title\x07plain\r\n"
	lines := d.pluginLines(pane, []byte(in), 1)
	want := []string{"FATAL Expired API Key provided", "plain"}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Fatalf("lines = %q, want %q", lines, want)
	}

	// 8-bit CSI too, both as the terminal receives it in UTF-8 (U+009B) and
	// as a bare byte; the 9B inside "ś" is a character, not an introducer.
	csi := string(rune(c1CSI))
	utf := []byte(csi + "31mFATAL" + csi + "0m Expired API Key\r\n")
	raw := append([]byte{c1CSI, '3', '1', 'm'}, "Błąd ścieżki"...)
	raw = append(raw, c1CSI, '0', 'm', '\r', '\n')
	lines = d.pluginLines(pane, append(utf, raw...), 1)
	want = []string{"FATAL Expired API Key", "Błąd ścieżki"}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Fatalf("8-bit CSI: lines = %q, want %q", lines, want)
	}
}

func TestPluginLines_GenerationResetsTail(t *testing.T) {
	d := newTestDaemon(t)
	registerFixmePlugin(t, d, "")
	pane := newFixmePane(t, d)

	d.pluginLines(pane, []byte("please update to: v1."), 1)
	lines := d.pluginLines(pane, []byte("55.1\r\n"), 2)
	if len(lines) != 1 || lines[0] != "55.1" {
		t.Fatalf("a tail joined across runs: lines = %q", lines)
	}
	// A restart also frees the run's dedupe set: the replacement child's own
	// notice is a new one.
	if !pluginSentOnce(pane, "u:1.55.1") {
		t.Fatal("fresh pane: first mark refused")
	}
	if pluginSentOnce(pane, "u:1.55.1") {
		t.Fatal("same run: second mark accepted")
	}
	d.pluginLines(pane, []byte("x\r\n"), 3)
	if !pluginSentOnce(pane, "u:1.55.1") {
		t.Fatal("new run: mark still held from the old one")
	}
}

func TestPluginLines_TailCapped(t *testing.T) {
	d := newTestDaemon(t)
	registerFixmePlugin(t, d, "")
	pane := newFixmePane(t, d)

	// Within the cap the fragment is carried.
	short := strings.Repeat("x", maxPluginLineTail-4)
	d.pluginLines(pane, []byte(short), 1)
	if lines := d.pluginLines(pane, []byte("tail\r\n"), 1); len(lines) != 1 || lines[0] != short+"tail" {
		t.Fatalf("a fragment within the cap was not carried (got %d lines, len %d)", len(lines), len(strings.Join(lines, "")))
	}
	// Over it the fragment is dropped, not truncated: the next flush starts
	// its own line.
	d.pluginLines(pane, []byte(strings.Repeat("y", maxPluginLineTail+1)), 1)
	pane.PluginMu.Lock()
	held := len(pane.pluginLineTail)
	pane.PluginMu.Unlock()
	if held != 0 {
		t.Fatalf("tail over the cap kept %d bytes", held)
	}
	if lines := d.pluginLines(pane, []byte("tail\r\n"), 1); len(lines) != 1 || lines[0] != "tail" {
		t.Fatalf("lines = %q, want just \"tail\"", lines)
	}
}

func TestApplyPluginHandlers_NoticeAndErrorBothSent(t *testing.T) {
	d := newTestDaemon(t)
	registerFixmePlugin(t, d, "")
	pane := newFixmePane(t, d)
	c := dialBroadcastReader(t, d)

	flush := []byte(fixmeNotice + "\r\nquietly noted\r\nFATAL Expired API Key provided\r\n")
	d.applyPluginHandlers(pane, pane.ID, flush, 1)

	got := readPluginErrors(t, c, 2)
	var update, errorDialog *ipc.PluginErrorPayload
	for i := range got {
		if got[i].Update != nil {
			update = &got[i]
		} else {
			errorDialog = &got[i]
		}
	}
	if update == nil || errorDialog == nil {
		t.Fatalf("frames = %+v, want one with Update and one with Fixes", got)
	}
	if update.PaneID != pane.ID || update.Plugin != "fixme" || update.Title != "Update available: Fix Me" {
		t.Errorf("update frame = %+v", *update)
	}
	if update.Update.Offered != "1.55.1" || update.Update.Installed != "" {
		t.Errorf("update info = %+v, want offered 1.55.1 and no installed version", *update.Update)
	}
	if errorDialog.PaneID != pane.ID || errorDialog.Plugin != "fixme" || errorDialog.Title != "Login expired" || errorDialog.Message != "Log in again." {
		t.Errorf("error frame = %+v", *errorDialog)
	}
	if len(errorDialog.Fixes) != 1 || errorDialog.Fixes[0] != (ipc.PluginFixInfo{Name: "login", Label: "Log in"}) {
		t.Errorf("fixes = %+v, want the login button", errorDialog.Fixes)
	}

	// The same lines again in the same run raise nothing more — a tool that
	// retries every second must not put a frame per second on the queue —
	// and the log-only handler never raised anything.
	d.applyPluginHandlers(pane, pane.ID, flush, 1)
	if again := pluginErrorsWithin(t, c, 300*time.Millisecond); len(again) != 0 {
		t.Fatalf("repeat in the same run sent %d more frames: %+v", len(again), again)
	}
}

func TestApplyPluginHandlers_NewRunRaisesAgain(t *testing.T) {
	d := newTestDaemon(t)
	registerFixmePlugin(t, d, "")
	pane := newFixmePane(t, d)
	c := dialBroadcastReader(t, d)

	line := []byte("FATAL Expired API Key provided\r\n")
	d.applyPluginHandlers(pane, pane.ID, line, 1)
	readPluginErrors(t, c, 1)
	d.applyPluginHandlers(pane, pane.ID, line, 2)
	if got := readPluginErrors(t, c, 1); got[0].Title != "Login expired" {
		t.Fatalf("second run frame = %+v", got[0])
	}
}

func TestApplyPluginHandlers_NotNowSuppressesNotice(t *testing.T) {
	d := newTestDaemon(t)
	registerFixmePlugin(t, d, "")
	pane := newFixmePane(t, d)
	c := dialBroadcastReader(t, d)
	if err := d.pluginUpdateSkips().Skip("fixme", "1.55.1"); err != nil {
		t.Fatalf("Skip: %v", err)
	}

	// The declined version with an error on the same flush: only the error
	// dialog goes out.
	d.applyPluginHandlers(pane, pane.ID, []byte(fixmeNotice+"\r\nFATAL api_key_expired\r\n"), 1)
	got := readPluginErrors(t, c, 1)
	if got[0].Update != nil {
		t.Fatalf("a declined notice was sent: %+v", got[0])
	}
	// A newer version interrupts again.
	newer := strings.Replace(fixmeNotice, "1.55.1", "1.56.0", 1)
	d.applyPluginHandlers(pane, pane.ID, []byte(newer+"\r\n"), 1)
	got = readPluginErrors(t, c, 1)
	if got[0].Update == nil || got[0].Update.Offered != "1.56.0" {
		t.Fatalf("newer notice frame = %+v", got[0])
	}
	if rest := pluginErrorsWithin(t, c, 200*time.Millisecond); len(rest) != 0 {
		t.Fatalf("unexpected extra frames: %+v", rest)
	}
}

func TestApplyPluginHandlers_UpdateTargetIsResolvedBinary(t *testing.T) {
	d := newTestDaemon(t)
	bin := filepath.Join(t.TempDir(), "stripe.cmd")
	if err := os.WriteFile(bin, []byte("@echo off\n"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	registerFixmePlugin(t, d, bin)
	pane := newFixmePane(t, d)
	c := dialBroadcastReader(t, d)

	d.applyPluginHandlers(pane, pane.ID, []byte(fixmeNotice+"\r\n"), 1)
	got := readPluginErrors(t, c, 1)
	want, err := filepath.EvalSymlinks(bin)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if got[0].Update == nil || got[0].Update.Target != want {
		t.Fatalf("target = %+v, want %q", got[0].Update, want)
	}
	// No [[update.managers]] in this plugin, so no manager owns the file and
	// the dialog asks to replace the path.
	if got[0].Update.Manager != "" {
		t.Fatalf("manager = %q, want none", got[0].Update.Manager)
	}
}

// The frame names the manager that owns the resolved binary, by the same
// match the update's argv is chosen with, so the dialog can ask "Update with
// <manager>?" instead of promising to replace a file a download never
// touches.
func TestApplyPluginHandlers_UpdateManagerNamed(t *testing.T) {
	d := newTestDaemon(t)
	bin := filepath.Join(t.TempDir(), "stripe.cmd")
	if err := os.WriteFile(bin, []byte("@echo off\n"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	toml := fixmePluginTOML + `
  [[update.managers]]
  name = "elsewhere"
  match = '/never/matches/'
  run = ["x", "y"]

  [[update.managers]]
  name = "fakemgr"
  match = '(?i)stripe\.cmd$'
  run = ["fakemgr", "upgrade", "stripe"]
`
	p := registerFixmeTOML(t, d, toml, bin)
	pane := newFixmePane(t, d)
	c := dialBroadcastReader(t, d)

	d.applyPluginHandlers(pane, pane.ID, []byte(fixmeNotice+"\r\n"), 1)
	got := readPluginErrors(t, c, 1)
	want, err := filepath.EvalSymlinks(bin)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if got[0].Update == nil || got[0].Update.Target != want || got[0].Update.Manager != "fakemgr" {
		t.Fatalf("update = %+v, want target %q owned by fakemgr", got[0].Update, want)
	}
	// And the update that follows runs that manager's command — one
	// matcher for the question and the answer.
	argv, err := d.updateArgv(p, want, "1.55.1")
	if err != nil || strings.Join(argv, " ") != "fakemgr upgrade stripe" {
		t.Fatalf("updateArgv = %q, %v; want the manager's command", argv, err)
	}
}

// Resolving the path is filesystem work under the blocking-FS budget, off
// the flush goroutine: a resolver that never answers (a PATH entry on a dead
// mount) costs one permit, not the dialog — the frame still leaves, without
// a target, once the daemon's target timeout has passed.
func TestApplyPluginHandlers_UpdateTargetTimesOut(t *testing.T) {
	d := newTestDaemon(t)
	registerFixmePlugin(t, d, "")
	pane := newFixmePane(t, d)
	c := dialBroadcastReader(t, d)

	parked := newParkedLookup()
	prevFn := resolvePluginBinaryFn
	resolvePluginBinaryFn = func(_, _ string) string {
		parked.park()
		return "/late/stripe"
	}
	// This daemon's own configuration, not a package variable.
	d.pluginUpdate.targetTimeout = 50 * time.Millisecond
	t.Cleanup(func() {
		parked.restore(t, func() { resolvePluginBinaryFn = prevFn })
	})

	started := time.Now()
	d.applyPluginHandlers(pane, pane.ID, []byte(fixmeNotice+"\r\n"), 1)
	if spent := time.Since(started); spent > defaultPluginTargetTimeout {
		t.Fatalf("the flush waited %v on the resolver", spent)
	}
	got := readPluginErrors(t, c, 1)
	if got[0].Update == nil || got[0].Update.Offered != "1.55.1" || got[0].Update.Target != "" {
		t.Fatalf("frame = %+v, want the update with no target", got[0])
	}
}

// A handler matches the chunk as flushed, newline or not: a full-screen tool
// redraws its error with a cursor move and no LF, and the last line before an
// exit has none. The notice keeps waiting for its line end, and a handler
// that matched once in a run does not match the redraw.
func TestApplyPluginHandlers_HandlerMatchesChunkWithoutLF(t *testing.T) {
	d := newTestDaemon(t)
	registerFixmePlugin(t, d, "")
	pane := newFixmePane(t, d)
	c := dialBroadcastReader(t, d)

	redraw := []byte("\x1b[5;1H\x1b[2K\x1b[31mFATAL\x1b[0m Expired API Key provided\x1b[6;1H" + fixmeNotice)
	quiet := quietReader(t, d)
	d.applyPluginHandlers(pane, pane.ID, redraw, 1)
	got := readPluginErrors(t, c, 1)
	if got[0].Title != "Login expired" || len(got[0].Fixes) != 1 {
		t.Fatalf("frame = %+v, want the login handler from a chunk with no LF", got[0])
	}
	// The redraw again, same run: nothing more. The second reader saw the
	// two flushes together: the one handler frame and no notice, since its
	// line has not ended.
	d.applyPluginHandlers(pane, pane.ID, redraw, 1)
	if all := pluginErrorsWithin(t, quiet, 300*time.Millisecond); len(all) != 1 || all[0].Update != nil {
		t.Fatalf("two redraws raised %+v, want the one handler frame and no notice before its line ended", all)
	}

	// Then the line end: the notice.
	d.applyPluginHandlers(pane, pane.ID, []byte("\r\n"), 1)
	got = readPluginErrors(t, c, 1)
	if got[0].Update == nil || got[0].Update.Offered != "1.55.1" {
		t.Fatalf("frame = %+v, want the notice once its line ended", got[0])
	}
}

// A notice printed without a trailing newline right before the tool exits is
// matched when the run ends, from the carried tail — through the PTY stream
// the daemon really reads, since the exit is what triggers it.
func TestApplyPluginHandlers_NoticeTailMatchedAtExit(t *testing.T) {
	d, pane, sessions, c := fixHarness(t)
	first := sessions.waitFor(t, 1)

	quiet := quietReader(t, d)
	d.applyPluginHandlers(pane, pane.ID, []byte(fixmeNotice), currentGen(pane))
	if early := pluginErrorsWithin(t, quiet, 200*time.Millisecond); len(early) != 0 {
		t.Fatalf("an unfinished notice was raised before the exit: %+v", early)
	}

	first.end(0)
	got := readPluginErrors(t, c, 1)
	if got[0].Update == nil || got[0].Update.Offered != "1.55.1" {
		t.Fatalf("frame = %+v, want the notice from the exit's tail", got[0])
	}
	pane.PluginMu.Lock()
	held := len(pane.pluginLineTail)
	pane.PluginMu.Unlock()
	if held != 0 {
		t.Fatalf("the tail was kept after the exit (%d bytes)", held)
	}
}

// Nothing is matched — or scraped — while a fix runs in the pane: `stripe
// login` printing the very error it fixes must not open a dialog whose
// buttons are then refused, and its banner must not overwrite the tool's own
// scraped state. The next run matches and scrapes again.
func TestApplyPluginHandlers_SkippedWhileFixRuns(t *testing.T) {
	d := newTestDaemon(t)
	// The fixme plugin with one scrape pattern, so the scrape gate is
	// observable too.
	toml := strings.Replace(fixmePluginTOML, "[persistence]\nstrategy = \"rerun\"\n",
		"[persistence]\nstrategy = \"rerun\"\n\n[[persistence.scrape]]\nname = \"version\"\npattern = 'stripe version (\\d+\\.\\d+\\.\\d+)'\n", 1)
	if toml == fixmePluginTOML {
		t.Fatal("fixme TOML lost its [persistence] block")
	}
	registerFixmeTOML(t, d, toml, fakeBinary(t, "stripe.exe"))
	pane := newFixmePane(t, d)
	sessions := installFixSessions(t)
	if err := d.spawnPane(pane, newSessionFn(80, 24), false); err != nil {
		t.Fatalf("initial spawn: %v", err)
	}
	sessions.waitFor(t, 1)
	c := dialBroadcastReader(t, d)
	fix := startLoginFix(t, pane, sessions, c, 2)
	scrapedVersion := func() string {
		pane.PluginMu.Lock()
		defer pane.PluginMu.Unlock()
		return pane.PluginState["version"]
	}

	both := []byte("stripe version 1.31.1\r\nFATAL Expired API Key provided\r\n" + fixmeNotice + "\r\n")
	quiet := quietReader(t, d)
	d.applyPluginHandlers(pane, pane.ID, both, currentGen(pane))
	if got := pluginErrorsWithin(t, quiet, 300*time.Millisecond); len(got) != 0 {
		t.Fatalf("%d dialogs raised from the fix child's output: %+v", len(got), got)
	}
	if v := scrapedVersion(); v != "" {
		t.Fatalf("version %q scraped from the fix child's output", v)
	}

	fix.end(0)
	sessions.waitFor(t, 3)
	if got := fixLabelOf(pane); got != "" {
		t.Fatalf("FixLabel = %q after the fix", got)
	}
	d.applyPluginHandlers(pane, pane.ID, both, currentGen(pane))
	if got := readPluginErrors(t, c, 2); got[0].PaneID != pane.ID || got[1].PaneID != pane.ID {
		t.Fatalf("frames after the fix = %+v", got)
	}
	if v := scrapedVersion(); v != "1.31.1" {
		t.Fatalf("version = %q after the fix, want 1.31.1 scraped from the normal run", v)
	}
}

// tooOldLine is what a Stripe CLI older than --all-snapshot prints and exits
// on — before any "newer version available" notice.
const tooOldLine = "Error: unknown flag: --all-snapshot\r\n"

// offerUpdateTOML is fixmeUpdateTOML (a full [update] table for this
// platform) plus a fix-less dialog handler with offer_update.
func offerUpdateTOML(t *testing.T) string {
	t.Helper()
	const anchor = "[[error_handlers]]\npattern = 'quietly noted'"
	handler := `[[error_handlers]]
pattern = 'unknown flag: --all-snapshot'
title = "Too old"
message = "Update installs the latest one."
action = "dialog"
offer_update = true

`
	toml := strings.Replace(fixmeUpdateTOML(), anchor, handler+anchor, 1)
	if toml == fixmeUpdateTOML() {
		t.Fatal("fixme TOML lost its log-only handler anchor")
	}
	return toml
}

// latestReleaseServer is a GitHub API stand-in answering the fixme repo's
// /releases/latest with status and tag, counting the hits, and installed as
// d's release endpoint (its own configuration, no package variable). The
// counter is atomic: httptest's goroutines write it, the test reads it.
func latestReleaseServer(t *testing.T, d *Daemon, status int, tag string) *atomic.Int32 {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/stripe/stripe-cli/releases/latest" {
			t.Errorf("unexpected GitHub request %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		w.WriteHeader(status)
		if status == http.StatusOK {
			w.Write([]byte(`{"tag_name":"` + tag + `","html_url":"x","assets":[]}`))
		}
	}))
	t.Cleanup(srv.Close)
	d.pluginUpdate.releaseBaseURL = srv.URL
	return &hits
}

// An offer_update handler on a tool too old to print its own notice: the
// handler's frame goes out at once and GitHub's latest release follows as
// the update frame, which the pane's Update request is then accepted for.
// One lookup per run however often the line repeats.
func TestApplyPluginHandlers_OfferUpdateFromLatestRelease(t *testing.T) {
	quild := fakeBinary(t, "quild.exe")
	origExe := quildExeFn
	quildExeFn = func() (string, error) { return quild, nil }
	t.Cleanup(func() { quildExeFn = origExe })

	d := newTestDaemon(t)
	hits := latestReleaseServer(t, d, http.StatusOK, "v1.55.1")
	registerFixmeTOML(t, d, offerUpdateTOML(t), fakeBinary(t, "stripe.exe"))
	pane := newFixmePane(t, d)
	sessions := installFixSessions(t)
	if err := d.spawnPane(pane, newSessionFn(80, 24), false); err != nil {
		t.Fatalf("initial spawn: %v", err)
	}
	sessions.waitFor(t, 1)
	c := dialBroadcastReader(t, d)
	quiet := quietReader(t, d)

	d.applyPluginHandlers(pane, pane.ID, []byte(tooOldLine), currentGen(pane))
	got := readPluginErrors(t, c, 2)
	var handler, offer *ipc.PluginErrorPayload
	for i := range got {
		if got[i].Update != nil {
			offer = &got[i]
		} else {
			handler = &got[i]
		}
	}
	if handler == nil || handler.Title != "Too old" || len(handler.Fixes) != 0 {
		t.Fatalf("frames = %+v, want the fix-less Too old handler", got)
	}
	if offer == nil || offer.Update.Offered != "1.55.1" || offer.Update.Installed != "" || offer.Title != "Update available: Fix Me" {
		t.Fatalf("frames = %+v, want the latest release offered with no installed version", got)
	}

	// The same line again in the same run: no second lookup, no more frames.
	d.applyPluginHandlers(pane, pane.ID, []byte(tooOldLine), currentGen(pane))
	if all := pluginErrorsWithin(t, quiet, 300*time.Millisecond); len(all) != 2 {
		t.Fatalf("the quiet reader saw %d frames across both flushes, want 2: %+v", len(all), all)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("GitHub asked %d times in one run, want 1", n)
	}

	// A NEW run (the pane restarted on the error, as a too-old CLI does in a
	// loop) is offered again — from the cache, not from GitHub: the
	// unauthenticated budget is 60 an hour, shared with Quil's own check.
	d.applyPluginHandlers(pane, pane.ID, []byte(tooOldLine), currentGen(pane)+1)
	again := readPluginErrors(t, c, 2)
	if again[0].Update == nil && again[1].Update == nil {
		t.Fatalf("new run frames = %+v, want the update offered again", again)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("GitHub asked %d times across two runs within %v, want 1 (cached)", n, latestReleaseCacheTTL)
	}
	// An entry past the TTL is asked for again.
	d.latestTags.put("stripe/stripe-cli", "1.55.1", time.Now().Add(-latestReleaseCacheTTL))
	if _, ok := d.latestTags.get("stripe/stripe-cli", time.Now()); ok {
		t.Fatal("a cache entry as old as the TTL was served")
	}

	// The offered version is the one the pane's Update is accepted for: the
	// cached offer above recorded it for the pane's current run.
	resp, _ := fixRequest(t, c, ipc.MsgPluginUpdateReq, "up-gh", ipc.PluginUpdateReqPayload{PaneID: pane.ID, Version: "1.55.1"})
	if !resp.OK {
		t.Fatalf("update to the latest release refused: %q", resp.Error)
	}
	if _, args, _ := sessions.waitFor(t, 2).argv(); !containsPair(args, "--tag", "v1.55.1") {
		t.Fatalf("update child argv %q lacks --tag v1.55.1", args)
	}
}

// An offer whose binary resolution is still running when the pane restarts
// and offers a version for its NEW run is dropped at delivery: the run
// check, the dedupe and the pluginOffered record are committed in one
// PluginMu span once the path is resolved, never before, so a slow old
// resolution can neither broadcast over the newer dialog nor leave
// pluginOffered naming the old run's version.
func TestOfferUpdate_SlowResolutionSupersededByNewRun(t *testing.T) {
	d := newTestDaemon(t)
	registerFixmePlugin(t, d, "")
	pane := newFixmePane(t, d)
	c := dialBroadcastReader(t, d)

	// The first resolution parks; every later one answers at once.
	parked, entered := parkFirstResolution(t, "/old/run/stripe", "/new/run/stripe")

	// Run 1 offers 1.55.1 and its resolution parks — waited for, since the
	// resolver runs on its own goroutine and run 2's could otherwise be the
	// first to arrive. Run 2 (a restart) offers 1.56.0, which resolves and
	// is delivered.
	setPTYGen(pane, 1)
	d.applyPluginHandlers(pane, pane.ID, []byte(fixmeNotice+"\r\n"), 1)
	waitEntered(t, entered)
	setPTYGen(pane, 2)
	newer := strings.Replace(fixmeNotice, "1.55.1", "1.56.0", 1)
	d.applyPluginHandlers(pane, pane.ID, []byte(newer+"\r\n"), 2)
	got := readPluginErrors(t, c, 1)
	if got[0].Update == nil || got[0].Update.Offered != "1.56.0" || got[0].Update.Target != "/new/run/stripe" {
		t.Fatalf("frame = %+v, want the new run's 1.56.0 offer", got[0])
	}

	// The old resolution comes back: nothing more is broadcast and the record
	// is the new run's.
	parked.release <- struct{}{}
	if late := pluginErrorsWithin(t, c, 300*time.Millisecond); len(late) != 0 {
		t.Fatalf("the superseded offer was broadcast: %+v", late)
	}
	pane.PluginMu.Lock()
	offered, oldSent := pane.pluginOffered, pane.pluginSent["u:1.55.1"]
	pane.PluginMu.Unlock()
	if offered != "1.56.0" || oldSent {
		t.Fatalf("offered=%q oldSent=%v: the superseded completion was recorded", offered, oldSent)
	}
}

// parkFirstResolution installs a resolver whose FIRST call signals entered
// and then parks until the returned parkedLookup is released, answering
// first; every later call answers rest at once. Restored at cleanup.
func parkFirstResolution(t *testing.T, first, rest string) (*parkedLookup, chan struct{}) {
	t.Helper()
	parked := newParkedLookup()
	entered := make(chan struct{}, 1)
	var calls atomic.Int32
	prevFn := resolvePluginBinaryFn
	resolvePluginBinaryFn = func(_, _ string) string {
		if calls.Add(1) == 1 {
			entered <- struct{}{}
			parked.park()
			return first
		}
		return rest
	}
	t.Cleanup(func() {
		parked.restore(t, func() { resolvePluginBinaryFn = prevFn })
	})
	return parked, entered
}

func waitEntered(t *testing.T, entered chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("the first resolution never started")
	}
}

// setPTYGen stands in for a spawn on a pane that has no PTY: the installed
// generation offerUpdate validates against.
func setPTYGen(pane *Pane, gen uint64) {
	pane.PluginMu.Lock()
	pane.ptyGen = gen
	pane.PluginMu.Unlock()
}

// assertNothingOffered reads quiet for d and checks that no update frame
// arrived and the pane records no offer and no sent version.
func assertNothingOffered(t *testing.T, quiet *ipc.Client, pane *Pane, version string) {
	t.Helper()
	if late := pluginErrorsWithin(t, quiet, 300*time.Millisecond); len(late) != 0 {
		t.Fatalf("a superseded offer was broadcast: %+v", late)
	}
	pane.PluginMu.Lock()
	offered, sent := pane.pluginOffered, pane.pluginSent["u:"+version]
	pane.PluginMu.Unlock()
	if offered != "" || sent {
		t.Fatalf("offered=%q sent=%v: the superseded completion was recorded", offered, sent)
	}
}

// A resolution still in flight when the pane is REALLY restarted — through
// restart_pane_req, with a replacement that prints nothing, so the output
// generation never moves — is dropped at delivery on the installed PTY
// generation; the in-flight mark went at the PTY boundary.
func TestOfferUpdate_SlowResolutionDroppedBySilentRestart(t *testing.T) {
	d, pane, sessions, c := fixHarness(t)
	quiet := quietReader(t, d)
	parked, entered := parkFirstResolution(t, "/old/stripe", "/new/stripe")

	d.applyPluginHandlers(pane, pane.ID, []byte(fixmeNotice+"\r\n"), currentGen(pane))
	waitEntered(t, entered)
	sendWithID(t, c, ipc.MsgRestartPaneReq, "r-silent", ipc.RestartPaneReqPayload{PaneID: pane.ID})
	got := readUntilID(t, c, ipc.MsgRestartPaneResp, "r-silent", 3*time.Second)
	var resp ipc.RestartPaneRespPayload
	if err := got[len(got)-1].DecodePayload(&resp); err != nil || !resp.Success {
		t.Fatalf("restart resp = %+v, %v", resp, err)
	}
	sessions.waitFor(t, 2)
	pane.PluginMu.Lock()
	inFlight := len(pane.pluginOffering)
	pane.PluginMu.Unlock()
	if inFlight != 0 {
		t.Fatalf("%d offers still marked in flight after the PTY was replaced", inFlight)
	}

	parked.release <- struct{}{}
	assertNothingOffered(t, quiet, pane, "1.55.1")
}

// Same window, closed by a fix starting instead: the fix child's run is
// never matched, so an offer resolved for the child it replaced must not
// open a dialog over the fix the broadcast just dismissed it for.
func TestOfferUpdate_SlowResolutionDroppedByFixStart(t *testing.T) {
	d, pane, sessions, c := fixHarness(t)
	quiet := quietReader(t, d)
	parked, entered := parkFirstResolution(t, "/old/stripe", "/new/stripe")

	d.applyPluginHandlers(pane, pane.ID, []byte(fixmeNotice+"\r\n"), currentGen(pane))
	waitEntered(t, entered)
	startLoginFix(t, pane, sessions, c, 2)

	parked.release <- struct{}{}
	assertNothingOffered(t, quiet, pane, "1.55.1")
}

// A GitHub answer that lands after the pane restarted is dropped: the run
// check, the dedupe and the pluginOffered record are one PluginMu span, so
// the old run's version is never written into the new run.
func TestOfferUpdate_StaleRunDropped(t *testing.T) {
	d := newTestDaemon(t)
	p := registerFixmeTOML(t, d, offerUpdateTOML(t), fakeBinary(t, "stripe.exe"))
	pane := newFixmePane(t, d)
	c := dialBroadcastReader(t, d)

	// The lookup for run 1 answers after run 2's child was installed.
	setPTYGen(pane, 2)
	d.offerUpdate(pane, pane.ID, newUpdateOfferSource(p), "1.55.1", 1)
	if got := pluginErrorsWithin(t, c, 300*time.Millisecond); len(got) != 0 {
		t.Fatalf("a stale run's offer was sent: %+v", got)
	}
	pane.PluginMu.Lock()
	offered, sent := pane.pluginOffered, pane.pluginSent["u:1.55.1"]
	pane.PluginMu.Unlock()
	if offered != "" || sent {
		t.Fatalf("offered=%q sent=%v: a stale run's offer was recorded into the new run", offered, sent)
	}
}

// A lookup that fails, or a latest release the user declined, leaves the
// handler's dialog as it is: Close only, no update frame.
func TestApplyPluginHandlers_OfferUpdateFailureOrDeclinedOffersNothing(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		decline bool
	}{
		{"github 500", http.StatusInternalServerError, false},
		{"declined", http.StatusOK, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newTestDaemon(t)
			hits := latestReleaseServer(t, d, tc.status, "v1.55.1")
			registerFixmeTOML(t, d, offerUpdateTOML(t), fakeBinary(t, "stripe.exe"))
			pane := newFixmePane(t, d)
			if tc.decline {
				if err := d.pluginUpdateSkips().Skip("fixme", "1.55.1"); err != nil {
					t.Fatalf("Skip: %v", err)
				}
			}
			c := dialBroadcastReader(t, d)

			d.applyPluginHandlers(pane, pane.ID, []byte(tooOldLine), 1)
			all := pluginErrorsWithin(t, c, 500*time.Millisecond)
			if len(all) != 1 || all[0].Update != nil || all[0].Title != "Too old" {
				t.Fatalf("frames = %+v, want the handler frame alone", all)
			}
			if n := hits.Load(); n != 1 {
				t.Fatalf("GitHub asked %d times, want 1", n)
			}
			pane.PluginMu.Lock()
			offered := pane.pluginOffered
			pane.PluginMu.Unlock()
			if offered != "" {
				t.Fatalf("pluginOffered = %q with nothing offered", offered)
			}
		})
	}
}

func TestApplyPluginHandlers_EmitsSidebarEvent(t *testing.T) {
	d := newTestDaemon(t)
	registerFixmePlugin(t, d, "")
	pane := newFixmePane(t, d)
	c := dialBroadcastReader(t, d)

	d.applyPluginHandlers(pane, pane.ID, []byte(fixmeNotice+"\r\nFATAL api_key_expired\r\n"), 1)
	readPluginErrors(t, c, 2)

	titles := map[string]bool{}
	for _, e := range d.events.Events() {
		if e.PaneID != pane.ID || e.Type != "plugin_attention" {
			continue
		}
		if e.Severity != "warning" || e.Data["plugin"] != "fixme" {
			t.Errorf("event = %+v, want severity warning and the plugin name", e)
		}
		titles[e.Title] = true
	}
	if !titles["Update available: Fix Me"] || !titles["Login expired"] {
		t.Fatalf("sidebar titles = %v, want both dialogs", titles)
	}

	// A muted pane keeps the dialog and loses the card, like every other
	// event from it.
	muted := newFixmePane(t, d)
	muted.PluginMu.Lock()
	muted.Muted = true
	muted.PluginMu.Unlock()
	d.applyPluginHandlers(muted, muted.ID, []byte("FATAL api_key_expired\r\n"), 1)
	if got := readPluginErrors(t, c, 1); got[0].PaneID != muted.ID {
		t.Fatalf("muted pane's dialog frame = %+v", got[0])
	}
	for _, e := range d.events.Events() {
		if e.PaneID == muted.ID {
			t.Fatalf("a muted pane got a sidebar card: %+v", e)
		}
	}
}
