package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func sandboxPane(t *testing.T, image string) *Pane {
	t.Helper()
	return &Pane{ID: "pane1", SandboxImage: image}
}

// A host pane's recorded path is already a host path and must pass through
// untouched — the rewrite is scoped to panes that ran in a container.
func TestHostTranscriptPath_HostPaneIsUnchanged(t *testing.T) {
	p := sandboxPane(t, "")
	in := filepath.Join("/home/u/.claude", "projects", "-w", "id.jsonl")
	if got := hostTranscriptPath(p, in); got != in {
		t.Errorf("hostTranscriptPath rewrote a host pane's path: %q", got)
	}
}

// The whole reason this function exists: a sandbox pane's hook records what
// the CONTAINER saw, and every consumer stats on the host. Left alone, every
// candidate reads missing, nothing is located, and a restart spawns
// --session-id against an id whose transcript exists — exit 129.
func TestHostTranscriptPath_RewritesAContainerPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QUIL_HOME", home)
	p := sandboxPane(t, "img")
	// The containment check now resolves symlinks, so the config directory has
	// to exist — a link planted under an agent-writable tree is what it
	// defends against, and a path that cannot be resolved at all is refused.
	if err := os.MkdirAll(sandboxClaudeConfigDir(home, p.ID), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	got := hostTranscriptPath(p, "/quil/claude/projects/-work-wt/abc.jsonl")
	if got == "" {
		t.Fatal("a legitimate container path was rejected")
	}
	want := filepath.Join(home, "sandbox", "panes", "pane1", "claude", "projects", "-work-wt", "abc.jsonl")
	if got != want {
		t.Errorf("hostTranscriptPath = %q, want %q", got, want)
	}
}

// The path is written by the agent and the consumer is a budgeted host stat,
// so an unvalidated value is an existence oracle for any host path ending
// <id>.jsonl — and a blocking-FS permit spent on a dead mount every restart.
func TestHostTranscriptPath_RejectsPathsOutsideTheContainerConfigDir(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	p := sandboxPane(t, "img")

	for _, in := range []string{
		"/etc/passwd",
		"/quil/events/abc.jsonl",
		`C:\Users\someone\.claude\projects\x\abc.jsonl`,
		"/quilclaude/abc.jsonl", // prefix must be a directory boundary
	} {
		if got := hostTranscriptPath(p, in); got != "" {
			t.Errorf("hostTranscriptPath(%q) = %q, want \"\" — a path outside the pane's config dir was accepted", in, got)
		}
	}
}

// filepath.Join CLEANS a traversal rather than refusing it, so the containment
// test has to run on the RESULT. A path that climbs back out must be rejected
// even though it starts with the right prefix.
func TestHostTranscriptPath_RejectsTraversalAfterJoin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QUIL_HOME", home)
	p := sandboxPane(t, "img")

	got := hostTranscriptPath(p, "/quil/claude/../../../../etc/passwd")
	if got != "" {
		t.Errorf("a traversal escaped the pane's config dir: %q", got)
	}
}

func TestHostTranscriptPath_EmptyStaysEmpty(t *testing.T) {
	if got := hostTranscriptPath(sandboxPane(t, "img"), ""); got != "" {
		t.Errorf("hostTranscriptPath(\"\") = %q", got)
	}
}

// A sandbox pane's Claude data lives under its own tree, not the daemon's
// ~/.claude — which is what the resume rewrite above translates into.
func TestSandboxClaudeConfigDir(t *testing.T) {
	got := filepath.ToSlash(sandboxClaudeConfigDir("/home/u/.quil", "pane1"))
	if !strings.HasSuffix(got, "sandbox/panes/pane1/claude") {
		t.Errorf("sandboxClaudeConfigDir = %q, want the pane's own config dir", got)
	}
}

// --- forwarder ---

func forwardFixture(t *testing.T) (*spoolForwarder, string, string) {
	t.Helper()
	dir := t.TempDir()
	return newSpoolForwarder(), dir, filepath.Join(dir, "dst.jsonl")
}

