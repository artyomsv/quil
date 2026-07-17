package main

import (
	"testing"
	"time"
)

func TestParentHandleTrustworthy(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name          string
		parentCreated time.Time
		selfCreated   time.Time
		want          bool
	}{
		// The original parent necessarily existed before (or at the same
		// coarse-clock tick as) the child it spawned.
		{"parent older than child", base.Add(-time.Minute), base, true},
		{"same creation tick", base, base, true},
		// A process created AFTER us that wears our recorded parent PID is
		// a PID-reuse impostor: the real parent is gone, so the watchdog
		// must treat the parent as already dead instead of waiting on a
		// stranger that may never exit.
		{"parent newer than child (PID reuse)", base.Add(time.Second), base, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := parentHandleTrustworthy(tt.parentCreated, tt.selfCreated); got != tt.want {
				t.Errorf("parentHandleTrustworthy(%v, %v) = %v, want %v",
					tt.parentCreated, tt.selfCreated, got, tt.want)
			}
		})
	}
}
