package codexhook

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSessionRecord_RoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := HookEnv{PaneID: "pane-abc", QuilDir: dir}
	if err := writeSessionFile(env, "01a05db1-9f44-73b2-b426-8aad5f5232f4", `C:\Users\x\.codex\sessions\2026\09\01\rollout-2026-09-01T17-58-36-01a05db1-9f44-73b2-b426-8aad5f5232f4.jsonl`); err != nil {
		t.Fatal(err)
	}
	rec, err := ReadPersistedSession(dir, "pane-abc")
	if err != nil {
		t.Fatal(err)
	}
	if rec.ID != "01a05db1-9f44-73b2-b426-8aad5f5232f4" {
		t.Errorf("ID = %q", rec.ID)
	}
	if want := `C:\Users\x\.codex\sessions\2026\09\01\rollout-2026-09-01T17-58-36-01a05db1-9f44-73b2-b426-8aad5f5232f4.jsonl`; rec.TranscriptPath != want {
		t.Errorf("TranscriptPath = %q, want %q (recorded verbatim)", rec.TranscriptPath, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "sessions", "codex-pane-abc.id")); err != nil {
		t.Errorf("record must live at sessions/codex-<paneID>.id: %v", err)
	}
	id, _, err := ReadPersistedSessionID(dir, "pane-abc")
	if err != nil || id != rec.ID {
		t.Errorf("ReadPersistedSessionID = %q, %v", id, err)
	}
}

func TestSessionRecord_MissingIsNotExist(t *testing.T) {
	t.Parallel()
	_, err := ReadPersistedSession(t.TempDir(), "pane-none")
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, want os.ErrNotExist", err)
	}
}

func TestWriteSessionFile_RejectsNonUUIDAndNewlinePath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := HookEnv{PaneID: "pane-abc", QuilDir: dir}
	if err := writeSessionFile(env, "not-a-uuid", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sessions", "codex-pane-abc.id")); !errors.Is(err, os.ErrNotExist) {
		t.Error("a non-uuid id must not be recorded")
	}
	if err := writeSessionFile(env, "01a05db1-9f44-73b2-b426-8aad5f5232f4", "evil\nline"); err != nil {
		t.Fatal(err)
	}
	rec, err := ReadPersistedSession(dir, "pane-abc")
	if err != nil {
		t.Fatal(err)
	}
	if rec.ID != "01a05db1-9f44-73b2-b426-8aad5f5232f4" {
		t.Errorf("id must survive a dropped path, got %q", rec.ID)
	}
	if rec.TranscriptPath != "" {
		t.Errorf("a path with a newline must be dropped, got %q", rec.TranscriptPath)
	}
}

func TestReadPersistedSession_RejectsBadPaneID(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"", "../x", `a\b`, "a/b", "bad\nid"} {
		if _, err := ReadPersistedSession(t.TempDir(), id); err == nil {
			t.Errorf("paneID %q accepted", id)
		}
	}
	if _, err := ReadPersistedSession("", "pane-abc"); err == nil {
		t.Error("empty quilDir accepted")
	}
}

func TestReadPersistedSession_RejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privilege on Windows")
	}
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target.id")
	if err := os.WriteFile(target, []byte("01a05db1-9f44-73b2-b426-8aad5f5232f4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "sessions", "codex-pane-abc.id")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPersistedSession(dir, "pane-abc"); err == nil {
		t.Error("symlinked record must be refused")
	}
}

func TestIsValidSessionID(t *testing.T) {
	t.Parallel()
	if !IsValidSessionID("01a05db1-9f44-73b2-b426-8aad5f5232f4") {
		t.Error("canonical uuid rejected")
	}
	for _, bad := range []string{"", "--last", "01a05db19f4473b2b4268aad5f5232f4", "x\n", "01a05db1-9f44-73b2-b426-8aad5f5232f4 ", "01a05db1-9f44-73b2-b426-8aad5f5232f4\n"} {
		if IsValidSessionID(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}
