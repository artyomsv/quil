package daemon

import (
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/memreport"
	"github.com/artyomsv/quil/internal/proctree"
)

var killBase = time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)

// killTree builds: 100 (pane's own shell, depth 1) -> 200 (depth 2) -> 300.
func killTree() *proctree.Node {
	table := []proctree.ProcessEntry{
		{PID: 100, PPID: 1, Name: "zsh", Start: killBase},
		{PID: 200, PPID: 100, Name: "node", Start: killBase.Add(10 * time.Second)},
		{PID: 300, PPID: 200, Name: "esbuild", Start: killBase.Add(20 * time.Second)},
	}
	return proctree.Build(table, []int{100})[100]
}

func TestValidateKillTarget(t *testing.T) {
	root := killTree()
	startOf := func(pid int) int64 {
		return proctree.Find(root, pid).Start.UnixMilli()
	}

	for _, tc := range []struct {
		name    string
		req     ipc.KillProcessReqPayload
		wantErr string
		why     string
	}{
		{
			name: "valid descendant",
			req:  ipc.KillProcessReqPayload{PID: 200, StartMS: startOf(200)},
		},
		{
			name: "deeper descendant is also valid",
			req:  ipc.KillProcessReqPayload{PID: 300, StartMS: startOf(300)},
		},
		{
			name:    "pane's own child is not killable",
			req:     ipc.KillProcessReqPayload{PID: 100, StartMS: startOf(100)},
			wantErr: refuseNotChild,
			why:     "depth 1 is the pane's shell or agent; that is restart-pane, not this",
		},
		{
			name:    "process not in this pane's tree",
			req:     ipc.KillProcessReqPayload{PID: 999, StartMS: killBase.UnixMilli()},
			wantErr: refuseNotFound,
			why:     "a client naming an arbitrary PID must not reach the signaller",
		},
		{
			name:    "start time does not match",
			req:     ipc.KillProcessReqPayload{PID: 200, StartMS: startOf(200) + 60_000},
			wantErr: refuseChanged,
			why:     "the PID was recycled between the snapshot and the confirm",
		},
		{
			name:    "client sent no start time",
			req:     ipc.KillProcessReqPayload{PID: 200},
			wantErr: refuseUnknownTime,
			why:     "identity cannot be confirmed, so the kill must not proceed",
		},
		{
			name:    "within tolerance still passes",
			req:     ipc.KillProcessReqPayload{PID: 200, StartMS: startOf(200) + 500},
			wantErr: "",
			why:     "both sides derive this from the same enumeration",
		},
		{
			name:    "just outside tolerance is refused",
			req:     ipc.KillProcessReqPayload{PID: 200, StartMS: startOf(200) + killStartTolerance + 1},
			wantErr: refuseChanged,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, refused := validateKillTarget(root, tc.req)

			if refused != tc.wantErr {
				t.Fatalf("refused = %q, want %q (%s)", refused, tc.wantErr, tc.why)
			}
			if tc.wantErr == "" {
				if got == nil || got.PID != tc.req.PID {
					t.Errorf("accepted but returned %+v, want node %d", got, tc.req.PID)
				}
				return
			}
			// The property that matters on every refusal: nothing to signal.
			if got != nil {
				t.Errorf("refused with %q but still returned a target (%d) — the "+
					"caller would signal it", refused, got.PID)
			}
		})
	}
}

// A target whose own start time the platform could not read is refused, even
// though the client supplied one. Windows can fail to open a handle.
func TestValidateKillTarget_UnknownDaemonSideStartIsRefused(t *testing.T) {
	table := []proctree.ProcessEntry{
		{PID: 100, PPID: 1, Name: "cmd", Start: killBase},
		{PID: 200, PPID: 100, Name: "node"}, // no start time
	}
	root := proctree.Build(table, []int{100})[100]

	got, refused := validateKillTarget(root, ipc.KillProcessReqPayload{
		PID: 200, StartMS: killBase.UnixMilli(),
	})
	if refused != refuseUnknownTime || got != nil {
		t.Errorf("got (%v, %q), want (nil, %q)", got, refused, refuseUnknownTime)
	}
}

func TestVersionIsStale(t *testing.T) {
	for _, tc := range []struct {
		name           string
		client, daemon string
		want           bool
	}{
		{"same version", "1.62.6", "1.62.6", false},
		{"older client", "1.62.4", "1.62.6", true},
		{"newer client", "1.63.0", "1.62.6", true},
		// An unknown version is not a stale one. Marking every unknown as
		// stale would put a warning next to perfectly current processes.
		{"client version unknown", "", "1.62.6", false},
		{"daemon version unknown", "1.62.6", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := versionIsStale(tc.client, tc.daemon); got != tc.want {
				t.Errorf("versionIsStale(%q, %q) = %v, want %v",
					tc.client, tc.daemon, got, tc.want)
			}
		})
	}
}

