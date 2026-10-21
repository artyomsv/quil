package main

import (
	"reflect"
	"runtime"
	"strings"
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

// #268: after an in-session update the new TUI must REPLACE this process, not
// run beside it as a child. Driven through respawnSelf so a regression to the
// wrapper shape (exec never attempted) fails here, not only on a Mac.
func TestRespawnSelf_ExecsInPlace_NoChildSpawned(t *testing.T) {
	var gotExe string
	var gotArgs, gotEnv []string
	calls := 0
	orig := execSelfFn
	execSelfFn = func(exe string, args, env []string) error {
		calls++
		gotExe, gotArgs, gotEnv = exe, args, env
		return nil // a real successful exec never returns
	}
	t.Cleanup(func() { execSelfFn = orig })

	// A path that cannot run: if respawnSelf fell through to the child spawn
	// despite the exec succeeding, Start would fail and print to stderr, but
	// the call count below is what pins it.
	const exe = "/nonexistent/quil-268"
	if !respawnSelf(exe) {
		t.Fatal("respawnSelf returned false; the swap is done, so it must always return true")
	}
	if calls != 1 {
		t.Fatalf("execSelfFn called %d times, want 1", calls)
	}
	if gotExe != exe {
		t.Errorf("exec path = %q, want %q", gotExe, exe)
	}
	if !reflect.DeepEqual(gotArgs, respawnArgs()) {
		t.Errorf("exec args = %q, want %q", gotArgs, respawnArgs())
	}
	if n := countRestartFlag(gotEnv); n != 1 {
		t.Errorf("exec env carries QUIL_UPDATE_RESTART %d times, want 1", n)
	}
}

func TestRespawnEnv_ReplacesInheritedRestartFlag(t *testing.T) {
	for _, inherited := range []string{"1", "0", ""} {
		t.Run("inherited="+inherited, func(t *testing.T) {
			t.Setenv("QUIL_UPDATE_RESTART", inherited)
			env := respawnEnv()
			if n := countRestartFlag(env); n != 1 {
				t.Fatalf("QUIL_UPDATE_RESTART appears %d times, want 1: a second "+
					"update in one session would pass duplicates to execve", n)
			}
			if last := env[len(env)-1]; last != "QUIL_UPDATE_RESTART=1" {
				t.Errorf("last entry = %q, want QUIL_UPDATE_RESTART=1", last)
			}
		})
	}
}

func countRestartFlag(env []string) int {
	n := 0
	for _, kv := range env {
		if strings.HasPrefix(kv, "QUIL_UPDATE_RESTART=") {
			n++
		}
	}
	return n
}
