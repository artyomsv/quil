package daemon

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/plugin"
)

// TestResolveSpawnArgs_Matrix exercises the arg-merging matrix that lives in
// resolveSpawnArgs. Each case mirrors a real spawn scenario from spawnPane.
// The point of the matrix is to lock in the regression that the restore branch
// for preassign_id / session_scrape now *appends* ResumeArgs to existing args
// instead of replacing them — without this, runtime toggle args (e.g.
// "--dangerously-skip-permissions") were dropped on daemon restart.
func TestResolveSpawnArgs_Matrix(t *testing.T) {
	tests := []struct {
		name       string
		plugin     *plugin.PanePlugin
		pane       *Pane
		restoring  bool
		want       []string
	}{
		{
			name: "fresh terminal — base args only",
			plugin: &plugin.PanePlugin{
				Command: plugin.CommandConfig{
					Cmd:  "bash",
					Args: []string{"-l"},
				},
				Persistence: plugin.PersistenceConfig{Strategy: "cwd_only"},
			},
			pane:      &Pane{},
			restoring: false,
			want:      []string{"-l"},
		},
		{
			name: "fresh ssh — InstanceArgs override base args",
			plugin: &plugin.PanePlugin{
				Command: plugin.CommandConfig{
					Cmd:  "ssh",
					Args: []string{"-o", "ServerAliveInterval=60"},
				},
				Persistence: plugin.PersistenceConfig{Strategy: "rerun"},
			},
			pane: &Pane{
				InstanceArgs: []string{"-p", "2222", "user@host"},
			},
			restoring: false,
			want:      []string{"-p", "2222", "user@host"},
		},
		{
			name: "fresh claude-code — preassign_id appends StartArgs after expansion",
			plugin: &plugin.PanePlugin{
				Command: plugin.CommandConfig{
					Cmd: "claude",
				},
				Persistence: plugin.PersistenceConfig{
					Strategy:  "preassign_id",
					StartArgs: []string{"--session-id", "{session_id}"},
				},
			},
			pane: &Pane{
				PluginState: map[string]string{"session_id": "abc-123"},
			},
			restoring: false,
			want:      []string{"--session-id", "abc-123"},
		},
		{
			name: "fresh claude-code with toggle — InstanceArgs + StartArgs",
			plugin: &plugin.PanePlugin{
				Command: plugin.CommandConfig{Cmd: "claude"},
				Persistence: plugin.PersistenceConfig{
					Strategy:  "preassign_id",
					StartArgs: []string{"--session-id", "{session_id}"},
				},
			},
			pane: &Pane{
				InstanceArgs: []string{"--dangerously-skip-permissions"},
				PluginState:  map[string]string{"session_id": "abc-123"},
			},
			restoring: false,
			want:      []string{"--dangerously-skip-permissions", "--session-id", "abc-123"},
		},
		{
			name: "RESTORE preassign_id — ResumeArgs only when InstanceArgs empty",
			plugin: &plugin.PanePlugin{
				Command: plugin.CommandConfig{Cmd: "claude"},
				Persistence: plugin.PersistenceConfig{
					Strategy:   "preassign_id",
					ResumeArgs: []string{"--continue"},
				},
			},
			pane: &Pane{
				PluginState: map[string]string{"session_id": "abc-123"},
			},
			restoring: true,
			want:      []string{"--continue"},
		},
		{
			name: "RESTORE preassign_id — InstanceArgs PRESERVED + ResumeArgs APPENDED (regression)",
			plugin: &plugin.PanePlugin{
				Command: plugin.CommandConfig{Cmd: "claude"},
				Persistence: plugin.PersistenceConfig{
					Strategy:   "preassign_id",
					ResumeArgs: []string{"--continue"},
				},
			},
			pane: &Pane{
				InstanceArgs: []string{"--dangerously-skip-permissions"},
				PluginState:  map[string]string{"session_id": "abc-123"},
			},
			restoring: true,
			// THIS is the regression test for daemon.go:1147. Before the fix,
			// args were replaced outright with ResumeArgs and the toggle was
			// dropped on every restart.
			want: []string{"--dangerously-skip-permissions", "--continue"},
		},
		{
			name: "RESTORE preassign_id — empty PluginState skips ResumeArgs",
			plugin: &plugin.PanePlugin{
				Command: plugin.CommandConfig{Cmd: "claude", Args: []string{}},
				Persistence: plugin.PersistenceConfig{
					Strategy:   "preassign_id",
					ResumeArgs: []string{"--resume", "{session_id}"},
				},
			},
			pane:      &Pane{},
			restoring: true,
			want:      []string{},
		},
		{
			name: "RESTORE rerun — InstanceArgs preserved, no resume args appended",
			plugin: &plugin.PanePlugin{
				Command: plugin.CommandConfig{Cmd: "ssh"},
				Persistence: plugin.PersistenceConfig{
					Strategy:   "rerun",
					ResumeArgs: []string{"--should-not-appear"}, // ignored for rerun
				},
			},
			pane: &Pane{
				InstanceArgs: []string{"-p", "2222", "user@host"},
			},
			restoring: true,
			want:      []string{"-p", "2222", "user@host"},
		},
		{
			name: "RESTORE session_scrape — InstanceArgs PRESERVED + ResumeArgs APPENDED",
			plugin: &plugin.PanePlugin{
				Command: plugin.CommandConfig{Cmd: "tool"},
				Persistence: plugin.PersistenceConfig{
					Strategy:   "session_scrape",
					ResumeArgs: []string{"--reattach", "{token}"},
				},
			},
			pane: &Pane{
				InstanceArgs: []string{"--verbose"},
				PluginState:  map[string]string{"token": "xyz"},
			},
			restoring: true,
			want:      []string{"--verbose", "--reattach", "xyz"},
		},
		{
			name: "fresh — non-preassign_id strategy ignores StartArgs",
			plugin: &plugin.PanePlugin{
				Command: plugin.CommandConfig{Cmd: "ssh"},
				Persistence: plugin.PersistenceConfig{
					Strategy:  "rerun",
					StartArgs: []string{"--should-not-appear"},
				},
			},
			pane:      &Pane{InstanceArgs: []string{"user@host"}},
			restoring: false,
			want:      []string{"user@host"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveSpawnArgs(tt.plugin, tt.pane, tt.restoring)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("resolveSpawnArgs:\n  got:  %v\n  want: %v", got, tt.want)
			}
		})
	}
}

