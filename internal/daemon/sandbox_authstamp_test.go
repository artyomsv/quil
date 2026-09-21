package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/sandbox"
)

// claudeConfigDir builds a Mapping whose HostClaudeConfig() is a temp dir.
//
// SharedClaudeRoot rather than HostPaneRoot because HostClaudeConfig returns it
// verbatim, so the test needs no knowledge of the per-pane layout — and the
// shared directory is the case that matters most here anyway: one directory
// serving every sandbox pane is where a stale onboarding answer reaches the
// most panes.
func claudeConfigDir(t *testing.T) (sandbox.Mapping, string) {
	t.Helper()
	dir := t.TempDir()
	return sandbox.Mapping{SharedClaudeRoot: dir}, dir
}

// writeClaudeJSON puts a config in place with onboarding already answered,
// plus state that must survive any repair.
func writeClaudeJSON(t *testing.T, dir string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"hasCompletedOnboarding": true,
		"theme":                  "dark",
		"projects":               map[string]any{"/repo": map[string]any{"hasTrustDialogAccepted": true}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), body, 0o600); err != nil {
		t.Fatalf("write .claude.json: %v", err)
	}
}

func readClaudeJSON(t *testing.T, dir string) map[string]any {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dir, ".claude.json"))
	if err != nil {
		t.Fatalf("read .claude.json: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

// The reported case. A pane prepared under the old token default carries a
// Quil-written config saying onboarding is done — and the sign-in screen is
// one of the screens that answer skips. Moving it to the browser flow without
// undoing that hands the user a prompt with no token and no way to log in.
func TestReconcileClaudeAuthMode_TokenToBrowserReopensOnboarding(t *testing.T) {
	m, dir := claudeConfigDir(t)
	writeClaudeJSON(t, dir)
	if err := writeClaudeAuthStamp(m, config.SandboxAuthToken); err != nil {
		t.Fatalf("stamp: %v", err)
	}

	if err := reconcileClaudeAuthMode(m, config.SandboxAuthBrowser); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := readClaudeJSON(t, dir)
	if _, ok := got["hasCompletedOnboarding"]; ok {
		t.Error("onboarding is still answered, so the pane opens on a prompt it " +
			"cannot authenticate and never reaches the sign-in inside onboarding")
	}
	// Everything else is the user's own state and must survive.
	if got["theme"] != "dark" {
		t.Errorf("theme = %v, want it preserved", got["theme"])
	}
	if _, ok := got["projects"]; !ok {
		t.Error("the projects entry was dropped; trust answers are the user's own state")
	}
}

// A pane prepared by a build older than the stamp has no record at all. Its
// config can only have been written while the token flow was the default, so
// it gets the same repair — the whole point of the legacy inference.
func TestReconcileClaudeAuthMode_NoStampWithAConfigIsTreatedAsTokenEra(t *testing.T) {
	m, dir := claudeConfigDir(t)
	writeClaudeJSON(t, dir)

	if err := reconcileClaudeAuthMode(m, config.SandboxAuthBrowser); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if _, ok := readClaudeJSON(t, dir)["hasCompletedOnboarding"]; ok {
		t.Error("a pre-stamp config was left answered; this is the upgrade case the " +
			"repair exists for")
	}
}

// A first run has nothing to repair and must not be given an empty config.
func TestReconcileClaudeAuthMode_FreshDirectoryIsLeftAlone(t *testing.T) {
	m, dir := claudeConfigDir(t)

	if err := reconcileClaudeAuthMode(m, config.SandboxAuthBrowser); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, ".claude.json")); !os.IsNotExist(err) {
		t.Errorf("a config appeared on a fresh directory (err = %v); the seed owns "+
			"that decision and it is gated on the pane having a credential", err)
	}
}

// Only the token → browser transition repairs. Every other pairing leaves the
// config alone, because the answer is either still correct or was never Quil's.
func TestReconcileClaudeAuthMode_OtherTransitionsLeaveOnboardingAnswered(t *testing.T) {
	for _, tt := range []struct {
		name string
		prev config.SandboxAuthMode
		now  config.SandboxAuthMode
	}{
		{"token stays token", config.SandboxAuthToken, config.SandboxAuthToken},
		{"browser stays browser", config.SandboxAuthBrowser, config.SandboxAuthBrowser},
		// The pane gains a credential; the answer it already has is fine.
		{"browser to token", config.SandboxAuthBrowser, config.SandboxAuthToken},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m, dir := claudeConfigDir(t)
			writeClaudeJSON(t, dir)
			if err := writeClaudeAuthStamp(m, tt.prev); err != nil {
				t.Fatalf("stamp: %v", err)
			}

			if err := reconcileClaudeAuthMode(m, tt.now); err != nil {
				t.Fatalf("reconcile: %v", err)
			}

			if _, ok := readClaudeJSON(t, dir)["hasCompletedOnboarding"]; !ok {
				t.Error("onboarding was reopened for a transition that did not lose a " +
					"credential; that is a sign-in the user did not need to repeat")
			}
		})
	}
}

// The stamp is what makes the NEXT prepare exact rather than inferred, so it
// must be written on every pass — including the ones that repair nothing.
func TestReconcileClaudeAuthMode_AlwaysRecordsTheModeItRanUnder(t *testing.T) {
	m, _ := claudeConfigDir(t)

	for _, want := range []config.SandboxAuthMode{
		config.SandboxAuthBrowser, config.SandboxAuthToken, config.SandboxAuthBrowser,
	} {
		if err := reconcileClaudeAuthMode(m, want); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		got, err := readClaudeAuthStamp(m)
		if err != nil {
			t.Fatalf("read stamp: %v", err)
		}
		if got != want {
			t.Fatalf("stamp = %q, want %q — without it the next prepare has to guess "+
				"again, and the guess is only right once", got, want)
		}
	}
}

// An unreadable or corrupt stamp must not fail the spawn. The repair is a
// convenience; refusing to open the pane over it would be worse than the
// onboarding screen it exists to restore.
func TestReconcileClaudeAuthMode_GarbageStampDoesNotFailTheSpawn(t *testing.T) {
	m, dir := claudeConfigDir(t)
	writeClaudeJSON(t, dir)
	if err := os.WriteFile(filepath.Join(dir, claudeAuthStampName), []byte("\x00not-a-mode"), 0o600); err != nil {
		t.Fatalf("write stamp: %v", err)
	}

	if err := reconcileClaudeAuthMode(m, config.SandboxAuthBrowser); err != nil {
		t.Errorf("reconcile = %v, want nil — a bad stamp is not a reason to refuse a pane", err)
	}
	got, err := readClaudeAuthStamp(m)
	if err != nil {
		t.Fatalf("read stamp: %v", err)
	}
	if got != config.SandboxAuthBrowser {
		t.Errorf("stamp = %q, want the garbage replaced by this run's mode", got)
	}
}

// A malformed .claude.json is the pane's own state and is never rewritten:
// re-encoding what could not be decoded would replace it with something
// smaller, and the file holds the user's trust answers and history.
func TestReconcileClaudeAuthMode_MalformedConfigIsNotRewritten(t *testing.T) {
	m, dir := claudeConfigDir(t)
	const broken = "{not json"
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(broken), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := reconcileClaudeAuthMode(m, config.SandboxAuthBrowser); err != nil {
		t.Errorf("reconcile = %v, want nil", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, ".claude.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(body) != broken {
		t.Errorf("config = %q, want it untouched", body)
	}
}
