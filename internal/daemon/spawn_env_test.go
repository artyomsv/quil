package daemon

import (
	"errors"
	"strings"
	"testing"
)

// TestClaudeHookSpawnPrep_PaneEnvUsesHookHome: the pane env must carry
// QUIL_HOOK_HOME, NOT QUIL_HOME — children inherit the pane env, and an
// inherited QUIL_HOME silently retargets quil dev builds at production
// (techdebt/daemon/1-3). The hook subcommand reads QUIL_HOOK_HOME.
func TestClaudeHookSpawnPrep_PaneEnvUsesHookHome(t *testing.T) {
	orig := quildExeFn
	quildExeFn = func() (string, error) { return "/fake/quild", nil }
	defer func() { quildExeFn = orig }()

	// quilDir must be writable: claudeHookSpawnPrep now writes the hook
	// settings to a per-pane file under <quilDir>/sessions/.
	quilDir := t.TempDir()
	_, env := claudeHookSpawnPrep(quilDir, "pane-abc123", "default", nil)
	assertHookHomeOnly(t, env, quilDir)
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

// TestCodexSpawnPrep_PaneEnvUsesHookHome mirrors the claude/opencode tests:
// children inherit the pane env, so QUIL_HOME must not appear there — and the
// prefix must be the single `-c hooks=…` override that registers the
// codex-hook command together with its trust hashes.
func TestCodexSpawnPrep_PaneEnvUsesHookHome(t *testing.T) {
	orig := quildExeFn
	quildExeFn = func() (string, error) { return "/fake/quild", nil }
	defer func() { quildExeFn = orig }()

	prefix, env := codexSpawnPrep("/data/quil", "pane-cx123", "default", "/usr/local/bin/codex")
	assertHookHomeOnly(t, env, "/data/quil")
	if len(prefix) != 2 || prefix[0] != "-c" || !strings.HasPrefix(prefix[1], "hooks={") {
		t.Errorf("prefix = %q, want [-c hooks={…}]", prefix)
	}
	if !strings.Contains(prefix[1], `codex-hook`) || !strings.Contains(prefix[1], "trusted_hash=") {
		t.Errorf("prefix must register the codex-hook command with trust: %s", prefix[1])
	}
	for _, kv := range env {
		switch {
		case kv == "QUIL_PANE_ID=pane-cx123", kv == "QUIL_HOOK_MODE=default", strings.HasPrefix(kv, "QUIL_HOOK_HOME="):
		default:
			t.Errorf("unexpected env entry %q", kv)
		}
	}
}

// TestCodexSpawnPrep_ShimDisablesHooks: an npm-installed codex is a cmd.exe
// shim that re-parses the quotes in the override; the spawn must then proceed
// WITHOUT the hook rather than with a mangled argument.
func TestCodexSpawnPrep_ShimDisablesHooks(t *testing.T) {
	orig := quildExeFn
	quildExeFn = func() (string, error) { return "/fake/quild", nil }
	defer func() { quildExeFn = orig }()

	prefix, env := codexSpawnPrep("/data/quil", "pane-cx123", "default", `C:\Users\x\AppData\Roaming\npm\codex.cmd`)
	if prefix != nil || env != nil {
		t.Errorf("shim: prefix=%q env=%q, want nil/nil", prefix, env)
	}
}

func TestCodexSpawnPrep_UnresolvableExeDisablesHooks(t *testing.T) {
	orig := quildExeFn
	quildExeFn = func() (string, error) { return "", errors.New("no exe") }
	defer func() { quildExeFn = orig }()

	prefix, env := codexSpawnPrep("/data/quil", "pane-cx123", "default", "/usr/local/bin/codex")
	if prefix != nil || env != nil {
		t.Errorf("prefix=%q env=%q, want nil/nil", prefix, env)
	}
}
