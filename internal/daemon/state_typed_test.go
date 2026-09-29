package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/gitinfo"
	"github.com/artyomsv/quil/internal/ipc"
)

// stateMap round-trips v through JSON into the shape a client decodes, so
// two states can be compared structurally. Byte comparison is wrong: struct
// keys marshal in declaration order, map keys alphabetically.
func stateMap(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}
	return m
}

// richStateDaemon builds a daemon whose workspace exercises every conditional
// key workspaceStateFromSnapshot/buildWorkspaceState can write, on both the
// broadcast (includeOverlays=true) and disk (includeOverlays=false) shapes:
// a pane with a name, a plugin_state map, muted, eager, converted_from,
// adopted, pinned_attention, marked_for_deletion, unseen, worktree_owned +
// worktree_path, quil_mcp, worktree_interrupted, instance_name +
// instance_args, cols/rows, session_id, history_lines, the three mouse
// flags, spawn_error, a model with context_tokens 0, and a git cache entry
// carrying branch, detached, worktree name, upstream 0/0 and stale; a
// SANDBOX pane (sandbox_image, sandbox_auth "", container_cwd, a
// non-terminal type); a pending pane preparing a worktree; an overlay pane;
// a tab with a layout and template_layout/template_main; a bootstrap
// project and a project with nil TabIDs; an update info; a size master; and
// daemon_limited.
func richStateDaemon(t *testing.T) *Daemon {
	t.Helper()
	d := newTestDaemonInDir(t, t.TempDir())

	// A git cache entry for the "full" pane's CWD, covering branch, detached,
	// linked worktree + name, an in-sync upstream (0 ahead, 0 behind — a
	// different statement from "no upstream"), and stale.
	cwd := t.TempDir()
	gitDir := filepath.Join(cwd, ".git", "worktrees", "feat-a")
	d.gitCache.byDir[gitDir] = &gitEntry{
		info: gitinfo.Info{
			Branch:         "feat/detached-test",
			Detached:       true,
			LinkedWorktree: true,
			WorktreeName:   "feat-a",
			HasUpstream:    true,
			Ahead:          0,
			Behind:         0,
		},
		repo:  true,
		stale: true,
	}
	d.gitCache.cwdToDir[cwd] = gitDir

	full := &Pane{
		ID:                    "pane-00000001",
		TabID:                 "tab-00000001",
		CWD:                   cwd,
		Name:                  "build",
		Type:                  "claude-code",
		PluginState:           map[string]string{"session_id": "sess-abc"},
		Muted:                 true,
		Eager:                 true,
		ConvertedFromTerminal: "terminal",
		Adopted:               true,
		PinnedAttention:       true,
		MarkedForDeletion:     true,
		Unseen:                true,
		WorktreeOwned:         true,
		WorktreePath:          "/repo/worktrees/feat-a",
		QuilMCP:               true,
		WorktreeInterrupted:   true,
		InstanceName:          "default",
		InstanceArgs:          []string{"--resume", "abc"},
		Cols:                  120,
		Rows:                  40,
		HistoryLines:          12,
		SpawnError:            "boom",
		LastModel:             "claude-opus",
		LastContextTokens:     0,
		MouseModes:            mouseModeState{normal: true, sgr: true, bracketedPaste: true},
	}
	full.colsSeq = 5

	sandbox := &Pane{
		ID:           "pane-00000002",
		TabID:        "tab-00000001",
		CWD:          t.TempDir(),
		Type:         "claude-code",
		SandboxImage: "img:sandbox",
		SandboxAuth:  "",
		ContainerCWD: "/container/work",
	}

	preparing := &Pane{
		ID:                "pane-00000003",
		TabID:             "tab-00000001",
		CWD:               t.TempDir(),
		PreparingWorktree: "feat/prep-branch",
		Pending:           true,
	}

	overlay := &Pane{
		ID:      "pane-00000004",
		TabID:   "tab-00000001",
		CWD:     t.TempDir(),
		Overlay: true,
	}

	tab := &Tab{
		ID:             "tab-00000001",
		Name:           "Build",
		Color:          "blue",
		Panes:          []string{full.ID, sandbox.ID, preparing.ID, overlay.ID},
		Layout:         json.RawMessage(`{"split":"H"}`),
		LayoutRev:      3,
		TemplateLayout: "columns",
		TemplateMain:   full.ID,
		ProjectID:      "proj-boot",
	}
	d.session.RestoreTab(tab, []*Pane{full, sandbox, preparing, overlay})

	boot := &Project{ID: "proj-boot", Name: "Default", RootDir: cwd, TabIDs: []string{tab.ID}, ActiveTab: tab.ID, Bootstrap: true}
	empty := &Project{ID: "proj-empty", Name: "Empty", RootDir: t.TempDir()}
	d.session.RestoreProjects([]*Project{boot, empty}, boot.ID)

	if changed := d.registerClient(new(ipc.Conn), ipc.AttachPayload{ClientID: "A", Cols: 200, Rows: 50}); !changed {
		t.Fatal("setup: registering the size master did not report a change")
	}

	d.limited = true

	if changed := d.setUpdateInfo(&ipc.UpdateInfo{LatestVersion: "9.9.9", StagedVersion: "9.9.9", InstallWritable: true}); !changed {
		t.Fatal("setup: setUpdateInfo did not report a change")
	}

	return d
}

