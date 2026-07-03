package claudehook

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// writeTranscript writes lines to a .jsonl file in a temp dir and returns its path.
func writeTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return path
}

func assistantLine(model string, input, cacheRead, cacheCreate int64, sidechain bool) string {
	side := "false"
	if sidechain {
		side = "true"
	}
	return `{"type":"assistant","isSidechain":` + side + `,"message":{"model":"` + model + `",` +
		`"usage":{"input_tokens":` + strconv.FormatInt(input, 10) +
		`,"cache_read_input_tokens":` + strconv.FormatInt(cacheRead, 10) +
		`,"cache_creation_input_tokens":` + strconv.FormatInt(cacheCreate, 10) + `,"output_tokens":10}}}`
}

func TestReadTranscriptUsage_LastAssistantWins(t *testing.T) {
	t.Parallel()
	path := writeTranscript(t,
		`{"type":"user","message":{"content":"hi"}}`,
		assistantLine("claude-sonnet-5", 5, 1000, 50, false),
		`{"type":"user","message":{"content":"more"}}`,
		assistantLine("claude-sonnet-5", 2, 200000, 300, false),
	)
	model, tokens, ok := readTranscriptUsage(path)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if model != "claude-sonnet-5" {
		t.Errorf("model = %q", model)
	}
	if want := int64(2 + 200000 + 300); tokens != want {
		t.Errorf("tokens = %d, want %d", tokens, want)
	}
}

func TestReadTranscriptUsage_SkipsSidechain(t *testing.T) {
	t.Parallel()
	path := writeTranscript(t,
		assistantLine("claude-opus-4-8", 3, 500000, 100, false),
		assistantLine("claude-haiku-4-5", 1, 9000, 0, true), // subagent — must be skipped
	)
	model, tokens, ok := readTranscriptUsage(path)
	if !ok || model != "claude-opus-4-8" {
		t.Fatalf("model = %q ok=%v, want main-conversation claude-opus-4-8", model, ok)
	}
	if want := int64(3 + 500000 + 100); tokens != want {
		t.Errorf("tokens = %d, want %d", tokens, want)
	}
}

func TestReadTranscriptUsage_SkipsMalformedTail(t *testing.T) {
	t.Parallel()
	path := writeTranscript(t,
		assistantLine("claude-sonnet-5", 1, 100, 0, false),
		`{"type":"assistant","message":{"model":`, // truncated write
	)
	model, _, ok := readTranscriptUsage(path)
	if !ok || model != "claude-sonnet-5" {
		t.Fatalf("model = %q ok=%v, want fallback to previous valid line", model, ok)
	}
}

func TestReadTranscriptUsage_Rejections(t *testing.T) {
	t.Parallel()
	valid := writeTranscript(t, assistantLine("m", 1, 1, 1, false))
	tests := []struct {
		name string
		path string
	}{
		{"missing file", filepath.Join(t.TempDir(), "nope.jsonl")},
		{"relative path", "session.jsonl"},
		{"wrong extension", strings.TrimSuffix(valid, ".jsonl") + ".txt"},
		{"empty path", ""},
		{"directory", t.TempDir() + string(os.PathSeparator) + "d.jsonl"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "directory" {
				if err := os.MkdirAll(tt.path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, ok := readTranscriptUsage(tt.path); ok {
				t.Errorf("readTranscriptUsage(%q) ok = true, want false", tt.path)
			}
		})
	}
}

func TestReadTranscriptUsage_ExpandsTildePath(t *testing.T) {
	// Claude Code sends transcript_path as "~/.claude/projects/.../x.jsonl"
	// (literal tilde) — regression for the silent no-data Stop events.
	home := t.TempDir()
	t.Setenv("HOME", home)        // os.UserHomeDir on Unix
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	sub := filepath.Join(home, ".claude", "projects", "p")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(sub, "s.jsonl")
	if err := os.WriteFile(full, []byte(assistantLine("claude-fable-5", 5, 80000, 400, false)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	model, tokens, ok := readTranscriptUsage("~/.claude/projects/p/s.jsonl")
	if !ok || model != "claude-fable-5" || tokens != 80405 {
		t.Fatalf("got model=%q tokens=%d ok=%v, want claude-fable-5/80405/true", model, tokens, ok)
	}
}

func TestReadTranscriptUsage_NoAssistantEntry(t *testing.T) {
	t.Parallel()
	path := writeTranscript(t,
		`{"type":"user","message":{"content":"hello"}}`,
		`{"type":"system","content":"assistant mentioned in text"}`,
	)
	if _, _, ok := readTranscriptUsage(path); ok {
		t.Error("ok = true for transcript with no assistant entries")
	}
}

func TestReadTranscriptUsage_TailWindowOnLargeFile(t *testing.T) {
	t.Parallel()
	// The target assistant line sits at the END of a file larger than the
	// tail window; the seek must land mid-file, drop the partial line, and
	// still find it.
	filler := `{"type":"user","message":{"content":"` + strings.Repeat("x", 1000) + `"}}`
	lines := make([]string, 0, 400)
	for range 400 { // ~400 KB of filler > transcriptTailBytes (256 KB)
		lines = append(lines, filler)
	}
	lines = append(lines, assistantLine("claude-opus-4-8", 2, 600000, 1000, false))
	path := writeTranscript(t, lines...)
	model, tokens, ok := readTranscriptUsage(path)
	if !ok || model != "claude-opus-4-8" || tokens != 601002 {
		t.Fatalf("got model=%q tokens=%d ok=%v", model, tokens, ok)
	}
}
