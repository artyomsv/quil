package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// TestParseWorkspaceState_ReadsTheDeletionMarkWireKey pins the CLIENT half of
// the wire contract. The daemon's own round-trip test proves its writer and
// reader agree with EACH OTHER, because both live in that package — a key the
// daemon writes and this parser looks for under a different name passes every
// test on both sides and simply never shows the mark.
//
// The literal is spelled out rather than shared through a constant, which would
// make the two sides agree by construction and test nothing. Its twin is
// TestSnapshot_MarkedForDeletionUsesTheWireKey in internal/daemon.
func TestParseWorkspaceState_ReadsTheDeletionMarkWireKey(t *testing.T) {
	t.Parallel()
	got := parseWorkspaceState(map[string]any{
		"panes": []any{
			map[string]any{"id": "p1", "tab_id": "t1", "marked_for_deletion": true},
			map[string]any{"id": "p2", "tab_id": "t1"},
		},
	})
	if len(got.Panes) != 2 {
		t.Fatalf("parsed %d panes, want 2", len(got.Panes))
	}
	if !got.Panes[0].MarkedForDeletion {
		t.Error(`the "marked_for_deletion" key did not reach PaneInfo — the daemon writes it under exactly that name`)
	}
	// Absent means false: the daemon omits the key when the pane is unmarked,
	// so a parser defaulting to true would advertise every pane in the
	// workspace as safe to close.
	if got.Panes[1].MarkedForDeletion {
		t.Error("a pane with no marked_for_deletion key parsed as marked")
	}
}

// TestSyncPaneMeta_AdoptsTheDaemonsDeletionMark. The daemon is the sole author
// of this mark, which is what makes it survive a restart and read the same on
// every client. A copy that only ever SET it — the shape a naive
// `if info.X { pane.X = true }` takes — would make an Unmark performed in
// another client, or the daemon's own clear when the attention pin replaces it,
// invisible here forever.
func TestSyncPaneMeta_AdoptsTheDaemonsDeletionMark(t *testing.T) {
	t.Parallel()
	pane := &PaneModel{ID: "p1"}
	syncPaneMeta(pane, &PaneInfo{ID: "p1", MarkedForDeletion: true}, false, 0, false)
	if !pane.markedForDeletion {
		t.Fatal("a daemon-reported deletion mark was not adopted")
	}
	syncPaneMeta(pane, &PaneInfo{ID: "p1", MarkedForDeletion: false}, false, 0, false)
	if pane.markedForDeletion {
		t.Error("a daemon-reported CLEAR was not adopted — the copy must be unconditional, " +
			"or an unmark from another client can never reach this one")
	}
}

// TestPaneRow_DeletionMarkSurvivesAWorkingPane is the headline of the sidebar
// half, and it is the exact situation the feature was asked for: the user is
// done with a pane whose deployment is still running. `working` outranks the
// mark in paneRow's switch — the spinner is what says the deployment has not
// finished — so the mark has to survive as a SUFFIX or it disappears for
// precisely as long as the reason to keep the pane alive lasts.
func TestPaneRow_DeletionMarkSurvivesAWorkingPane(t *testing.T) {
	t.Parallel()
	pane := &PaneModel{ID: "p1", Name: "deploy"}
	pane.markedForDeletion = true
	pane.working = true

	row := paneRow(pane, false, defaultSidebarWidth)
	if !strings.Contains(row, glyphDeletion) {
		t.Errorf("paneRow = %q dropped the deletion mark on a working pane — "+
			"the mark exists for panes that are still busy", row)
	}
	if n := lipgloss.Width(row); n != defaultSidebarWidth {
		t.Errorf("row measures %d cells, want exactly %d", n, defaultSidebarWidth)
	}
}

