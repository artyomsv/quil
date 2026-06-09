package claudehook

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEnsureScripts_WritesBothVariants(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := EnsureScripts(dir); err != nil {
		t.Fatalf("EnsureScripts: %v", err)
	}
	hookDir := filepath.Join(dir, "claudehook")
	for _, name := range []string{unixScriptName, windowsScriptName} {
		p := filepath.Join(hookDir, name)
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("missing %s: %v", p, err)
		}
		if info.Size() == 0 {
			t.Errorf("%s is empty", p)
		}
	}
}

// TestEnsureScripts_WindowsScriptHasUTF8BOM guards the encoding fix for the
// PowerShell 5.1 hook. Without a UTF-8 BOM, powershell.exe decodes the .ps1
// with the system ANSI codepage and the ✓/… glyphs in notification titles
// mis-decode — a byte landing on the 0x91-0x94 curly-quote range terminates a
// string literal and the whole script fails to parse (observed in the wild as
// "Stop hook error ... Unexpected token"). The .sh twin must stay BOM-free or
// sh tries to execute the BOM as a command.
func TestEnsureScripts_WindowsScriptHasUTF8BOM(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := EnsureScripts(dir); err != nil {
		t.Fatalf("EnsureScripts: %v", err)
	}
	hookDir := filepath.Join(dir, "claudehook")

	ps1, err := os.ReadFile(filepath.Join(hookDir, windowsScriptName))
	if err != nil {
		t.Fatalf("read ps1: %v", err)
	}
	bom := []byte{0xEF, 0xBB, 0xBF}
	if !bytes.HasPrefix(ps1, bom) {
		t.Errorf("%s missing UTF-8 BOM prefix; first bytes = % x", windowsScriptName, ps1[:min(3, len(ps1))])
	}
	// The script must still parse as UTF-8 after the BOM — the bytes
	// following it should contain the original ✓ glyph intact, not a
	// double-encoded BOM or mangled content.
	if !bytes.Contains(ps1, []byte("✓")) {
		t.Errorf("%s missing the ✓ glyph after BOM — content may be corrupted", windowsScriptName)
	}

	sh, err := os.ReadFile(filepath.Join(hookDir, unixScriptName))
	if err != nil {
		t.Fatalf("read sh: %v", err)
	}
	if bytes.HasPrefix(sh, bom) {
		t.Errorf("%s must NOT have a UTF-8 BOM — sh would try to execute it", unixScriptName)
	}
}

func TestEnsureScripts_Idempotent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := EnsureScripts(dir); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := EnsureScripts(dir); err != nil {
		t.Fatalf("second call: %v", err)
	}
}

func TestEnsureScripts_EmptyDir(t *testing.T) {
	t.Parallel()
	if err := EnsureScripts(""); err == nil {
		t.Fatal("expected error for empty quilDir")
	}
}

func TestEnsureScripts_RejectsShellUnsafePath(t *testing.T) {
	t.Parallel()
	for _, p := range []string{
		`/tmp/has"quote/quil`,
		"/tmp/has`backtick/quil",
		"/tmp/has\nnewline/quil",
		"/tmp/has$dollar/quil",
	} {
		if err := EnsureScripts(p); err == nil {
			t.Errorf("EnsureScripts(%q) returned nil, expected rejection", p)
		}
	}
}

func TestValidateQuilDir_AcceptsCommonPaths(t *testing.T) {
	t.Parallel()
	for _, p := range []string{
		"/home/user/.quil",
		`E:\Projects\Stukans\Prototypes\calyx\.quil`,
		`C:\Users\artjo\.quil`,
		"/tmp/quil with spaces",
	} {
		if err := ValidateQuilDir(p); err != nil {
			t.Errorf("ValidateQuilDir(%q) = %v, want nil", p, err)
		}
	}
}

func TestScriptPath_PlatformMatchesBinary(t *testing.T) {
	t.Parallel()
	p := ScriptPath("/tmp/quil")
	want := unixScriptName
	if runtime.GOOS == "windows" {
		want = windowsScriptName
	}
	if filepath.Base(p) != want {
		t.Errorf("ScriptPath base = %q, want %q", filepath.Base(p), want)
	}
}

func TestHookCommand_ContainsScriptPath(t *testing.T) {
	t.Parallel()
	quilDir := filepath.Join("C:", "quil-home")
	if runtime.GOOS != "windows" {
		quilDir = "/tmp/quil-home"
	}
	cmd := HookCommand(quilDir)
	if !strings.Contains(cmd, ScriptPath(quilDir)) {
		t.Errorf("HookCommand %q missing ScriptPath %q", cmd, ScriptPath(quilDir))
	}
}

