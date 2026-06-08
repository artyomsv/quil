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
	t.Parallel()
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
	t.Parallel()
	raw := []byte("short data")
	got := trimToNewlineSafe(raw, 4096)
	if string(got) != "short data" {
		t.Errorf("trimToNewlineSafe small input: got %q, want %q", got, "short data")
	}
}

func TestTrimToNewlineSafe_NoNewlineFallback(t *testing.T) {
	t.Parallel()
	// Pathological case: no newline AND no ESC in the bounded scan
	// window. We accept some leading garbage rather than dropping the
	// whole window — small loss compared to silently emitting nothing.
	raw := []byte(strings.Repeat("a", 5000))
	got := trimToNewlineSafe(raw, 4096)
	if len(got) != 4096 {
		t.Errorf("trimToNewlineSafe no-boundary fallback: got len=%d, want 4096", len(got))
	}
}

// TestTrimToNewlineSafe_SeeksESCWhenNoNewline reproduces the field bug shape:
// Claude TUI emits one big screen paint with very few newlines, so the
// newline-only seek falls through and leaves leading ANSI parameter bytes
// in the slice. The hardened version also recognises ESC bytes as a clean
// boundary because ansi.Strip handles full sequences from there.
func TestTrimToNewlineSafe_SeeksESCWhenNoNewline(t *testing.T) {
	t.Parallel()
	// 100 bytes of CSI parameter garbage (no newline, no ESC), then an ESC
	// starting a fresh sequence + real content.
	prefix := strings.Repeat("a", 100)
	tail := "2;30;30;30m garbage chars no newline " + "\x1b[31mreal content here"
	raw := []byte(prefix + tail)
	tailLen := len(tail) - 5 // boundary lands inside the garbage prefix

	got := trimToNewlineSafe(raw, tailLen)
	gotStr := string(got)

	if strings.Contains(gotStr, "garbage chars") {
		t.Errorf("trimToNewlineSafe should have advanced past garbage to the ESC: %q", gotStr)
	}
	if !strings.Contains(gotStr, "real content here") {
		t.Errorf("trimToNewlineSafe should have preserved the post-ESC content: %q", gotStr)
	}
	if got[0] != 0x1b {
		t.Errorf("first byte after seek should be ESC; got 0x%02x", got[0])
	}
}

// TestTrimToNewlineSafe_NewlineBeatsESCWhenFirst confirms whichever boundary
// comes first wins (newline before ESC in this case).
func TestTrimToNewlineSafe_NewlineBeatsESCWhenFirst(t *testing.T) {
	t.Parallel()
	prefix := strings.Repeat("a", 100)
	tail := "garbage\nline-after-newline\x1b[31mansi-later"
	raw := []byte(prefix + tail)
	tailLen := len(tail) - 5

	got := trimToNewlineSafe(raw, tailLen)
	gotStr := string(got)

	if strings.Contains(gotStr, "garbage") {
		t.Errorf("trimToNewlineSafe should have advanced past the newline: %q", gotStr)
	}
	if !strings.HasPrefix(gotStr, "line-after-newline") {
		t.Errorf("trimToNewlineSafe should start at next line, not skip to ESC: %q", gotStr)
	}
}

// TestTrimToNewlineSafe_ScanBoundary asserts that the scan stops at maxScan
// bytes — far-away boundaries don't drag the whole window forward.
func TestTrimToNewlineSafe_ScanBoundary(t *testing.T) {
	t.Parallel()
	// 2 KiB of plain text (no boundary), then a newline. Scan window is
	// 512 bytes, so the seek must not find the newline and must return
	// the un-advanced slice.
	prefix := strings.Repeat("a", 100)
	pad := strings.Repeat("b", 2000)
	tail := pad + "\nafter-far-newline"
	raw := []byte(prefix + tail)
	tailLen := len(tail) - 50

	got := trimToNewlineSafe(raw, tailLen)
	if len(got) != tailLen {
		t.Errorf("scan should not advance past maxScan; got len=%d, want %d", len(got), tailLen)
	}
}

func TestPaneOutputExcerpt_FieldReproducesAndFixesANSILeak(t *testing.T) {
	t.Parallel()
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

// TestLastNLines_AppliesCarriageReturnReset proves that the field-observed
// "%   ...   \r \r\ruser@host" excerpt collapses to just the post-CR
// content (what the terminal would actually display), not the prompt rune
// that was immediately overwritten.
func TestLastNLines_AppliesCarriageReturnReset(t *testing.T) {
	t.Parallel()
	in := "%                                          \r \r\ruser_name@host01: /path"
	got := lastNLines(in, 5)
	want := "user_name@host01: /path"
	if got != want {
		t.Errorf("lastNLines CR-reset: got %q, want %q", got, want)
	}
}

// TestIsPromptLikeLine_EmptyString_DirectCall pins the explicit guard at the
// top of isPromptLikeLine. The function returns true for an empty string so
// that multi-line prompt-only excerpts with blank lines between prompts
// continue to classify correctly. The isPromptOnlyExcerpt level filters
// blank lines before dispatch, so this branch only matters if a future
// caller invokes isPromptLikeLine directly with "".
func TestIsPromptLikeLine_EmptyString_DirectCall(t *testing.T) {
	t.Parallel()
	if !isPromptLikeLine("") {
		t.Errorf("isPromptLikeLine(\"\") = false, want true (empty lines are vacuously prompt-like)")
	}
}

func TestLastNLines_NoCarriageReturnUntouched(t *testing.T) {
	t.Parallel()
	in := "line one\nline two\nline three"
	got := lastNLines(in, 5)
	want := "line one\nline two\nline three"
	if got != want {
		t.Errorf("lastNLines no-CR: got %q, want %q", got, want)
	}
}

func TestIsPromptOnlyExcerpt(t *testing.T) {
	t.Parallel()
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
		// Suffix-rune classifier: "%cargo" ends in "o" → not prompt.
		{"prompt rune as prefix of a word", "%cargo", false},
		// "%%" ends with "%" but the char before is also "%", not whitespace
		// — H1 fix rejects it. Realistic shells don't emit "%%" as a prompt.
		{"repeated prompt rune", "%%", false},
		// H1 regression: percentage-of-number must NOT be classified as
		// prompt-like. The `%` is preceded by a digit, not whitespace.
		{"percentage of number", "build complete: 100%", false},
		// H1 sanity check: prompt rune preceded by a space IS a real prompt.
		{"prompt rune after path", "~/projects/quil %", true},
		// OSC 0 window-title leak shape — must suppress.
		{"hostname leak", "user_name@host01: /Users/me/proj", true},
		// Empty / whitespace-only lines must be treated as prompt-like so
		// "$\n\n%" (prompt-blank-prompt) still classifies as prompt-only.
		{"empty line classified as prompt-like", "", false}, // empty EXCERPT is false (early return)
		{"whitespace-only excerpt", "   \n\t\n", false},
		// `ls` output should NOT be classified as prompt material.
		{"ls output", "LICENSE\tcmd\tgo.sum", false},
		{"long line with prompt rune still emits", strings.Repeat("a", 250) + "%", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isPromptOnlyExcerpt(tt.input); got != tt.want {
				t.Errorf("isPromptOnlyExcerpt(%q): got %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
