package daemon

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/artyomsv/quil/internal/config"
)

func TestIsValidHexID(t *testing.T) {
	cases := []struct {
		name   string
		id     string
		prefix string
		want   bool
	}{
		{"valid pane id", "pane-a1b2c3d4", "pane-", true},
		{"valid tab id", "tab-deadbeef", "tab-", true},
		{"all digits", "pane-12345678", "pane-", true},
		{"all hex letters", "pane-abcdefab", "pane-", true},
		{"uppercase hex rejected", "pane-A1B2C3D4", "pane-", false},
		{"prefix mismatch", "tab-a1b2c3d4", "pane-", false},
		{"too short", "pane-abc", "pane-", false},
		{"too long", "pane-a1b2c3d4e", "pane-", false},
		{"non-hex char", "pane-a1b2c3dz", "pane-", false},
		{"empty string", "", "pane-", false},
		{"prefix only", "pane-", "pane-", false},
		{"missing dash", "panea1b2c3d4", "pane-", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isValidHexID(tc.id, tc.prefix); got != tc.want {
				t.Errorf("isValidHexID(%q, %q) = %v, want %v", tc.id, tc.prefix, got, tc.want)
			}
		})
	}
}

// TestWorkspaceStateFromSnapshot drives the pure half of buildWorkspaceState
// directly, asserting key shape and value handling. It also exercises the
// optional-field elision for `name`, `type`, `instance_*`, and `layout`.
func TestWorkspaceStateFromSnapshot(t *testing.T) {
	d := New(config.Default())

	tabs := []*Tab{
		{
			ID:     "tab-aaaaaaaa",
			Name:   "Build",
			Color:  "blue",
			Panes:  []string{"pane-11111111"},
			Layout: json.RawMessage(`{"split":"H"}`),
		},
		{
			ID:    "tab-bbbbbbbb",
			Name:  "Notes",
			Panes: []string{"pane-22222222"},
		},
	}
	panesByTab := map[string][]*Pane{
		"tab-aaaaaaaa": {
			{
				ID:           "pane-11111111",
				TabID:        "tab-aaaaaaaa",
				CWD:          "/home/user",
				Name:         "make",
				Type:         "claude-code",
				InstanceName: "default",
				InstanceArgs: []string{"--resume", "abc"},
				PluginState:  map[string]string{"session_id": "abc"},
			},
		},
		"tab-bbbbbbbb": {
			{
				ID:    "pane-22222222",
				TabID: "tab-bbbbbbbb",
				CWD:   "/tmp",
				Type:  "terminal", // omitted from output by design
			},
		},
	}

	state := d.workspaceStateFromSnapshot("tab-aaaaaaaa", tabs, panesByTab, false)

	if got := state["active_tab"]; got != "tab-aaaaaaaa" {
		t.Errorf("active_tab = %v, want tab-aaaaaaaa", got)
	}

	tabsOut, _ := state["tabs"].([]map[string]any)
	if len(tabsOut) != 2 {
		t.Fatalf("tabs len = %d, want 2", len(tabsOut))
	}
	if tabsOut[0]["id"] != "tab-aaaaaaaa" || tabsOut[0]["color"] != "blue" {
		t.Errorf("tab[0] = %+v", tabsOut[0])
	}
	if _, ok := tabsOut[0]["layout"]; !ok {
		t.Error("tab[0] missing layout key")
	}
	if _, ok := tabsOut[1]["layout"]; ok {
		t.Error("tab[1] has layout — should be elided when zero-length")
	}

	panesOut, _ := state["panes"].([]map[string]any)
	if len(panesOut) != 2 {
		t.Fatalf("panes len = %d, want 2", len(panesOut))
	}

	pane0 := panesOut[0]
	if pane0["id"] != "pane-11111111" || pane0["cwd"] != "/home/user" {
		t.Errorf("pane[0] basic fields = %+v", pane0)
	}
	if pane0["name"] != "make" {
		t.Errorf("pane[0] name = %v, want 'make'", pane0["name"])
	}
	if pane0["type"] != "claude-code" {
		t.Errorf("pane[0] type = %v, want 'claude-code'", pane0["type"])
	}
	if pane0["instance_name"] != "default" {
		t.Errorf("pane[0] instance_name = %v", pane0["instance_name"])
	}
	if args, ok := pane0["instance_args"].([]string); !ok || !reflect.DeepEqual(args, []string{"--resume", "abc"}) {
		t.Errorf("pane[0] instance_args = %v, want [--resume abc]", pane0["instance_args"])
	}
	if ps, ok := pane0["plugin_state"].(map[string]string); !ok || ps["session_id"] != "abc" {
		t.Errorf("pane[0] plugin_state = %v", pane0["plugin_state"])
	}

	// Pane 2 is a default terminal with no extras → optional fields must
	// all be elided to keep workspace.json compact.
	pane1 := panesOut[1]
	for _, k := range []string{"name", "type", "instance_name", "instance_args", "plugin_state"} {
		if _, ok := pane1[k]; ok {
			t.Errorf("pane[1] has unexpected %q key: %v", k, pane1[k])
		}
	}
}

