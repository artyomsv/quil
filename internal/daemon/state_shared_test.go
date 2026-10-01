package daemon

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/persist"
)

// The frozen oracle (state_oracle_test.go) cannot know these keys; this test
// is their contract instead: wire, workspace.json and restore.
func TestSharedData_WireDiskAndRestoreRoundTrip(t *testing.T) {
	d := newTestDaemon(t) // lazy_restore_test.go:66 — sets QUIL_HOME
	p := d.session.CreateProject("api", t.TempDir())
	if err := d.session.GroupOp("create", "empty", ""); err != nil {
		t.Fatal(err)
	}
	if err := d.session.SetProjectGroup(p.ID, "infra"); err != nil {
		t.Fatal(err)
	}
	d.session.RecordRecentCWD("/one")
	d.session.RecordRecentCWD("/two")
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pane.noteMu.Lock()
	pane.NoteRev.Store(7)
	pane.noteMu.Unlock()

	wire := stateMap(t, d.buildWorkspaceState())
	if wire["shared_data"] != true {
		t.Error("broadcast lacks shared_data: true")
	}
	if g, _ := wire["groups"].([]any); len(g) != 2 {
		t.Errorf("groups on the wire = %v", wire["groups"])
	}
	if r, _ := wire["recent_cwds"].([]any); len(r) != 2 || r[0] != "/two" {
		t.Errorf("recent_cwds on the wire = %v", wire["recent_cwds"])
	}

	d.snapshot()
	raw, err := os.ReadFile(config.WorkspacePath())
	if err != nil {
		t.Fatal(err)
	}
	var disk map[string]any
	if err := json.Unmarshal(raw, &disk); err != nil {
		t.Fatal(err)
	}
	if _, ok := disk["shared_data"]; ok {
		t.Error("shared_data written to workspace.json; it is broadcast-only")
	}
	if g, _ := disk["groups"].([]any); len(g) != 2 {
		t.Errorf("groups on disk = %v", disk["groups"])
	}

	fresh := newTestDaemonInDir(t, os.Getenv("QUIL_HOME"))
	if err := fresh.restoreWorkspace(); err != nil {
		t.Fatal(err)
	}
	groups, recent := fresh.session.SharedSnapshot()
	if len(groups) != 2 || len(recent) != 2 || recent[0] != "/two" {
		t.Errorf("restored groups=%v recent=%v", groups, recent)
	}
	for _, rp := range fresh.session.Projects() {
		if rp.ID == p.ID && rp.Group != "infra" {
			t.Errorf("restored project group = %q", rp.Group)
		}
	}
	rp := fresh.session.Pane(pane.ID)
	if rp == nil {
		t.Fatal("pane not restored")
	}
	rp.noteMu.Lock()
	rev := rp.NoteRev.Load()
	rp.noteMu.Unlock()
	if rev != 7 {
		t.Errorf("restored note_rev = %d, want 7", rev)
	}
}

// A corrupt note_rev in workspace.json must not reach the uint64 conversion,
// which is undefined for a negative or huge float: negative reads as absent
// (so 0, or 1 when a note file exists), huge is capped.
func TestRestore_CorruptNoteRev_ClampedBeforeConversion(t *testing.T) {
	for _, tc := range []struct {
		name     string
		rev      float64
		noteFile bool
		want     uint64
	}{
		{"negative", -5, false, 0},
		{"negative with a note file", -5, true, 1},
		{"huge", 1e30, false, maxPersistedNoteRev},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newTestDaemon(t)
			tab := d.session.CreateTab("t")
			pane, err := d.session.CreatePane(tab.ID, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			d.snapshot()
			raw, err := os.ReadFile(config.WorkspacePath())
			if err != nil {
				t.Fatal(err)
			}
			var disk map[string]any
			if err := json.Unmarshal(raw, &disk); err != nil {
				t.Fatal(err)
			}
			panes, _ := disk["panes"].([]any)
			found := false
			for _, p := range panes {
				if pm, ok := p.(map[string]any); ok && pm["id"] == pane.ID {
					pm["note_rev"] = tc.rev
					found = true
				}
			}
			if !found {
				t.Fatalf("pane %s not in workspace.json", pane.ID)
			}
			out, err := json.Marshal(disk)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(config.WorkspacePath(), out, 0o600); err != nil {
				t.Fatal(err)
			}
			if tc.noteFile {
				if err := persist.SaveNotes(config.NotesDir(), pane.ID, "kept"); err != nil {
					t.Fatal(err)
				}
			}
			fresh := newTestDaemonInDir(t, os.Getenv("QUIL_HOME"))
			if err := fresh.restoreWorkspace(); err != nil {
				t.Fatal(err)
			}
			rp := fresh.session.Pane(pane.ID)
			if rp == nil {
				t.Fatal("pane not restored")
			}
			if got := rp.NoteRev.Load(); got != tc.want {
				t.Errorf("restored note_rev = %d, want %d", got, tc.want)
			}
		})
	}
}

// The frame builders must read the projects and the group list in ONE sm.mu
// hold: two holds let a rename land between them, and the frame then names a
// group its own list lacks. A race test cannot pin that reliably, so this
// pins the structure instead — the only session read the builders make is one
// SnapshotView each, and the shared helper makes none of its own.
func TestFrameBuilders_ReadSessionStateInOneHold(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "daemon.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	callsByFunc := map[string]map[string]int{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		calls := map[string]int{}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			// d.session.X(...) and d.X(...) are both recorded by method name.
			if inner, ok := sel.X.(*ast.SelectorExpr); ok && inner.Sel.Name == "session" {
				calls["session."+sel.Sel.Name]++
			} else {
				calls[sel.Sel.Name]++
			}
			return true
		})
		callsByFunc[fn.Name.Name] = calls
	}
	for _, name := range []string{"buildWorkspaceState", "snapshot"} {
		calls, ok := callsByFunc[name]
		if !ok {
			t.Fatalf("%s not found in daemon.go", name)
		}
		if calls["session.SnapshotView"] != 1 || calls["workspaceStateFromView"] != 1 {
			t.Errorf("%s: SnapshotView x%d, workspaceStateFromView x%d; want one each",
				name, calls["session.SnapshotView"], calls["workspaceStateFromView"])
		}
		for _, second := range []string{"session.SnapshotState", "session.SharedSnapshot"} {
			if calls[second] != 0 {
				t.Errorf("%s calls %s: a second hold beside SnapshotView", name, second)
			}
		}
	}
	for _, name := range []string{"workspaceStateFromView", "workspaceStateFromSnapshot"} {
		calls, ok := callsByFunc[name]
		if !ok {
			t.Fatalf("%s not found in daemon.go", name)
		}
		for call := range calls {
			if strings.HasPrefix(call, "session.") {
				t.Errorf("%s calls %s: the builder must take everything from its argument", name, call)
			}
		}
	}
}

// A project whose group is missing from the list gets the name appended.
func TestRestoreShared_RepairsAProjectGroupMissingFromTheList(t *testing.T) {
	sm := NewSessionManager(1024)
	p := sm.CreateProject("api", "/r")
	sm.mu.Lock()
	sm.projects[p.ID].Group = "orphan"
	sm.mu.Unlock()
	sm.RestoreShared([]string{"kept"}, nil)
	groups, _ := sm.SharedSnapshot()
	if len(groups) != 2 || groups[1] != "orphan" {
		t.Errorf("groups = %v, want [kept orphan]", groups)
	}
}