// TestPaneRow_DeletionMarkSuffixKeepsItsOwnColour. When a live state outranks
// the mark it survives as a suffix, and it must be painted in its OWN colour
// rather than inheriting the outranking state's — the same distinction the pin
// suffix makes. An amber ⌫ reads as part of the blocked state instead of as
// the user's own decision about the pane.
func TestPaneRow_DeletionMarkSuffixKeepsItsOwnColour(t *testing.T) {
	t.Parallel()
	delSGR := styleSGR(t, sidebarDeletionStyle)
	if delSGR == "" {
		t.Skip("lipgloss renders without colour here — this assertion cannot discriminate")
	}
	for _, tt := range []struct {
		name  string
		setup func(p *PaneModel)
	}{
		{"blocked", func(p *PaneModel) { p.blockedSince = time.Now() }},
		{"working", func(p *PaneModel) { p.working = true }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pane := &PaneModel{ID: "p1", Name: "deploy"}
			pane.markedForDeletion = true
			tt.setup(pane)
			row := paneRow(pane, false, defaultSidebarWidth)
			if !strings.Contains(row, delSGR+" "+glyphDeletion) {
				t.Errorf("paneRow = %q, want the %s suffix painted in the deletion colour, not the %s state's",
					row, glyphDeletion, tt.name)
			}
		})
	}
}

// TestPaneRow_DeletionMarkIsTheGlyphOnAnIdlePane. With nothing live to outrank
// it the mark becomes the row's primary glyph, replacing the idle ○ — the row
// is then answering "what is this pane for" with "nothing, close it", which is
// the whole point.
//
// It also outranks `unseen`: a finished turn the user has not looked at is
// exactly what they were waiting for before deciding the pane was disposable,
// so showing the ✓ over the mark would hide the decision behind its cause.
func TestPaneRow_DeletionMarkIsTheGlyphOnAnIdlePane(t *testing.T) {
	t.Parallel()
	pane := &PaneModel{ID: "p1", Name: "deploy"}
	pane.markedForDeletion = true

	row := paneRow(pane, false, defaultSidebarWidth)
	if !strings.Contains(row, glyphDeletion) {
		t.Fatalf("paneRow = %q, want the %s glyph", row, glyphDeletion)
	}
	if strings.Contains(row, glyphIdle) {
		t.Errorf("paneRow = %q still shows the idle ○ — the mark replaces it", row)
	}

	pane.unseen = true
	row = paneRow(pane, false, defaultSidebarWidth)
	if strings.Contains(row, glyphDone) {
		t.Errorf("paneRow = %q shows the unseen ✓ over the deletion mark", row)
	}
}

// TestTabLabel_MarksATabHoldingAMarkedPane. The tab bar is where the user first
// scans for what needs doing, and a tab whose panes are all disposable should
// say so without being opened.
func TestTabLabel_MarksATabHoldingAMarkedPane(t *testing.T) {
	t.Parallel()
	build := func(marked bool) Model {
		pane := newTestPane("pane-1")
		pane.markedForDeletion = marked
		m := Model{}
		m.setTabs([]*TabModel{{ID: "tab-1", Name: "build", Root: &LayoutNode{Pane: pane}, ActivePane: "other"}})
		return m
	}
	if got := build(true).tabLabel(0); !strings.Contains(got, glyphDeletion) {
		t.Errorf("tabLabel = %q, want the %s marker", got, glyphDeletion)
	}
	if got := build(false).tabLabel(0); strings.Contains(got, glyphDeletion) {
		t.Errorf("tabLabel = %q on an unmarked tab, want no marker", got)
	}
}

