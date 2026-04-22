package tui

import (
	"log"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// Watchdog state is package-level by deliberate choice. A single quil TUI
// process hosts exactly one Bubble Tea Model (the binary's entry point in
// cmd/quil/main.go runs one tea.Program) and therefore exactly one Update
// call site whose wedge we want to detect. The sync.Once below guarantees
// only one watchdog goroutine is ever started. If a future refactor makes
// this package host multiple concurrent Models in one process, migrate
// these to a per-Model struct; until then, globals are the simplest
// correct shape and make markUpdateStart/markUpdateEnd callable from
// Model's value receiver without propagating a pointer into every Update
// invocation.

// updateStartNs records the unix-nano time at which the current Update
// call started, or 0 when no Update is in flight. Written by Update's
// entry/defer on the Bubble Tea program goroutine; read by the watchdog
// goroutine. Atomic so reads are race-free without a mutex.
var updateStartNs atomic.Int64

// updateDumpSeqNs memoizes the updateStartNs value of the Update the
// watchdog has already dumped, so it does not re-dump every tick while a
// single Update stays wedged. Reset implicitly when Update finishes and a
// new one starts with a different start-ns.
var updateDumpSeqNs atomic.Int64

// watchdogOnce guards against double-starting the watchdog goroutine if
// Init() is ever called twice in a process's lifetime. Consequence: the
// watchdog cannot be re-initialized after shutdown — acceptable for a
// process-lifetime diagnostic and matches the current binary's lifecycle.
var watchdogOnce sync.Once

// stackBufPool reuses the 1 MiB buffer passed to runtime.Stack so a wedge
// that persists for many ticks doesn't allocate repeatedly. Note: only one
// dump fires per stuck Update thanks to updateDumpSeqNs memoization; the
// pool is defensive in case that invariant is ever relaxed.
var stackBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 1<<20)
		return &b
	},
}

// watchdogConfig controls a single runUpdateWatchdog invocation. Exposed
// as a struct so tests can inject a fake clock and stack/log collectors.
type watchdogConfig struct {
	threshold time.Duration
	tick      time.Duration
	now       func() time.Time
	stack     func(buf []byte, all bool) int
	logf      func(format string, args ...any)
}

func defaultWatchdogConfig() watchdogConfig {
	return watchdogConfig{
		threshold: 10 * time.Second,
		tick:      2 * time.Second,
		now:       time.Now,
		stack:     runtime.Stack,
		logf:      log.Printf,
	}
}

// startUpdateWatchdog launches the watchdog goroutine exactly once per
// process. It has no shutdown path by design — the watchdog's lifetime is
// the process lifetime, and the OS reaps it on exit. Calling twice is a
// no-op (sync.Once).
func startUpdateWatchdog(cfg watchdogConfig) {
	watchdogOnce.Do(func() {
		go runUpdateWatchdog(cfg)
	})
}

func runUpdateWatchdog(cfg watchdogConfig) {
	t := time.NewTicker(cfg.tick)
	defer t.Stop()
	for range t.C {
		checkStuckUpdate(cfg)
	}
}

// checkStuckUpdate is the per-tick check. Extracted so tests can drive it
// directly instead of spinning a real ticker.
//
// NOTE: the re-read at the second Load is load-bearing — it protects against
// the Update finishing between the first Load and here, which would otherwise
// produce a spurious stack dump. See
// TestCheckStuckUpdate_UpdateFinishedBetweenReads_Skips.
func checkStuckUpdate(cfg watchdogConfig) {
	startNs := updateStartNs.Load()
	if startNs == 0 {
		return
	}
	elapsed := cfg.now().UnixNano() - startNs
	if time.Duration(elapsed) < cfg.threshold {
		return
	}
	if updateStartNs.Load() != startNs {
		return
	}
	if updateDumpSeqNs.Load() == startNs {
		return
	}
	updateDumpSeqNs.Store(startNs)
	bufPtr := stackBufPool.Get().(*[]byte)
	defer stackBufPool.Put(bufPtr)
	buf := *bufPtr
	n := cfg.stack(buf, true)
	cfg.logf("watchdog: Update stuck for %s, dumping goroutines", time.Duration(elapsed))
	cfg.logf("watchdog: stacks:\n%s", buf[:n])
}

// markUpdateStart is called at the top of Update to record the in-flight
// start time. Paired with markUpdateEnd in Update's defer.
func markUpdateStart(t time.Time) {
	updateStartNs.Store(t.UnixNano())
}

// markUpdateEnd is called in Update's defer to signal the watchdog that
// the call returned cleanly.
func markUpdateEnd() {
	updateStartNs.Store(0)
}
