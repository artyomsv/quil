package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
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

// A missing directory is reported as an error, not as an empty listing.
//
// This test used to be named for the timeout and claimed to pin it. It never
// did: ENOENT returns instantly, so the timer branch was unreachable and
// deleting the entire select-and-timer mechanism left it green. It is kept for
// what it actually checks, and the timeout has its own test below.
func TestReadDirWithin_MissingDirErrors(t *testing.T) {
	_, err := readDirWithin(filepath.Join(t.TempDir(), "absent"), 50*time.Millisecond)
	if err == nil {
		t.Fatal("readDirWithin returned no error for an absent directory")
	}
	if strings.Contains(err.Error(), "timed out") {
		t.Errorf("error = %q, want the underlying ENOENT rather than a timeout", err)
	}
}

// TestReadDirWithin_TimerFiresOnAHungRead pins that the deadline bounds the
// SYSCALL — the branch the old test never reached.
//
// Bounding only the loop over already-returned entries protects nothing: that
// loop is pure CPU. The blocking case is os.ReadDir on a dead network mount, and
// if it is unbounded the single-flight slot is held for the rest of the session,
// with every later listing answered "another directory listing is already
// running" by something that will never finish.
func TestReadDirWithin_TimerFiresOnAHungRead(t *testing.T) {
	waitForFSCallsDrained(t)

	block := make(chan struct{})
	orig := readDirPath
	readDirPath = func(string) ([]os.DirEntry, error) {
		<-block
		return nil, nil
	}
	t.Cleanup(func() { restoreSeam(t, block, func() { readDirPath = orig }) })

	start := time.Now()
	entries, err := readDirWithin("/never-answers", 50*time.Millisecond)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a read that never returns produced no error; the timer branch is dead")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error = %q, want it to name the timeout so the user can diagnose it", err)
	}
	if entries != nil {
		t.Errorf("entries = %v, want nil when the read never answered", entries)
	}
	if elapsed > time.Second {
		t.Errorf("took %v against a 50ms budget; the read is not bounded", elapsed)
	}
}

// Both halves of "nothing to list": an empty request with no fallback is an
// answer, not a crash and not a listing of some arbitrary directory.
func TestBrowseDirResponse_NoPathAndNoFallback(t *testing.T) {
	got := browseDirResponse(ipc.BrowseDirReqPayload{}, "")

	if got.Error == "" {
		t.Error("Error is empty when there is nothing to list and no default")
	}
	if got.Entries != nil {
		t.Errorf("entries = %+v, want none", got.Entries)
	}
	if got.Resolved != "" {
		t.Errorf("Resolved = %q, want empty — nothing was resolved", got.Resolved)
	}
}

// TestBeginBrowseScan_RejectionEchoesBothKeyHalves pins the FULL staleness key
// on the rejection path.
//
// The client matches a response on (Path, Child) together, so a rejection
// carrying only Path is dropped as stale by every descend request — and a
// descend is the common case, since that is what clicking a folder sends. The
// user then waits out the entire client-side timeout for an answer the daemon
// delivered immediately, and sees nothing about why.
func TestBeginBrowseScan_RejectionEchoesBothKeyHalves(t *testing.T) {
	d := &Daemon{}
	req := ipc.BrowseDirReqPayload{Path: "/srv/work", Child: "sub"}

	if _, ok := d.beginBrowseScan(req); !ok {
		t.Fatal("first claim was refused; the slot starts free")
	}
	rejection, ok := d.beginBrowseScan(req)
	if ok {
		t.Fatal("second claim succeeded; the single-flight guard does not hold")
	}
	if rejection.Path != req.Path {
		t.Errorf("rejection Path = %q, want the request echoed (%q)", rejection.Path, req.Path)
	}
	if rejection.Child != req.Child {
		t.Errorf("rejection Child = %q, want %q — the client matches on BOTH halves, so a "+
			"rejection missing this one is dropped as stale", rejection.Child, req.Child)
	}
	if rejection.Error == "" {
		t.Error("rejection carries no reason")
	}

	d.browseScanning.Store(false)
	if _, ok := d.beginBrowseScan(req); !ok {
		t.Error("slot was not released")
	}
}

