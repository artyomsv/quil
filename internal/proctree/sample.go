package proctree

import "time"

// CPUReading is one platform's answer about CPU usage.
//
// The two shapes are not interchangeable, and the split is deliberate rather
// than an abstraction leak. Windows and Linux expose CUMULATIVE CPU time, so a
// percentage is a rate this package computes across two ticks. Darwin has no
// such number reachable without CGo, so it reports the kernel's own
// INSTANTANEOUS figure — a decaying average over the kernel's window, not usage
// over ours.
//
// Collapsing them behind one "percent" would make the Darwin number look like
// the other two while meaning something different. It is carried separately so
// the dialog can say so.
type CPUReading struct {
	// Cumulative is total CPU time consumed per PID, since process start.
	Cumulative map[int]time.Duration
	// Instant is a kernel-reported percentage per PID.
	Instant map[int]float64
	// Supported is false on platforms with no CPU source at all.
	Supported bool
}

// cpuSample is one PID's previous reading, kept between ticks.
//
// Start is stored alongside the time because a cumulative counter is only
// comparable against itself: if the PID was recycled between ticks, the "delta"
// is a difference between two unrelated processes.
type cpuSample struct {
	start      time.Time
	cumulative time.Duration
}

// Sampler turns cumulative CPU readings into percentages across ticks.
//
// It is the state an on-demand design has nowhere to put, and the reason the
// collector runs on a tick rather than answering a request directly.
type Sampler struct {
	prev   map[int]cpuSample
	prevAt time.Time
}

// NewSampler returns a Sampler with no history, so its first Update reports
// UnknownCPU for every PID.
func NewSampler() *Sampler {
	return &Sampler{prev: map[int]cpuSample{}}
}

// Update records this tick's reading and returns a percentage per PID.
//
// now is a parameter rather than a clock field so tests drive elapsed time
// exactly; starts comes from the process table and is what makes PID reuse
// detectable.
//
// Every PID whose percentage cannot be computed HONESTLY is absent from the
// result, which Decorate leaves as UnknownCPU. That covers a first sample, a
// process that appeared this tick, a recycled PID, a non-positive elapsed time
// and a negative delta.
func (s *Sampler) Update(now time.Time, r CPUReading, starts map[int]time.Time) map[int]float64 {
	if !r.Supported {
		s.prevAt = now
		return nil
	}

	// Platforms reporting an instantaneous percentage need no history: the
	// kernel already did the averaging, over its own window rather than ours.
	if r.Instant != nil {
		s.prevAt = now
		out := make(map[int]float64, len(r.Instant))
		for pid, pct := range r.Instant {
			if pct >= 0 {
				out[pid] = pct
			}
		}
		return out
	}

	elapsed := now.Sub(s.prevAt)
	cur := make(map[int]cpuSample, len(r.Cumulative))
	out := make(map[int]float64, len(r.Cumulative))

	for pid, total := range r.Cumulative {
		cur[pid] = cpuSample{start: starts[pid], cumulative: total}

		prev, ok := s.prev[pid]
		if !ok {
			// First time we have seen this PID: nothing to subtract from.
			continue
		}
		if elapsed <= 0 {
			continue
		}
		// PID reuse. The counter restarted at zero under a different process,
		// so the difference is meaningless — and a LARGE positive one would
		// render as a plausible-looking spike on a process that never ran.
		if !sameProcess(prev.start, cur[pid].start) {
			continue
		}
		delta := total - prev.cumulative
		if delta < 0 {
			// A cumulative counter cannot decrease. If it did, the identity
			// check above missed a reuse; refuse rather than report.
			continue
		}
		out[pid] = float64(delta) / float64(elapsed) * 100
	}

	s.prev = cur
	s.prevAt = now
	return out
}

// sameProcess reports whether two start times describe the same process.
//
// Unknown on either side counts as NOT the same, so an unreadable start time
// costs a percentage rather than risking a fabricated one. This is the opposite
// direction from Build's link handling, and deliberately so: there, refusing
// hides a real child; here, accepting invents a number.
func sameProcess(a, b time.Time) bool {
	if a.IsZero() || b.IsZero() {
		return false
	}
	return a.Equal(b)
}
