package codexhook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/hookevents"
)

// The measured codex shape: a shell call reports its own workdir as cwd and
// the command in tool_input. A worktree add spools one probe for that cwd.
func TestRunHook_CodexBashWorktreeAddSpoolsProbe(t *testing.T) {
	dir := t.TempDir()
	env := HookEnv{PaneID: "pane-cx", QuilDir: dir, Mode: "default"}
	run(t, env, `{"hook_event_name":"PostToolUse","tool_name":"Bash","cwd":"/w/repo2",`+
		`"tool_input":{"command":"git worktree add ../wt-codex -b codex-branch"},`+
		`"tool_response":"Preparing worktree (new branch 'codex-branch')\n"}`, 1)

	got := readSpool(t, dir, "pane-cx")
	if len(got) != 1 || got[0].HookEvent != hookevents.EventWorktreeProbe || got[0].CWD != "/w/repo2" ||
		got[0].Source != hookevents.SourceCodex {
		t.Fatalf("spool = %+v, want one codex probe for /w/repo2", got)
	}
}

// Codex fires PostToolUse for every tool call; anything but a matching shell
// command must spool nothing at all.
func TestRunHook_CodexPostToolUseOtherwiseSilent(t *testing.T) {
	dir := t.TempDir()
	env := HookEnv{PaneID: "pane-cx", QuilDir: dir, Mode: "default"}
	run(t, env, `{"hook_event_name":"PostToolUse","tool_name":"Bash","cwd":"/w/r","tool_input":{"command":"go test ./..."}}`, 1)
	run(t, env, `{"hook_event_name":"PostToolUse","tool_name":"apply_patch","cwd":"/w/r","tool_input":{"command":"worktree"}}`, 1)

	if _, err := os.Stat(filepath.Join(dir, "events", "pane-cx.jsonl")); !os.IsNotExist(err) {
		t.Errorf("spooled a line for a non-matching tool call (stat err=%v)", err)
	}
}

// Adding PostToolUse adds its own trust key; codex skips an untrusted hook
// silently, so a missing key would make the probe never run.
func TestBuildConfigOverride_RegistersPostToolUse(t *testing.T) {
	v, err := BuildConfigOverride(`& $env:QUIL_HOOK_EXE codex-hook`, "windows")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"PostToolUse=[", `post_tool_use:0:0`} {
		if !strings.Contains(v, want) {
			t.Errorf("override lacks %q", want)
		}
	}
}
