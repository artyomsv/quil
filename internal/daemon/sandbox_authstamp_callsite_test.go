package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/artyomsv/quil/internal/config"
)

// --- the on switch ---
//
// reconcileClaudeAuthMode has a green unit test whether or not prepareSandbox
// ever calls it, and "nobody calls it" IS the bug: a pane that was prepared for
// a forwarded token, restored under the browser flow, opens on a prompt with no
// credential and no sign-in screen. These drive the real prepare.

// A pane whose directory was prepared under the token flow, now resolving to
// the browser flow, must come back with its sign-in reachable.
func TestPrepareSandbox_ReopensOnboardingWhenTheModeDropsTheToken(t *testing.T) {
	d, pane, _ := sandboxCallsiteFixture(t)
	d.cfg = tokenFlowConfig()
	t.Setenv(oauthTokenEnv, "sk-ant-test-token")

	// First prepare: token flow, so the seed lands and the stamp records it.
	m, err := d.prepareSandbox(context.Background(), pane, "claude-code", "img:1")
	if err != nil {
		t.Fatalf("prepareSandbox (token): %v", err)
	}
	cfgPath := filepath.Join(m.HostClaudeConfig(), claudeConfigName)
	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("the token pass wrote no config, so this test proves nothing: %v", err)
	}

	// The user moves to the browser flow — or, as shipped, the default does.
	d.cfg.Sandbox.Auth = string(config.SandboxAuthBrowser)
	if _, err := d.prepareSandbox(context.Background(), pane, "claude-code", "img:1"); err != nil {
		t.Fatalf("prepareSandbox (browser): %v", err)
	}

	body, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := got[claudeOnboardingKey]; ok {
		t.Error("prepareSandbox left onboarding answered after the pane stopped " +
			"receiving a token — the pane opens on a prompt it cannot authenticate")
	}
}

// The mirror: a pane that keeps its token must not be asked to sign in again.
func TestPrepareSandbox_KeepsOnboardingWhileTheTokenStays(t *testing.T) {
	d, pane, _ := sandboxCallsiteFixture(t)
	d.cfg = tokenFlowConfig()
	t.Setenv(oauthTokenEnv, "sk-ant-test-token")

	m, err := d.prepareSandbox(context.Background(), pane, "claude-code", "img:1")
	if err != nil {
		t.Fatalf("prepareSandbox: %v", err)
	}
	if _, err := d.prepareSandbox(context.Background(), pane, "claude-code", "img:1"); err != nil {
		t.Fatalf("prepareSandbox (second): %v", err)
	}

	body, err := os.ReadFile(filepath.Join(m.HostClaudeConfig(), claudeConfigName))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := got[claudeOnboardingKey]; !ok {
		t.Error("onboarding was reopened for a pane that still has its token; that is " +
			"four first-run screens the seed exists to remove")
	}
}

// A codex pane has no Claude onboarding answer and no Claude sign-in mode, so
// nothing here may touch its directory. Pinned in both directions for the
// reason UsesClaudeAuthName exists: gating fewer than all the auth decisions on
// it is what once ran `claude setup-token` for a codex pane.
func TestPrepareSandbox_LeavesACodexPaneUnstamped(t *testing.T) {
	d, pane, _ := sandboxCallsiteFixture(t)
	d.cfg = tokenFlowConfig()

	m, err := d.prepareSandbox(context.Background(), pane, "codex", "img:1")
	if err != nil {
		t.Fatalf("prepareSandbox: %v", err)
	}
	if _, err := os.Stat(filepath.Join(m.HostClaudeConfig(), claudeAuthStampName)); !os.IsNotExist(err) {
		t.Errorf("a codex pane was stamped with a Claude sign-in mode (err = %v)", err)
	}
}
