//go:build integration

package daemon

import (
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/persist"
)

// The Bootstrap flag has to survive the REAL write path, not just the parser.
//
// TestParseRestoredProjects_KeepsBootstrap hand-builds the map it parses, so it
// says nothing about whether `workspaceStateFromSnapshot` ever writes the key.
// If that line were dropped, every test in the suite would still pass: the
// parser would read absence, absence means "the user named it", and the flag
// would silently degrade to false for every project on the next restart —
// turning every un-adopted Default into a permanent one and occupying the host
// against the rule this feature exists to enforce. Absence being the safe
// direction for a HOSTILE value is exactly what makes it a silent failure here.
//
// Mirrors TestSnapshot_PaneSetConsistentAcrossWorkspaceAndBuffers: drive the
// daemon's own snapshot, then read what actually landed on disk.
func TestSnapshot_BootstrapFlagSurvivesTheRealWritePath(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())

	d := New(config.Default())
	// CreateTab with no project bootstraps one — the case the flag exists for.
	d.session.CreateTab("Shell")
	named := d.session.CreateProject("cluster-management", "/srv/cluster")

	d.snapshot()

	state, err := persist.Load(config.WorkspacePath())
	if err != nil {
		t.Fatalf("Load workspace: %v", err)
	}
	raw, _ := state["projects"].([]any)
	if len(raw) != 2 {
		t.Fatalf("snapshot holds %d projects, want 2", len(raw))
	}

	got := map[string]bool{}
	for _, p := range raw {
		pm, _ := p.(map[string]any)
		id, _ := pm["id"].(string)
		b, ok := pm["bootstrap"].(bool)
		if !ok {
			t.Errorf("project %s has no bootstrap key on disk — the parser reads "+
				"absence as \"the user named it\", so every bootstrapped project "+
				"becomes permanent on the next restart and nothing fails loudly", id)
		}
		got[id] = b
	}
	if got[named.ID] {
		t.Errorf("the named project persisted as Bootstrap; the next create on "+
			"its host would adopt and rename it")
	}
	for id, b := range got {
		if id != named.ID && !b {
			t.Errorf("the bootstrapped project %s persisted as not-Bootstrap, so "+
				"the host is occupied by a Default nobody named", id)
		}
	}

	// And back again: a fresh daemon restoring this snapshot must see the same
	// flags, which is the half a parser-only test cannot reach.
	fresh := New(config.Default())
	fresh.restoreWorkspace()
	for _, p := range fresh.session.Projects() {
		want := p.ID != named.ID
		if p.Bootstrap != want {
			t.Errorf("after restore, project %q Bootstrap = %v, want %v",
				p.Name, p.Bootstrap, want)
		}
	}
}