// TestResolveSpawnArgs_ClaudeResumePromotion covers the restore-path logic
// that upgrades claude-code's resume args from the fallback ["--continue"]
// to ["--resume", "<uuid>"] when the pre-assigned session file is already
// on disk. Without this promotion, N panes sharing a CWD all converge on
// claude's "most recent session in cwd" — the exact bug this guards
// against. The filesystem probe is stubbed so the test never touches
// ~/.claude/.
func TestResolveSpawnArgs_ClaudeResumePromotion(t *testing.T) {
	claudePlugin := &plugin.PanePlugin{
		Name:    "claude-code",
		Command: plugin.CommandConfig{Cmd: "claude"},
		Persistence: plugin.PersistenceConfig{
			Strategy:   "preassign_id",
			StartArgs:  []string{"--session-id", "{session_id}"},
			ResumeArgs: []string{"--continue"},
		},
	}

	tests := []struct {
		name         string
		pane         *Pane
		sessionFound bool // stub return value for claudeSessionExistsFn
		want         []string
	}{
		{
			name: "session file on disk — promoted to --resume",
			pane: &Pane{
				CWD:         `E:\Projects\Stukans\Prototypes\calyx`,
				PluginState: map[string]string{"session_id": "abc-123"},
			},
			sessionFound: true,
			want:         []string{"--resume", "abc-123"},
		},
		{
			name: "session file missing — falls back to --continue",
			pane: &Pane{
				CWD:         `E:\Projects\Stukans\Prototypes\calyx`,
				PluginState: map[string]string{"session_id": "abc-123"},
			},
			sessionFound: false,
			want:         []string{"--continue"},
		},
		{
			name: "InstanceArgs + session file on disk — toggle preserved, --resume appended",
			pane: &Pane{
				CWD:          `E:\Projects\Stukans\Prototypes\calyx`,
				InstanceArgs: []string{"--dangerously-skip-permissions"},
				PluginState:  map[string]string{"session_id": "abc-123"},
			},
			sessionFound: true,
			want:         []string{"--dangerously-skip-permissions", "--resume", "abc-123"},
		},
		{
			name: "InstanceArgs + session file missing — toggle preserved, --continue fallback",
			pane: &Pane{
				CWD:          `E:\Projects\Stukans\Prototypes\calyx`,
				InstanceArgs: []string{"--dangerously-skip-permissions"},
				PluginState:  map[string]string{"session_id": "abc-123"},
			},
			sessionFound: false,
			want:         []string{"--dangerously-skip-permissions", "--continue"},
		},
		{
			name: "empty session_id — no promotion even if stub says found",
			pane: &Pane{
				CWD:         `E:\Projects\Stukans\Prototypes\calyx`,
				PluginState: map[string]string{"session_id": ""},
			},
			sessionFound: true,
			want:         []string{"--continue"},
		},
	}

	origProbe := claudeSessionExistsFn
	t.Cleanup(func() { claudeSessionExistsFn = origProbe })

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claudeSessionExistsFn = func(cwd, sessionID string) bool {
				if cwd != tt.pane.CWD {
					t.Errorf("probe cwd = %q, want %q", cwd, tt.pane.CWD)
				}
				return tt.sessionFound
			}
			got := resolveSpawnArgs(claudePlugin, tt.pane, true)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("resolveSpawnArgs:\n  got:  %v\n  want: %v", got, tt.want)
			}
		})
	}
}

