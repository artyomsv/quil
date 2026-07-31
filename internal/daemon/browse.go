package daemon

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

// MaxBrowseEntries caps one listing.
//
// A response must be incapable of exceeding maxFrameSize BY CONSTRUCTION rather
// than because directories are usually small: the path is chosen by whoever is
// driving the dialog, and /nix/store or a node_modules root is an ordinary place
// to land. At ~256 bytes per entry this bounds a listing to well under the frame.
const MaxBrowseEntries = 500

// browseTimeout bounds one listing END TO END: the directory read AND the
// symlink resolution that follows it.
//
// Both block on the same thing — an unresponsive mount — and both run while the
// single-flight slot is held, so a budget covering only the read would leave the
// slot pinned by the stats instead. Same wedge, one syscall later.
//
// A var rather than a const purely so a test can shorten it, matching the
// client-side browseTimeout/gitScanTimeout in internal/tui. The property worth
// pinning is that browseDirResponse RETURNS — releasing the slot — even when a
// link never answers, and asserting that against the production value would cost
// a real ten seconds per run. Production reads the value declared here; the
// tests that override it run sequentially within this package.
var browseTimeout = 10 * time.Second

// handleBrowseDirReq answers a directory listing.
//
// Worker goroutine + single-flight, the shape handleClaudeSessionsReq
// established: this is file I/O, and running it on the conn's dispatch goroutine
// would freeze every other message from that client. The rejection still echoes
// the request key — the TUI matches on that echo, so an unmatched error is
// dropped as stale and the field sits on its pending state until the timeout
// fires, showing nothing about why.
func (d *Daemon) handleBrowseDirReq(conn *ipc.Conn, msg *ipc.Message) {
	req := browseReq(msg)
	rejection, ok := d.beginBrowseScan(req)
	if !ok {
		respondTo(conn, msg.ID, ipc.MsgBrowseDirResp, rejection)
		return
	}
	fallback := d.defaultCWD()
	go func() {
		defer d.browseScanning.Store(false)
		respondTo(conn, msg.ID, ipc.MsgBrowseDirResp, browseDirResponse(req, fallback))
	}()
}

// beginBrowseScan claims the single-flight slot, returning the rejection to send
// when it is already taken.
//
// Split from the handler for the reason beginClaudeSessionsScan is: ipc.Conn
// cannot be constructed outside its own package, so the handler wrapper is
// untestable — but the decision it makes is the part worth pinning.
//
// Takes the decoded request rather than the message so the echo cannot drift
// from what browseDirResponse echoes: BOTH halves of the key are copied here,
// because the client matches a response on (Path, Child) together. A rejection
// carrying only Path is dropped as stale by every descend request — the field
// then waits out its full client-side timeout for an answer it already holds,
// which is the exact failure this echo exists to prevent.
func (d *Daemon) beginBrowseScan(req ipc.BrowseDirReqPayload) (ipc.BrowseDirRespPayload, bool) {
	if d.browseScanning.CompareAndSwap(false, true) {
		return ipc.BrowseDirRespPayload{}, true
	}
	return ipc.BrowseDirRespPayload{
		Path:  req.Path,
		Child: req.Child,
		Error: "another directory listing is already running",
	}, false
}

// browseReq decodes a request, degrading to the zero value. A malformed frame
// yields an empty Path, which the response echoes — matching whatever the client
// sent, since it could not have sent anything this decode would reject.
func browseReq(msg *ipc.Message) ipc.BrowseDirReqPayload {
	var req ipc.BrowseDirReqPayload
	if err := msg.DecodePayload(&req); err != nil {
		log.Printf("handleBrowseDirReq: decode: %v", err)
	}
	return req
}

