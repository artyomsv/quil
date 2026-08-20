package proctree

import (
	"testing"
	"time"
)

func cumulative(m map[int]time.Duration) CPUReading {
	return CPUReading{Cumulative: m, Supported: true}
}

func TestSampler_FirstUpdateReportsNothing(t *testing.T) {
	s := NewSampler()
	got := s.Update(at(0), cumulative(map[int]time.Duration{100: time.Second}), map[int]time.Time{100: at(0)})

	if len(got) != 0 {
		t.Errorf("first update returned %v; with nothing to subtract from, every "+
			"PID must be absent so it renders as unknown rather than 0%%", got)
	}
}

func TestSampler_PercentAcrossTwoTicks(t *testing.T) {
	starts := map[int]time.Time{100: at(0)}
	s := NewSampler()
	s.Update(at(0), cumulative(map[int]time.Duration{100: 0}), starts)

	// 1s of CPU consumed over 5s of wall clock = 20%.
	got := s.Update(at(5), cumulative(map[int]time.Duration{100: time.Second}), starts)

	if pct, ok := got[100]; !ok || pct < 19.9 || pct > 20.1 {
		t.Errorf("percent = %v (present=%v), want ~20", pct, ok)
	}
}

// Per-core, not machine-normalised: a process saturating two cores reports
// 200%. This matches top and ps, and it is the case that matters most — a
// runaway across several cores must not be flattened into "100%".
func TestSampler_PerCorePercentExceeds100(t *testing.T) {
	starts := map[int]time.Time{100: at(0)}
	s := NewSampler()
	s.Update(at(0), cumulative(map[int]time.Duration{100: 0}), starts)

	// 10s of CPU over 5s of wall clock — only possible across two cores.
	got := s.Update(at(5), cumulative(map[int]time.Duration{100: 10 * time.Second}), starts)

	if pct := got[100]; pct < 199 || pct > 201 {
		t.Errorf("percent = %v, want ~200; a multi-core runaway must not be "+
			"clamped to 100", pct)
	}
}

// The PID-reuse case. Same PID, different process: the cumulative counter
// restarted, so the delta is between two unrelated processes.
func TestSampler_RecycledPIDReportsNothing(t *testing.T) {
	s := NewSampler()
	s.Update(at(0), cumulative(map[int]time.Duration{100: 50 * time.Second}), map[int]time.Time{100: at(0)})

	// Same PID, but it started later — a different process wearing the number.
	got := s.Update(at(5), cumulative(map[int]time.Duration{100: time.Second}), map[int]time.Time{100: at(3)})

	if pct, ok := got[100]; ok {
		t.Errorf("recycled PID reported %v%%; the counter restarted under a "+
			"different process, so any delta is fiction", pct)
	}
}

func TestSampler_UnknownStartReportsNothing(t *testing.T) {
	s := NewSampler()
	s.Update(at(0), cumulative(map[int]time.Duration{100: 0}), map[int]time.Time{100: {}})
	got := s.Update(at(5), cumulative(map[int]time.Duration{100: time.Second}), map[int]time.Time{100: {}})

	if _, ok := got[100]; ok {
		t.Error("an unreadable start time yielded a percentage; identity cannot " +
			"be confirmed, so the number would be a guess")
	}
}

func TestSampler_NegativeDeltaReportsNothing(t *testing.T) {
	starts := map[int]time.Time{100: at(0)}
	s := NewSampler()
	s.Update(at(0), cumulative(map[int]time.Duration{100: 10 * time.Second}), starts)
	got := s.Update(at(5), cumulative(map[int]time.Duration{100: time.Second}), starts)

	if pct, ok := got[100]; ok {
		t.Errorf("decreasing cumulative counter reported %v%%; a cumulative "+
			"counter cannot go backwards, so the identity check missed a reuse", pct)
	}
}

func TestSampler_NonPositiveElapsedReportsNothing(t *testing.T) {
	starts := map[int]time.Time{100: at(0)}
	s := NewSampler()
	s.Update(at(5), cumulative(map[int]time.Duration{100: 0}), starts)
	// Same instant: dividing by zero elapsed would be +Inf.
	got := s.Update(at(5), cumulative(map[int]time.Duration{100: time.Second}), starts)

	if pct, ok := got[100]; ok {
		t.Errorf("zero elapsed reported %v%%", pct)
	}
}

func TestSampler_UnsupportedPlatformReportsNothing(t *testing.T) {
	s := NewSampler()
	got := s.Update(at(0), CPUReading{Supported: false}, nil)

	if len(got) != 0 {
		t.Errorf("unsupported platform returned %v, want nothing", got)
	}
}

// Darwin's shape: the kernel already averaged, so there is no history to keep
// and the first update is immediately useful.
func TestSampler_InstantReadingNeedsNoHistory(t *testing.T) {
	s := NewSampler()
	got := s.Update(at(0), CPUReading{Instant: map[int]float64{100: 37.5}, Supported: true}, nil)

	if pct, ok := got[100]; !ok || pct != 37.5 {
		t.Errorf("instant reading = %v (present=%v), want 37.5", pct, ok)
	}
}

func TestSampler_InstantReadingDropsNegative(t *testing.T) {
	s := NewSampler()
	got := s.Update(at(0), CPUReading{Instant: map[int]float64{100: -1}, Supported: true}, nil)

	if _, ok := got[100]; ok {
		t.Error("a negative instant reading was passed through; it means the " +
			"platform had no answer, and must stay unknown")
	}
}

// A PID present this tick but not last must not inherit anything.
func TestSampler_NewPIDReportsNothingUntilSecondSighting(t *testing.T) {
	starts := map[int]time.Time{100: at(0), 200: at(4)}
	s := NewSampler()
	s.Update(at(0), cumulative(map[int]time.Duration{100: 0}), starts)

	got := s.Update(at(5), cumulative(map[int]time.Duration{
		100: time.Second,
		200: 3 * time.Second, // appeared this tick
	}), starts)

	if _, ok := got[100]; !ok {
		t.Error("the PID seen twice lost its percentage")
	}
	if pct, ok := got[200]; ok {
		t.Errorf("a PID seen for the first time reported %v%%; its cumulative "+
			"total would be charged entirely to this one interval", pct)
	}
}