// TestTabStyle_DeletionMarkDoesNotRecolourTheTab pins a deliberate omission,
// which is the only kind of design decision a test can protect.
//
// The tab colour carries exactly one fact, and the ranking is by urgency:
// blocked (an agent is waiting on you) outranks pinned (come back to this)
// outranks unseen (something finished). A pane you have already decided to
// throw away is the LEAST urgent thing in the workspace — giving it a colour
// would make it compete with three states that all want the user to act. The
// glyph tabLabel adds is the whole signal, exactly as it is for the eager
// marker.
func TestTabStyle_DeletionMarkDoesNotRecolourTheTab(t *testing.T) {
	t.Parallel()
	build := func(marked bool) Model {
		pane := newTestPane("pane-1")
		pane.markedForDeletion = marked
		m := Model{}
		m.setTabs([]*TabModel{{ID: "tab-1", Name: "build", Root: &LayoutNode{Pane: pane}, ActivePane: "other"}})
		return m
	}
	plain, marked := build(false).tabStyle(0), build(true).tabStyle(0)
	if plain.GetBackground() != marked.GetBackground() {
		t.Errorf("a tab holding a marked pane renders background %v against a plain tab's %v — "+
			"the deletion mark is a glyph only, so it cannot outshout blocked/pinned/unseen",
			marked.GetBackground(), plain.GetBackground())
	}
}

// TestSidebarRows_CountsTheDeletionMarkInANonActiveProject drives the real
// sidebarRows over two projects with the mark in the one that is NOT active —
// the case the roll-up exists for, since a project you are not looking at is
// where a forgotten pane actually accumulates.
func TestSidebarRows_CountsTheDeletionMarkInANonActiveProject(t *testing.T) {
	t.Parallel()
	marked := newTestPane("pane-marked")
	marked.markedForDeletion = true
	plain := newTestPane("pane-plain")

	m := Model{
		projects: []*ProjectModel{
			{ID: "p0", Name: "active", tabs: []*TabModel{{ID: "t0", Name: "a", Root: &LayoutNode{Pane: plain}}}},
			{ID: "p1", Name: "background", tabs: []*TabModel{{ID: "t1", Name: "b", Root: &LayoutNode{Pane: marked}}}},
		},
		activeProject: 0,
		sidebarOpen:   true,
		sidebarWidth:  defaultSidebarWidth, width: 200, height: 40,
	}

	rows, _ := m.sidebarRows(defaultSidebarWidth)
	var activeRow, backgroundRow string
	for _, r := range rows {
		if r.kind != sidebarRowProject {
			continue
		}
		switch r.index {
		case 0:
			activeRow = r.text
		case 1:
			backgroundRow = r.text
		}
	}
	if backgroundRow == "" {
		t.Fatal("the background project has no row")
	}
	if want := glyphDeletion + "1"; !strings.Contains(backgroundRow, want) {
		t.Errorf("background project row %q is missing the %s badge", backgroundRow, want)
	}
	if strings.Contains(activeRow, glyphDeletion) {
		t.Errorf("active project row %q shows a deletion badge for another project's pane", activeRow)
	}
}

// TestPaneView_DeletionMarkChangesTheRender covers both halves of what a border
// colour needs: that View actually paints something different, and that the
// flag is part of paneRenderKey. PaneModel caches its render and returns the
// cached string whenever the key matches, so a border that reads the flag
// without the key being aware of it shows the OLD border until some unrelated
// input changes — for a mark set by hand and then looked at, that is
// indistinguishable from the mark not working.
func TestPaneView_DeletionMarkChangesTheRender(t *testing.T) {
	t.Parallel()
	p := NewPaneModel("pane-deletion-view", 1024)
	defer p.Dispose()
	p.Width, p.Height = 40, 12

	before := p.View()
	renders := p.renderCount

	p.markedForDeletion = true
	after := p.View()

	if p.renderCount == renders {
		t.Error("View() served the cached frame after the mark was set — paneRenderKey does not " +
			"carry markedForDeletion, so the border stays stale until some unrelated input changes")
	}
	if after == before {
		t.Error("the rendered pane is byte-identical with the mark set — the border ignores the flag")
	}
}

