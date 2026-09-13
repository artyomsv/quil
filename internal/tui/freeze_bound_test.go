package tui

import (
	"errors"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
)

// ageOutage backdates a destination's outage so the freeze window has expired
// without the test waiting for it. The field is what freezesInput reads, so
// setting it is the same lever real time pulls.
func ageOutage(t *testing.T, m *Model, dest string, age time.Duration) {
	t.Helper()
	ls := m.linkFor(dest)
	if ls.downAt.IsZero() {
		t.Fatalf("link %q has no downAt — handleLinkLost must stamp it, or the bound can never expire", dest)
	}
	ls.downAt = time.Now().Add(-age)
}

// TestFreezeInput_LongOutageReleasesTheKeyboard is the regression test for the
// 2026-09-10 lock-out.
//
// A remote host was powered off at 23:26. ssh answered "Connection timed out",
// which ClassifyLinkFailure correctly treats as TRANSIENT — so the ladder never
// parked, `active` stayed true, and freezeInput swallowed every key except quit
// for 7.5 hours while the user sat on that host's project. The daemon log shows
// reconnect attempt 120. The only way out was Ctrl+Q.
//
// The freeze is right for a blip and wrong for a switched-off machine, and
// nothing in the old code could tell those apart, because the classifier is
// correct: a timeout IS transient. The distinction that works is duration.
func TestFreezeInput_LongOutageReleasesTheKeyboard(t *testing.T) {
	m := Model{
		projects:      []*ProjectModel{{ID: "proj-gpu", Dest: "gpu01"}},
		activeProject: 0,
	}
	m.handleLinkLost("gpu01", errors.New("ssh: connect to host 192.168.2.60 port 22: Connection timed out"))

	key := tea.KeyPressMsg{Code: 'a', Text: "a"}
	if _, frozen := m.freezeInput(key); !frozen {
		t.Fatal("a fresh drop must still freeze — the blip case is what the freeze is for")
	}

	ageOutage(t, &m, "gpu01", reconnectFreezeWindow+time.Second)

	if _, frozen := m.freezeInput(key); frozen {
		t.Fatal("a host that has been unreachable past reconnectFreezeWindow must not hold the keyboard — " +
			"the ladder keeps climbing, but the user gets their client back")
	}
}

// The ladder must keep running after the freeze lifts. Releasing input is a UI
// decision; it must not be mistaken for giving up on the host, or a machine
// switched back on would never be reattached.
func TestFreezeInput_ReleasingTheKeyboardDoesNotStopTheLadder(t *testing.T) {
	m := Model{
		projects:      []*ProjectModel{{ID: "proj-gpu", Dest: "gpu01"}},
		activeProject: 0,
	}
	m.handleLinkLost("gpu01", errors.New("connection timed out"))
	ageOutage(t, &m, "gpu01", reconnectFreezeWindow*10)

	if _, frozen := m.freezeInput(tea.KeyPressMsg{Code: 'a', Text: "a"}); frozen {
		t.Fatal("precondition: the keyboard should be free by now")
	}
	if !m.linkOf("gpu01").active {
		t.Error("the reconnect ladder must still be active — the host may come back, and nothing else would reattach it")
	}
	// Guards against the fix being implemented by parking the link instead,
	// which stops retrying and then needs a manual resume key.
	if m.linkOf("gpu01").parked {
		t.Error("the link must NOT be parked — parking stops retrying, and a powered-off host that comes back should reattach itself")
	}
}

// A PARKED link freezes nothing, whatever its downAt says.
//
// Parked means the ladder has stopped: a rejected key, a changed host key, an
// algorithm mismatch. There is no imminent reconnect for a keystroke to be
// confused about, so the modal state bought nothing and cost the user every key
// but the resume one — on a banner reached by a wrong `IdentityFile`, i.e. a
// configuration mistake rather than an outage.
func TestFreezeInput_ParkedLinkIsNotFrozen(t *testing.T) {
	m := Model{
		projects:      []*ProjectModel{{ID: "proj-gpu", Dest: "gpu01"}},
		activeProject: 0,
	}
	m.handleLinkLost("gpu01", errors.New("Permission denied (publickey)"))
	m.linkFor("gpu01").parked = true

	if _, frozen := m.freezeInput(tea.KeyPressMsg{Code: 'a', Text: "a"}); frozen {
		t.Fatal("a parked link must not freeze input — nothing is retrying, so there is no late delivery to protect against")
	}
}

