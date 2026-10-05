package notifyclass

import (
	"testing"

	"github.com/artyomsv/quil/internal/config"
)

func TestGroup(t *testing.T) {
	cases := map[string]string{
		"hook.claude.Stop":             AgentTurn,
		"hook.opencode.chat.message":   AgentTurn,
		"hook.codex.PermissionRequest": AgentBlocked,
		"hook.newtool.Unheard":         System,
		"hook.nosource":                System,
		"bell":                         AgentBlocked,
		"pane_destroyed":               Pane,
		"worktree_failed":              System,
		"command_complete":             Commands,
		"never_seen_before":            System,
	}
	for typ, want := range cases {
		if got := Group(typ); got != want {
			t.Errorf("Group(%q) = %q, want %q", typ, got, want)
		}
	}
}

func TestShownGroups_Defaults(t *testing.T) {
	s := ShownGroups(config.Default().Notification.Events)
	if !s[AgentTurn] || s[Commands] || s[Idle] {
		t.Errorf("defaults = %v", s)
	}
	if len(s) != 10 {
		t.Errorf("%d groups, want 10", len(s))
	}
}

// The copies the page receives are copies: a caller cannot edit the tables.
func TestTables_AreCopies(t *testing.T) {
	h := HookGroups()
	h["Stop"] = Idle
	p := PlainGroups()
	p["bell"] = Idle
	if Group("hook.claude.Stop") != AgentTurn || Group("bell") != AgentBlocked {
		t.Error("editing a returned table changed Group")
	}
}