// TestBuildCtxMenuItems_DeletionMarkLabelFlips. The row is a toggle, so its
// label has to say which way it will go — a static "Mark for deletion" on an
// already-marked pane gives the user no way to tell the mark took.
func TestBuildCtxMenuItems_DeletionMarkLabelFlips(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	pane := m.curTabs()[0].Root.Left.Pane

	find := func() ctxMenuItem {
		t.Helper()
		for _, it := range m.buildCtxMenuItems(pane) {
			if it.id == ctxActMarkDeletion {
				return it
			}
		}
		t.Fatal("no Mark for deletion row in the context menu")
		return ctxMenuItem{}
	}

	if got := find(); got.label != "Mark for deletion" {
		t.Errorf("label = %q on an unmarked pane, want %q", got.label, "Mark for deletion")
	}
	pane.markedForDeletion = true
	if got := find(); got.label != "Unmark for deletion" {
		t.Errorf("label = %q on a marked pane, want %q", got.label, "Unmark for deletion")
	}
}

// TestCtxMenu_ExecuteMarkForDeletion_SendsTheMarkToTheDaemon asserts on the
// SENT MESSAGE, not on the local field — the daemon owns the mark and answers
// back on the next broadcast. Asserting the local bool would pass against an
// implementation that only flips it locally, which is the bug: syncPaneMeta
// overwrites it on every workspace_state (the git ticker alone delivers one
// every 5 s), so the mark would visibly undo itself and never survive a
// restart.
//
// Driven through executeCtxMenuItem and the returned Cmd rather than by calling
// the sender directly: the send only counts if the menu actually produces it.
func TestCtxMenu_ExecuteMarkForDeletion_SendsTheMarkToTheDaemon(t *testing.T) {
	t.Parallel()
	fake := &fakeSender{}
	m := newSplitDragTestModel(t)
	m.client = fake
	updated, _ := m.Update(tea.MouseClickMsg{X: 70, Y: 10, Button: tea.MouseRight})
	got := updated.(Model) // targeting p2
	updated, cmd := got.executeCtxMenuItem(ctxMenuItem{id: ctxActMarkDeletion, label: "Mark for deletion", enabled: true})
	got = updated.(Model)
	runCmd(cmd)

	if len(fake.sent) == 0 {
		t.Fatal("Mark for deletion sent nothing — the daemon owns the mark, so a purely local flip is lost on the next broadcast")
	}
	last := fake.sent[len(fake.sent)-1]
	if last.Type != ipc.MsgUpdatePane {
		t.Fatalf("sent %q, want %q", last.Type, ipc.MsgUpdatePane)
	}
	var payload ipc.UpdatePanePayload
	if err := last.DecodePayload(&payload); err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if payload.PaneID != "p2" {
		t.Errorf("PaneID = %q, want p2 — the menu targets the right-clicked pane", payload.PaneID)
	}
	if payload.MarkedForDeletion == nil {
		t.Fatal("MarkedForDeletion is nil — an absent field means 'leave it alone', so the mark was never set")
	}
	if !*payload.MarkedForDeletion {
		t.Error("MarkedForDeletion = false, want true on a Mark")
	}
	// Nothing local, for the reason above. The pane is the one the menu
	// targeted; if the handler wrote through, this is where it shows.
	if got.curTabs()[0].Root.Right.Pane.markedForDeletion {
		t.Error("the handler wrote the mark locally — the next broadcast would revert it, " +
			"and over ssh that window is hundreds of milliseconds of a mark that then vanishes")
	}

	// Unmark must send an explicit FALSE rather than omitting the field, which
	// is the whole reason the payload field is a *bool. The menu CLOSES on
	// dispatch, so it has to be reopened — executing against a closed menu
	// resolves no pane and sends nothing, which would pass a weaker assertion.
	got.curTabs()[0].Root.Right.Pane.markedForDeletion = true
	updated, _ = got.Update(tea.MouseClickMsg{X: 70, Y: 10, Button: tea.MouseRight})
	got = updated.(Model)
	before := len(fake.sent)
	_, cmd = got.executeCtxMenuItem(ctxMenuItem{id: ctxActMarkDeletion, label: "Unmark for deletion", enabled: true})
	runCmd(cmd)
	if len(fake.sent) == before {
		t.Fatal("Unmark for deletion sent nothing")
	}
	if err := fake.sent[len(fake.sent)-1].DecodePayload(&payload); err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if payload.MarkedForDeletion == nil || *payload.MarkedForDeletion {
		t.Error("Unmark for deletion did not send an explicit false")
	}
}

