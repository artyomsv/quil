package main

import (
	"runtime"
	"testing"
)

// After an in-session update the old process does not exit: respawnSelf blocks
// on cmd.Run() for the whole life of the new TUI, holding the console so the
// shell does not print a prompt over it. That process is the one that just ran
// a full session, so it parks still holding its entire heap — 37 panes' worth
// of VT emulators and scrollback.
//
// Measured in production 2026-08-18: three such wrappers (one per in-session
// update) holding 326 MB, 436 MB and 16 MB. They are NOT a permanent leak —
// each exits when the TUI below it does, cascading back to the shell — but they
// hold that memory for the whole session, and the chain grows by one per update.
//
// releaseHeapBeforePark is what makes parking cheap. The scope change at the
// call site is the other half and cannot be unit-tested: it is the absence of a
// live reference, which the compiler and GC arbitrate, not an assertion. This
// test covers the part that can be pinned — that the helper actually returns
// memory to the OS rather than merely running a GC.
func TestReleaseHeapBeforePark_ReturnsMemoryToTheOS(t *testing.T) {
	// Build a heap roughly the shape of a parked wrapper's: many live
	// allocations, then all dropped.
	blocks := make([][]byte, 0, 256)
	for i := 0; i < 256; i++ {
		b := make([]byte, 256*1024) // 64 MB total
		for j := 0; j < len(b); j += 4096 {
			b[j] = byte(i) // touch pages so they are really resident
		}
		blocks = append(blocks, b)
	}
	var afterAlloc runtime.MemStats
	runtime.ReadMemStats(&afterAlloc)

	blocks = nil
	_ = blocks

	releaseHeapBeforePark()

	var afterRelease runtime.MemStats
	runtime.ReadMemStats(&afterRelease)

	if afterRelease.HeapAlloc >= afterAlloc.HeapAlloc {
		t.Errorf("HeapAlloc did not fall: %d -> %d; the dropped blocks were not collected",
			afterAlloc.HeapAlloc, afterRelease.HeapAlloc)
	}
	// The point is returning pages to the OS, not just collecting them. A plain
	// runtime.GC() would satisfy the check above and still leave the wrapper's
	// RSS untouched, which is the entire failure being fixed.
	if afterRelease.HeapReleased <= afterAlloc.HeapReleased {
		t.Errorf("HeapReleased did not grow: %d -> %d; memory was collected but "+
			"not handed back, so the parked wrapper's RSS would not shrink",
			afterAlloc.HeapReleased, afterRelease.HeapReleased)
	}
}
