package tui

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// Every workspace_state broadcast used to make the TUI re-send one
// MsgUpdateLayout per tab and one MsgResizePane per pane, unconditionally.
// Both are must-deliver frames on a 64-slot queue, so at 33 tabs + 36 panes a
// single broadcast enqueued 69 criticals, overflowed, and the client's own IPC
// layer closed the connection — which local mode reports as a clean quit
// ("TUI exited normally", 2026-08-09).
//
// These tests pin the echo away: a broadcast that already agrees with what the
// TUI holds must produce no traffic at all.

// broadcastLayout renders a layout tree the way a REAL broadcast carries it.
//
// This round trip is the whole point of the helper. The daemon stores
// MarshalLayout's bytes, but parseWorkspaceState decodes the entire state into
// map[string]any and re-marshals the layout sub-map. Go marshals a map with
// keys sorted ALPHABETICALLY and a struct in DECLARATION order, so for any node
// with more than one key the bytes that reach the client differ from
// MarshalLayout's for the identical tree.
//
// A test that skips the round trip compares struct-ordered bytes with
// struct-ordered bytes, and therefore passes against a naive byte-diff
// implementation that is wrong for every split tab in existence.
func broadcastLayout(t *testing.T, root *LayoutNode) json.RawMessage {
	t.Helper()
	raw, err := MarshalLayout(root)
	if err != nil {
		t.Fatalf("MarshalLayout: %v", err)
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("decode to generic: %v", err)
	}
	out, err := json.Marshal(generic)
	if err != nil {
		t.Fatalf("re-marshal generic: %v", err)
	}
	return out
}

// TestBroadcastLayoutBytesDifferForASplit pins the PREMISE of the tests below.
// If a struct-field reorder or a stdlib change ever made these two encodings
// agree, the unchanged-layout tests would still pass while having stopped
// testing anything — they would be comparing identical bytes.
func TestBroadcastLayoutBytesDifferForASplit(t *testing.T) {
	t.Parallel()
	root := NewLeaf(NewPaneModel("p1", 1024))
	root.SplitLeaf("p1", SplitHorizontal)
	root.Right.Pane = NewPaneModel("p2", 1024)

	direct, err := MarshalLayout(root)
	if err != nil {
		t.Fatalf("MarshalLayout: %v", err)
	}
	viaBroadcast := broadcastLayout(t, root)

	if string(direct) == string(viaBroadcast) {
		t.Fatalf("struct-ordered and map-ordered encodings agree (%s) — the "+
			"unchanged-layout tests would be comparing identical bytes and "+
			"would no longer catch a byte-diff implementation", direct)
	}
}

// echoModel builds a Model holding one SPLIT tab and one single-pane tab, then
// returns a broadcast that agrees with it exactly.
//
// The split tab is load-bearing. Production at the time of the crash had 33
// tabs and every one of them was single-leaf, so a byte-diff fix would have
// suppressed all 33 sends, passed the real-world reproduction, and still been
// wrong for anyone with a split.
func echoModel(t *testing.T) (Model, WorkspaceStateMsg) {
	t.Helper()
	m := Model{
		cfg:            config.Default(),
		notifications:  NewNotificationCenter(30, 50),
		mcpHighlights:  make(map[string]bool),
		tabDragFromIdx: -1,
		sized:          true,
		width:          100,
		height:         40,
	}
	initial := WorkspaceStateMsg{
		ActiveTab: "t-split",
		Tabs: []TabInfo{
			{ID: "t-split", Name: "Split", Panes: []string{"p1", "p2"}},
			{ID: "t-solo", Name: "Solo", Panes: []string{"p3"}},
		},
		Panes: []PaneInfo{
			{ID: "p1", TabID: "t-split"},
			{ID: "p2", TabID: "t-split"},
			{ID: "p3", TabID: "t-solo"},
		},
	}
	m.applyWorkspaceState(initial, "")
	m.resizeTabs()

	if len(m.curTabs()) != 2 {
		t.Fatalf("setup: tabs = %d, want 2", len(m.curTabs()))
	}
	if got := len(m.curTabs()[0].Leaves()); got != 2 {
		t.Fatalf("setup: split tab has %d leaves, want 2 — this test is "+
			"meaningless without a split", got)
	}

	// The echo: same tabs and panes, now carrying the layout the daemon would
	// have stored and broadcast back.
	echo := initial
	echo.Tabs = []TabInfo{
		{ID: "t-split", Name: "Split", Panes: []string{"p1", "p2"},
			Layout: broadcastLayout(t, m.curTabs()[0].Root)},
		{ID: "t-solo", Name: "Solo", Panes: []string{"p3"},
			Layout: broadcastLayout(t, m.curTabs()[1].Root)},
	}
	return m, echo
}

