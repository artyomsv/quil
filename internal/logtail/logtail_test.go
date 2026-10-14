package logtail

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestTail_ShortFileReturnsAll(t *testing.T) {
	p := filepath.Join(t.TempDir(), "small.log")
	content := "line1\nline2\nline3\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Tail(p, 1024)
	if err != nil || got != content {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestTail_LargeFileStartsAtALine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "big.log")
	if err := os.WriteFile(p, []byte(strings.Repeat("X\nY\n", 1024)), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Tail(p, 256)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, TruncatedMarker) || len(got) > 256+len(TruncatedMarker) {
		t.Fatalf("got %d bytes: %q", len(got), got[:40])
	}
	body := strings.TrimPrefix(got, TruncatedMarker)
	if !strings.HasPrefix(body, "X\n") && !strings.HasPrefix(body, "Y\n") {
		t.Fatalf("partial first line: %q", body[:8])
	}
}

// With no newline in the window, the cut moves to a whole character.
func TestTail_NoNewlineCutsAtAWholeCharacter(t *testing.T) {
	p := filepath.Join(t.TempDir(), "wide.log")
	if err := os.WriteFile(p, []byte(strings.Repeat("é", 400)), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Tail(p, 101) // odd: the window starts inside a 2-byte rune
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(strings.TrimPrefix(got, TruncatedMarker)) {
		t.Fatal("the tail starts inside a character")
	}
}

func TestTail_RefusesNonRegular(t *testing.T) {
	dir := t.TempDir()
	if _, err := Tail(dir, 1024); err == nil {
		t.Fatal("a directory was read")
	}
	target := filepath.Join(dir, "real.log")
	_ = os.WriteFile(target, []byte("x\n"), 0o600)
	link := filepath.Join(dir, "link.log")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := Tail(link, 1024); err == nil {
		t.Fatal("a link was followed")
	}
}

func TestTail_MissingIsNotExist(t *testing.T) {
	_, err := Tail(filepath.Join(t.TempDir(), "none.log"), 10)
	if !os.IsNotExist(err) {
		t.Fatalf("err = %v", err)
	}
}

func TestJoinDir_NewestFirstAndShared(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	for i, name := range []string{"old.log", "new.log", "skip.txt"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(name+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		mt := now.Add(time.Duration(i) * time.Minute)
		_ = os.Chtimes(p, mt, mt)
	}
	text, n, err := JoinDir(dir, MaxBytes)
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if strings.Index(text, "new.log") > strings.Index(text, "old.log") || strings.Contains(text, "skip.txt") {
		t.Fatalf("order or filter wrong:\n%s", text)
	}
}

func TestJoinDir_EmptyAndMissing(t *testing.T) {
	if _, n, err := JoinDir(t.TempDir(), MaxBytes); err != nil || n != 0 {
		t.Fatalf("empty: %d %v", n, err)
	}
	if _, _, err := JoinDir(filepath.Join(t.TempDir(), "none"), MaxBytes); err == nil {
		t.Fatal("a missing dir read as empty")
	}
}

func TestJoinDir_PerFileFloor(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 100; i++ {
		_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("p%03d.log", i)), []byte(strings.Repeat("z\n", 8000)), 0o600)
	}
	text, n, err := JoinDir(dir, MaxBytes)
	if err != nil || n != 100 {
		t.Fatal(n, err)
	}
	// Each file gets at least 4 KiB, as in the TUI: 100 files may exceed MaxBytes.
	if len(text) < 100*4096/2 {
		t.Fatalf("only %d bytes for 100 files", len(text))
	}
}