// TestResolveSpawnArgs_ClaudeResumePromotion_NotAppliedToOtherPlugins locks
// in that the claude-specific promotion never fires for other plugins,
// even if they happen to use the preassign_id strategy. The probe should
// not be called at all.
func TestResolveSpawnArgs_ClaudeResumePromotion_NotAppliedToOtherPlugins(t *testing.T) {
	origProbe := claudeSessionExistsFn
	t.Cleanup(func() { claudeSessionExistsFn = origProbe })
	claudeSessionExistsFn = func(cwd, sessionID string) bool {
		t.Errorf("probe was called for a non-claude plugin (cwd=%q, id=%q)", cwd, sessionID)
		return true
	}

	p := &plugin.PanePlugin{
		Name:    "some-other-ai",
		Command: plugin.CommandConfig{Cmd: "tool"},
		Persistence: plugin.PersistenceConfig{
			Strategy:   "preassign_id",
			ResumeArgs: []string{"--resume", "{session_id}"},
		},
	}
	pane := &Pane{
		CWD:         `E:\anywhere`,
		PluginState: map[string]string{"session_id": "xyz"},
	}
	got := resolveSpawnArgs(p, pane, true)
	want := []string{"--resume", "xyz"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("resolveSpawnArgs:\n  got:  %v\n  want: %v", got, want)
	}
}

