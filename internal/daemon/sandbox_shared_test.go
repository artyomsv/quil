package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/config"
)

// The one rule both the mount and the resume path read. A pane's own choice
// wins over the config; a non-Claude agent never joins the Claude trust domain.
func TestSharesClaudeConfig(t *testing.T) {
	tests := []struct {
		name, choice, plugin string
		cfg, want            bool
	}{
		{"own beats config on", "own", "claude-code", true, false},
		{"shared beats config off", "shared", "claude-code", false, true},
		{"empty follows config on", "", "claude-code", true, true},
		{"empty follows config off", "", "claude-code", false, false},
		{"codex never shares config on", "", "codex", true, false},
		{"opencode never shares even if asked", "shared", "opencode", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sharesClaudeConfig(tt.choice, tt.cfg, tt.plugin); got != tt.want {
				t.Errorf("sharesClaudeConfig(%q, %v, %q) = %v, want %v", tt.choice, tt.cfg, tt.plugin, got, tt.want)
			}
		})
	}
}

// The mount follows the PANE's choice, not the config: a Shared pane on a
// config-off daemon must mount the shared directory, an Own pane on a
// config-on daemon must not, and a codex container never does — it could
// otherwise plant hooks every Shared Claude pane then runs.
func TestPrepareSandbox_SharedRootFollowsThePaneChoice(t *testing.T) {
	for _, tc := range []struct {
		name, choice, plugin string
		cfgShared, want      bool
	}{
		{"shared pane config off", "shared", "claude-code", false, true},
		{"own pane config on", "own", "claude-code", true, false},
		{"unset pane config on", "", "claude-code", true, true},
		{"codex pane config on", "", "codex", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, pane, _ := sandboxCallsiteFixture(t)
			d.cfg = config.Default()
			d.cfg.Sandbox.SharedClaudeConfig = tc.cfgShared
			pane.SandboxClaudeConfig = tc.choice
			stubNoSavedToken(t)
			m, err := d.prepareSandbox(context.Background(), pane, tc.plugin, "img:1")
			if err != nil {
				t.Fatalf("prepareSandbox: %v", err)
			}
			if got := m.SharedClaudeRoot != ""; got != tc.want {
				t.Errorf("SharedClaudeRoot = %q, want shared=%v", m.SharedClaudeRoot, tc.want)
			}
		})
	}
}

// Shared mode mounts one directory over the per-pane one, but the CONTAINER
// path is /quil/claude either way — so the resume path must map to the same
// directory the mount used, decided by the same rule. A Shared pane on a
// config-OFF daemon is the case the old config-only switch got wrong: it
// mapped to the per-pane root, classified the transcript missing, and the
// restored pane took --session-id for a session that has one — exit 129.
func TestHostTranscriptPath_FollowsThePaneChoice(t *testing.T) {
	for _, tc := range []struct {
		name, choice string
		cfg, shared  bool
	}{
		{"shared pane config off", "shared", false, true},
		{"own pane config on", "own", true, false},
		{"unset pane config on", "", true, true},
		{"unset pane config off", "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("QUIL_HOME", home)
			shared := sharedClaudeConfigDir(home)
			own := sandboxClaudeConfigDir(home, "pane1")
			for _, dir := range []string{shared, own} {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
			}
			prev := sharedClaudeConfigDefault
			setSharedClaudeConfigDefault(tc.cfg)
			t.Cleanup(func() { sharedClaudeConfigDefault = prev })

			pane := &Pane{ID: "pane1", Type: "claude-code", SandboxImage: "img", SandboxClaudeConfig: tc.choice}
			got := hostTranscriptPath(pane, "/quil/claude/projects/-w/abc.jsonl")
			if got == "" {
				t.Fatal("a valid transcript path was rejected")
			}
			want := own
			if tc.shared {
				want = shared
			}
			if !strings.HasPrefix(filepath.ToSlash(got), filepath.ToSlash(want)+"/") {
				t.Errorf("hostTranscriptPath = %q, want it under %q", got, want)
			}
		})
	}
}
