package memreport

import (
	"context"
	"sync"
	"testing"
	"time"

	ringbuf "github.com/artyomsv/quil/internal/ringbuf"
)

// paneView is a minimal in-test fake satisfying PaneSource. Keeps tests
// self-contained without importing the daemon package (which would form a
// cycle).
type paneView struct {
	id          string
	tabID       string
	outputBuf   *ringbuf.RingBuffer
	ghostSnap   []byte
	pluginState map[string]string
	pid         int
	alive       bool
}

func (p *paneView) Snapshot() PaneSourceSnapshot {
	s := PaneSourceSnapshot{
		PaneID: p.id,
		TabID:  p.tabID,
		Alive:  p.alive,
		PID:    p.pid,
	}
	if p.outputBuf != nil {
		s.HeapBytes += uint64(p.outputBuf.Len())
	}
	s.HeapBytes += uint64(len(p.ghostSnap))
	for k, v := range p.pluginState {
		s.HeapBytes += uint64(len(k) + len(v))
	}
	return s
}

func TestCollector_GoHeapOnly(t *testing.T) {
	rb := ringbuf.NewRingBuffer(1024)
	rb.Write([]byte("hello world")) // 11 bytes
	p := &paneView{
		id:        "p1",
		tabID:     "t1",
		outputBuf: rb,
		ghostSnap: make([]byte, 100),
		pluginState: map[string]string{
			"session_id": "abc",
		},
		pid:   0,
		alive: false,
	}
	snap := collectFrom([]PaneSource{p}, func(pids []int) map[int]uint64 { return nil })
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
	mk := func(id string, heap uint64) PaneSource {
		rb := ringbuf.NewRingBuffer(int(heap) + 16)
		rb.Write(make([]byte, heap))
		return &paneView{
			id: id, tabID: "t1", outputBuf: rb, alive: false,
		}
	}
	snap := collectFrom([]PaneSource{mk("small", 10), mk("big", 1000), mk("mid", 100)},
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

type stubLister struct {
	panes []PaneSource
}

func (s *stubLister) PaneSources() []PaneSource { return s.panes }

func TestCollector_RunPopulatesLatest(t *testing.T) {
	l := &stubLister{panes: nil}
	c := NewCollector(l, 50*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	// First collection runs synchronously at Run entry; Latest should
	// become non-nil almost immediately.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if c.Latest() != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Latest() never became non-nil")
}

func TestCollector_ConcurrentReaders(t *testing.T) {
	l := &stubLister{panes: nil}
	c := NewCollector(l, 10*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	// Wait for first snapshot.
	for i := 0; i < 50 && c.Latest() == nil; i++ {
		time.Sleep(10 * time.Millisecond)
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = c.Latest()
			}
		}()
	}
	wg.Wait()
}
