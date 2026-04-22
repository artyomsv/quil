package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// resetWatchdogGlobals clears the package-level atomics between tests. Not a
// t.Cleanup helper because some tests want to assert on the post-state, and
// tests also must not run in parallel (shared globals).
func resetWatchdogGlobals() {
	updateStartNs.Store(0)
	updateDumpSeqNs.Store(0)
}

// fakeRecorder captures logf output and stack-dump calls so tests can assert
// on them without touching the real logger or runtime.
type fakeRecorder struct {
	stackCalls int
	stackBody  string
	logLines   []string
}

func (r *fakeRecorder) cfg(now time.Time, threshold, tick time.Duration) watchdogConfig {
	return watchdogConfig{
		threshold: threshold,
		tick:      tick,
		now:       func() time.Time { return now },
		stack: func(buf []byte, _ bool) int {
			r.stackCalls++
			n := copy(buf, r.stackBody)
			return n
		},
		logf: func(format string, args ...any) {
			r.logLines = append(r.logLines, fmt.Sprintf(format, args...))
		},
	}
}

func TestCheckStuckUpdate_NoUpdate_NoDump(t *testing.T) {
	resetWatchdogGlobals()
	r := &fakeRecorder{stackBody: "irrelevant"}
	now := time.Unix(1_000, 0)

	checkStuckUpdate(r.cfg(now, 10*time.Second, 2*time.Second))

	if r.stackCalls != 0 {
		t.Errorf("stack called %d times, want 0", r.stackCalls)
	}
	if len(r.logLines) != 0 {
		t.Errorf("log lines = %v, want none", r.logLines)
	}
	if updateDumpSeqNs.Load() != 0 {
		t.Errorf("updateDumpSeqNs = %d, want 0", updateDumpSeqNs.Load())
	}
}

func TestCheckStuckUpdate_FastUpdate_NoDump(t *testing.T) {
	resetWatchdogGlobals()
	start := time.Unix(1_000, 0)
	updateStartNs.Store(start.UnixNano())
	r := &fakeRecorder{stackBody: "irrelevant"}
	now := start.Add(3 * time.Second)

	checkStuckUpdate(r.cfg(now, 10*time.Second, 2*time.Second))

	if r.stackCalls != 0 {
		t.Errorf("stack called %d times, want 0", r.stackCalls)
	}
	if len(r.logLines) != 0 {
		t.Errorf("log lines = %v, want none", r.logLines)
	}
	if updateDumpSeqNs.Load() != 0 {
		t.Errorf("updateDumpSeqNs = %d, want 0 (no dump should have happened)", updateDumpSeqNs.Load())
	}
}

func TestCheckStuckUpdate_StuckUpdate_EmitsDump(t *testing.T) {
	resetWatchdogGlobals()
	start := time.Unix(1_000, 0)
	updateStartNs.Store(start.UnixNano())
	r := &fakeRecorder{stackBody: "goroutine 1 [running]:\nmain.main()"}
	now := start.Add(15 * time.Second)

	checkStuckUpdate(r.cfg(now, 10*time.Second, 2*time.Second))

	if r.stackCalls != 1 {
		t.Fatalf("stack called %d times, want 1", r.stackCalls)
	}
	if len(r.logLines) != 2 {
		t.Fatalf("log lines = %d, want 2", len(r.logLines))
	}
	if !strings.Contains(r.logLines[0], "stuck for") {
		t.Errorf("first log line = %q, want 'stuck for' in it", r.logLines[0])
	}
	if !strings.Contains(r.logLines[1], "goroutine 1") {
		t.Errorf("second log line = %q, want goroutine dump", r.logLines[1])
	}
	if updateDumpSeqNs.Load() != start.UnixNano() {
		t.Errorf("updateDumpSeqNs = %d, want %d", updateDumpSeqNs.Load(), start.UnixNano())
	}
}