// TestStatIsDirWithin_ExhaustedBudgetDoesNotStat pins that link resolution
// respects a spent deadline.
//
// os.Stat follows the link, so it parks on a dead mount exactly as os.ReadDir
// does — and it runs AFTER the read, while the single-flight slot is still held.
// An unbounded stat here would move the wedge one syscall later rather than
// preventing it: every subsequent listing in the session answered "another
// directory listing is already running" by something that never finishes.
func TestStatIsDirWithin_ExhaustedBudgetDoesNotStat(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "real")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported here: %v", err)
	}

	// A healthy link that WOULD resolve, so a pass cannot come from the link
	// being unresolvable anyway.
	if isDir, answered := statIsDirWithin(link, time.Second); !answered || !isDir {
		t.Fatalf("with budget: isDir=%v answered=%v, want both true", isDir, answered)
	}

	isDir, answered := statIsDirWithin(link, 0)
	if answered {
		t.Error("a spent budget still statted the link; the deadline does not bound resolution")
	}
	if isDir {
		t.Error("isDir reported true without a stat")
	}
}

// TestClassifyEntries_SpentDeadlineLeavesLinksUnresolved pins the user-visible
// half: an exhausted budget degrades links to non-navigable entries, and still
// LISTS them — the name is evidence something is there.
func TestClassifyEntries_SpentDeadlineLeavesLinksUnresolved(t *testing.T) {
	root, entries := twoLinksAndADir(t)

	// Fresh deadline first, so a pass cannot come from an unresolvable fixture.
	live := classifyEntries(root, entries, time.Now().Add(time.Second))
	for _, e := range live {
		if strings.HasPrefix(e.Name, "link-") && !e.IsDir {
			t.Fatalf("%q did not resolve with a live deadline; the fixture is wrong", e.Name)
		}
	}

	spent := classifyEntries(root, entries, time.Now().Add(-time.Second))
	if len(spent) != len(entries) {
		t.Fatalf("entries = %d, want all %d listed even when unresolved", len(spent), len(entries))
	}
	for _, e := range spent {
		if strings.HasPrefix(e.Name, "link-") && e.IsDir {
			t.Errorf("%q resolved despite a spent deadline", e.Name)
		}
		// A plain directory needs no stat, so the budget must not touch it.
		if e.Name == "real" && !e.IsDir {
			t.Error("a plain directory was misreported; only LINKS depend on the budget")
		}
	}
}

// TestClassifyEntries_OneHungLinkCostsOneGoroutine pins the goroutine bound.
//
// A stat that times out has consumed the shared budget, so every later link is
// classified without a syscall. Without that sharing, a directory holding 500
// links into an unreachable mount would park 500 goroutines — and a goroutine
// blocked in a syscall pins an OS thread — from one keypress.
//
// Driven through the statPath seam because a path that never answers is exactly
// what no real filesystem provides on demand, and this rule is unobservable
// without one.
func TestClassifyEntries_OneHungLinkCostsOneGoroutine(t *testing.T) {
	root, entries := twoLinksAndADir(t)

	block := make(chan struct{})
	var calls atomic.Int64
	orig := statPath
	statPath = func(string) (os.FileInfo, error) {
		calls.Add(1)
		<-block
		return nil, os.ErrNotExist
	}
	t.Cleanup(func() { restoreSeam(t, block, func() { statPath = orig }) })

	got := classifyEntries(root, entries, time.Now().Add(50*time.Millisecond))

	if n := calls.Load(); n != 1 {
		t.Errorf("stat calls = %d, want 1 — the first link's timeout must spend the shared "+
			"budget, or a dead mount costs one parked thread per entry", n)
	}
	if len(got) != len(entries) {
		t.Errorf("entries = %d, want all %d listed", len(got), len(entries))
	}
	for _, e := range got {
		if strings.HasPrefix(e.Name, "link-") && e.IsDir {
			t.Errorf("%q reported as a directory though its stat never answered", e.Name)
		}
	}
}

// twoLinksAndADir builds a directory holding two symlinks (sorting first) and
// the real directory they point at.
func twoLinksAndADir(t *testing.T) (string, []os.DirEntry) {
	t.Helper()
	root := t.TempDir()
	target := filepath.Join(root, "real")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, name := range []string{"link-a", "link-b"} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			// Windows needs developer mode or elevation for symlinks.
			t.Skipf("symlink unsupported here: %v", err)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	return root, entries
}

