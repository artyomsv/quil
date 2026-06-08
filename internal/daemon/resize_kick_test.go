package daemon

import (
	"reflect"
	"testing"
)

// Windows ConPTY delivers resizes as WINDOW_BUFFER_SIZE_EVENTs in the
// child's console input queue. Events fired before the child reads input
// (claude/node mid-boot) are dropped and never replayed — the TUI's
// initial resize_pane lands ~25 ms after spawn and can be lost, leaving
// the child at the spawn-time 80x24. resizeKick re-applies the size on
// the pane's first output, with a 1-column jiggle so a size-change event
// fires even if the buffer dimensions already match.

func TestResizeKick_ReappliesSizeWithJiggle(t *testing.T) {
	fake := &fakeSession{}
	resizeKick(fake, 238, 45)

	want := [][2]uint16{{45, 237}, {45, 238}}
	if !reflect.DeepEqual(fake.resizes, want) {
		t.Errorf("resizes = %v, want %v (jiggle then real size)", fake.resizes, want)
	}
}

func TestResizeKick_UnknownSizeIsNoOp(t *testing.T) {
	for _, dims := range [][2]int{{0, 0}, {0, 45}, {238, 0}} {
		fake := &fakeSession{}
		resizeKick(fake, dims[0], dims[1])
		if len(fake.resizes) != 0 {
			t.Errorf("cols=%d rows=%d: expected no resizes, got %v", dims[0], dims[1], fake.resizes)
		}
	}
}

func TestResizeKick_SingleColumnSkipsJiggle(t *testing.T) {
	fake := &fakeSession{}
	resizeKick(fake, 1, 45)

	want := [][2]uint16{{45, 1}}
	if !reflect.DeepEqual(fake.resizes, want) {
		t.Errorf("resizes = %v, want %v (no zero-width jiggle)", fake.resizes, want)
	}
}
