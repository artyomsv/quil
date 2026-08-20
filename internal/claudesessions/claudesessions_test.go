package claudesessions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// TestEscapeCWD locks in claude's on-disk naming convention for per-project
// session directories. If claude ever changes this (e.g. starts
// percent-encoding instead), this test fails in CI instead of panes silently
// falling back to --continue everywhere and the session picker listing an
// empty directory.
//
// Moved here from internal/daemon when the rule was extracted into this
// package; the vectors and their field provenance are unchanged.
func TestEscapeCWD(t *testing.T) {
	tests := []struct {
		name string
		cwd  string
		want string
	}{
		{"windows path", `E:\Projects\Stukans\Prototypes\calyx`, "E--Projects-Stukans-Prototypes-calyx"},
		{"unix path", "/home/user/project", "-home-user-project"},
		{"windows with dot-dir", `C:\Users\artjo\.claude`, "C--Users-artjo--claude"},
		{"mixed separators", `E:/Projects\mixed`, "E--Projects-mixed"},
		{"root-only windows", `C:\`, "C--"},
		{"empty", "", ""},
		// Regression: macOS home like /Users/Foo_Bar lands under
		// ~/.claude/projects/-Users-Foo-Bar (Claude encodes _ as -). Before
		// the fix every Claude pane on a path with an underscore restarted
		// with --continue instead of --resume <id>.
		{"unix path with underscore", "/Users/Artjoms_Stukans/Projects/crypto-finance", "-Users-Artjoms-Stukans-Projects-crypto-finance"},
		{"underscore-only segment", "/home/foo_bar/quil", "-home-foo-bar-quil"},
		{"multiple underscores", "/a_b/c_d_e", "-a-b-c-d-e"},
		// Field evidence 2026-07-05: a worktree CWD containing ".claude"
		// landed under E--Projects-Stukans-quil--claude-worktrees-… — Claude
		// encodes EVERY non-alphanumeric as '-'. The escaper mirrors that rule
		// exactly; only [A-Za-z0-9] survives.
		{"dot encoded", "/foo.bar/quil", "-foo-bar-quil"},
		{"space encoded", "/foo bar/quil", "-foo-bar-quil"},
		{"uppercase preserved", "/Foo/BAR", "-Foo-BAR"},
		{"worktree dot-dir (real incident)",
			`E:\Projects\Stukans\quil\.claude\worktrees\resize-artifacts`,
			"E--Projects-Stukans-quil--claude-worktrees-resize-artifacts"},
		// Cross-OS + unicode parity with claude's JS sanitizer
		// (replace(/[^a-zA-Z0-9]/g,"-") over UTF-16 code units, extracted
		// from the binary 2026-07-05): BMP non-ASCII → one dash, astral
		// (emoji, surrogate pair) → two dashes.
		{"macos home with accent", "/Users/josé/proj", "-Users-jos--proj"},
		{"astral char is two units", "/tmp/😀dir", "-tmp---dir"},
		// >200-char names: claude truncates to 200 then appends
		// "-"+base36(abs(hash)). The exact form is transcribed from the
		// claude binary (2026-07-05):
		//   Ows(e){let t=e.replace(/[^a-zA-Z0-9]/g,"-");
		//          if(t.length<=200)return t;
		//          return `${t.slice(0,200)}-${Math.abs(Pke(e)).toString(36)}`}
		//   Pke(e){let h=0;for(u of utf16(e))h=(h<<5)-h+u|0;return h}
		// Critically, the hash argument is the ORIGINAL cwd `e`, NOT the
		// dashified `t` — so EscapeCWD hashes `units` (original), and a
		// >200-char path resolves to the SAME dir claude writes. The
		// `-ut7e65` suffix below is computed by that transcribed algorithm;
		// it has not been diffed against a claude-generated dir for a
		// >200-char cwd (no such path exists on disk to observe), but the
		// algorithm is byte-for-byte the binary's. If claude ever changes
		// the scheme, the sanity gate fails safe: a wrong probe dir just
		// forces the --continue fallback (pane still restores), and this
		// vector flags the drift.
		{"exactly 200 keeps no suffix", "/" + strings.Repeat("a", 199),
			"-" + strings.Repeat("a", 199)},
		{"long path truncates with hash suffix", "/home/user/" + strings.Repeat("a", 200),
			"-home-user-" + strings.Repeat("a", 189) + "-ut7e65"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EscapeCWD(tt.cwd); got != tt.want {
				t.Errorf("EscapeCWD(%q) = %q, want %q", tt.cwd, got, tt.want)
			}
		})
	}
}

func TestProjectDir_EmptyCWD_ReturnsEmpty(t *testing.T) {
	if got := ProjectDir(""); got != "" {
		t.Errorf("ProjectDir(\"\") = %q, want empty", got)
	}
}

func TestTranscriptPath_EmptySessionID_ReturnsEmpty(t *testing.T) {
	if got := TranscriptPath("/home/user/proj", ""); got != "" {
		t.Errorf("TranscriptPath with empty id = %q, want empty", got)
	}
}

func TestTranscriptPath_BuildsJSONLPath(t *testing.T) {
	got := TranscriptPath("/home/user/proj", "abc-123")
	if got == "" {
		t.Skip("home directory unavailable in this environment")
	}
	wantSuffix := filepath.Join("-home-user-proj", "abc-123.jsonl")
	if !strings.HasSuffix(got, wantSuffix) {
		t.Errorf("TranscriptPath = %q, want suffix %q", got, wantSuffix)
	}
}

// typedPrompt builds a transcript line matching the shape claude writes for a
// prompt the user typed.
func typedPrompt(text string) string {
	return fmt.Sprintf(
		`{"type":"user","isSidechain":false,"promptSource":"typed","message":{"role":"user","content":%q},"timestamp":"2026-07-01T10:00:00.000Z"}`,
		text)
}

// writeSession creates a transcript file and stamps its mtime, returning the
// session id.
func writeSession(t *testing.T, dir, id string, mtime time.Time, lines ...string) string {
	t.Helper()
	path := filepath.Join(dir, id+".jsonl")
	body := strings.Join(lines, "\n")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write session %s: %v", id, err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes %s: %v", id, err)
	}
	return id
}

func TestListDir_MissingDirectory_ReturnsEmptyNotError(t *testing.T) {
	got, _, err := listDir(context.Background(), filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("listDir on missing dir returned error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("listDir on missing dir = %d sessions, want 0", len(got))
	}
}

// TestListDir_ExactlyCapIsNotTruncated: a directory holding exactly MaxSessions
// has nothing hidden, so the flag must stay false — the picker would otherwise
// tell the user older sessions exist when they do not.
func TestListDir_ExactlyCapIsNotTruncated(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < MaxSessions; i++ {
		writeSession(t, dir, fmt.Sprintf("s%03d", i),
			base.Add(time.Duration(i)*time.Minute), typedPrompt("prompt"))
	}

	got, truncated, err := listDir(context.Background(), dir)
	if err != nil {
		t.Fatalf("listDir: %v", err)
	}
	if len(got) != MaxSessions {
		t.Fatalf("listDir returned %d sessions, want %d", len(got), MaxSessions)
	}
	if truncated {
		t.Error("truncated = true for exactly the cap with nothing dropped")
	}
}

func TestListDir_SortsNewestFirst(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	writeSession(t, dir, "oldest", base.Add(-48*time.Hour), typedPrompt("first task"))
	writeSession(t, dir, "newest", base, typedPrompt("third task"))
	writeSession(t, dir, "middle", base.Add(-24*time.Hour), typedPrompt("second task"))

	got, _, err := listDir(context.Background(), dir)
	if err != nil {
		t.Fatalf("listDir: %v", err)
	}
	want := []string{"newest", "middle", "oldest"}
	if len(got) != len(want) {
		t.Fatalf("listDir returned %d sessions, want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Errorf("session[%d].ID = %q, want %q", i, got[i].ID, id)
		}
	}
}

func TestListDir_ExtractsFirstTypedPrompt(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, dir, "s1", time.Now(),
		`{"type":"last-prompt","leafUuid":"x","sessionId":"s1"}`,
		`{"type":"mode","mode":"normal"}`,
		typedPrompt("Add resume option to the setup dialog"),
		typedPrompt("a later prompt that must not win"),
	)

	got, _, err := listDir(context.Background(), dir)
	if err != nil {
		t.Fatalf("listDir: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("listDir returned %d sessions, want 1", len(got))
	}
	if want := "Add resume option to the setup dialog"; got[0].Title != want {
		t.Errorf("Title = %q, want %q", got[0].Title, want)
	}
}

func TestListDir_SkipsSidechainAndNonTypedPrompts(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, dir, "s1", time.Now(),
		// Subagent prompt — belongs to a sidechain, not the conversation.
		`{"type":"user","isSidechain":true,"promptSource":"typed","message":{"role":"user","content":"subagent instruction"}}`,
		// Machine-injected turn — not typed by the user.
		`{"type":"user","isSidechain":false,"promptSource":"injected","message":{"role":"user","content":"injected turn"}}`,
		typedPrompt("the real first prompt"),
	)

	got, _, err := listDir(context.Background(), dir)
	if err != nil {
		t.Fatalf("listDir: %v", err)
	}
	if want := "the real first prompt"; got[0].Title != want {
		t.Errorf("Title = %q, want %q", got[0].Title, want)
	}
}

func TestListDir_ContentBlockArrayShape(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, dir, "s1", time.Now(),
		`{"type":"user","isSidechain":false,"promptSource":"typed","message":{"role":"user","content":[{"type":"text","text":"prompt with attachment"},{"type":"image","source":{}}]}}`,
	)

	got, _, err := listDir(context.Background(), dir)
	if err != nil {
		t.Fatalf("listDir: %v", err)
	}
	if want := "prompt with attachment"; got[0].Title != want {
		t.Errorf("Title = %q, want %q", got[0].Title, want)
	}
}

func TestListDir_MalformedLinesSkipped(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, dir, "s1", time.Now(),
		`{"promptSource":"typed" this is not json`,
		typedPrompt("valid prompt"),
	)

	got, _, err := listDir(context.Background(), dir)
	if err != nil {
		t.Fatalf("listDir: %v", err)
	}
	if want := "valid prompt"; got[0].Title != want {
		t.Errorf("Title = %q, want %q", got[0].Title, want)
	}
}

func TestListDir_NoTypedPrompt_EmptyTitleStillListed(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, dir, "s1", time.Now(),
		`{"type":"mode","mode":"normal","sessionId":"s1"}`,
	)

	got, _, err := listDir(context.Background(), dir)
	if err != nil {
		t.Fatalf("listDir: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("listDir returned %d sessions, want 1 (a titleless session must still list)", len(got))
	}
	if got[0].Title != "" {
		t.Errorf("Title = %q, want empty", got[0].Title)
	}
}

func TestListDir_IgnoresDirectoriesAndNonJSONL(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, dir, "real", time.Now(), typedPrompt("real session"))
	// Claude keeps per-session scratch directories named like session UUIDs.
	if err := os.Mkdir(filepath.Join(dir, "scratch.jsonl"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, _, err := listDir(context.Background(), dir)
	if err != nil {
		t.Fatalf("listDir: %v", err)
	}
	if len(got) != 1 || got[0].ID != "real" {
		t.Errorf("listDir = %+v, want only the real session", got)
	}
}

func TestListDir_SkipsEmptyFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "empty.jsonl"), nil, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, _, err := listDir(context.Background(), dir)
	if err != nil {
		t.Fatalf("listDir: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("listDir = %d sessions, want 0", len(got))
	}
}

func TestListDir_CapsAtMaxSessions(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < MaxSessions+25; i++ {
		writeSession(t, dir, fmt.Sprintf("s%03d", i),
			base.Add(time.Duration(i)*time.Minute), typedPrompt("prompt"))
	}

	got, truncated, err := listDir(context.Background(), dir)
	if err != nil {
		t.Fatalf("listDir: %v", err)
	}
	if len(got) != MaxSessions {
		t.Fatalf("listDir returned %d sessions, want %d", len(got), MaxSessions)
	}
	if !truncated {
		t.Error("truncated = false, want true when sessions were dropped by the cap")
	}
	// Newest first: the highest index has the latest mtime.
	if want := fmt.Sprintf("s%03d", MaxSessions+24); got[0].ID != want {
		t.Errorf("got[0].ID = %q, want %q (newest must survive the cap)", got[0].ID, want)
	}
}

func TestSanitizeTitle(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain passes through", "fix the daemon", "fix the daemon"},
		{"newlines collapse to spaces", "line one\nline two", "line one line two"},
		{"tabs and runs collapse", "a\t\t  b", "a b"},
		{"ansi escape dropped, not spaced", "red \x1b[31mtext\x1b[0m", "red [31mtext[0m"},
		{"null byte dropped", "a\x00b", "ab"},
		{"carriage return separates", "a\r\nb", "a b"},
		{"surrounding space trimmed", "  padded  ", "padded"},
		{"empty stays empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeTitle(tt.in); got != tt.want {
				t.Errorf("sanitizeTitle(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSanitizeTitle_TruncatesOnRuneBoundary(t *testing.T) {
	// Multi-byte runes: a naive byte slice would split one and emit U+FFFD.
	in := strings.Repeat("é", MaxTitleRunes+50)
	got := sanitizeTitle(in)
	runes := []rune(got)
	if len(runes) != MaxTitleRunes+1 { // +1 for the ellipsis
		t.Fatalf("sanitizeTitle produced %d runes, want %d", len(runes), MaxTitleRunes+1)
	}
	if runes[len(runes)-1] != '…' {
		t.Errorf("truncated title does not end in an ellipsis: %q", string(runes[len(runes)-3:]))
	}
	if strings.ContainsRune(got, '\uFFFD') {
		t.Error("truncation split a multi-byte rune")
	}
}

func TestReadTitle_UnreadableFile_ReturnsEmpty(t *testing.T) {
	if got := readTitle(filepath.Join(t.TempDir(), "nope.jsonl")); got != "" {
		t.Errorf("readTitle on missing file = %q, want empty", got)
	}
}

func TestReadTitle_PromptBeyondScanWindow_ReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	// Pad past the scan window before the first typed prompt appears.
	padding := strings.Repeat(`{"type":"system","note":"`+strings.Repeat("x", 512)+`"}`+"\n", 200)
	path := filepath.Join(dir, "s1.jsonl")
	if err := os.WriteFile(path, []byte(padding+typedPrompt("too late")), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if len(padding) <= titleScanBytes {
		t.Fatalf("test padding (%d bytes) must exceed the scan window (%d)", len(padding), titleScanBytes)
	}
	if got := readTitle(path); got != "" {
		t.Errorf("readTitle = %q, want empty for a prompt past the scan window", got)
	}
}

// --- ReadDetail ------------------------------------------------------------

// toolResult builds the shape claude records for a tool result: type "user",
// but with no promptSource, because the user did not type it.
// toolResult builds the entry claude writes for a tool RESULT: type "user",
// but carrying a top-level toolUseResult. The content is a bare STRING, which
// is the common case and the reason content shape cannot classify these — the
// sibling field is what identifies them.
func toolResult(text string) string {
	return fmt.Sprintf(
		`{"type":"user","isSidechain":false,"toolUseResult":{"stdout":"…"},"message":{"role":"user","content":%q},"timestamp":"2026-07-01T10:05:00.000Z"}`,
		text)
}

// metaEntry builds a claude-injected meta entry: no tag, no toolUseResult, only
// isMeta. Hook notices take this shape, so isMeta is the only marker that
// identifies them.
func metaEntry(text string) string {
	return fmt.Sprintf(
		`{"type":"user","isSidechain":false,"isMeta":true,"message":{"role":"user","content":%q},"timestamp":"2026-07-01T10:06:00.000Z"}`,
		text)
}

// plainUserEntry is an old-schema user entry carrying no marker at all — no
// promptSource, no isMeta, no toolUseResult. This is what a real prompt looks
// like on a build that predates promptSource.
func plainUserEntry(text string) string {
	return fmt.Sprintf(
		`{"type":"user","isSidechain":false,"message":{"role":"user","content":%q},"timestamp":"2026-07-01T10:07:00.000Z"}`,
		text)
}

func assistantMsg(text string) string {
	return fmt.Sprintf(
		`{"type":"assistant","isSidechain":false,"message":{"role":"assistant","content":%q},"timestamp":"2026-07-01T10:01:00.000Z"}`,
		text)
}

// TestReadDetail_CountsOnlyTypedPrompts is the finding that shaped this whole
// read: claude records a tool RESULT as type "user" too. Counting raw user
// entries reports hundreds for a conversation with a handful of real prompts.
func TestReadDetail_CountsOnlyTypedPrompts(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, dir, "s1", time.Now(),
		typedPrompt("first thing"),
		assistantMsg("working on it"),
		toolResult("tool output that is not a prompt"),
		toolResult("more tool output"),
		typedPrompt("second thing"),
		assistantMsg("done"),
		typedPrompt("last thing"),
	)

	d, err := readDetail(context.Background(), filepath.Join(dir, "s1.jsonl"), "s1")
	if err != nil {
		t.Fatalf("readDetail: %v", err)
	}
	if d.UserPrompts != 3 {
		t.Errorf("UserPrompts = %d, want 3 (tool results are type \"user\" but were not typed)", d.UserPrompts)
	}
	if d.FirstPrompt != "first thing" {
		t.Errorf("FirstPrompt = %q, want %q", d.FirstPrompt, "first thing")
	}
	if d.LastPrompt != "last thing" {
		t.Errorf("LastPrompt = %q, want %q", d.LastPrompt, "last thing")
	}
}

// TestReadDetail_SkipsSidechainPrompts: sidechain entries are a subagent's
// conversation, not the user's, and must not be attributed to them.
func TestReadDetail_SkipsSidechainPrompts(t *testing.T) {
	dir := t.TempDir()
	sidechain := `{"type":"user","isSidechain":true,"promptSource":"typed","message":{"role":"user","content":"subagent task"},"timestamp":"2026-07-01T10:02:00.000Z"}`
	writeSession(t, dir, "s1", time.Now(), typedPrompt("mine"), sidechain)

	d, err := readDetail(context.Background(), filepath.Join(dir, "s1.jsonl"), "s1")
	if err != nil {
		t.Fatalf("readDetail: %v", err)
	}
	if d.UserPrompts != 1 {
		t.Errorf("UserPrompts = %d, want 1", d.UserPrompts)
	}
	if d.LastPrompt != "mine" {
		t.Errorf("LastPrompt = %q, want the user's own prompt", d.LastPrompt)
	}
}

func TestReadDetail_ReportsStartedSizeAndModified(t *testing.T) {
	dir := t.TempDir()
	mtime := time.Now().Add(-3 * time.Hour).Truncate(time.Second)
	writeSession(t, dir, "s1", mtime, typedPrompt("hello"))

	d, err := readDetail(context.Background(), filepath.Join(dir, "s1.jsonl"), "s1")
	if err != nil {
		t.Fatalf("readDetail: %v", err)
	}
	if d.Started.IsZero() {
		t.Error("Started is zero — the opening entry carried a timestamp")
	}
	if !d.Modified.Equal(mtime) {
		t.Errorf("Modified = %v, want %v", d.Modified, mtime)
	}
	if d.SizeBytes <= 0 {
		t.Errorf("SizeBytes = %d, want the file size", d.SizeBytes)
	}
	if d.ID != "s1" {
		t.Errorf("ID = %q, want s1", d.ID)
	}
}

// TestReadDetail_StartTimestampBeyondOpeningLines: the start scan is bounded,
// so a transcript that opens with many undated entries reports no start time
// rather than parsing the whole file looking for one.
func TestReadDetail_StartTimestampBeyondOpeningLines(t *testing.T) {
	dir := t.TempDir()
	lines := make([]string, 0, startScanLines+2)
	for i := 0; i < startScanLines+1; i++ {
		lines = append(lines, `{"type":"system","note":"no timestamp here"}`)
	}
	lines = append(lines, typedPrompt("late"))
	writeSession(t, dir, "s1", time.Now(), lines...)

	d, err := readDetail(context.Background(), filepath.Join(dir, "s1.jsonl"), "s1")
	if err != nil {
		t.Fatalf("readDetail: %v", err)
	}
	if !d.Started.IsZero() {
		t.Errorf("Started = %v, want zero past the opening-line budget", d.Started)
	}
	// The prompt itself is still found — only the start scan is bounded.
	if d.UserPrompts != 1 {
		t.Errorf("UserPrompts = %d, want 1", d.UserPrompts)
	}
}

func TestReadDetail_MalformedAndPartialLinesSkipped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s1.jsonl")
	// No trailing newline on the last line: a transcript a live claude is
	// appending to routinely ends mid-entry.
	body := typedPrompt("good") + "\n" +
		`{"type":"user","promptSource":"typed",` + "\n" +
		`{"type":"user","isSidechain":false,"promptSource":"typed","message":{"role":"user","content":"trunc`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	d, err := readDetail(context.Background(), path, "s1")
	if err != nil {
		t.Fatalf("readDetail: %v", err)
	}
	if d.UserPrompts != 1 || d.LastPrompt != "good" {
		t.Errorf("UserPrompts = %d, LastPrompt = %q; want 1 and %q — malformed lines must be skipped, not counted",
			d.UserPrompts, d.LastPrompt, "good")
	}
}

func TestReadDetail_MissingFileIsAnError(t *testing.T) {
	if _, err := readDetail(context.Background(), filepath.Join(t.TempDir(), "nope.jsonl"), "nope"); err == nil {
		t.Error("readDetail on a missing transcript returned nil error")
	}
}

// TestReadDetail_RejectsTraversalSessionID guards the path the id takes: it is
// a filename component built from a value that arrives over a socket.
func TestReadDetail_RejectsTraversalSessionID(t *testing.T) {
	for _, id := range []string{
		"",
		"..",
		".",
		"../secrets",
		"sub/dir",
		`..\windows`,
	} {
		if _, err := ReadDetail(context.Background(), t.TempDir(), id); err == nil {
			t.Errorf("ReadDetail accepted session id %q", id)
		}
	}
}

func TestSanitizePrompt(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"keeps line structure", "line one\nline two", "line one\nline two"},
		{"crlf collapses to lf", "line one\r\nline two", "line one\nline two"},
		{"tab becomes space", "a\tb", "a b"},
		{"drops escape sequences", "red \x1b[31mtext\x1b[0m", "red [31mtext[0m"},
		{"drops bidi override", "safe‮txet", "safetxet"},
		{"collapses blank runs", "a\n\n\n\nb", "a\n\nb"},
		{"drops leading blanks", "\n\n\nhello", "hello"},
		{"trims trailing blanks", "hello\n\n\n", "hello"},
		{"strips trailing spaces", "hello   \nworld  ", "hello\nworld"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizePrompt(tt.in); got != tt.want {
				t.Errorf("sanitizePrompt(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSanitizePrompt_TruncatesOnRuneBoundary(t *testing.T) {
	in := strings.Repeat("é", MaxPromptRunes+50)
	got := sanitizePrompt(in)
	runes := []rune(got)
	if len(runes) != MaxPromptRunes+1 { // +1 for the ellipsis
		t.Errorf("len = %d runes, want %d", len(runes), MaxPromptRunes+1)
	}
	if runes[len(runes)-1] != '…' {
		t.Errorf("truncated prompt must end with an ellipsis, got %q", string(runes[len(runes)-1]))
	}
	if !utf8.ValidString(got) {
		t.Error("truncation split a multi-byte rune")
	}
}

// TestSanitizePrompt_MultilineSurvivesTitleCollapse pins the difference between
// the two sanitizers: a title is one row, a prompt is a paragraph.
func TestSanitizePrompt_MultilineSurvivesTitleCollapse(t *testing.T) {
	in := "first line\nsecond line"
	if got := sanitizeTitle(in); strings.Contains(got, "\n") {
		t.Errorf("sanitizeTitle kept a newline: %q", got)
	}
	if got := sanitizePrompt(in); !strings.Contains(got, "\n") {
		t.Errorf("sanitizePrompt dropped the line break: %q", got)
	}
}

// List degrades to an empty result on a cancelled context rather than failing.
// Every failure in this package already degrades to fewer sessions, never an
// error that blocks pane creation, and cancellation is the same class of event.
func TestList_CancelledContext_DegradesWithoutError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	sessions, truncated, err := List(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("List returned an error on cancel: %v (want degraded, not failed)", err)
	}
	if len(sessions) != 0 {
		t.Errorf("sessions = %d, want 0", len(sessions))
	}
	if truncated {
		t.Error("truncated set on a cancelled scan")
	}
}

// ReadDetail is the deliberate asymmetry: it returns the error instead of
// degrading. It answers about ONE highlighted row, and a truncated read would
// silently misreport that session's prompt count -- worse than an empty panel.
func TestReadDetail_CancelledContext_ReturnsError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := ReadDetail(ctx, t.TempDir(), "aaaaaaaa-0000-4000-8000-000000000000")
	if err == nil {
		t.Fatal("ReadDetail succeeded on a cancelled context, want an error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want it to wrap context.Canceled", err)
	}
}

// A cancelled scan must not claim it truncated anything.
//
// This also pins WHERE the cancellation check sits. The candidate-gathering
// loop runs over every entry in the directory and stats each one; the title
// loop below it is already capped at MaxSessions. Guarding only the capped loop
// leaves the unbounded one running, and the tell is `truncated`: the cap is
// applied before titles are read, so a scan that reached it did all the stats
// and then reported a truncation it should never have discovered.
func TestListDir_CancelledContext_DoesNotReportTruncation(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < MaxSessions+1; i++ {
		writeSession(t, dir, fmt.Sprintf("s%04d", i),
			base.Add(time.Duration(i)*time.Minute), typedPrompt("prompt"))
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	sessions, truncated, err := listDir(ctx, dir)
	if err != nil {
		t.Fatalf("listDir returned an error on cancel: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("sessions = %d, want 0", len(sessions))
	}
	if truncated {
		t.Error("truncated = true on a cancelled scan; the candidate loop ran to " +
			"completion, so the check is guarding only the capped title loop")
	}
}

// CLAUDE_CONFIG_DIR is Claude Code's own relocation knob: setting it moves
// everything Claude stores, projects/ included. Ignoring it made the session
// picker list nothing for anyone who sets it — indistinguishable from having
// no sessions at all.
func TestConfigDir_PrefersEnv(t *testing.T) {
	// t.TempDir() rather than a "/tmp/..." literal: filepath.Abs prepends a
	// drive letter on Windows, and this repo runs its test binaries natively
	// there as well as in Linux Docker.
	want := t.TempDir()
	t.Setenv(claudeConfigDirEnv, want)
	if got := ConfigDir(); got != want {
		t.Errorf("ConfigDir() = %q, want %q", got, want)
	}
}

func TestConfigDir_FallsBackToHomeDotClaude(t *testing.T) {
	t.Setenv(claudeConfigDirEnv, "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory in this environment")
	}
	if got, want := ConfigDir(), filepath.Join(home, ".claude"); got != want {
		t.Errorf("ConfigDir() = %q, want %q", got, want)
	}
}

func TestConfigDir_ExpandsTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory in this environment")
	}
	t.Setenv(claudeConfigDirEnv, "~/scoped")
	if got, want := ConfigDir(), filepath.Join(home, "scoped"); got != want {
		t.Errorf("ConfigDir() = %q, want %q", got, want)
	}
}

// ProjectDirIn takes the directory so a resolved value cannot be undercut by a
// concurrent Setenv, and so tests need no environment at all.
func TestProjectDirIn_DoesNotConsultEnv(t *testing.T) {
	t.Setenv(claudeConfigDirEnv, filepath.Join(t.TempDir(), "should-not-be-read"))
	got := ProjectDirIn("/explicit", "/work/repo")
	want := filepath.Join("/explicit", "projects", EscapeCWD("/work/repo"))
	if got != want {
		t.Errorf("ProjectDirIn = %q, want %q", got, want)
	}
}

func TestListIn_ReadsTheGivenConfigDir(t *testing.T) {
	cfg := t.TempDir()
	const cwd = "/work/repo"
	dir := filepath.Join(cfg, "projects", EscapeCWD(cwd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"user","promptSource":"typed","message":{"content":"hello there"}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "11111111-2222-3333-4444-555555555555.jsonl"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	sessions, _, err := ListIn(context.Background(), cfg, cwd)
	if err != nil {
		t.Fatalf("ListIn: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("len(sessions) = %d, want 1", len(sessions))
	}
	if sessions[0].Title != "hello there" {
		t.Errorf("Title = %q, want %q", sessions[0].Title, "hello there")
	}
}

// writeSchemaTranscript writes one transcript into a throwaway dir and returns
// its path, for the schema-fallback tests below.
func writeSchemaTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "11111111-2222-3333-4444-555555555555.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// The typed pass runs first and its result wins outright — the regression guard
// for "the fallbacks are additive and change nothing for existing installs".
func TestReadTitle_TypedPromptStillWins(t *testing.T) {
	path := writeSchemaTranscript(t,
		`{"type":"ai-title","aiTitle":"Generated Title","leafUuid":"u1","timestamp":"2026-07-01T10:00:30.000Z"}`,
		`{"type":"user","promptSource":"typed","message":{"content":"the typed one"}}`,
	)
	if got := readTitle(path); got != "the typed one" {
		t.Errorf("readTitle = %q, want the typed prompt to win", got)
	}
}

func TestReadTitle_FallsBackToAiTitle(t *testing.T) {
	path := writeSchemaTranscript(t,
		`{"type":"ai-title","aiTitle":"Refactor the parser","leafUuid":"u1","timestamp":"2026-07-01T10:00:30.000Z"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"sure"}]}}`,
	)
	if got := readTitle(path); got != "Refactor the parser" {
		t.Errorf("readTitle = %q, want the ai-title", got)
	}
}

// The guard that must NOT be added. Reading the pass below this one, it is
// natural to conclude the ai-title pass is missing a matching
// promptSource-absent condition. It is not: measured 2026-08-20, current Claude
// builds emit ai-title entries AND promptSource, so that guard would make the
// pass dead code on every current transcript.
//
// This transcript is the case it would kill — records promptSource, carries an
// ai-title, has no typed prompt. TestReadTitle_FallsBackToAiTitle above does
// not cover it: that fixture has no promptSource at all, so the guard would
// leave it green while breaking this.
func TestReadTitle_AiTitleFiresOnCurrentSchemaWithNoTypedPrompt(t *testing.T) {
	path := writeSchemaTranscript(t,
		`{"type":"ai-title","aiTitle":"Refactor the parser","leafUuid":"u1"}`,
		`{"type":"user","promptSource":"compact","message":{"content":"This session is being continued from a previous conversation..."}}`,
	)
	if got := readTitle(path); got != "Refactor the parser" {
		t.Errorf("readTitle = %q, want the ai-title — the pass must not be gated on promptSource being absent", got)
	}
}

// --- machinery rejection ----------------------------------------------------
//
// The shape-detecting passes promote message CONTENT, so they must first
// establish the content is a prompt. promptSource-absence cannot do that: it is
// a per-entry test, and entries that are not prompts routinely carry no
// promptSource. Three markers separate them, and each fixture below is
// reachable only by the one it targets — a fixture carrying two markers would
// stay green with either filter deleted.

// The PRESERVATION test. It must keep passing through every filter added: an
// old-schema transcript whose first string-content entry is ordinary prose is
// the case the fallback exists to serve.
func TestReadTitle_OldSchemaProsePromptIsPreserved(t *testing.T) {
	path := writeSchemaTranscript(t,
		plainUserEntry("Read docs/global-question-domain.md and summarise it"),
	)
	if got := readTitle(path); got != "Read docs/global-question-domain.md and summarise it" {
		t.Errorf("readTitle = %q, want the real prompt", got)
	}
}

func TestReadTitle_ToolResultIsNotATitle(t *testing.T) {
	path := writeSchemaTranscript(t,
		toolResult("total 9224 -rw-r--r-- 1 artjo"),
		plainUserEntry("the real prompt"),
	)
	if got := readTitle(path); got != "the real prompt" {
		t.Errorf("readTitle = %q, want the real prompt — a tool result is not a prompt", got)
	}
}

func TestReadTitle_MetaEntryIsNotATitle(t *testing.T) {
	path := writeSchemaTranscript(t,
		metaEntry("A session-scoped Stop hook is now active"),
		plainUserEntry("the real prompt"),
	)
	if got := readTitle(path); got != "the real prompt" {
		t.Errorf("readTitle = %q, want the real prompt — isMeta marks claude's own text", got)
	}
}

func TestReadTitle_CommandMarkupIsNotATitle(t *testing.T) {
	path := writeSchemaTranscript(t,
		plainUserEntry("<command-name>/goal</command-name> <command-message>goal</command-message>"),
		plainUserEntry("the real prompt"),
	)
	if got := readTitle(path); got != "the real prompt" {
		t.Errorf("readTitle = %q, want the real prompt — command markup is not a prompt", got)
	}
}

// The denylist must reject CLAUDE's markup, not markup in general. A user who
// pastes HTML has typed a prompt.
func TestReadTitle_UserPastedMarkupIsATitle(t *testing.T) {
	path := writeSchemaTranscript(t,
		plainUserEntry("<html><body>why does this render wrong?</body></html>"),
	)
	if got := readTitle(path); got == "" {
		t.Error("readTitle = empty, want the pasted markup — only claude's own tags are machinery")
	}
}

func TestReadDetail_ToolResultIsNotAPrompt(t *testing.T) {
	assertDetailIgnoresMachinery(t, toolResult("main-agent"))
}

func TestReadDetail_MetaEntryIsNotAPrompt(t *testing.T) {
	assertDetailIgnoresMachinery(t, metaEntry("A session-scoped Stop hook is now active"))
}

func TestReadDetail_CommandMarkupIsNotAPrompt(t *testing.T) {
	assertDetailIgnoresMachinery(t, plainUserEntry("<local-command-stdout>ok</local-command-stdout>"))
}

// assertDetailIgnoresMachinery pins BOTH halves of a rejection in readDetail's
// shape-detecting pass. The count is incremented separately from the text, so a
// text-only assertion can pass while UserPrompts is wrong — which is the
// stronger failure this pass can produce.
func assertDetailIgnoresMachinery(t *testing.T, machinery string) {
	t.Helper()
	path := writeSchemaTranscript(t, machinery, plainUserEntry("the real prompt"))
	d, err := readDetail(context.Background(), path, "11111111-2222-3333-4444-555555555555")
	if err != nil {
		t.Fatalf("readDetail: %v", err)
	}
	if d.UserPrompts != 1 {
		t.Errorf("UserPrompts = %d, want 1 — the machinery entry was counted", d.UserPrompts)
	}
	if d.FirstPrompt != "the real prompt" {
		t.Errorf("FirstPrompt = %q, want %q", d.FirstPrompt, "the real prompt")
	}
}

func TestReadTitle_FallsBackToStringContentUserEntry(t *testing.T) {
	path := writeSchemaTranscript(t,
		`{"type":"user","message":{"content":"no promptSource here"}}`,
	)
	if got := readTitle(path); got != "no promptSource here" {
		t.Errorf("readTitle = %q, want the shape-detected prompt", got)
	}
}

// Array content is a tool result, not a prompt — the shape is what separates
// them when no schema marker is available.
func TestReadTitle_ArrayContentUserEntryIsAToolResult(t *testing.T) {
	path := writeSchemaTranscript(t,
		`{"type":"user","message":{"content":[{"type":"tool_result","text":"exit 0"}]}}`,
	)
	if got := readTitle(path); got != "" {
		t.Errorf("readTitle = %q, want empty — array content is a tool result", got)
	}
}

func TestReadTitle_SidechainExcludedOnEveryPath(t *testing.T) {
	path := writeSchemaTranscript(t,
		`{"type":"user","isSidechain":true,"message":{"content":"subagent chatter"}}`,
	)
	if got := readTitle(path); got != "" {
		t.Errorf("readTitle = %q, want empty — sidechain entries are not the user's conversation", got)
	}
}

// The overbroad-fallback guard. This transcript is CURRENT schema — it records
// promptSource — but has no "typed" entry. Without the promptSource-absent
// condition the fallback would promote a compaction summary to the title.
func TestReadTitle_CurrentSchemaNonTypedEntryIsNotPromoted(t *testing.T) {
	path := writeSchemaTranscript(t,
		`{"type":"user","promptSource":"compact","message":{"content":"This session is being continued from a previous conversation that ran out of context..."}}`,
	)
	if got := readTitle(path); got != "" {
		t.Errorf("readTitle = %q, want empty — a non-typed current-schema entry is not a prompt", got)
	}
}

func TestReadDetail_FallsBackToShapeWhenNoPromptSource(t *testing.T) {
	path := writeSchemaTranscript(t,
		`{"type":"user","timestamp":"2026-08-19T10:00:00Z","message":{"content":"first question"}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","text":"exit 0"}]}}`,
		`{"type":"user","message":{"content":"second question"}}`,
	)
	d, err := readDetail(context.Background(), path, "11111111-2222-3333-4444-555555555555")
	if err != nil {
		t.Fatalf("readDetail: %v", err)
	}
	if d.UserPrompts != 2 {
		t.Errorf("UserPrompts = %d, want 2 (the tool result is not a prompt)", d.UserPrompts)
	}
	if d.FirstPrompt != "first question" {
		t.Errorf("FirstPrompt = %q, want %q", d.FirstPrompt, "first question")
	}
	if d.LastPrompt != "second question" {
		t.Errorf("LastPrompt = %q, want %q", d.LastPrompt, "second question")
	}
}

// A transcript carrying BOTH shapes must be read exactly as before: only the
// typed entry counts, or the fallback has changed behaviour for an existing
// install.
func TestReadDetail_TypedTranscriptDoesNotUseFallback(t *testing.T) {
	path := writeSchemaTranscript(t,
		`{"type":"user","promptSource":"typed","timestamp":"2026-08-19T10:00:00Z","message":{"content":"typed one"}}`,
		`{"type":"user","message":{"content":"untyped noise"}}`,
	)
	d, err := readDetail(context.Background(), path, "11111111-2222-3333-4444-555555555555")
	if err != nil {
		t.Fatalf("readDetail: %v", err)
	}
	if d.UserPrompts != 1 {
		t.Errorf("UserPrompts = %d, want 1 — the typed pass must win outright", d.UserPrompts)
	}
	if d.LastPrompt != "typed one" {
		t.Errorf("LastPrompt = %q, want %q", d.LastPrompt, "typed one")
	}
}

// The detail-side overbroad-fallback guard.
func TestReadDetail_CurrentSchemaWithNoTypedEntriesStaysEmpty(t *testing.T) {
	path := writeSchemaTranscript(t,
		`{"type":"user","promptSource":"slash_command","timestamp":"2026-08-19T10:00:00Z","message":{"content":"/compact"}}`,
		`{"type":"user","promptSource":"compact","message":{"content":"This session is being continued from a previous conversation..."}}`,
	)
	d, err := readDetail(context.Background(), path, "11111111-2222-3333-4444-555555555555")
	if err != nil {
		t.Fatalf("readDetail: %v", err)
	}
	if d.UserPrompts != 0 {
		t.Errorf("UserPrompts = %d, want 0 — non-typed current-schema entries are not prompts", d.UserPrompts)
	}
	if d.FirstPrompt != "" || d.LastPrompt != "" {
		t.Errorf("prompts = (%q, %q), want both empty", d.FirstPrompt, d.LastPrompt)
	}
	if d.Started.IsZero() {
		t.Error("Started is zero — the timestamp scan is independent of prompt classification")
	}
}

// Sidechain entries belong to subagents, so a subagent's ai-title is not this
// session's title. The ai-title pass had no sidechain guard while the pass below
// it did, which made the two asymmetric for no reason.
func TestReadTitle_SidechainAiTitleIsNotPromoted(t *testing.T) {
	path := writeSchemaTranscript(t,
		`{"type":"ai-title","isSidechain":true,"aiTitle":"Subagent task","leafUuid":"u1"}`,
	)
	if got := readTitle(path); got != "" {
		t.Errorf("readTitle = %q, want empty — a sidechain ai-title is a subagent's, not the session's", got)
	}
}

// The ai-title pre-filter is a SUBSTRING match, so an entry merely containing
// that text reaches the unmarshal. Only a real ai-title entry may title the row.
func TestReadTitle_NonAiTitleEntryContainingTheMarkerIsIgnored(t *testing.T) {
	path := writeSchemaTranscript(t,
		`{"type":"assistant","aiTitle":"not mine","message":{"content":[{"type":"text","text":"mentions \"type\":\"ai-title\" in passing"}]}}`,
	)
	if got := readTitle(path); got != "" {
		t.Errorf("readTitle = %q, want empty — only a real ai-title entry may title the session", got)
	}
}

// A relative, non-tilde value is a third branch: neither the tilde expansion
// nor the default fallback, but filepath.Abs. Every caller expects an absolute
// path, so a relative one leaking through would resolve against whatever the
// daemon's working directory happened to be.
func TestConfigDir_AbsolutisesRelativeValue(t *testing.T) {
	t.Setenv(claudeConfigDirEnv, "myconfig")
	got := ConfigDir()
	if !filepath.IsAbs(got) {
		t.Errorf("ConfigDir() = %q, want an absolute path", got)
	}
	if filepath.Base(got) != "myconfig" {
		t.Errorf("ConfigDir() = %q, want it to end in %q", got, "myconfig")
	}
}

// json.Unmarshal accepts JSON null into a *string and reports success, so a
// round-trip test for "is this a string" counted `"content": null` as a prompt.
func TestReadDetail_NullContentIsNotAPrompt(t *testing.T) {
	path := writeSchemaTranscript(t,
		`{"type":"user","timestamp":"2026-08-19T10:00:00Z","message":{"content":null}}`,
		`{"type":"user","message":{"content":"a real one"}}`,
	)
	d, err := readDetail(context.Background(), path, "11111111-2222-3333-4444-555555555555")
	if err != nil {
		t.Fatalf("readDetail: %v", err)
	}
	if d.UserPrompts != 1 {
		t.Errorf("UserPrompts = %d, want 1 — null content is not a prompt", d.UserPrompts)
	}
	if d.FirstPrompt != "a real one" {
		t.Errorf("FirstPrompt = %q, want %q", d.FirstPrompt, "a real one")
	}
}

func TestTranscriptPathIn_DoesNotConsultEnv(t *testing.T) {
	t.Setenv(claudeConfigDirEnv, filepath.Join(t.TempDir(), "should-not-be-read"))
	got := TranscriptPathIn("/explicit", "/work/repo", "abc-123")
	want := filepath.Join("/explicit", "projects", EscapeCWD("/work/repo"), "abc-123.jsonl")
	if got != want {
		t.Errorf("TranscriptPathIn = %q, want %q", got, want)
	}
}

func TestTranscriptPathIn_EmptyArgsReturnEmpty(t *testing.T) {
	if got := TranscriptPathIn("/explicit", "/work/repo", ""); got != "" {
		t.Errorf("empty session id = %q, want empty", got)
	}
	if got := TranscriptPathIn("/explicit", "", "abc-123"); got != "" {
		t.Errorf("empty cwd = %q, want empty", got)
	}
}

// TranscriptPath must DELEGATE rather than carry a second copy of the join —
// two spellings of one path is how they drift.
func TestTranscriptPath_AgreesWithTranscriptPathIn(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv(claudeConfigDirEnv, cfg)
	got := TranscriptPath("/work/repo", "abc-123")
	want := TranscriptPathIn(cfg, "/work/repo", "abc-123")
	if got == "" {
		t.Fatal("TranscriptPath returned empty with the config dir set")
	}
	if got != want {
		t.Errorf("TranscriptPath = %q, TranscriptPathIn = %q; the two must agree", got, want)
	}
}

// The join ReadDetailIn uses must be the one under test, not a parallel copy:
// point TranscriptPathIn at a real file and assert ReadDetailIn reads THAT one.
// Before this, TestTranscriptPath_BuildsJSONLPath certified a join with no
// production caller while the join actually used had no direct test.
func TestReadDetailIn_ReadsPathFromTranscriptPathIn(t *testing.T) {
	cfg := t.TempDir()
	const cwd = "/work/repo"
	const id = "11111111-2222-3333-4444-555555555555"

	path := TranscriptPathIn(cfg, cwd, id)
	if path == "" {
		t.Fatal("TranscriptPathIn returned empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(typedPrompt("the only prompt")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	d, err := ReadDetailIn(context.Background(), cfg, cwd, id)
	if err != nil {
		t.Fatalf("ReadDetailIn: %v", err)
	}
	if d.FirstPrompt != "the only prompt" {
		t.Errorf("FirstPrompt = %q, want %q — ReadDetailIn is not reading the path TranscriptPathIn builds",
			d.FirstPrompt, "the only prompt")
	}
}
