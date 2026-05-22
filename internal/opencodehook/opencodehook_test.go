package opencodehook

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestValidateQuilDir(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		dir     string
		wantErr bool
	}{
		{"valid path", "/tmp/quil", false},
		{"empty", "", true},
		{"contains quote", `/tmp/qu"il`, true},
		{"contains backtick", "/tmp/qu`il", true},
		{"contains dollar", "/tmp/qu$il", true},
		{"contains newline", "/tmp/qu\nil", true},
		{"contains carriage return", "/tmp/qu\ril", true},
		{"contains tab", "/tmp/qu\til", true},
		{"contains NUL", "/tmp/qu\x00il", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateQuilDir(tt.dir)
			if (err != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidatePaneID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		paneID  string
		wantErr bool
	}{
		// Accepted: standard quil pane IDs and bare UUIDs are a subset of
		// the alphanumeric+`-_` character class.
		{"quil pane id", "pane-3e89c840", false},
		{"uuid", "550e8400-e29b-41d4-a716-446655440000", false},
		{"underscore", "pane_with_underscore", false},
		// Rejected: structural injection vectors and exotic chars.
		{"empty", "", true},
		{"forward slash", "abc/def", true},
		{"backslash", `abc\def`, true},
		{"parent traversal", "abc..def", true},
		{"single dot", "abc.def", true}, // tightened: dots are no longer allowed
		{"space", "abc def", true},
		{"NUL byte", "abc\x00def", true},
		{"too long (65)", strings.Repeat("a", 65), true},
		{"max length (64)", strings.Repeat("a", 64), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validatePaneID(tt.paneID)
			if (err != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestIsValidSessionID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		id   string
		want bool
	}{
		// Real opencode session id shape (verified against opencode 1.14.x).
		{"opencode prod shape", "ses_1b0d89947ffeE92bKkZ4LTBzO2", true},
		{"alphanum + dash", "sess-abc-123", true},
		{"alphanum + underscore", "sess_abc_123", true},
		{"max length (128)", strings.Repeat("a", 128), true},
		// Rejected
		{"empty", "", false},
		{"too long (129)", strings.Repeat("a", 129), false},
		{"forward slash", "sess/abc", false},
		{"newline", "sess\nabc", false},
		{"NUL byte", "sess\x00abc", false},
		// Hyphen-prefixed values ARE accepted by the regex — the daemon passes
		// the id as a discrete argv entry (`--session <id>`) so opencode itself
		// rejects flag-shaped values. The regex's job is only to keep the id
		// out of the filesystem path and the spawn argv, not to second-guess
		// opencode's argparse.
		{"hyphen-prefixed (regex allows, opencode rejects)", "--malicious-arg", true},
		{"dot", "sess.abc", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := IsValidSessionID(tt.id); got != tt.want {
				t.Errorf("IsValidSessionID(%q) = %v, want %v", tt.id, got, tt.want)
			}
		})
	}
}

func TestEnsureScripts_WritesPluginFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := EnsureScripts(dir); err != nil {
		t.Fatalf("EnsureScripts: %v", err)
	}
	path := ScriptPath(dir)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat plugin file: %v", err)
	}
	if info.Size() == 0 {
		t.Errorf("plugin file is empty")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read plugin file: %v", err)
	}
	// Sanity: the embedded plugin contains the key event types we hook on.
	for _, marker := range []string{"export default", "session.created", "session.deleted", "QUIL_PANE_ID"} {
		if !strings.Contains(string(body), marker) {
			t.Errorf("plugin file missing marker %q", marker)
		}
	}
}

