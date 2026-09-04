package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/claudehook"
	"github.com/artyomsv/quil/internal/codexhook"
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
		name      string
		plugin    *plugin.PanePlugin
		pane      *Pane
		restoring bool
		want      []string
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
			got := resolveSpawnArgs(tt.plugin, tt.pane, tt.restoring, "", claimAny)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("resolveSpawnArgs:\n  got:  %v\n  want: %v", got, tt.want)
			}
		})
	}
}

// TestResolveSpawnArgs_ClaudeResumePromotion covers the restore-path logic that
// resumes a claude-code pane's own session instead of the plugin's --continue
// fallback. Without it, N panes sharing a CWD all converge on claude's
// most-recent-session-in-cwd lookup — the bug this guards against.
//
// Ids here are canonical uuids ON PURPOSE. An earlier version of this table used
// "abc-123", which the argv shape gate rejects outright, so every case passed
// through the malformed-id branch and the promotion under test was never
// exercised. The transcript probe is stubbed so the test never touches ~/.claude.
func TestResolveSpawnArgs_ClaudeResumePromotion(t *testing.T) {
	claudePlugin := &plugin.PanePlugin{
		Name:    "claude-code",
		Command: plugin.CommandConfig{Cmd: "claude", Sessions: "claude"},
		Persistence: plugin.PersistenceConfig{
			Strategy:   "preassign_id",
			StartArgs:  []string{"--session-id", "{session_id}"},
			ResumeArgs: []string{"--continue"},
		},
	}
	const sess = "2db05609-f1d5-4576-b5b2-ff114519726b"
	// Joined with filepath so the separator matches the test platform: the
	// id/path binding uses filepath.Base, which only splits on the host's
	// separator, so a hard-coded Windows path would silently fail to bind on Linux.
	transcript := filepath.Join("/home/u/.claude/projects/E--proj", sess+".jsonl")

	tests := []struct {
		name  string
		pane  *Pane
		found bool // does the recorded transcript exist?
		want  []string
	}{
		{
			name: "transcript located — resumed",
			pane: &Pane{
				CWD: `E:\Projects\Stukans\Prototypes\calyx`,
				PluginState: map[string]string{
					"session_id":      sess,
					"transcript_path": transcript,
				},
			},
			found: true,
			want:  []string{"--resume", sess},
		},
		{
			// The behaviour this PR inverted: a session we cannot find is still
			// OUR session. --continue would hand the pane a sibling's conversation.
			name: "transcript not located — still resumed, never --continue",
			pane: &Pane{
				CWD:         `E:\Projects\Stukans\Prototypes\calyx`,
				PluginState: map[string]string{"session_id": sess},
			},
			found: false,
			want:  []string{"--resume", sess},
		},
		{
			name: "InstanceArgs preserved alongside the resume",
			pane: &Pane{
				CWD:          `E:\Projects\Stukans\Prototypes\calyx`,
				InstanceArgs: []string{"--dangerously-skip-permissions"},
				PluginState: map[string]string{
					"session_id":      sess,
					"transcript_path": transcript,
				},
			},
			found: true,
			want:  []string{"--dangerously-skip-permissions", "--resume", sess},
		},
		{
			name: "empty session_id — nothing recorded, configured fallback stands",
			pane: &Pane{
				CWD:         `E:\Projects\Stukans\Prototypes\calyx`,
				PluginState: map[string]string{"session_id": ""},
			},
			found: true,
			want:  []string{"--continue"},
		},
	}

	origHook, origProbe := readHookSessionFn, transcriptExistsFn
	t.Cleanup(func() { readHookSessionFn, transcriptExistsFn = origHook, origProbe })
	readHookSessionFn = func(string) (claudehook.SessionRecord, error) {
		return claudehook.SessionRecord{}, os.ErrNotExist
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transcriptExistsFn = func(p string) (bool, bool) { return tt.found && p == transcript, true }
			got := resolveSpawnArgs(claudePlugin, tt.pane, true, "", claimAny)
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
	origProbe := transcriptExistsFn
	t.Cleanup(func() { transcriptExistsFn = origProbe })
	transcriptExistsFn = func(path string) (bool, bool) {
		t.Errorf("probe was called for a non-claude plugin (path=%q)", path)
		return true, true
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
	got := resolveSpawnArgs(p, pane, true, "", claimAny)
	want := []string{"--resume", "xyz"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("resolveSpawnArgs:\n  got:  %v\n  want: %v", got, want)
	}
}

// TestResolveSpawnArgs_ClaudeHookSessionID covers the restore-path rule that
// prefers the SessionStart hook's recorded id over the preassigned one. That is
// what keeps /clear, /resume and compaction rotations working: the hook file
// captures the live id.
//
// The hook id wins on AUTHORITY, not on whether its transcript could be found —
// ranking a located preassigned id above an unlocated hook id would resume the
// pre-rotation conversation, which is the silent wrong-session failure this
// package exists to prevent. It yields only to positive proof: a recorded path
// that names the hook id and is not there.
func TestResolveSpawnArgs_ClaudeHookSessionID(t *testing.T) {
	claudePlugin := &plugin.PanePlugin{
		Name:    "claude-code",
		Command: plugin.CommandConfig{Cmd: "claude", Sessions: "claude"},
		Persistence: plugin.PersistenceConfig{
			Strategy:   "preassign_id",
			StartArgs:  []string{"--session-id", "{session_id}"},
			ResumeArgs: []string{"--continue"},
		},
	}
	const (
		rotated     = "9c7c1f4a-2b6d-4f2e-9a1b-77c0d5e3a412"
		preassigned = "2db05609-f1d5-4576-b5b2-ff114519726b"
	)
	rotatedPath := filepath.Join("/home/u/.claude/projects/E--proj", rotated+".jsonl")
	preassignedPath := filepath.Join("/home/u/.claude/projects/E--proj", preassigned+".jsonl")

	tests := []struct {
		name    string
		hookRec claudehook.SessionRecord
		pane    *Pane
		onDisk  []string // transcripts that exist
		want    []string
	}{
		{
			name:    "hook id located — resumed",
			hookRec: claudehook.SessionRecord{ID: rotated, TranscriptPath: rotatedPath},
			pane: &Pane{ID: "pane-abc", CWD: `E:\project`,
				PluginState: map[string]string{"session_id": preassigned}},
			onDisk: []string{rotatedPath, preassignedPath},
			want:   []string{"--resume", rotated},
		},
		{
			name:    "hook id unlocated, preassigned located — hook id still wins",
			hookRec: claudehook.SessionRecord{ID: rotated}, // no path recorded, so no proof
			pane: &Pane{ID: "pane-abc", CWD: `E:\project`,
				PluginState: map[string]string{
					"session_id":      preassigned,
					"transcript_path": preassignedPath,
				}},
			onDisk: []string{preassignedPath},
			want:   []string{"--resume", rotated},
		},
		{
			name:    "hook id PROVEN gone — yields to the preassigned id",
			hookRec: claudehook.SessionRecord{ID: rotated, TranscriptPath: rotatedPath},
			pane: &Pane{ID: "pane-abc", CWD: `E:\project`,
				PluginState: map[string]string{
					"session_id":      preassigned,
					"transcript_path": preassignedPath,
				}},
			onDisk: []string{preassignedPath}, // rotatedPath is absent
			want:   []string{"--resume", preassigned},
		},
		{
			name:    "no hook record — preassigned id resumed",
			hookRec: claudehook.SessionRecord{},
			pane: &Pane{ID: "pane-abc", CWD: `E:\project`,
				PluginState: map[string]string{"session_id": preassigned}},
			onDisk: nil,
			want:   []string{"--resume", preassigned},
		},
	}

	origHook, origProbe := readHookSessionFn, transcriptExistsFn
	t.Cleanup(func() { readHookSessionFn, transcriptExistsFn = origHook, origProbe })

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			readHookSessionFn = func(paneID string) (claudehook.SessionRecord, error) {
				if paneID != tt.pane.ID {
					t.Errorf("hook read paneID = %q, want %q", paneID, tt.pane.ID)
				}
				if tt.hookRec.ID == "" {
					return claudehook.SessionRecord{}, os.ErrNotExist
				}
				return tt.hookRec, nil
			}
			transcriptExistsFn = func(p string) (bool, bool) {
				for _, d := range tt.onDisk {
					if d == p {
						return true, true
					}
				}
				return false, true
			}
			got := resolveSpawnArgs(claudePlugin, tt.pane, true, "", claimAny)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("resolveSpawnArgs:\n  got:  %v\n  want: %v", got, tt.want)
			}
		})
	}
}

