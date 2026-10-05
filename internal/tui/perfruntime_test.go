package tui

import (
	"bytes"
	"math"
	"runtime/metrics"
	"strings"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/logger"
)

// captureLoggedLine runs fn with the package logger pointed at a buffer and
// returns what was written.
//
// logger.Save puts back exactly what was there before, including the nil
// handler of a package that never called Init: a hard-coded level would
// silently change every later test in the package into one that formats
// every `logger.Info` argument it reaches.
func captureLoggedLine(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	t.Cleanup(logger.Save())
	logger.Init("info", &buf)
	fn()
	return buf.String()
}

// sampleAt builds a runtimeSample with every metric marked present. Tests that
// care about an ABSENT metric clear the flag themselves, so the absent case is
// always written explicitly rather than inherited from a zero value.
func sampleAt(cpu time.Duration, cycles uint64, assist float64, pause time.Duration, live uint64, gor int) runtimeSample {
	return runtimeSample{
		cpuUsed: cpu, gcCycles: cycles,
		gcAssist: assist, gcPause: pause, heapLive: live, goroutines: gor,
		haveCPU: true, haveCycles: true, havePause: true, haveAssist: true, haveLive: true,
	}
}

func TestFormatRuntimeDelta_LongUptime_ReportsWindowDeltas(t *testing.T) {
	// A process up for a long time: large cumulative counters, small change
	// across this one window. Reporting the totals would drown the change.
	prev := sampleAt(66*time.Minute+40*time.Second, 900, 12.0, 30*time.Second, 500<<20, 100)
	cur := sampleAt(66*time.Minute+41*time.Second+500*time.Millisecond, 902, 12.004,
		30*time.Second+1500*time.Microsecond, 998<<20, 142)

	got := formatRuntimeDelta(prev, cur, 5*time.Second)

	for _, want := range []string{"cpu=1.5s/5s", "gor=142", "heap=998MB", "gc=2", "pause=1.5ms", "assist=4ms"} {
		if !strings.Contains(got, want) {
			t.Errorf("formatRuntimeDelta() = %q, missing %q", got, want)
		}
	}
	// A leaked cumulative total would render as the whole-uptime figure. Both
	// strings below are what a leak actually PRINTS, not what the struct holds
	// — an earlier version asserted on the raw seconds ("4000"), which the
	// duration formatter can never emit, so that half of the check could not
	// fail whatever the code did.
	for _, leak := range []string{"1h6m41.5s", "gc=902"} {
		if strings.Contains(got, leak) {
			t.Errorf("formatRuntimeDelta() = %q — leaked a cumulative total (%q)", got, leak)
		}
	}
}

func TestFormatRuntimeDelta_MetricUnavailable_RendersQuestionMark(t *testing.T) {
	// The whole point of the have* flags: a Go build that does not supply a
	// metric must not produce a confident "0", which would read as a finding
	// ("no GC assist time") drawn from a value nobody measured.
	prev := sampleAt(100*time.Second, 10, 1.0, time.Second, 1<<20, 5)
	cur := sampleAt(105*time.Second, 11, 1.5, 2*time.Second, 2<<20, 6)
	cur.haveAssist = false
	cur.haveCPU = false
	cur.haveLive = false

	got := formatRuntimeDelta(prev, cur, 5*time.Second)

	for _, want := range []string{"cpu=?", "assist=?", "heap=?"} {
		if !strings.Contains(got, want) {
			t.Errorf("formatRuntimeDelta() = %q, missing %q", got, want)
		}
	}
	// The metrics that WERE available must still be reported.
	if !strings.Contains(got, "gc=1") {
		t.Errorf("formatRuntimeDelta() = %q — dropped an available metric alongside the missing ones", got)
	}
}

func TestFormatRuntimeDelta_FirstSampleUnavailable_RendersQuestionMark(t *testing.T) {
	// The mirror of the case above, and the one that actually happens: the
	// FIRST reading, taken in newEventLoopStats, is the one most likely to have
	// failed. Both guards are `prev.haveX && cur.haveX`, so a test that only
	// ever clears cur leaves half of each guard unexercised.
	prev := sampleAt(100*time.Second, 10, 1.0, time.Second, 1<<20, 5)
	prev.haveCPU = false
	prev.haveAssist = false
	prev.havePause = false
	cur := sampleAt(105*time.Second, 11, 1.5, 2*time.Second, 2<<20, 6)

	got := formatRuntimeDelta(prev, cur, 5*time.Second)

	for _, want := range []string{"cpu=?", "assist=?", "pause=?"} {
		if !strings.Contains(got, want) {
			t.Errorf("formatRuntimeDelta() = %q, missing %q", got, want)
		}
	}
	// heap comes from cur alone — a level, not a delta — so an unreadable first
	// sample must not suppress it.
	if !strings.Contains(got, "heap=2MB") {
		t.Errorf("formatRuntimeDelta() = %q — heap needs no prior sample and must survive one", got)
	}
}

