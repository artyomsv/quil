package tui

import (
	"strings"
	"testing"
)

// vtRow reads one row of the live screen as text. Trailing blanks trimmed so a
// row assertion is about content, not padding.
func vtRow(p *PaneModel, y int) string {
	var b strings.Builder
	for x := 0; x < p.vt.Width(); x++ {
		cell := p.vt.CellAt(x, y)
		if cell == nil || cell.Content == "" {
			b.WriteByte(' ')
			continue
		}
		b.WriteString(cell.Content)
	}
	return strings.TrimRight(b.String(), " ")
}

// The daemon follows a ghostsnap replay with this sequence (ghostScrollOut in
// internal/daemon/daemon.go). It is reproduced here rather than imported
// because the thing under test is what the SEQUENCE DOES TO THE GRID, which
// only the emulator can answer — and getting that wrong is what shipped twice:
// once replaying onto the visible screen, once scrolling without homing.
func scrollOutSeq(rows int) []byte {
	return []byte(strings.Repeat("\r\n", rows) + "\x1b[H")
}

const testPrompt = "PS E:/quil> "

func TestGhostScrollOut_LeavesTheChildAFreshScreenAtRowZero(t *testing.T) {
	const cols, rows = 40, 10
	p := NewPaneModel("p1", testRingBufSize)
	defer p.Dispose()
	p.ResizeVT(cols, rows)

	// A restored shell screen: some output, then a prompt with the cursor
	// parked after it — the buffer ends mid-line by construction.
	p.AppendOutput([]byte("first line\r\nsecond line\r\n" + testPrompt))
	if p.screenBlank() {
		t.Fatal("setup: the replayed screen should not be blank")
	}

	p.AppendOutput(scrollOutSeq(rows))

	// Property 1: the child gets a blank screen, so nothing of the previous
	// session can be painted through.
	if !p.screenBlank() {
		t.Errorf("screen not blank after scroll-out; row 0 = %q, row %d = %q",
			vtRow(p, 0), rows-1, vtRow(p, rows-1))
	}

	// Property 2: the child's first output lands on ROW 0. Its own model puts
	// the prompt there and it redraws with absolute positioning (CSI 1;30H),
	// so a prompt drawn anywhere else means typing appears somewhere the
	// prompt is not — the reported symptom.
	p.AppendOutput([]byte(testPrompt))
	if got := vtRow(p, 0); got != strings.TrimRight(testPrompt, " ") {
		t.Errorf("child prompt landed elsewhere; row 0 = %q", got)
	}

	// Property 3: an absolute redraw to row 1 (1-based) reaches the same row
	// the prompt is on — this is the exact sequence PSReadLine emits.
	p.AppendOutput([]byte("\x1b[1;13Hls -la"))
	if got, want := vtRow(p, 0), testPrompt+"ls -la"; got != want {
		t.Errorf("row 0 = %q, want %q — the typed text belongs on the prompt's own row", got, want)
	}
}

// Without the HOME the cursor is left on the bottom row, so the child's prompt
// is drawn there while its absolute redraws still go to row 1 — the prompt at
// the bottom of the pane, typing at the top.
func TestGhostScrollOut_WithoutHomeSplitsPromptFromInput(t *testing.T) {
	const cols, rows = 40, 10
	p := NewPaneModel("p1", testRingBufSize)
	defer p.Dispose()
	p.ResizeVT(cols, rows)
	p.AppendOutput([]byte("first line\r\n" + testPrompt))

	p.AppendOutput([]byte(strings.Repeat("\r\n", rows))) // scroll, no home
	p.AppendOutput([]byte(testPrompt))
	p.AppendOutput([]byte("\x1b[1;13Hls -la"))

	if got, want := vtRow(p, 0), testPrompt+"ls -la"; got == want {
		t.Fatal("prompt and input share row 0 without the home; this test no " +
			"longer models the bug and the home may have become unnecessary")
	}
	if got := vtRow(p, 0); !strings.Contains(got, "ls -la") {
		t.Errorf("row 0 = %q, want the stranded input the home exists to prevent", got)
	}
	if got := vtRow(p, rows-1); !strings.Contains(got, "PS E:/quil>") {
		t.Errorf("bottom row = %q, want the prompt stranded there", got)
	}
}
