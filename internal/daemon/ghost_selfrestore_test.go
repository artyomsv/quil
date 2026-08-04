package daemon

import (
	"testing"

	"github.com/artyomsv/quil/internal/plugin"
)

// A pane whose resume strategy hands the respawned child a session id gets its
// whole transcript painted back BY THE CHILD. Replaying Quil's saved copy as
// well puts the same conversation in the grid twice, and the join is corrupted
// rather than merely redundant: the child starts writing wherever the replay
// left the cursor, so its banner lands on top of the saved prompt line —
// "passE:\Projects\Stukans\monorepoo cycle)" is a working-directory line
// rendered through a permissions line (reported 2026-08-02 and 2026-08-03).
//
// handleAttach's own behaviour is covered by
// TestHandleAttach_SkipsGhostReplayWhenTheChildRepaints (integration tag);
// this pins the predicate that decides it.
func TestRestoresOwnHistory_IsAboutTheStrategyNotThePluginName(t *testing.T) {
	cases := []struct {
		strategy string
		want     bool
	}{
		{"preassign_id", true},   // claude-code → --resume <id>
		{"session_scrape", true}, // opencode   → --session <id>
		{"rerun", false},         // re-runs a command that starts from nothing
		{"cwd_only", false},      // a shell reprints none of its scrollback
		{"none", false},
		{"", false},
	}
	for _, tc := range cases {
		p := &plugin.PanePlugin{}
		p.Persistence.Strategy = tc.strategy
		if got := restoresOwnHistory(p); got != tc.want {
			t.Errorf("strategy %q: restoresOwnHistory = %v, want %v", tc.strategy, got, tc.want)
		}
	}
	if restoresOwnHistory(nil) {
		t.Error("an unknown plugin must not be assumed to restore its own history")
	}
}

// The shipped defaults must actually select the behaviour: a strategy renamed
// in TOML without updating restoresOwnHistory would silently restore the bug.
func TestShippedPlugins_SelectTheRightGhostBehaviour(t *testing.T) {
	dir := t.TempDir()
	if _, err := plugin.EnsureDefaultPlugins(dir); err != nil {
		t.Fatalf("EnsureDefaultPlugins: %v", err)
	}
	reg := plugin.NewRegistry()
	if err := reg.LoadFromDir(dir); err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}

	repaints := map[string]bool{
		"claude-code": true,
		"opencode":    true,
		"terminal":    false,
		"ssh":         false,
	}
	for name, want := range repaints {
		p := reg.Get(name)
		if p == nil {
			t.Fatalf("builtin plugin %q missing from the registry", name)
		}
		if got := restoresOwnHistory(p); got != want {
			t.Errorf("%s (strategy %q): restoresOwnHistory = %v, want %v",
				name, p.Persistence.Strategy, got, want)
		}
	}
}