func TestCheckStuckUpdate_AlreadyDumpedSameUpdate_Skips(t *testing.T) {
	resetWatchdogGlobals()
	start := time.Unix(1_000, 0)
	updateStartNs.Store(start.UnixNano())
	r := &fakeRecorder{stackBody: "stacks"}
	cfg := r.cfg(start.Add(15*time.Second), 10*time.Second, 2*time.Second)

	checkStuckUpdate(cfg)
	checkStuckUpdate(cfg)

	if r.stackCalls != 1 {
		t.Errorf("stack called %d times, want 1 (second call should be skipped)", r.stackCalls)
	}
}

func TestCheckStuckUpdate_NewStuckUpdate_DumpsAgain(t *testing.T) {
	resetWatchdogGlobals()
	start1 := time.Unix(1_000, 0)
	updateStartNs.Store(start1.UnixNano())
	r := &fakeRecorder{stackBody: "stacks"}

	checkStuckUpdate(r.cfg(start1.Add(15*time.Second), 10*time.Second, 2*time.Second))
	if r.stackCalls != 1 {
		t.Fatalf("after first stuck update: stack called %d times, want 1", r.stackCalls)
	}

	updateStartNs.Store(0)
	start2 := time.Unix(2_000, 0)
	updateStartNs.Store(start2.UnixNano())

	checkStuckUpdate(r.cfg(start2.Add(15*time.Second), 10*time.Second, 2*time.Second))

	if r.stackCalls != 2 {
		t.Errorf("after second stuck update: stack called %d times, want 2", r.stackCalls)
	}
	if updateDumpSeqNs.Load() != start2.UnixNano() {
		t.Errorf("updateDumpSeqNs = %d, want %d", updateDumpSeqNs.Load(), start2.UnixNano())
	}
}

func TestCheckStuckUpdate_UpdateFinishedBetweenReads_Skips(t *testing.T) {
	resetWatchdogGlobals()
	start := time.Unix(1_000, 0)
	updateStartNs.Store(start.UnixNano())
	r := &fakeRecorder{stackBody: "stacks"}

	// now() is called after the initial Load and before the re-read; flipping
	// updateStartNs to 0 inside it simulates Update finishing during that window.
	cfg := watchdogConfig{
		threshold: 10 * time.Second,
		tick:      2 * time.Second,
		now: func() time.Time {
			updateStartNs.Store(0)
			return start.Add(15 * time.Second)
		},
		stack: func(buf []byte, _ bool) int {
			r.stackCalls++
			return copy(buf, r.stackBody)
		},
		logf: func(format string, args ...any) {
			r.logLines = append(r.logLines, fmt.Sprintf(format, args...))
		},
	}

	checkStuckUpdate(cfg)

	if r.stackCalls != 0 {
		t.Errorf("stack called %d times, want 0 (Update finished during race window)", r.stackCalls)
	}
	// Load-bearing: updateDumpSeqNs must NOT be advanced when the dump was
	// skipped due to race. A refactor that Store()s before the re-read would
	// break the same-start-ns dedup semantics and mask a legitimate later
	// stuck Update.
	if updateDumpSeqNs.Load() != 0 {
		t.Errorf("updateDumpSeqNs = %d, want 0 (should not be advanced when dump is skipped)", updateDumpSeqNs.Load())
	}
}

func TestMarkUpdateStart_SetsAtomic(t *testing.T) {
	resetWatchdogGlobals()
	t0 := time.Unix(42, 0)

	markUpdateStart(t0)

	if got := updateStartNs.Load(); got != t0.UnixNano() {
		t.Errorf("updateStartNs = %d, want %d", got, t0.UnixNano())
	}
}

func TestMarkUpdateEnd_ClearsAtomic(t *testing.T) {
	resetWatchdogGlobals()
	updateStartNs.Store(time.Unix(42, 0).UnixNano())

	markUpdateEnd()

	if got := updateStartNs.Load(); got != 0 {
		t.Errorf("updateStartNs = %d, want 0", got)
	}
}
