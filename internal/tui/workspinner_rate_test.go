package tui

import (
	"testing"
	"time"
)

// The work spinner's cadence is a cost decision as much as a visual one, and
// nothing else in the package records that.
//
// Bubble Tea calls model.View() once per message (its FPS option throttles the
// terminal flush, not the View construction), so every spinner tick costs a
// whole frame — and a frame is ~2.8 ms at 37 tabs, 73% of it lipgloss measuring
// grapheme widths on the composed screen. Production measurement 2026-08-18:
// the spinner was 50 of every ~77 messages in a 5 s window, i.e. ~65% of ALL
// renders, and it never idles while any agent is mid-turn.
//
// Coalescing cannot recover those frames the way it recovers the others: a
// spinner tick genuinely changes what is on screen. The rate itself is the only
// lever, which is why it gets a bound rather than a comment.
//
// Both directions matter, so both are asserted:
//   - too fast burns the render loop for an indicator nobody is staring at
//   - too slow stops reading as motion, and motion is the whole point (every
//     other sidebar state — ▲ ✓ ◆ ○ — describes something finished)
func TestWorkSpinner_RateBalancesMotionAgainstRenderCost(t *testing.T) {
	t.Parallel()

	const (
		minInterval = 150 * time.Millisecond
		maxCycle    = 2500 * time.Millisecond
	)

	if workSpinnerInterval < minInterval {
		t.Errorf("workSpinnerInterval = %s, must be >= %s — every tick is a full "+
			"View() at ~2.8 ms on a large workspace, and the spinner is the single "+
			"largest source of renders",
			workSpinnerInterval, minInterval)
	}

	cycle := workSpinnerInterval * time.Duration(len(spinnerFrames))
	if cycle > maxCycle {
		t.Errorf("full spinner cycle = %s (%s x %d frames), must be <= %s — slower "+
			"than this stops reading as motion, which is what distinguishes "+
			"'working' from every finished state in the sidebar's vocabulary",
			cycle, workSpinnerInterval, len(spinnerFrames), maxCycle)
	}
}
