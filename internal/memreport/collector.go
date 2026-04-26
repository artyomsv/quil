package memreport

import (
	"context"
	"sort"
	"sync/atomic"
	"time"
)

// PaneSourceSnapshot is the per-pane view the collector needs each tick. It
// is intentionally a value type so the adapter can fill it under a single
// lock acquisition and hand back a torn-free copy.
type PaneSourceSnapshot struct {
	PaneID    string
	TabID     string
	Alive     bool
	PID       int
	HeapBytes uint64 // OutputBuf + GhostSnap + plugin-state key/value bytes
}

// PaneSource is the minimal view the collector needs over a daemon pane.
// Keeping this as an interface lets the daemon satisfy it externally without
// creating a package import cycle (memreport ↔ daemon). The single Snapshot
// call replaces the old seven-method interface — adapters can now grab the
// per-pane mutex once instead of six times per tick.
type PaneSource interface {
	Snapshot() PaneSourceSnapshot
}

// PaneLister is implemented by *daemon.SessionManager; the collector calls
// it each tick to enumerate current panes.
type PaneLister interface {
	PaneSources() []PaneSource
}

// Collector periodically scans a PaneLister and stores an atomic snapshot
// of per-pane memory usage.
type Collector struct {
	lister PaneLister
	every  time.Duration
	last   atomic.Pointer[Snapshot]
	busy   atomic.Bool
}

// NewCollector constructs a Collector but does not start it. Call Run in a
// goroutine.
func NewCollector(lister PaneLister, every time.Duration) *Collector {
	if every <= 0 {
		every = 5 * time.Second
	}
	return &Collector{lister: lister, every: every}
}

// Run blocks until ctx is cancelled, performing one collection up front so
// Latest() is never nil afterwards, then on every tick.
func (c *Collector) Run(ctx context.Context) {
	c.collect()
	t := time.NewTicker(c.every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.collect()
		}
	}
}

// Latest returns the most recent Snapshot, or nil if Run has not yet
// completed its first pass.
func (c *Collector) Latest() *Snapshot {
	return c.last.Load()
}

func (c *Collector) collect() {
	// If a previous tick is still running (unlikely at 5 s cadence, but
	// possible under heavy load), skip this tick.
	if !c.busy.CompareAndSwap(false, true) {
		return
	}
	defer c.busy.Store(false)

	panes := c.lister.PaneSources()
	snap := collectFrom(panes, procRSSBatch)
	c.last.Store(&snap)
}

// collectFrom is the pure core, exported for testing via the PaneSource
// abstraction. rssFn is injected so tests can stub procRSSBatch.
func collectFrom(panes []PaneSource, rssFn func([]int) map[int]uint64) Snapshot {
	// Take a single per-pane snapshot under that pane's lock; everything
	// downstream operates on the captured values.
	snaps := make([]PaneSourceSnapshot, len(panes))
	alivePIDs := make([]int, 0, len(panes))
	for i, p := range panes {
		snaps[i] = p.Snapshot()
		if snaps[i].Alive && snaps[i].PID > 0 {
			alivePIDs = append(alivePIDs, snaps[i].PID)
		}
	}

	rss := rssFn(alivePIDs)
	if rss == nil {
		rss = map[int]uint64{}
	}

	result := Snapshot{
		At:    time.Now(),
		Panes: make([]PaneMem, 0, len(snaps)),
	}

	for _, s := range snaps {
		var paneRSS uint64
		if s.Alive && s.PID > 0 {
			paneRSS = rss[s.PID]
		}

		pm := PaneMem{
			PaneID:      s.PaneID,
			TabID:       s.TabID,
			GoHeapBytes: s.HeapBytes,
			PTYRSSBytes: paneRSS,
			Total:       s.HeapBytes + paneRSS,
		}
		result.Panes = append(result.Panes, pm)
		result.Total += pm.Total
	}

	sort.Slice(result.Panes, func(i, j int) bool {
		return result.Panes[i].Total > result.Panes[j].Total
	})
	return result
}