// TestSendMarkedForDeletion_DegenerateInputs covers the two paths the happy
// path cannot reach: an empty pane id (no Cmd at all) and a send that fails.
// Neither has a user-visible fallback by design — there is no dialog to surface
// it in from a context menu — so what is pinned here is that the failure is
// survived rather than panicking, and that nothing is sent for an empty id.
func TestSendMarkedForDeletion_DegenerateInputs(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	fake := &fakeSender{sendErr: errors.New("socket closed")}
	m.client = fake

	if cmd := m.sendMarkedForDeletion("", true); cmd != nil {
		t.Error("an empty pane id should produce no command at all")
	}
	if len(fake.sent) != 0 {
		t.Error("an empty pane id sent a message")
	}

	runCmd(m.sendMarkedForDeletion("p2", true))
	if len(fake.sent) != 1 {
		t.Errorf("sent %d messages, want 1 — the send is attempted even when it will fail", len(fake.sent))
	}
}

// TestPaneRow_BothMarksSurviveTogether pins that the two suffix width
// reservations are INDEPENDENT — neither mark may be the one dropped to fit the
// other.
//
// Read the assertions in the right order, because the obvious one is the weak
// one. The width check CANNOT fail for a missing reservation:
// renderStyledSegments truncates each segment against the remaining budget and
// pads the rest, so the row measures exactly w for any input at all. It is here
// as a cheap guard on truncateCells and the cluster-boundary precondition, and
// that is all it is. The GLYPH-PRESENCE checks are what actually guard
// paneRow's subtraction — verified by mutation: dropping delSuffix from `avail`
// fails this test on the dropped mark, never on the width.
//
// The blocked arm is the one with teeth. Under that same mutation the working
// arm PASSES, because only a long blocked reason puts enough pressure on the
// budget to push the ⌫ out of it. Do not delete it as redundant with the
// cheaper one above it.
func TestPaneRow_BothMarksSurviveTogether(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		setup func(p *PaneModel)
	}{
		{"blocked with a long reason", func(p *PaneModel) {
			p.blockedSince = time.Now()
			p.blockedReason = "AskUserQuestion"
		}},
		{"working", func(p *PaneModel) { p.working = true }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pane := &PaneModel{ID: "pane-b16e3850", Name: "agent"}
			pane.pinnedAttention = true
			pane.markedForDeletion = true
			tt.setup(pane)

			row := paneRow(pane, false, defaultSidebarWidth)
			if n := lipgloss.Width(row); n != defaultSidebarWidth {
				t.Errorf("row measures %d cells, want exactly %d — an over-wide row wraps and "+
					"desyncs every sidebar click below it", n, defaultSidebarWidth)
			}
			// Both marks survive: each is width-reserved independently, so
			// neither may be the one that gets dropped to fit the other.
			if !strings.Contains(row, glyphPinned) {
				t.Errorf("paneRow = %q dropped the pin when both marks were set", row)
			}
			if !strings.Contains(row, glyphDeletion) {
				t.Errorf("paneRow = %q dropped the deletion mark when both marks were set", row)
			}
		})
	}
}

