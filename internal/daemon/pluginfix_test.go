package daemon

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/plugin"
	apty "github.com/artyomsv/quil/internal/pty"
)

// A plugin fix is a one-shot child in the pane, asked for by NAME over IPC.
// Driven through handleMessage with a dialed client, because the refusals
// ARE the contract — an unknown name, a second fix over a running one, a
// pane still preparing a worktree — and through the PTY stream the daemon
// really reads for the exit branch, with a child that ends when the test
// says so rather than at once.

// fixSession is a fakeSession whose child lives until end() or Close() is
// called. fakeSession.Read fails immediately, which would have every
// spawned child exit 0 — and so run the fix's exit branch — before the
// request under test arrives. Start is guarded: it runs on the daemon's
// dispatch or stream goroutine while the test reads what it recorded.
type fixSession struct {
	fakeSession
	mu   sync.Mutex
	exit chan struct{}
	code int
	once sync.Once
}

func newFixSession() *fixSession { return &fixSession{exit: make(chan struct{})} }

func (s *fixSession) Start(cmd string, args ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fakeSession.Start(cmd, args...)
}

func (s *fixSession) Read([]byte) (int, error) {
	<-s.exit
	return 0, io.EOF
}

func (s *fixSession) WaitExit() int { return s.code }

// Close is what restartPaneInPlace calls on the replaced child; it ends the
// read like a real PTY close does, so no stream goroutine outlives the test.
func (s *fixSession) Close() error {
	s.end(0)
	return nil
}

// end makes the child exit with code: Read returns EOF and the stream
// goroutine runs onPaneExitGeneration, as a real PTY would. code is written
// inside the once so WaitExit, which runs after the close, never races it.
func (s *fixSession) end(code int) {
	s.once.Do(func() {
		s.code = code
		close(s.exit)
	})
}

// argv returns what Start was given, and whether it was called.
func (s *fixSession) argv() (string, []string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startCmd, append([]string(nil), s.startArgs...), s.started
}

// fixSessions installs a newSessionFn that hands out fixSessions and keeps
// them in spawn order. Installed AFTER newTestDaemon, which sets its own.
type fixSessions struct {
	mu   sync.Mutex
	made []*fixSession
}

func installFixSessions(t *testing.T) *fixSessions {
	t.Helper()
	f := &fixSessions{}
	prev := newSessionFn
	newSessionFn = func(cols, rows int) apty.Session {
		s := newFixSession()
		f.mu.Lock()
		f.made = append(f.made, s)
		f.mu.Unlock()
		return s
	}
	// Sessions are not ended here on purpose: ending the live one after the
	// server stopped would run the exit branch — restart, snapshot — into
	// a QUIL_HOME the test is removing. Replaced sessions are closed by
	// restartPaneInPlace (Close ends the read); the one live reader a test
	// leaves parked is harmless.
	t.Cleanup(func() { newSessionFn = prev })
	return f
}

func (f *fixSessions) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.made)
}