func TestBuildSettingsJSON_ContainsExpectedKeys(t *testing.T) {
	t.Parallel()
	js, err := BuildSettingsJSON("sh /tmp/hook.sh")
	if err != nil {
		t.Fatalf("BuildSettingsJSON: %v", err)
	}
	// Wire-format check — Claude expects this exact key chain. A future
	// refactor that renames any of these would break Claude silently; the
	// raw-string assertion catches it before tests round-trip through
	// settingsSchema.
	for _, want := range []string{`"hooks"`, `"SessionStart"`, `"type":"command"`, `"command":"sh /tmp/hook.sh"`} {
		if !strings.Contains(js, want) {
			t.Errorf("BuildSettingsJSON missing %q in output: %s", want, js)
		}
	}
}

// TestBuildSettingsJSON_RegistersAllForwardedEvents pins the multi-event
// registration introduced by Phase C. Every name in forwardedHookEvents
// must appear in the JSON so the same hook script is invoked for the full
// notification tier (not just SessionStart). A future contributor who
// removes a name from the slice without expecting the JSON to shrink will
// see this test fail with the missing name.
func TestBuildSettingsJSON_RegistersAllForwardedEvents(t *testing.T) {
	t.Parallel()
	js, err := BuildSettingsJSON("sh /tmp/hook.sh")
	if err != nil {
		t.Fatalf("BuildSettingsJSON: %v", err)
	}
	for _, name := range forwardedHookEvents {
		if !strings.Contains(js, `"`+name+`"`) {
			t.Errorf("BuildSettingsJSON missing event registration %q in output: %s", name, js)
		}
	}
}

// TestForwardedHookEvents_NoDuplicates guards a silent footgun: the
// BuildSettingsJSON loop overwrites by name in the Hooks map, so a
// duplicate entry would dedupe without warning. This test catches that at
// build time rather than at the first Claude session.
func TestForwardedHookEvents_NoDuplicates(t *testing.T) {
	t.Parallel()
	seen := make(map[string]bool)
	for _, name := range forwardedHookEvents {
		if seen[name] {
			t.Errorf("duplicate entry in forwardedHookEvents: %q", name)
		}
		seen[name] = true
	}
}

func TestBuildSettingsJSON_EscapesQuotesInCommand(t *testing.T) {
	t.Parallel()
	js, err := BuildSettingsJSON(`sh "/tmp/with quotes/hook.sh"`)
	if err != nil {
		t.Fatalf("BuildSettingsJSON: %v", err)
	}
	if !strings.Contains(js, `\"/tmp/with quotes/hook.sh\"`) {
		t.Errorf("expected JSON-escaped quotes in: %s", js)
	}
}

func TestReadPersistedSessionID_Missing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, _, err := ReadPersistedSessionID(dir, "pane-missing")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want ErrNotExist", err)
	}
}

func TestReadPersistedSessionID_Present(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sessionsDir := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0700); err != nil {
		t.Fatal(err)
	}
	want := "11111111-2222-3333-4444-555555555555"
	if err := os.WriteFile(filepath.Join(sessionsDir, "pane-abc.id"), []byte(want+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, mtime, err := ReadPersistedSessionID(dir, "pane-abc")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if mtime.IsZero() {
		t.Error("mtime is zero")
	}
}

func TestReadPersistedSessionID_TrimsWhitespaceAndCRLF(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sessionsDir := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0700); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"lf", "abc123\n", "abc123"},
		{"crlf (windows hook)", "abc123\r\n", "abc123"},
		{"leading and trailing spaces", "  abc123  \n", "abc123"},
		{"crlf with spaces", "  abc123  \r\n", "abc123"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := filepath.Join(sessionsDir, "pane-"+tt.name+".id")
			if err := os.WriteFile(f, []byte(tt.content), 0600); err != nil {
				t.Fatal(err)
			}
			got, _, err := ReadPersistedSessionID(dir, "pane-"+tt.name)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReadPersistedSessionID_EmptyArgs(t *testing.T) {
	t.Parallel()
	if _, _, err := ReadPersistedSessionID("", "pane"); err == nil {
		t.Error("expected error for empty quilDir")
	}
	if _, _, err := ReadPersistedSessionID("/tmp", ""); err == nil {
		t.Error("expected error for empty paneID")
	}
}

func TestReadPersistedSessionID_RejectsPathTraversal(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{
		"../../etc/passwd",
		"..",
		"a/b",
		`a\b`,
		"pane..id",
	} {
		if _, _, err := ReadPersistedSessionID("/tmp", bad); err == nil {
			t.Errorf("ReadPersistedSessionID(_, %q) returned nil, expected rejection", bad)
		}
	}
}

func TestReadPersistedSessionID_CapsLargeFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sessionsDir := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0700); err != nil {
		t.Fatal(err)
	}
	// 10 KiB of garbage — Read should cap at 256 bytes via LimitReader.
	junk := strings.Repeat("x", 10*1024)
	if err := os.WriteFile(filepath.Join(sessionsDir, "pane-big.id"), []byte(junk), 0600); err != nil {
		t.Fatal(err)
	}
	got, _, err := ReadPersistedSessionID(dir, "pane-big")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > 256 {
		t.Errorf("ReadPersistedSessionID returned %d bytes, expected <= 256", len(got))
	}
}
