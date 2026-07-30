package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

func TestBrowseDirResponse_ListsDirsAndFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := browseDirResponse(ipc.BrowseDirReqPayload{Path: root}, "")

	if got.Error != "" {
		t.Fatalf("Error = %q, want empty", got.Error)
	}
	if got.Path != root {
		t.Errorf("Path = %q, want the request echoed verbatim (%q)", got.Path, root)
	}
	if len(got.Entries) != 2 {
		t.Fatalf("entries = %v, want 2", got.Entries)
	}
	// Directories first — the browser's navigation rows must not sink below a
	// long tail of files.
	if !got.Entries[0].IsDir || got.Entries[0].Name != "sub" {
		t.Errorf("entries[0] = %+v, want the directory first", got.Entries[0])
	}
	if got.Entries[1].IsDir || got.Entries[1].Name != "file.txt" {
		t.Errorf("entries[1] = %+v, want the file second", got.Entries[1])
	}
	if got.Parent == "" {
		t.Error("Parent is empty for a non-root directory; the browser has no way up")
	}
}

// The echo is the staleness key. A trailing separator must survive it.
func TestBrowseDirResponse_EchoesPathVerbatim(t *testing.T) {
	root := t.TempDir()
	req := ipc.BrowseDirReqPayload{Path: root + string(filepath.Separator)}

	got := browseDirResponse(req, "")

	if got.Path != req.Path {
		t.Errorf("Path = %q, want %q — daemon-side normalisation makes a live request look stale",
			got.Path, req.Path)
	}
	if got.Resolved == "" {
		t.Error("Resolved is empty; the cleaned path belongs there, not in Path")
	}
	if got.Resolved == got.Path {
		t.Error("Resolved kept the trailing separator; it is meant to be the CLEANED answer")
	}
}