// TestWorkspaceStateFromSnapshot_Typed_MatchesOldMap is the equality oracle:
// the new typed builder must produce the exact same wire shape the frozen
// pre-3a map builder does, on the rich fixture, on both the broadcast and
// disk shapes.
func TestWorkspaceStateFromSnapshot_Typed_MatchesOldMap(t *testing.T) {
	for _, includeOverlays := range []bool{true, false} {
		t.Run(fmt.Sprintf("includeOverlays=%v", includeOverlays), func(t *testing.T) {
			d := richStateDaemon(t)
			at, tabs, panes, projects, ap := d.session.SnapshotState()
			got := stateMap(t, d.workspaceStateFromSnapshot(at, tabs, panes, projects, ap, includeOverlays))
			want := stateMap(t, d.oldWorkspaceStateMap(at, tabs, panes, projects, ap, includeOverlays))
			if !reflect.DeepEqual(got, want) {
				t.Errorf("typed state differs from the pre-3a map\n got: %v\nwant: %v", got, want)
			}
		})
	}
}

// TestBuildWorkspaceState_Typed_MatchesOldMapExceptRev is the same oracle for
// the broadcast entry point, which also carries update/size_master/clients/
// daemon_limited. rev and run_id are new in 3a (Task 4) and have no map-side
// counterpart.
func TestBuildWorkspaceState_Typed_MatchesOldMapExceptRev(t *testing.T) {
	d := richStateDaemon(t)
	got := stateMap(t, d.buildWorkspaceState())
	delete(got, "rev")         // new in 3a, Task 4
	delete(got, "run_id")      // new in 3a, Task 4
	delete(got, "shared_data") // new in 3b, broadcast-only
	want := stateMap(t, d.oldBuildWorkspaceStateMap())
	if !reflect.DeepEqual(got, want) {
		t.Errorf("typed broadcast differs from the pre-3a map\n got: %v\nwant: %v", got, want)
	}
}

// TestSnapshot_WorkspaceJSON_SameShapeAsBefore proves workspace.json itself —
// what actually reaches disk through persist.Save — is unchanged in shape,
// and that a fresh daemon can restore the tabs/panes it wrote back out of it.
func TestSnapshot_WorkspaceJSON_SameShapeAsBefore(t *testing.T) {
	d := richStateDaemon(t)
	quilHome := os.Getenv("QUIL_HOME")
	d.snapshot()

	raw, err := os.ReadFile(config.WorkspacePath())
	if err != nil {
		t.Fatalf("read workspace.json: %v", err)
	}
	var onDisk map[string]any
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("unmarshal workspace.json: %v", err)
	}

	activeTab, tabs, panesByTab, projects, activeProject := d.session.SnapshotState()
	want := d.oldWorkspaceStateMap(activeTab, tabs, panesByTab, projects, activeProject, false)
	// snapshot() writes size_master itself, for the restart reserve —
	// workspaceStateFromSnapshot/oldWorkspaceStateMap never do.
	if id := d.masterID(); id != "" {
		want["size_master"] = id
	}
	if !reflect.DeepEqual(onDisk, stateMap(t, want)) {
		t.Errorf("workspace.json differs from the pre-3a disk shape\n  got: %v\n want: %v", onDisk, stateMap(t, want))
	}

	d2 := newTestDaemonInDir(t, quilHome)
	if err := d2.restoreWorkspace(); err != nil {
		t.Fatalf("restoreWorkspace: %v", err)
	}
	if got := d2.session.Tab("tab-00000001"); got == nil {
		t.Error("tab-00000001 did not come back from a restore over the snapshot this test just wrote")
	}
	for _, id := range []string{"pane-00000001", "pane-00000002", "pane-00000003"} {
		if got := d2.session.Pane(id); got == nil {
			t.Errorf("pane %s did not come back from restore", id)
		}
	}
	if got := d2.session.Pane("pane-00000004"); got != nil {
		t.Error("the overlay pane was persisted and came back — overlays must not survive a disk snapshot")
	}
}
