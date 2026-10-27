package daemon

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/claudehook"
	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/sbx"
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
	if err := os.WriteFile(history, []byte(`{"ts_ms":1,"text":"old"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d.sbxHookState.panes = map[string]sbxHooks{pane.ID: {tree: tree}}
	d.spoolFwd = newSpoolForwarder()
	d.forwardSbxHookFiles() // the previous daemon delivered these

	// The daemon restarts: a fresh forwarder, and Spool.Init cleared the
	// spool (the history copy persists). The preparation baselines the pane
	// before the agent starts.
	d.spoolFwd = newSpoolForwarder()
	if err := os.Remove(filepath.Join(home, "events", pane.ID+".jsonl")); err != nil {
		t.Fatal(err)
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
	appendTo(history, `{"ts_ms":3,"text":"new"}`+"\n")
	d.forwardSbxHookFiles()

	if got, _ := os.ReadFile(filepath.Join(home, "events", pane.ID+".jsonl")); string(got) != `{"event":"new"}`+"\n" {
		t.Errorf("events after the restart = %q, want only the new line", got)
	}
	if got, _ := os.ReadFile(filepath.Join(home, "history", pane.ID+".jsonl")); string(got) != `{"ts_ms":1,"text":"old"}`+"\n"+`{"ts_ms":3,"text":"new"}`+"\n" {
		t.Errorf("history after the restart = %q, want the delivered line once and the new one", got)
	}
}

// History is user data and the source's length is not a receipt: an entry
// the hook appended after the last tick and before a crash is still pending.
// After the restart the pending entry is delivered, the delivered one is not
// delivered twice, and a new one follows — each exactly once.
func TestBaselineSbxHookOffsets_HistoryResumesFromDeliveredEntries(t *testing.T) {
	d, pane, _ := sbxResumeFixture(t)
	home := config.QuilDir()
	tree := sbxHookTree(home, "team-box")
	if err := os.MkdirAll(filepath.Join(tree, "history"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "history"), 0o700); err != nil {
		t.Fatal(err)
	}
	a, b, c := `{"ts_ms":1,"text":"A"}`+"\n", `{"ts_ms":2,"text":"B"}`+"\n", `{"ts_ms":3,"text":"C"}`+"\n"
	src := filepath.Join(tree, "history", pane.ID+".jsonl")
	dst := filepath.Join(home, "history", pane.ID+".jsonl")
	if err := os.WriteFile(src, []byte(a+b), 0o600); err != nil { // B never reached the host
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte(a), 0o600); err != nil {
		t.Fatal(err)
	}
	d.sbxHookState.panes = map[string]sbxHooks{pane.ID: {tree: tree}}
	d.spoolFwd = newSpoolForwarder()
	d.baselineSbxHookOffsets(tree, pane.ID)
	f, err := os.OpenFile(src, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(c); err != nil {
		t.Fatal(err)
	}
	f.Close()

	d.forwardSbxHookFiles()

	if got, _ := os.ReadFile(dst); string(got) != a+b+c {
		t.Errorf("history = %q, want A, B and C once each", got)
	}
}

// The preparation seeds a pane's offsets BEFORE it publishes the pane to the
// watcher, so a tick that lands between the two cannot forward a retained
// file from zero.
func TestPrepareSbxHooks_BaselinesBeforePublishing(t *testing.T) {
	d, pane, r := sbxResumeFixture(t)
	fakeLinuxQuild(t)
	home := config.QuilDir()
	tree := sbxHookTree(home, "team-box")
	if err := os.MkdirAll(filepath.Join(tree, "events"), 0o700); err != nil {
		t.Fatal(err)
	}
	old := `{"event":"old"}` + "\n"
	if err := os.WriteFile(filepath.Join(tree, "events", pane.ID+".jsonl"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	d.spoolFwd = newSpoolForwarder()
	ok := d.prepareSbxHooks(context.Background(), sbx.Client{Bin: "/fake/sbx", Runner: r}, "team-box", []string{pane.ID})
	if !ok {
		t.Fatal("hooks were not prepared")
	}
	d.spoolFwd.mu.Lock()
	off := d.spoolFwd.offsets[pane.ID]
	d.spoolFwd.mu.Unlock()
	if off != int64(len(old)) {
		t.Errorf("events offset at publish = %d, want %d (the retained file's end)", off, len(old))
	}
	d.forwardSbxHookFiles()
	if got, err := os.ReadFile(filepath.Join(home, "events", pane.ID+".jsonl")); err == nil && len(got) > 0 {
		t.Errorf("a retained event was replayed: %q", got)
	}
}

// A pane prepared again (Alt+R) is still registered, so the watcher can
// forward it WHILE its baseline runs. Observation and commit are one hold:
// a forward that arrives between them waits, and an entry the watcher
// delivers is never delivered twice by a rewound offset.
func TestBaselineSbxHookOffsets_ReseedOfALivePaneDoesNotRewind(t *testing.T) {
	d, pane, _ := sbxResumeFixture(t)
	home := config.QuilDir()
	tree := sbxHookTree(home, "team-box")
	if err := os.MkdirAll(filepath.Join(tree, "history"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "history"), 0o700); err != nil {
		t.Fatal(err)
	}
	a, b := `{"ts_ms":1,"text":"A"}`+"\n", `{"ts_ms":2,"text":"B"}`+"\n"
	src := filepath.Join(tree, "history", pane.ID+".jsonl")
	dst := filepath.Join(home, "history", pane.ID+".jsonl")
	if err := os.WriteFile(src, []byte(a+b), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte(a), 0o600); err != nil {
		t.Fatal(err)
	}
	d.sbxHookState.panes = map[string]sbxHooks{pane.ID: {tree: tree}} // registered already
	d.spoolFwd = newSpoolForwarder()
	d.spoolFwd.setOffsetForTest(pane.ID+"/history", int64(len(a)))

	// The watcher ticks at the worst moment: after the baseline observed
	// end(A), before it committed. It must WAIT for the commit — a forward
	// that ran here would deliver B and then be rewound to end(A).
	forwarded := make(chan struct{})
	prev := sbxBaselineObservedFn
	sbxBaselineObservedFn = func(string) {
		go func() {
			d.forwardSbxHookFiles()
			close(forwarded)
		}()
		select {
		case <-forwarded:
			t.Error("the watcher forwarded between the baseline's observation and its commit")
		case <-time.After(150 * time.Millisecond):
		}
	}
	t.Cleanup(func() { sbxBaselineObservedFn = prev })

	d.baselineSbxHookOffsets(tree, pane.ID)
	<-forwarded
	d.forwardSbxHookFiles() // the next tick

	if got, _ := os.ReadFile(dst); string(got) != a+b {
		t.Errorf("history = %q, want A then B exactly once", got)
	}
}

// The first publication: the pane is not visible to the watcher until its
// offsets are committed.
func TestPrepareSbxHooks_PaneIsUnpublishedUntilItsBaselineIsCommitted(t *testing.T) {
	d, pane, r := sbxResumeFixture(t)
	fakeLinuxQuild(t)
	d.spoolFwd = newSpoolForwarder()
	seen := false
	prev := sbxBaselineObservedFn
	sbxBaselineObservedFn = func(id string) {
		seen = true
		if _, published := d.sbxHooksFor(id); published {
			t.Error("the pane was published to the watcher before its baseline was committed")
		}
	}
	t.Cleanup(func() { sbxBaselineObservedFn = prev })
	if !d.prepareSbxHooks(context.Background(), sbx.Client{Bin: "/fake/sbx", Runner: r}, "team-box", []string{pane.ID}) {
		t.Fatal("hooks were not prepared")
	}
	if !seen {
		t.Fatal("the baseline never ran")
	}
	if _, published := d.sbxHooksFor(pane.ID); !published {
		t.Error("the pane was not published after its baseline")
	}
}

// The daemon's own history copy is read by its TAIL, bounded: a sandbox that
// grew it cannot make the next restart read all of it, and the newest
// delivered entries are the ones that matter for resuming.
func TestHistoryBaseline_ReadsABoundedTailOfTheCopy(t *testing.T) {
	const tail = 48 // two short entries, cut inside a third
	dir := t.TempDir()
	src := filepath.Join(dir, "src.jsonl")
	dst := filepath.Join(dir, "dst.jsonl")
	var lines []string
	for i := 1; i <= 50; i++ {
		lines = append(lines, `{"ts_ms":`+strconv.Itoa(i)+`}`)
	}
	// The copy holds entries 1..50; the source holds 49, 50 and a pending 51.
	if err := os.WriteFile(dst, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srcBody := lines[48] + "\n" + lines[49] + "\n" + `{"ts_ms":51}` + "\n"
	if err := os.WriteFile(src, []byte(srcBody), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	off, err := historyBaselineWithin(root, "src.jsonl", dst, tail)
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(len(lines[48]) + 1 + len(lines[49]) + 1); off != want {
		t.Errorf("offset = %d, want %d (past the last delivered entry, 50)", off, want)
	}
	// And a copy whose tail holds none of the source's entries resumes from 0.
	if err := os.WriteFile(dst, []byte(strings.Join(lines[:10], "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if off, err := historyBaselineWithin(root, "src.jsonl", dst, tail); err != nil || off != 0 {
		t.Errorf("offset = %d, %v; want 0 when the copy's tail holds none of the source", off, err)
	}
	// The discriminating case: the only source entry the copy holds sits in
	// the copy's HEAD, outside the tail. A read of the whole copy would
	// resume past it; the bounded read never sees it and resumes from 0.
	if err := os.WriteFile(dst, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte(lines[0]+"\n"+`{"ts_ms":51}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if off, err := historyBaselineWithin(root, "src.jsonl", dst, tail); err != nil || off != 0 {
		t.Errorf("offset = %d, %v; want 0: entry 1 is in the copy only outside its tail, so the whole copy was read", off, err)
	}
	// The control: a tail covering the whole copy does see entry 1.
	if off, err := historyBaselineWithin(root, "src.jsonl", dst, 1<<20); err != nil || off != int64(len(lines[0])+1) {
		t.Errorf("offset = %d, %v; want past entry 1 with a tail that covers the whole copy", off, err)
	}
}