// TestClaudeHookSpawnPrep covers the fresh-spawn injection helper. It must
// (a) emit --settings + QUIL_PANE_ID/QUIL_HOOK_MODE/QUIL_HOOK_HOME env when the
// quild executable resolves, (b) silently skip both when the executable cannot
// be resolved so the spawn proceeds like the pre-feature daemon, and (c) warn
// (not error) when --settings is already in the user's args (precedence unverified).
func TestClaudeHookSpawnPrep(t *testing.T) {
	tests := []struct {
		name       string
		exeErr     error
		userArgs   []string
		paneID     string
		wantPrefix bool
		wantEnvVar bool
	}{
		{
			name:       "exe resolves — injects --settings + env",
			exeErr:     nil,
			userArgs:   []string{"--enable-auto-mode"},
			paneID:     "pane-abc",
			wantPrefix: true,
			wantEnvVar: true,
		},
		{
			name:       "exe unresolvable — no injection, no env",
			exeErr:     os.ErrNotExist,
			userArgs:   []string{"--enable-auto-mode"},
			paneID:     "pane-abc",
			wantPrefix: false,
			wantEnvVar: false,
		},
		{
			name:       "user already passed --settings — still injects (warning logged)",
			exeErr:     nil,
			userArgs:   []string{"--settings", `{"foo":"bar"}`, "--enable-auto-mode"},
			paneID:     "pane-abc",
			wantPrefix: true,
			wantEnvVar: true,
		},
	}

	origExe := claudeHookExeFn
	t.Cleanup(func() { claudeHookExeFn = origExe })

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claudeHookExeFn = func() (string, error) {
				if tt.exeErr != nil {
					return "", tt.exeErr
				}
				return "/opt/quil/quild", nil
			}
			// Writable: claudeHookSpawnPrep now writes the hook settings to a
			// per-pane file under <quilDir>/sessions/.
			quilDir := t.TempDir()
			prefix, env := claudeHookSpawnPrep(quilDir, tt.paneID, "default", tt.userArgs)
			if tt.wantPrefix {
				if len(prefix) != 2 || prefix[0] != "--settings" {
					t.Errorf("prefix = %v, want [--settings ...]", prefix)
				}
				// The argument must be a bare PATH, not inline JSON — that is
				// the whole point of the file indirection, since cmd.exe
				// re-splits quotes when claude is reached through its .cmd shim.
				if strings.ContainsAny(prefix[1], `"{}`) {
					t.Errorf("prefix[1] = %q contains shell metacharacters; want a bare path", prefix[1])
				}
				body, err := os.ReadFile(prefix[1])
				if err != nil {
					t.Fatalf("read settings file %s: %v", prefix[1], err)
				}
				if !strings.Contains(string(body), `"SessionStart"`) {
					t.Errorf("settings file missing SessionStart key: %s", body)
				}
				if !strings.Contains(string(body), "claude-hook") {
					t.Errorf("settings file missing native claude-hook command: %s", body)
				}
			} else if prefix != nil {
				t.Errorf("prefix = %v, want nil", prefix)
			}
			if !tt.wantEnvVar {
				if env != nil {
					t.Errorf("env = %v, want nil", env)
				}
			} else {
				// claudeHookSpawnPrep returns QUIL_PANE_ID, QUIL_HOOK_MODE,
				// and QUIL_HOOK_HOME so the native subcommand resolves its data dir
				// and tier independent of the daemon's inherited environment.
				if len(env) != 3 {
					t.Fatalf("env = %v, want 3 entries (pane id + hook mode + quil hook home)", env)
				}
				if env[0] != "QUIL_PANE_ID="+tt.paneID {
					t.Errorf("env[0] = %q, want QUIL_PANE_ID=%s", env[0], tt.paneID)
				}
				if env[1] != "QUIL_HOOK_MODE=default" {
					t.Errorf("env[1] = %q, want QUIL_HOOK_MODE=default", env[1])
				}
				if env[2] != "QUIL_HOOK_HOME="+quilDir {
					t.Errorf("env[2] = %q, want QUIL_HOOK_HOME=%s", env[2], quilDir)
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
	got := resolveSpawnArgs(p, &Pane{}, false, "", claimAny)
	got[0] = "MUTATED"
	if p.Command.Args[0] != "-l" {
		t.Errorf("plugin.Command.Args was mutated: got %q, want %q", p.Command.Args[0], "-l")
	}
}

// TestResolveSpawnArgs_OpencodeResume covers the opencode restore branch of
// resumeTemplateFor: when our JS plugin recorded a session id we promote to
// --session <id>, otherwise we fall back to the configured --continue.
//
// Unlike the claude-code test there is no session-exists probe; opencode is
// asked to resume the id and handles staleness itself.
func TestResolveSpawnArgs_OpencodeResume(t *testing.T) {
	opencodePlugin := &plugin.PanePlugin{
		Name:    "opencode",
		Command: plugin.CommandConfig{Cmd: "opencode"},
		Persistence: plugin.PersistenceConfig{
			Strategy:   "session_scrape",
			ResumeArgs: []string{"--continue"},
		},
	}

	tests := []struct {
		name    string
		pane    *Pane
		hookID  string
		hookErr error
		want    []string
	}{
		{
			name:   "hook id present — resume via --session",
			pane:   &Pane{ID: "pane-abc"},
			hookID: "sess-1234",
			want:   []string{"--session", "sess-1234"},
		},
		{
			name:    "hook id missing (ErrNotExist) — fallback to --continue",
			pane:    &Pane{ID: "pane-abc"},
			hookErr: os.ErrNotExist,
			want:    []string{"--continue"},
		},
		{
			name:   "hook id empty string — fallback to --continue",
			pane:   &Pane{ID: "pane-abc"},
			hookID: "",
			want:   []string{"--continue"},
		},
		{
			name:   "InstanceArgs + hook id — toggle preserved, --session appended",
			pane:   &Pane{ID: "pane-abc", InstanceArgs: []string{"--print-logs"}},
			hookID: "sess-1234",
			want:   []string{"--print-logs", "--session", "sess-1234"},
		},
		{
			// Guards opencodeResumeTemplate's shape check: a malformed id
			// (corrupted file, manual edit) must not be passed to opencode
			// as a discrete argv entry; we fall back to --continue so the
			// pane recovers a coherent state instead of erroring on a
			// nonsense --session value.
			name:   "hook id fails shape validation — fallback to --continue",
			pane:   &Pane{ID: "pane-abc"},
			hookID: "not a valid id with spaces\nand newlines",
			want:   []string{"--continue"},
		},
		{
			name:   "hook id with NUL byte — fallback to --continue",
			pane:   &Pane{ID: "pane-abc"},
			hookID: "sess\x00abc",
			want:   []string{"--continue"},
		},
	}

	// Subtests mutate readOpencodeSessionIDFn — not parallel-safe.
	orig := readOpencodeSessionIDFn
	t.Cleanup(func() { readOpencodeSessionIDFn = orig })

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			readOpencodeSessionIDFn = func(paneID string) (string, error) {
				if paneID != tt.pane.ID {
					t.Errorf("hook read paneID = %q, want %q", paneID, tt.pane.ID)
				}
				return tt.hookID, tt.hookErr
			}
			got := resolveSpawnArgs(opencodePlugin, tt.pane, true, "", claimAny)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("resolveSpawnArgs:\n  got:  %v\n  want: %v", got, tt.want)
			}
		})
	}
}

