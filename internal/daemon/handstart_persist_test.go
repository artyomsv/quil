package daemon

import "testing"

// The loop these marks serve spans restarts. Without persistence a user whose
// habit is shell -> agent -> /exit -> shell finds every cycle after a daemon
// restart ending on a dead agent pane, and an adopted session becomes
// indistinguishable from one Quil preassigned — so the UI would claim tracking
// that the restart had silently dropped.
func TestSnapshot_HandStartMarksSurviveTheRoundTrip(t *testing.T) {
	dir := t.TempDir()

	d := newTestDaemonInDir(t, dir)
	tab := d.session.CreateTab("t")
	pane := mustPane(t, d, tab.ID)
	pane.Type = "claude-code"
	pane.ConvertedFromTerminal = "terminal"
	pane.Adopted = true
	pane.PluginState = map[string]string{"session_id": "ADOPTED"}
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
	if got.ConvertedFromTerminal != "terminal" {
		t.Errorf("ConvertedFromTerminal = %q, want terminal", got.ConvertedFromTerminal)
	}
	if !got.Adopted {
		t.Error("the adoption mark did not survive")
	}
	if got.PluginState["session_id"] != "ADOPTED" {
		t.Errorf("session_id = %q, want ADOPTED", got.PluginState["session_id"])
	}
}

// A pane that was never converted or adopted must not gain either mark from a
// round trip — the absence is as meaningful as the presence.
func TestSnapshot_OrdinaryPaneGainsNoHandStartMarks(t *testing.T) {
	dir := t.TempDir()
	d := newTestDaemonInDir(t, dir)
	tab := d.session.CreateTab("t")
	mustPane(t, d, tab.ID)
	d.snapshot()

	restored := newTestDaemonInDir(t, dir)
	if err := restored.restoreWorkspace(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	for _, tb := range restored.session.Tabs() {
		for _, p := range restored.session.Panes(tb.ID) {
			if p.ConvertedFromTerminal != "" || p.Adopted {
				t.Fatalf("plain pane came back converted=%q adopted=%v", p.ConvertedFromTerminal, p.Adopted)
			}
		}
	}
}