// browseDirResponse is the pure half: resolve → read → sort → cap → echo.
//
// fallback is the directory an empty request means, passed in rather than read
// from the Daemon so this stays a pure function — d.defaultCWD() is a method and
// the decision it makes (client CWD, validated, else the daemon's own) belongs
// to the caller.
//
// CONTRACT: Path echoes req.Path VERBATIM on every path INCLUDING the error
// ones. It is the client's staleness key, not a statement about what was read.
func browseDirResponse(req ipc.BrowseDirReqPayload, fallback string) ipc.BrowseDirRespPayload {
	out := ipc.BrowseDirRespPayload{Path: req.Path, Child: req.Child}

	target := req.Path
	if target == "" {
		target = fallback
	}
	if target == "" {
		out.Error = "no directory to list and no default available"
		return out
	}
	// The join happens HERE because separators belong to the machine holding
	// the filesystem. A client computing it would use its own, and a Windows
	// TUI against a Linux daemon would ask for a path that cannot exist.
	if req.Child != "" {
		if !validBrowseChild(req.Child) {
			out.Error = fmt.Sprintf("invalid directory name %q", req.Child)
			return out
		}
		target = filepath.Join(target, req.Child)
	}
	target = expandHome(target)
	abs, err := filepath.Abs(target)
	if err != nil {
		out.Error = fmt.Sprintf("resolve %s: %v", target, err)
		return out
	}
	out.Resolved = filepath.Clean(abs)

	// ONE deadline for every blocking call this response makes: the root probe,
	// the read, and the link resolution after it. All three hold the
	// single-flight slot, so what has to be bounded is their SUM — a budget per
	// call would let a listing hold the slot for a multiple of what the constant
	// claims. Declared here rather than beside the read because the root probe
	// runs first and stats up to 26 drive letters.
	deadline := time.Now().Add(browseTimeout)

	// A root directory is its own parent; reporting that would render an "up"
	// row that navigates to where the user already is. At a root, Roots carries
	// whatever sits above it instead — the drive list on Windows, nothing on
	// Unix. The client cannot decide either of these: both depend on the
	// platform holding the filesystem, not the one drawing the picker.
	if parent := filepath.Dir(out.Resolved); parent != out.Resolved {
		out.Parent = parent
	} else {
		out.Roots = filesystemRoots(deadline)
	}

	entries, err := readDirWithin(out.Resolved, time.Until(deadline))
	if err != nil {
		out.Error = err.Error()
		return out
	}

	// Sorted BEFORE capping, which the cap makes load-bearing rather than
	// cosmetic. os.ReadDir returns entries in name order, so capping first would
	// take the alphabetical head — and in a directory of 600 where the
	// subdirectories happen to sort late, that is 500 files and not one folder
	// to navigate into. Sorting directories to the front first means the cap can
	// only ever drop trailing FILES, so a browser always keeps its exits.
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir() != entries[j].IsDir() {
			return entries[i].IsDir()
		}
		return entries[i].Name() < entries[j].Name()
	})
	if len(entries) > MaxBrowseEntries {
		entries = entries[:MaxBrowseEntries]
		out.Truncated = true
	}
	// Classified AFTER the cap, so the number of links that can be stated is
	// bounded by MaxBrowseEntries rather than by the directory's real size.
	out.Entries = classifyEntries(out.Resolved, entries, deadline)
	return out
}

