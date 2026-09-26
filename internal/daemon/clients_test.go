package daemon

import (
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

// fakeGraceTimer is one time.AfterFunc the registry armed. Tests fire it by
// hand after moving the fake clock, so no test waits on real time.
type fakeGraceTimer struct {
	d       time.Duration
	f       func()
	stopped bool
}

// clientsHarness drives a daemon's client registry with a fake clock and a
// fake timer seam, and counts the elections a timer expiry changed.
type clientsHarness struct {
	t       *testing.T
	d       *Daemon
	now     time.Time
	timers  []*fakeGraceTimer
	changes int
}

func newClientsHarness(t *testing.T, grace time.Duration) *clientsHarness {
	t.Helper()
	h := &clientsHarness{t: t}
	h.install(&Daemon{}, grace)
	return h
}

// install points d's registry at the harness's clock and timers. Used on a
// zero Daemon, and on one built by New for the snapshot and restore test.
//
// The seams are written under the registry's lock, because on a daemon with a
// live server the dispatch goroutines read them under that same lock.
func (h *clientsHarness) install(d *Daemon, grace time.Duration) {
	h.d = d
	h.now = time.Unix(1_800_000_000, 0)
	d.clients.mu.Lock()
	defer d.clients.mu.Unlock()
	d.clients.now = func() time.Time { return h.now }
	d.clients.afterFn = func(dur time.Duration, f func()) func() bool {
		tm := &fakeGraceTimer{d: dur, f: f}
		h.timers = append(h.timers, tm)
		return func() bool {
			was := !tm.stopped
			tm.stopped = true
			return was
		}
	}
	d.clients.grace = grace
	d.clients.onChange = func() { h.changes++ }
}

func (h *clientsHarness) advance(d time.Duration) { h.now = h.now.Add(d) }

// armed returns the live timer, or nil when none is armed.
func (h *clientsHarness) armed() *fakeGraceTimer {
	var live *fakeGraceTimer
	for _, tm := range h.timers {
		if !tm.stopped {
			live = tm
		}
	}
	return live
}

// fire runs the armed timer's callback, as time.AfterFunc would at expiry.
func (h *clientsHarness) fire() {
	h.t.Helper()
	tm := h.armed()
	if tm == nil {
		h.t.Fatal("no grace timer is armed")
	}
	tm.stopped = true
	tm.f()
}

// attach registers a new conn as the client id, at a RAW geometry.
func (h *clientsHarness) attach(id string, cols, rows int) (*ipc.Conn, bool) {
	c := new(ipc.Conn)
	changed := h.d.registerClient(c, ipc.AttachPayload{ClientID: id, Cols: cols, Rows: rows})
	return c, changed
}

// reattach is attach from a client's reconnect path: Reattach is set.
func (h *clientsHarness) reattach(id string, cols, rows int) (*ipc.Conn, bool) {
	c := new(ipc.Conn)
	changed := h.d.registerClient(c, ipc.AttachPayload{ClientID: id, Cols: cols, Rows: rows, Reattach: true})
	return c, changed
}

func (h *clientsHarness) wantMaster(want string) {
	h.t.Helper()
	if got := h.d.masterID(); got != want {
		h.t.Fatalf("masterID = %q, want %q", got, want)
	}
}

const testGrace = 3 * time.Minute

func TestElect_OldestPaintableWins(t *testing.T) {
	h := newClientsHarness(t, testGrace)
	a, changed := h.attach("A", 200, 50)
	if !changed {
		t.Fatal("the first paintable client must be elected, which is a change")
	}
	h.wantMaster("A")

	h.advance(time.Second)
	b, changed := h.attach("B", 100, 30)
	if changed {
		t.Error("a younger client attaching must not change the master")
	}
	h.wantMaster("A")
	if !h.d.isMasterConn(a) || h.d.isMasterConn(b) {
		t.Error("isMasterConn must name A's conn only")
	}
	if h.d.masterConn() != a {
		t.Error("masterConn must be A's conn")
	}
	if got := h.d.followerConns(nil); len(got) != 1 || got[0] != b {
		t.Errorf("followerConns = %v, want only B's conn", got)
	}
	if h.d.clientCount() != 2 {
		t.Errorf("clientCount = %d, want 2", h.d.clientCount())
	}
	if h.d.sizeAuthorityOpen() {
		t.Error("with a master elected, size authority is not open")
	}
}

// A console-less client attaches at 0x0 and a tiny one at 1x1. Electing
// either would resize every pane to one column: the 1x1 incident.
func TestElect_ZeroGeometryNeverElected(t *testing.T) {
	h := newClientsHarness(t, testGrace)
	if _, changed := h.attach("zero", 0, 0); changed {
		t.Error("a 0x0 attach must not change the master")
	}
	if _, changed := h.attach("one", 1, 1); changed {
		t.Error("a 1x1 attach must not change the master")
	}
	h.wantMaster("")
	if !h.d.sizeAuthorityOpen() {
		t.Error("with no master and no reservation, size authority must be open")
	}
	if h.d.masterConn() != nil {
		t.Error("masterConn must be nil with no master")
	}
}

// The threshold is the TUI's own paintable floor, inclusive.
func TestElect_ThresholdIsTUIPaintableFloor(t *testing.T) {
	h := newClientsHarness(t, testGrace)
	h.attach("narrow", daemonMinClientCols-1, daemonMinClientRows)
	h.attach("short", daemonMinClientCols, daemonMinClientRows-1)
	h.wantMaster("")
	h.attach("floor", daemonMinClientCols, daemonMinClientRows)
	h.wantMaster("floor")
}

// Review focus 1: a master whose window becomes unpaintable loses the slot at
// once, and with no eligible client there is no master at all.
func TestElect_MasterGoingUnpaintableHandsOver(t *testing.T) {
	h := newClientsHarness(t, testGrace)
	a, _ := h.attach("A", 200, 50)
	h.advance(time.Second)
	b, _ := h.attach("B", 100, 30)
	h.wantMaster("A")

	if !h.d.setClientGeometry(a, 0, 0) {
		t.Error("the master going unpaintable must change the master")
	}
	h.wantMaster("B")

	if !h.d.setClientGeometry(b, 0, 0) {
		t.Error("the last eligible client going unpaintable must change the master")
	}
	h.wantMaster("")
	if !h.d.sizeAuthorityOpen() {
		t.Error("with nobody eligible, size authority must be open")
	}
	if h.armed() != nil {
		t.Error("a geometry change is not a lost link and must arm no grace timer")
	}
}

func TestGrace_LostMasterReservedWhileFollowerAttached(t *testing.T) {
	h := newClientsHarness(t, testGrace)
	a, _ := h.attach("A", 200, 50)
	h.advance(time.Second)
	b, _ := h.attach("B", 100, 30)

	if h.d.forgetAttachedClient(a) {
		t.Error("a lost master with a follower attached keeps its slot, which is no change")
	}
	h.wantMaster("A")
	if h.d.isMasterConn(b) {
		t.Error("the follower must not become master inside the grace time")
	}
	if h.d.sizeAuthorityOpen() {
		t.Error("a reserved slot must keep size authority closed")
	}
	if h.d.masterConn() != nil {
		t.Error("masterConn must be nil while the master's link is lost")
	}
	tm := h.armed()
	if tm == nil || tm.d != testGrace {
		t.Fatalf("grace timer = %+v, want one armed for %v", tm, testGrace)
	}

	h.advance(testGrace)
	h.fire()
	h.wantMaster("B")
	if !h.d.isMasterConn(b) {
		t.Error("after the grace time the follower must be the master")
	}
	if h.changes != 1 {
		t.Errorf("expiry changes = %d, want exactly 1", h.changes)
	}
}

// The reservation protects followers. Once every follower that was attached
// at the loss has gone too, it protects nobody and the election proceeds.
func TestGrace_ReservationLapsesWhenProtectedFollowersLeave(t *testing.T) {
	h := newClientsHarness(t, testGrace)
	a, _ := h.attach("A", 200, 50)
	b, _ := h.attach("B", 100, 30)
	h.d.forgetAttachedClient(a)
	h.wantMaster("A")

	h.d.forgetAttachedClient(b)
	h.advance(time.Second)
	if _, changed := h.attach("C", 120, 40); !changed {
		t.Error("a new client with nobody left to protect must be elected")
	}
	h.wantMaster("C")
}

func TestGrace_LoneMasterNotReserved(t *testing.T) {
	h := newClientsHarness(t, testGrace)
	a, _ := h.attach("A", 200, 50)
	if !h.d.forgetAttachedClient(a) {
		t.Error("a lone master leaving must clear the master")
	}
	h.wantMaster("")
	if h.armed() != nil {
		t.Error("a lone master's loss must arm no grace timer")
	}

	h.advance(time.Second)
	c, changed := h.attach("C", 120, 40)
	if !changed || !h.d.isMasterConn(c) {
		t.Error("a relaunched TUI with a new id must be the master at once")
	}
}

func TestGrace_ReattachSameIDRestoresWithoutChange(t *testing.T) {
	h := newClientsHarness(t, testGrace)
	a, _ := h.attach("A", 200, 50)
	firstAttach := h.now
	h.advance(time.Second)
	h.attach("B", 100, 30)
	h.d.forgetAttachedClient(a)
	tm := h.armed()

	h.advance(time.Minute)
	a2, changed := h.attach("A", 200, 50)
	if changed {
		t.Error("the master returning inside grace must not be a master change")
	}
	if !h.d.isMasterConn(a2) {
		t.Error("the returning master must hold the slot on its new conn")
	}
	if !tm.stopped || h.armed() != nil {
		t.Error("the grace timer must be stopped when the master returns")
	}
	rec, ok := h.d.clientByConn(a2)
	if !ok || !rec.attachedAt.Equal(firstAttach) {
		t.Errorf("attachedAt = %v, want the first attach %v", rec.attachedAt, firstAttach)
	}
	if h.changes != 0 {
		t.Errorf("changes = %d, want 0", h.changes)
	}
}

func TestDetach_SkipsGrace(t *testing.T) {
	h := newClientsHarness(t, testGrace)
	a, _ := h.attach("A", 200, 50)
	h.advance(time.Second)
	b, _ := h.attach("B", 100, 30)

	if !h.d.detachClient(a) {
		t.Error("a detaching master must hand over at once")
	}
	if h.d.forgetAttachedClient(a) {
		t.Error("the disconnect after a detach must find no record and change nothing")
	}
	if !h.d.isMasterConn(b) {
		t.Error("after a clean exit the follower must be the master at once")
	}
	if h.armed() != nil {
		t.Error("a detach must arm no grace timer")
	}
	if h.d.clientCount() != 1 {
		t.Errorf("clientCount = %d, want 1", h.d.clientCount())
	}
}

// A daemon restart keeps the previous master's slot for min(grace, 30 s),
// written to and read back from a real workspace.json.
func TestRestartReserve_PreviousMasterReclaims(t *testing.T) {
	restartedWithMaster := func(t *testing.T) *clientsHarness {
		t.Helper()
		home := t.TempDir()
		d1 := newTestDaemonInDir(t, home)
		h1 := &clientsHarness{t: t}
		h1.install(d1, testGrace)
		h1.attach("A", 200, 50)
		h1.wantMaster("A")
		d1.snapshot()

		d2 := newTestDaemonInDir(t, home)
		h := &clientsHarness{t: t}
		h.install(d2, testGrace)
		if err := d2.restoreWorkspace(); err != nil {
			t.Fatalf("restoreWorkspace: %v", err)
		}
		h.wantMaster("A")
		if tm := h.armed(); tm == nil || tm.d != restartReserveCap {
			t.Fatalf("restart timer = %+v, want one armed for %v", tm, restartReserveCap)
		}
		return h
	}

	t.Run("previous master reattaches after another client", func(t *testing.T) {
		h := restartedWithMaster(t)
		b, changed := h.reattach("B", 100, 30)
		if changed || h.d.isMasterConn(b) {
			t.Error("a client reattaching first after a restart must not take the reserved slot")
		}
		if h.d.sizeAuthorityOpen() {
			t.Error("the restart reservation must keep size authority closed")
		}
		h.advance(2 * time.Second)
		a, changed := h.attach("A", 200, 50)
		if changed {
			t.Error("the previous master reclaiming its slot must not be a change")
		}
		if !h.d.isMasterConn(a) {
			t.Error("the previous master must be the master again")
		}
		if h.armed() != nil {
			t.Error("the restart timer must be stopped once the master is back")
		}
	})

	t.Run("previous master never comes", func(t *testing.T) {
		h := restartedWithMaster(t)
		b, _ := h.reattach("B", 100, 30)
		h.advance(restartReserveCap)
		h.fire()
		if !h.d.isMasterConn(b) {
			t.Error("after the restart reserve lapses, the oldest attached client must win")
		}
		if h.changes != 1 {
			t.Errorf("changes = %d, want 1", h.changes)
		}
	})

	// After an unclean stop (reboot, kill) the next TUI is a new process with
	// a new id. Kept waiting on the reserve, it would be a follower for 30 s
	// with nobody else attached to protect.
	t.Run("fresh single first attach takes the slot at once", func(t *testing.T) {
		h := restartedWithMaster(t)
		b, changed := h.attach("B", 100, 30)
		if !changed || !h.d.isMasterConn(b) {
			t.Error("a new process attaching alone during the restart reserve must be the master at once")
		}
		if h.armed() != nil {
			t.Error("the restart timer must be stopped once the reserve yields")
		}
		h.wantMaster("B")
	})

	// A TUI older than this branch sends no ClientID and no Reattach, so its
	// reconnect looks like a cold start. Missing fields say nothing: it must
	// not clear the reserve, even alone.
	t.Run("attach with no client id alone keeps the reserve", func(t *testing.T) {
		h := restartedWithMaster(t)
		c := new(ipc.Conn)
		if changed := h.d.registerClient(c, ipc.AttachPayload{Cols: 100, Rows: 30}); changed {
			t.Error("an attach with no ClientID must not change the master during the restart reserve")
		}
		if h.d.isMasterConn(c) {
			t.Error("an attach with no ClientID must not take the reserved slot")
		}
		if h.armed() == nil {
			t.Error("the restart timer must still be armed")
		}
		h.wantMaster("A")
	})

	t.Run("fresh first attach beside a reattached client keeps the reserve", func(t *testing.T) {
		h := restartedWithMaster(t)
		h.reattach("B", 100, 30)
		c, changed := h.attach("C", 100, 30)
		if changed || h.d.isMasterConn(c) {
			t.Error("a first attach that is not the only client must not take the reserved slot")
		}
		h.wantMaster("A")
	})
}

// Stop closes every conn, and each close runs onClientDisconnect concurrently
// with the final snapshot. Those disconnects are not lost links: treating them
// as such clears a lone master (nobody left to protect) before the snapshot
// writes size_master, and the restart reserve then has nothing to restore.
func TestRestartReserve_ShutdownDisconnectKeepsMaster(t *testing.T) {
	home := t.TempDir()
	d := newTestDaemonInDir(t, home)
	h := &clientsHarness{t: t}
	h.install(d, testGrace)
	a, _ := h.attach("A", 200, 50)

	d.shutdownOnce.Do(func() { close(d.shutdown) })
	d.onClientDisconnect(a)
	h.wantMaster("A")
	d.snapshot()

	d2 := newTestDaemonInDir(t, home)
	h2 := &clientsHarness{t: t}
	h2.install(d2, testGrace)
	if err := d2.restoreWorkspace(); err != nil {
		t.Fatalf("restoreWorkspace: %v", err)
	}
	h2.wantMaster("A")
}

// A shorter configured grace also shortens the restart reserve.
func TestRestartReserve_BoundedByGrace(t *testing.T) {
	h := newClientsHarness(t, 10*time.Second)
	h.d.clients.reserveAfterRestart("A")
	if tm := h.armed(); tm == nil || tm.d != 10*time.Second {
		t.Fatalf("restart timer = %+v, want 10s", tm)
	}

	h = newClientsHarness(t, 0)
	h.d.clients.reserveAfterRestart("A")
	h.wantMaster("")
	if h.armed() != nil || !h.d.sizeAuthorityOpen() {
		t.Error("with no grace there is no restart reserve")
	}
}

func TestTakeControl_EligibleFollowerBecomesMaster(t *testing.T) {
	h := newClientsHarness(t, testGrace)
	h.attach("A", 200, 50)
	h.advance(time.Second)
	b, _ := h.attach("B", 100, 30)

	if !h.d.takeControl(b) {
		t.Error("take_control from an eligible follower must change the master")
	}
	if !h.d.isMasterConn(b) {
		t.Error("the follower must be the master after take_control")
	}
	if h.d.takeControl(b) {
		t.Error("take_control from the master itself changes nothing")
	}
}

func TestTakeControl_IneligibleIgnored(t *testing.T) {
	h := newClientsHarness(t, testGrace)
	a, _ := h.attach("A", 200, 50)
	b, _ := h.attach("B", 0, 0)

	if h.d.takeControl(b) {
		t.Error("take_control from an unpaintable follower must be ignored")
	}
	if h.d.takeControl(new(ipc.Conn)) {
		t.Error("take_control from a conn that never attached must be ignored")
	}
	if !h.d.isMasterConn(a) {
		t.Error("the master must be unchanged")
	}
}

func TestClients_AnonymousIDAndTruncation(t *testing.T) {
	h := newClientsHarness(t, testGrace)
	c, _ := h.attach("", 200, 50)
	rec, ok := h.d.clientByConn(c)
	if !ok || len(rec.id) <= len("anon-") || rec.id[:5] != "anon-" {
		t.Fatalf("id = %q, want a daemon-minted anon-<uuid>", rec.id)
	}
	// A re-attach on the same conn with no id keeps the minted one.
	h.d.registerClient(c, ipc.AttachPayload{Cols: 200, Rows: 50})
	if again, _ := h.d.clientByConn(c); again.id != rec.id {
		t.Errorf("re-attach minted a new id %q, want %q", again.id, rec.id)
	}

	long := make([]byte, 500)
	for i := range long {
		long[i] = 'x'
	}
	c2, _ := h.attach(string(long), 200, 50)
	if rec2, _ := h.d.clientByConn(c2); len(rec2.id) != maxClientIDLen {
		t.Errorf("id length = %d, want %d", len(rec2.id), maxClientIDLen)
	}
}

func TestClients_ListAndMostRecentlyActive(t *testing.T) {
	h := newClientsHarness(t, testGrace)
	if h.d.mostRecentlyActiveConn() != nil {
		t.Error("with no client, there is no most recently active conn")
	}
	a, _ := h.attach("A", 200, 50)
	h.advance(time.Second)
	b, _ := h.attach("B", 100, 30)
	if h.d.mostRecentlyActiveConn() != b {
		t.Error("with no input yet, the most recently attached client is the most active")
	}

	h.advance(time.Second)
	h.d.touchClientInput(a)
	typedAt := h.now
	if h.d.mostRecentlyActiveConn() != a {
		t.Error("the client that typed last must be the most active")
	}
	h.d.touchClientInput(new(ipc.Conn)) // a bridge: not a client, never stamped

	list := h.d.listClients()
	if len(list) != 2 {
		t.Fatalf("listClients = %d rows, want 2", len(list))
	}
	byID := map[string]ipc.ClientInfo{}
	for _, ci := range list {
		byID[ci.Client] = ci
	}
	if ci := byID["A"]; !ci.Master || ci.Cols != 200 || ci.Rows != 50 ||
		ci.LastInputAt != typedAt.UTC().Format(time.RFC3339) || ci.AttachedAt == "" {
		t.Errorf("A = %+v", ci)
	}
	if ci := byID["B"]; ci.Master || ci.LastInputAt != "" {
		t.Errorf("B = %+v, want a follower that never typed", ci)
	}
}