// TestTemplateHasPlaceholder locks in the brace-detection predicate that
// gates the restore branch's static-vs-dynamic template handling. Without
// this gate, session_scrape panes with empty PluginState would drop their
// --continue fallback on restore — a real bug found during the opencode
// implementation. Direct coverage so a regression here is visible at the
// unit level instead of only via the resume matrix.
func TestTemplateHasPlaceholder(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		template []string
		want     bool
	}{
		{"nil", nil, false},
		{"empty", []string{}, false},
		{"static single arg", []string{"--continue"}, false},
		{"static multi arg", []string{"--session", "fixed-id"}, false},
		{"placeholder", []string{"--session", "{session_id}"}, true},
		{"placeholder in middle of arg", []string{"prefix-{id}-suffix"}, true},
		{"open brace only", []string{"{partial"}, false},
		{"close brace only", []string{"partial}"}, false},
		{"matched outside arg boundaries", []string{"{a", "b}"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := templateHasPlaceholder(tt.template); got != tt.want {
				t.Errorf("templateHasPlaceholder(%v) = %v, want %v", tt.template, got, tt.want)
			}
		})
	}
}

// TestOpencodeSpawnPrep covers the env-injection helper: it must emit the
// three env vars when the JS plugin file is present, and nil when missing
// so the spawn proceeds without session tracking rather than failing.
func TestOpencodeSpawnPrep(t *testing.T) {
	tests := []struct {
		name        string
		statErr     error
		paneID      string
		wantEnv     bool
		wantPaneEnv string
	}{
		{
			name:        "script present — injects three env vars",
			statErr:     nil,
			paneID:      "pane-abc",
			wantEnv:     true,
			wantPaneEnv: "QUIL_PANE_ID=pane-abc",
		},
		{
			name:    "script missing — no injection",
			statErr: os.ErrNotExist,
			paneID:  "pane-abc",
			wantEnv: false,
		},
	}

	orig := opencodeHookScriptStatFn
	t.Cleanup(func() { opencodeHookScriptStatFn = orig })

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opencodeHookScriptStatFn = func(string) error { return tt.statErr }
			env := opencodeSpawnPrep("/tmp/quil", tt.paneID, "default")
			if tt.wantEnv {
				if len(env) != 4 {
					t.Fatalf("env = %v, want 4 entries (pane, home, mode, config)", env)
				}
				if env[0] != tt.wantPaneEnv {
					t.Errorf("env[0] = %q, want %q", env[0], tt.wantPaneEnv)
				}
				if env[1] != "QUIL_HOOK_HOME=/tmp/quil" {
					t.Errorf("env[1] = %q, want QUIL_HOOK_HOME=/tmp/quil", env[1])
				}
				if env[2] != "QUIL_HOOK_MODE=default" {
					t.Errorf("env[2] = %q, want QUIL_HOOK_MODE=default", env[2])
				}
				if !strings.HasPrefix(env[3], "OPENCODE_CONFIG_CONTENT=") {
					t.Errorf("env[3] = %q, want OPENCODE_CONFIG_CONTENT=... prefix", env[3])
				}
				if !strings.Contains(env[3], "quil-session-tracker.js") {
					t.Errorf("env[3] missing plugin filename: %s", env[3])
				}
				// Round-trip-parse the inline config so a future regression in
				// configContentSchema's wire format gets caught here, not by
				// opencode silently ignoring the plugin entry at load time.
				jsonPart := strings.TrimPrefix(env[3], "OPENCODE_CONFIG_CONTENT=")
				var parsed struct {
					Plugin []string `json:"plugin"`
				}
				if err := json.Unmarshal([]byte(jsonPart), &parsed); err != nil {
					t.Errorf("OPENCODE_CONFIG_CONTENT not valid JSON: %v (%s)", err, jsonPart)
				} else if len(parsed.Plugin) != 1 || !strings.HasSuffix(parsed.Plugin[0], "quil-session-tracker.js") {
					t.Errorf("parsed.Plugin = %v, want one entry ending in quil-session-tracker.js", parsed.Plugin)
				}
			} else {
				if env != nil {
					t.Errorf("env = %v, want nil", env)
				}
			}
		})
	}
}

