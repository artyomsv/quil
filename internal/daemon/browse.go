package daemon

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
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

// browseTimeout bounds one listing.
const browseTimeout = 10 * time.Second

// handleBrowseDirReq answers a directory listing.
//
// Worker goroutine + single-flight, the shape handleClaudeSessionsReq
// established: this is file I/O, and running it on the conn's dispatch goroutine
// would freeze every other message from that client. The rejection still echoes
// the requested path — the TUI matches on that echo, so an unmatched error is
// dropped as stale and the field sits on its pending state until the timeout
// fires, showing nothing about why.
func (d *Daemon) handleBrowseDirReq(conn *ipc.Conn, msg *ipc.Message) {
	rejection, ok := d.beginBrowseScan(msg)
	if !ok {
		respondTo(conn, msg.ID, ipc.MsgBrowseDirResp, rejection)
		return
	}
	fallback := d.defaultCWD()
	go func() {
		defer d.browseScanning.Store(false)
		respondTo(conn, msg.ID, ipc.MsgBrowseDirResp, browseDirResponse(browseReq(msg), fallback))
	}()
}

// beginBrowseScan claims the single-flight slot, returning the rejection to send
// when it is already taken.
//
// Split from the handler for the reason beginClaudeSessionsScan is: ipc.Conn
// cannot be constructed outside its own package, so the handler wrapper is
// untestable — but the decision it makes is the part worth pinning.
func (d *Daemon) beginBrowseScan(msg *ipc.Message) (ipc.BrowseDirRespPayload, bool) {
	if d.browseScanning.CompareAndSwap(false, true) {
		return ipc.BrowseDirRespPayload{}, true
	}
	return ipc.BrowseDirRespPayload{
		Path:  browseReq(msg).Path,
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
	abs, err := filepath.Abs(target)
	if err != nil {
		out.Error = fmt.Sprintf("resolve %s: %v", target, err)
		return out
	}
	out.Resolved = filepath.Clean(abs)
	// A root directory is its own parent; reporting that would render an "up"
	// row that navigates to where the user already is. At a root, Roots carries
	// whatever sits above it instead — the drive list on Windows, nothing on
	// Unix. The client cannot decide either of these: both depend on the
	// platform holding the filesystem, not the one drawing the picker.
	if parent := filepath.Dir(out.Resolved); parent != out.Resolved {
		out.Parent = parent
	} else {
		out.Roots = filesystemRoots()
	}

	entries, err := readDirWithin(out.Resolved, browseTimeout)
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
	out.Entries = make([]ipc.BrowseEntry, 0, len(entries))
	for _, e := range entries {
		out.Entries = append(out.Entries, ipc.BrowseEntry{
			Name:  e.Name(),
			IsDir: entryIsDir(out.Resolved, e),
		})
	}
	return out
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

// entryIsDir reports whether e is a directory, resolving links.
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
func entryIsDir(dir string, e os.DirEntry) bool {
	if e.IsDir() {
		return true
	}
	if e.Type()&os.ModeSymlink == 0 {
		return false
	}
	info, err := os.Stat(filepath.Join(dir, e.Name()))
	return err == nil && info.IsDir()
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
	type result struct {
		entries []os.DirEntry
		err     error
	}
	ch := make(chan result, 1)
	go func() {
		entries, err := os.ReadDir(dir)
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
