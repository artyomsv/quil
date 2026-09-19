package daemon

import (
	"bytes"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// syncBuffer collects log output. claudeHookSpawnPrep logs on the calling
// goroutine, but log.SetOutput is process-wide, so guard the buffer anyway.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func captureHookPrepLog(t *testing.T, userArgs []string) (string, []string) {
	t.Helper()

	quilDir := t.TempDir()
	prevExe := quildExeFn
	quildExeFn = func() (string, error) { return filepath.Join(quilDir, "quild"), nil }
	t.Cleanup(func() { quildExeFn = prevExe })

	var out syncBuffer
	prevOut := log.Writer()
	log.SetOutput(&out)
	t.Cleanup(func() { log.SetOutput(prevOut) })

	prefix, _ := claudeHookSpawnPrep(hostHookPaths(quilDir), "pane-abcdef01", "", userArgs)
	return out.String(), prefix
}

// Before #221 the three refusal paths each logged "claude hooks disabled" and
// the success path logged nothing at all, so `grep -i hook quild.log` could not
// tell a live hook from one that had never been registered. Every outcome must
// now leave a line.
func TestClaudeHookSpawnPrep_LogsOnSuccess(t *testing.T) {
	got, prefix := captureHookPrepLog(t, nil)

	if len(prefix) != 2 || prefix[0] != "--settings" {
		t.Fatalf("expected a --settings prefix, got %v", prefix)
	}
	if !strings.Contains(got, "claude hooks registered") {
		t.Fatalf("a successful registration logged no positive evidence: %q", got)
	}
	if !strings.Contains(got, prefix[1]) {
		t.Fatalf("log does not name the settings file it wrote (%s): %q", prefix[1], got)
	}
}

// Quil PREPENDS its own --settings. When the plugin's args already carry one,
// which file claude honours is unverified — so the hook may never become
// active. Claiming "registered" there hands a troubleshooting run a positive
// confirmation for a hook that is not running, which is the same ambiguity the
// success line was added to remove.
func TestClaudeHookSpawnPrep_DoesNotClaimRegisteredWhenSettingsAreContested(t *testing.T) {
	got, prefix := captureHookPrepLog(t, []string{"--settings", "/tmp/theirs.json"})

	if len(prefix) != 2 || prefix[0] != "--settings" {
		t.Fatalf("expected quil to still pass its own --settings, got %v", prefix)
	}
	if strings.Contains(got, "claude hooks registered") {
		t.Fatalf("claimed registration although the caller's own --settings may "+
			"take precedence; the hook may not be active: %q", got)
	}
	if !strings.Contains(got, "unverified") {
		t.Fatalf("contested --settings left no warning at all: %q", got)
	}
	if !strings.Contains(got, prefix[1]) {
		t.Fatalf("warning does not name the settings file it wrote (%s): %q", prefix[1], got)
	}
}
