package daemon

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/proctree"
)

// A quil process reports its OWN cpu and rss, the same way it already reports
// its own role and version. Nothing here reads the OS process table: that is
// what the previous attempt at this section did, and it was wrong on both
// platforms (see QuilProcInfo's doc comment).

func helloFor(pid int) ipc.ClientHelloPayload {
	return ipc.ClientHelloPayload{
		Role: "tui", PID: pid, Version: "1.0.0", ExeName: "quil.exe", UptimeMS: 1000,
	}
}

// The load-bearing test of the whole feature. Go's zero value for the CPU field
// is 0.0, which formatCPU renders as "0%" — a confident claim that a process is
// idle. A process that has not reported yet must be UNKNOWN, which is a
// different pixel on screen and a different meaning.
func TestDescribe_WithoutStatReportsUnknownCPUNotZero(t *testing.T) {
	now := time.Unix(2000, 0)
	r := newHelloRegistry()
	r.nowFunc = func() time.Time { return now }
	conn := &ipc.Conn{}
	r.put(conn, helloFor(42))

	got, _ := r.describe([]*ipc.Conn{conn}, "1.0.0", now)
	if len(got) != 1 {
		t.Fatalf("describe returned %d rows, want 1", len(got))
	}
	if got[0].CPUPct != proctree.UnknownCPU {
		t.Errorf("CPUPct = %v, want UnknownCPU (%v) — 0 would render as \"0%%\", "+
			"claiming a process is idle when it has simply not reported",
			got[0].CPUPct, proctree.UnknownCPU)
	}
	if got[0].RSSBytes != 0 {
		t.Errorf("RSSBytes = %d, want 0", got[0].RSSBytes)
	}
}

func TestDescribe_CarriesSelfReportedStat(t *testing.T) {
	now := time.Unix(2000, 0)
	r := newHelloRegistry()
	r.nowFunc = func() time.Time { return now }
	conn := &ipc.Conn{}
	r.put(conn, helloFor(42))
	r.putStat(conn, ipc.ClientStatPayload{CPUPct: 11.5, RSSBytes: 703 << 20})

	got, _ := r.describe([]*ipc.Conn{conn}, "1.0.0", now)
	if len(got) != 1 {
		t.Fatalf("describe returned %d rows, want 1", len(got))
	}
	if got[0].CPUPct != 11.5 {
		t.Errorf("CPUPct = %v, want 11.5", got[0].CPUPct)
	}
	if got[0].RSSBytes != 703<<20 {
		t.Errorf("RSSBytes = %d, want %d", got[0].RSSBytes, uint64(703)<<20)
	}
}

// The age is measured on the DAEMON's clock, against when the stat arrived —
// never from a client-supplied timestamp. In remote mode the client is a
// different machine, and its clock is skewed by an unknown amount; uptime
// already carries a duration rather than a timestamp for the same reason.
func TestDescribe_StatAgeMeasuredOnDaemonClock(t *testing.T) {
	arrived := time.Unix(2000, 0)
	r := newHelloRegistry()
	r.nowFunc = func() time.Time { return arrived }
	conn := &ipc.Conn{}
	r.put(conn, helloFor(42))
	r.putStat(conn, ipc.ClientStatPayload{CPUPct: 5, RSSBytes: 100})

	got, _ := r.describe([]*ipc.Conn{conn}, "1.0.0", arrived.Add(3*time.Second))
	if len(got) != 1 {
		t.Fatalf("describe returned %d rows, want 1", len(got))
	}
	if got[0].StatAgeMS != 3000 {
		t.Errorf("StatAgeMS = %d, want 3000", got[0].StatAgeMS)
	}
}

func TestDescribe_StatAgeIsZeroWhenNothingReported(t *testing.T) {
	now := time.Unix(2000, 0)
	r := newHelloRegistry()
	r.nowFunc = func() time.Time { return now }
	conn := &ipc.Conn{}
	r.put(conn, helloFor(42))

	got, _ := r.describe([]*ipc.Conn{conn}, "1.0.0", now.Add(time.Hour))
	if got[0].StatAgeMS != 0 {
		t.Errorf("StatAgeMS = %d with no stat ever reported, want 0 — a nonzero "+
			"age would read as a stat that went stale rather than one that "+
			"never arrived", got[0].StatAgeMS)
	}
}

// A reconnecting process gets a fresh conn. The old conn's stat must not
// outlive it, or a dead TUI's last CPU reading is attributed to a live one.
func TestHelloRegistry_ForgetDropsStatToo(t *testing.T) {
	now := time.Unix(2000, 0)
	r := newHelloRegistry()
	r.nowFunc = func() time.Time { return now }
	conn := &ipc.Conn{}
	r.put(conn, helloFor(42))
	r.putStat(conn, ipc.ClientStatPayload{CPUPct: 99, RSSBytes: 1 << 30})

	r.forget(conn)
	r.put(conn, helloFor(42))

	got, _ := r.describe([]*ipc.Conn{conn}, "1.0.0", now)
	if got[0].CPUPct != proctree.UnknownCPU {
		t.Errorf("CPUPct = %v after forget, want UnknownCPU — the previous "+
			"connection's reading survived a disconnect", got[0].CPUPct)
	}
}