func TestSpoolForwarder_ForwardsWholeLinesOnly(t *testing.T) {
	f, root, dst := forwardFixture(t)
	src := filepath.Join(root, "src.jsonl")
	// The second line is still being written: no trailing newline.
	if err := os.WriteFile(src, []byte(`{"a":1}`+"\n"+`{"b":2`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := f.forward(root, "src.jsonl", dst, "p1"); err != nil {
		t.Fatalf("forward: %v", err)
	}
	body, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(body) != `{"a":1}`+"\n" {
		t.Errorf("forwarded %q — a partial line would hand the parser truncated JSON", body)
	}
}

// The offset must advance, or every pass re-forwards everything and the
// sidebar fills with duplicates.
func TestSpoolForwarder_DoesNotResend(t *testing.T) {
	f, root, dst := forwardFixture(t)
	src := filepath.Join(root, "src.jsonl")
	if err := os.WriteFile(src, []byte("a\nb\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := f.forward(root, "src.jsonl", dst, "p1"); err != nil {
		t.Fatalf("first: %v", err)
	}
	n, err := f.forward(root, "src.jsonl", dst, "p1")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if n != 0 {
		t.Errorf("second pass forwarded %d bytes; the offset did not advance", n)
	}
}

// The file is agent-writable, so it can be truncated or replaced underneath
// the forwarder. Seeking past the end would silently forward nothing for the
// life of the pane.
func TestSpoolForwarder_RecoversFromTruncation(t *testing.T) {
	f, root, dst := forwardFixture(t)
	src := filepath.Join(root, "src.jsonl")
	if err := os.WriteFile(src, []byte("aaaa\nbbbb\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := f.forward(root, "src.jsonl", dst, "p1"); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := os.WriteFile(src, []byte("c\n"), 0o600); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	n, err := f.forward(root, "src.jsonl", dst, "p1")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if n == 0 {
		t.Error("forwarded nothing after a truncation; the pane is wedged")
	}
}

// Spool caps a line but not a file. Without a cap here, an agent writing a
// gigabyte would have it copied onto the host's disk at copy speed.
func TestSpoolForwarder_CapsOnePass(t *testing.T) {
	f, root, dst := forwardFixture(t)
	src := filepath.Join(root, "src.jsonl")
	line := strings.Repeat("x", 1023) + "\n"
	var b strings.Builder
	for b.Len() < sandboxForwardCap*2 {
		b.WriteString(line)
	}
	if err := os.WriteFile(src, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	n, err := f.forward(root, "src.jsonl", dst, "p1")
	if err != nil {
		t.Fatalf("forward: %v", err)
	}
	if n > sandboxForwardCap {
		t.Errorf("forwarded %d bytes in one pass, cap is %d", n, sandboxForwardCap)
	}
}

// A pane id can be reused after a teardown. Carrying the old offset over would
// have the new pane silently forward nothing until it caught up.
func TestSpoolForwarder_ForgetResetsTheOffset(t *testing.T) {
	f, root, dst := forwardFixture(t)
	src := filepath.Join(root, "src.jsonl")
	if err := os.WriteFile(src, []byte("a\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := f.forward(root, "src.jsonl", dst, "p1"); err != nil {
		t.Fatalf("forward: %v", err)
	}
	f.forget("p1")
	n, err := f.forward(root, "src.jsonl", dst, "p1")
	if err != nil {
		t.Fatalf("after forget: %v", err)
	}
	if n == 0 {
		t.Error("forget did not reset the offset")
	}
}

func TestSpoolForwarder_MissingSourceIsNotAnError(t *testing.T) {
	f, root, dst := forwardFixture(t)
	if n, err := f.forward(root, "src.jsonl", dst, "p1"); err != nil || n != 0 {
		t.Errorf("forward on a pane with no events = %d, %v; want 0, nil", n, err)
	}
}

// The container can create symlinks under its own writable /quil/claude, and
// the accepted path is handed to a host os.Stat — so a link pointing outside
// the tree is an existence oracle for any host path.
func TestHostTranscriptPath_RefusesASymlinkOutOfTheTree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a symlink needs privilege on Windows; the hazard is the Linux/remote host")
	}
	home := t.TempDir()
	t.Setenv("QUIL_HOME", home)
	p := sandboxPane(t, "img")

	cfg := sandboxClaudeConfigDir(home, p.ID)
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(cfg, "projects")); err != nil {
		t.Skipf("symlink unsupported here: %v", err)
	}

	if got := hostTranscriptPath(p, "/quil/claude/projects/-w/abc.jsonl"); got != "" {
		t.Errorf("a symlink escaped the pane's config dir: %q", got)
	}
}