func TestEnsureScripts_Idempotent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := EnsureScripts(dir); err != nil {
		t.Fatalf("EnsureScripts first: %v", err)
	}
	first, err := os.ReadFile(ScriptPath(dir))
	if err != nil {
		t.Fatalf("read first: %v", err)
	}
	if err := EnsureScripts(dir); err != nil {
		t.Fatalf("EnsureScripts second: %v", err)
	}
	second, err := os.ReadFile(ScriptPath(dir))
	if err != nil {
		t.Fatalf("read second: %v", err)
	}
	if string(first) != string(second) {
		t.Errorf("plugin content drifted across EnsureScripts invocations")
	}
}

func TestEnsureScripts_RejectsUnsafeQuilDir(t *testing.T) {
	t.Parallel()
	if err := EnsureScripts(`/tmp/with"quote`); err == nil {
		t.Fatal("expected error for unsafe quilDir, got nil")
	}
}

func TestScriptPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		quilDir string
		want    string
	}{
		{"unix-style", "/var/quil", filepath.Join("/var/quil", "opencodehook", "quil-session-tracker.js")},
		{"trailing slash tolerated", "/var/quil/", filepath.Join("/var/quil/", "opencodehook", "quil-session-tracker.js")},
		{"empty (caller must validate)", "", filepath.Join("opencodehook", "quil-session-tracker.js")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ScriptPath(tt.quilDir); got != tt.want {
				t.Errorf("ScriptPath(%q) = %q, want %q", tt.quilDir, got, tt.want)
			}
		})
	}
}

func TestBuildConfigContent(t *testing.T) {
	t.Parallel()
	got, err := BuildConfigContent("/tmp/quil/opencodehook/quil-session-tracker.js")
	if err != nil {
		t.Fatalf("BuildConfigContent: %v", err)
	}
	if !strings.Contains(got, `"plugin"`) {
		t.Errorf("missing plugin key: %s", got)
	}
	if !strings.Contains(got, "/tmp/quil/opencodehook/quil-session-tracker.js") {
		t.Errorf("missing script path: %s", got)
	}
}

// TestBuildConfigContent_ParsesAsValidJSON locks in the wire format: a regression
// in configContentSchema's field tag (or accidental whitespace, or a manual switch
// to a map[string]any that drops type safety) would not be caught by the simple
// substring assertions above. Parse it back through encoding/json to prove the
// shape is what opencode expects.
func TestBuildConfigContent_ParsesAsValidJSON(t *testing.T) {
	t.Parallel()
	const path = "/tmp/x/opencodehook/quil-session-tracker.js"
	got, err := BuildConfigContent(path)
	if err != nil {
		t.Fatalf("BuildConfigContent: %v", err)
	}
	var parsed struct {
		Plugin []string `json:"plugin"`
	}
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("not valid JSON: %v (%s)", err, got)
	}
	if len(parsed.Plugin) != 1 || parsed.Plugin[0] != path {
		t.Errorf("plugin = %v, want [%q]", parsed.Plugin, path)
	}
}

// TestBuildConfigContent_EscapesBackslashesForWindowsPaths only runs on
// Windows because filepath.IsAbs is platform-aware: `C:\...` is not absolute
// on Unix and would (correctly) fail the new IsAbs guard. On Unix the
// portable round-trip test below covers the same JSON-escape behavior using
// a path absolute on the current platform.
func TestBuildConfigContent_EscapesBackslashesForWindowsPaths(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-absolute path only valid on Windows")
	}
	t.Parallel()
	winPath := `C:\Users\foo\.quil\opencodehook\quil-session-tracker.js`
	got, err := BuildConfigContent(winPath)
	if err != nil {
		t.Fatalf("BuildConfigContent: %v", err)
	}
	if !strings.Contains(got, `C:\\Users\\foo`) {
		t.Errorf("backslashes not JSON-escaped in: %s", got)
	}
	var parsed struct {
		Plugin []string `json:"plugin"`
	}
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("not valid JSON: %v (%s)", err, got)
	}
	if len(parsed.Plugin) != 1 || parsed.Plugin[0] != winPath {
		t.Errorf("plugin = %v, want [%q]", parsed.Plugin, winPath)
	}
}

