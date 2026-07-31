package daemon

import (
	"os"
	"path/filepath"
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

// TestSymlinkIsDirWithin_ExhaustedBudgetDoesNotStat pins that link resolution
// respects a spent deadline.
//
// os.Stat follows the link, so it parks on a dead mount exactly as os.ReadDir
// does — and it runs AFTER the read, while the single-flight slot is still held.
// An unbounded stat here would move the wedge one syscall later rather than
// preventing it: every subsequent listing in the session answered "another
// directory listing is already running" by something that never finishes.
func TestSymlinkIsDirWithin_ExhaustedBudgetDoesNotStat(t *testing.T) {
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
	if isDir, answered := symlinkIsDirWithin(link, time.Second); !answered || !isDir {
		t.Fatalf("with budget: isDir=%v answered=%v, want both true", isDir, answered)
	}

	isDir, answered := symlinkIsDirWithin(link, 0)
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
	t.Cleanup(func() {
		statPath = orig
		// Released last so the parked goroutines return their FS slots; leaving
		// them held would starve every later test in this package.
		close(block)
	})

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

// TestReadDirWithin_RefusesPastTheCap pins the accumulation bound.
//
// Every listing that times out leaves its goroutine parked in the syscall, and a
// goroutine blocked in a syscall pins an OS thread — the runtime aborts the
// process past 10 000. A daemon runs for weeks, so a client re-driving
// navigation into a mount that never answers has an unbounded path to killing
// the daemon and every pane with it. Refusing loudly is the better outcome.
func TestReadDirWithin_RefusesPastTheCap(t *testing.T) {
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
	before := blockingFSCalls.Load()

	if _, err := readDirWithin(t.TempDir(), time.Second); err != nil {
		t.Fatalf("readDirWithin: %v", err)
	}

	// The release happens in the worker goroutine, so it is not ordered against
	// this line by anything but the handoff the successful read already made.
	deadline := time.Now().Add(2 * time.Second)
	for blockingFSCalls.Load() != before && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := blockingFSCalls.Load(); got != before {
		t.Errorf("outstanding calls = %d, want %d — a completed read did not return its slot", got, before)
	}
}
