package tui

import "testing"

// Scrollback depth is per-pane and every pane carries its own VT emulator,
// visible or not — so the figure multiplies by pane count. Measured in
// production 2026-08-18: 37 panes at the hardcoded 10 000 lines put the TUI at
// 1.13 GB resident.
//
// The default is deliberately UNCHANGED. Nobody's scrollback shrinks on
// upgrade; this exists so a user running dozens of panes on a memory-tight
// machine can trade depth for footprint, which was previously impossible at
// any price.
func TestScrollbackLines_DefaultMatchesHistoricalHardcodedDepth(t *testing.T) {
	if got := scrollbackLines(); got != defaultScrollbackLines {
		t.Errorf("scrollbackLines() = %d, want %d — the shipped default must not "+
			"change, or every existing install silently loses history on upgrade",
			got, defaultScrollbackLines)
	}
	if defaultScrollbackLines != 10000 {
		t.Errorf("defaultScrollbackLines = %d, want 10000 — this was the hardcoded "+
			"value before it became configurable", defaultScrollbackLines)
	}
}

func TestSetScrollbackLines_AppliesToNewPanes(t *testing.T) {
	orig := scrollbackLines()
	t.Cleanup(func() { SetScrollbackLines(orig) })

	SetScrollbackLines(500)
	if got := scrollbackLines(); got != 500 {
		t.Fatalf("scrollbackLines() = %d, want 500", got)
	}

	// The setting is only real if it reaches the emulator a pane is built with.
	p := NewPaneModel("pane-scrollback", 1024)
	t.Cleanup(p.Dispose)
	for i := 0; i < 700; i++ {
		p.AppendOutput([]byte("line of scrollback content\r\n"))
	}
	// A BAND, not just an upper bound. `sb > 500` alone is satisfied by zero,
	// which is precisely the "pane with no history at all" outcome the clamp
	// below exists to prevent — so the obvious assertion passes hardest on the
	// worst failure.
	sb := p.vt.ScrollbackLen()
	if sb > 500 {
		t.Errorf("pane retained %d scrollback lines with a 500 cap — the "+
			"configured depth never reached the emulator", sb)
	}
	if sb == 0 {
		t.Error("pane retained NO scrollback after 700 lines — the configured depth " +
			"reached the emulator as zero, which is worse than ignoring it")
	}
}

// Zero means "unset" in TOML, and a caller passing junk must not produce a pane
// with no history at all (or a negative allocation).
func TestSetScrollbackLines_RejectsUnsetAndNonsense(t *testing.T) {
	orig := scrollbackLines()
	t.Cleanup(func() { SetScrollbackLines(orig) })

	for _, n := range []int{0, -1, -10000} {
		SetScrollbackLines(n)
		if got := scrollbackLines(); got != defaultScrollbackLines {
			t.Errorf("SetScrollbackLines(%d) = %d, want the default %d — an unset "+
				"or nonsensical config value must fall back, not disable scrollback",
				n, got, defaultScrollbackLines)
		}
	}
}