// echoRecorder records sends and keeps listenForMessages harmless.
//
// listenForMessages rides the SAME batch as the sends under test (model.go's
// WorkspaceStateMsg arm), so executing that batch executes it too. fakeSender's
// Receive returns (nil, nil), which nil-derefs on msg.Type; returning an error
// instead would synthesise a link loss and quit the model mid-assertion. An
// inert message is the only option that leaves the batch's other commands
// behaving exactly as they do in production.
type echoRecorder struct {
	sent []*ipc.Message
}

func (e *echoRecorder) Send(msg *ipc.Message) error {
	e.sent = append(e.sent, msg)
	return nil
}

func (e *echoRecorder) Receive() (*ipc.Message, error) {
	return &ipc.Message{Type: "test-inert"}, nil
}

func sentCounts(fs *echoRecorder) (layouts, resizes int) {
	for _, msg := range fs.sent {
		switch msg.Type {
		case ipc.MsgUpdateLayout:
			layouts++
		case ipc.MsgResizePane:
			resizes++
		}
	}
	return layouts, resizes
}

// runBatch executes a (possibly batched) command and every command it fans out
// to, so the sends that ride tea.Cmd goroutines in production land in the fake.
func runBatch(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			runBatch(c)
		}
	}
}

func TestWorkspaceState_UnchangedSplitLayout_SendsNoLayoutUpdate(t *testing.T) {
	t.Parallel()
	m, echo := echoModel(t)
	fs := &echoRecorder{}
	m.client = fs

	next, cmd := m.Update(echo)
	_ = next
	runBatch(cmd)

	layouts, _ := sentCounts(fs)
	if layouts != 0 {
		t.Errorf("MsgUpdateLayout count = %d, want 0 — the broadcast already "+
			"carried these layouts, so echoing them back is pure queue "+
			"pressure on a 64-slot must-deliver channel", layouts)
	}
}

// withSizes teaches the echo the sizes the TUI would compute, the same way the
// daemon learns them: they are the payload of the last resize it accepted.
func withSizes(m Model, echo WorkspaceStateMsg) WorkspaceStateMsg {
	for i := range echo.Panes {
		for _, tab := range m.curTabs() {
			for _, pane := range tab.Leaves() {
				if pane.ID != echo.Panes[i].ID {
					continue
				}
				cols, rows := paneVTSize(pane.WideCanvas, pane.MinNativeCols,
					pane.Width, pane.Height, pane.NativeW, tab.CanvasW, tab.CanvasH)
				echo.Panes[i].Cols = uint16(cols)
				echo.Panes[i].Rows = uint16(rows)
			}
		}
	}
	return echo
}

// The FIRST resize after attach is never suppressed, even when the reported
// size already matches. The daemon's real duplicate guard is appliedCols/Rows,
// which it ZEROES on every PTY install — a restored pane's repaint kick
// (repaintAfterResize) depends on that first client resize arriving. Diffing it
// away would leave restored panes blank until the user typed.
func TestWorkspaceState_FirstResizeAfterAttach_IsAlwaysSent(t *testing.T) {
	t.Parallel()
	m, echo := echoModel(t)
	echo = withSizes(m, echo)

	fs := &echoRecorder{}
	m.client = fs

	next, cmd := m.Update(echo)
	_ = next
	runBatch(cmd)

	_, resizes := sentCounts(fs)
	if resizes != 3 {
		t.Errorf("MsgResizePane count = %d, want 3 (one per pane) — the daemon "+
			"zeroes its applied-size guard on PTY install and needs the first "+
			"client resize to kick a repaint", resizes)
	}
}