// TestDaemon_DefaultCWD covers the three branches of (*Daemon).defaultCWD:
// (1) clientCWD set and valid → returns the resolved client path,
// (2) clientCWD set but stale → falls back to os.Getwd(),
// (3) clientCWD unset → falls back to os.Getwd().
//
// We bypass New() and build a minimal Daemon literal because defaultCWD only
// depends on the atomic.Pointer field, not on session/registry/etc.
func TestDaemon_DefaultCWD(t *testing.T) {
	hostCWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}

	t.Run("client CWD set and valid", func(t *testing.T) {
		dir := t.TempDir()
		d := &Daemon{}
		d.clientCWD.Store(&dir)
		got := d.defaultCWD()
		// EvalSymlinks is applied; on macOS t.TempDir() lives under
		// /var/folders/... which symlinks to /private/var/folders/...,
		// so we compare the resolved form.
		want, err := filepath.EvalSymlinks(dir)
		if err != nil {
			t.Fatalf("EvalSymlinks: %v", err)
		}
		if got != want {
			t.Errorf("defaultCWD = %q, want %q", got, want)
		}
	})

	t.Run("client CWD set but stale", func(t *testing.T) {
		dir := t.TempDir()
		stale := dir + "/does-not-exist"
		d := &Daemon{}
		d.clientCWD.Store(&stale)
		if got := d.defaultCWD(); got != hostCWD {
			t.Errorf("stale path should fall back to os.Getwd(); got %q, want %q", got, hostCWD)
		}
	})

	t.Run("client CWD unset", func(t *testing.T) {
		d := &Daemon{}
		if got := d.defaultCWD(); got != hostCWD {
			t.Errorf("unset should fall back to os.Getwd(); got %q, want %q", got, hostCWD)
		}
	})

	t.Run("client CWD empty string", func(t *testing.T) {
		empty := ""
		d := &Daemon{}
		d.clientCWD.Store(&empty)
		if got := d.defaultCWD(); got != hostCWD {
			t.Errorf("empty string should fall back to os.Getwd(); got %q, want %q", got, hostCWD)
		}
	})
}

// saveHookStubs captures and restores the package-level hook reader vars
// so the per-test stub installs cannot leak into other tests in the
// package. Returns no value; the cleanup runs at test end.
//
// NOTE: tests that use this helper MUST NOT call t.Parallel() — they
// mutate package-level state shared with every other daemon test, and a
// parallel scheduler would let two stubs collide.
func saveHookStubs(t *testing.T) {
	t.Helper()
	origClaudeHook := readHookSessionIDFn
	origOpencodeHook := readOpencodeSessionIDFn
	t.Cleanup(func() {
		readHookSessionIDFn = origClaudeHook
		readOpencodeSessionIDFn = origOpencodeHook
	})
}