// TestPaneView_DeletionMarkOutranksThePinOnTheBorder pins the ordering the
// border comment states. The border can hold one colour, so with both flags set
// (only reachable mid-broadcast) it must show the mark the user chose MOST
// RECENTLY — and since setting the deletion mark is what clears the pin on the
// daemon, deletion is by construction the newer of the two.
//
// Asserted by comparing renders rather than by matching an SGR literal: the
// question is "which branch won", and the deletion-only pane is the exact answer
// that branch should produce.
func TestPaneView_DeletionMarkOutranksThePinOnTheBorder(t *testing.T) {
	t.Parallel()
	deletionOnly := NewPaneModel("pane-border-cmp", 1024)
	defer deletionOnly.Dispose()
	deletionOnly.Width, deletionOnly.Height = 40, 12
	deletionOnly.markedForDeletion = true

	both := NewPaneModel("pane-border-cmp", 1024)
	defer both.Dispose()
	both.Width, both.Height = 40, 12
	both.markedForDeletion = true
	both.pinnedAttention = true

	pinOnly := NewPaneModel("pane-border-cmp", 1024)
	defer pinOnly.Dispose()
	pinOnly.Width, pinOnly.Height = 40, 12
	pinOnly.pinnedAttention = true

	if pinOnly.View() == deletionOnly.View() {
		t.Fatal("a pinned pane and a marked pane render identically — this test cannot discriminate")
	}
	if both.View() != deletionOnly.View() {
		t.Error("a pane carrying both marks does not render as the deletion mark — the border " +
			"must show the mark that was set last, and setting deletion is what clears the pin")
	}
}

// TestTabMarkedForDeletion_ExcludesTheFocusedPane pins a branch every other
// test in this file deliberately avoids: they set ActivePane to something else
// so the tab reports its mark, which means the exclusion itself was never
// exercised.
//
// The rule is the pin's: a mark is an explicit note to self, so the ACTIVE tab
// reports it too — except when the marked pane is the one in focus, because the
// tab bar answers "which tab should I go to" and the tab you are on is not an
// answer. The pane's own border is already saying it there.
func TestTabMarkedForDeletion_ExcludesTheFocusedPane(t *testing.T) {
	t.Parallel()
	build := func(activePane string) Model {
		pane := newTestPane("pane-1")
		pane.markedForDeletion = true
		m := Model{}
		m.setTabs([]*TabModel{{ID: "tab-1", Name: "build", Root: &LayoutNode{Pane: pane}, ActivePane: activePane}})
		return m
	}
	if !build("other").tabMarkedForDeletion(0) {
		t.Error("a marked pane that is NOT focused must mark its tab")
	}
	if build("pane-1").tabMarkedForDeletion(0) {
		t.Error("the focused pane of the active tab must not mark its own tab — " +
			"the tab bar points at tabs to go to, and you are already here")
	}
}

// TestCtxMenu_ClearAttentionLeavesTheDeletionMark pins the scope of that row.
// "Clear attention" drops the three attention marks and deliberately stops
// there: the deletion mark is a different vocabulary with its own Unmark row,
// and the two can never be set at once anyway.
//
// The companion assertion is that the row is DISABLED on a pane whose only mark
// is the deletion one — the row exists to answer "is this pane still flagged"
// as much as to clear it, so enabling it for a mark it will not touch would
// make it a control that visibly does nothing.
func TestCtxMenu_ClearAttentionLeavesTheDeletionMark(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	pane := m.curTabs()[0].Root.Left.Pane
	pane.markedForDeletion = true

	for _, it := range m.buildCtxMenuItems(pane) {
		if it.id == ctxActClearAttention && it.enabled {
			t.Error("Clear attention is enabled on a pane whose only mark is the deletion one, " +
				"which it does not clear — the row would do nothing")
		}
	}

	updated, _ := m.Update(tea.MouseClickMsg{X: 20, Y: 10, Button: tea.MouseRight})
	got := updated.(Model)
	updated, _ = got.executeCtxMenuItem(ctxMenuItem{id: ctxActClearAttention, label: "Clear attention", enabled: true})
	got = updated.(Model)

	if !got.curTabs()[0].Root.Left.Pane.markedForDeletion {
		t.Error("Clear attention cleared the deletion mark — it is scoped to the attention " +
			"vocabulary, and the mark has its own Unmark row")
	}
}
