package daemon

import (
	"reflect"
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