func TestFormatRuntimeDelta_CounterReset_RefusesNegativeDelta(t *testing.T) {
	// These counters only rise. A fall means the two samples did not come from
	// one continuous run, and the honest render is "unknown" — clamping to zero
	// would publish "this window used no CPU", which is a conclusion rather
	// than a missing reading.
	prev := sampleAt(4000*time.Second, 900, 12.0, 30*time.Second, 500<<20, 100)
	cur := sampleAt(10*time.Second, 3, 0.1, time.Second, 400<<20, 90)

	got := formatRuntimeDelta(prev, cur, 5*time.Second)

	for _, want := range []string{"cpu=?", "gc=?", "pause=?", "assist=?"} {
		if !strings.Contains(got, want) {
			t.Errorf("formatRuntimeDelta() = %q, missing %q", got, want)
		}
	}
	// heap is a LEVEL, not a delta, so it is always reportable.
	if !strings.Contains(got, "heap=400MB") {
		t.Errorf("formatRuntimeDelta() = %q — heap is a level and must survive a counter reset", got)
	}
}

func TestFormatRuntimeDelta_NoCycleCompleted_MarksAssistUnpublished(t *testing.T) {
	// Mark-assist CPU is accounted at mark termination. A window in which no
	// cycle completed therefore has no assist figure to give, and "0s" there
	// would assert the collector charged the mutator nothing — a finding this
	// reading does not support. It gets its own sentinel, distinct from the "?"
	// that means the metric could not be read at all.
	prev := sampleAt(100*time.Second, 42, 3.0, time.Second, 8<<20, 20)
	cur := sampleAt(101*time.Second, 42, 3.0, time.Second, 8<<20, 20)

	got := formatRuntimeDelta(prev, cur, 5*time.Second)

	if !strings.Contains(got, "assist=-") {
		t.Errorf("formatRuntimeDelta() = %q, want assist=- when no GC cycle completed", got)
	}
	if strings.Contains(got, "assist=0s") {
		t.Errorf("formatRuntimeDelta() = %q — printed a measured-looking zero for an unpublished figure", got)
	}
	if !strings.Contains(got, "gc=0") || !strings.Contains(got, "cpu=1s/") {
		t.Errorf("formatRuntimeDelta() = %q — the live figures must still be reported", got)
	}
}

func TestFormatHeapBytes_SubMegabyte_DoesNotCollapseToZero(t *testing.T) {
	// The one remaining place this file could print a confident zero.
	if got := formatHeapBytes(700 << 10); got != "700KB" {
		t.Errorf("formatHeapBytes(700KB) = %q, want 700KB", got)
	}
	if got := formatHeapBytes(3 << 20); got != "3MB" {
		t.Errorf("formatHeapBytes(3MB) = %q, want 3MB", got)
	}
}

func TestHistogramTotal_InfiniteLastBucket_UsesFiniteBound(t *testing.T) {
	// The branch no constructed-sample test can reach through
	// formatRuntimeDelta: the runtime's final bucket has an upper bound of
	// +Inf, and time.Duration(+Inf * 1e9) converts to a garbage value that
	// would also poison the NEXT window, since formatRuntimeDelta compares the
	// two totals before subtracting. A mutation run proved this branch was
	// unguarded by any test: replacing the +Inf check with `if false` left the
	// whole package green.
	h := &metrics.Float64Histogram{
		Buckets: []float64{0, 0.001, math.Inf(1)},
		Counts:  []uint64{0, 2}, // two pauses in the [1ms, +Inf) bucket
	}
	if got := histogramTotal(h); got != 2*time.Millisecond {
		t.Errorf("histogramTotal() = %v, want 2ms — the +Inf bound leaked into the duration", got)
	}
	if got := histogramTotal(nil); got != 0 {
		t.Errorf("histogramTotal(nil) = %v, want 0", got)
	}
}

func TestHistogramTotal_ShortBuckets_DoesNotPanic(t *testing.T) {
	// Unreachable under the documented contract, which is exactly why it is
	// worth a test: this runs on the Bubble Tea program goroutine, where a
	// panic ends the user's session. A truncated diagnostic is the better
	// failure.
	h := &metrics.Float64Histogram{
		Buckets: []float64{0, 0.001},
		Counts:  []uint64{1, 5}, // one count too many for the bucket list
	}
	if got := histogramTotal(h); got != time.Millisecond {
		t.Errorf("histogramTotal() = %v, want 1ms — the in-contract prefix should still be summed", got)
	}
}