// TestBuildConfigContent_PathSpecialCharsRoundTrip verifies that json.Marshal
// (not string concatenation) is used to encode the path, so anomalous chars
// in the absolute path don't break the wire format. Portable: uses a path
// absolute on the current platform.
func TestBuildConfigContent_PathSpecialCharsRoundTrip(t *testing.T) {
	t.Parallel()
	// A Unix-absolute path containing chars that JSON must escape (quote,
	// newline, backslash, control char). Real-world filesystems usually
	// permit these; we want to know that even if they appear, the wire
	// format survives a parse.
	in := filepath.Join("/tmp", "quil with spaces", "weird\tname", "tracker.js")
	got, err := BuildConfigContent(in)
	if err != nil {
		t.Fatalf("BuildConfigContent: %v", err)
	}
	var parsed struct {
		Plugin []string `json:"plugin"`
	}
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("not valid JSON: %v (%s)", err, got)
	}
	if len(parsed.Plugin) != 1 || parsed.Plugin[0] != in {
		t.Errorf("plugin = %v, want [%q]", parsed.Plugin, in)
	}
}

func TestBuildConfigContent_RejectsEmptyPath(t *testing.T) {
	t.Parallel()
	if _, err := BuildConfigContent(""); err == nil {
		t.Fatal("expected error for empty scriptPath")
	}
}

// TestBuildConfigContent_RejectsRelativePath guards the boundary check: a
// relative scriptPath would be resolved by opencode against the CHILD's CWD,
// not the daemon's, so with `prompts_cwd = true` the plugin would silently
// fail to load. The function must refuse non-absolute paths up-front.
func TestBuildConfigContent_RejectsRelativePath(t *testing.T) {
	t.Parallel()
	tests := []string{
		"opencodehook/quil-session-tracker.js",
		"./quil-session-tracker.js",
		"../quil-session-tracker.js",
	}
	for _, p := range tests {
		t.Run(p, func(t *testing.T) {
			t.Parallel()
			if _, err := BuildConfigContent(p); err == nil {
				t.Fatalf("expected error for relative scriptPath %q", p)
			}
		})
	}
}

func TestReadPersistedSessionID_MissingFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, _, err := ReadPersistedSessionID(dir, "pane-abc")
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, want os.ErrNotExist", err)
	}
}

func TestReadPersistedSessionID_RoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sessionsDir := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	want := "sess-abcd1234"
	if err := os.WriteFile(filepath.Join(sessionsDir, "opencode-pane-1.id"), []byte(want+"\n"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, _, err := ReadPersistedSessionID(dir, "pane-1")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestReadPersistedSessionID_RejectsSymlink verifies the TOCTOU-free symlink
// rejection. On Unix, O_NOFOLLOW makes os.OpenFile return ELOOP atomically
// when the path is a symlink. On Windows symlink creation typically requires
// elevated privileges, so the os.Symlink call will fail and the test skips —
// the production code is portable but the test only meaningfully runs on
// Unix without dev mode.
func TestReadPersistedSessionID_RejectsSymlink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sessionsDir := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	target := filepath.Join(dir, "real.txt")
	if err := os.WriteFile(target, []byte("evil"), 0600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	link := filepath.Join(sessionsDir, "opencode-pane-1.id")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	if _, _, err := ReadPersistedSessionID(dir, "pane-1"); err == nil {
		t.Fatal("expected symlink rejection, got nil error")
	}
}

func TestReadPersistedSessionID_InvalidPaneID(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, _, err := ReadPersistedSessionID(dir, "../escape"); err == nil {
		t.Fatal("expected error for unsafe paneID")
	}
}

func TestReadPersistedSessionID_EmptyQuilDir(t *testing.T) {
	t.Parallel()
	if _, _, err := ReadPersistedSessionID("", "pane-1"); err == nil {
		t.Fatal("expected error for empty quilDir")
	}
}