// A stat for a connection that never said hello is dropped rather than creating
// a row: a row with cpu and rss but no role, pid or version is not describable.
func TestPutStat_WithoutHelloCreatesNoRow(t *testing.T) {
	now := time.Unix(2000, 0)
	r := newHelloRegistry()
	r.nowFunc = func() time.Time { return now }
	r.openedAtOf = func(*ipc.Conn) time.Time { return now }
	conn := &ipc.Conn{}
	r.putStat(conn, ipc.ClientStatPayload{CPUPct: 11.5, RSSBytes: 1 << 20})

	got, _ := r.describe([]*ipc.Conn{conn}, "1.0.0", now)
	if len(got) != 0 {
		t.Errorf("describe returned %d rows for a conn that never said hello, want 0", len(got))
	}
}

func TestPutStat_NilConnIsIgnored(t *testing.T) {
	r := newHelloRegistry()
	r.putStat(nil, ipc.ClientStatPayload{CPUPct: 1}) // must not panic
}

// A stat is self-reported by any process that can open the socket, and it is
// RETAINED for the connection's lifetime and copied into every tree-bearing
// response — the same exposure the hello fields are truncated for.
//
// The danger here is not length but NaN and infinity: encoding/json REFUSES to
// marshal them, so one hostile (or buggy) client parks the whole response for
// every client on "Loading…" and freezes the status-bar total. That is the same
// denial the hello's truncation exists to prevent, reached through a float.
func TestSanitizeClientStat_RejectsUnmarshalableFloats(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   float64
	}{
		{"NaN", math.NaN()},
		{"positive infinity", math.Inf(1)},
		{"negative infinity", math.Inf(-1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeClientStat(ipc.ClientStatPayload{CPUPct: tc.in, RSSBytes: 5})
			if got.CPUPct != proctree.UnknownCPU {
				t.Errorf("CPUPct = %v, want UnknownCPU", got.CPUPct)
			}
			if _, err := json.Marshal(got); err != nil {
				t.Errorf("sanitized payload still will not marshal: %v", err)
			}
			if got.RSSBytes != 5 {
				t.Errorf("RSSBytes = %d, want 5 — a bad CPU must not discard a "+
					"good RSS", got.RSSBytes)
			}
		})
	}
}

func TestSanitizeClientStat_PassesRealValuesThrough(t *testing.T) {
	got := sanitizeClientStat(ipc.ClientStatPayload{CPUPct: 11.5, RSSBytes: 703 << 20})
	if got.CPUPct != 11.5 || got.RSSBytes != 703<<20 {
		t.Errorf("sanitize altered a valid payload: %+v", got)
	}
}

func TestSanitizeClientStat_KeepsNegativeUnknownMarker(t *testing.T) {
	got := sanitizeClientStat(ipc.ClientStatPayload{CPUPct: proctree.UnknownCPU})
	if got.CPUPct >= 0 {
		t.Errorf("CPUPct = %v, want the negative unknown marker preserved", got.CPUPct)
	}
}

// The daemon's own row is not a hello — it reads itself directly — so it needs
// the same "never reported yet" honesty the client rows get from describe.
func TestProcCollector_SelfStatStartsUnknown(t *testing.T) {
	c := newProcCollector(nil, func([]int) map[int]uint64 { return nil })

	pct, rss := c.SelfStat()
	if pct != proctree.UnknownCPU {
		t.Errorf("SelfStat cpu = %v before any collect, want UnknownCPU (%v) — "+
			"0 renders as \"0%%\" and claims the daemon is idle", pct, proctree.UnknownCPU)
	}
	if rss != 0 {
		t.Errorf("SelfStat rss = %d before any collect, want 0", rss)
	}
}

// Dropping the CPU history when the gate lapses is what the pane sampler
// already does: a percentage computed across a closed-dialog gap of unknown
// length is measured over an interval nobody observed.
func TestProcCollector_ExpiryResetsSelfStat(t *testing.T) {
	now := time.Unix(5000, 0)
	c := newProcCollector(nil, func([]int) map[int]uint64 { return nil })
	c.now = func() time.Time { return now }

	c.mu.Lock()
	c.selfPct, c.selfRSS = 42.0, 999
	c.deadline = now.Add(-time.Second) // already lapsed
	c.running = true
	c.mu.Unlock()

	if !c.expired() {
		t.Fatal("expired() = false for a lapsed deadline")
	}

	pct, rss := c.SelfStat()
	if pct != proctree.UnknownCPU {
		t.Errorf("SelfStat cpu = %v after expiry, want UnknownCPU — a reading "+
			"from before the dialog closed is not a reading for now", pct)
	}
	if rss != 0 {
		t.Errorf("SelfStat rss = %d after expiry, want 0", rss)
	}
}

// A client that reports a negative percentage is reporting "unknown" in the
// same convention the wire uses everywhere else; it must survive as unknown
// rather than being rendered as a negative number.
func TestDescribe_NegativeReportedCPUStaysUnknown(t *testing.T) {
	now := time.Unix(2000, 0)
	r := newHelloRegistry()
	r.nowFunc = func() time.Time { return now }
	conn := &ipc.Conn{}
	r.put(conn, helloFor(42))
	r.putStat(conn, ipc.ClientStatPayload{CPUPct: proctree.UnknownCPU, RSSBytes: 512})

	got, _ := r.describe([]*ipc.Conn{conn}, "1.0.0", now)
	if got[0].CPUPct >= 0 {
		t.Errorf("CPUPct = %v, want a negative unknown marker", got[0].CPUPct)
	}
	if got[0].RSSBytes != 512 {
		t.Errorf("RSSBytes = %d, want 512 — an unknown CPU must not discard a "+
			"known RSS from the same report", got[0].RSSBytes)
	}
}