// A settings-write failure must degrade to spawning WITHOUT rotation tracking,
// never to failing the spawn: this turned a pure function into an effectful one,
// and a full or read-only disk must not stop a pane from opening.
//
// Reachable only because WriteSettingsFile rejects a quil dir holding a
// character a shell would re-interpret — before that guard existed there was no
// way to make the write fail without an unwritable filesystem.
func TestClaudeHookSpawnPrep_WriteFailureDegradesInsteadOfFailing(t *testing.T) {
	orig := claudeHookExeFn
	claudeHookExeFn = func() (string, error) { return "/fake/quild", nil }
	defer func() { claudeHookExeFn = orig }()

	badDir := filepath.Join(t.TempDir(), "R&D")

	prefix, env := claudeHookSpawnPrep(badDir, "pane-abc123", "default", nil)
	if prefix != nil {
		t.Errorf("prefix = %v, want nil so the spawn proceeds with no --settings", prefix)
	}
	if env != nil {
		t.Errorf("env = %v, want nil", env)
	}
}

// TestResolveSpawnArgs_CodexResume covers the codex restore branch of
// resumeTemplateFor: a recorded id becomes `resume <id>`, anything else
// starts FRESH — the plugin ships resume_args = [] because `resume --last`
// is codex's most-recent-session lookup, the same sibling trap as
// `claude --continue`.
func TestResolveSpawnArgs_CodexResume(t *testing.T) {
	codexPlugin := &plugin.PanePlugin{
		Name:    plugin.CodexPluginName,
		Command: plugin.CommandConfig{Cmd: "codex"},
		Persistence: plugin.PersistenceConfig{
			Strategy:   "session_scrape",
			ResumeArgs: nil,
		},
	}
	const sid = "01a05db1-9f44-73b2-b426-8aad5f5232f4"

	tests := []struct {
		name string
		pane *Pane
		rec  codexhook.SessionRecord
		err  error
		want []string
	}{
		{"recorded id — resume by id", &Pane{ID: "pane-abc"}, codexhook.SessionRecord{ID: sid}, nil, []string{"resume", sid}},
		{"recorded id keeps runtime toggles", &Pane{ID: "pane-abc", InstanceArgs: []string{"--search"}}, codexhook.SessionRecord{ID: sid}, nil, []string{"--search", "resume", sid}},
		{"no record — fresh start", &Pane{ID: "pane-abc"}, codexhook.SessionRecord{}, os.ErrNotExist, nil},
		{"empty id — fresh start", &Pane{ID: "pane-abc"}, codexhook.SessionRecord{}, nil, nil},
		{"flag-shaped id — fresh start", &Pane{ID: "pane-abc"}, codexhook.SessionRecord{ID: "--last"}, nil, nil},
		{"non-uuid id — fresh start", &Pane{ID: "pane-abc"}, codexhook.SessionRecord{ID: "ses_abc"}, nil, nil},
	}

	// Subtests mutate readCodexSessionFn — not parallel-safe.
	orig := readCodexSessionFn
	t.Cleanup(func() { readCodexSessionFn = orig })

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			readCodexSessionFn = func(paneID string) (codexhook.SessionRecord, error) {
				if paneID != tt.pane.ID {
					t.Errorf("read paneID = %q, want %q", paneID, tt.pane.ID)
				}
				return tt.rec, tt.err
			}
			got := resolveSpawnArgs(codexPlugin, tt.pane, true, "", claimAny)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("resolveSpawnArgs:\n  got:  %v\n  want: %v", got, tt.want)
			}
			if tt.want != nil {
				tt.pane.PluginMu.Lock()
				sidState := tt.pane.PluginState["session_id"]
				tt.pane.PluginMu.Unlock()
				if sidState != tt.rec.ID {
					t.Errorf("PluginState[session_id] = %q, want %q", sidState, tt.rec.ID)
				}
			}
		})
	}
}