// TestDaemon_RefreshPluginStateFromHooks verifies the shutdown helper
// that pulls live hook-recorded session ids into PluginState before the
// final snapshot. Without this, workspace.json ships with the original
// preassigned id and is stale after any /clear / /resume / compaction
// rotation; if the hook file later disappears the restore falls back to
// --continue.
func TestDaemon_RefreshPluginStateFromHooks(t *testing.T) {
	// NOTE: stubs mutate package-level vars — must not be marked t.Parallel().
	saveHookStubs(t)

	d := New(config.Default())
	tab := &Tab{ID: "tab-1", Name: "test", Panes: []string{"pane-claude", "pane-opencode", "pane-term", "pane-nilstate"}}
	panes := []*Pane{
		// Direct writes to PluginState without PluginMu are safe here:
		// the panes have not been published to any goroutine until
		// RestoreTab returns; no concurrent reader exists.
		{ID: "pane-claude", TabID: "tab-1", Type: "claude-code", PluginState: map[string]string{"session_id": "stale-preassigned"}},
		{ID: "pane-opencode", TabID: "tab-1", Type: "opencode", PluginState: map[string]string{"session_id": "stale-preassigned"}},
		{ID: "pane-term", TabID: "tab-1", Type: "terminal", PluginState: map[string]string{"session_id": "ignore-me"}},
		// nil PluginState exercises the lazy allocation branch in
		// refreshPluginStateFromHooks.
		{ID: "pane-nilstate", TabID: "tab-1", Type: "claude-code", PluginState: nil},
	}
	d.session.RestoreTab(tab, panes)

	readHookSessionIDFn = func(paneID string) (string, error) {
		switch paneID {
		case "pane-claude":
			return "live-claude-id", nil
		case "pane-nilstate":
			return "live-nilstate-id", nil
		}
		return "", nil
	}
	readOpencodeSessionIDFn = func(paneID string) (string, error) {
		if paneID == "pane-opencode" {
			return "live-opencode-id", nil
		}
		return "", nil
	}

	d.refreshPluginStateFromHooks()

	if got := panes[0].PluginState["session_id"]; got != "live-claude-id" {
		t.Errorf("claude pane session_id = %q, want %q (hook id should have overwritten the stale preassigned id)", got, "live-claude-id")
	}
	if got := panes[1].PluginState["session_id"]; got != "live-opencode-id" {
		t.Errorf("opencode pane session_id = %q, want %q", got, "live-opencode-id")
	}
	if got := panes[2].PluginState["session_id"]; got != "ignore-me" {
		t.Errorf("terminal pane session_id = %q, want %q (non-AI panes must not be touched)", got, "ignore-me")
	}
	if panes[3].PluginState == nil {
		t.Fatal("nil PluginState pane: map should have been lazily allocated")
	}
	if got := panes[3].PluginState["session_id"]; got != "live-nilstate-id" {
		t.Errorf("nil PluginState pane session_id = %q, want %q", got, "live-nilstate-id")
	}
}

// TestDaemon_RefreshPluginStateFromHooks_EmptyHookIDPreservesExisting
// guards the "hook file exists but is empty / unreadable" path: we must
// NOT clear PluginState["session_id"] in that case — the preassigned id
// is still better than nothing for the restore probe. Covers both an
// empty-string-no-error read and an error read; the production code
// path swallows both into the same fallthrough.
func TestDaemon_RefreshPluginStateFromHooks_EmptyHookIDPreservesExisting(t *testing.T) {
	// NOTE: stubs mutate package-level vars — must not be marked t.Parallel().
	saveHookStubs(t)

	cases := []struct {
		name string
		stub func(string) (string, error)
	}{
		{"empty string no error", func(string) (string, error) { return "", nil }},
		{"read error", func(string) (string, error) { return "", errors.New("simulated disk error") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := New(config.Default())
			tab := &Tab{ID: "tab-1", Name: "test", Panes: []string{"pane-claude"}}
			panes := []*Pane{
				{ID: "pane-claude", TabID: "tab-1", Type: "claude-code", PluginState: map[string]string{"session_id": "preassigned-fallback"}},
			}
			d.session.RestoreTab(tab, panes)
			readHookSessionIDFn = tc.stub

			d.refreshPluginStateFromHooks()

			if got := panes[0].PluginState["session_id"]; got != "preassigned-fallback" {
				t.Errorf("empty/error hook read should leave existing session_id intact; got %q", got)
			}
		})
	}
}

