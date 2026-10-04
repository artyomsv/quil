package daemon

import (
	"testing"

	"github.com/artyomsv/quil/internal/config"
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
	d := &Daemon{cfg: config.Default()}
	pane := &Pane{ID: "p1"}
	if err := d.applySandboxSpecFor(pane, &ipc.SandboxSpec{Image: "img:1", Auth: "token", ClaudeConfig: "shared"}); err != nil {
		t.Fatalf("applySandboxSpec: %v", err)
	}
	pane.PluginMu.Lock()
	auth, cc := pane.SandboxAuth, pane.SandboxClaudeConfig
	pane.PluginMu.Unlock()
	if auth != "token" || cc != "own" {
		t.Errorf("auth=%q claude_config=%q, want token + own", auth, cc)
	}
}

// The token-never-shares rule must hold for the EFFECTIVE values, not only the
// literal wire pair: an empty auth or an empty choice is filled in from the
// daemon's config, and either way the pane would otherwise end up a token pane
// in the shared directory. A pane whose effective values do not conflict keeps
// its recorded choice, empty included.
func TestSettleSandboxSharing_UsesTheEffectiveValues(t *testing.T) {
	tests := []struct {
		name         string
		auth, choice string
		cfgAuth      string
		cfgShared    bool
		wantChoice   string
	}{
		{"shared choice, auth from a token config", "", "shared", "token", false, "own"},
		{"token auth, choice from a shared config", "token", "", "", true, "own"},
		{"both from a token+shared config", "", "", "token", true, "own"},
		{"browser follows a shared config", "browser", "", "", true, ""},
		{"browser shared stays shared", "browser", "shared", "token", false, "shared"},
		{"token own stays own", "token", "own", "", true, "own"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &Daemon{cfg: config.Default()}
			d.cfg.Sandbox.Auth, d.cfg.Sandbox.SharedClaudeConfig = tt.cfgAuth, tt.cfgShared
			pane := &Pane{ID: "p1"}
			if err := d.applySandboxSpecFor(pane, &ipc.SandboxSpec{Image: "img:1", Auth: tt.auth, ClaudeConfig: tt.choice}); err != nil {
				t.Fatalf("applySandboxSpecFor: %v", err)
			}
			if pane.SandboxClaudeConfig != tt.wantChoice {
				t.Errorf("SandboxClaudeConfig = %q, want %q", pane.SandboxClaudeConfig, tt.wantChoice)
			}
		})
	}
}

// And the create path uses it: a create that names neither field, on a
// daemon configured token + shared, records "own".
func TestHandleCreateTab_TokenSharedConfigRecordsOwn(t *testing.T) {
	cfg := config.Default()
	cfg.Sandbox.Auth, cfg.Sandbox.SharedClaudeConfig = "token", true
	d := overlayTestDaemon(t, cfg)
	stubSandboxMapping(t)

	d.handleCreateTab(nil, createTabMsg(t, &ipc.FirstPaneSpec{
		Type:    "terminal",
		Sandbox: &ipc.SandboxSpec{Image: "quil-sandbox:latest"},
	}))

	p := newTabPane(t, d)
	p.PluginMu.Lock()
	got := p.SandboxClaudeConfig
	p.PluginMu.Unlock()
	if got != "own" {
		t.Errorf("SandboxClaudeConfig = %q, want own — a token pane would mount the shared directory", got)
	}
}
