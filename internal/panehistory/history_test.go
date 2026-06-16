package panehistory

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestAppend_WritesEntry_ReadBack(t *testing.T) {
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

func TestAppend_CapsOversizeText(t *testing.T) {
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

func TestAppend_CapsOversizeText_MultiByteRuneSafe(t *testing.T) {
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
	got, err := Read(t.TempDir(), "pane-none")
	if err != nil || got != nil {
		t.Fatalf("want nil,nil; got %+v, %v", got, err)
	}
}

func TestPreview_MultilineTruncated(t *testing.T) {
	text := "line one\nline two\nline three\nline four"
	got := Preview(text, 3, 100)
	want := []string{"line one", "line two", "line three"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestPreview_LongLineWidthCapped(t *testing.T) {
	got := Preview(strings.Repeat("x", 50), 3, 10)
	if len(got) != 1 {
		t.Fatalf("want 1 line, got %d", len(got))
	}
	if !strings.HasSuffix(got[0], "…") || len([]rune(got[0])) > 10 {
		t.Fatalf("line not width-capped: %q (%d runes)", got[0], len([]rune(got[0])))
	}
}

func TestPreview_NormalizesTabsAndCR(t *testing.T) {
	got := Preview("a\tb\r\nc", 3, 100)
	if got[0] != "a    b" || got[1] != "c" {
		t.Fatalf("tabs/CR not normalized: %q", got)
	}
}

func TestCompact_KeepsLastN(t *testing.T) {
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
	dir := t.TempDir()
	Append(dir, "pane-d", Entry{TsMs: 1, Text: "a"})
	if err := Compact(dir, "pane-d", 5); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	got, _ := Read(dir, "pane-d")
	if len(got) != 1 {
		t.Fatalf("want 1 entry, got %d", len(got))
	}
}
