package daemon

import (
	"strings"
	"testing"
)

// TestClaudeHookSpawnPrep_PaneEnvUsesHookHome: the pane env must carry
// QUIL_HOOK_HOME, NOT QUIL_HOME — children inherit the pane env, and an
// inherited QUIL_HOME silently retargets quil dev builds at production
// (techdebt/daemon/1-3). The hook subcommand reads QUIL_HOOK_HOME.
func TestClaudeHookSpawnPrep_PaneEnvUsesHookHome(t *testing.T) {
	orig := claudeHookExeFn
	claudeHookExeFn = func() (string, error) { return "/fake/quild", nil }
	defer func() { claudeHookExeFn = orig }()

	_, env := claudeHookSpawnPrep("/data/quil", "pane-abc123", "default", nil)
	assertHookHomeOnly(t, env, "/data/quil")
}

// TestOpencodeSpawnPrep_PaneEnvUsesHookHome mirrors the claude test for the
// opencode env builder. Children inherit the pane env, so QUIL_HOME must not
// appear there.
func TestOpencodeSpawnPrep_PaneEnvUsesHookHome(t *testing.T) {
	orig := opencodeHookScriptStatFn
	opencodeHookScriptStatFn = func(string) error { return nil }
	defer func() { opencodeHookScriptStatFn = orig }()

	env := opencodeSpawnPrep("/data/quil", "pane-oc123", "default")
	assertHookHomeOnly(t, env, "/data/quil")
}

func assertHookHomeOnly(t *testing.T, env []string, dir string) {
	t.Helper()
	var hookHome bool
	for _, kv := range env {
		if strings.HasPrefix(kv, "QUIL_HOME=") {
			t.Errorf("pane env still carries %s — retargets dev builds at production", kv)
		}
		if kv == "QUIL_HOOK_HOME="+dir {
			hookHome = true
		}
	}
	if !hookHome {
		t.Errorf("pane env missing QUIL_HOOK_HOME=%s; env = %v", dir, env)
	}
}
