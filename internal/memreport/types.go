package memreport

import "time"

// PaneMem is a single pane's daemon-side memory accounting. TUI-side bytes
// (VT grid, notes editor) are computed by the TUI at render time and are not
// part of this struct.
type PaneMem struct {
	PaneID      string
	TabID       string
	GoHeapBytes uint64
	PTYRSSBytes uint64
	Total       uint64 // GoHeapBytes + PTYRSSBytes
}

// Snapshot is the collector's output, refreshed every ~5 s. Readers must
// treat it as immutable — the collector replaces the pointer atomically
// rather than mutating in place.
type Snapshot struct {
	At    time.Time
	Panes []PaneMem // sorted by Total desc
	Total uint64    // sum of Panes[*].Total
}
