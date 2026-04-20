package memreport

import (
	"testing"

	ringbuf "github.com/artyomsv/quil/internal/ringbuf"
)

// paneView is a minimal in-test fake satisfying paneSource. Keeps tests
// self-contained without importing the daemon package (which would form a
// cycle).
type paneView struct {
	ID          string
	TabID       string
	OutputBuf   *ringbuf.RingBuffer
	GhostSnap   []byte
	PluginState map[string]string
	PID         int
	Alive       bool
}

func (p *paneView) paneID() string                 { return p.ID }
func (p *paneView) tabID() string                  { return p.TabID }
func (p *paneView) outputBuf() *ringbuf.RingBuffer { return p.OutputBuf }
func (p *paneView) ghostSnap() []byte              { return p.GhostSnap }
func (p *paneView) pluginState() map[string]string { return p.PluginState }
func (p *paneView) pid() int                       { return p.PID }
func (p *paneView) alive() bool                    { return p.Alive }

func TestCollector_GoHeapOnly(t *testing.T) {
	rb := ringbuf.NewRingBuffer(1024)
	rb.Write([]byte("hello world")) // 11 bytes
	p := &paneView{
		ID:        "p1",
		TabID:     "t1",
		OutputBuf: rb,
		GhostSnap: make([]byte, 100),
		PluginState: map[string]string{
			"session_id": "abc",
		},
		PID:   0,
		Alive: false,
	}
	snap := collectFrom([]paneSource{p}, func(pids []int) map[int]uint64 { return nil })
	if len(snap.Panes) != 1 {
		t.Fatalf("got %d panes, want 1", len(snap.Panes))
	}
	// 11 (OutputBuf) + 100 (GhostSnap) + len("session_id")=10 + len("abc")=3 = 124
	if got := snap.Panes[0].GoHeapBytes; got != 124 {
		t.Errorf("GoHeapBytes = %d, want 124", got)
	}
	if snap.Panes[0].PTYRSSBytes != 0 {
		t.Errorf("exited pane RSS = %d, want 0", snap.Panes[0].PTYRSSBytes)
	}
	if snap.Total != 124 {
		t.Errorf("Total = %d, want 124", snap.Total)
	}
}

func TestCollector_TotalAndSort(t *testing.T) {
	mk := func(id string, heap uint64) paneSource {
		rb := ringbuf.NewRingBuffer(int(heap) + 16)
		rb.Write(make([]byte, heap))
		return &paneView{
			ID: id, TabID: "t1", OutputBuf: rb, Alive: false,
		}
	}
	snap := collectFrom([]paneSource{mk("small", 10), mk("big", 1000), mk("mid", 100)},
		func(pids []int) map[int]uint64 { return nil })
	if len(snap.Panes) != 3 {
		t.Fatalf("got %d panes", len(snap.Panes))
	}
	if snap.Panes[0].PaneID != "big" || snap.Panes[2].PaneID != "small" {
		t.Errorf("sort order wrong: %+v", snap.Panes)
	}
	if snap.Total != 1110 {
		t.Errorf("Total = %d, want 1110", snap.Total)
	}
}

func TestCollector_LatestBeforeRun(t *testing.T) {
	c := &Collector{}
	if snap := c.Latest(); snap != nil {
		t.Errorf("Latest() before Run() = %+v, want nil", snap)
	}
}
