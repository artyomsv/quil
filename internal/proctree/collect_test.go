package proctree

import (
	"errors"
	"testing"
	"time"
)

// fakeSources builds a Sources over a fixed table, recording what the two-pass
// asked for. This is how the Windows path is exercised on Linux CI, where
// table_windows.go is never compiled.
type fakeSources struct {
	table []ProcessEntry
	// enrichedWith records the PID set handed to EnrichStarts — the assertion
	// that the second pass is bounded rather than machine-wide.
	enrichedWith []int
	enrichCalls  int
	starts       map[int]time.Time
}

func (f *fakeSources) sources(hasStarts bool) Sources {
	return Sources{
		Table: func() ([]ProcessEntry, error) { return append([]ProcessEntry(nil), f.table...), nil },
		CPU:   func(pids []int) CPUReading { return CPUReading{Cumulative: map[int]time.Duration{}, Supported: true} },
		RSS:   func(pids []int) map[int]uint64 { return map[int]uint64{} },

		HasStarts: hasStarts,
		EnrichStarts: func(t []ProcessEntry, pids []int) []ProcessEntry {
			f.enrichCalls++
			f.enrichedWith = append([]int(nil), pids...)
			for i := range t {
				if s, ok := f.starts[t[i].PID]; ok {
					t[i].Start = s
				}
			}
			return t
		},
	}
}

func TestCollect_SinglePassWhenTableHasStarts(t *testing.T) {
	f := &fakeSources{table: []ProcessEntry{
		p(100, 1, "zsh", 0),
		p(200, 100, "node", 10),
	}}

	s := NewSampler()
	trees, err := s.Collect(at(0), []int{100}, f.sources(true))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if f.enrichCalls != 0 {
		t.Errorf("EnrichStarts called %d times on a platform that already has "+
			"start times; that is a wasted pass over the whole table", f.enrichCalls)
	}
	if Find(trees[100], 200) == nil {
		t.Error("child missing from tree")
	}
}

// The Windows shape: the table has no start times, so a tentative tree is built
// first and only ITS pids are enriched.
func TestCollect_TwoPassEnrichesOnlyDescendants(t *testing.T) {
	f := &fakeSources{
		table: []ProcessEntry{
			{PID: 100, PPID: 1, Name: "cmd"},
			{PID: 200, PPID: 100, Name: "node"},
			{PID: 300, PPID: 200, Name: "esbuild"},
			// Not a descendant. Must never be enriched — that is the whole
			// point of bounding the handle opens.
			{PID: 900, PPID: 1, Name: "explorer"},
			{PID: 901, PPID: 900, Name: "chrome"},
		},
		starts: map[int]time.Time{100: at(0), 200: at(10), 300: at(20)},
	}

	s := NewSampler()
	trees, err := s.Collect(at(0), []int{100}, f.sources(false))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if f.enrichCalls != 1 {
		t.Fatalf("EnrichStarts called %d times, want exactly 1", f.enrichCalls)
	}
	got := map[int]bool{}
	for _, pid := range f.enrichedWith {
		got[pid] = true
	}
	for _, want := range []int{100, 200, 300} {
		if !got[want] {
			t.Errorf("descendant %d was not enriched; its start time stays "+
				"unknown and the kill path would refuse it", want)
		}
	}
	for _, unwanted := range []int{900, 901} {
		if got[unwanted] {
			t.Errorf("non-descendant %d was enriched — the second pass is "+
				"opening handles for the whole machine, which is the cost this "+
				"design exists to avoid", unwanted)
		}
	}

	// And the rebuild actually used the enriched times.
	if n := Find(trees[100], 300); n == nil || n.Start.IsZero() {
		t.Error("tree was not rebuilt with the enriched start times")
	}
}

// The reason the two-pass exists at all: without start times the splice cannot
// be rejected, so the rebuild must happen AFTER enrichment.
func TestCollect_TwoPassRejectsSpliceAfterEnrichment(t *testing.T) {
	f := &fakeSources{
		table: []ProcessEntry{
			{PID: 100, PPID: 1, Name: "cmd"},
			{PID: 200, PPID: 100, Name: "impostor"},
		},
		// 200 started BEFORE 100, so the link is a recycled PID.
		starts: map[int]time.Time{100: at(100), 200: at(50)},
	}

	s := NewSampler()
	trees, err := s.Collect(at(0), []int{100}, f.sources(false))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if Find(trees[100], 200) != nil {
		t.Error("splice survived the two-pass; the rebuild either did not run " +
			"or ran against the un-enriched table")
	}
}

func TestCollect_PropagatesTableError(t *testing.T) {
	src := Sources{
		Table: func() ([]ProcessEntry, error) { return nil, ErrUnsupported },
		CPU:   func([]int) CPUReading { return CPUReading{} },
	}
	s := NewSampler()
	if _, err := s.Collect(at(0), []int{100}, src); !errors.Is(err, ErrUnsupported) {
		t.Errorf("err = %v, want ErrUnsupported", err)
	}
}

func TestCollect_DecoratesFromBatchReads(t *testing.T) {
	table := []ProcessEntry{
		p(100, 1, "zsh", 0),
		p(200, 100, "node", 10),
	}
	src := Sources{
		Table:     func() ([]ProcessEntry, error) { return table, nil },
		HasStarts: true,
		RSS:       func([]int) map[int]uint64 { return map[int]uint64{200: 1 << 20} },
		CPU: func([]int) CPUReading {
			return CPUReading{Cumulative: map[int]time.Duration{200: time.Second}, Supported: true}
		},
	}

	s := NewSampler()
	// First collect seeds the sampler; CPU is unknown by design.
	if _, err := s.Collect(at(0), []int{100}, src); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	src.CPU = func([]int) CPUReading {
		return CPUReading{Cumulative: map[int]time.Duration{200: 2 * time.Second}, Supported: true}
	}
	trees, err := s.Collect(at(5), []int{100}, src)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	n := Find(trees[100], 200)
	if n.RSSBytes != 1<<20 {
		t.Errorf("RSS = %d, want %d", n.RSSBytes, 1<<20)
	}
	// 1s of CPU over 5s wall = 20%.
	if n.CPUPct < 19.9 || n.CPUPct > 20.1 {
		t.Errorf("CPU = %v, want ~20", n.CPUPct)
	}
	// The root had no CPU reading at all and must stay unknown.
	if trees[100].CPUPct != UnknownCPU {
		t.Errorf("root CPU = %v, want UnknownCPU", trees[100].CPUPct)
	}
}
