package panehistory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// syntheticSample mirrors the <task-notification> block Claude Code injects as a
// synthetic UserPromptSubmit when a background Task/subagent finishes — the
// machine-generated "trash" that must never appear in user input history.
const syntheticSample = "<task-notification>\n" +
	"<task-id>a6dd63b896dd3ae5f</task-id>\n" +
	"<tool-use-id>toolu_01EpT6QV91j5eWigUTc8HE5V</tool-use-id>\n" +
	"<summary>Agent finished</summary>\n" +
	"</task-notification>"

// agentMessageSample mirrors the <agent-message> block delivered as a
// UserPromptSubmit when a subagent reports back to its parent — same class of
// machine-generated turn as syntheticSample, and the one users actually saw
// filling their history list.
const agentMessageSample = `<agent-message from="impl-final">` + "\n" +
	"STATUS: Done. Config block corrected, committed on top of 3a1f24f95e\n" +
	"</agent-message>"

// writeRawLine appends one entry directly to the pane file, bypassing Append's
// filters — used to seed pre-existing synthetic junk that predates the fix.
func writeRawLine(t *testing.T, dir, paneID string, e Entry) {
	t.Helper()
	if err := os.MkdirAll(Dir(dir), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	line, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	f, err := os.OpenFile(Path(dir, paneID), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		t.Fatalf("write: %v", err)
	}
	f.Close()
}

func TestIsSyntheticPrompt_MatchesKnownTags(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		text string
		want bool
	}{
		{"task notification", syntheticSample, true},
		{"leading whitespace before tag", "\n\t  " + syntheticSample, true},
		{"tag with attribute", `<task-notification version="2">x</task-notification>`, true},
		{"agent message", agentMessageSample, true},
		{"agent message trailing whitespace", agentMessageSample + "\n\n", true},
		// A turn can carry several concatenated reports; the first opens and the
		// last closes, so the both-ends rule still classifies it.
		{"two agent messages in one turn", agentMessageSample + "\n" + agentMessageSample, true},
		// Mixed kinds in one turn — the realistic parallel-subagent case. The
		// ends must be matched independently, not as a pair, or this slips
		// through: task-notification's open matches but not its close, and
		// agent-message's close matches but not its open.
		{"task notification then agent message", syntheticSample + "\n" + agentMessageSample, true},
		{"agent message then task notification", agentMessageSample + "\n" + syntheticSample, true},
		{"real prompt", "fix the input history bug", false},
		{"empty", "", false},
		{"tag mentioned mid-sentence", "why does <task-notification> appear?", false},
		{"agent message tag mid-sentence", "what is an <agent-message> block?", false},
		// Opens with the tag but is not a complete block — a user quoting the
		// tag in a real question must be preserved (requires the close tag too).
		{"open tag without close", "<task-notification is confusing, what is it?", false},
		{"agent message open without close", "<agent-message keeps showing up, why?", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsSyntheticPrompt(tt.text); got != tt.want {
				t.Fatalf("IsSyntheticPrompt(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

func TestAppend_SkipsSyntheticPrompt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := Append(dir, "pane-syn", Entry{TsMs: 1, Text: syntheticSample}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, _ := Read(dir, "pane-syn")
	if len(got) != 0 {
		t.Fatalf("synthetic prompt was recorded: %+v", got)
	}
}

func TestRead_FiltersPreexistingSyntheticEntries(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Two interspersed synthetic entries to guard against an off-by-one in the
	// filter accumulation loop.
	writeRawLine(t, dir, "pane-mix", Entry{V: 1, TsMs: 1, Text: "real one"})
	writeRawLine(t, dir, "pane-mix", Entry{V: 1, TsMs: 2, Text: syntheticSample})
	writeRawLine(t, dir, "pane-mix", Entry{V: 1, TsMs: 3, Text: "real two"})
	writeRawLine(t, dir, "pane-mix", Entry{V: 1, TsMs: 4, Text: syntheticSample})
	writeRawLine(t, dir, "pane-mix", Entry{V: 1, TsMs: 5, Text: "real three"})

	got, err := Read(dir, "pane-mix")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 3 || got[0].Text != "real one" || got[1].Text != "real two" || got[2].Text != "real three" {
		t.Fatalf("synthetic entries not filtered from Read: %+v", got)
	}
}

func TestRead_AllSynthetic_ReturnsEmpty(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeRawLine(t, dir, "pane-allsyn", Entry{V: 1, TsMs: 1, Text: syntheticSample})
	writeRawLine(t, dir, "pane-allsyn", Entry{V: 1, TsMs: 2, Text: syntheticSample})

	got, err := Read(dir, "pane-allsyn")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("all-synthetic file should read back with no entries, got %+v", got)
	}
}

// TestCompact_UnderCap_LeavesDiskUntouched pins the race-avoidance property: at
// or under the ring cap Compact must NOT rewrite the file (a rewrite would race
// the Claude hook's cross-process append). Read still hides the junk from
// callers, so the display is clean even though the bytes remain on disk.
func TestCompact_UnderCap_LeavesDiskUntouched(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeRawLine(t, dir, "pane-under", Entry{V: 1, TsMs: 1, Text: "real"})
	writeRawLine(t, dir, "pane-under", Entry{V: 1, TsMs: 2, Text: syntheticSample})

	before, err := os.ReadFile(Path(dir, "pane-under"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if err := Compact(dir, "pane-under", MaxEntries); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	after, err := os.ReadFile(Path(dir, "pane-under"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("under-cap Compact rewrote the file; before/after differ")
	}
	// Read still hides the synthetic entry from callers.
	got, _ := Read(dir, "pane-under")
	if len(got) != 1 || got[0].Text != "real" {
		t.Fatalf("Read did not hide synthetic entry: %+v", got)
	}
}

// TestCompact_ManySyntheticFewReal_LeavesDiskUntouched is the regression guard
// for the gate: the raw line count exceeds keepLast because of pre-existing
// synthetic junk, but the real-entry count does not. Compact must NOT rewrite
// (which would race the hook's append); the junk is left on disk and hidden by
// Read.
func TestCompact_ManySyntheticFewReal_LeavesDiskUntouched(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeRawLine(t, dir, "pane-junky", Entry{V: 1, TsMs: 1, Text: "real"})
	for i := 2; i <= 5; i++ { // 4 synthetic => 5 raw lines, over keepLast=2
		writeRawLine(t, dir, "pane-junky", Entry{V: 1, TsMs: int64(i), Text: syntheticSample})
	}

	before, err := os.ReadFile(Path(dir, "pane-junky"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if err := Compact(dir, "pane-junky", 2); err != nil { // raw=5 > 2, but real=1 <= 2
		t.Fatalf("Compact: %v", err)
	}
	after, err := os.ReadFile(Path(dir, "pane-junky"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("Compact rewrote despite real entries within cap (append-race risk)")
	}
	got, _ := Read(dir, "pane-junky")
	if len(got) != 1 || got[0].Text != "real" {
		t.Fatalf("Read did not hide synthetic entries: %+v", got)
	}
}

// TestCompact_OverCap_PurgesSyntheticAndTrims exercises both removal paths in a
// single call: the file exceeds keepLast (triggering a rewrite), and synthetic
// junk is dropped while the last keepLast real entries are retained.
func TestCompact_OverCap_PurgesSyntheticAndTrims(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// 5 real interleaved with 2 synthetic => 7 raw lines, over a keepLast of 2.
	writeRawLine(t, dir, "pane-over", Entry{V: 1, TsMs: 1, Text: "r1"})
	writeRawLine(t, dir, "pane-over", Entry{V: 1, TsMs: 2, Text: syntheticSample})
	writeRawLine(t, dir, "pane-over", Entry{V: 1, TsMs: 3, Text: "r2"})
	writeRawLine(t, dir, "pane-over", Entry{V: 1, TsMs: 4, Text: "r3"})
	writeRawLine(t, dir, "pane-over", Entry{V: 1, TsMs: 5, Text: syntheticSample})
	writeRawLine(t, dir, "pane-over", Entry{V: 1, TsMs: 6, Text: "r4"})
	writeRawLine(t, dir, "pane-over", Entry{V: 1, TsMs: 7, Text: "r5"})

	if err := Compact(dir, "pane-over", 2); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	raw, err := os.ReadFile(Path(dir, "pane-over"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if strings.Contains(string(raw), "task-notification") {
		t.Fatalf("synthetic entry still on disk after over-cap Compact:\n%s", raw)
	}
	got, _ := Read(dir, "pane-over")
	if len(got) != 2 || got[0].Text != "r4" || got[1].Text != "r5" {
		t.Fatalf("want last 2 real entries [r4 r5], got %+v", got)
	}
}

func TestCompact_NoOpOnMissingFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := Compact(dir, "pane-never", MaxEntries); err != nil {
		t.Fatalf("Compact on missing file: %v", err)
	}
	if _, err := os.Stat(Path(dir, "pane-never")); !os.IsNotExist(err) {
		t.Fatalf("Compact created a file for a never-written pane")
	}
}

func TestAppend_WritesEntry_ReadBack(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := Append(dir, "pane-abc", Entry{TsMs: 100, SessionID: "s1", Text: "hello"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := Read(dir, "pane-abc")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 1 || got[0].Text != "hello" || got[0].TsMs != 100 || got[0].V != 1 {
		t.Fatalf("unexpected entries: %+v", got)
	}
}

func TestAppend_SkipsEmptyAndWhitespace(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, s := range []string{"", "   ", "\n\t "} {
		if err := Append(dir, "pane-x", Entry{TsMs: 1, Text: s}); err != nil {
			t.Fatalf("Append(%q): %v", s, err)
		}
	}
	got, _ := Read(dir, "pane-x")
	if len(got) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(got))
	}
}

func TestAppend_OversizeText_CappedWithMarker(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	big := strings.Repeat("a", MaxEntryBytes+5000)
	if err := Append(dir, "pane-y", Entry{TsMs: 1, Text: big}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, _ := Read(dir, "pane-y")
	if len(got) != 1 {
		t.Fatalf("want 1 entry, got %d", len(got))
	}
	if len(got[0].Text) > MaxEntryBytes {
		t.Fatalf("text not capped: %d bytes", len(got[0].Text))
	}
	if !strings.HasSuffix(got[0].Text, "…[truncated]") {
		t.Fatalf("missing truncation marker")
	}
}

func TestAppend_OversizeMultiByteText_ValidUTF8WithMarker(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// All multi-byte runes so truncation must land on a rune boundary.
	big := strings.Repeat("日", MaxEntryBytes) // 3 bytes each, far over the cap
	if err := Append(dir, "pane-mb", Entry{TsMs: 1, Text: big}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, _ := Read(dir, "pane-mb")
	if len(got) != 1 {
		t.Fatalf("want 1 entry, got %d", len(got))
	}
	if len(got[0].Text) > MaxEntryBytes {
		t.Fatalf("text not capped: %d bytes", len(got[0].Text))
	}
	if !utf8.ValidString(got[0].Text) {
		t.Fatalf("truncation produced invalid UTF-8")
	}
	if !strings.HasSuffix(got[0].Text, "…[truncated]") {
		t.Fatalf("missing truncation marker")
	}
}

func TestRead_SkipsTrailingPartialLine(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := Append(dir, "pane-p", Entry{TsMs: 1, Text: "first"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	f, err := os.OpenFile(Path(dir, "pane-p"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := f.WriteString(`{"v":1,"ts_ms":2,"text":"partia`); err != nil {
		t.Fatalf("write: %v", err)
	}
	f.Close()

	got, err := Read(dir, "pane-p")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 1 || got[0].Text != "first" {
		t.Fatalf("partial line not skipped: %+v", got)
	}
}

func TestRead_MissingFile_NoError(t *testing.T) {
	t.Parallel()
	got, err := Read(t.TempDir(), "pane-none")
	if err != nil || got != nil {
		t.Fatalf("want nil,nil; got %+v, %v", got, err)
	}
}

// TestRead_OversizeFile_ReadsTailOnly verifies the memory guard: a file larger
// than maxReadBytes is read from the tail, dropping the oldest entries and
// keeping the newest. NOT parallel — it mutates the package-level maxReadBytes;
// Go runs non-parallel tests to completion before parallel ones resume, and the
// defer restores the value, so no parallel sibling observes the mutation.
func TestRead_OversizeFile_ReadsTailOnly(t *testing.T) {
	orig := maxReadBytes
	maxReadBytes = 150
	defer func() { maxReadBytes = orig }()

	dir := t.TempDir()
	const n = 30
	for i := 0; i < n; i++ {
		if err := Append(dir, "pane-big", Entry{TsMs: int64(i), Text: "x"}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	got, err := Read(dir, "pane-big")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) == 0 || len(got) >= n {
		t.Fatalf("expected a trimmed tail (0 < len < %d), got %d", n, len(got))
	}
	if got[len(got)-1].TsMs != n-1 {
		t.Fatalf("newest entry not retained: last TsMs = %d, want %d", got[len(got)-1].TsMs, n-1)
	}
	if got[0].TsMs == 0 {
		t.Fatalf("oldest entry should have been dropped by the tail read")
	}
}

func TestAppend_RejectsSymlink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(Dir(dir), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	target := filepath.Join(t.TempDir(), "evil.txt")
	if err := os.Symlink(target, Path(dir, "pane-sym")); err != nil {
		t.Skipf("symlink unsupported: %v", err) // e.g. Windows without privilege
	}
	if err := Append(dir, "pane-sym", Entry{TsMs: 1, Text: "x"}); err == nil {
		t.Fatal("Append should refuse to write through a symlink")
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("Append followed the symlink and wrote the target")
	}
}

func TestRead_RejectsSymlink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(Dir(dir), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "x"), Path(dir, "pane-sym2")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := Read(dir, "pane-sym2"); err == nil {
		t.Fatal("Read should refuse to read through a symlink")
	}
}

func TestPreviewLine_FlattensToOneLine(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"multiline joined by single spaces", "line one\nline two\nline three", "line one line two line three"},
		{"tabs and CRLF collapse", "a\tb\r\nc", "a b c"},
		{"whitespace runs collapse", "a  \n\n\t  b", "a b"},
		{"leading and trailing whitespace dropped", "\n\n  hello  \n\n", "hello"},
		{"blank lines only", "\n\t\n  ", ""},
		{"already one line unchanged", "just a prompt", "just a prompt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := PreviewLine(tt.in, 200); got != tt.want {
				t.Fatalf("PreviewLine(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// The list row is drawn inside a fixed-width bordered modal, so an escape
// sequence pasted into a prompt would repaint or reposition the box. U+009B is
// included because it is the C1 CSI introducer — a control that no
// "is it below 0x20?" filter catches.
func TestPreviewLine_DropsControlCharacters(t *testing.T) {
	t.Parallel()
	got := PreviewLine("red\x1b[31mtext\x07 end0m", 200)
	want := "red[31mtext end0m"
	if got != want {
		t.Fatalf("PreviewLine = %q, want %q", got, want)
	}
	for _, r := range got {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			t.Fatalf("control rune %U survived in %q", r, got)
		}
	}
}

// Bidi overrides and ZWSP are category Cf: printable to unicode.IsControl and
// invisible to unicode.IsSpace, so neither the control filter nor the
// whitespace collapse catches them without an explicit Cf case. The row is what
// the user reads before pressing Enter, so a reversed row could name a
// different prompt than the one that opens.
func TestPreviewLine_DropsUnicodeFormatCharacters(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"bidi override", "safe ‮reversed", "safe reversed"},
		{"bidi isolates", "⁦a⁩ ⁧b⁩", "a b"},
		{"zero-width space defeats whitespace collapse", "one​two", "onetwo"},
		{"LRM/RLM", "a‎b‏c", "abc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := PreviewLine(tt.in, 200)
			if got != tt.want {
				t.Fatalf("PreviewLine(%q) = %q, want %q", tt.in, got, tt.want)
			}
			for _, r := range got {
				if unicode.Is(unicode.Cf, r) {
					t.Fatalf("format rune %U survived in %q", r, got)
				}
			}
		})
	}
}

// Ranging a Go string yields utf8.RuneError for an invalid byte, and writing
// that back out encodes a proper U+FFFD — so a prompt carrying raw non-UTF-8
// bytes still leaves a valid string for lipgloss to measure and truncate.
func TestPreviewLine_InvalidUTF8YieldsValidString(t *testing.T) {
	t.Parallel()
	got := PreviewLine("ok\xff\xfe end", 200)
	if !utf8.ValidString(got) {
		t.Fatalf("PreviewLine returned invalid UTF-8: %q", got)
	}
	if !strings.HasPrefix(got, "ok") || !strings.HasSuffix(got, "end") {
		t.Fatalf("printable text not preserved around invalid bytes: %q", got)
	}
}

func TestPreviewLine_TruncatesRuneSafe(t *testing.T) {
	t.Parallel()
	got := PreviewLine(strings.Repeat("é", 50), 10)
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("want ellipsis suffix, got %q", got)
	}
	if len(got) > 10 {
		t.Fatalf("want <= 10 bytes, got %d (%q)", len(got), got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("truncation sliced a rune: %q", got)
	}
}

func TestCompact_KeepsLastN(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for i := 0; i < 10; i++ {
		if err := Append(dir, "pane-c", Entry{TsMs: int64(i), Text: "msg"}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if err := Compact(dir, "pane-c", 3); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	got, _ := Read(dir, "pane-c")
	if len(got) != 3 {
		t.Fatalf("want 3 entries, got %d", len(got))
	}
	if got[0].TsMs != 7 || got[2].TsMs != 9 {
		t.Fatalf("kept wrong window: %+v", got)
	}
}

func TestCompact_NoOpWhenUnderLimit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := Append(dir, "pane-d", Entry{TsMs: 1, Text: "a"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := Compact(dir, "pane-d", 5); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	got, _ := Read(dir, "pane-d")
	if len(got) != 1 {
		t.Fatalf("want 1 entry, got %d", len(got))
	}
}