// TestWorkspaceState_OverlayPane_BroadcastVsDisk verifies that overlay panes
// are present (and flagged overlay=true) in live broadcasts but absent from
// disk snapshots — both in the pane list and in the tab's pane-ID list.
func TestWorkspaceState_OverlayPane_BroadcastVsDisk(t *testing.T) {
	d := New(config.Default())

	tab := &Tab{
		ID:    "tab-aabbccdd",
		Name:  "Work",
		Panes: []string{"pane-11111111", "pane-22222222"},
	}
	normal := &Pane{
		ID:    "pane-11111111",
		TabID: "tab-aabbccdd",
		CWD:   "/tmp",
	}
	overlay := &Pane{
		ID:      "pane-22222222",
		TabID:   "tab-aabbccdd",
		CWD:     "/tmp/repo",
		Overlay: true,
	}

	tabs := []*Tab{tab}
	panesByTab := map[string][]*Pane{tab.ID: {normal, overlay}}

	// Broadcast: overlay pane must be included and carry overlay=true.
	live := d.workspaceStateFromSnapshot(tab.ID, tabs, panesByTab, true)
	livePanes := live["panes"].([]map[string]any)
	if len(livePanes) != 2 {
		t.Fatalf("broadcast panes = %d, want 2", len(livePanes))
	}
	var flagged bool
	for _, p := range livePanes {
		if p["id"] == overlay.ID {
			if p["overlay"] == true {
				flagged = true
			} else {
				t.Errorf("broadcast overlay pane missing overlay=true; got %v", p["overlay"])
			}
		}
	}
	if !flagged {
		t.Error("broadcast pane list did not contain the overlay pane")
	}

	// Disk: overlay pane must be absent from both the pane list and the
	// tab's pane-ID list.
	disk := d.workspaceStateFromSnapshot(tab.ID, tabs, panesByTab, false)
	diskPanes := disk["panes"].([]map[string]any)
	if len(diskPanes) != 1 {
		t.Fatalf("disk panes = %d, want 1", len(diskPanes))
	}
	if diskPanes[0]["id"] != normal.ID {
		t.Fatalf("disk panes[0].id = %v, want %s", diskPanes[0]["id"], normal.ID)
	}
	diskTabs := disk["tabs"].([]map[string]any)
	if len(diskTabs) != 1 {
		t.Fatalf("disk tabs = %d, want 1", len(diskTabs))
	}
	ids := diskTabs[0]["panes"].([]string)
	for _, id := range ids {
		if id == overlay.ID {
			t.Error("disk tab pane-ID list must not reference the overlay pane")
		}
	}
}

// TestEnsureTabNotEmpty_LastNormalPaneGone_DestroysOverlayAndRecovers verifies
// that ensureTabNotEmpty destroys orphaned overlay panes and creates a
// replacement terminal pane when the last normal pane in a tab is gone.
func TestEnsureTabNotEmpty_LastNormalPaneGone_DestroysOverlayAndRecovers(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	normal, err := d.session.CreatePane(tab.ID, "/tmp")
	if err != nil {
		t.Fatalf("create normal pane: %v", err)
	}
	overlay, err := d.session.CreatePane(tab.ID, "/tmp/repo")
	if err != nil {
		t.Fatalf("create overlay pane: %v", err)
	}
	overlay.PluginMu.Lock()
	overlay.Overlay = true
	overlay.PluginMu.Unlock()

	// Destroy the last normal pane, then drive ensureTabNotEmpty.
	if err := d.session.DestroyPane(normal.ID); err != nil {
		t.Fatalf("destroy normal pane: %v", err)
	}
	d.ensureTabNotEmpty(tab.ID)

	panes := d.session.Panes(tab.ID)
	for _, p := range panes {
		if p.ID == overlay.ID {
			t.Error("overlay pane must be destroyed when the last normal pane goes")
		}
		p.PluginMu.Lock()
		ov := p.Overlay
		p.PluginMu.Unlock()
		if ov {
			t.Error("no overlay panes may remain")
		}
	}
	if len(panes) == 0 {
		t.Error("auto-recovery must have created a replacement pane")
	}
}

