package daemon

import (
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/ringbuf"
)

// TestTrimToNewlineSafe_AdvancesPastPartialANSI proves the bug-fix: when the
// trailing-window slice begins inside an ANSI escape sequence, advance to
// the next newline so the parameter bytes never reach ansi.Strip as plain
// text. Reproduces the field-observed garbage where excerpts contained
// "2;30;30;30m" (fragment of \x1b[2;30;30;30m) at the start.
func TestTrimToNewlineSafe_AdvancesPastPartialANSI(t *testing.T) {
	// Craft a buffer where the maxTail slice would otherwise begin inside
	// an ANSI sequence. Layout: <noise to push past tail-start>\x1b[31mRED\n<rest>
	prefix := strings.Repeat("x", 100)
	// Truncate the ANSI sequence at the tail boundary deliberately.
	body := "\x1b[2;30;30;30mPARAM-LEAK\nsafe-line\n"
	raw := []byte(prefix + body)
	// maxTail = len(body) - 5 starts five bytes INTO the escape sequence —
	// past the `\x1b[` but before `m`. Mirrors the field bug shape.
	tailLen := len(body) - 5

	got := trimToNewlineSafe(raw, tailLen)
	gotStr := string(got)

	if strings.Contains(gotStr, "PARAM-LEAK") {
		t.Errorf("trimToNewlineSafe leaked the half-sequence text: %q", gotStr)
	}
	if !strings.Contains(gotStr, "safe-line") {
		t.Errorf("trimToNewlineSafe dropped the safe trailing line: %q", gotStr)
	}
}

func TestTrimToNewlineSafe_NoTrimWhenSmaller(t *testing.T) {
	raw := []byte("short data")
	got := trimToNewlineSafe(raw, 4096)
	if string(got) != "short data" {
		t.Errorf("trimToNewlineSafe small input: got %q, want %q", got, "short data")
	}
}

func TestTrimToNewlineSafe_NoNewlineFallback(t *testing.T) {
	// Pathological case: no newline in the window. We accept some leading
	// garbage rather than dropping the whole window — small loss compared
	// to silently emitting nothing.
	raw := []byte(strings.Repeat("a", 5000))
	got := trimToNewlineSafe(raw, 4096)
	if len(got) != 4096 {
		t.Errorf("trimToNewlineSafe no-newline fallback: got len=%d, want 4096", len(got))
	}
}

func TestPaneOutputExcerpt_FieldReproducesAndFixesANSILeak(t *testing.T) {
	// Reproduce the screenshot bug end-to-end. The ring buffer holds a
	// stream of bytes that exceeds 4096; the trailing slice would have
	// previously begun inside the CSI parameters and leaked them.
	pane := &Pane{OutputBuf: ringbuf.NewRingBuffer(8192)}
	pad := strings.Repeat("filler-padding-line\n", 200) // ~4 KiB of harmless padding
	tail := "\x1b[2;30;30;30mClaude prompt\n\x1b[31merror line\n"
	pane.OutputBuf.Write([]byte(pad + tail))

	got := paneOutputExcerpt(pane, 3)
	if strings.Contains(got, "2;30;30;30m") || strings.Contains(got, ";30m") {
		t.Errorf("ANSI parameter fragments leaked into excerpt: %q", got)
	}
}

func TestIsPromptOnlyExcerpt(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"empty is not prompt-only", "", false},
		{"zsh percent", "%", true},
		{"bash dollar", "$", true},
		{"powershell gt", ">", true},
		{"starship arrow", "❯", true},
		{"root hash", "#", true},
		{"agnoster arrow", "➜", true},
		{"prompt with surrounding whitespace", "  %  ", true},
		{"prompt across multiple lines", "$\n\n%\n", true},
		{"meaningful first line", "error: missing semicolon\n%", false},
		{"meaningful last line", "%\nbuilding...", false},
		{"long word", "%cargo", false},
		{"text resembling prompt then content", "%%", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isPromptOnlyExcerpt(tt.input); got != tt.want {
				t.Errorf("isPromptOnlyExcerpt(%q): got %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