// A delayed clipboard paste is gated by its own pane's destination, and the
// bound has to reach that arm too — it is the arm that carries a real payload
// to a real PTY, so an unbounded freeze there is the one that silently eats a
// paste the user will assume arrived.
func TestFreezeInput_LongOutageReleasesTheDelayedPasteToo(t *testing.T) {
	m := Model{
		projects:      []*ProjectModel{{ID: "proj-gpu", Dest: "gpu01"}},
		activeProject: 0,
	}
	m.handleLinkLost("gpu01", errors.New("connection timed out"))

	msg := clipboardPastedMsg{text: "SECRET", paneID: "p1"}
	if _, frozen := m.freezeInput(msg); !frozen {
		t.Fatal("precondition: a fresh drop must freeze the delayed paste")
	}

	ageOutage(t, &m, "gpu01", reconnectFreezeWindow+time.Second)

	if _, frozen := m.freezeInput(msg); frozen {
		t.Error("the paste arm must respect the same bound as the key arm — one freeze with two lifetimes is two freezes")
	}
}

// escapeModel builds a two-project client: the active one lives on a
// destination that can drop, the second is local.
//
// Fuller than the fixtures above because these tests drive Update rather than
// freezeInput, and Update's dispatcher needs a keymap, a config, a pane to call
// active and a client to talk to. That is the point — the choke point is only
// meaningful in the machine it chokes.
func escapeModel(t *testing.T) Model {
	t.Helper()
	m := Model{
		client:        newFakeConn(),
		cfg:           config.Default(),
		width:         100,
		height:        30,
		notifications: NewNotificationCenter(30, 50),
		mcpHighlights: make(map[string]bool),
		projects: []*ProjectModel{
			{ID: "proj-gpu", Dest: "gpu01", tabs: []*TabModel{tabWith(&PaneModel{ID: "pane-gpu"})}},
			{ID: "proj-local", Dest: "", tabs: []*TabModel{tabWith(&PaneModel{ID: "pane-local"})}},
		},
		activeProject: 0,
	}
	m.initKeymap()
	return m
}

// projectNextKey is the default binding for "project.next" (alt+shift+right).
//
// It is the probe key for the two tests below because it is the exact key the
// trapped user needed: the one that walks off the dead host's project onto a
// healthy one. Asserting on it means asserting on the escape itself rather than
// on a proxy for it.
func projectNextKey() tea.KeyPressMsg {
	return tea.KeyPressMsg{Mod: tea.ModAlt | tea.ModShift, Code: tea.KeyRight}
}

// TestUpdate_LongOutageLetsTheUserLeaveTheDeadProject drives the bound through
// Update, and that is deliberate rather than thorough.
//
// freezeInput is called from ONE place, unconditionally, ahead of Update's type
// switch. A test that only calls the method proves the decision is right and
// says nothing about whether the choke point still consults it — and the choke
// point is exactly where this class of bug lives: the gate once sat behind an
// active-destination check at the call site, which made it unreachable whenever
// the active project was healthy and silently disabled the per-destination
// paste gate with it.
func TestUpdate_LongOutageLetsTheUserLeaveTheDeadProject(t *testing.T) {
	m := escapeModel(t)
	m.handleLinkLost("gpu01", errors.New("ssh: connect to host 192.168.2.60 port 22: Connection timed out"))
	ageOutage(t, &m, "gpu01", reconnectFreezeWindow+time.Second)

	updated, _ := m.Update(projectNextKey())
	got, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", updated)
	}
	if got.activeProject != 1 {
		t.Error("the switch-away key did not reach the dispatcher — the user is still on the powered-off host's " +
			"project with no key but quit, which is the entire complaint this fix exists for")
	}
}

// The control: the SAME key on the SAME fixture must be swallowed while the
// outage is fresh. Without it, a fix that deleted the freeze outright would
// satisfy every other assertion in this file.
func TestUpdate_FreshOutageStillSwallowsTheSameKey(t *testing.T) {
	m := escapeModel(t)
	m.handleLinkLost("gpu01", errors.New("connection timed out"))

	updated, _ := m.Update(projectNextKey())
	got, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", updated)
	}
	if got.activeProject != 0 {
		t.Error("a key must still be swallowed inside the freeze window — deleting the freeze is not the fix")
	}
}

// handleLinkLost must stamp downAt, or freezesInput's zero-value branch keeps
// the old unbounded behaviour and every test above passes only because it sets
// the field by hand.
func TestHandleLinkLost_StampsTheOutageStart(t *testing.T) {
	m := Model{
		projects:      []*ProjectModel{{ID: "proj-gpu", Dest: "gpu01"}},
		activeProject: 0,
	}
	before := time.Now()
	m.handleLinkLost("gpu01", errors.New("connection reset"))

	got := m.linkOf("gpu01").downAt
	if got.IsZero() {
		t.Fatal("downAt not stamped — the freeze bound can never expire")
	}
	if got.Before(before.Add(-time.Second)) || got.After(time.Now().Add(time.Second)) {
		t.Errorf("downAt = %v, want ~now", got)
	}
}

