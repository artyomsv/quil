package daemon

import (
	"testing"
	"time"
)

// The watchdog used to measure staleness as
// time.Since(time.Unix(0, lastSnapshotDone)), where lastSnapshotDone was a
// wall-clock UnixNano. Wall clock advances across a suspend; the tickers that
// would have refreshed the value do not. So a laptop closed for ten minutes
// produced "no snapshot completed for 10m9s — daemon may be wedged" plus a
// full goroutine dump, on a daemon that was never wedged (#221, macOS).
//
// These tests pin the ENCODING, because that is what the defect was. No
// test can suspend the machine, but a wall-clock instant and a monotonic
// duration are trivially distinguishable by magnitude and by what they do
// when startedAt is moved.

func TestSnapshotStale_UnarmedBeforeFirstSnapshot(t *testing.T) {
	d := &Daemon{startedAt: time.Now()}

	if _, armed := d.snapshotStale(); armed {
		t.Fatal("canary reported armed before any snapshot completed; the zero " +
			"value of lastSnapshotDone must mean \"never\", or the watchdog " +
			"measures staleness against the epoch and dumps on the first tick")
	}
}

func TestSnapshotStale_FreshSnapshotIsNotStale(t *testing.T) {
	d := &Daemon{startedAt: time.Now()}
	d.markSnapshotDone()

	stale, armed := d.snapshotStale()
	if !armed {
		t.Fatal("canary not armed after markSnapshotDone")
	}
	if stale < 0 {
		t.Fatalf("staleness went backwards: %v", stale)
	}
	if stale > time.Second {
		t.Fatalf("a snapshot recorded microseconds ago reads as %v stale — the "+
			"stored value and the measurement are not on the same clock", stale)
	}
}

func TestSnapshotStale_StoresAMonotonicDurationNotAWallClockInstant(t *testing.T) {
	d := &Daemon{startedAt: time.Now()}
	d.markSnapshotDone()

	got := d.lastSnapshotDone.Load()

	// A wall-clock UnixNano is ~1.7e18 and climbing. A duration since a
	// startedAt set moments ago is microseconds. Anything above an hour here
	// is an absolute timestamp, whatever it claims to be.
	if got > int64(time.Hour) {
		t.Fatalf("lastSnapshotDone = %d, which is an absolute timestamp, not a "+
			"duration since startedAt; suspend time will be counted as staleness", got)
	}
	if got <= 0 {
		t.Fatalf("lastSnapshotDone = %d; must be positive so that 0 keeps "+
			"meaning \"no snapshot yet\"", got)
	}
}

// markSnapshotDone stores a duration measured from d.startedAt, so the two
// must agree on the origin. Backdating startedAt is the cheapest way to prove
// the subtraction uses it on both sides: a wall-clock implementation ignores
// startedAt entirely and reports ~0 here.
func TestSnapshotStale_MeasuresFromStartedAt(t *testing.T) {
	d := &Daemon{startedAt: time.Now()}
	d.markSnapshotDone()

	d.startedAt = d.startedAt.Add(-time.Hour)

	stale, armed := d.snapshotStale()
	if !armed {
		t.Fatal("canary not armed after markSnapshotDone")
	}
	if stale < 59*time.Minute {
		t.Fatalf("moving startedAt back an hour changed staleness by %v; the "+
			"watchdog is not measuring against startedAt", stale)
	}
}
