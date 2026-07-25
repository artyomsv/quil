package claudesessions

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	got, _, err := listDir(filepath.Join(t.TempDir(), "does-not-exist"))
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

	got, truncated, err := listDir(dir)
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

	got, _, err := listDir(dir)
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

	got, _, err := listDir(dir)
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

	got, _, err := listDir(dir)
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

	got, _, err := listDir(dir)
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

	got, _, err := listDir(dir)
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

	got, _, err := listDir(dir)
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

	got, _, err := listDir(dir)
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

	got, _, err := listDir(dir)
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

	got, truncated, err := listDir(dir)
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
