package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/codexhook"
	"github.com/artyomsv/quil/internal/hookevents"
)

// registerCodexPlugin loads the shipped codex plugin's shape into d's registry
// through LoadFromDir, so the TOML passes the same validation the default
// does. New() builds an empty registry, and spawnPane degrades an unknown type
// to a plain terminal — which is exactly the failure these tests exist to see.
func registerCodexPlugin(t *testing.T, d *Daemon) {
	t.Helper()
	dir := t.TempDir()
	const toml = `
[plugin]
name = "codex"
display_name = "Codex"
category = "ai"
schema_version = 1

[command]
cmd = "codex"
prompts_cwd = true
record_history = true

[persistence]
strategy = "session_scrape"
resume_args = []
`
	if err := os.WriteFile(filepath.Join(dir, "codex.toml"), []byte(toml), 0o644); err != nil {
		t.Fatalf("write codex.toml: %v", err)
	}
	if err := d.registry.LoadFromDir(dir); err != nil {
		t.Fatalf("load test plugins: %v", err)
	}
}

// TestSpawnPane_CodexArmInjectsHookOverride pins the WIRING, not the helpers.
// codexSpawnPrep and resolveSpawnArgs are each tested on their own, and a
// mutation run showed the whole codex arm of spawnPane could be deleted with
// the suite green: the pane then spawns with no hooks at all — no
// notifications, no work state, no input history, no session resume — and
// nothing reports it. This drives the real spawnPane with a fake PTY and reads
// what the child would have received.
func TestSpawnPane_CodexArmInjectsHookOverride(t *testing.T) {
	// Mutates quildExeFn — not parallel-safe.
	orig := quildExeFn
	quildExeFn = func() (string, error) { return "/opt/quil/quild", nil }
	t.Cleanup(func() { quildExeFn = orig })

	d := newTestDaemon(t)
	registerCodexPlugin(t, d)

	fake := &fakeSession{}
	pane := &Pane{ID: "pane-c0dec0de", Type: "codex", CWD: t.TempDir()}
	if err := d.spawnPane(pane, fake, false); err != nil {
		t.Fatalf("spawnPane: %v", err)
	}
	if !fake.started {
		t.Fatal("Start was never called")
	}
	if len(fake.startArgs) < 2 || fake.startArgs[0] != "-c" || !strings.HasPrefix(fake.startArgs[1], "hooks={") {
		t.Fatalf("argv must begin with the -c hooks override, got %q", fake.startArgs)
	}
	if !strings.Contains(fake.startArgs[1], "codex-hook") || !strings.Contains(fake.startArgs[1], "trusted_hash=") {
		t.Errorf("override does not register the codex-hook command with trust: %s", fake.startArgs[1])
	}

	var paneID, hookHome, mode, history bool
	for _, kv := range fake.env {
		switch {
		case kv == "QUIL_PANE_ID=pane-c0dec0de":
			paneID = true
		case strings.HasPrefix(kv, "QUIL_HOOK_HOME="):
			hookHome = true
		case kv == "QUIL_HOOK_MODE=default":
			mode = true
		case kv == "QUIL_RECORD_HISTORY=1":
			history = true
		case strings.HasPrefix(kv, "QUIL_HOME="):
			t.Errorf("pane env carries %s — children inherit it and a dev build would retarget at production", kv)
		}
	}
	if !paneID || !hookHome || !mode || !history {
		t.Errorf("pane env missing hook context: pane=%v home=%v mode=%v history=%v (env=%q)", paneID, hookHome, mode, history, fake.env)
	}
}

// TestSpawnPane_CodexRestoreResumesRecordedSession drives the restore branch
// through spawnPane end to end: the hook's record must reach argv as
// `-c hooks=… resume <id>` — override first, because -c is global and the
// subcommand must come after it.
func TestSpawnPane_CodexRestoreResumesRecordedSession(t *testing.T) {
	origExe, origRead := quildExeFn, readCodexSessionFn
	quildExeFn = func() (string, error) { return "/opt/quil/quild", nil }
	const sid = "01a05db1-9f44-73b2-b426-8aad5f5232f4"
	readCodexSessionFn = func(string) (codexhook.SessionRecord, error) {
		return codexhook.SessionRecord{ID: sid}, nil
	}
	t.Cleanup(func() { quildExeFn, readCodexSessionFn = origExe, origRead })

	d := newTestDaemon(t)
	registerCodexPlugin(t, d)

	fake := &fakeSession{}
	pane := &Pane{ID: "pane-c0dec0de", Type: "codex", CWD: t.TempDir()}
	if err := d.spawnPane(pane, fake, true); err != nil {
		t.Fatalf("spawnPane: %v", err)
	}
	args := fake.startArgs
	if len(args) != 4 || args[0] != "-c" || !strings.HasPrefix(args[1], "hooks={") || args[2] != "resume" || args[3] != sid {
		t.Fatalf("argv = %q, want [-c hooks={…} resume %s]", args, sid)
	}
}

// TestEmitHookEvent_CodexTierGate covers the daemon-side half of
// `[notification.hooks] codex = "off"`: the source's own tier drops the
// event, and no other source's tier speaks for it. Titles differ per push
// because the queue aggregates by (pane, title).
func TestEmitHookEvent_CodexTierGate(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, "")
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	payload := func(title string) hookevents.Payload {
		return hookevents.Payload{
			V: hookevents.SchemaVersion, TsMs: 1, PaneID: pane.ID,
			Source: hookevents.SourceCodex, HookEvent: "Stop",
			Title: title, Severity: hookevents.SeverityWarning,
		}
	}

	d.cfg.Notification.Hooks.Codex = "off"
	d.emitHookEvent(payload("Reply ready"))
	if n := d.events.Count(); n != 0 {
		t.Fatalf("codex tier off: %d events queued, want 0", n)
	}

	d.cfg.Notification.Hooks.Codex = "default"
	d.emitHookEvent(payload("Reply ready"))
	if n := d.events.Count(); n != 1 {
		t.Fatalf("codex tier default: %d events queued, want 1", n)
	}

	// Another source's tier must not gate codex.
	d.cfg.Notification.Hooks.Claude = "off"
	d.cfg.Notification.Hooks.OpenCode = "off"
	d.emitHookEvent(payload("Compaction complete"))
	if n := d.events.Count(); n != 2 {
		t.Fatalf("claude/opencode tiers off: %d events queued, want 2", n)
	}
}
