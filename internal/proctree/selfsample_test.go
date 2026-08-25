package proctree

import (
	"testing"
	"time"
)

// A quil process measuring ITSELF needs none of Sampler's PID-reuse machinery:
// a process cannot be replaced by a different process while it is doing the
// measuring. What it does need is the same refusal to invent a number, which is
// what these tests pin.

func fixedReader(vals ...time.Duration) func() (time.Duration, bool) {
	i := 0
	return func() (time.Duration, bool) {
		if i >= len(vals) {
			return vals[len(vals)-1], true
		}
		v := vals[i]
		i++
		return v, true
	}
}

func TestSelfSampler_FirstReadHasNothingToSubtractFrom(t *testing.T) {
	s := &SelfSampler{read: fixedReader(500 * time.Millisecond)}

	if pct, ok := s.Percent(time.Unix(100, 0)); ok {
		t.Errorf("first Percent = %v, ok=true; want ok=false — there is no "+
			"previous reading to delta against", pct)
	}
}

func TestSelfSampler_ComputesPercentAcrossTicks(t *testing.T) {
	// 100 ms of CPU consumed over 1 s of wall clock is 10% of one core.
	s := &SelfSampler{read: fixedReader(1*time.Second, 1100*time.Millisecond)}

	if _, ok := s.Percent(time.Unix(100, 0)); ok {
		t.Fatal("first Percent reported ok, want false")
	}
	pct, ok := s.Percent(time.Unix(101, 0))
	if !ok {
		t.Fatal("second Percent ok = false, want true")
	}
	if pct < 9.99 || pct > 10.01 {
		t.Errorf("Percent = %v, want 10", pct)
	}
}

func TestSelfSampler_RefusesNonPositiveElapsed(t *testing.T) {
	s := &SelfSampler{read: fixedReader(1*time.Second, 2*time.Second)}

	s.Percent(time.Unix(100, 0))
	// Same instant: dividing by zero elapsed would produce +Inf, which formats
	// as a plausible-looking enormous percentage.
	if pct, ok := s.Percent(time.Unix(100, 0)); ok {
		t.Errorf("Percent with zero elapsed = %v, ok=true; want ok=false", pct)
	}
}

func TestSelfSampler_RefusesBackwardsCounter(t *testing.T) {
	// A cumulative counter cannot decrease. If it appears to, the reading is
	// untrustworthy and a percentage must not be invented from it.
	s := &SelfSampler{read: fixedReader(2*time.Second, 1*time.Second)}

	s.Percent(time.Unix(100, 0))
	if pct, ok := s.Percent(time.Unix(101, 0)); ok {
		t.Errorf("Percent with a decreasing counter = %v, ok=true; want ok=false", pct)
	}
}

func TestSelfSampler_UnreadableCPUIsUnknownNotZero(t *testing.T) {
	// Darwin reaches this: it reports an instantaneous kernel figure and no
	// cumulative counter, so there is nothing to delta. Reporting 0% there
	// would label every quil process idle on that platform.
	s := &SelfSampler{read: func() (time.Duration, bool) { return 0, false }}

	if _, ok := s.Percent(time.Unix(100, 0)); ok {
		t.Error("Percent ok = true on a platform with no cumulative CPU source; want false")
	}
	if _, ok := s.Percent(time.Unix(101, 0)); ok {
		t.Error("Percent ok = true on the second tick too; want false")
	}
}

// A read that fails once must not poison the next tick with a stale baseline:
// the delta would span two windows and read as a spike.
func TestSelfSampler_RecoversAfterAFailedRead(t *testing.T) {
	calls := 0
	s := &SelfSampler{read: func() (time.Duration, bool) {
		calls++
		switch calls {
		case 1:
			return 1 * time.Second, true
		case 2:
			return 0, false
		default:
			return 3 * time.Second, true
		}
	}}

	s.Percent(time.Unix(100, 0)) // baseline
	if _, ok := s.Percent(time.Unix(101, 0)); ok {
		t.Error("Percent after a failed read reported ok; want false")
	}
	// 3s total at t=102. If the failed tick left the t=100 baseline in place,
	// this computes (3s-1s)/1s = 200%. Correct behaviour is to have re-based on
	// the failed tick, leaving nothing to compare and reporting unknown again.
	pct, ok := s.Percent(time.Unix(102, 0))
	if ok && pct > 100 {
		t.Errorf("Percent = %v after recovering from a failed read — a stale "+
			"baseline was carried across the gap", pct)
	}
}

// A zero-valued or struct-literal-constructed owner leaves this nil, and the
// collector calls Percent on every tick. Unknown is the answer that matches the
// rest of this package (procTreeCPU(nil) does the same); a panic in a
// diagnostic path would take the daemon with it.
func TestSelfSampler_NilReceiverIsUnknownNotAPanic(t *testing.T) {
	var s *SelfSampler
	if _, ok := s.Percent(time.Unix(100, 0)); ok {
		t.Error("nil SelfSampler reported ok; want unknown")
	}
}

func TestSelfSampler_NilReaderIsUnknownNotAPanic(t *testing.T) {
	s := &SelfSampler{} // read left nil, as a struct literal leaves it
	if _, ok := s.Percent(time.Unix(100, 0)); ok {
		t.Error("SelfSampler with a nil reader reported ok; want unknown")
	}
}

func TestNewSelfSampler_ReadsThisProcess(t *testing.T) {
	s := NewSelfSampler()
	if s.read == nil {
		t.Fatal("NewSelfSampler left the reader nil")
	}
	// Two ticks with real work in between. The assertion is deliberately weak:
	// this is the one test touching a real platform source, and the value it
	// returns is not deterministic. It pins that the wiring exists, not what
	// the machine was doing.
	if _, ok := s.Percent(time.Now()); ok {
		t.Error("first real Percent reported ok; want false")
	}
	sink := 0
	for i := 0; i < 3_000_000; i++ {
		sink += i % 7
	}
	_ = sink
	pct, ok := s.Percent(time.Now())

	// cpuIsSampled is the platform's own statement that it has a cumulative
	// counter to delta. Where it does, a second reading after real work MUST
	// produce an answer — without this, the test passes on a platform whose
	// CPU source is entirely broken and silently returns unknown forever.
	//
	// This is the assertion that has to run on Windows specifically: CI is
	// Linux and never compiles table_windows.go, so a regression in
	// GetProcessTimes is invisible until this binary is run natively.
	if cpuIsSampled && !ok {
		t.Error("platform reports cpuIsSampled but Percent gave no answer after " +
			"two readings — the cumulative CPU source is not working")
	}
	if !cpuIsSampled && ok {
		t.Error("platform has no cumulative counter but Percent answered anyway")
	}
	if ok && (pct < 0 || pct > 10000) {
		t.Errorf("real Percent = %v, outside any believable range", pct)
	}
}
