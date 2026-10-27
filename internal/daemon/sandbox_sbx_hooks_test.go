package daemon

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/claudehook"
	"github.com/artyomsv/quil/internal/config"
)

// fakeLinuxQuild stands in for the fetched hook binary: a file the daemon
// is pointed at through QUIL_SANDBOX_QUILD, as a dev build is.
func fakeLinuxQuild(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "quild-linux-arm64")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(sandboxQuildEnv, p)
	return p
}

// A claude pane in an sbx sandbox gets hooks: the shared tree and the hook
// binary's folder are mounted at their host paths, the settings file is
// written into the tree, and the exec names it first, with the hook
// environment.
func TestSpawnPane_SbxClaudeGetsHooks(t *testing.T) {
	d, pane, r := sbxResumeFixture(t)
	quild := fakeLinuxQuild(t)
	s := attachRun(t, d, pane)
	if s == nil {
		t.Fatalf("the pane was not started; error = %q", pane.SpawnError)
	}
	tree := sbxHookTree(config.QuilDir(), "team-box")
	if !r.ran("/fake/sbx", "exec", "team-box", "uname", "-m") {
		t.Errorf("the VM was not asked its architecture: %v", r.calls)
	}
	if !r.ran("/fake/sbx", "mount", "team-box", tree) {
		t.Errorf("the hook tree was not shared: %v", r.calls)
	}
	binDir := filepath.Dir(quild)
	if !r.ran("/fake/sbx", "mount", "team-box", binDir+":"+binDir+":ro") {
		t.Errorf("the hook binary's folder was not shared read-only: %v", r.calls)
	}
	cmd := d.registry.Get("claude-code").Command.Cmd
	i := slices.Index(s.startArgs, cmd)
	if i < 0 || len(s.startArgs) < i+3 || s.startArgs[i+1] != "--settings" {
		t.Fatalf("exec args = %v, want --settings right after %s", s.startArgs, cmd)
	}
	settings := s.startArgs[i+2]
	if !strings.HasPrefix(settings, tree+string(os.PathSeparator)) {
		t.Errorf("settings = %q, want a file inside the shared tree %s", settings, tree)
	}
	body, err := os.ReadFile(settings)
	if err != nil {
		t.Fatalf("the settings file was not written: %v", err)
	}
	// JSON-escaped in the file, so the path and the subcommand are checked,
	// not the exact shell spelling.
	if !strings.Contains(string(body), quild) || !strings.Contains(string(body), "claude-hook") {
		t.Errorf("settings do not name the hook binary at its shared path:\n%s", body)
	}
	for _, want := range []string{"QUIL_HOOK_HOME=" + tree, "QUIL_PANE_ID=" + pane.ID} {
		if !slices.Contains(s.startArgs, want) {
			t.Errorf("exec args lack -e %s: %v", want, s.startArgs)
		}
	}
	if h, ok := d.sbxHooksFor(pane.ID); !ok || h.tree != tree || h.quild != quild {
		t.Errorf("hooks recorded for the pane = %+v, %v", h, ok)
	}
}

// No hook binary (a dev build with nothing to fetch) is not a refusal: the
// pane starts hookless and is told why.
func TestSpawnPane_SbxWithoutAHookBinaryStartsHookless(t *testing.T) {
	d, pane, r := sbxResumeFixture(t)
	s := attachRun(t, d, pane)
	if s == nil {
		t.Fatalf("the pane was not started; error = %q", pane.SpawnError)
	}
	if slices.Contains(s.startArgs, "--settings") {
		t.Errorf("a hookless pane was handed --settings: %v", s.startArgs)
	}
	if r.ran("/fake/sbx", "mount") {
		t.Errorf("something was mounted with no hook binary to run: %v", r.calls)
	}
	if out := string(pane.OutputBuf.Bytes()); !strings.Contains(out, "hooks are not available") {
		t.Errorf("the pane was not told hooks are off:\n%s", out)
	}
	if _, ok := d.sbxHooksFor(pane.ID); ok {
		t.Error("hooks were recorded for a hookless pane")
	}
}

