package claudehook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/hookevents"
)

func runProbe(t *testing.T, stdin map[string]any) (string, HookEnv) {
	t.Helper()
	dir := t.TempDir()
	env := HookEnv{PaneID: "pane-wt", QuilDir: dir, Mode: "default"}
	raw, err := json.Marshal(stdin)
	if err != nil {
		t.Fatal(err)
	}
	if err := RunHook(strings.NewReader(string(raw)), env, 1); err != nil {
		t.Fatalf("RunHook: %v", err)
	}
	return dir, env
}

func noSpool(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, "events", "pane-wt.jsonl")); !os.IsNotExist(err) {
		t.Errorf("spooled a line, want none (stat err=%v)", err)
	}
}

// The measured EnterWorktree payload: the worktree path comes from the tool's
// response. A 140-byte Windows path is under the cap and must survive whole.
func TestRunHook_EnterWorktreeSpoolsProbeWithWorktreePath(t *testing.T) {
	path := `C:\Users\someone\AppData\Local\Temp\claude\9de8ab2da69d7559\581b2df7-70e5\scratchpad\wtprobe\repo\.claude\worktrees\probe-a`
	dir, _ := runProbe(t, map[string]any{
		"hook_event_name": "PostToolUse", "tool_name": "EnterWorktree", "session_id": "s1",
		"cwd":           path,
		"tool_input":    map[string]any{"name": "probe-a"},
		"tool_response": map[string]any{"worktreePath": path, "worktreeBranch": "worktree-probe-a"},
	})
	got := readSpool(t, dir, "pane-wt")
	if len(got) != 1 || got[0].HookEvent != hookevents.EventWorktreeProbe || got[0].CWD != path ||
		got[0].Data[hookevents.DataProbeTrigger] != hookevents.ProbeEnter {
		t.Fatalf("spool = %+v, want one enter probe naming %s", got, path)
	}
}

// A shell command that makes a worktree spools a probe for the session's cwd,
// carrying the trigger and NEVER the command text (it can hold a credential).
func TestRunHook_BashWorktreeAddSpoolsProbe(t *testing.T) {
	dir, _ := runProbe(t, map[string]any{
		"hook_event_name": "PostToolUse", "tool_name": "Bash", "cwd": "/w/repo",
		"tool_input": map[string]any{"command": "git worktree add ../wt-bash -b bash-branch"},
	})
	got := readSpool(t, dir, "pane-wt")
	if len(got) != 1 || got[0].CWD != "/w/repo" || got[0].Data[hookevents.DataProbeTrigger] != hookevents.ProbeShell || len(got[0].Data) != 1 {
		t.Fatalf("spool = %+v, want one shell probe for /w/repo carrying only the trigger", got)
	}
}

// A Bash PostToolUse must never spool the PostToolUse resume edge: that edge
// clears the pane's unseen mark, and it would fire on every shell command.
func TestRunHook_BashPostToolUseNeverSpoolsResumeEdge(t *testing.T) {
	dir, _ := runProbe(t, map[string]any{
		"hook_event_name": "PostToolUse", "tool_name": "Bash", "cwd": "/w/repo",
		"tool_input": map[string]any{"command": "git worktree list"},
	})
	for _, p := range readSpool(t, dir, "pane-wt") {
		if p.HookEvent == "PostToolUse" {
			t.Errorf("spooled a PostToolUse resume edge for a Bash call: %+v", p)
		}
	}
}

// An ordinary shell command spools nothing.
func TestRunHook_BashWithoutWorktreeWordSpoolsNothing(t *testing.T) {
	dir, _ := runProbe(t, map[string]any{
		"hook_event_name": "PostToolUse", "tool_name": "Bash", "cwd": "/w/repo",
		"tool_input": map[string]any{"command": "go test ./..."},
	})
	noSpool(t, dir)
}

// A directory over the cap is dropped, not cut: a cut path names another one.
func TestRunHook_ProbeDirOverCapIsDropped(t *testing.T) {
	dir, _ := runProbe(t, map[string]any{
		"hook_event_name": "PostToolUse", "tool_name": "Bash",
		"cwd":        "/" + strings.Repeat("d", hookevents.MaxProbeDirBytes),
		"tool_input": map[string]any{"command": "git worktree add ../x"},
	})
	noSpool(t, dir)
}

// The registration and the handler agree: a matcher missing Bash would mean
// the probe never fires for a shell-made worktree.
func TestBuildSettingsJSON_PostToolUseMatcherCoversWorktreeTools(t *testing.T) {
	js, err := BuildSettingsJSON("quild claude-hook")
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"Bash", "EnterWorktree", "ExitWorktree"} {
		if !strings.Contains(js, tool) {
			t.Errorf("PostToolUse matcher lacks %s: %s", tool, js)
		}
	}
	if strings.Contains(js, "WorktreeCreate") {
		t.Error("registered a WorktreeCreate hook — that REPLACES Claude's own worktree creation")
	}
}
