package proctree

import (
	"os"
	"time"
)

// SelfSampler reports THIS process's CPU usage, as a percentage of one core.
//
// Separate from Sampler, which measures other processes, because measuring
// yourself removes the one thing that makes Sampler complicated: a process
// cannot be recycled onto a different PID while it is doing the measuring, so
// there is no identity check and no start time to compare. What survives is the
// refusal to invent a number — every case where a percentage cannot be computed
// honestly reports ok=false, and callers turn that into an em dash rather than
// a 0% that reads as idle.
//
// It exists because quil's own processes are the ones missing from the process
// dialog: the daemon samples every pane's children, and nothing sampled the TUI
// that was burning more CPU than any of them.
type SelfSampler struct {
	// read is a seam. The platform sources live behind readCPU, which is
	// unexported, so tests inject a deterministic counter here instead of
	// trying to make a real CPU burn reproducible.
	read func() (time.Duration, bool)

	prev     time.Duration
	prevAt   time.Time
	havePrev bool
}

// NewSelfSampler returns a sampler reading this process's own CPU counter.
//
// The first Percent always reports unknown: a rate needs two readings.
func NewSelfSampler() *SelfSampler {
	return &SelfSampler{read: readSelfCPUTime}
}

// Percent returns CPU used since the previous call, as a percentage of one
// core, or ok=false when no honest answer exists.
//
// ok=false covers: the first call, a platform with no cumulative counter, a
// failed read, a non-positive elapsed time, and a counter that went backwards.
// A nil sampler, or one built as a struct literal without a reader, answers
// unknown rather than panicking. Owners are constructed both ways (the proc
// collector's tests build it as a literal), and this runs on a tick inside the
// daemon — a diagnostic that can crash the process it diagnoses is worse than
// no diagnostic. Same convention as procTreeCPU(nil).
func (s *SelfSampler) Percent(now time.Time) (float64, bool) {
	if s == nil || s.read == nil {
		return 0, false
	}
	total, ok := s.read()
	if !ok {
		// Drop the baseline. Keeping it would make the NEXT tick delta across
		// both windows, and a two-window delta over a one-window elapsed
		// renders as a spike the process never had.
		s.havePrev = false
		return 0, false
	}

	prev, prevAt, had := s.prev, s.prevAt, s.havePrev
	s.prev, s.prevAt, s.havePrev = total, now, true

	if !had {
		return 0, false
	}
	elapsed := now.Sub(prevAt)
	if elapsed <= 0 {
		return 0, false
	}
	delta := total - prev
	if delta < 0 {
		return 0, false
	}
	return float64(delta) / float64(elapsed) * 100, true
}

// readSelfCPUTime returns this process's cumulative CPU time.
//
// Deliberately routed through the same per-platform readCPU the pane trees use,
// so there is one CPU source per platform rather than two that can disagree.
//
// Reports false on Darwin, and that is correct rather than a gap: that platform
// exposes an instantaneous kernel average and no cumulative counter reachable
// without CGo (see CPUReading), so there is nothing to delta. quil's own rows
// show an em dash there instead of a number meaning something different from
// the one beside it.
func readSelfCPUTime() (time.Duration, bool) {
	pid := os.Getpid()
	r := readCPU([]int{pid})
	if !r.Supported || r.Cumulative == nil {
		return 0, false
	}
	d, ok := r.Cumulative[pid]
	return d, ok
}