// waitFor blocks until the n-th session exists and was started.
func (f *fixSessions) waitFor(t *testing.T, n int) *fixSession {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		f.mu.Lock()
		var s *fixSession
		if len(f.made) >= n {
			s = f.made[n-1]
		}
		f.mu.Unlock()
		if s != nil {
			if _, _, started := s.argv(); started {
				return s
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("session %d was never spawned (have %d)", n, f.count())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// fakeBinary writes an executable file called name that a PATH-style lookup
// of its absolute path accepts. Callers name it ".exe": that is executable
// by extension on Windows and by mode bit elsewhere, and — unlike ".cmd" —
// is not a batch script, which fixLaunchArgv would wrap in cmd.exe on a
// Windows host and so change every argv the fix tests assert on.
func fakeBinary(t *testing.T, name string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(bin, []byte("@echo off\n"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	return bin
}

// fixHarness is a daemon with the fixme plugin on a fake stripe binary, a
// fixme pane running its normal command on the first fixSession, and a
// dialed client.
func fixHarness(t *testing.T) (*Daemon, *Pane, *fixSessions, *ipc.Client) {
	t.Helper()
	d := newTestDaemon(t)
	registerFixmePlugin(t, d, fakeBinary(t, "stripe.exe"))
	pane := newFixmePane(t, d)
	sessions := installFixSessions(t)
	if err := d.spawnPane(pane, newSessionFn(80, 24), false); err != nil {
		t.Fatalf("initial spawn: %v", err)
	}
	sessions.waitFor(t, 1)
	c := dialBroadcastReader(t, d)
	return d, pane, sessions, c
}

// fixRequest sends typ with id and returns the typed answer, plus every
// frame read on the way to it.
func fixRequest(t *testing.T, c *ipc.Client, typ, id string, payload any) (ipc.PluginFixRespPayload, []*ipc.Message) {
	t.Helper()
	respType := ipc.MsgPluginFixResp
	if typ == ipc.MsgPluginUpdateReq {
		respType = ipc.MsgPluginUpdateResp
	}
	sendWithID(t, c, typ, id, payload)
	got := readUntilID(t, c, respType, id, 3*time.Second)
	var resp ipc.PluginFixRespPayload
	if err := got[len(got)-1].DecodePayload(&resp); err != nil {
		t.Fatalf("decode %s: %v", respType, err)
	}
	return resp, got
}

func fixLabelOf(pane *Pane) string {
	pane.PluginMu.Lock()
	defer pane.PluginMu.Unlock()
	return pane.FixLabel
}

// startLoginFix presses Log in and returns the fix's session, the n-th one
// spawned.
func startLoginFix(t *testing.T, pane *Pane, sessions *fixSessions, c *ipc.Client, n int) *fixSession {
	t.Helper()
	resp, _ := fixRequest(t, c, ipc.MsgPluginFixReq, fmt.Sprintf("fix-%d", n), ipc.PluginFixReqPayload{PaneID: pane.ID, Fix: "login"})
	if !resp.OK {
		t.Fatalf("login fix refused: %q", resp.Error)
	}
	fix := sessions.waitFor(t, n)
	// The plugin's own path override, not a PATH walk: the fix names the
	// plugin's tool.
	if cmd, args, _ := fix.argv(); !strings.HasSuffix(cmd, "stripe.exe") || !reflect.DeepEqual(args, []string{"login"}) {
		t.Fatalf("fix child = %s %q, want <path>/stripe.exe [login]", cmd, args)
	}
	if got := fixLabelOf(pane); got != "Log in" {
		t.Fatalf("FixLabel = %q while the fix runs, want \"Log in\"", got)
	}
	return fix
}

func TestPluginFixReq_UnknownFixRefused(t *testing.T) {
	_, pane, sessions, c := fixHarness(t)
	for i, name := range []string{"nope", plugin.FixNameUpdate, ""} {
		resp, _ := fixRequest(t, c, ipc.MsgPluginFixReq, fmt.Sprintf("u-%d", i), ipc.PluginFixReqPayload{PaneID: pane.ID, Fix: name})
		if resp.OK || !strings.Contains(resp.Error, "no fix named") {
			t.Fatalf("fix %q: resp = %+v, want a refusal naming the fix", name, resp)
		}
	}
	// A pane the daemon does not have is a refusal too, never silence.
	resp, _ := fixRequest(t, c, ipc.MsgPluginFixReq, "u-x", ipc.PluginFixReqPayload{PaneID: "pane-nope", Fix: "login"})
	if resp.OK || resp.Error == "" {
		t.Fatalf("unknown pane: resp = %+v", resp)
	}
	if n := sessions.count(); n != 1 {
		t.Fatalf("%d sessions spawned by refused requests, want the original only", n)
	}
	if got := fixLabelOf(pane); got != "" {
		t.Fatalf("FixLabel = %q after refusals", got)
	}
}

// The plugin file is read at the PRESS, not at the dialog: a fix removed in
// between is refused rather than run from the dialog's memory of it.
func TestPluginFixReq_FixRemovedAfterDialogRefused(t *testing.T) {
	d, pane, sessions, c := fixHarness(t)

	start := strings.Index(fixmePluginTOML, "  [[error_handlers.fixes]]")
	end := strings.Index(fixmePluginTOML, `run = ["stripe", "login"]`) + len(`run = ["stripe", "login"]`)
	if start < 0 || end < len(`run = ["stripe", "login"]`) {
		t.Fatal("fixme TOML lost its fixes block")
	}
	dir := t.TempDir()
	without := fixmePluginTOML[:start] + fixmePluginTOML[end:]
	if err := os.WriteFile(filepath.Join(dir, "fixme.toml"), []byte(without), 0o644); err != nil {
		t.Fatalf("write fixme.toml: %v", err)
	}
	if err := d.registry.LoadFromDir(dir); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, ok := d.registry.Get("fixme").FindFix("login"); ok {
		t.Fatal("the reload kept the fix; the test cannot show anything")
	}

	resp, _ := fixRequest(t, c, ipc.MsgPluginFixReq, "gone", ipc.PluginFixReqPayload{PaneID: pane.ID, Fix: "login"})
	if resp.OK || !strings.Contains(resp.Error, "no fix named") {
		t.Fatalf("resp = %+v, want a refusal", resp)
	}
	if n := sessions.count(); n != 1 {
		t.Fatalf("%d sessions, want 1", n)
	}
}

func TestPluginFixReq_SecondWhileRunningRefused(t *testing.T) {
	_, pane, sessions, c := fixHarness(t)
	startLoginFix(t, pane, sessions, c, 2)

	resp, _ := fixRequest(t, c, ipc.MsgPluginFixReq, "again", ipc.PluginFixReqPayload{PaneID: pane.ID, Fix: "login"})
	if resp.OK || !strings.Contains(resp.Error, "already running") {
		t.Fatalf("second fix: resp = %+v, want an already-running refusal", resp)
	}
	if n := sessions.count(); n != 2 {
		t.Fatalf("%d sessions, want 2 (the pane and one fix)", n)
	}
}

func TestPluginFixReq_PreparingWorktreeRefused(t *testing.T) {
	_, pane, sessions, c := fixHarness(t)
	pane.PluginMu.Lock()
	pane.PreparingWorktree = "feat-x"
	pane.PluginMu.Unlock()

	resp, _ := fixRequest(t, c, ipc.MsgPluginFixReq, "wt", ipc.PluginFixReqPayload{PaneID: pane.ID, Fix: "login"})
	if resp.OK || !strings.Contains(resp.Error, "worktree") {
		t.Fatalf("resp = %+v, want a worktree refusal", resp)
	}
	if n := sessions.count(); n != 1 {
		t.Fatalf("%d sessions, want 1", n)
	}
	if got := fixLabelOf(pane); got != "" {
		t.Fatalf("FixLabel = %q after a refusal", got)
	}
}

// An overlay is destroyed on its child's exit, so a fix there has no pane
// to come back to.
func TestPluginFixReq_OverlayRefused(t *testing.T) {
	_, pane, sessions, c := fixHarness(t)
	pane.PluginMu.Lock()
	pane.Overlay = true
	pane.PluginMu.Unlock()

	resp, _ := fixRequest(t, c, ipc.MsgPluginFixReq, "ov", ipc.PluginFixReqPayload{PaneID: pane.ID, Fix: "login"})
	if resp.OK || !strings.Contains(resp.Error, "overlay") {
		t.Fatalf("resp = %+v, want an overlay refusal", resp)
	}
	if n := sessions.count(); n != 1 {
		t.Fatalf("%d sessions, want 1", n)
	}
}

// A fix whose program is not on PATH is refused with the pane untouched —
// the lookup happens before the pane is stopped, not in the child's Start.
func TestPluginFixReq_UnlocatableCommandRefused(t *testing.T) {
	d, pane, sessions, c := fixHarness(t)
	dir := t.TempDir()
	toml := strings.Replace(fixmePluginTOML, `run = ["stripe", "login"]`, `run = ["quil-no-such-tool-7f3a", "login"]`, 1)
	if err := os.WriteFile(filepath.Join(dir, "fixme.toml"), []byte(toml), 0o644); err != nil {
		t.Fatalf("write fixme.toml: %v", err)
	}
	if err := d.registry.LoadFromDir(dir); err != nil {
		t.Fatalf("reload: %v", err)
	}

	resp, _ := fixRequest(t, c, ipc.MsgPluginFixReq, "nf", ipc.PluginFixReqPayload{PaneID: pane.ID, Fix: "login"})
	if resp.OK || !strings.Contains(resp.Error, "could not locate quil-no-such-tool-7f3a") {
		t.Fatalf("resp = %+v, want a could-not-locate refusal", resp)
	}
	if n := sessions.count(); n != 1 {
		t.Fatalf("%d sessions, want 1: the pane must not have been stopped", n)
	}
	if got := fixLabelOf(pane); got != "" {
		t.Fatalf("FixLabel = %q after a refusal", got)
	}
}

// The PATH walk is bounded: a lookup that never answers (a PATH entry on a
// dead mount) costs the request, not the dispatch goroutine, and the pane
// is not stopped for a program nobody found.
func TestPluginFixReq_LookupTimesOutRefused(t *testing.T) {
	d, pane, sessions, c := fixHarness(t)
	dir := t.TempDir()
	toml := strings.Replace(fixmePluginTOML, `run = ["stripe", "login"]`, `run = ["other-tool", "login"]`, 1)
	if err := os.WriteFile(filepath.Join(dir, "fixme.toml"), []byte(toml), 0o644); err != nil {
		t.Fatalf("write fixme.toml: %v", err)
	}
	if err := d.registry.LoadFromDir(dir); err != nil {
		t.Fatalf("reload: %v", err)
	}
	parked := newParkedLookup()
	prevLook := lookPathFn
	lookPathFn = func(string) (string, error) {
		parked.park()
		return "/late/other-tool", nil
	}
	// This daemon's own configuration, not a package variable.
	d.pluginUpdate.targetTimeout = 50 * time.Millisecond
	t.Cleanup(func() {
		parked.restore(t, func() { lookPathFn = prevLook })
	})

	started := time.Now()
	resp, _ := fixRequest(t, c, ipc.MsgPluginFixReq, "slow", ipc.PluginFixReqPayload{PaneID: pane.ID, Fix: "login"})
	if spent := time.Since(started); spent > defaultPluginTargetTimeout {
		t.Fatalf("the request waited %v on the lookup", spent)
	}
	if resp.OK || !strings.Contains(resp.Error, "could not locate") {
		t.Fatalf("resp = %+v, want a could-not-locate refusal", resp)
	}
	if n := sessions.count(); n != 1 {
		t.Fatalf("%d sessions, want 1", n)
	}
}

// A Windows batch shim (Scoop's scoop.cmd) cannot be started by
// CreateProcess, which the PTY's Spawn calls without an interpreter, so the
// fix argv is wrapped in cmd.exe; everything else, and everything off
// windows, runs as resolved. An argument cmd.exe would re-parse is refused.
func TestFixLaunchArgv(t *testing.T) {
	env := func(vars map[string]string) func(string) string {
		return func(k string) string { return vars[k] }
	}
	comspec := env(map[string]string{"ComSpec": `C:\Windows\system32\cmd.exe`, "SystemRoot": `C:\Windows`})
	for _, tc := range []struct {
		name string
		argv []string
		goos string
		env  func(string) string
		want []string
		err  string
	}{
		{"linux passes through", []string{"/usr/bin/scoop.cmd", "update"}, "linux", comspec, []string{"/usr/bin/scoop.cmd", "update"}, ""},
		{"windows exe passes through", []string{`C:\tools\stripe.exe`, "login"}, "windows", comspec, []string{`C:\tools\stripe.exe`, "login"}, ""},
		{"windows cmd wrapped", []string{`C:\Users\me\scoop\shims\scoop.cmd`, "update", "stripe"}, "windows", comspec,
			[]string{`C:\Windows\system32\cmd.exe`, "/d", "/c", `C:\Users\me\scoop\shims\scoop.cmd`, "update", "stripe"}, ""},
		{"windows BAT wrapped", []string{`C:\tools\fix.BAT`}, "windows", comspec, []string{`C:\Windows\system32\cmd.exe`, "/d", "/c", `C:\tools\fix.BAT`}, ""},
		{"no ComSpec falls back to SystemRoot", []string{`C:\t\fix.cmd`}, "windows", env(map[string]string{"SystemRoot": `C:\Windows`}),
			[]string{filepath.Join(`C:\Windows`, "System32", "cmd.exe"), "/d", "/c", `C:\t\fix.cmd`}, ""},
		{"no env at all refused", []string{`C:\t\fix.cmd`}, "windows", env(nil), nil, "cannot locate cmd.exe"},
		{"path with spaces wrapped", []string{`C:\Program Files\t\fix.cmd`, "x"}, "windows", comspec,
			[]string{`C:\Windows\system32\cmd.exe`, "/d", "/c", `C:\Program Files\t\fix.cmd`, "x"}, ""},
		{"path with metacharacter refused", []string{`C:\t&u\fix.cmd`}, "windows", comspec, nil, "re-parse"},
		{"metacharacter refused", []string{`C:\t\fix.cmd`, "a&b"}, "windows", comspec, nil, "re-parse"},
		{"percent refused", []string{`C:\t\fix.cmd`, "%PATH%"}, "windows", comspec, nil, "re-parse"},
		{"quote refused", []string{`C:\t\fix.cmd`, `say "hi"`}, "windows", comspec, nil, "re-parse"},
		{"whitespace refused", []string{`C:\t\fix.cmd`, "two words"}, "windows", comspec, nil, "re-parse"},
		{"newline refused", []string{`C:\t\fix.cmd`, "a\nb"}, "windows", comspec, nil, "re-parse"},
		{"metacharacter fine for an exe", []string{`C:\t\fix.exe`, "a&b"}, "windows", comspec, []string{`C:\t\fix.exe`, "a&b"}, ""},
	} {
		got, err := fixLaunchArgv(tc.argv, tc.goos, tc.env)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%s: err = %v, want one mentioning %q", tc.name, err, tc.err)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: argv = %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
}

// A restart and a fix request on two connections cannot interleave: the
// restart's guard and its restart are one lifecycleMu span, so a fix that
// arrives while the restart is between the two waits, then starts, and its
// child is the one left running — never replaced by a restart that read "no
// fix" a moment earlier.
func TestRestartPaneReq_SerializedWithFix(t *testing.T) {
	d, pane, sessions, c := fixHarness(t)
	c2 := quietReader(t, d)

	entered := make(chan struct{}, 1)
	resume := make(chan struct{})
	prev := restartGuardPassedFn
	restartGuardPassedFn = func(p *Pane) {
		if p.ID == pane.ID {
			entered <- struct{}{}
			<-resume
		}
	}
	t.Cleanup(func() { restartGuardPassedFn = prev })

	// The restart passes its guard and parks, holding the pane's lifecycle.
	sendWithID(t, c, ipc.MsgRestartPaneReq, "r-park", ipc.RestartPaneReqPayload{PaneID: pane.ID})
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("the restart never reached its guard")
	}
	// The fix on another connection must wait for it.
	sendWithID(t, c2, ipc.MsgPluginFixReq, "f-wait", ipc.PluginFixReqPayload{PaneID: pane.ID, Fix: "login"})
	time.Sleep(200 * time.Millisecond)
	if got := fixLabelOf(pane); got != "" {
		t.Fatalf("FixLabel = %q while a restart held the pane: the fix slipped in", got)
	}
	if n := sessions.count(); n != 1 {
		t.Fatalf("%d sessions while the restart was parked, want 1", n)
	}

	close(resume)
	got := readUntilID(t, c, ipc.MsgRestartPaneResp, "r-park", 3*time.Second)
	var rresp ipc.RestartPaneRespPayload
	if err := got[len(got)-1].DecodePayload(&rresp); err != nil || !rresp.Success {
		t.Fatalf("restart resp = %+v, %v; want success", rresp, err)
	}
	got = readUntilID(t, c2, ipc.MsgPluginFixResp, "f-wait", 3*time.Second)
	var fresp ipc.PluginFixRespPayload
	if err := got[len(got)-1].DecodePayload(&fresp); err != nil || !fresp.OK {
		t.Fatalf("fix resp = %+v, %v; want OK after the restart", fresp, err)
	}
	// Restart first (the normal command), then the fix — and the fix child
	// is the one running now.
	if _, args, _ := sessions.waitFor(t, 2).argv(); !reflect.DeepEqual(args, []string{"listen"}) {
		t.Fatalf("second child ran %q, want the restart's [listen]", args)
	}
	if _, args, _ := sessions.waitFor(t, 3).argv(); !reflect.DeepEqual(args, []string{"login"}) {
		t.Fatalf("third child ran %q, want the fix's [login]", args)
	}
	if n := sessions.count(); n != 3 {
		t.Fatalf("%d sessions, want 3: nothing may replace the fix child", n)
	}
	if got := fixLabelOf(pane); got != "Log in" {
		t.Fatalf("FixLabel = %q, want the fix running", got)
	}
}

// Alt+R and the MCP restart_pane tool both reach handleRestartPaneReq; a
// fix in progress refuses them the way a worktree placeholder does.
func TestRestartPaneReq_RefusedWhileFixRuns(t *testing.T) {
	_, pane, sessions, c := fixHarness(t)
	startLoginFix(t, pane, sessions, c, 2)

	sendWithID(t, c, ipc.MsgRestartPaneReq, "r1", ipc.RestartPaneReqPayload{PaneID: pane.ID})
	got := readUntilID(t, c, ipc.MsgRestartPaneResp, "r1", 3*time.Second)
	var resp ipc.RestartPaneRespPayload
	if err := got[len(got)-1].DecodePayload(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Success {
		t.Fatal("restart_pane_req succeeded over a running fix")
	}
	if n := sessions.count(); n != 2 {
		t.Fatalf("%d sessions, want 2: the restart must spawn nothing", n)
	}
	if got := fixLabelOf(pane); got != "Log in" {
		t.Fatalf("FixLabel = %q, the refused restart must leave the fix running", got)
	}
}

// A zero exit of a then = restart fix starts the pane's NORMAL command: the
// one-shot argv was consumed by the fix's own spawn, so the restart reads
// the plugin's args, not the fix's.
func TestFixExit_ZeroRestartsNormalArgv(t *testing.T) {
	_, pane, sessions, c := fixHarness(t)
	fix := startLoginFix(t, pane, sessions, c, 2)

	fix.end(0)
	next := sessions.waitFor(t, 3)
	if _, args, _ := next.argv(); !reflect.DeepEqual(args, []string{"listen"}) {
		t.Fatalf("restart after the fix ran %q, want the plugin's [listen]", args)
	}
	if got := fixLabelOf(pane); got != "" {
		t.Fatalf("FixLabel = %q after the fix exited", got)
	}
	// Still a one-shot: the pane's persisted args are untouched.
	pane.PluginMu.Lock()
	instArgs := pane.InstanceArgs
	pane.PluginMu.Unlock()
	if len(instArgs) != 0 {
		t.Fatalf("InstanceArgs = %q, the fix must not persist", instArgs)
	}
}

// A non-zero exit leaves the pane on the fix's output with one status line
// and no restart; it is not a pane death, so no process_exit card.
func TestFixExit_NonZeroStaysNoProcessExitEvent(t *testing.T) {
	_, pane, sessions, c := fixHarness(t)
	fix := startLoginFix(t, pane, sessions, c, 2)

	fix.end(2)
	want := "fix failed (exit 2) — Alt+R restarts"
	frames := readUntil(t, c, "the fix status line", func(m *ipc.Message) bool {
		if m.Type != ipc.MsgPaneOutput {
			return false
		}
		var p ipc.PaneOutputPayload
		return m.DecodePayload(&p) == nil && p.PaneID == pane.ID && strings.Contains(string(p.Data), want)
	})
	frames = append(frames, readFor(c, 300*time.Millisecond)...)
	for _, m := range frames {
		if m.Type != ipc.MsgPaneEvent {
			continue
		}
		var e ipc.PaneEventPayload
		if err := m.DecodePayload(&e); err == nil && e.Type == "process_exit" {
			t.Fatalf("a process_exit card was emitted for the fix: %+v", e)
		}
	}
	if n := sessions.count(); n != 2 {
		t.Fatalf("%d sessions, want 2: a failed fix must not restart the pane", n)
	}
	if got := fixLabelOf(pane); got != "" {
		t.Fatalf("FixLabel = %q after the fix failed", got)
	}
	pane.PluginMu.Lock()
	code := pane.ExitCode
	pane.PluginMu.Unlock()
	if code == nil || *code != 2 {
		t.Fatalf("ExitCode = %v, want 2 so Alt+R has a dead pane to restart", code)
	}
	if !strings.Contains(string(pane.OutputBuf.Bytes()), want) {
		t.Fatal("the status line is not in OutputBuf; a reconnect would not replay it")
	}
}

// The normal child's exit landing between startFix's marker and the restart
// that follows is the PANE's exit — card and all — not the fix's: the
// claim is by generation, not by marker.
func TestFixExit_OldGenerationIsNotTheFix(t *testing.T) {
	_, pane, sessions, c := fixHarness(t)
	first := sessions.waitFor(t, 1)
	// The window: marker and argv set, restart not yet run.
	pane.PluginMu.Lock()
	pane.FixLabel, pane.fixArgv, pane.fixThen = "Log in", []string{"stripe", "login"}, plugin.FixThenRestart
	pane.PluginMu.Unlock()

	first.end(0)
	readUntil(t, c, "the pane's own process_exit card", func(m *ipc.Message) bool {
		if m.Type != ipc.MsgPaneEvent {
			return false
		}
		var e ipc.PaneEventPayload
		return m.DecodePayload(&e) == nil && e.PaneID == pane.ID && e.Type == "process_exit"
	})
	if n := sessions.count(); n != 1 {
		t.Fatalf("%d sessions: the old child's exit must not be treated as a fix succeeding", n)
	}
	if got := fixLabelOf(pane); got != "Log in" {
		t.Fatalf("FixLabel = %q: the old child's exit cleared the marker", got)
	}
	if d := (&Daemon{}); d.onFixExit(pane, 0, 1) {
		t.Fatal("onFixExit claimed an exit of a generation that was never the fix's")
	}
}

// Same window on a hand-converted pane: the agent's clean exit would return
// the pane to its shell through restartPaneInPlace, which would consume the
// armed fix argv. While a fix is armed the conversion branch stands aside.
func TestFixArmed_ShellReturnStandsAside(t *testing.T) {
	_, pane, sessions, c := fixHarness(t)
	first := sessions.waitFor(t, 1)
	pane.PluginMu.Lock()
	pane.ConvertedFromTerminal = "terminal"
	pane.FixLabel, pane.fixArgv, pane.fixThen = "Log in", []string{"stripe", "login"}, plugin.FixThenRestart
	pane.PluginMu.Unlock()

	first.end(0)
	readUntil(t, c, "the pane's own process_exit card", func(m *ipc.Message) bool {
		if m.Type != ipc.MsgPaneEvent {
			return false
		}
		var e ipc.PaneEventPayload
		return m.DecodePayload(&e) == nil && e.PaneID == pane.ID && e.Type == "process_exit"
	})
	if n := sessions.count(); n != 1 {
		t.Fatalf("%d sessions: the shell return consumed the armed fix", n)
	}
	pane.PluginMu.Lock()
	converted, argv, label := pane.ConvertedFromTerminal, pane.fixArgv, pane.FixLabel
	pane.PluginMu.Unlock()
	if converted != "terminal" || len(argv) == 0 || label != "Log in" {
		t.Fatalf("converted=%q argv=%q label=%q: the armed fix must be left for startFix's restart", converted, argv, label)
	}
}

// A deferred pane is spawned before the fix is armed: the lazy spawn must
// not consume the fix argv, or the restart that follows would replace the
// fix with the normal command and leave the marker on forever.
func TestPluginFixReq_PendingPaneRunsFixOnce(t *testing.T) {
	d := newTestDaemon(t)
	registerFixmePlugin(t, d, fakeBinary(t, "stripe.exe"))
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	pane.PluginMu.Lock()
	pane.Type = "fixme"
	pane.PluginMu.Unlock()
	pane.Pending.Store(true)
	sessions := installFixSessions(t)
	c := dialBroadcastReader(t, d)

	resp, _ := fixRequest(t, c, ipc.MsgPluginFixReq, "pend", ipc.PluginFixReqPayload{PaneID: pane.ID, Fix: "login"})
	if !resp.OK {
		t.Fatalf("login fix refused on a pending pane: %q", resp.Error)
	}
	// The lazy spawn ran the normal command; the fix came second.
	if _, args, _ := sessions.waitFor(t, 1).argv(); !reflect.DeepEqual(args, []string{"listen"}) {
		t.Fatalf("lazy spawn ran %q, want [listen]", args)
	}
	fix := sessions.waitFor(t, 2)
	if _, args, _ := fix.argv(); !reflect.DeepEqual(args, []string{"login"}) {
		t.Fatalf("second child ran %q, want the fix [login]", args)
	}
	if got := fixLabelOf(pane); got != "Log in" {
		t.Fatalf("FixLabel = %q while the fix runs", got)
	}
	if pane.Pending.Load() {
		t.Fatal("pane still Pending with a fix running")
	}
	fix.end(0)
	if _, args, _ := sessions.waitFor(t, 3).argv(); !reflect.DeepEqual(args, []string{"listen"}) {
		t.Fatalf("restart after the fix ran %q, want [listen]", args)
	}
	if got := fixLabelOf(pane); got != "" {
		t.Fatalf("FixLabel = %q after the fix: the marker stuck", got)
	}
}

// The marker travels in the broadcast and never in the disk snapshot: a
// daemon that dies mid-fix restores the pane through its normal strategy.
func TestFixMarker_NotInSnapshot(t *testing.T) {
	d := newTestDaemon(t)
	registerFixmePlugin(t, d, "")
	pane := newFixmePane(t, d)
	pane.PluginMu.Lock()
	pane.FixLabel = "Log in"
	pane.PluginMu.Unlock()

	find := func(st ipc.WorkspaceState) *ipc.PaneState {
		for i := range st.Panes {
			if st.Panes[i].ID == pane.ID {
				return &st.Panes[i]
			}
		}
		t.Fatalf("pane %s missing from the state", pane.ID)
		return nil
	}
	if got := find(d.workspaceStateFromView(d.session.SnapshotView(), true)).FixLabel; got != "Log in" {
		t.Fatalf("broadcast fix_label = %q, want \"Log in\"", got)
	}
	if got := find(d.workspaceStateFromView(d.session.SnapshotView(), false)).FixLabel; got != "" {
		t.Fatalf("snapshot fix_label = %q, want it absent", got)
	}
}

// offerUpdate has the daemon raise the fixme notice for pane's current run
// and consumes the frame: the version it offered is the only one a client's
// Update or Not now is then accepted for.
func offerUpdate(t *testing.T, d *Daemon, pane *Pane, c *ipc.Client) {
	t.Helper()
	d.applyPluginHandlers(pane, pane.ID, []byte(fixmeNotice+"\r\n"), currentGen(pane))
	if got := readPluginErrors(t, c, 1); got[0].Update == nil || got[0].Update.Offered != "1.55.1" {
		t.Fatalf("offer frame = %+v, want the 1.55.1 notice", got[0])
	}
}

// Not now records the version the daemon offered for the pane, and nothing
// else: an act-class client that could decline "999.0.0" would hide every
// future notice from everyone.
func TestPluginFixReq_NotNowRecordsSkip(t *testing.T) {
	d, pane, sessions, c := fixHarness(t)

	resp, _ := fixRequest(t, c, ipc.MsgPluginFixReq, "nn-0", ipc.PluginFixReqPayload{PaneID: pane.ID, Fix: ipc.PluginFixNotNow})
	if resp.OK {
		t.Fatal("not_now without a version was accepted")
	}
	// Well-formed, but nothing was offered yet.
	resp, _ = fixRequest(t, c, ipc.MsgPluginFixReq, "nn-pre", ipc.PluginFixReqPayload{PaneID: pane.ID, Fix: ipc.PluginFixNotNow, Version: "v1.55.1"})
	if resp.OK || !strings.Contains(resp.Error, "no update was offered") {
		t.Fatalf("not_now before any notice: resp = %+v, want a no-offer refusal", resp)
	}
	offerUpdate(t, d, pane, c)
	// Another version than the offered one, however plausible.
	resp, _ = fixRequest(t, c, ipc.MsgPluginFixReq, "nn-other", ipc.PluginFixReqPayload{PaneID: pane.ID, Fix: ipc.PluginFixNotNow, Version: "999.0.0"})
	if resp.OK || !strings.Contains(resp.Error, "not the version offered") {
		t.Fatalf("not_now for an unoffered version: resp = %+v, want a refusal naming the offer", resp)
	}
	if d.pluginUpdateSkips().Suppressed("fixme", "1.55.1") {
		t.Fatal("a refused Not now recorded a skip")
	}
	resp, _ = fixRequest(t, c, ipc.MsgPluginFixReq, "nn-1", ipc.PluginFixReqPayload{PaneID: pane.ID, Fix: ipc.PluginFixNotNow, Version: "v1.55.1"})
	if !resp.OK {
		t.Fatalf("not_now refused: %q", resp.Error)
	}
	skips := d.pluginUpdateSkips()
	if !skips.Suppressed("fixme", "1.55.1") || !skips.Suppressed("fixme", "1.54.0") {
		t.Fatal("the declined version (and older) is not suppressed")
	}
	if skips.Suppressed("fixme", "1.56.0") {
		t.Fatal("a newer version is suppressed too")
	}
	if n := sessions.count(); n != 1 {
		t.Fatalf("%d sessions, want 1: Not now runs nothing", n)
	}
	if got := fixLabelOf(pane); got != "" {
		t.Fatalf("FixLabel = %q after Not now", got)
	}
}

// A version is a client's string that becomes a tag and an asset name on an
// argv: anything that is not version-shaped is refused before the pane is
// touched, by both request types.
func TestPluginVersion_ShapeRefused(t *testing.T) {
	_, pane, sessions, c := fixHarness(t)
	for i, bad := range []string{"1.2.3; rm -rf /", "../x", "1.2.3 --flag", "latest", "v", " 1.2.3"} {
		resp, _ := fixRequest(t, c, ipc.MsgPluginUpdateReq, fmt.Sprintf("bv-u-%d", i), ipc.PluginUpdateReqPayload{PaneID: pane.ID, Version: bad})
		if resp.OK || !strings.Contains(resp.Error, "not a version") && !strings.Contains(resp.Error, "no version") {
			t.Fatalf("update %q: resp = %+v, want a version refusal", bad, resp)
		}
		resp, _ = fixRequest(t, c, ipc.MsgPluginFixReq, fmt.Sprintf("bv-n-%d", i), ipc.PluginFixReqPayload{PaneID: pane.ID, Fix: ipc.PluginFixNotNow, Version: bad})
		if resp.OK {
			t.Fatalf("not_now %q was accepted", bad)
		}
	}
	for _, good := range []string{"1.55.1", "v1.55.1", "2026.10.23", "1.2.3-rc.1", "1.2.3+build.7"} {
		if _, err := pluginVersionArg(good); err != nil {
			t.Errorf("%q refused: %v", good, err)
		}
	}
	if n := sessions.count(); n != 1 {
		t.Fatalf("%d sessions, want 1", n)
	}
}

// updatePlugin is a stripe-shaped plugin with a full [update] table for
// THIS platform, compiled as a loaded plugin would be.
func updatePlugin(t *testing.T, managers ...plugin.UpdateManager) *plugin.PanePlugin {
	t.Helper()
	p := &plugin.PanePlugin{
		Name:        "fixme",
		DisplayName: "Fix Me",
		Command:     plugin.CommandConfig{Cmd: "stripe", DetectCmd: "stripe --version"},
		Update: &plugin.UpdateSpec{
			Notice:    `please update to: v?(\d+\.\d+\.\d+)`,
			GitHub:    "stripe/stripe-cli",
			Binary:    "stripe",
			Assets:    map[string]string{runtime.GOOS + "_" + runtime.GOARCH: "stripe_{version}_test.zip"},
			Checksums: map[string]string{runtime.GOOS: "stripe-checksums.txt"},
			Managers:  managers,
		},
	}
	if err := p.Update.Compile(); err != nil {
		t.Fatalf("compile: %v", err)
	}
	return p
}

func TestUpdateArgv_ManagerMatch(t *testing.T) {
	d := &Daemon{}
	brew := plugin.UpdateManager{Name: "brew", Match: `/Cellar/|/homebrew/`, Run: []string{"brew", "upgrade", "stripe/stripe-cli/stripe"}}
	scoop := plugin.UpdateManager{Name: "scoop", Match: `(?i)\\scoop\\`, Run: []string{"scoop", "update", "stripe"}}
	p := updatePlugin(t, brew, scoop)

	got, err := d.updateArgv(p, "/opt/homebrew/Cellar/stripe/1.31.1/bin/stripe", "1.55.1")
	if err != nil || !reflect.DeepEqual(got, brew.Run) {
		t.Fatalf("brew path: argv = %q, %v; want %q", got, err, brew.Run)
	}
	got, err = d.updateArgv(p, `C:\Users\me\scoop\apps\stripe\current\stripe.exe`, "1.55.1")
	if err != nil || !reflect.DeepEqual(got, scoop.Run) {
		t.Fatalf("scoop path: argv = %q, %v; want %q", got, err, scoop.Run)
	}
	// The manager's argv is a copy: the plugin's own slice is never handed
	// to a spawn that may append to it.
	got[0] = "changed"
	if scoop.Run[0] != "scoop" {
		t.Fatal("updateArgv returned the manager's own slice")
	}
}

func TestUpdateArgv_ManualFlags(t *testing.T) {
	origExe := quildExeFn
	quildExeFn = func() (string, error) { return "/opt/quil/quild", nil }
	t.Cleanup(func() { quildExeFn = origExe })

	d := &Daemon{}
	p := updatePlugin(t, plugin.UpdateManager{Name: "brew", Match: `/Cellar/`, Run: []string{"brew", "upgrade", "stripe"}})
	got, err := d.updateArgv(p, "/usr/local/bin/stripe", "v1.55.1")
	if err != nil {
		t.Fatalf("updateArgv: %v", err)
	}
	want := []string{"/opt/quil/quild", "plugin-update",
		"--target", "/usr/local/bin/stripe",
		"--repo", "stripe/stripe-cli",
		"--tag", "v1.55.1",
		"--asset", "stripe_1.55.1_test.zip",
		"--checksums", "stripe-checksums.txt",
		"--binary", "stripe",
		"--detect", "stripe --version",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv =\n%q\nwant\n%q", got, want)
	}
	// No detect command, no --detect flag.
	p.Command.DetectCmd = ""
	got, err = d.updateArgv(p, "/usr/local/bin/stripe", "1.55.1")
	if err != nil || len(got) != len(want)-2 {
		t.Fatalf("without detect: argv = %q, %v", got, err)
	}
}

func TestUpdateArgv_NoAssetForPlatform(t *testing.T) {
	d := &Daemon{}
	p := updatePlugin(t)
	p.Update.Assets = map[string]string{"plan9_mips": "stripe_{version}_plan9.tgz"}
	if _, err := d.updateArgv(p, "/usr/local/bin/stripe", "1.55.1"); err == nil || !strings.Contains(err.Error(), runtime.GOOS+"_"+runtime.GOARCH) {
		t.Fatalf("err = %v, want a refusal naming this platform", err)
	}
	p = updatePlugin(t)
	p.Update.Checksums = nil
	if _, err := d.updateArgv(p, "/usr/local/bin/stripe", "1.55.1"); err == nil || !strings.Contains(err.Error(), "checksums") {
		t.Fatalf("err = %v, want a checksums refusal", err)
	}
	p.Update = nil
	if _, err := d.updateArgv(p, "/usr/local/bin/stripe", "1.55.1"); err == nil {
		t.Fatal("a plugin with no [update] produced an argv")
	}
	// A notice-only [update] table (no download fields, no manager match)
	// is refused before anything runs, like a missing platform.
	p = updatePlugin(t)
	p.Update.GitHub, p.Update.Binary = "", ""
	if _, err := d.updateArgv(p, "/usr/local/bin/stripe", "1.55.1"); err == nil {
		t.Fatal("a notice-only [update] produced an argv")
	}
}

// fixmeUpdateTOML is fixmePluginTOML with a full [update] table for this
// platform, so the request-level update path can run end to end.
func fixmeUpdateTOML() string {
	platform := runtime.GOOS + "_" + runtime.GOARCH
	return fixmePluginTOML + fmt.Sprintf(`github = "stripe/stripe-cli"
binary = "stripe"
assets = { %s = "stripe_{version}_test.zip" }
checksums = { %s = "stripe-checksums.txt" }

  [[update.managers]]
  name = "brew"
  match = '/Cellar/'
  run = ["brew", "upgrade", "stripe"]
`, platform, runtime.GOOS)
}

// registerFixmeUpdatePlugin loads fixmeUpdateTOML with bin as the binary.
func registerFixmeUpdatePlugin(t *testing.T, d *Daemon, bin string) {
	t.Helper()
	dir := t.TempDir()
	toml := strings.Replace(fixmeUpdateTOML(), "#PATH#", "path = '"+filepath.ToSlash(bin)+"'", 1)
	if err := os.WriteFile(filepath.Join(dir, "fixme.toml"), []byte(toml), 0o644); err != nil {
		t.Fatalf("write fixme.toml: %v", err)
	}
	if err := d.registry.LoadFromDir(dir); err != nil {
		t.Fatalf("load test plugins: %v", err)
	}
	if d.registry.Get("fixme") == nil {
		t.Fatal("fixme plugin did not load")
	}
}

// The manual path: the pane's child becomes this daemon's own quild running
// plugin-update against the resolved binary, with the OFFERED version's tag.
func TestPluginUpdateReq_ManualRunsQuild(t *testing.T) {
	quild := fakeBinary(t, "quild.exe")
	origExe := quildExeFn
	quildExeFn = func() (string, error) { return quild, nil }
	t.Cleanup(func() { quildExeFn = origExe })

	d := newTestDaemon(t)
	bin := fakeBinary(t, "stripe.exe")
	registerFixmeUpdatePlugin(t, d, bin)
	pane := newFixmePane(t, d)
	sessions := installFixSessions(t)
	if err := d.spawnPane(pane, newSessionFn(80, 24), false); err != nil {
		t.Fatalf("initial spawn: %v", err)
	}
	c := dialBroadcastReader(t, d)

	// Only the offered version: the dialog cannot say one version and ask
	// for another, and a client cannot ask for one the tool never named.
	resp, _ := fixRequest(t, c, ipc.MsgPluginUpdateReq, "up-pre", ipc.PluginUpdateReqPayload{PaneID: pane.ID, Version: "1.55.1"})
	if resp.OK || !strings.Contains(resp.Error, "no update was offered") {
		t.Fatalf("update before any notice: resp = %+v, want a no-offer refusal", resp)
	}
	offerUpdate(t, d, pane, c)
	resp, _ = fixRequest(t, c, ipc.MsgPluginUpdateReq, "up-other", ipc.PluginUpdateReqPayload{PaneID: pane.ID, Version: "1.54.0"})
	if resp.OK || !strings.Contains(resp.Error, "not the version offered") {
		t.Fatalf("update to an unoffered version: resp = %+v, want a refusal", resp)
	}
	if n := sessions.count(); n != 1 {
		t.Fatalf("%d sessions after the refusals, want 1", n)
	}

	resp, _ = fixRequest(t, c, ipc.MsgPluginUpdateReq, "up-1", ipc.PluginUpdateReqPayload{PaneID: pane.ID, Version: "1.55.1"})
	if !resp.OK {
		t.Fatalf("update refused: %q", resp.Error)
	}
	fix := sessions.waitFor(t, 2)
	cmd, args, _ := fix.argv()
	resolved, err := filepath.EvalSymlinks(bin)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if cmd != quild || len(args) == 0 || args[0] != "plugin-update" {
		t.Fatalf("update child = %s %q, want %s plugin-update", cmd, args, quild)
	}
	for _, pair := range [][2]string{{"--target", resolved}, {"--tag", "v1.55.1"}, {"--asset", "stripe_1.55.1_test.zip"}, {"--repo", "stripe/stripe-cli"}} {
		if !containsPair(args, pair[0], pair[1]) {
			t.Errorf("argv %q lacks %s %s", args, pair[0], pair[1])
		}
	}
	if got := fixLabelOf(pane); got != "Update to 1.55.1" {
		t.Fatalf("FixLabel = %q", got)
	}
	// Admin-class at the daemon, so a second press while it runs is the
	// same already-running refusal a handler fix gets.
	resp, _ = fixRequest(t, c, ipc.MsgPluginUpdateReq, "up-2", ipc.PluginUpdateReqPayload{PaneID: pane.ID, Version: "1.55.1"})
	if resp.OK || !strings.Contains(resp.Error, "already running") {
		t.Fatalf("second update: resp = %+v", resp)
	}
}

// An update the daemon cannot describe — here a notice-only [update] with
// no manager match — is refused before the pane is touched.
func TestPluginUpdateReq_RefusedBeforeStop(t *testing.T) {
	d, pane, sessions, c := fixHarness(t)
	offerUpdate(t, d, pane, c)

	resp, _ := fixRequest(t, c, ipc.MsgPluginUpdateReq, "up-0", ipc.PluginUpdateReqPayload{PaneID: pane.ID, Version: "1.55.1"})
	if resp.OK || !strings.Contains(resp.Error, "no download") {
		t.Fatalf("resp = %+v, want the notice-only refusal", resp)
	}
	resp, _ = fixRequest(t, c, ipc.MsgPluginUpdateReq, "up-v", ipc.PluginUpdateReqPayload{PaneID: pane.ID})
	if resp.OK {
		t.Fatal("an update with no version was accepted")
	}
	if n := sessions.count(); n != 1 {
		t.Fatalf("%d sessions, want 1: the pane must not have been stopped", n)
	}
	if got := fixLabelOf(pane); got != "" {
		t.Fatalf("FixLabel = %q after a refusal", got)
	}
}
