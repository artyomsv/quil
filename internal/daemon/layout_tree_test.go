package daemon

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/layouttree"
)

// Spec 5b §3.2: the stored tree follows pane membership under the same sm.mu
// hold, so no stored or broadcast tree references a closed pane (AC-7) and a
// replace keeps its slot (AC-13).

// CreateOverlayPane is CreatePane for an overlay, test-only: the pane is
// published already marked treeless, as buildPane publishes one.
func (sm *SessionManager) CreateOverlayPane(tabID string, cwd string) (*Pane, error) {
	return sm.createPane(tabID, cwd, true)
}

func ltLeaf(id string) *layouttree.Node { return &layouttree.Node{PaneID: id} }

func ltSplit(dir layouttree.SplitDir, ratio float64, l, r *layouttree.Node) *layouttree.Node {
	return &layouttree.Node{Split: &dir, Ratio: ratio, Left: l, Right: r}
}

func ltRaw(t *testing.T, n *layouttree.Node) json.RawMessage {
	t.Helper()
	raw, err := layouttree.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// ltTabWith creates a tab holding n fresh panes (no PTY) and returns their ids.
func ltTabWith(t *testing.T, d *Daemon, n int) (*Tab, []string) {
	t.Helper()
	tab := d.session.CreateTab("lt")
	ids := make([]string, n)
	for i := range ids {
		p, err := d.session.CreatePane(tab.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = p.ID
	}
	return tab, ids
}

func ltStore(t *testing.T, d *Daemon, tabID string, n *layouttree.Node) {
	t.Helper()
	if got := d.session.SetTabLayout(tabID, ltRaw(t, n), nil); got != layoutStored {
		t.Fatalf("SetTabLayout = %v, want layoutStored", got)
	}
}

// ltState is the tab's stored tree and revision, read under sm.mu.
func ltState(t *testing.T, d *Daemon, tabID string) (*layouttree.Node, uint64) {
	t.Helper()
	d.session.mu.RLock()
	tab := d.session.tabs[tabID]
	raw, rev := append(json.RawMessage(nil), tab.Layout...), tab.LayoutRev
	d.session.mu.RUnlock()
	n, err := layouttree.Parse(raw)
	if err != nil {
		t.Fatalf("stored layout does not parse: %v (%s)", err, raw)
	}
	return n, rev
}

func ltWant(t *testing.T, d *Daemon, tabID string, want *layouttree.Node, wantRev uint64) {
	t.Helper()
	got, rev := ltState(t, d, tabID)
	if !reflect.DeepEqual(got, want) {
		g, _ := json.Marshal(got)
		w, _ := json.Marshal(want)
		t.Errorf("stored tree = %s, want %s", g, w)
	}
	if rev != wantRev {
		t.Errorf("LayoutRev = %d, want %d", rev, wantRev)
	}
}

func TestDestroyPane_PrunesTheStoredTree(t *testing.T) {
	d := newTestDaemon(t)
	tab, id := ltTabWith(t, d, 3)
	ltStore(t, d, tab.ID, ltSplit(layouttree.Horizontal, 0.3, ltLeaf(id[0]),
		ltSplit(layouttree.Vertical, 0.7, ltLeaf(id[1]), ltLeaf(id[2]))))

	if err := d.session.DestroyPane(id[1]); err != nil {
		t.Fatal(err)
	}
	ltWant(t, d, tab.ID, ltSplit(layouttree.Horizontal, 0.3, ltLeaf(id[0]), ltLeaf(id[2])), 2)
}

func TestDestroyPane_LastLeafStoresNoTree(t *testing.T) {
	d := newTestDaemon(t)
	tab, id := ltTabWith(t, d, 1)
	ltStore(t, d, tab.ID, ltLeaf(id[0]))
	if err := d.session.DestroyPane(id[0]); err != nil {
		t.Fatal(err)
	}
	ltWant(t, d, tab.ID, nil, 2)
}

// No stored tree, or a pane the tree never held (an overlay): nothing to
// prune, so the revision stays and no client is made to re-adopt.
func TestDestroyPane_NothingToPruneKeepsTheRevision(t *testing.T) {
	d := newTestDaemon(t)
	tab, id := ltTabWith(t, d, 2)
	if err := d.session.DestroyPane(id[1]); err != nil {
		t.Fatal(err)
	}
	ltWant(t, d, tab.ID, nil, 0)

	ltStore(t, d, tab.ID, ltLeaf(id[0]))
	ov, err := d.session.CreateOverlayPane(tab.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.session.DestroyPane(ov.ID); err != nil {
		t.Fatal(err)
	}
	ltWant(t, d, tab.ID, ltLeaf(id[0]), 1)
}

// AC-13: the new pane takes the old leaf — position, orientation and a
// nested non-50/50 ratio all kept — in one revision.
func TestReplacePane_SubstitutesKeepingANestedRatio(t *testing.T) {
	d := newTestDaemon(t)
	tab, id := ltTabWith(t, d, 3)
	ltStore(t, d, tab.ID, ltSplit(layouttree.Horizontal, 0.3, ltLeaf(id[0]),
		ltSplit(layouttree.Vertical, 0.7, ltLeaf(id[1]), ltLeaf(id[2]))))

	np := d.session.NewPane("")
	if err := d.session.ReplacePane(id[1], np); err != nil {
		t.Fatal(err)
	}
	ltWant(t, d, tab.ID, ltSplit(layouttree.Horizontal, 0.3, ltLeaf(id[0]),
		ltSplit(layouttree.Vertical, 0.7, ltLeaf(np.ID), ltLeaf(id[2]))), 2)
}

func TestMovePane_PrunesTheSourceAndPlacesInTheTarget(t *testing.T) {
	d := newTestDaemon(t)
	src, s := ltTabWith(t, d, 2)
	dst, g := ltTabWith(t, d, 2)
	ltStore(t, d, src.ID, ltSplit(layouttree.Horizontal, 0.5, ltLeaf(s[0]), ltLeaf(s[1])))
	ltStore(t, d, dst.ID, ltSplit(layouttree.Horizontal, 0.4, ltLeaf(g[0]), ltLeaf(g[1])))

	if _, res := d.session.MovePane(s[1], dst.ID); res != movePaneMoved {
		t.Fatalf("MovePane = %v", res)
	}
	ltWant(t, d, src.ID, ltLeaf(s[0]), 2)
	// The spiral without the cell-size flip: the last leaf (g[1], under a
	// side-by-side parent) stacks, the moved pane below.
	ltWant(t, d, dst.ID, ltSplit(layouttree.Horizontal, 0.4, ltLeaf(g[0]),
		ltSplit(layouttree.Vertical, 0.5, ltLeaf(g[1]), ltLeaf(s[1]))), 2)
}

// A target no client has described yet keeps no tree: clients place the
// arrival and store it as before.
func TestMovePane_TargetWithoutATreeStaysEmpty(t *testing.T) {
	d := newTestDaemon(t)
	src, s := ltTabWith(t, d, 2)
	dst, _ := ltTabWith(t, d, 1)
	if _, res := d.session.MovePane(s[1], dst.ID); res != movePaneMoved {
		t.Fatalf("MovePane = %v", res)
	}
	ltWant(t, d, src.ID, nil, 0)
	ltWant(t, d, dst.ID, nil, 0)
}

func TestSetTabLayout_DeadLeafIsNeverStored(t *testing.T) {
	d := newTestDaemon(t)
	tab, id := ltTabWith(t, d, 2)
	// Unversioned (an old client) and current-revision writes alike.
	ltStore(t, d, tab.ID, ltSplit(layouttree.Horizontal, 0.5, ltLeaf(id[0]),
		ltSplit(layouttree.Vertical, 0.5, ltLeaf(id[1]), ltLeaf("pane-dead0001"))))
	ltWant(t, d, tab.ID, ltSplit(layouttree.Horizontal, 0.5, ltLeaf(id[0]), ltLeaf(id[1])), 1)

	rev := uint64(1)
	if got := d.session.SetTabLayout(tab.ID, ltRaw(t, ltSplit(layouttree.Vertical, 0.5,
		ltLeaf("pane-dead0001"), ltSplit(layouttree.Horizontal, 0.6, ltLeaf(id[0]), ltLeaf(id[1])))), &rev); got != layoutStored {
		t.Fatalf("SetTabLayout = %v", got)
	}
	ltWant(t, d, tab.ID, ltSplit(layouttree.Horizontal, 0.6, ltLeaf(id[0]), ltLeaf(id[1])), 2)
}

// A leaf from another tab, a repeated leaf and an overlay are pruned; a live
// pane the write lacks is placed by the arrival rule.
func TestSetTabLayout_ForeignDuplicateAndOverlayLeavesArePruned(t *testing.T) {
	d := newTestDaemon(t)
	tab, id := ltTabWith(t, d, 2)
	_, other := ltTabWith(t, d, 1)
	ov, err := d.session.CreateOverlayPane(tab.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	ltStore(t, d, tab.ID, ltSplit(layouttree.Horizontal, 0.5,
		ltSplit(layouttree.Vertical, 0.5, ltLeaf(id[0]), ltLeaf(other[0])),
		ltSplit(layouttree.Vertical, 0.5, ltLeaf(id[0]), ltLeaf(ov.ID))))
	// id[0] survives once; id[1] is missing, so the first leaf splits stacked.
	ltWant(t, d, tab.ID, ltSplit(layouttree.Vertical, 0.5, ltLeaf(id[0]), ltLeaf(id[1])), 1)
}

// A valid write is stored as the client's own bytes, so the TUI's echo
// detection still recognises its own write (spec §3.2, r3).
func TestSetTabLayout_ValidWriteIsStoredByteForByte(t *testing.T) {
	d := newTestDaemon(t)
	tab, id := ltTabWith(t, d, 2)
	raw := json.RawMessage(`{"split":0, "ratio":0.25, "left":{"pane_id":"` + id[0] + `"}, "right":{"pane_id":"` + id[1] + `"}}`)
	if got := d.session.SetTabLayout(tab.ID, raw, nil); got != layoutStored {
		t.Fatalf("SetTabLayout = %v", got)
	}
	d.session.mu.RLock()
	stored := string(d.session.tabs[tab.ID].Layout)
	d.session.mu.RUnlock()
	if stored != string(raw) {
		t.Errorf("stored %s, want the client's bytes %s", stored, raw)
	}
}

// The TUI's own serialization (tui.SerializeLayout: every inner node carries
// an explicit split, a leaf only its id) of a nested tree with uneven ratios
// is a valid write and must come back as the same bytes; a Clone-induced
// difference here would make every TUI write look corrected.
func TestSetTabLayout_TUIShapedWriteIsStoredByteForByte(t *testing.T) {
	d := newTestDaemon(t)
	tab, id := ltTabWith(t, d, 4)
	raw := ltRaw(t, ltSplit(layouttree.Horizontal, 1.0/3, ltLeaf(id[0]),
		ltSplit(layouttree.Vertical, 0.7, ltLeaf(id[1]),
			ltSplit(layouttree.Horizontal, 0.55, ltLeaf(id[2]), ltLeaf(id[3])))))
	if got := d.session.SetTabLayout(tab.ID, raw, nil); got != layoutStored {
		t.Fatalf("SetTabLayout = %v", got)
	}
	d.session.mu.RLock()
	stored := string(d.session.tabs[tab.ID].Layout)
	d.session.mu.RUnlock()
	if stored != string(raw) {
		t.Errorf("stored %s, want the client's bytes %s", stored, raw)
	}
}

// Malformed shapes a client can write — an inner node missing a child, a
// leaf with no id, an inner node with neither, a leaf carrying children, a
// split that is neither direction — are repaired, never stored as sent and
// never allowed to panic the tree operations (layouttree.Normalize walks
// Left without a nil check).
func TestSetTabLayout_MalformedShapesAreRepaired(t *testing.T) {
	cases := []struct {
		name string
		raw  func(id []string) string
		want func(id []string) *layouttree.Node
	}{
		{
			name: "inner node with no left child",
			raw: func(id []string) string {
				return `{"split":1,"ratio":0.5,"right":{"split":0,"ratio":0.4,"left":{"pane_id":"` + id[0] + `"},"right":{"pane_id":"` + id[1] + `"}}}`
			},
			want: func(id []string) *layouttree.Node {
				return ltSplit(layouttree.Horizontal, 0.4, ltLeaf(id[0]), ltLeaf(id[1]))
			},
		},
		{
			name: "leaf with an empty id beside an empty inner node",
			raw: func(id []string) string {
				return `{"split":0,"ratio":0.5,"left":{"pane_id":""},"right":{"split":1,"ratio":0.6,"left":{"pane_id":"` + id[0] + `"},"right":{"split":0}}}`
			},
			// Only id[0] survives; id[1] is placed by the arrival rule.
			want: func(id []string) *layouttree.Node {
				return ltSplit(layouttree.Vertical, 0.5, ltLeaf(id[0]), ltLeaf(id[1]))
			},
		},
		{
			name: "leaf carrying children and a split",
			raw: func(id []string) string {
				return `{"split":1,"ratio":0.5,"left":{"pane_id":"` + id[0] + `","split":0,"ratio":0.2,"left":{"pane_id":"pane-junk0001"}},"right":{"pane_id":"` + id[1] + `"}}`
			},
			want: func(id []string) *layouttree.Node {
				return ltSplit(layouttree.Vertical, 0.5, ltLeaf(id[0]), ltLeaf(id[1]))
			},
		},
		{
			name: "split that is neither direction",
			raw: func(id []string) string {
				return `{"split":7,"ratio":0.5,"left":{"pane_id":"` + id[0] + `"},"right":{"pane_id":"` + id[1] + `"}}`
			},
			want: func(id []string) *layouttree.Node {
				return ltSplit(layouttree.Horizontal, 0.5, ltLeaf(id[0]), ltLeaf(id[1]))
			},
		},
		{
			name: "bytes that do not parse",
			raw:  func([]string) string { return `{"split":` },
			want: func(id []string) *layouttree.Node {
				return ltSplit(layouttree.Vertical, 0.5, ltLeaf(id[0]), ltLeaf(id[1]))
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newTestDaemon(t)
			tab, id := ltTabWith(t, d, 2)
			raw := json.RawMessage(tc.raw(id))
			if got := d.session.SetTabLayout(tab.ID, raw, nil); got != layoutStored {
				t.Fatalf("SetTabLayout = %v", got)
			}
			ltWant(t, d, tab.ID, tc.want(id), 1)
			d.session.mu.RLock()
			stored := string(d.session.tabs[tab.ID].Layout)
			d.session.mu.RUnlock()
			if stored == string(raw) {
				t.Errorf("the malformed bytes were stored as sent: %s", stored)
			}
		})
	}
}

// AC-7 across snapshot/restore: a dead leaf a write carried is dropped before
// the snapshot, so it never reaches the workspace file and cannot come back.
func TestSetTabLayout_DeadLeafNeverReachesTheWorkspaceFile(t *testing.T) {
	dir := t.TempDir()
	d := newTestDaemonInDir(t, dir)
	tab, id := ltTabWith(t, d, 2)
	ltStore(t, d, tab.ID, ltSplit(layouttree.Horizontal, 0.5, ltLeaf(id[0]),
		ltSplit(layouttree.Vertical, 0.5, ltLeaf(id[1]), ltLeaf("pane-dead0001"))))
	d.snapshot()

	d2 := newTestDaemonInDir(t, dir)
	if err := d2.restoreWorkspace(); err != nil {
		t.Fatalf("restoreWorkspace: %v", err)
	}
	got, _ := ltState(t, d2, tab.ID)
	for _, p := range layouttree.PaneIDs(got) {
		if p == "pane-dead0001" {
			t.Fatal("the restored tree names a pane that was never live")
		}
	}
}

// AC-7 for a workspace.json this daemon did not validate (an older daemon
// stored writes unchecked, and restore skips a pane id it cannot accept): the
// restored tree loses every leaf that is not a live pane before anything is
// broadcast or snapshotted, keeps the rest of its shape, and keeps its
// revision. A tab whose file tree is already valid keeps its bytes.
func TestRestoreWorkspace_PrunesDeadAndSkippedLeaves(t *testing.T) {
	dir := t.TempDir()
	d := newTestDaemonInDir(t, dir)
	bad, b := ltTabWith(t, d, 2)
	good, g := ltTabWith(t, d, 2)
	ltStore(t, d, bad.ID, ltSplit(layouttree.Horizontal, 0.3, ltLeaf(b[0]), ltLeaf(b[1])))
	goodTree := ltSplit(layouttree.Vertical, 0.6, ltLeaf(g[0]), ltLeaf(g[1]))
	ltStore(t, d, good.ID, goodTree)
	d.snapshot()

	// Rewrite the file as an older daemon could have left it: the bad tab's
	// tree names a pane that is not live and one whose id restore skips, and
	// its pane list carries the skipped id too.
	const dead, skipped = "pane-dead0001", "pane-NOTHEX!"
	path := config.WorkspacePath()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var ws map[string]any
	if err := json.Unmarshal(raw, &ws); err != nil {
		t.Fatal(err)
	}
	var fileTree map[string]any
	if err := json.Unmarshal(ltRaw(t, ltSplit(layouttree.Horizontal, 0.3, ltLeaf(b[0]),
		ltSplit(layouttree.Vertical, 0.5, ltLeaf(b[1]),
			ltSplit(layouttree.Horizontal, 0.5, ltLeaf(dead), ltLeaf(skipped))))), &fileTree); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tb := range ws["tabs"].([]any) {
		tm := tb.(map[string]any)
		if tm["id"] == bad.ID {
			tm["layout"] = fileTree
			tm["panes"] = append(tm["panes"].([]any), skipped)
			found = true
		}
	}
	if !found {
		t.Fatalf("tab %s missing from workspace.json", bad.ID)
	}
	out, err := json.Marshal(ws)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}

	d2 := newTestDaemonInDir(t, dir)
	if err := d2.restoreWorkspace(); err != nil {
		t.Fatalf("restoreWorkspace: %v", err)
	}

	// The broadcast is what clients adopt; the snapshot is what the next
	// restart reads. Both come from the same stored tree.
	var got json.RawMessage
	for _, tb := range d2.buildWorkspaceState().Tabs {
		if tb.ID == bad.ID {
			got = tb.Layout
		}
	}
	tree, err := layouttree.Parse(got)
	if err != nil {
		t.Fatalf("broadcast layout does not parse: %v (%s)", err, got)
	}
	want := ltSplit(layouttree.Horizontal, 0.3, ltLeaf(b[0]), ltLeaf(b[1]))
	if !reflect.DeepEqual(tree, want) {
		g, _ := json.Marshal(tree)
		w, _ := json.Marshal(want)
		t.Errorf("broadcast tree = %s, want %s", g, w)
	}
	ltWant(t, d2, bad.ID, want, 1)
	ltWant(t, d2, good.ID, goodTree, 1)
}

func TestInsertPaneLocked_SplitsTheTargetInTheNormalizedTree(t *testing.T) {
	d := newTestDaemon(t)
	tab, id := ltTabWith(t, d, 2)
	ltStore(t, d, tab.ID, ltSplit(layouttree.Horizontal, 0.4, ltLeaf(id[0]), ltLeaf(id[1])))
	c, err := d.session.CreatePane(tab.ID, "")
	if err != nil {
		t.Fatal(err)
	}

	d.session.mu.Lock()
	ok := d.session.insertPaneLocked(d.session.tabs[tab.ID], c.ID, id[1], layouttree.Vertical)
	d.session.mu.Unlock()
	if !ok {
		t.Fatal("insert refused")
	}
	ltWant(t, d, tab.ID, ltSplit(layouttree.Horizontal, 0.4, ltLeaf(id[0]),
		ltSplit(layouttree.Vertical, 0.5, ltLeaf(id[1]), ltLeaf(c.ID))), 2)

	// Unknown target: refused, nothing stored.
	e, err := d.session.CreatePane(tab.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	d.session.mu.Lock()
	ok = d.session.insertPaneLocked(d.session.tabs[tab.ID], e.ID, "pane-nope0001", layouttree.Vertical)
	d.session.mu.Unlock()
	if ok {
		t.Fatal("an unknown target was accepted")
	}
	_, rev := ltState(t, d, tab.ID)
	if rev != 2 {
		t.Errorf("LayoutRev = %d after a refused insert, want 2", rev)
	}
}

// Empty target = first leaf of the normalized tree; with no stored tree the
// arrival rule builds the base first.
func TestInsertPaneLocked_NoTreeAndNoTarget(t *testing.T) {
	d := newTestDaemon(t)
	tab, id := ltTabWith(t, d, 2)
	c, err := d.session.CreatePane(tab.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	d.session.mu.Lock()
	ok := d.session.insertPaneLocked(d.session.tabs[tab.ID], c.ID, "", layouttree.Horizontal)
	d.session.mu.Unlock()
	if !ok {
		t.Fatal("insert refused")
	}
	// Normalize(no tree, [a b]) = a/b; the first leaf a splits left|right.
	ltWant(t, d, tab.ID, ltSplit(layouttree.Vertical, 0.5,
		ltSplit(layouttree.Horizontal, 0.5, ltLeaf(id[0]), ltLeaf(c.ID)), ltLeaf(id[1])), 1)
}

// Spec §3.2 (r3): an ID-bearing write refused by the revision check is
// answered stale, so the browser reverts its drag at once.
func TestHandleUpdateLayout_StaleIDBearingWriteIsAnswered(t *testing.T) {
	d, client := mcpTestDaemon(t)
	helloAsScript(t, client)
	tab, id := ltTabWith(t, d, 1)
	stale := uint64(7)
	resp := roundTrip(t, client, ipc.MsgUpdateLayout, ipc.MsgError,
		ipc.UpdateLayoutPayload{TabID: tab.ID, Layout: ltRaw(t, ltLeaf(id[0])), BaseRev: &stale})
	if e := decodeInto[ipc.ErrorPayload](t, resp); e.Code != ipc.ErrCodeStale || e.Type != ipc.MsgUpdateLayout {
		t.Errorf("error = %+v, want stale for update_layout", e)
	}
	ltWant(t, d, tab.ID, nil, 0)
}

// The ruling for a gone tab: an ID-bearing write is answered stale too, so
// the browser does not wait out its timeout.
func TestHandleUpdateLayout_UnknownTabIDBearingWriteIsAnsweredStale(t *testing.T) {
	_, client := mcpTestDaemon(t)
	helloAsScript(t, client)
	resp := roundTrip(t, client, ipc.MsgUpdateLayout, ipc.MsgError,
		ipc.UpdateLayoutPayload{TabID: "tab-gone0001", Layout: ltRaw(t, ltLeaf("pane-gone0001"))})
	if e := decodeInto[ipc.ErrorPayload](t, resp); e.Code != ipc.ErrCodeStale || e.Type != ipc.MsgUpdateLayout {
		t.Errorf("error = %+v, want stale for update_layout", e)
	}
}

// The TUI sends no id: it gets no answer, exactly as before.
func TestHandleUpdateLayout_StaleIDLessWriteIsSilent(t *testing.T) {
	d, client := mcpTestDaemon(t)
	helloAsScript(t, client)
	tab, id := ltTabWith(t, d, 1)
	stale := uint64(7)
	sendNoID(t, client, ipc.MsgUpdateLayout,
		ipc.UpdateLayoutPayload{TabID: tab.ID, Layout: ltRaw(t, ltLeaf(id[0])), BaseRev: &stale})
	drainNoFrameWithID(t, client, "")
}