// TestResumeTemplateFor_CodexArmWinsOverClaudeCapability: a codex pane must
// never take the claude arm even if its TOML is edited to say
// sessions = "claude" — that arm would prepend --settings and read the wrong
// record file.
func TestResumeTemplateFor_CodexArmWinsOverClaudeCapability(t *testing.T) {
	orig := readCodexSessionFn
	t.Cleanup(func() { readCodexSessionFn = orig })
	readCodexSessionFn = func(string) (codexhook.SessionRecord, error) {
		return codexhook.SessionRecord{ID: "01a05db1-9f44-73b2-b426-8aad5f5232f4", TranscriptPath: "/r/x.jsonl"}, nil
	}
	p := &plugin.PanePlugin{
		Name:        plugin.CodexPluginName,
		Command:     plugin.CommandConfig{Cmd: "codex", Sessions: plugin.ClaudeSessionSource},
		Persistence: plugin.PersistenceConfig{Strategy: "session_scrape"},
	}
	pane := &Pane{ID: "pane-abc"}
	got := resumeTemplateFor(p, pane, claimAny)
	if !reflect.DeepEqual(got, []string{"resume", "{session_id}"}) {
		t.Errorf("template = %v", got)
	}
	pane.PluginMu.Lock()
	defer pane.PluginMu.Unlock()
	if pane.PluginState["transcript_path"] != "/r/x.jsonl" {
		t.Errorf("transcript_path not recorded with the id: %v", pane.PluginState)
	}
}
