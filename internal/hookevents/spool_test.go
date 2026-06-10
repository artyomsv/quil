package hookevents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSpoolLine(t *testing.T, path string, p Payload) {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open spool: %v", err)
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		t.Fatalf("write spool: %v", err)
	}
}

func TestSpool_Tick_ReadsAppendedLines(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := NewSpool(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	path := filepath.Join(dir, "pane-1.jsonl")
	p1 := Payload{V: SchemaVersion, PaneID: "pane-1", Source: SourceClaude, HookEvent: "Stop", Title: "Reply ready", Severity: SeverityInfo, TsMs: 1, Seq: 1}
	p2 := Payload{V: SchemaVersion, PaneID: "pane-1", Source: SourceClaude, HookEvent: "PermissionRequest", Title: "Needs approval: Bash", Severity: SeverityWarning, TsMs: 2, Seq: 2}
	writeSpoolLine(t, path, p1)
	writeSpoolLine(t, path, p2)

	got := s.Tick()
	if len(got) != 2 {
		t.Fatalf("Tick: got %d payloads, want 2", len(got))
	}
	if got[0].HookEvent != "Stop" || got[1].HookEvent != "PermissionRequest" {
		t.Errorf("Tick order: got %s, %s", got[0].HookEvent, got[1].HookEvent)
	}
}

func TestSpool_Tick_StripsLeadingBOM(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := NewSpool(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	path := filepath.Join(dir, "pane-1.jsonl")
	p := Payload{V: SchemaVersion, PaneID: "pane-1", Source: SourceClaude, HookEvent: "UserPromptSubmit", Title: "Working on: x", Severity: SeverityInfo, TsMs: 1, Seq: 1}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Simulate Windows PowerShell 5.1 `Add-Content -Encoding UTF8`: it
	// prepends a UTF-8 BOM (EF BB BF) when it CREATES the file, so the first
	// event line per pane — always the start edge — is BOM-prefixed. Go's
	// json.Unmarshal rejects a leading BOM, so the reader must strip it.
	line := append([]byte{0xEF, 0xBB, 0xBF}, b...)
	line = append(line, '\n')
	if err := os.WriteFile(path, line, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := s.Tick()
	if len(got) != 1 {
		t.Fatalf("Tick: got %d payloads, want 1 (BOM-prefixed first line must parse)", len(got))
	}
	if got[0].HookEvent != "UserPromptSubmit" {
		t.Errorf("got hook_event %q, want UserPromptSubmit", got[0].HookEvent)
	}
}

func TestSpool_Tick_OffsetSurvivesAcrossCalls(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := NewSpool(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	path := filepath.Join(dir, "pane-1.jsonl")
	p1 := Payload{V: SchemaVersion, PaneID: "pane-1", Source: SourceClaude, HookEvent: "Stop", Title: "t", Severity: SeverityInfo, TsMs: 1, Seq: 1}
	writeSpoolLine(t, path, p1)

	first := s.Tick()
	if len(first) != 1 {
		t.Fatalf("first Tick: got %d, want 1", len(first))
	}

	// Second Tick with no new writes — must return zero.
	second := s.Tick()
	if len(second) != 0 {
		t.Errorf("second Tick (no new lines): got %d, want 0", len(second))
	}

	// Append more lines, Tick again — only the new ones.
	p2 := Payload{V: SchemaVersion, PaneID: "pane-1", Source: SourceClaude, HookEvent: "PermissionRequest", Title: "t", Severity: SeverityInfo, TsMs: 2, Seq: 2}
	writeSpoolLine(t, path, p2)
	third := s.Tick()
	if len(third) != 1 || third[0].HookEvent != "PermissionRequest" {
		t.Errorf("third Tick: got %+v, want 1 PermissionRequest", third)
	}
}

func TestSpool_Tick_SkipsPartialTrailingLine(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := NewSpool(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	path := filepath.Join(dir, "pane-1.jsonl")
	// Write one complete line then a partial line with no trailing newline
	// — simulates a hook write that the daemon polls in the middle of.
	complete := Payload{V: SchemaVersion, PaneID: "pane-1", Source: SourceClaude, HookEvent: "Stop", Title: "Reply ready", Severity: SeverityInfo, TsMs: 1, Seq: 1}
	b, _ := json.Marshal(complete)
	partial := `{"v":1,"pane_id":"pane-1","src":"claude","hook_event":"PermissionRequest","title":"partial"`
	if err := os.WriteFile(path, append(append(b, '\n'), []byte(partial)...), 0o600); err != nil {
		t.Fatalf("write spool: %v", err)
	}

	got := s.Tick()
	if len(got) != 1 || got[0].HookEvent != "Stop" {
		t.Errorf("Tick should consume only the complete line; got %+v", got)
	}

	// Now flush the partial by appending the missing close. The partial
	// content is whatever remains after Stop's newline; finishing the JSON
	// + adding a newline turns it into a valid second line.
	finish := `,"sev":"warning"}` + "\n"
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(finish)
	f.Close()

	got2 := s.Tick()
	if len(got2) != 1 || got2[0].HookEvent != "PermissionRequest" {
		t.Errorf("after partial completion: got %+v, want 1 PermissionRequest", got2)
	}
}

func TestSpool_Tick_DropsMalformed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := NewSpool(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	path := filepath.Join(dir, "pane-1.jsonl")
	garbage := "{not valid json\n"
	good := Payload{V: SchemaVersion, PaneID: "pane-1", Source: SourceClaude, HookEvent: "Stop", Title: "Reply ready", Severity: SeverityInfo, TsMs: 1, Seq: 1}
	gb, _ := json.Marshal(good)
	if err := os.WriteFile(path, append([]byte(garbage), append(gb, '\n')...), 0o600); err != nil {
		t.Fatalf("write spool: %v", err)
	}

	got := s.Tick()
	if len(got) != 1 || got[0].HookEvent != "Stop" {
		t.Errorf("Tick should drop garbage and keep the valid line; got %+v", got)
	}
}

func TestSpool_Init_TruncatesExistingFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Pre-seed a stale file as though a previous daemon left it.
	path := filepath.Join(dir, "pane-old.jsonl")
	if err := os.WriteFile(path, []byte("stale content from previous run\n"), 0o600); err != nil {
		t.Fatalf("seed spool: %v", err)
	}

	s := NewSpool(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after init: %v", err)
	}
	if info.Size() != 0 {
		t.Errorf("Init should truncate stale spool; got size %d", info.Size())
	}
}

func TestSpool_Cleanup_RemovesFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := NewSpool(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	path := filepath.Join(dir, "pane-1.jsonl")
	if err := os.WriteFile(path, []byte("noise\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	s.Cleanup("pane-1")

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("Cleanup should unlink spool; stat err = %v", err)
	}
}

func TestSpool_Tick_IgnoresNonJSONLFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := NewSpool(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	// A non-jsonl file should be silently ignored.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got := s.Tick()
	if len(got) != 0 {
		t.Errorf("non-.jsonl files must be ignored; got %d payloads", len(got))
	}
}

func TestSpool_Tick_DropsOversizeLine(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := NewSpool(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	path := filepath.Join(dir, "pane-1.jsonl")
	// A line larger than MaxTotalBytes must be dropped silently.
	big := strings.Repeat("x", MaxTotalBytes+10) + "\n"
	if err := os.WriteFile(path, []byte(big), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got := s.Tick()
	if len(got) != 0 {
		t.Errorf("oversize line must be dropped; got %d payloads", len(got))
	}
}

func TestSpoolCleanup_RemovesParseErrCount(t *testing.T) {
	t.Parallel()
	s := NewSpool(t.TempDir())
	s.mu.Lock()
	s.parseErrCounts["pane-x"] = 3
	s.mu.Unlock()

	s.Cleanup("pane-x")

	s.mu.Lock()
	_, ok := s.parseErrCounts["pane-x"]
	s.mu.Unlock()
	if ok {
		t.Error("Cleanup left parseErrCounts entry — monotonic map growth")
	}
}