// A reconnect must clear the outage stamp, so a LATER drop is measured from
// itself rather than from an outage that already ended. Carrying it forward
// would leave a link that dropped, healed and dropped again unfrozen from the
// first instant — exactly the blip the freeze is for.
func TestFinishReconnect_ClearsTheOutageStamp(t *testing.T) {
	m := Model{
		projects:      []*ProjectModel{{ID: "proj-gpu", Dest: "gpu01"}},
		activeProject: 0,
		links:         map[string]*reconnectState{},
	}
	m.handleLinkLost("gpu01", errors.New("connection reset"))
	ageOutage(t, &m, "gpu01", reconnectFreezeWindow*2)

	m.finishReconnect("gpu01", &fakeSender{})

	if !m.linkOf("gpu01").downAt.IsZero() {
		t.Fatal("finishReconnect left the previous outage's downAt behind — the next drop would start unfrozen")
	}
}

// TestEnqueueInput_OfflineDestDropsPTYBytesInsteadOfQueueingThem is the
// regression test for the review finding that bounding the freeze reopened the
// exact hazard the freeze existed for.
//
// Releasing the freeze after reconnectFreezeWindow releases PTY-bound bytes
// along with navigation, and there is a QUEUE in between: enqueueInput hands the
// entry to inputForwarder, which resolves the connection later, and Router.Send
// looks up r.conns[dest] at SEND time. So an entry still in the channel when
// finishReconnect swaps the conn in is delivered to the REPLACEMENT — bytes
// typed at an offline host landing in the live agent session that came back,
// at a prompt that moved on.
//
// Dropping at enqueue is what closes it, and it is the only place that can:
// freezeInput matches on message TYPE and cannot tell a key the client consumes
// from one headed at a PTY, while everything reaching enqueueInput is bytes for
// a child process by construction.
func TestEnqueueInput_OfflineDestDropsPTYBytesInsteadOfQueueingThem(t *testing.T) {
	m := escapeModel(t)
	m.inputCh = make(chan paneInput, inputForwardBuffer)
	m.handleLinkLost("gpu01", errors.New("connection timed out"))
	ageOutage(t, &m, "gpu01", reconnectFreezeWindow+time.Second)

	// Past the window, so the key is no longer frozen and reaches the PTY path.
	m.enqueueInput("pane-gpu", []byte("rm -rf /\r"))

	if n := len(m.inputCh); n != 0 {
		got := <-m.inputCh
		t.Fatalf("queued %d entry for an offline destination (%q) — the forwarder resolves the conn "+
			"later, so finishReconnect can hand these bytes to the recovered pane", n, got.data)
	}
}

// The control: the same call on a HEALTHY destination must still queue, or the
// drop would have broken typing everywhere rather than fixing anything.
func TestEnqueueInput_HealthyDestStillQueues(t *testing.T) {
	m := escapeModel(t)
	m.inputCh = make(chan paneInput, inputForwardBuffer)

	m.enqueueInput("pane-gpu", []byte("echo hi\r"))

	if len(m.inputCh) != 1 {
		t.Fatal("a healthy destination must still queue PTY input")
	}
}

// A background destination dropping must not stop typing into a pane on a
// DIFFERENT daemon. The drop is scoped by the PANE's destination, not by the
// active project, which is the same scoping freezeInput's paste arm uses.
func TestEnqueueInput_BackgroundOutageDoesNotBlockAnotherDaemonsPane(t *testing.T) {
	m := escapeModel(t)
	m.inputCh = make(chan paneInput, inputForwardBuffer)
	m.handleLinkLost("gpu01", errors.New("connection timed out"))

	// pane-local lives on the local daemon, which never dropped.
	m.enqueueInput("pane-local", []byte("echo hi\r"))

	if len(m.inputCh) != 1 {
		t.Fatal("one daemon's outage must not drop input bound for another daemon's pane")
	}
}

// A PARKED link drops PTY bytes too. freezesInput exempts parked so the client
// stays usable, but parked means the ladder has STOPPED with the link still
// down — there is no conn to carry these bytes and, if the operator resumes,
// the entry would cross the reconnect exactly as an unparked one would.
func TestEnqueueInput_ParkedDestAlsoDropsPTYBytes(t *testing.T) {
	m := escapeModel(t)
	m.inputCh = make(chan paneInput, inputForwardBuffer)
	m.handleLinkLost("gpu01", errors.New("Permission denied (publickey)"))
	m.linkFor("gpu01").parked = true

	if _, frozen := m.freezeInput(tea.KeyPressMsg{Code: 'a', Text: "a"}); frozen {
		t.Fatal("precondition: a parked link must not freeze the client")
	}
	m.enqueueInput("pane-gpu", []byte("a"))

	if len(m.inputCh) != 0 {
		t.Error("a parked destination must still drop PTY bytes — the link is down either way")
	}
}