// A terminal pane in the sandbox asks for no hooks: nothing is fetched or
// mounted for it.
func TestSpawnPane_SbxTerminalAsksForNoHooks(t *testing.T) {
	d, pane, r := sbxResumeFixture(t)
	fakeLinuxQuild(t)
	pane.Type = "terminal"
	s := attachRun(t, d, pane)
	if s == nil {
		t.Fatalf("the pane was not started; error = %q", pane.SpawnError)
	}
	if r.ran("/fake/sbx", "mount") || r.ran("/fake/sbx", "exec", "team-box", "uname", "-m") {
		t.Errorf("a terminal pane prepared hooks: %v", r.calls)
	}
}

// What the hooks write in the shared tree reaches the daemon's own folders:
// event lines into the spool, history lines into history/, and the session
// record into sessions/ — each read through an os.Root, so a symlink the
// agent plants in the tree is never followed.
func TestForwardSbxHookFiles_MovesEventsHistoryAndTheSessionRecord(t *testing.T) {
	d, pane, _ := sbxResumeFixture(t)
	home := config.QuilDir()
	tree := sbxHookTree(home, "team-box")
	for _, dir := range []string{"events", "history", "sessions"} {
		if err := os.MkdirAll(filepath.Join(tree, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	d.sbxHookState.panes = map[string]sbxHooks{pane.ID: {tree: tree}}
	d.spoolFwd = newSpoolForwarder() // set by Start, which the fixture skips
	write := func(rel, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(tree, rel), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("events/"+pane.ID+".jsonl", `{"event":"Stop"}`+"\n"+`{"event":"partial"`)
	write("history/"+pane.ID+".jsonl", `{"v":1,"text":"hi"}`+"\n")
	write("sessions/"+pane.ID+".id", sbxSessionA+"\n")
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("host-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(tree, "sessions", pane.ID+".transcript")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}

	d.forwardSbxHookFiles()

	if got, _ := os.ReadFile(filepath.Join(home, "events", pane.ID+".jsonl")); string(got) != `{"event":"Stop"}`+"\n" {
		t.Errorf("events forwarded = %q, want the complete line only", got)
	}
	if got, _ := os.ReadFile(filepath.Join(home, "history", pane.ID+".jsonl")); string(got) != `{"v":1,"text":"hi"}`+"\n" {
		t.Errorf("history forwarded = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(home, "sessions", pane.ID+".id")); string(got) != sbxSessionA+"\n" {
		t.Errorf("session record synced = %q", got)
	}
	if _, err := os.Stat(filepath.Join(home, "sessions", pane.ID+".transcript")); err == nil {
		t.Error("a symlink planted in the tree was followed and its target copied")
	}

	// A rewritten record (claude's /clear) replaces the copy.
	write("sessions/"+pane.ID+".id", sbxSessionB+"\n")
	d.forwardSbxHookFiles()
	if got, _ := os.ReadFile(filepath.Join(home, "sessions", pane.ID+".id")); string(got) != sbxSessionB+"\n" {
		t.Errorf("rotated session record synced = %q", got)
	}

	// A destroyed pane is forgotten on the next pass.
	d.sbxHookState.panes["pane-gone0000"] = sbxHooks{tree: tree}
	d.forwardSbxHookFiles()
	if _, ok := d.sbxHooksFor("pane-gone0000"); ok {
		t.Error("a pane that no longer exists kept its hook record")
	}
}

// A restart follows the session the hook recorded: claude rotated it on
// /clear, and the id Quil minted names the conversation the user left.
func TestSpawnPane_SbxRestartFollowsTheHookRecordedSession(t *testing.T) {
	d, pane, r := sbxResumeFixture(t)
	pane.PluginState = map[string]string{"session_id": sbxSessionA}
	prev := readHookSessionFn
	readHookSessionFn = func(paneID string) (claudehook.SessionRecord, error) {
		return claudehook.SessionRecord{ID: sbxSessionB}, nil
	}
	t.Cleanup(func() { readHookSessionFn = prev })
	r.transcript = true
	s := attachRun(t, d, pane)
	if s == nil {
		t.Fatalf("the pane was not started; error = %q", pane.SpawnError)
	}
	if !r.probed(sbxSessionB) || r.probed(sbxSessionA) {
		t.Errorf("probed the wrong session: %v", r.probeCalls())
	}
	if got := sessionArgsOf(t, d, s); !slices.Equal(got, []string{"--resume", sbxSessionB}) {
		t.Errorf("args = %v, want --resume of the hook's session", got)
	}
	if pane.PluginState["session_id"] != sbxSessionB {
		t.Errorf("session_id = %q, want the hook's %q", pane.PluginState["session_id"], sbxSessionB)
	}
}

// The input-history gate follows the plugin, as on the host: the hook records
// a prompt only when the exec carries it.
func TestSpawnPane_SbxHistoryOptInFollowsThePlugin(t *testing.T) {
	for name, on := range map[string]bool{"recording": true, "not recording": false} {
		t.Run(name, func(t *testing.T) {
			d, pane, _ := sbxResumeFixture(t)
			fakeLinuxQuild(t)
			d.registry.Get("claude-code").Command.RecordHistory = on
			s := attachRun(t, d, pane)
			if s == nil {
				t.Fatalf("the pane was not started; error = %q", pane.SpawnError)
			}
			if got := slices.Contains(s.startArgs, "QUIL_RECORD_HISTORY=1"); got != on {
				t.Errorf("QUIL_RECORD_HISTORY=1 present = %v, want %v: %v", got, on, s.startArgs)
			}
		})
	}
}

// A tree that outlives the daemon is not replayed: a fresh forwarder starts
// at the end of what the tree already holds, and only lines appended after
// that reach the daemon's own folders.
func TestBaselineSbxHookOffsets_NoReplayAfterARestart(t *testing.T) {
	d, pane, _ := sbxResumeFixture(t)
	home := config.QuilDir()
	tree := sbxHookTree(home, "team-box")
	for _, dir := range []string{"events", "history"} {
		if err := os.MkdirAll(filepath.Join(tree, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	events := filepath.Join(tree, "events", pane.ID+".jsonl")
	history := filepath.Join(tree, "history", pane.ID+".jsonl")
	if err := os.WriteFile(events, []byte(`{"event":"old"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(history, []byte(`{"text":"old"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d.sbxHookState.panes = map[string]sbxHooks{pane.ID: {tree: tree}}
	d.spoolFwd = newSpoolForwarder()
	d.forwardSbxHookFiles() // the previous daemon delivered these

	// The daemon restarts: a fresh forwarder, and Spool.Init cleared the
	// spool. The preparation baselines the pane before the agent starts.
	d.spoolFwd = newSpoolForwarder()
	for _, p := range []string{filepath.Join(home, "events", pane.ID+".jsonl"), filepath.Join(home, "history", pane.ID+".jsonl")} {
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
	d.baselineSbxHookOffsets(tree, pane.ID)
	appendTo := func(p, line string) {
		t.Helper()
		f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.WriteString(line); err != nil {
			t.Fatal(err)
		}
	}
	appendTo(events, `{"event":"new"}`+"\n")
	appendTo(history, `{"text":"new"}`+"\n")
	d.forwardSbxHookFiles()

	if got, _ := os.ReadFile(filepath.Join(home, "events", pane.ID+".jsonl")); string(got) != `{"event":"new"}`+"\n" {
		t.Errorf("events after the restart = %q, want only the new line", got)
	}
	if got, _ := os.ReadFile(filepath.Join(home, "history", pane.ID+".jsonl")); string(got) != `{"text":"new"}`+"\n" {
		t.Errorf("history after the restart = %q, want only the new line", got)
	}
}