func TestHelloRegistry_ForgetOnDisconnect(t *testing.T) {
	r := newHelloRegistry()
	conn := &ipc.Conn{}

	r.put(conn, ipc.ClientHelloPayload{Role: "tui", PID: 42, Version: "1.62.6"})
	if len(r.byConn) != 1 {
		t.Fatal("hello was not recorded")
	}

	r.forget(conn)
	if len(r.byConn) != 0 {
		t.Error("a disconnected conn kept its identity; the process it described " +
			"is gone and would still be listed as running")
	}
}

func TestHelloRegistry_UptimeExtrapolatesFromDuration(t *testing.T) {
	r := newHelloRegistry()
	now := killBase
	r.nowFunc = func() time.Time { return now }

	conn := &ipc.Conn{}
	r.put(conn, ipc.ClientHelloPayload{Role: "bridge", PID: 7, Version: "1.62.6", UptimeMS: 60_000})

	// Ten seconds later the reported uptime must have grown by ten seconds.
	got, _ := r.describe([]*ipc.Conn{conn}, "1.62.6", now.Add(10*time.Second))
	if len(got) != 1 {
		t.Fatalf("described %d processes, want 1", len(got))
	}
	if got[0].UptimeMS != 70_000 {
		t.Errorf("UptimeMS = %d, want 70000 — the hello is sent once, so a frozen "+
			"value would read as ~0 forever", got[0].UptimeMS)
	}
}

// The age gate. Connections without a hello are not only old bridges: every
// short-lived probe (`quil status`, the version gate, `quild guard`, the
// daemonctl dials) holds one for a second or two and, by design, never
// identifies itself. Counting those would make the dialog state something
// false — that a client predating the feature is running — at random moments.
func TestHelloRegistry_UnidentifiedCountIgnoresYoungConns(t *testing.T) {
	now := killBase
	young, old := &ipc.Conn{}, &ipc.Conn{}
	ages := map[*ipc.Conn]time.Time{
		young: now.Add(-2 * time.Second),         // a probe, mid-flight
		old:   now.Add(-2 * procUnidentifiedAge), // a bridge that cannot say hello
	}

	r := newHelloRegistry()
	r.nowFunc = func() time.Time { return now }
	r.openedAtOf = func(c *ipc.Conn) time.Time { return ages[c] }

	_, unidentified := r.describe([]*ipc.Conn{young, old}, "1.62.6", now)

	if unidentified != 1 {
		t.Errorf("unidentified = %d, want 1 — only the connection too old to be "+
			"a probe should count", unidentified)
	}
}

func TestHelloRegistry_IdentifiedConnsAreNeverUnidentified(t *testing.T) {
	now := killBase
	conn := &ipc.Conn{}
	r := newHelloRegistry()
	r.nowFunc = func() time.Time { return now }
	r.openedAtOf = func(*ipc.Conn) time.Time { return now.Add(-time.Hour) }
	r.put(conn, ipc.ClientHelloPayload{Role: "bridge", PID: 9, Version: "1.62.4"})

	got, unidentified := r.describe([]*ipc.Conn{conn}, "1.62.6", now)

	if unidentified != 0 {
		t.Errorf("unidentified = %d, want 0", unidentified)
	}
	if len(got) != 1 || !got[0].Stale {
		t.Errorf("got %+v, want one stale bridge", got)
	}
}

// --- collector gating ---

type fakePaneSource struct{ snap memreport.PaneSourceSnapshot }

func (f fakePaneSource) Snapshot() memreport.PaneSourceSnapshot { return f.snap }

type fakePaneLister struct{ sources []memreport.PaneSource }

func (f fakePaneLister) PaneSources() []memreport.PaneSource { return f.sources }

// gateCollector builds a collector whose clock the test drives, with the
// background loop never started — the goroutine is not what is under test here,
// the deadline arithmetic is.
// gateCollector builds a collector through the production constructor, so the
// fields it seeds (the self-sampler, the unknown-CPU starting value) are the
// ones under test. A struct literal here left selfCPU nil, which made the
// nil-receiver guard in SelfSampler.Percent load-bearing for a state no
// production path produces — a test shaping the code rather than checking it.
func gateCollector(now *time.Time) *procCollector {
	c := newProcCollector(fakePaneLister{}, func([]int) map[int]uint64 { return nil })
	c.now = func() time.Time { return *now }
	return c
}