// TestEscapeClaudeCWD locks in claude's on-disk naming convention for
// per-project session directories. If claude ever changes this (e.g.
// starts percent-encoding instead), this test fails in CI instead of
// panes silently falling back to --continue everywhere.
func TestEscapeClaudeCWD(t *testing.T) {
	tests := []struct {
		name string
		cwd  string
		want string
	}{
		{"windows path", `E:\Projects\Stukans\Prototypes\calyx`, "E--Projects-Stukans-Prototypes-calyx"},
		{"unix path", "/home/user/project", "-home-user-project"},
		{"windows with dot-dir", `C:\Users\artjo\.claude`, "C--Users-artjo-.claude"},
		{"mixed separators", `E:/Projects\mixed`, "E--Projects-mixed"},
		{"root-only windows", `C:\`, "C--"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := escapeClaudeCWD(tt.cwd)
			if got != tt.want {
				t.Errorf("escapeClaudeCWD(%q) = %q, want %q", tt.cwd, got, tt.want)
			}
		})
	}
}

// TestResolveSpawnArgs_ClaudeHookSessionID covers the restore-path logic that
// prefers the SessionStart hook's recorded session id over the preassigned
// one. This is what keeps /clear, /resume, and compaction rotations working:
// the hook file captures the live id and resumeTemplateFor promotes it to
// --resume when the matching jsonl is on disk.
func TestResolveSpawnArgs_ClaudeHookSessionID(t *testing.T) {
	claudePlugin := &plugin.PanePlugin{
		Name:    "claude-code",
		Command: plugin.CommandConfig{Cmd: "claude"},
		Persistence: plugin.PersistenceConfig{
			Strategy:   "preassign_id",
			StartArgs:  []string{"--session-id", "{session_id}"},
			ResumeArgs: []string{"--continue"},
		},
	}

	tests := []struct {
		name              string
		pane              *Pane
		hookID            string
		hookErr           error
		sessionFoundForID string // claudeSessionExistsFn returns true only for this id
		want              []string
	}{
		{
			name: "hook id present, hook file on disk — resume via hook id",
			pane: &Pane{
				ID:          "pane-abc",
				CWD:         `E:\project`,
				PluginState: map[string]string{"session_id": "preassigned-111"},
			},
			hookID:            "rotated-222",
			sessionFoundForID: "rotated-222",
			want:              []string{"--resume", "rotated-222"},
		},
		{
			name: "hook id present, hook file missing, preassigned on disk — falls back to preassigned",
			pane: &Pane{
				ID:          "pane-abc",
				CWD:         `E:\project`,
				PluginState: map[string]string{"session_id": "preassigned-111"},
			},
			hookID:            "rotated-222",
			sessionFoundForID: "preassigned-111",
			want:              []string{"--resume", "preassigned-111"},
		},
		{
			name: "hook id present, neither file on disk — --continue fallback",
			pane: &Pane{
				ID:          "pane-abc",
				CWD:         `E:\project`,
				PluginState: map[string]string{"session_id": "preassigned-111"},
			},
			hookID:            "rotated-222",
			sessionFoundForID: "", // neither matches
			want:              []string{"--continue"},
		},
		{
			name: "no hook file — legacy path, preassigned on disk",
			pane: &Pane{
				ID:          "pane-abc",
				CWD:         `E:\project`,
				PluginState: map[string]string{"session_id": "preassigned-111"},
			},
			hookErr:           os.ErrNotExist,
			sessionFoundForID: "preassigned-111",
			want:              []string{"--resume", "preassigned-111"},
		},
		{
			name: "InstanceArgs + hook id — toggle preserved, hook id wins",
			pane: &Pane{
				ID:           "pane-abc",
				CWD:          `E:\project`,
				InstanceArgs: []string{"--dangerously-skip-permissions"},
				PluginState:  map[string]string{"session_id": "preassigned-111"},
			},
			hookID:            "rotated-222",
			sessionFoundForID: "rotated-222",
			want:              []string{"--dangerously-skip-permissions", "--resume", "rotated-222"},
		},
		{
			// Hook file exists but is empty after trim (hook fired before
			// session_id was extracted). Should fall through to the
			// preassigned-id probe identically to the ErrNotExist case.
			name: "hook returns empty string with no error — fallthrough to preassigned",
			pane: &Pane{
				ID:          "pane-abc",
				CWD:         `E:\project`,
				PluginState: map[string]string{"session_id": "preassigned-111"},
			},
			hookID:            "",
			hookErr:           nil,
			sessionFoundForID: "preassigned-111",
			want:              []string{"--resume", "preassigned-111"},
		},
	}

	// NOTE: subtests are intentionally NOT marked t.Parallel(). They mutate
	// package-level vars (readHookSessionIDFn, claudeSessionExistsFn) and a
	// concurrent run would cross-contaminate. The Cleanup below restores both
	// when the outer test completes.
	origHook := readHookSessionIDFn
	origProbe := claudeSessionExistsFn
	t.Cleanup(func() {
		readHookSessionIDFn = origHook
		claudeSessionExistsFn = origProbe
	})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			readHookSessionIDFn = func(paneID string) (string, error) {
				if paneID != tt.pane.ID {
					t.Errorf("hook read paneID = %q, want %q", paneID, tt.pane.ID)
				}
				return tt.hookID, tt.hookErr
			}
			claudeSessionExistsFn = func(cwd, sessionID string) bool {
				if cwd != tt.pane.CWD {
					t.Errorf("probe cwd = %q, want %q", cwd, tt.pane.CWD)
				}
				return sessionID == tt.sessionFoundForID
			}
			got := resolveSpawnArgs(claudePlugin, tt.pane, true)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("resolveSpawnArgs:\n  got:  %v\n  want: %v", got, tt.want)
			}
		})
	}
}

// TestClaudeHookSpawnPrep covers the fresh-spawn injection helper. It must
// (a) emit --settings + QUIL_PANE_ID env when the hook script is present,
// (b) silently skip both when the script is missing so the spawn proceeds
// like the pre-feature daemon, and (c) warn (not error) when --settings is
// already in the user's args because Claude treats later-wins.
func TestClaudeHookSpawnPrep(t *testing.T) {
	tests := []struct {
		name        string
		statErr     error
		userArgs    []string
		paneID      string
		wantPrefix  bool
		wantEnvVar  string
	}{
		{
			name:       "script present — injects --settings + env",
			statErr:    nil,
			userArgs:   []string{"--enable-auto-mode"},
			paneID:     "pane-abc",
			wantPrefix: true,
			wantEnvVar: "QUIL_PANE_ID=pane-abc",
		},
		{
			name:       "script missing — no injection, no env",
			statErr:    os.ErrNotExist,
			userArgs:   []string{"--enable-auto-mode"},
			paneID:     "pane-abc",
			wantPrefix: false,
			wantEnvVar: "",
		},
		{
			name:       "user already passed --settings — still injects (later-wins warning logged)",
			statErr:    nil,
			userArgs:   []string{"--settings", `{"foo":"bar"}`, "--enable-auto-mode"},
			paneID:     "pane-abc",
			wantPrefix: true,
			wantEnvVar: "QUIL_PANE_ID=pane-abc",
		},
	}

	origStat := claudeHookScriptStatFn
	t.Cleanup(func() { claudeHookScriptStatFn = origStat })

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claudeHookScriptStatFn = func(string) error { return tt.statErr }
			prefix, env := claudeHookSpawnPrep("/tmp/quil", tt.paneID, tt.userArgs)
			if tt.wantPrefix {
				if len(prefix) != 2 || prefix[0] != "--settings" {
					t.Errorf("prefix = %v, want [--settings ...]", prefix)
				}
				if !strings.Contains(prefix[1], `"SessionStart"`) {
					t.Errorf("prefix[1] missing SessionStart key: %s", prefix[1])
				}
			} else {
				if prefix != nil {
					t.Errorf("prefix = %v, want nil", prefix)
				}
			}
			if tt.wantEnvVar == "" {
				if env != nil {
					t.Errorf("env = %v, want nil", env)
				}
			} else {
				if len(env) != 1 || env[0] != tt.wantEnvVar {
					t.Errorf("env = %v, want [%q]", env, tt.wantEnvVar)
				}
			}
		})
	}
}

// TestResolveSpawnArgs_DoesNotMutatePluginArgs guards against accidental
// aliasing — a future change that returns p.Command.Args directly would
// allow callers to mutate the plugin's static config.
func TestResolveSpawnArgs_DoesNotMutatePluginArgs(t *testing.T) {
	p := &plugin.PanePlugin{
		Command: plugin.CommandConfig{
			Cmd:  "bash",
			Args: []string{"-l"},
		},
		Persistence: plugin.PersistenceConfig{Strategy: "cwd_only"},
	}
	got := resolveSpawnArgs(p, &Pane{}, false)
	got[0] = "MUTATED"
	if p.Command.Args[0] != "-l" {
		t.Errorf("plugin.Command.Args was mutated: got %q, want %q", p.Command.Args[0], "-l")
	}
}
