package memreport

import (
	"context"
	"sort"
	"sync/atomic"
	"time"

	ringbuf "github.com/artyomsv/quil/internal/ringbuf"
)

// PaneSource is the minimal view the collector needs over a daemon pane.
// Keeping this as an interface lets the daemon satisfy it externally without
// creating a package import cycle (memreport ↔ daemon).
type PaneSource interface {
	PaneID() string
	TabID() string
	OutputBuf() *ringbuf.RingBuffer
	GhostSnap() []byte
	PluginState() map[string]string
	PID() int
	Alive() bool
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
	// Gather alive PIDs for a single batched RSS query.
	alivePIDs := make([]int, 0, len(panes))
	for _, p := range panes {
		if p.Alive() && p.PID() > 0 {
			alivePIDs = append(alivePIDs, p.PID())
		}
	}
	rss := rssFn(alivePIDs)
	if rss == nil {
		rss = map[int]uint64{}
	}

	result := Snapshot{
		At:    time.Now(),
		Panes: make([]PaneMem, 0, len(panes)),
	}

	for _, p := range panes {
		heap := uint64(0)
		if buf := p.OutputBuf(); buf != nil {
			heap += uint64(buf.Len())
		}
		heap += uint64(len(p.GhostSnap()))
		for k, v := range p.PluginState() {
			heap += uint64(len(k) + len(v))
		}

		var paneRSS uint64
		if p.Alive() && p.PID() > 0 {
			paneRSS = rss[p.PID()]
		}

		pm := PaneMem{
			PaneID:      p.PaneID(),
			TabID:       p.TabID(),
			GoHeapBytes: heap,
			PTYRSSBytes: paneRSS,
			Total:       heap + paneRSS,
		}
		result.Panes = append(result.Panes, pm)
		result.Total += pm.Total
	}

	sort.Slice(result.Panes, func(i, j int) bool {
		return result.Panes[i].Total > result.Panes[j].Total
	})
	return result
}