// A response must be incapable of overflowing the frame.
func TestBrowseDirResponse_CapsEntries(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < MaxBrowseEntries+50; i++ {
		if err := os.Mkdir(filepath.Join(root, "d"+strconv.Itoa(i)), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	got := browseDirResponse(ipc.BrowseDirReqPayload{Path: root}, "")

	if len(got.Entries) > MaxBrowseEntries {
		t.Errorf("entries = %d, want <= %d", len(got.Entries), MaxBrowseEntries)
	}
	if !got.Truncated {
		t.Error("Truncated not set on a capped listing")
	}
}

// TestBrowseDirResponse_CapKeepsDirectories pins the sort-before-cap ordering.
//
// Capping the raw os.ReadDir order instead takes the alphabetical head, so a
// directory whose subdirectories happen to sort after its files returns a
// listing with nothing to navigate into — the browser dead-ends on a directory
// that visibly has folders in it. Sorting first means the cap can only ever drop
// trailing files.
func TestBrowseDirResponse_CapKeepsDirectories(t *testing.T) {
	root := t.TempDir()
	// Files sort BEFORE the directory: "a…" beats "zzz-dir".
	for i := 0; i < MaxBrowseEntries+10; i++ {
		name := filepath.Join(root, "a"+strconv.Itoa(i)+".txt")
		if err := os.WriteFile(name, []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, "zzz-dir"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	got := browseDirResponse(ipc.BrowseDirReqPayload{Path: root}, "")

	if !got.Truncated {
		t.Fatal("Truncated not set; the fixture is meant to exceed the cap")
	}
	// Reported by count and head rather than by dumping the slice: a failure
	// here returns 500 entries, and printing them buries the one fact that
	// matters under a screen of filenames.
	if len(got.Entries) == 0 {
		t.Fatal("no entries returned")
	}
	if !got.Entries[0].IsDir {
		t.Fatalf("the only directory was capped away: %d entries, first is %q (a file)",
			len(got.Entries), got.Entries[0].Name)
	}
}

// TestBrowseDirResponse_SymlinkedDirIsADirectory pins link resolution.
//
// os.ReadDir builds its entries from the directory record alone, so a symlink to
// a directory — and a Windows junction, which reports identically — arrives as
// ModeSymlink with IsDir() false. Trusting that flag makes the picker refuse to
// descend into exactly the symlinked project directories people navigate by, and
// renders them as if they were files.
func TestBrowseDirResponse_SymlinkedDirIsADirectory(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "real")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		// Windows needs developer mode or elevation for symlinks; the
		// behaviour is still worth pinning wherever it can be created.
		t.Skipf("symlink unsupported here: %v", err)
	}

	got := browseDirResponse(ipc.BrowseDirReqPayload{Path: root}, "")

	for _, e := range got.Entries {
		if e.Name == "link" {
			if !e.IsDir {
				t.Error("a symlink to a directory was reported as a file; the picker cannot descend into it")
			}
			return
		}
	}
	t.Fatalf("the symlink is missing from the listing entirely: %+v", got.Entries)
}

// A dangling link is not a directory, but it must still be listed — the name is
// evidence that something is there.
func TestBrowseDirResponse_BrokenSymlinkIsListedNotADir(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(root, "dangling")
	if err := os.Symlink(filepath.Join(root, "absent"), link); err != nil {
		t.Skipf("symlink unsupported here: %v", err)
	}

	got := browseDirResponse(ipc.BrowseDirReqPayload{Path: root}, "")

	if len(got.Entries) != 1 {
		t.Fatalf("entries = %+v, want the dangling link listed", got.Entries)
	}
	if got.Entries[0].IsDir {
		t.Error("a dangling symlink was reported as a directory")
	}
}

// TestBrowseDirResponse_ChildDescends pins the server-side join.
//
// The client cannot compute this path: separators belong to the machine holding
// the filesystem, so a Windows TUI attached to a Linux daemon would build a
// `C:\srv\work` shaped string with filepath.Join and list nothing at all.
func TestBrowseDirResponse_ChildDescends(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "sub", "leaf"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	got := browseDirResponse(ipc.BrowseDirReqPayload{Path: root, Child: "sub"}, "")

	if got.Error != "" {
		t.Fatalf("Error = %q, want empty", got.Error)
	}
	if got.Resolved != filepath.Join(root, "sub") {
		t.Errorf("Resolved = %q, want the child directory", got.Resolved)
	}
	// Both halves of the request echo, or two descents from one directory are
	// indistinguishable as staleness keys.
	if got.Path != root || got.Child != "sub" {
		t.Errorf("echo = (%q, %q), want (%q, %q)", got.Path, got.Child, root, "sub")
	}
	if len(got.Entries) != 1 || got.Entries[0].Name != "leaf" {
		t.Errorf("entries = %+v, want the child's contents", got.Entries)
	}
}

func TestValidBrowseChild(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"work", true},
		{"a b", true},
		{"", false},
		{".", false},
		{"..", false},
		// Rejected on EVERY platform, not just where the separator is native:
		// Windows accepts both, so a filepath.Separator-only check would miss
		// exactly the platform that has two.
		{"a/b", false},
		{`a\b`, false},
		{"../etc", false},
		{"/etc", false},
	}
	for _, tt := range tests {
		if got := validBrowseChild(tt.name); got != tt.want {
			t.Errorf("validBrowseChild(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// A traversal attempt is refused rather than sanitised — the client has no
// reason to send one, and a silent rewrite lists a directory nobody asked for.
func TestBrowseDirResponse_ChildWithSeparatorIsRefused(t *testing.T) {
	root := t.TempDir()

	got := browseDirResponse(ipc.BrowseDirReqPayload{Path: root, Child: ".."}, "")

	if got.Error == "" {
		t.Error("a '..' child was accepted")
	}
	if got.Entries != nil {
		t.Errorf("entries returned for a refused request: %+v", got.Entries)
	}
}

// TestBrowseDirResponse_NonRootReportsParentNotRoots pins that the two are
// mutually exclusive: an ordinary directory has somewhere to go up TO, so the
// client should navigate by Parent and never see a root list.
func TestBrowseDirResponse_NonRootReportsParentNotRoots(t *testing.T) {
	got := browseDirResponse(ipc.BrowseDirReqPayload{Path: t.TempDir()}, "")

	if got.Parent == "" {
		t.Error("Parent is empty for a non-root directory; the browser has no way up")
	}
	if len(got.Roots) != 0 {
		t.Errorf("Roots = %v on a non-root directory; it is only meaningful AT a root", got.Roots)
	}
}

// TestBrowseDirResponse_RootReportsRootsNotParent is the other half. A root is
// its own parent, so reporting Parent would render an "up" row that navigates
// to where the user already is; what sits above it is the root list, which only
// the daemon can enumerate.
func TestBrowseDirResponse_RootReportsRootsNotParent(t *testing.T) {
	// The platform's own root, so this holds on both Unix ("/") and Windows
	// (the volume of the temp dir).
	root := filepath.VolumeName(t.TempDir()) + string(filepath.Separator)

	got := browseDirResponse(ipc.BrowseDirReqPayload{Path: root}, "")

	if got.Error != "" {
		t.Fatalf("Error = %q listing the filesystem root", got.Error)
	}
	if got.Parent != "" {
		t.Errorf("Parent = %q at a root; a root is its own parent", got.Parent)
	}
	if runtime.GOOS == "windows" {
		if len(got.Roots) == 0 {
			t.Error("no drive letters reported at a Windows root; 'up' from C:\\ has nothing to show")
		}
	} else if len(got.Roots) != 0 {
		t.Errorf("Roots = %v on Unix; / has nothing above it", got.Roots)
	}
}

// TestExpandHome pins that "~" is the DAEMON's home.
//
// The setup dialog expanded it with the TUI's own os.UserHomeDir, so against a
// remote host "~/project" asked for C:\Users\them\project on a Linux server.
func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory available: %v", err)
	}

	if got := expandHome("~"); got != home {
		t.Errorf("expandHome(\"~\") = %q, want %q", got, home)
	}
	if got := expandHome("~/project"); got != filepath.Join(home, "project") {
		t.Errorf("expandHome(\"~/project\") = %q, want %q", got, filepath.Join(home, "project"))
	}
	// Not a prefix we expand: resolving another account's home needs a user
	// database lookup. Passing it through fails visibly rather than landing
	// somewhere unintended.
	if got := expandHome("~someone/x"); got != "~someone/x" {
		t.Errorf("expandHome(\"~someone/x\") = %q, want it untouched", got)
	}
	// An ordinary path is never rewritten.
	if got := expandHome("/srv/work"); got != "/srv/work" {
		t.Errorf("expandHome(\"/srv/work\") = %q, want it untouched", got)
	}
}

// The handler must actually apply the expansion, not merely offer it.
func TestBrowseDirResponse_ExpandsTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory available: %v", err)
	}

	got := browseDirResponse(ipc.BrowseDirReqPayload{Path: "~"}, "")

	if got.Error != "" {
		t.Fatalf("Error = %q listing ~", got.Error)
	}
	if got.Resolved != filepath.Clean(home) {
		t.Errorf("Resolved = %q, want the daemon's home %q", got.Resolved, filepath.Clean(home))
	}
	// The echo is untouched by expansion: it is the client's staleness key, and
	// the client sent "~".
	if got.Path != "~" {
		t.Errorf("Path = %q, want the request echoed verbatim", got.Path)
	}
}