// The crash shape is REPEATED broadcasts, not the first one: 12 reorders in
// 553 ms meant 12 rounds of tabs+panes frames. Once a pane has been sized, a
// broadcast reporting that same size must produce nothing.
func TestWorkspaceState_UnchangedSizes_SendNoResizeOnRepeat(t *testing.T) {
	t.Parallel()
	m, echo := echoModel(t)
	echo = withSizes(m, echo)

	// First broadcast: sends the sizes, satisfying the post-attach kick.
	m.client = &echoRecorder{}
	first, cmd := m.Update(echo)
	runBatch(cmd)
	m = first.(Model)

	// Second, identical broadcast — the drag-storm case.
	fs := &echoRecorder{}
	m.client = fs
	_, cmd = m.Update(echo)
	runBatch(cmd)

	_, resizes := sentCounts(fs)
	if resizes != 0 {
		t.Errorf("MsgResizePane count = %d, want 0 on a repeat broadcast — "+
			"every pane already has the size the broadcast reports", resizes)
	}
}

// The reported configuration, at scale: 33 tabs and 36 panes, which made one
// broadcast enqueue 69 must-deliver frames onto a 64-slot queue. The count is
// what matters here — the queue does not care which tab a frame describes.
func TestWorkspaceState_ReportedCrashConfiguration_SettlesToSilence(t *testing.T) {
	t.Parallel()
	const tabs, splits = 33, 3 // 30 single-leaf + 3 two-pane = 36 panes

	m := Model{
		cfg:            config.Default(),
		notifications:  NewNotificationCenter(30, 200),
		mcpHighlights:  make(map[string]bool),
		tabDragFromIdx: -1,
		sized:          true,
		width:          209,
		height:         58,
	}
	state := WorkspaceStateMsg{ActiveTab: "tab-0"}
	for i := 0; i < tabs; i++ {
		tabID := "tab-" + strconv.Itoa(i)
		paneIDs := []string{"pane-" + strconv.Itoa(i) + "a"}
		if i < splits {
			paneIDs = append(paneIDs, "pane-"+strconv.Itoa(i)+"b")
		}
		state.Tabs = append(state.Tabs, TabInfo{ID: tabID, Name: tabID, Panes: paneIDs})
		for _, pid := range paneIDs {
			state.Panes = append(state.Panes, PaneInfo{ID: pid, TabID: tabID})
		}
	}
	if len(state.Panes) != 36 {
		t.Fatalf("setup: panes = %d, want 36", len(state.Panes))
	}

	m.applyWorkspaceState(state, "")
	m.resizeTabs()

	// Round one: the daemon knows no layouts and no sizes yet, so this is the
	// legitimate expensive broadcast. It is bounded by backpressure, not by
	// suppression — see the ipc client-side SendBlocking test.
	echo := state
	for i := range echo.Tabs {
		echo.Tabs[i].Layout = broadcastLayout(t, m.curTabs()[i].Root)
	}
	echo = withSizes(m, echo)

	m.client = &echoRecorder{}
	next, cmd := m.Update(echo)
	runBatch(cmd)
	m = next.(Model)

	// Round two: identical state, the shape a reorder storm repeats 12 times.
	fs := &echoRecorder{}
	m.client = fs
	_, cmd = m.Update(echo)
	runBatch(cmd)

	layouts, resizes := sentCounts(fs)
	if layouts+resizes != 0 {
		t.Errorf("a repeat broadcast at 33 tabs/36 panes produced %d layout + %d "+
			"resize frames, want 0 — 69 of these on a %d-slot must-deliver queue "+
			"is what closed the connection", layouts, resizes, 64)
	}
}

// A reconnect reinstalls the daemon's PTYs, which zeroes its applied-size
// guard — so the first-resize kick has to be re-armed or a restored pane comes
// back blank and stays blank.
func TestReattach_ReArmsTheFirstResizeKick(t *testing.T) {
	t.Parallel()
	m, echo := echoModel(t)
	echo = withSizes(m, echo)

	m.client = &echoRecorder{}
	first, cmd := m.Update(echo)
	runBatch(cmd)
	m = first.(Model)

	m.armReattachReset("")

	fs := &echoRecorder{}
	m.client = fs
	_, cmd = m.Update(echo)
	runBatch(cmd)

	_, resizes := sentCounts(fs)
	if resizes != 3 {
		t.Errorf("MsgResizePane count = %d after reattach, want 3 — the daemon "+
			"zeroed its guard on PTY install, so the suppression state from "+
			"before the outage describes a daemon that no longer exists", resizes)
	}
}

