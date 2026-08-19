package hookevents

import (
	"bytes"
	"encoding/json"
	"errors"
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

// Init must UNLINK stale spools, not truncate them. Tick walks every .jsonl in
// the directory and pays open+stat+close on each one, so a file left behind for
// a pane that no longer exists costs syscalls on every 200 ms tick for as long
// as the daemon runs — and Init preserving them means the set only ever grows,
// across every restart. Observed in production 2026-08-18: 349 spool files for
// 37 live panes, 332 of them the zero-byte husks this truncate produced,
// driving ~7,000 handle operations/sec and 21% of one core in kernel time.
func TestSpool_Init_RemovesStaleFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Both shapes a previous daemon leaves behind: one still holding events,
	// one already truncated to zero by an earlier Init.
	seed := map[string]string{
		"pane-old.jsonl":   "stale content from previous run\n",
		"pane-older.jsonl": "",
	}
	for name, body := range seed {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	s := NewSpool(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	var left []string
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read spool dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jsonl") {
			left = append(left, e.Name())
		}
	}
	if len(left) != 0 {
		t.Errorf("Init should unlink stale spools, not truncate them; %d left behind: %v", len(left), left)
	}
}

// The reason Init touches stale files at all: notifications are ephemeral, so a
// previous session's events must never replay into this one. Removal has to keep
// that guarantee that truncation provided.
func TestSpool_Init_DoesNotReplayPreviousSessionEvents(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	path := filepath.Join(dir, "pane-1.jsonl")
	writeSpoolLine(t, path, Payload{
		V: SchemaVersion, PaneID: "pane-1", Source: SourceClaude,
		HookEvent: "Stop", Title: "Reply ready", Severity: SeverityInfo, TsMs: 1, Seq: 1,
	})

	s := NewSpool(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	if got := s.Tick(); len(got) != 0 {
		t.Errorf("Init must not let a previous session's events replay; Tick returned %+v", got)
	}
}

// Removal can fail where truncation would have succeeded: on Windows an open
// handle without FILE_SHARE_DELETE fails the unlink, and this project's primary
// platform is Windows. A failed remove must not leave a file still holding a
// previous session's events — the no-replay guarantee is the floor, and
// unlinking is only the optimisation on top of it.
func TestSpool_Init_TruncatesWhenRemoveFails(t *testing.T) {
	dir := t.TempDir()

	path := filepath.Join(dir, "pane-1.jsonl")
	writeSpoolLine(t, path, Payload{
		V: SchemaVersion, PaneID: "pane-1", Source: SourceClaude,
		HookEvent: "Stop", Title: "Reply ready", Severity: SeverityInfo, TsMs: 1, Seq: 1,
	})

	orig := removeFn
	removeFn = func(string) error { return errors.New("sharing violation") }
	t.Cleanup(func() { removeFn = orig })

	s := NewSpool(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("file should still exist when remove fails: %v", err)
	}
	if info.Size() != 0 {
		t.Errorf("Init must fall back to truncation when remove fails; size = %d", info.Size())
	}
	if got := s.Tick(); len(got) != 0 {
		t.Errorf("undeletable spool must not replay previous events; Tick returned %+v", got)
	}
}

// Init now DELETES rather than truncates, so the .jsonl filter stops being a
// tidiness detail and becomes the blast radius. The spool directory is
// $QUIL_HOME/events, and nothing else there is Init's to remove.
func TestSpool_Init_LeavesNonSpoolFilesAlone(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	keep := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(keep, []byte("not a spool\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "subdir.jsonl"), 0o700); err != nil {
		t.Fatalf("seed dir: %v", err)
	}

	s := NewSpool(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	if _, err := os.Stat(keep); err != nil {
		t.Errorf("Init removed a non-spool file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "subdir.jsonl")); err != nil {
		t.Errorf("Init removed a directory whose name ends in .jsonl: %v", err)
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

// Tick used to open+fstat+close every spool file on every tick, including the
// files with nothing new — which is nearly all of them, nearly all the time.
// At 5 Hz across a large workspace that was the daemon's dominant syscall
// source: measured 151 IO-other + 58 IO-read ops/sec with ~10 live spools.
//
// Serial, not parallel: stubs the openFile package var (same constraint the
// removeFn tests carry).
func TestSpool_Tick_DoesNotOpenFilesWithNothingNew(t *testing.T) {
	dir := t.TempDir()
	s := NewSpool(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	path := filepath.Join(dir, "pane-1.jsonl")
	writeSpoolLine(t, path, Payload{
		V: SchemaVersion, PaneID: "pane-1", Source: SourceClaude,
		HookEvent: "Stop", Title: "Reply ready", Severity: SeverityInfo, TsMs: 1, Seq: 1,
	})

	if got := len(s.Tick()); got != 1 {
		t.Fatalf("first Tick returned %d payloads, want 1", got)
	}

	var opens int
	restore := openFile
	openFile = func(name string) (*os.File, error) {
		opens++
		return restore(name)
	}
	t.Cleanup(func() { openFile = restore })

	if got := len(s.Tick()); got != 0 {
		t.Fatalf("second Tick returned %d payloads, want 0", got)
	}
	if opens != 0 {
		t.Errorf("second Tick opened %d files, want 0 — an idle spool must cost no file handle", opens)
	}
}

// The size shortcut must not swallow real data.
func TestSpool_Tick_StillReadsWhenTheFileGrew(t *testing.T) {
	dir := t.TempDir()
	s := NewSpool(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	path := filepath.Join(dir, "pane-1.jsonl")
	writeSpoolLine(t, path, Payload{
		V: SchemaVersion, PaneID: "pane-1", Source: SourceClaude,
		HookEvent: "Stop", Title: "one", Severity: SeverityInfo, TsMs: 1, Seq: 1,
	})
	if got := len(s.Tick()); got != 1 {
		t.Fatalf("first Tick = %d, want 1", got)
	}

	writeSpoolLine(t, path, Payload{
		V: SchemaVersion, PaneID: "pane-1", Source: SourceClaude,
		HookEvent: "Stop", Title: "two", Severity: SeverityInfo, TsMs: 2, Seq: 2,
	})
	if got := len(s.Tick()); got != 1 {
		t.Errorf("Tick after append = %d, want 1 — the size shortcut dropped a real line", got)
	}
}

// A file the spool has never seen must always be read, however its size
// compares to the zero-value offset an unseen paneID maps to.
func TestSpool_Tick_ReadsAFileItHasNeverSeenBefore(t *testing.T) {
	dir := t.TempDir()
	s := NewSpool(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	// Empty file: size 0, and offsets[paneID] is also 0 for an unseen pane. A
	// naive `size == off` skip would drop it forever, including the moment it
	// first gains content.
	path := filepath.Join(dir, "pane-new.jsonl")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	s.Tick()

	writeSpoolLine(t, path, Payload{
		V: SchemaVersion, PaneID: "pane-new", Source: SourceClaude,
		HookEvent: "Stop", Title: "first", Severity: SeverityInfo, TsMs: 1, Seq: 1,
	})
	if got := len(s.Tick()); got != 1 {
		t.Errorf("Tick = %d, want 1 — a previously-empty file must still be read once it grows", got)
	}
}

// An empty spool file records no offset (readPaneFile's idle branch returns
// before writing one), so it is permanently "unseen". Without a size-only skip
// it would pay open+fstat+close on every tick for the life of the daemon —
// which is the husk cost the Init unlink fix removed at startup, reappearing
// for any pane whose spool exists but is still empty.
func TestSpool_Tick_DoesNotOpenAnEmptyFileRepeatedly(t *testing.T) {
	dir := t.TempDir()
	s := NewSpool(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	path := filepath.Join(dir, "pane-empty.jsonl")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	s.Tick() // first sighting

	var opens int
	restore := openFile
	openFile = func(name string) (*os.File, error) {
		opens++
		return restore(name)
	}
	t.Cleanup(func() { openFile = restore })

	s.Tick()
	s.Tick()
	if opens != 0 {
		t.Errorf("two idle Ticks opened an empty file %d times, want 0", opens)
	}

	// ...and it must still be read the moment it gains content.
	openFile = restore
	writeSpoolLine(t, path, Payload{
		V: SchemaVersion, PaneID: "pane-empty", Source: SourceClaude,
		HookEvent: "Stop", Title: "first", Severity: SeverityInfo, TsMs: 1, Seq: 1,
	})
	if got := len(s.Tick()); got != 1 {
		t.Errorf("Tick after the empty file grew = %d, want 1", got)
	}
}

// Rotation runs from readPaneFile's idle branch, so a file at the threshold
// must never take the skip — it would be stranded to grow without bound.
func TestSpool_Tick_StillRotatesAnIdleFileAtTheThreshold(t *testing.T) {
	dir := t.TempDir()
	s := NewSpool(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	path := filepath.Join(dir, "pane-big.jsonl")
	big := bytes.Repeat([]byte("\n"), rotationThreshold+1)
	if err := os.WriteFile(path, big, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	s.Tick() // consumes; offset reaches size
	s.Tick() // idle tick: must still reach the rotate path

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size() >= rotationThreshold {
		t.Errorf("an idle file at the rotation threshold was skipped and never rotated (size=%d)", info.Size())
	}
}

// The rotation boundary EXACTLY, not threshold+1.
//
// readPaneFile rotates on `size >= rotationThreshold`, so the skip must refuse
// at `size == rotationThreshold` too. Seeding threshold+1 leaves the boundary
// itself untested and lets `<` mutate to `<=` undetected — a file sitting at
// exactly 16 MiB would then take the skip and never rotate until it grew one
// more byte.
func TestSpool_Tick_RotatesAFileAtExactlyTheThreshold(t *testing.T) {
	dir := t.TempDir()
	s := NewSpool(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	path := filepath.Join(dir, "pane-exact.jsonl")
	if err := os.WriteFile(path, bytes.Repeat([]byte("\n"), rotationThreshold), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	s.Tick() // consumes; offset reaches size == rotationThreshold
	s.Tick() // idle tick: must NOT be skipped, must reach rotate

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size() >= rotationThreshold {
		t.Errorf("a file at EXACTLY rotationThreshold was skipped and never rotated (size=%d)", info.Size())
	}
}