// TestEnsureTabNotEmpty_NormalPanesRemain_NoOp verifies that ensureTabNotEmpty
// is a no-op when at least one normal pane still exists in the tab.
func TestEnsureTabNotEmpty_NormalPanesRemain_NoOp(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	_, err := d.session.CreatePane(tab.ID, "/tmp")
	if err != nil {
		t.Fatalf("create normal pane: %v", err)
	}
	overlay, err := d.session.CreatePane(tab.ID, "/tmp/repo")
	if err != nil {
		t.Fatalf("create overlay pane: %v", err)
	}
	overlay.PluginMu.Lock()
	overlay.Overlay = true
	overlay.PluginMu.Unlock()

	d.ensureTabNotEmpty(tab.ID)

	if got := len(d.session.Panes(tab.ID)); got != 2 {
		t.Errorf("panes = %d, want 2 (no-op when a normal pane remains)", got)
	}
}

// TestWorkspaceState_OverlayFlip_NoRace is a -race regression guard for the
// Pane.Overlay locking discipline: handleCreatePane sets Overlay AFTER the
// pane is published to the session maps, so a concurrent snapshot/broadcast
// goroutine reading the pane must observe the field only under PluginMu.
// One goroutine flips Overlay under PluginMu while another builds the
// workspace state in a loop; the race detector flags any unlocked access.
func TestWorkspaceState_OverlayFlip_NoRace(t *testing.T) {
	d := New(config.Default())

	tab := &Tab{ID: "tab-racerace", Name: "race", Panes: []string{"pane-cafecafe"}}
	pane := &Pane{ID: "pane-cafecafe", TabID: "tab-racerace", CWD: "/tmp"}
	tabs := []*Tab{tab}
	panesByTab := map[string][]*Pane{tab.ID: {pane}}

	const iters = 100
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < iters; i++ {
			pane.PluginMu.Lock()
			pane.Overlay = i%2 == 0
			pane.PluginMu.Unlock()
		}
	}()

	for i := 0; i < iters; i++ {
		_ = d.workspaceStateFromSnapshot(tab.ID, tabs, panesByTab, true)
	}
	<-done
}

// TestOnPaneExit_OverlayAutoDestroyed verifies that onPaneExit destroys an
// overlay pane on exit while leaving normal panes intact. This is the fix for
// the Alt+G dead-overlay issue: without auto-destroy, the TUI state machine
// would re-show a dead overlay forever.
func TestOnPaneExit_OverlayAutoDestroyed(t *testing.T) {
	// newTestDaemon mutates package-level vars — must not be t.Parallel().
	d := newTestDaemon(t)
	tab := d.session.CreateTab("work")

	normal, err := d.session.CreatePane(tab.ID, "/tmp")
	if err != nil {
		t.Fatalf("create normal pane: %v", err)
	}

	overlay, err := d.session.CreatePane(tab.ID, "/tmp/repo")
	if err != nil {
		t.Fatalf("create overlay pane: %v", err)
	}
	overlay.PluginMu.Lock()
	overlay.Overlay = true
	overlay.PluginMu.Unlock()

	// Simulate overlay process exit.
	d.onPaneExit(overlay, 0)

	// Overlay pane must be gone from the session.
	if p := d.session.Pane(overlay.ID); p != nil {
		t.Error("overlay pane must be destroyed after exit; still present in session")
	}

	// Normal pane must still exist.
	if p := d.session.Pane(normal.ID); p == nil {
		t.Error("normal pane must not be destroyed when overlay exits")
	}
}

// TestOnPaneExit_NormalPaneSurvivesExit verifies that a normal (non-overlay)
// pane is NOT auto-destroyed on exit — it survives as an exited husk, which
// is the existing deliberate behavior.
func TestOnPaneExit_NormalPaneSurvivesExit(t *testing.T) {
	// newTestDaemon mutates package-level vars — must not be t.Parallel().
	d := newTestDaemon(t)
	tab := d.session.CreateTab("work")

	normal, err := d.session.CreatePane(tab.ID, "/tmp")
	if err != nil {
		t.Fatalf("create normal pane: %v", err)
	}

	d.onPaneExit(normal, 1)

	// Normal pane must still exist in session (as an exited husk).
	if p := d.session.Pane(normal.ID); p == nil {
		t.Error("normal pane must survive exit as an exited husk")
	}
	// ExitCode must be set.
	normal.PluginMu.Lock()
	exitCode := normal.ExitCode
	normal.PluginMu.Unlock()
	if exitCode == nil || *exitCode != 1 {
		t.Errorf("ExitCode = %v, want 1", exitCode)
	}
}