func TestProcCollector_GateHoldsWhileRenewed(t *testing.T) {
	now := killBase
	c := gateCollector(&now)

	c.deadline = now.Add(procGateWindow)

	// One tick later, well inside the window.
	now = now.Add(procTickInterval)
	if c.expired() {
		t.Fatal("gate expired one tick after a renewal")
	}

	// Renewed again by the dialog's next refresh.
	c.deadline = now.Add(procGateWindow)
	now = now.Add(procTickInterval)
	if c.expired() {
		t.Error("gate expired despite continuous renewal")
	}
}

// The property the decaying deadline exists for: a client that vanishes without
// sending anything — crashed, killed, connection dropped mid-dialog — still
// stops the collector. An explicit close message cannot do this, because the
// client that most needs to send one is exactly the client that cannot.
func TestProcCollector_GateExpiresWithoutAnyCloseMessage(t *testing.T) {
	now := killBase
	c := gateCollector(&now)
	c.running = true
	c.deadline = now.Add(procGateWindow)
	c.last = &procSnapshot{At: now}

	// Nobody renews. Nobody says goodbye.
	now = now.Add(procGateWindow + time.Second)

	if !c.expired() {
		t.Fatal("gate never expired; the collector would enumerate the process " +
			"table every five seconds forever with nobody watching")
	}
	if c.running {
		t.Error("running flag survived expiry, so a later Renew would not restart the loop")
	}
	if c.last != nil {
		t.Error("a stale snapshot survived expiry and would be served on the next open")
	}
}

// The gate must tolerate a skipped tick. 15 s against a 5 s cadence leaves two
// missed ticks of margin; one busy-CAS skip plus one dropped frame must not
// stop a dialog somebody is looking at.
func TestProcCollector_GateSurvivesOneMissedTick(t *testing.T) {
	now := killBase
	c := gateCollector(&now)
	c.deadline = now.Add(procGateWindow)

	now = now.Add(2 * procTickInterval)
	if c.expired() {
		t.Errorf("gate expired after two missed ticks; procGateWindow (%v) must "+
			"stay comfortably above procTickInterval (%v)", procGateWindow, procTickInterval)
	}
}

func TestProcCollector_GateWindowExceedsTickInterval(t *testing.T) {
	// The relationship IS the design. Pinned so a later tuning of one constant
	// cannot silently make the gate lapse between two consecutive refreshes.
	if procGateWindow <= 2*procTickInterval {
		t.Errorf("procGateWindow (%v) must exceed two tick intervals (%v)",
			procGateWindow, 2*procTickInterval)
	}
}

func TestProcCollector_CollectMapsTreesByPaneID(t *testing.T) {
	now := killBase
	c := gateCollector(&now)
	c.lister = fakePaneLister{sources: []memreport.PaneSource{
		fakePaneSource{snap: memreport.PaneSourceSnapshot{PaneID: "pane-a", Alive: true, PID: 100}},
		// Dead panes and unspawned ones have no process to walk.
		fakePaneSource{snap: memreport.PaneSourceSnapshot{PaneID: "pane-dead", Alive: false, PID: 200}},
		fakePaneSource{snap: memreport.PaneSourceSnapshot{PaneID: "pane-pending", Alive: true, PID: 0}},
	}}
	c.src = proctree.Sources{
		Table: func() ([]proctree.ProcessEntry, error) {
			return []proctree.ProcessEntry{
				{PID: 100, PPID: 1, Name: "zsh", Start: killBase},
				{PID: 101, PPID: 100, Name: "node", Start: killBase.Add(time.Second)},
				{PID: 200, PPID: 1, Name: "other", Start: killBase},
			}, nil
		},
		HasStarts: true,
		CPU:       func([]int) proctree.CPUReading { return proctree.CPUReading{} },
	}

	c.collect()

	snap := c.Latest()
	if snap == nil {
		t.Fatal("no snapshot after collect")
	}
	if _, ok := snap.Trees["pane-a"]; !ok {
		t.Error("live pane missing from the snapshot")
	}
	if _, ok := snap.Trees["pane-dead"]; ok {
		t.Error("a dead pane produced a tree")
	}
	if _, ok := snap.Trees["pane-pending"]; ok {
		t.Error("a pane with no PID produced a tree")
	}
}