// expandHome resolves a leading ~ against the DAEMON's home directory.
//
// Done here rather than in the client, and for the same reason the path join
// is: "~" names a directory on the machine holding the filesystem. The setup
// dialog expanded it with the TUI's own os.UserHomeDir, so a user typing
// ~/project against a remote host asked for C:\Users\them\project on a Linux
// server. filepath.Abs has the identical problem for a relative path — it
// resolves against the caller's working directory — which is why that also runs
// here, immediately after this.
//
// A bare "~" and a "~/..." prefix are expanded; "~user" is deliberately not,
// since resolving another account's home needs a user database lookup and no
// caller has asked for it. It falls through as a literal, which fails visibly
// rather than silently landing somewhere unintended.
func expandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, `~\`) {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}

// validBrowseChild reports whether name is a single path element.
//
// Child is documented as a leaf name and is joined onto a caller-supplied
// directory, so anything carrying a separator — or ".." — is not the thing the
// field is for. Rejecting rather than sanitising: the client has no reason to
// send one, every legitimate value comes straight from a listing this daemon
// produced, and a silent rewrite would list a directory nobody asked for.
//
// Both separators are checked on every platform. Windows accepts '/' as well as
// '\', so a check using only filepath.Separator would miss half the cases on the
// platform that has two.
func validBrowseChild(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	return !strings.ContainsAny(name, `/\`)
}

// classifyEntries turns directory records into wire entries, resolving links
// under the deadline that already bounded the read.
//
// os.ReadDir returns an fs.DirEntry built from the directory record alone, so a
// symlink — and a Windows junction, which reports the same way — has type
// ModeSymlink and IsDir() == false even when it points at a directory. A picker
// that trusted IsDir() would refuse to descend into exactly the symlinked
// project directories people navigate by, and it would look like the entry was
// a file. The local browser this replaces already stats them for that reason.
//
// Only links are stated, so an ordinary listing costs no extra syscalls. A
// broken or unreadable link degrades to "not a directory", which renders it as
// a non-navigable entry rather than dropping it — the name is still evidence
// that something is there.
//
// The SHARED deadline is what bounds the goroutine cost, and it needs no
// help: a stat that times out has by definition consumed the remaining budget,
// so every later link sees a non-positive one and is classified from its
// directory record without a syscall. One hung link therefore costs exactly one
// abandoned goroutine for the whole listing, not one per entry — which is what
// keeps a directory of 500 links into a dead mount from pinning 500 OS threads
// on a single keypress.
//
// An explicit "stop resolving" flag was tried here and removed: it could only
// ever fire in states the deadline already covers, so it was a second mechanism
// asserting what the first guarantees.
func classifyEntries(dir string, entries []os.DirEntry, deadline time.Time) []ipc.BrowseEntry {
	out := make([]ipc.BrowseEntry, 0, len(entries))
	for _, e := range entries {
		isDir := e.IsDir()
		if !isDir && e.Type()&os.ModeSymlink != 0 {
			isDir, _ = statIsDirWithin(filepath.Join(dir, e.Name()), time.Until(deadline))
		}
		out = append(out, ipc.BrowseEntry{Name: e.Name(), IsDir: isDir})
	}
	return out
}

// statIsDirWithin reports whether path is a directory, and whether the stat
// answered within d at all.
//
// os.Stat FOLLOWS symlinks, so it parks on a dead mount exactly as os.ReadDir
// does — and every caller here runs while the single-flight slot is held. A
// path that never answers degrades to "not a directory", which for a link is
// the same rendering a broken one already gets and for a drive letter means the
// drive is simply omitted.
//
// A non-positive budget is refused without spawning anything: an earlier call
// may have consumed the whole deadline, and a goroutine started only to be
// abandoned in the same breath is pure cost.
func statIsDirWithin(path string, d time.Duration) (isDir, answered bool) {
	if d <= 0 || !claimBlockingFSCall() {
		return false, false
	}
	ch := make(chan bool, 1)
	go func() {
		defer releaseBlockingFSCall()
		info, err := statPath(path)
		ch <- err == nil && info.IsDir()
	}()

	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case r := <-ch:
		return r, true
	case <-timer.C:
		return false, false
	}
}

// readDirWithin reads a directory, giving up after d.
//
// The timeout bounds the SYSCALL, which is the only thing here that can block:
// os.ReadDir on a dead NFS or SMB mount parks indefinitely, and a browser is
// exactly where an unreachable mount gets clicked on. Bounding only the loop
// over already-returned entries — which is pure CPU and cannot block — would
// leave the single-flight slot held forever by the first such click, and every
// later listing in the session answered "another directory listing is already
// running" with nothing running that would ever finish.
//
// The abandoned goroutine outlives this call, parked in the syscall until the
// mount answers or the daemon exits. That is deliberate: the read cannot be
// cancelled, so the choice is between leaking one goroutine per hung path and
// denying the feature outright. The channel is buffered so that goroutine can
// hand off its result and exit rather than blocking on a receiver that has gone.
func readDirWithin(dir string, d time.Duration) ([]os.DirEntry, error) {
	if !claimBlockingFSCall() {
		return nil, fmt.Errorf("too many directory listings are stuck on unresponsive paths; "+
			"listing %s was not attempted", dir)
	}
	type result struct {
		entries []os.DirEntry
		err     error
	}
	ch := make(chan result, 1)
	go func() {
		defer releaseBlockingFSCall()
		entries, err := readDirPath(dir)
		ch <- result{entries, err}
	}()

	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case r := <-ch:
		return r.entries, r.err
	case <-timer.C:
		return nil, fmt.Errorf("listing %s timed out after %v — is it an unreachable network mount?", dir, d)
	}
}

// maxBlockingFSCalls bounds how many uncancellable filesystem calls may be in
// flight across the whole process.
//
// Single-flight already serialises browsing, so this is not about concurrency —
// it is about ACCUMULATION. Every listing that times out leaves its goroutine
// parked in the syscall, and a goroutine blocked in a syscall pins an OS thread;
// the Go runtime aborts the process past 10 000 of them. A daemon stays up for
// weeks, so a client re-driving navigation into a mount that never comes back —
// a retry loop, or a user who simply keeps trying — has an unbounded path to
// killing the daemon and every pane with it. Past the cap a listing is refused
// with a message that names the situation, which is a far better outcome than
// the process dying.
//
// The cap is small on purpose. Reaching it at all means several paths are hung
// simultaneously, and at that point refusing loudly beats queueing more work
// behind mounts that are not answering.
const maxBlockingFSCalls = 8

// blockingFSCalls counts the calls currently outstanding.
//
// Package scope rather than a Daemon field, because the resource it protects is
// process-scoped: OS threads are counted by the runtime, not per Daemon. It also
// keeps browseDirResponse a pure function of its arguments, which is what makes
// the whole resolve→read→sort→cap chain testable without constructing a Daemon.
var blockingFSCalls atomic.Int64

// claimBlockingFSCall reserves a slot, reporting false when the cap is reached.
func claimBlockingFSCall() bool {
	if blockingFSCalls.Add(1) > maxBlockingFSCalls {
		blockingFSCalls.Add(-1)
		return false
	}
	return true
}

// releaseBlockingFSCall returns a slot. Called from the worker goroutine, which
// may be long after the caller gave up on it — that is the point: the slot is
// held for as long as the syscall really is, not for as long as anyone waits.
func releaseBlockingFSCall() { blockingFSCalls.Add(-1) }

// statPath and readDirPath are the seams the two blocking filesystem calls go
// through, following the package-var pattern of readHookSessionIDFn and
// claudeSessionExistsFn.
//
// Both exist for the same reason: the branches worth pinning are the ones where
// the syscall NEVER returns, and no real filesystem offers that on demand — a
// dead NFS mount is the production case and cannot be conjured in a unit test.
//
// readDirPath was added after review found the timeout test was passing for the
// wrong reason. It drove an ABSENT directory, which fails instantly with ENOENT,
// so the timer branch never ran: deleting the whole select-and-timer mechanism
// left that test green. A test that cannot fail is worse than no test, because
// it reports coverage of exactly the branch it never reaches.
var (
	statPath    = os.Stat
	readDirPath = os.ReadDir
)

// maxDirsExistPaths bounds one existence request.
//
// recentCWDMax is 5, so this is a hostile-client backstop rather than a product
// limit: what matters is that a single request cannot start an unbounded number
// of blocking stats.
const maxDirsExistPaths = 32

// handleDirsExistReq answers "which of these paths are still directories".
//
// The recent-locations pick list used to answer this in the TUI with os.Stat,
// which reads the machine drawing the UI. Against a remote daemon every server
// path failed that test and the list rendered silently empty — indistinguishable
// from a feature that had never been used, because structurally nothing failed.
//
// Its OWN single-flight slot, never browseScanning's. The two look like the same
// question and are not, because of TIMING: this check hands over to the directory
// browser when it gives up, the client gives up after recentScanTimeout (8 s),
// and the daemon holds its slot for as long as the syscall really runs — up to
// browseTimeout (10 s) when a stat parks. Sharing would make that handover's
// browse requests rejected by the very request that just timed out, dead-ending
// the dialog in exactly the dead-mount case the bound exists for. Same reasoning
// that gave gitDiscovering its own slot.
func (d *Daemon) handleDirsExistReq(conn *ipc.Conn, msg *ipc.Message) {
	var req ipc.DirsExistReqPayload
	if err := msg.DecodePayload(&req); err != nil {
		log.Printf("handleDirsExistReq: decode: %v", err)
	}
	rejection, ok := d.beginDirsCheck()
	if !ok {
		respondTo(conn, msg.ID, ipc.MsgDirsExistResp, rejection)
		return
	}
	go func() {
		defer d.dirsChecking.Store(false)
		respondTo(conn, msg.ID, ipc.MsgDirsExistResp, dirsExistResponse(req))
	}()
}

// beginDirsCheck claims the single-flight slot, returning the rejection to send
// when it is already taken.
//
// Split from the handler for the same reason as beginBrowseScan and
// beginGitDiscover: ipc.Conn cannot be built outside its package, so a test can
// only reach this decision through a seam. Without it the slot-independence
// property — this handler must NOT share browseScanning — is untestable, and it
// is the invariant here most likely to be "tidied" away by a later refactor.
func (d *Daemon) beginDirsCheck() (ipc.DirsExistRespPayload, bool) {
	if d.dirsChecking.CompareAndSwap(false, true) {
		return ipc.DirsExistRespPayload{}, true
	}
	return ipc.DirsExistRespPayload{
		Error: "another existence check is already running",
	}, false
}

// dirsExistResponse is the pure half.
//
// ONE deadline is shared by every stat, exactly as classifyEntries shares one
// with the directory read that precedes it. That makes the timeout
// self-propagating: a stat that times out has by definition spent the budget, so
// every later path sees a non-positive one and is dropped without a syscall —
// capping a list of dead mounts at ONE abandoned goroutine rather than one per
// entry.
//
// A path that does not answer is omitted rather than reported as an error. The
// caller's fallback is the directory browser either way, and an unreachable
// entry is exactly as unusable as a deleted one.
func dirsExistResponse(req ipc.DirsExistReqPayload) ipc.DirsExistRespPayload {
	if len(req.Paths) > maxDirsExistPaths {
		return ipc.DirsExistRespPayload{Error: "too many paths in one existence check"}
	}
	deadline := time.Now().Add(browseTimeout)
	var out ipc.DirsExistRespPayload
	for _, p := range req.Paths {
		if isDir, answered := statIsDirWithin(p, time.Until(deadline)); answered && isDir {
			out.Paths = append(out.Paths, p)
		}
	}
	return out
}
