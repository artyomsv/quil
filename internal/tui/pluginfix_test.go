package tui

import (
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// The plugin dialog opens on its own, from pane output, so every test here
// drives it the way the daemon and the user do: PluginErrorMsg and
// WorkspaceStateMsg in, keys in, and the assertion is on what left through
// the client — a helper-level test would pass with the letter keys, the hold
// and the y/n step all disconnected from Update.

// pluginTestClock is the Model's injectable clock, so a test can stand on
// either side of pluginLetterGrace.
type pluginTestClock struct{ now time.Time }

func (c *pluginTestClock) advance(d time.Duration) { c.now = c.now.Add(d) }

// newPluginDialogTestModel is newMovePaneTestModel (p1 active in the active
// tab, p2 beside it, p3 in a background tab, p4 in another project, a remote
// project on user@TEST-host with an empty tab) with a fake conn capturing
// every send and a clock the test moves.
func newPluginDialogTestModel(t *testing.T) (*Model, *fakeConn, *pluginTestClock) {
	t.Helper()
	m := newMovePaneTestModel(t)
	fake := newFakeConn()
	m.client = fake
	clk := &pluginTestClock{now: time.Unix(1_700_000_000, 0)}
	m.now = func() time.Time { return clk.now }
	return m, fake, clk
}

// remotePaneOnScreen puts pane rp in the remote project's tab and makes that
// project active, so a frame from user@TEST-host is for a pane on screen.
func remotePaneOnScreen(t *testing.T, m *Model) {
	t.Helper()
	for i, p := range m.projects {
		if p.ID == "proj-remote" {
			rp := NewPaneModel("rp", 1024)
			p.tabs[0].Root = NewLeaf(rp)
			p.tabs[0].ActivePane = "rp"
			m.activeProject = i
			return
		}
	}
	t.Fatal("fixture has no proj-remote")
}

func pluginErrorFrame(paneID string) PluginErrorMsg {
	return PluginErrorMsg{
		PaneID:  paneID,
		Title:   "Stripe login expired",
		Message: "Log in again to continue.",
		Plugin:  "stripe",
		Fixes:   []ipc.PluginFixInfo{{Name: "login", Label: "Log in"}},
	}
}

func pluginUpdateFrame(paneID string) PluginErrorMsg {
	return PluginErrorMsg{
		PaneID:  paneID,
		Title:   "Update available: Stripe CLI",
		Message: "Stripe CLI 1.56.0 is available.",
		Plugin:  "stripe",
		Update:  &ipc.PluginUpdateInfo{Offered: "1.56.0", Target: "/usr/local/bin/stripe"},
	}
}

func letterKey(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

func pluginUpdate(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	updated, _ := m.Update(msg)
	got, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", updated)
	}
	return got
}

// fixReqSent decodes the one plugin_fix_req the fake saw.
func fixReqSent(t *testing.T, fake *fakeConn) (*ipc.Message, ipc.PluginFixReqPayload) {
	t.Helper()
	if fake.sentCount() != 1 {
		t.Fatalf("sent %d messages, want exactly 1", fake.sentCount())
	}
	msg := fake.lastSent()
	if msg.Type != ipc.MsgPluginFixReq {
		t.Fatalf("sent %q, want %q", msg.Type, ipc.MsgPluginFixReq)
	}
	var p ipc.PluginFixReqPayload
	if err := msg.DecodePayload(&p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return msg, p
}

func TestPluginDialog_EnterOnArrivalOnlyCloses(t *testing.T) {
	t.Parallel()
	m, fake, _ := newPluginDialogTestModel(t)
	got := pluginUpdate(t, *m, pluginErrorFrame("p1"))
	if got.dialog != dialogPluginError || got.pluginDialogOpen != "p1" {
		t.Fatalf("setup: dialog=%v open=%q, want the plugin dialog for p1", got.dialog, got.pluginDialogOpen)
	}

	got = pluginUpdate(t, got, tea.KeyPressMsg{Code: tea.KeyEnter})

	if got.dialog != dialogNone {
		t.Errorf("dialog = %v after Enter, want closed", got.dialog)
	}
	if fake.sentCount() != 0 {
		t.Errorf("Enter on a dialog that opened by itself sent %d message(s) — a stray Enter must only close", fake.sentCount())
	}
	if len(got.pluginDialogs) != 0 {
		t.Errorf("pluginDialogs = %d after close, want 0", len(got.pluginDialogs))
	}
}

func TestPluginDialog_FixLetterSendsName(t *testing.T) {
	t.Parallel()
	m, fake, clk := newPluginDialogTestModel(t)
	got := pluginUpdate(t, *m, pluginErrorFrame("p1"))
	clk.advance(pluginLetterGrace)

	got = pluginUpdate(t, got, letterKey('l'))

	msg, p := fixReqSent(t, fake)
	if p.PaneID != "p1" || p.Fix != "login" || p.Version != "" {
		t.Errorf("payload = %+v, want pane p1, fix login, no version", p)
	}
	if msg.ID == "" {
		t.Error("plugin_fix_req sent without an ID — the daemon's refusal would be silence")
	}
	if got.dialog != dialogNone {
		t.Errorf("dialog = %v after the fix left, want closed", got.dialog)
	}
}

// The letters are inert for pluginLetterGrace after the dialog opens: it
// opened under fingers typing at the pane. Enter and Esc are live at once.
func TestPluginDialog_LetterInertRightAfterOpen(t *testing.T) {
	t.Parallel()
	m, fake, clk := newPluginDialogTestModel(t)
	got := pluginUpdate(t, *m, pluginErrorFrame("p1"))

	got = pluginUpdate(t, got, letterKey('l'))
	if fake.sentCount() != 0 || got.dialog != dialogPluginError {
		t.Fatalf("l right after open: sent=%d dialog=%v, want nothing sent and the dialog still up", fake.sentCount(), got.dialog)
	}
	got = pluginUpdate(t, got, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got.dialog != dialogNone {
		t.Fatalf("Enter right after open did not close: dialog=%v", got.dialog)
	}

	got = pluginUpdate(t, got, pluginErrorFrame("p1"))
	clk.advance(pluginLetterGrace - time.Millisecond)
	got = pluginUpdate(t, got, letterKey('l'))
	if fake.sentCount() != 0 {
		t.Fatalf("l one ms inside the grace sent %d message(s)", fake.sentCount())
	}
	clk.advance(time.Millisecond)
	pluginUpdate(t, got, letterKey('l'))
	if _, p := fixReqSent(t, fake); p.Fix != "login" {
		t.Errorf("fix = %q, want login once the grace is over", p.Fix)
	}
}

// Arrows + Enter reach the same fix with no grace: focus starts on Close, one
// Left is the fix button.
func TestPluginDialog_ArrowThenEnterSendsFix(t *testing.T) {
	t.Parallel()
	m, fake, _ := newPluginDialogTestModel(t)
	got := pluginUpdate(t, *m, pluginErrorFrame("p1"))
	got = pluginUpdate(t, got, tea.KeyPressMsg{Code: tea.KeyLeft})
	got = pluginUpdate(t, got, tea.KeyPressMsg{Code: tea.KeyEnter})

	_, p := fixReqSent(t, fake)
	if p.Fix != "login" {
		t.Errorf("fix = %q, want login", p.Fix)
	}
	if got.dialog != dialogNone {
		t.Errorf("dialog = %v, want closed", got.dialog)
	}
}

func TestPluginDialog_UpdateNeedsY(t *testing.T) {
	t.Parallel()
	m, fake, clk := newPluginDialogTestModel(t)
	got := pluginUpdate(t, *m, pluginUpdateFrame("p1"))
	if got.dialog != dialogPluginError {
		t.Fatalf("setup: dialog=%v, want the plugin dialog", got.dialog)
	}
	clk.advance(pluginLetterGrace)

	got = pluginUpdate(t, got, letterKey('u'))
	if d := got.pluginDialogs["p1"]; d == nil || !d.confirming {
		t.Fatal("u did not arm the Replace step")
	}
	if out := stripANSI(got.renderPluginErrorDialog()); !strings.Contains(out, "Replace /usr/local/bin/stripe? y/n") {
		t.Errorf("confirm render does not name the binary:\n%s", out)
	}
	if fake.sentCount() != 0 {
		t.Fatalf("arming the confirm sent %d message(s)", fake.sentCount())
	}

	// n cancels the confirm and sends nothing; the dialog stays.
	got = pluginUpdate(t, got, letterKey('n'))
	if fake.sentCount() != 0 {
		t.Errorf("n on the Replace step sent %d message(s), want 0", fake.sentCount())
	}
	if got.dialog != dialogPluginError {
		t.Fatalf("dialog = %v after n, want still open", got.dialog)
	}
	if d := got.pluginDialogs["p1"]; d == nil || d.confirming {
		t.Fatal("n did not leave the Replace step")
	}

	got = pluginUpdate(t, got, letterKey('u'))
	got = pluginUpdate(t, got, letterKey('y'))
	if fake.sentCount() != 1 {
		t.Fatalf("sent %d messages after y, want 1", fake.sentCount())
	}
	msg := fake.lastSent()
	if msg.Type != ipc.MsgPluginUpdateReq {
		t.Fatalf("sent %q, want %q", msg.Type, ipc.MsgPluginUpdateReq)
	}
	var p ipc.PluginUpdateReqPayload
	if err := msg.DecodePayload(&p); err != nil {
		t.Fatal(err)
	}
	if p.PaneID != "p1" || p.Version != "1.56.0" {
		t.Errorf("payload = %+v, want pane p1 at the offered 1.56.0", p)
	}
	if msg.ID == "" {
		t.Error("plugin_update_req sent without an ID")
	}
	if got.dialog != dialogNone {
		t.Errorf("dialog = %v after y, want closed", got.dialog)
	}
}

// A binary a package manager owns is upgraded by that manager, so the
// question names it rather than promising to replace a path a download never
// touches; y still sends the offered version.
func TestPluginDialog_ManagerConfirmNamesTheManager(t *testing.T) {
	t.Parallel()
	m, fake, clk := newPluginDialogTestModel(t)
	frame := pluginUpdateFrame("p1")
	frame.Update.Manager = "brew"
	got := pluginUpdate(t, *m, frame)
	clk.advance(pluginLetterGrace)

	got = pluginUpdate(t, got, letterKey('u'))
	out := stripANSI(got.renderPluginErrorDialog())
	if !strings.Contains(out, "Update with brew? y/n") || strings.Contains(out, "Replace") {
		t.Errorf("confirm render for a managed binary:\n%s", out)
	}
	got = pluginUpdate(t, got, letterKey('y'))
	if fake.sentCount() != 1 || fake.lastSent().Type != ipc.MsgPluginUpdateReq {
		t.Fatalf("y sent %d message(s), want one plugin_update_req", fake.sentCount())
	}
	var p ipc.PluginUpdateReqPayload
	if err := fake.lastSent().DecodePayload(&p); err != nil {
		t.Fatal(err)
	}
	if p.Version != "1.56.0" {
		t.Errorf("version = %q, want the offered 1.56.0", p.Version)
	}
	if got.dialog != dialogNone {
		t.Errorf("dialog = %v after y, want closed", got.dialog)
	}
}

// A dialog for an overlay pane offers nothing but Close: the daemon refuses
// every fix request for an overlay, so the buttons are greyed with the
// reason rather than offered and refused.
func TestPluginDialog_OverlayGreysAllButClose(t *testing.T) {
	t.Parallel()
	m, fake, clk := newPluginDialogTestModel(t)
	tab := m.activeTabModel()
	if tab == nil {
		t.Fatal("fixture has no active tab")
	}
	tab.overlayPane = NewPaneModel("ov", 1024)

	got := pluginUpdate(t, *m, pluginErrorFrame("ov"))
	got = pluginUpdate(t, got, pluginUpdateFrame("ov"))
	if got.dialog != dialogPluginError || got.pluginDialogOpen != "ov" {
		t.Fatalf("dialog=%v open=%q, want the overlay pane's dialog", got.dialog, got.pluginDialogOpen)
	}
	clk.advance(pluginLetterGrace)

	got = pluginUpdate(t, got, letterKey('l'))
	got = pluginUpdate(t, got, letterKey('u'))
	got = pluginUpdate(t, got, letterKey('n'))
	if fake.sentCount() != 0 {
		t.Errorf("sent %d message(s) for an overlay pane, want 0", fake.sentCount())
	}
	d := got.pluginDialogs["ov"]
	if d == nil || d.confirming || got.dialog != dialogPluginError {
		t.Fatal("a greyed button armed the Replace step or closed the dialog")
	}
	// Arrows + Enter on a greyed button do nothing either.
	got = pluginUpdate(t, got, tea.KeyPressMsg{Code: tea.KeyLeft})
	got = pluginUpdate(t, got, tea.KeyPressMsg{Code: tea.KeyEnter})
	if fake.sentCount() != 0 || got.dialog != dialogPluginError {
		t.Errorf("Enter on a greyed button: sent=%d dialog=%v", fake.sentCount(), got.dialog)
	}
	out := stripANSI(got.renderPluginErrorDialog())
	if !strings.Contains(out, "Log in, Update, Not now: "+overlayDisabledReason) {
		t.Errorf("render does not grey the three buttons with the overlay reason:\n%s", out)
	}
	got = pluginUpdate(t, got, tea.KeyPressMsg{Code: tea.KeyEscape})
	if got.dialog != dialogNone {
		t.Errorf("Esc did not close: dialog=%v", got.dialog)
	}
}

// An update-only dialog has a visible Close too (Esc alone is not
// discoverable), and that is where focus starts: Enter closes and records
// nothing. Not now, by its letter, records the offered version and runs
// nothing.
func TestPluginDialog_NotNowOnUpdateNotice(t *testing.T) {
	t.Parallel()
	m, fake, clk := newPluginDialogTestModel(t)
	got := pluginUpdate(t, *m, pluginUpdateFrame("p1"))
	btns := got.pluginDialogs["p1"].buttons()
	if len(btns) != 3 || btns[2].kind != pluginButtonClose {
		t.Fatalf("buttons = %+v, want Update, Not now, Close", btns)
	}
	got = pluginUpdate(t, got, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got.dialog != dialogNone || fake.sentCount() != 0 {
		t.Fatalf("Enter: dialog=%v sent=%d, want closed and nothing sent", got.dialog, fake.sentCount())
	}

	got = pluginUpdate(t, got, pluginUpdateFrame("p1"))
	clk.advance(pluginLetterGrace)
	got = pluginUpdate(t, got, letterKey('n'))

	_, p := fixReqSent(t, fake)
	if p.Fix != ipc.PluginFixNotNow || p.Version != "1.56.0" || p.PaneID != "p1" {
		t.Errorf("payload = %+v, want not_now for 1.56.0 on p1", p)
	}
	if got.dialog != dialogNone {
		t.Errorf("dialog = %v, want closed", got.dialog)
	}
}

// Every rendered row fits the box: long lines are clamped and eight fixes
// wrap onto further button rows.
func TestPluginDialog_RenderFitsTheDialogWidth(t *testing.T) {
	t.Parallel()
	m, _, _ := newPluginDialogTestModel(t)
	frame := pluginErrorFrame("p1")
	frame.Title = strings.Repeat("Title ", 20)
	frame.Message = strings.Repeat("a long line of explanation ", 12) + "\nshort"
	frame.Fixes = nil
	for i := 0; i < maxPluginDialogFixes; i++ {
		frame.Fixes = append(frame.Fixes, ipc.PluginFixInfo{
			Name:  "fix" + string(rune('a'+i)),
			Label: strings.Repeat(string(rune('A'+i)), 30),
		})
	}
	got := pluginUpdate(t, *m, frame)
	got = pluginUpdate(t, got, pluginUpdateFrame("p1"))
	got.pluginDialogs["p1"].notice = "could not send: " + strings.Repeat("x", 100)

	out := stripANSI(got.renderPluginErrorDialog())
	rows := strings.Split(out, "\n")
	for i, row := range rows {
		if w := lipgloss.Width(row); w > pluginDialogInnerWidth {
			t.Errorf("row %d is %d cells, max %d: %q", i, w, pluginDialogInnerWidth, row)
		}
	}
	if n := strings.Count(out, "[A]"); n != 1 {
		t.Errorf("first fix button rendered %d times", n)
	}
	if len(rows) < 8 {
		t.Errorf("only %d rows — the eight buttons did not wrap:\n%s", len(rows), out)
	}
	// The long message is wrapped, not cut: every word of it is on screen.
	if n := strings.Count(out, "explanation"); n != 12 {
		t.Errorf("message shows %d of 12 sentences — it was cut, not wrapped:\n%s", n, out)
	}
}

func TestWrapPluginLine(t *testing.T) {
	t.Parallel()
	got := wrapPluginLine("Stripe keys from 'stripe login' last 90 days. Log in again to continue.", 52)
	want := []string{"Stripe keys from 'stripe login' last 90 days. Log in", "again to continue."}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("wrapPluginLine = %q, want %q", got, want)
	}
	if got := wrapPluginLine("", 52); !reflect.DeepEqual(got, []string{""}) {
		t.Errorf("empty line = %q, want one empty row (keeps blank lines)", got)
	}
	long := strings.Repeat("x", 80)
	if got := wrapPluginLine(long, 52); len(got) != 1 || lipgloss.Width(got[0]) > 52 {
		t.Errorf("an over-wide word = %q, want one row cut to 52 cells", got)
	}
}

func TestPluginDialog_HeldUnderOpenDialog(t *testing.T) {
	t.Parallel()
	m, _, _ := newPluginDialogTestModel(t)
	m.dialog = dialogAbout
	m.dialogCursor = 0

	got := pluginUpdate(t, *m, pluginErrorFrame("p1"))
	if got.dialog != dialogAbout {
		t.Fatalf("dialog = %v, want About still open — the plugin dialog must not replace it", got.dialog)
	}
	if got.pluginDialogs["p1"] == nil {
		t.Fatal("the frame was not held for p1")
	}

	// Esc closes About; the held dialog opens on the way out of that Update.
	got = pluginUpdate(t, got, tea.KeyPressMsg{Code: tea.KeyEscape})
	if got.dialog != dialogPluginError || got.pluginDialogOpen != "p1" {
		t.Errorf("dialog=%v open=%q after About closed, want the held plugin dialog for p1", got.dialog, got.pluginDialogOpen)
	}
}

// A rename owns the keyboard as much as a dialog does: the held dialog waits
// until it ends.
func TestPluginDialog_HeldWhileRenaming(t *testing.T) {
	t.Parallel()
	m, _, _ := newPluginDialogTestModel(t)
	m.renaming = true

	got := pluginUpdate(t, *m, pluginErrorFrame("p1"))
	if got.dialog != dialogNone || got.pluginDialogs["p1"] == nil {
		t.Fatalf("dialog=%v held=%v during a rename, want none and held", got.dialog, got.pluginDialogs["p1"] != nil)
	}

	got.renaming = false
	got = pluginUpdate(t, got, flashExpireMsg{})
	if got.dialog != dialogPluginError || got.pluginDialogOpen != "p1" {
		t.Errorf("dialog=%v open=%q after the rename ended, want the held dialog", got.dialog, got.pluginDialogOpen)
	}
}

// A second pane's frame while a plugin dialog is open waits its turn.
func TestPluginDialog_SecondPaneWaitsForTheFirst(t *testing.T) {
	t.Parallel()
	m, fake, _ := newPluginDialogTestModel(t)
	got := pluginUpdate(t, *m, pluginErrorFrame("p1"))
	got = pluginUpdate(t, got, pluginErrorFrame("p2"))
	if got.pluginDialogOpen != "p1" {
		t.Fatalf("open = %q, want p1 still — p2's dialog must be held", got.pluginDialogOpen)
	}

	got = pluginUpdate(t, got, tea.KeyPressMsg{Code: tea.KeyEscape})
	if got.dialog != dialogPluginError || got.pluginDialogOpen != "p2" {
		t.Errorf("dialog=%v open=%q after closing p1's, want p2's", got.dialog, got.pluginDialogOpen)
	}
	if fake.sentCount() != 0 {
		t.Errorf("closing sent %d message(s)", fake.sentCount())
	}
}

func TestPluginDialog_HeldForOffScreenPane(t *testing.T) {
	t.Parallel()
	m, _, _ := newPluginDialogTestModel(t)

	// p3 lives in tab-src-2, which is not the active tab.
	got := pluginUpdate(t, *m, pluginErrorFrame("p3"))
	if got.dialog != dialogNone {
		t.Fatalf("dialog = %v for an off-screen pane, want none", got.dialog)
	}
	if got.pluginDialogs["p3"] == nil {
		t.Fatal("the frame was not held for p3")
	}

	// The user (or MCP) goes to p3: its tab becomes active, the dialog opens.
	got = pluginUpdate(t, got, setActivePaneMsg{PaneID: "p3"})
	if got.dialog != dialogPluginError || got.pluginDialogOpen != "p3" {
		t.Errorf("dialog=%v open=%q after switching to p3's tab, want its held dialog", got.dialog, got.pluginDialogOpen)
	}
}

func TestPluginDialog_MergeSamePane(t *testing.T) {
	t.Parallel()
	labels := func(d *pluginDialog) []string {
		var out []string
		for _, b := range d.buttons() {
			out = append(out, b.label)
		}
		return out
	}
	want := "Log in|Update|Not now|Close"

	for _, order := range []struct {
		name          string
		first, second PluginErrorMsg
	}{
		{"error then update", pluginErrorFrame("p1"), pluginUpdateFrame("p1")},
		{"update then error", pluginUpdateFrame("p1"), pluginErrorFrame("p1")},
	} {
		t.Run(order.name, func(t *testing.T) {
			m, _, _ := newPluginDialogTestModel(t)
			got := pluginUpdate(t, *m, order.first)
			got = pluginUpdate(t, got, order.second)

			if len(got.pluginDialogs) != 1 || got.pluginDialogOpen != "p1" {
				t.Fatalf("dialogs=%d open=%q, want one merged dialog for p1", len(got.pluginDialogs), got.pluginDialogOpen)
			}
			d := got.pluginDialogs["p1"]
			if gotLabels := strings.Join(labels(d), "|"); gotLabels != want {
				t.Errorf("buttons = %s, want %s", gotLabels, want)
			}
			if d.focus != len(d.buttons())-1 {
				t.Errorf("focus = %d, want Close (last)", d.focus)
			}
			out := stripANSI(got.renderPluginErrorDialog())
			errAt := strings.Index(out, "Stripe login expired")
			updAt := strings.Index(out, "Update available")
			if errAt < 0 || updAt < 0 || errAt > updAt {
				t.Errorf("error text must come first in either order (err@%d upd@%d):\n%s", errAt, updAt, out)
			}
		})
	}

	// The same handler matching again does not duplicate its button.
	m, _, _ := newPluginDialogTestModel(t)
	got := pluginUpdate(t, *m, pluginErrorFrame("p1"))
	got = pluginUpdate(t, got, pluginErrorFrame("p1"))
	if n := len(got.pluginDialogs["p1"].fixes); n != 1 {
		t.Errorf("fixes = %d after the same frame twice, want 1", n)
	}
}

// pluginDialogWorkspace is the fixture's workspace as the local daemon would
// broadcast it, with p1 running a fix.
func pluginDialogWorkspace(fixLabel string) WorkspaceStateMsg {
	return WorkspaceStateMsg{
		Dest: "",
		Projects: []ProjectInfo{
			{ID: "proj-source", Name: "source", TabIDs: []string{"tab-src-1", "tab-src-2"}, ActiveTab: "tab-src-1"},
			{ID: "proj-b", Name: "beta", TabIDs: []string{"tab-b-1"}, ActiveTab: "tab-b-1"},
		},
		Tabs: []TabInfo{
			{ID: "tab-src-1", Name: "Shell", ProjectID: "proj-source", Panes: []string{"p1", "p2"}},
			{ID: "tab-src-2", Name: "Build", ProjectID: "proj-source", Panes: []string{"p3"}},
			{ID: "tab-b-1", Name: "Shell", ProjectID: "proj-b", Panes: []string{"p4"}},
		},
		Panes: []PaneInfo{
			{ID: "p1", TabID: "tab-src-1", Type: "terminal", FixLabel: fixLabel},
			{ID: "p2", TabID: "tab-src-1", Type: "terminal"},
			{ID: "p3", TabID: "tab-src-2", Type: "terminal"},
			{ID: "p4", TabID: "tab-b-1", Type: "terminal"},
		},
	}
}

func TestPluginDialog_ClosedWhenFixStarts(t *testing.T) {
	t.Parallel()
	m, _, _ := newPluginDialogTestModel(t)
	got := pluginUpdate(t, *m, pluginErrorFrame("p1"))
	if got.dialog != dialogPluginError {
		t.Fatalf("setup: dialog=%v", got.dialog)
	}

	// Another client pressed Log in: the broadcast shows p1 running the fix.
	got = pluginUpdate(t, got, pluginDialogWorkspace("Log in"))

	if got.dialog != dialogNone {
		t.Errorf("dialog = %v after FixLabel arrived, want closed", got.dialog)
	}
	if len(got.pluginDialogs) != 0 {
		t.Errorf("pluginDialogs = %d, want the pane's dialog dropped", len(got.pluginDialogs))
	}
	leaf := got.projectByID("proj-source").tabs[0].Root.FindLeaf("p1")
	if leaf == nil || leaf.Pane == nil || leaf.Pane.FixLabel != "Log in" {
		t.Fatalf("p1.FixLabel not adopted from the broadcast: %+v", leaf)
	}
	if out := stripANSI(leaf.Pane.View()); !strings.Contains(out, "running fix: Log in (Ctrl+C stops)") {
		t.Errorf("pane border does not show the running fix and how to stop it:\n%s", out)
	}
	// A pane too narrow for the hint keeps the marker and loses the hint; one
	// too narrow for the label still says a fix runs.
	if got := fixBorderLabel("Log in", 80, false); got != "running fix: Log in (Ctrl+C stops)" {
		t.Errorf("fixBorderLabel(80) = %q", got)
	}
	if got := fixBorderLabel("Log in", 30, false); got != "running fix: Log in" {
		t.Errorf("fixBorderLabel(30) = %q, want the hint dropped first", got)
	}
	if got := fixBorderLabel("Log in", 20, false); !strings.HasPrefix(got, "running fix") || len([]rune(got)) > 14 {
		t.Errorf("fixBorderLabel(20) = %q, want the marker cut to the pane", got)
	}

	// The next broadcast without the label clears the border.
	got = pluginUpdate(t, got, pluginDialogWorkspace(""))
	leaf = got.projectByID("proj-source").tabs[0].Root.FindLeaf("p1")
	if leaf == nil || leaf.Pane == nil || leaf.Pane.FixLabel != "" {
		t.Errorf("FixLabel kept after the fix ended: %+v", leaf)
	}
}

// A held dialog for a pane that stopped running a fix stays held; one for a
// pane the broadcast no longer lists is dropped.
func TestPluginDialog_HeldDialogDroppedWithItsPane(t *testing.T) {
	t.Parallel()
	m, _, _ := newPluginDialogTestModel(t)
	got := pluginUpdate(t, *m, pluginErrorFrame("p3"))
	ws := pluginDialogWorkspace("")
	ws.Tabs[1].Panes = nil
	ws.Panes = append(ws.Panes[:2], ws.Panes[3:]...)
	got = pluginUpdate(t, got, ws)
	if got.pluginDialogs["p3"] != nil {
		t.Error("held dialog survived its pane's destruction")
	}
}

// A frame for a pane this Model cannot place, on a destination it has no
// connection for, is dropped: nothing will ever place it.
func TestPluginDialog_UnknownPaneOnUnknownDestDropped(t *testing.T) {
	t.Parallel()
	m, _, _ := newPluginDialogTestModel(t)
	frame := pluginErrorFrame("ghost")
	frame.Dest = "user@nowhere"
	got := pluginUpdate(t, *m, frame)
	if got.dialog != dialogNone || len(got.pluginDialogs) != 0 {
		t.Errorf("dialog=%v held=%d, want nothing filed", got.dialog, len(got.pluginDialogs))
	}
}

// Disconnecting a host takes its dialogs, held or open, with it.
func TestPluginDialog_DroppedWithDisconnectedDest(t *testing.T) {
	t.Parallel()
	m, _, _ := newPluginDialogTestModel(t)
	remotePaneOnScreen(t, m)
	m.client = NewRouter(map[string]Client{"": newFakeConn(), "user@TEST-host": newFakeConn()})
	frame := pluginErrorFrame("rp")
	frame.Dest = "user@TEST-host"
	got := pluginUpdate(t, *m, frame)
	if got.dialog != dialogPluginError || got.pluginDialogOpen != "rp" {
		t.Fatalf("setup: dialog=%v open=%q", got.dialog, got.pluginDialogOpen)
	}

	if !got.disconnectDest("user@TEST-host") {
		t.Fatal("setup: disconnectDest refused the remote host")
	}
	if got.dialog != dialogNone || len(got.pluginDialogs) != 0 {
		t.Errorf("dialog=%v held=%d after the host left, want none", got.dialog, len(got.pluginDialogs))
	}
}

func TestPluginDialog_UpdateDisabledWithoutAdmin(t *testing.T) {
	t.Parallel()
	m, fake, clk := newPluginDialogTestModel(t)
	m.destRights = map[string]string{"": ipc.RightsStandard}
	got := pluginUpdate(t, *m, pluginUpdateFrame("p1"))
	clk.advance(pluginLetterGrace)

	got = pluginUpdate(t, got, letterKey('u'))
	if d := got.pluginDialogs["p1"]; d == nil || d.confirming {
		t.Error("u armed the Replace step on a standard token")
	}
	// Two Lefts move from Close over Not now onto Update; Enter there must do
	// nothing too.
	got = pluginUpdate(t, got, tea.KeyPressMsg{Code: tea.KeyLeft})
	got = pluginUpdate(t, got, tea.KeyPressMsg{Code: tea.KeyLeft})
	if d := got.pluginDialogs["p1"]; d.buttons()[d.focus].kind != pluginButtonUpdate {
		t.Fatalf("setup: focus is not on Update (focus=%d)", d.focus)
	}
	got = pluginUpdate(t, got, tea.KeyPressMsg{Code: tea.KeyEnter})
	if d := got.pluginDialogs["p1"]; d == nil || d.confirming || got.dialog != dialogPluginError {
		t.Error("Enter on the disabled Update button armed the Replace step or closed the dialog")
	}
	if fake.sentCount() != 0 {
		t.Errorf("sent %d message(s) without admin rights, want 0", fake.sentCount())
	}
	if out := stripANSI(got.renderPluginErrorDialog()); !strings.Contains(out, updateDisabledReason) {
		t.Errorf("render does not say why Update is disabled:\n%s", out)
	}

	// Not now still works for that user, or the dialog comes back every start.
	got = pluginUpdate(t, got, letterKey('n'))
	_, p := fixReqSent(t, fake)
	if p.Fix != ipc.PluginFixNotNow {
		t.Errorf("fix = %q, want not_now", p.Fix)
	}
}

// A read-only connection keeps only Close: fixes and Not now are act-class.
func TestPluginDialog_ReadOnlyGreysFixesAndNotNow(t *testing.T) {
	t.Parallel()
	m, fake, clk := newPluginDialogTestModel(t)
	m.destRights = map[string]string{"": ipc.RightsReadOnly}
	got := pluginUpdate(t, *m, pluginErrorFrame("p1"))
	got = pluginUpdate(t, got, pluginUpdateFrame("p1"))
	clk.advance(pluginLetterGrace)

	got = pluginUpdate(t, got, letterKey('l'))
	got = pluginUpdate(t, got, letterKey('n'))
	got = pluginUpdate(t, got, letterKey('u'))
	if fake.sentCount() != 0 {
		t.Errorf("sent %d message(s) on a read-only connection, want 0", fake.sentCount())
	}
	if d := got.pluginDialogs["p1"]; d == nil || d.confirming {
		t.Error("u armed the Replace step on a read-only token")
	}
	if out := stripANSI(got.renderPluginErrorDialog()); !strings.Contains(out, actDisabledReason) {
		t.Errorf("render does not say why the fixes are disabled:\n%s", out)
	}
	got = pluginUpdate(t, got, tea.KeyPressMsg{Code: tea.KeyEscape})
	if got.dialog != dialogNone {
		t.Errorf("Esc did not close: dialog=%v", got.dialog)
	}
}

func TestPluginDialog_SanitizesLabels(t *testing.T) {
	t.Parallel()
	m, fake, clk := newPluginDialogTestModel(t)
	rlo := string(rune(0x202e)) // RIGHT-TO-LEFT OVERRIDE: printable, reverses the row
	frame := pluginErrorFrame("p1")
	frame.Title = "Stripe" + rlo + " login expired"
	frame.Message = "Log in" + rlo + " again."
	frame.Fixes[0].Label = "L" + rlo + "og in"
	got := pluginUpdate(t, *m, frame)
	clk.advance(pluginLetterGrace)

	out := got.renderPluginErrorDialog()
	if strings.Contains(out, rlo) {
		t.Errorf("render carries U+202E from the daemon's text:\n%s", stripANSI(out))
	}
	if plain := stripANSI(out); !strings.Contains(plain, "[L]og in") {
		t.Errorf("fix button not rendered as [L]og in:\n%s", plain)
	}

	// The letter is chosen on the sanitized label, and the NAME goes back raw.
	got = pluginUpdate(t, got, letterKey('l'))
	_, p := fixReqSent(t, fake)
	if p.Fix != "login" {
		t.Errorf("fix = %q, want login", p.Fix)
	}
}

// Two fixes whose labels start alike take distinct letters, and a label with
// no free letter gets none rather than a wrong one.
func TestPluginDialog_FixLettersAreUnique(t *testing.T) {
	t.Parallel()
	d := &pluginDialog{hasError: true, fixes: []ipc.PluginFixInfo{
		{Name: "login", Label: "Log in"},
		{Name: "logout", Label: "Log out"},
		{Name: "cu", Label: "cu"}, // c and u are reserved
	}}
	var keys []rune
	for _, b := range d.buttons() {
		keys = append(keys, b.key)
	}
	if want := []rune{'l', 'o', 0, 'c'}; string(keys) != string(want) {
		t.Errorf("keys = %q, want %q", string(keys), string(want))
	}
	if got := bracketLetter("Log out", 'o'); got != "L[o]g out" {
		t.Errorf("bracketLetter = %q", got)
	}
}

func TestPluginDialog_OldPayloadTextOnly(t *testing.T) {
	t.Parallel()
	m, fake, _ := newPluginDialogTestModel(t)
	// An older daemon: title and message, no fixes, no update.
	got := pluginUpdate(t, *m, PluginErrorMsg{PaneID: "p1", Title: "boom", Message: "it broke"})
	if got.dialog != dialogPluginError {
		t.Fatalf("dialog = %v, want the plugin dialog", got.dialog)
	}
	out := stripANSI(got.renderPluginErrorDialog())
	if !strings.Contains(out, "boom") || !strings.Contains(out, "it broke") || !strings.Contains(out, "[C]lose") {
		t.Errorf("text-only dialog render:\n%s", out)
	}

	got = pluginUpdate(t, got, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got.dialog != dialogNone {
		t.Errorf("Enter did not close: dialog=%v", got.dialog)
	}
	got = pluginUpdate(t, got, PluginErrorMsg{PaneID: "p1", Title: "boom", Message: "it broke"})
	got = pluginUpdate(t, got, tea.KeyPressMsg{Code: tea.KeyEscape})
	if got.dialog != dialogNone {
		t.Errorf("Esc did not close: dialog=%v", got.dialog)
	}
	if fake.sentCount() != 0 {
		t.Errorf("a text-only dialog sent %d message(s)", fake.sentCount())
	}
}

// A dialog for a pane on another daemon answers that daemon.
func TestPluginDialog_RepliesToTheFramesDest(t *testing.T) {
	t.Parallel()
	m, fake, clk := newPluginDialogTestModel(t)
	remotePaneOnScreen(t, m)
	frame := pluginErrorFrame("rp")
	frame.Dest = "user@TEST-host"
	got := pluginUpdate(t, *m, frame)
	if got.dialog != dialogPluginError {
		t.Fatalf("dialog = %v, want open for the remote pane on screen", got.dialog)
	}
	clk.advance(pluginLetterGrace)
	pluginUpdate(t, got, letterKey('l'))
	msg, _ := fixReqSent(t, fake)
	if msg.Origin != "user@TEST-host" {
		t.Errorf("request stamped for %q, want the frame's dest user@TEST-host", msg.Origin)
	}
}

// A destination the router has no conn for cannot be sent to: the dialog
// stays, says so, and nothing is reported as done.
func TestPluginDialog_UnreachableDestKeepsDialog(t *testing.T) {
	t.Parallel()
	m, _, clk := newPluginDialogTestModel(t)
	remotePaneOnScreen(t, m)
	local := newFakeConn()
	m.client = NewRouter(map[string]Client{"": local})
	frame := pluginErrorFrame("rp")
	frame.Dest = "user@TEST-host"
	got := pluginUpdate(t, *m, frame)
	if got.dialog != dialogPluginError {
		t.Fatalf("setup: dialog=%v", got.dialog)
	}
	clk.advance(pluginLetterGrace)

	got = pluginUpdate(t, got, letterKey('l'))

	if local.sentCount() != 0 {
		t.Errorf("the local conn received %d message(s) meant for the unreachable host", local.sentCount())
	}
	if got.dialog != dialogPluginError {
		t.Errorf("dialog = %v, want still open — the fix never left", got.dialog)
	}
	if d := got.pluginDialogs["rp"]; d == nil {
		t.Error("the dialog was dropped although its request never left")
	} else if !strings.Contains(d.notice, "could not send") {
		t.Errorf("dialog notice = %q, want the send failure", d.notice)
	}
	if !got.flashErr {
		t.Error("no error flash for the failed send")
	}
}

func TestPluginDialog_RefusalLandsInTheStatusBar(t *testing.T) {
	t.Parallel()
	m, _, _ := newPluginDialogTestModel(t)
	got := pluginUpdate(t, *m, pluginFixRespMsg{Resp: ipc.PluginFixRespPayload{Error: "stripe has no fix named \"login\""}})
	if !got.flashErr || !strings.Contains(got.flashText, "no fix named") {
		t.Errorf("flash = %q (err=%v), want the daemon's reason as an error flash", got.flashText, got.flashErr)
	}
	got = pluginUpdate(t, *m, pluginFixRespMsg{Resp: ipc.PluginFixRespPayload{OK: true}})
	if got.flashText != "" {
		t.Errorf("an OK answer flashed %q", got.flashText)
	}
}