// A broadcast is the full state of ONE daemon. It says nothing about another
// daemon's tabs, so it must not trigger sends for them — diffing them against
// it would find "no stored layout" and re-send every one. Before scoping, any
// daemon's broadcast re-sent every daemon's layouts.
func TestWorkspaceState_DoesNotResendAnotherDaemonsTabs(t *testing.T) {
	t.Parallel()
	m, echo := echoModel(t)

	remote := NewTabModel("t-remote", "Remote")
	remote.Root = NewLeaf(NewPaneModel("p-remote", 1024))
	m.projects = append(m.projects, &ProjectModel{
		ID: "proj-gpu", Name: "remote", Dest: "gpu01",
		tabs: []*TabModel{remote},
	})

	fs := &echoRecorder{}
	m.client = fs

	// echo carries Dest "" — the local daemon.
	next, cmd := m.Update(echo)
	runBatch(cmd)

	// Guard the premise. If the local broadcast's reconciliation dropped the
	// remote project, no send for it is possible and the assertion below would
	// hold for a reason that has nothing to do with scoping — this test passed
	// with the scoping deleted before this guard existed.
	// The TAB, not just the project: an earlier version of this guard checked
	// only that the project survived, and the reconciliation had emptied its
	// tab list — leaving nothing for the diff to walk, so the test passed with
	// the scoping deleted.
	var remoteTabs int
	for _, proj := range next.(Model).projects {
		if proj.Dest != "gpu01" {
			continue
		}
		for _, tab := range proj.tabs {
			if tab.Root != nil {
				remoteTabs++
			}
		}
	}
	if remoteTabs == 0 {
		t.Fatal("setup: no remote tab with a layout survived the local " +
			"broadcast, so this test cannot observe whether one was re-sent")
	}

	// Matched by SUBSTRING against every payload rather than by decoding a
	// specific field: the leak this guards against showed up first as a resize
	// and then as a layout under a tab id the assertion was not looking at, so
	// the check must not depend on knowing which field carries the identifier.
	remoteIDs := []string{remote.ID}
	for _, pane := range remote.Leaves() {
		remoteIDs = append(remoteIDs, pane.ID)
	}
	for _, msg := range fs.sent {
		body := msg.Type + " " + string(msg.Payload)
		for _, id := range remoteIDs {
			if strings.Contains(string(msg.Payload), id) {
				t.Errorf("a local-daemon broadcast produced %q, which names the "+
					"remote daemon's %q — the broadcast contains nothing to diff "+
					"that against, so everything it owns looks changed", body, id)
			}
		}
	}
}

// A daemon that holds no layout for a tab (freshly created, or restored before
// any client described it) MUST still be told. Suppressing this send would lose
// the user's arrangement on disk, silently and permanently.
func TestWorkspaceState_AbsentLayout_StillSends(t *testing.T) {
	t.Parallel()
	m, echo := echoModel(t)
	echo.Tabs[0].Layout = nil

	fs := &echoRecorder{}
	m.client = fs

	_, cmd := m.Update(echo)
	runBatch(cmd)

	layouts, _ := sentCounts(fs)
	if layouts != 1 {
		t.Errorf("MsgUpdateLayout count = %d, want 1 — a tab the daemon has no "+
			"layout for must be sent, or the arrangement is never persisted",
			layouts)
	}
}

// The mirror of the suppression test: a real divergence must still reach the
// daemon, and only for the tab that diverged.
func TestWorkspaceState_ChangedLayout_SendsOnlyThatTab(t *testing.T) {
	t.Parallel()
	m, echo := echoModel(t)

	// The daemon's copy of the split tab is a stale single leaf.
	stale := NewLeaf(NewPaneModel("p1", 1024))
	echo.Tabs[0].Layout = broadcastLayout(t, stale)

	fs := &echoRecorder{}
	m.client = fs

	_, cmd := m.Update(echo)
	runBatch(cmd)

	var gotTabs []string
	for _, msg := range fs.sent {
		if msg.Type != ipc.MsgUpdateLayout {
			continue
		}
		var p ipc.UpdateLayoutPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			t.Fatalf("decode layout payload: %v", err)
		}
		gotTabs = append(gotTabs, p.TabID)
	}
	if len(gotTabs) != 1 || gotTabs[0] != "t-split" {
		t.Errorf("layout sends = %v, want exactly [t-split]", gotTabs)
	}
}