// A missing directory is an answer, not a transport failure.
func TestBrowseDirResponse_MissingDir_ReportsError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")

	got := browseDirResponse(ipc.BrowseDirReqPayload{Path: missing}, "")

	if got.Error == "" {
		t.Error("Error is empty for a missing directory")
	}
	if got.Path != missing {
		t.Errorf("Path = %q, want %q — the echo is dropped on the error path and the "+
			"client cannot match the response", got.Path, missing)
	}
}

// An empty request means "the daemon's default", which is what makes the first
// open of the dialog work without the client naming a path it cannot know.
func TestBrowseDirResponse_EmptyPathUsesTheFallback(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	got := browseDirResponse(ipc.BrowseDirReqPayload{Path: ""}, root)

	if got.Error != "" {
		t.Fatalf("Error = %q, want empty", got.Error)
	}
	if got.Path != "" {
		t.Errorf("Path = %q, want the empty request echoed unchanged", got.Path)
	}
	if got.Resolved != filepath.Clean(root) {
		t.Errorf("Resolved = %q, want the fallback %q", got.Resolved, filepath.Clean(root))
	}
}

// TestReadDirWithin_TimesOut pins that the deadline bounds the SYSCALL.
//
// Bounding only the loop over already-returned entries protects nothing: that
// loop is pure CPU. The blocking case is os.ReadDir on a dead network mount,
// and if it is unbounded the single-flight slot is held for the rest of the
// session — every later listing answered "another directory listing is already
// running" by something that will never finish.
//
// Driven through a directory that cannot be read rather than a real hung mount:
// what is asserted is that the call RETURNS within the budget, which is the
// property the slot depends on.
func TestReadDirWithin_TimesOut(t *testing.T) {
	start := time.Now()
	_, err := readDirWithin(filepath.Join(t.TempDir(), "absent"), 50*time.Millisecond)
	if err == nil {
		t.Fatal("readDirWithin returned no error for an absent directory")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("readDirWithin took %v; it is not bounded", elapsed)
	}
}

// The rejection must carry the requested path, or the TUI drops it as stale and
// waits out its whole timeout on an answer it already has.
func TestBeginBrowseScan_RejectionEchoesThePath(t *testing.T) {
	d := &Daemon{}
	msg, err := ipc.NewMessage(ipc.MsgBrowseDirReq, ipc.BrowseDirReqPayload{Path: "/srv/work"})
	if err != nil {
		t.Fatalf("build message: %v", err)
	}

	if _, ok := d.beginBrowseScan(msg); !ok {
		t.Fatal("first claim was refused; the slot starts free")
	}
	rejection, ok := d.beginBrowseScan(msg)
	if ok {
		t.Fatal("second claim succeeded; the single-flight guard does not hold")
	}
	if rejection.Path != "/srv/work" {
		t.Errorf("rejection Path = %q, want the request echoed", rejection.Path)
	}
	if rejection.Error == "" {
		t.Error("rejection carries no reason")
	}

	d.browseScanning.Store(false)
	if _, ok := d.beginBrowseScan(msg); !ok {
		t.Error("slot was not released")
	}
}
