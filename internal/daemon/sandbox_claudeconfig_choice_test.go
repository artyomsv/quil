package daemon

import (
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

// Any IPC client can set claude_config, and the two values mount different
// directories — one of them a trust domain shared with other panes. An unknown
// value follows the config rather than being stored and acted on.
func TestApplySandboxSpec_RecordsTheClaudeConfigChoice(t *testing.T) {
	tests := []struct{ name, wire, want string }{
		{"own is recorded", "own", "own"},
		{"shared is recorded", "shared", "shared"},
		{"absent follows the config", "", ""},
		{"unknown is refused", "everyone", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pane := &Pane{ID: "p1"}
			if err := applySandboxSpec(pane, &ipc.SandboxSpec{Image: "img:1", ClaudeConfig: tt.wire}); err != nil {
				t.Fatalf("applySandboxSpec: %v", err)
			}
			pane.PluginMu.Lock()
			got := pane.SandboxClaudeConfig
			pane.PluginMu.Unlock()
			if got != tt.want {
				t.Errorf("SandboxClaudeConfig = %q, want %q", got, tt.want)
			}
		})
	}
}

// The choice is persisted: the resume path maps a transcript through it, so a
// pane restored under the other directory looks for its session where it is
// not. An older snapshot (no key) restores as "" — follow the config, which is
// what those panes did before the choice existed.
func TestSnapshot_ClaudeConfigChoiceSurvivesTheRoundTrip(t *testing.T) {
	for _, tc := range []struct{ name, choice string }{
		{"shared", "shared"},
		{"own", "own"},
		{"unset", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			d := newTestDaemonInDir(t, dir)
			tab := d.session.CreateTab("t")
			pane := mustPane(t, d, tab.ID)
			pane.Type = "claude-code"
			pane.SandboxImage = "img:1"
			pane.SandboxClaudeConfig = tc.choice
			d.snapshot()

			restored := newTestDaemonInDir(t, dir)
			if err := restored.restoreWorkspace(); err != nil {
				t.Fatalf("restore: %v", err)
			}
			var got *Pane
			for _, tb := range restored.session.Tabs() {
				for _, p := range restored.session.Panes(tb.ID) {
					got = p
				}
			}
			if got == nil {
				t.Fatal("no pane survived the round trip")
			}
			if got.SandboxClaudeConfig != tc.choice {
				t.Errorf("SandboxClaudeConfig = %q, want %q", got.SandboxClaudeConfig, tc.choice)
			}
		})
	}
}

// A token pane never enters the shared directory, whoever asks. Any IPC
// client (MCP) can send token + shared; stored as-is, the token pane and the
// browser panes in that directory would keep resetting each other's
// onboarding through the .quil-auth stamp.
func TestApplySandboxSpec_TokenPaneNeverShares(t *testing.T) {
	pane := &Pane{ID: "p1"}
	if err := applySandboxSpec(pane, &ipc.SandboxSpec{Image: "img:1", Auth: "token", ClaudeConfig: "shared"}); err != nil {
		t.Fatalf("applySandboxSpec: %v", err)
	}
	pane.PluginMu.Lock()
	auth, cc := pane.SandboxAuth, pane.SandboxClaudeConfig
	pane.PluginMu.Unlock()
	if auth != "token" || cc != "own" {
		t.Errorf("auth=%q claude_config=%q, want token + own", auth, cc)
	}
}