// TestBrowseDirResponse_ReportsAnIncompleteRootList pins the mapping from the
// sweep's answer onto the wire.
//
// This needs the listFilesystemRoots seam to exist at all. The Unix build of
// filesystemRoots always reports complete — one root, nothing to enumerate — so
// on the platform CI runs this assignment is statically dead, and inverting or
// deleting it passes every other test in the repo. The only arm that can report
// an incomplete list is the Windows one, which the Linux image never compiles:
// the same untestable-behind-a-build-tag shape that let the permit exhaustion
// ship in the first place.
func TestBrowseDirResponse_ReportsAnIncompleteRootList(t *testing.T) {
	dir := t.TempDir()

	tests := []struct {
		name     string
		complete bool
		want     bool
	}{
		{"sweep gave up on unresponsive drives", false, true},
		{"every drive answered", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orig := listFilesystemRoots
			listFilesystemRoots = func(time.Time) ([]string, bool) {
				return []string{`C:\`}, tt.complete
			}
			t.Cleanup(func() { listFilesystemRoots = orig })

			// A path that IS its own parent, so the response takes the roots
			// branch rather than reporting a parent directory.
			resp := browseDirResponse(ipc.BrowseDirReqPayload{Path: rootOf(dir)}, "")

			if resp.RootsTruncated != tt.want {
				t.Errorf("RootsTruncated = %v, want %v — the daemon's own warning never reaches the client",
					resp.RootsTruncated, tt.want)
			}
			// The entry listing is a separate statement and must not be dragged
			// along: the directory read here succeeds completely.
			if resp.Truncated {
				t.Error("Truncated = true — an incomplete drive sweep was reported as a capped file listing")
			}
		})
	}
}

// rootOf walks up until the path is its own parent, which is the condition
// browseDirResponse uses to decide it is at a filesystem root.
func rootOf(path string) string {
	for {
		parent := filepath.Dir(path)
		if parent == path {
			return path
		}
		path = parent
	}
}

// TestReadDirWithin_SpentBudgetStartsNoGoroutine mirrors the guard
// statIsDirWithin has always had, on the function that lacked it.
//
// A goroutine spawned only to be abandoned in the same breath is pure cost: it
// takes a process-wide permit and pins an OS thread for a result no one is left
// to read. This is reachable rather than theoretical because the roots sweep
// runs FIRST and shares the response's deadline, so it can hand this call a
// budget that is already gone.
func TestReadDirWithin_SpentBudgetStartsNoGoroutine(t *testing.T) {
	waitForFSCallsDrained(t)

	if _, err := readDirWithin(t.TempDir(), 0); err == nil {
		t.Fatal("readDirWithin accepted a spent budget")
	}

	if n := blockingFSCalls.Load(); n != 0 {
		t.Errorf("outstanding blocking calls = %d, want 0 — a permit was taken for a read that could not finish", n)
	}
}

// TestReadDirWithin_RefusesPastTheCap pins the accumulation bound.
//
// Every listing that times out leaves its goroutine parked in the syscall, and a
// goroutine blocked in a syscall pins an OS thread — the runtime aborts the
// process past 10 000. A daemon runs for weeks, so a client re-driving
// navigation into a mount that never answers has an unbounded path to killing
// the daemon and every pane with it. Refusing loudly is the better outcome.
func TestReadDirWithin_RefusesPastTheCap(t *testing.T) {
	waitForFSCallsDrained(t)
	for i := 0; i < maxBlockingFSCalls; i++ {
		if !claimBlockingFSCall() {
			t.Fatalf("claim %d refused below the cap of %d", i, maxBlockingFSCalls)
		}
	}
	t.Cleanup(func() {
		for i := 0; i < maxBlockingFSCalls; i++ {
			releaseBlockingFSCall()
		}
	})

	if claimBlockingFSCall() {
		releaseBlockingFSCall()
		t.Fatal("the cap admitted one more than it allows")
	}

	// A directory that reads instantly, so a returned error can only come from
	// the cap and not from the listing itself.
	_, err := readDirWithin(t.TempDir(), time.Second)
	if err == nil {
		t.Fatal("readDirWithin ran past the cap; abandoned readers are unbounded")
	}
	if !strings.Contains(err.Error(), "stuck") {
		t.Errorf("error = %q, want it to name the situation", err)
	}
}

// The cap must be self-healing, or one burst disables browsing permanently.
func TestBlockingFSCalls_SlotIsReturned(t *testing.T) {
	waitForFSCallsDrained(t)

	if _, err := readDirWithin(t.TempDir(), time.Second); err != nil {
		t.Fatalf("readDirWithin: %v", err)
	}

	waitForFSCallsDrained(t)
}

// restoreSeam unparks a test's fake filesystem call, waits for its goroutine to
// actually exit, and only then puts the real function back.
//
// The order is the whole point, and getting it wrong is a genuine data race that
// -race reports: statPath and readDirPath are plain package vars, so restoring
// one while a goroutine is still parked INSIDE it is an unsynchronized write
// against that goroutine's read. Logically the read already happened, but
// nothing establishes a happens-before edge, which is exactly the situation the
// detector exists to catch. Waiting on the counter supplies that edge, because
// the parked goroutine's own deferred releaseBlockingFSCall is an atomic write
// this loop reads.
//
// This ran clean under -race before the third seam test was added, which is
// worth remembering: race detection observes actual interleavings and proves
// nothing about the ones it did not see.
func restoreSeam(t *testing.T, unpark chan struct{}, restore func()) {
	t.Helper()
	close(unpark)
	waitForFSCallsDrained(t)
	restore()
}

// waitForFSCallsDrained blocks until no blocking filesystem call is outstanding.
//
// The counter is process-wide and releaseBlockingFSCall runs in the WORKER
// goroutine — after it has handed its result to the caller, not before — so a
// test that has already returned can still be holding a slot for a moment
// longer. Every test in this file therefore has to wait for the baseline rather
// than assume it, or it races whichever test ran before it. That ordering is
// deliberate in the production code (the slot is held for as long as the syscall
// really runs, not for as long as anyone waits), so the test is what has to
// adapt.
func waitForFSCallsDrained(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if n := blockingFSCalls.Load(); n == 0 {
			return
		} else if time.Now().After(deadline) {
			t.Fatalf("%d blocking filesystem calls still outstanding after 2s", n)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestBrowseDirResponse_HungLinkStillReturnsWithinTheBudget pins the property
// the whole shared-deadline design exists for, at the level that actually
// matters: browseDirResponse RETURNS when a link never answers, which is what
// releases the single-flight slot.
//
// The two halves are pinned separately elsewhere, but neither proves the sum.
// This is the end-to-end statement — a listing containing an unresponsive link
// is bounded, and it still answers with a complete listing rather than failing.
func TestBrowseDirResponse_HungLinkStillReturnsWithinTheBudget(t *testing.T) {
	waitForFSCallsDrained(t)
	root, entries := twoLinksAndADir(t)

	const budget = 200 * time.Millisecond
	block := make(chan struct{})
	var calls atomic.Int64
	origStat, origTimeout := statPath, browseTimeout
	statPath = func(string) (os.FileInfo, error) {
		calls.Add(1)
		<-block
		return nil, os.ErrNotExist
	}
	browseTimeout = budget
	t.Cleanup(func() {
		restoreSeam(t, block, func() { statPath, browseTimeout = origStat, origTimeout })
	})

	start := time.Now()
	got := browseDirResponse(ipc.BrowseDirReqPayload{Path: root}, "")
	elapsed := time.Since(start)

	// A hung LINK is not a failed listing: the directory read succeeded, so the
	// user gets their entries with the unresolvable one marked non-navigable.
	if got.Error != "" {
		t.Fatalf("Error = %q; a hung link must not fail the whole listing", got.Error)
	}
	if len(got.Entries) != len(entries) {
		t.Errorf("entries = %d, want all %d listed", len(got.Entries), len(entries))
	}
	// The deterministic half. Timing alone is too weak to pin this: with only
	// two links, a per-call budget instead of a shared one costs 2×budget, which
	// any threshold loose enough to survive a slow CI box would also admit.
	if n := calls.Load(); n != 1 {
		t.Errorf("stat calls = %d, want 1 — the budget is per-call rather than "+
			"shared, so cost scales with the number of links", n)
	}
	// Headroom over the budget rather than a tight bound: what is pinned is that
	// the call is bounded AT ALL, and a loaded CI box is slow, not broken.
	if elapsed > 10*budget {
		t.Errorf("took %v against a %v budget — the listing is unbounded and the "+
			"single-flight slot stays held", elapsed, budget)
	}
}

// TestDirsExistResponse_KeepsDirectoriesInRequestOrder pins the whole point of
// the RPC: the surviving entries, in the order the client asked, with files and
// deleted paths dropped.
func TestDirsExistResponse_KeepsDirectoriesInRequestOrder(t *testing.T) {
	waitForFSCallsDrained(t)

	tmp := t.TempDir()
	alpha := filepath.Join(tmp, "alpha")
	bravo := filepath.Join(tmp, "bravo")
	for _, d := range []string{alpha, bravo} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	file := filepath.Join(tmp, "plain.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	gone := filepath.Join(tmp, "deleted")

	out := dirsExistResponse(ipc.DirsExistReqPayload{Paths: []string{bravo, gone, file, alpha}})

	want := []string{bravo, alpha}
	if !reflect.DeepEqual(out.Paths, want) {
		t.Errorf("Paths = %v, want %v (request order, non-directories dropped)", out.Paths, want)
	}
	if out.Error != "" {
		t.Errorf("Error = %q, want none — nothing failed", out.Error)
	}
}

// TestDirsExistResponse_EmptyResultIsNotAnError keeps "none of these exist any
// more" distinguishable from a failure. Only one of the two justifies telling
// the user their remembered directories are gone.
func TestDirsExistResponse_EmptyResultIsNotAnError(t *testing.T) {
	waitForFSCallsDrained(t)

	out := dirsExistResponse(ipc.DirsExistReqPayload{
		Paths: []string{filepath.Join(t.TempDir(), "never-existed")},
	})
	if out.Error != "" {
		t.Errorf("Error = %q, want none — an absent directory is a finding, not a failure", out.Error)
	}
	if len(out.Paths) != 0 {
		t.Errorf("Paths = %v, want empty", out.Paths)
	}
}

// TestDirsExistResponse_RefusesPastTheCap bounds how many blocking stats one
// request can start. recentCWDMax is 5, so this is a hostile-client backstop.
func TestDirsExistResponse_RefusesPastTheCap(t *testing.T) {
	paths := make([]string, maxDirsExistPaths+1)
	for i := range paths {
		paths[i] = fmt.Sprintf("/nope-%d", i)
	}
	out := dirsExistResponse(ipc.DirsExistReqPayload{Paths: paths})
	if out.Error == "" {
		t.Error("Error = empty — an oversized request was accepted, so one client can start " +
			"an unbounded number of blocking stats")
	}
	if len(out.Paths) != 0 {
		t.Errorf("Paths = %v, want empty on refusal", out.Paths)
	}
}

// TestDirsExistResponse_OneHungPathCostsOneStat is the refutation test for the
// SHARED deadline. Mirrors TestClassifyEntries_OneHungLinkCostsOneGoroutine: a
// budget per path would park one thread per entry on a dead mount, and a daemon
// runs for weeks.
func TestDirsExistResponse_OneHungPathCostsOneStat(t *testing.T) {
	waitForFSCallsDrained(t)

	block := make(chan struct{})
	var calls atomic.Int64
	origStat, origTimeout := statPath, browseTimeout
	statPath = func(string) (os.FileInfo, error) {
		calls.Add(1)
		<-block
		return nil, os.ErrNotExist
	}
	browseTimeout = 50 * time.Millisecond
	t.Cleanup(func() {
		restoreSeam(t, block, func() { statPath, browseTimeout = origStat, origTimeout })
	})

	out := dirsExistResponse(ipc.DirsExistReqPayload{Paths: []string{"/a", "/b", "/c", "/d"}})

	if n := calls.Load(); n != 1 {
		t.Errorf("stat calls = %d, want 1 — the first path's timeout must spend the shared "+
			"budget, or a dead mount costs one parked thread per entry", n)
	}
	if len(out.Paths) != 0 {
		t.Errorf("Paths = %v, want empty — a path whose stat never answered is not a directory", out.Paths)
	}
}

// TestDirsCheckAndBrowse_HaveIndependentSlots pins the single-flight invariant
// this handler most needs pinned: dirsChecking is NOT browseScanning.
//
// The two look like the same question and are not, because of timing. The
// existence check hands over to the directory browser when the client gives up
// at 8 s, while the daemon holds its slot for as long as the syscall really runs
// — up to browseTimeout (10 s). Sharing one guard would make that handover's
// browse requests rejected by the very request that just timed out, dead-ending
// the dialog in exactly the dead-mount case the bound exists for.
//
// Claims through beginDirsCheck rather than the atomic directly: pointing the
// handler's CompareAndSwap at browseScanning must FAIL this test, and it only
// does if the test walks the same path the handler walks.
func TestDirsCheckAndBrowse_HaveIndependentSlots(t *testing.T) {
	d := &Daemon{}

	if _, ok := d.beginDirsCheck(); !ok {
		t.Fatal("existence check refused while its slot was free")
	}
	if _, ok := d.beginBrowseScan(ipc.BrowseDirReqPayload{Path: "/a"}); !ok {
		t.Error("a held existence-check slot blocked a directory listing; the guards are shared")
	}

	gitMsg, err := ipc.NewMessage(ipc.MsgGitReposReq, ipc.GitReposReqPayload{CWD: "/a"})
	if err != nil {
		t.Fatalf("build message: %v", err)
	}
	if _, ok := d.beginGitDiscover(gitMsg); !ok {
		t.Error("a held existence-check slot blocked git discovery; the guards are shared")
	}
}

// TestBeginDirsCheck_RejectsWhileHeld pins the rejection path, which is
// otherwise unreachable from a test because ipc.Conn cannot be constructed
// outside its package.
func TestBeginDirsCheck_RejectsWhileHeld(t *testing.T) {
	d := &Daemon{}
	if _, ok := d.beginDirsCheck(); !ok {
		t.Fatal("first claim refused")
	}
	rejection, ok := d.beginDirsCheck()
	if ok {
		t.Fatal("second claim succeeded while the slot was held")
	}
	if rejection.Error == "" {
		t.Error("rejection carries no Error — the client cannot tell it from an empty answer, " +
			"and an empty answer means 'none of your directories exist'")
	}
	if len(rejection.Paths) != 0 {
		t.Errorf("rejection carries Paths = %v, want none", rejection.Paths)
	}
}

// TestProjectCWD_DoesNotWedgeOnAnUnreachableRoot: a project's RootDir becomes a
// pane's spawn CWD, and resolving it used to be an unbounded os.Stat plus
// EvalSymlinks run straight on the IPC dispatch goroutine. A root on a dead NFS
// or SMB mount parks that goroutine in an uninterruptible syscall — and with it
// every pane on the daemon, since that is the goroutine serving them all.
//
// Worse than the client-CWD case it was copied from: a RootDir is typed once in
// a dialog, persisted, and re-read on every new tab, so the wedge survives
// restarts and re-arms rather than being refreshed on each attach.
//
// Driven through the statPath seam for the reason classifyEntries' test gives —
// no real filesystem provides a path that never answers on demand.
func TestProjectCWD_DoesNotWedgeOnAnUnreachableRoot(t *testing.T) {
	d := newTestDaemon(t)
	p := d.session.CreateProject("stuck", "/mnt/dead-share/work")

	block := make(chan struct{})
	var calls atomic.Int64
	orig := statPath
	statPath = func(string) (os.FileInfo, error) {
		calls.Add(1)
		<-block
		return nil, os.ErrNotExist
	}
	t.Cleanup(func() { restoreSeam(t, block, func() { statPath = orig }) })

	done := make(chan string, 1)
	go func() { done <- d.projectCWD(p.ID) }()

	select {
	case got := <-done:
		// The fallback, not the unreachable root: a spawn there would inherit
		// the same hang in the child.
		if got == "/mnt/dead-share/work" {
			t.Errorf("projectCWD returned the unreachable root %q; it must fall "+
				"back when the probe cannot answer", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("projectCWD never returned — the dispatch goroutine is parked in " +
			"the stat, and every pane on this daemon is parked behind it")
	}

	// This assertion is the load-bearing one, and without it the test passes
	// against the very code it exists to reject. An unbounded os.Stat does not
	// go through this seam at all, so it answers ENOENT from the real
	// filesystem in microseconds and the timing check above never fires. What
	// distinguishes the two implementations is not how long the call takes on a
	// path that happens not to exist — it is whether the probe is bounded, and
	// going through the seam is what bounded means here.
	if calls.Load() == 0 {
		t.Error("projectCWD resolved its root without going through statPath, " +
			"so the probe is unbounded: on a dead mount it parks the IPC " +
			"dispatch goroutine and every pane behind it")
	}
}