// TestEventLoopStats_Flush_EmitsRuntimeSection checks the wiring, not the
// formatter. The formatter is exercised directly above; what this covers is
// that flush() calls it, puts the result on the line, AND advances its delta
// baseline — the "on switch" a bottom-up test suite never touches.
func TestEventLoopStats_Flush_EmitsRuntimeSection(t *testing.T) {
	prevRead := readRuntimeSample
	t.Cleanup(func() { readRuntimeSample = prevRead })

	n := 0
	readRuntimeSample = func() runtimeSample {
		n++
		// Each reading is one second of CPU and one GC cycle beyond the last.
		return sampleAt(time.Duration(n)*time.Second, uint64(n), 0, 0, 777<<20, 33)
	}

	out := captureLoggedLine(t, func() {
		s := newEventLoopStats() // reading 1
		s.recordView(3 * time.Millisecond)
		s.flush() // reading 2, emits
		s.recordView(3 * time.Millisecond)
		s.flush() // reading 3 — must be compared against reading 2, not reading 1
	})

	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 perf lines, got %d: %q", len(lines), out)
	}
	if !strings.Contains(lines[0], "rt(") {
		t.Fatalf("flush() line = %q — no runtime section, so the perf log still cannot say why a frame was slow", lines[0])
	}
	for _, want := range []string{"gor=33", "heap=777MB", "gc=1"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("first flush line = %q, missing %q", lines[0], want)
		}
	}
	// The second window is one cycle and one second of CPU beyond the FIRST
	// FLUSH. Measuring it against the construction-time sample instead would
	// say gc=2 and cpu=2s — every window reporting the total since startup,
	// which is the failure the delta design exists to prevent and which looks
	// entirely plausible in a log. Dropping `s.lastRuntime = rt` from flush()
	// leaves the whole package green without this assertion.
	for _, want := range []string{"gc=1", "cpu=1s/"} {
		if !strings.Contains(lines[1], want) {
			t.Errorf("second flush line = %q, missing %q — flush did not advance its delta baseline", lines[1], want)
		}
	}
	if n != 3 {
		t.Errorf("readRuntimeSample called %d times, want 3 (one at construction, one per flush)", n)
	}
}

// TestReadRuntimeSample_ThisGoVersion_ResolvesEveryMetric is the guard against
// a typo in runtimeMetricNames.
//
// A misspelled name is not an error anywhere — metrics.Read returns KindBad,
// the formatter honestly renders "?", and the perf log then reports "unknown"
// forever for a metric the runtime was supplying all along. That failure is
// invisible in production and invisible in every other test here, because they
// all drive the formatter with constructed samples.
func TestReadRuntimeSample_ThisGoVersion_ResolvesEveryMetric(t *testing.T) {
	s := readRuntimeSample()

	for _, c := range []struct {
		name string
		have bool
	}{
		{"processCPU (getrusage / GetProcessTimes)", s.haveCPU},
		{"/gc/cycles/total:gc-cycles", s.haveCycles},
		{"/gc/pauses:seconds", s.havePause},
		{"/cpu/classes/gc/mark/assist:cpu-seconds", s.haveAssist},
		{"/gc/heap/live:bytes", s.haveLive},
	} {
		if !c.have {
			t.Errorf("%s not supplied — check the name in runtimeMetricNames against this Go version", c.name)
		}
	}
	if s.goroutines < 1 {
		t.Errorf("goroutines = %d, want at least the test's own", s.goroutines)
	}
}

// TestProcessCPU_Advances_WithoutAGarbageCollection is the regression test for
// the defect that made the first version of this file useless.
//
// The obvious CPU source is runtime/metrics' /cpu/classes/total minus
// /cpu/classes/idle. Those values are a snapshot taken at gcMarkTermination, so
// they do not move unless a GC CYCLE COMPLETES. Measured on Go 1.25, four
// goroutines burning CPU for three seconds with the collector off reported
// 0.000s used. In a TUI that collects roughly every nine minutes that is a
// permanent, confident zero — and the perf line reads a near-zero cpu as "the
// process was not running", the exact opposite of the truth.
//
// This test burns CPU with the collector disabled and requires the figure to
// move. It fails against the metrics-based implementation and passes against
// the OS one.
func TestProcessCPU_Advances_WithoutAGarbageCollection(t *testing.T) {
	before, ok := processCPU()
	if !ok {
		t.Skip("processCPU unavailable on this platform")
	}

	// Deliberately NOT debug.SetGCPercent(-1): this test runs in the shared
	// package binary, and switching the collector off process-wide would
	// change every test scheduled after it. Busy work that allocates nothing
	// starves the collector of any reason to run just as effectively.
	cyclesBefore := readCyclesForTest()
	x := 0
	deadline := time.Now().Add(150 * time.Millisecond)
	for time.Now().Before(deadline) {
		for i := 0; i < 200000; i++ {
			x += i
		}
	}
	_ = x

	after, ok := processCPU()
	if !ok {
		t.Fatal("processCPU stopped reporting mid-test")
	}
	if after <= before {
		t.Fatalf("processCPU did not advance across %v of busy work: before=%v after=%v — "+
			"the CPU source is not a live counter", 150*time.Millisecond, before, after)
	}
	if got := readCyclesForTest() - cyclesBefore; got != 0 {
		t.Logf("note: %d GC cycle(s) ran during the busy loop; the assertion above no longer "+
			"distinguishes a live counter from a GC-published one", got)
	}
}

// readCyclesForTest reads the completed-cycle counter so the test above can say
// whether its own premise held.
func readCyclesForTest() uint64 {
	s := []metrics.Sample{{Name: "/gc/cycles/total:gc-cycles"}}
	metrics.Read(s)
	if s[0].Value.Kind() != metrics.KindUint64 {
		return 0
	}
	return s[0].Value.Uint64()
}
