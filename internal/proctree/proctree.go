// Package proctree builds per-pane process trees from an OS process table.
//
// Split like internal/notify and internal/memreport: every file with logic is
// platform-neutral and takes its syscalls as parameters, and the build-tagged
// files hold enumeration only. CI is Linux, so anything behind //go:build
// windows or //go:build darwin is never compiled by dev.sh test — keeping the
// logic here is what makes it testable at all.
//
// This package deliberately performs NO classification. The previous attempt at
// this feature inferred process identity from image paths and was wrong on both
// platforms; the daemon spawned the PTY children, so it knows their PIDs as
// fact and hands them here as roots.
package proctree

import (
	"errors"
	"time"
)

// ErrUnsupported is returned by enumeration on a platform with no process
// table source. Distinct from an empty table: "we cannot look" and "nothing is
// there" are different claims, and the dialog renders them differently.
var ErrUnsupported = errors.New("proctree: not supported on this platform")

// Sources are the platform hooks Collect needs.
//
// Injected rather than called directly so the whole collection sequence —
// including the Windows two-pass — is testable on Linux CI, where none of the
// real implementations for the other platforms are even compiled.
type Sources struct {
	// Table enumerates the process table.
	Table func() ([]ProcessEntry, error)
	// CPU reads CPU usage for the given PIDs.
	CPU func([]int) cpuReading
	// RSS reads resident memory for the given PIDs.
	RSS func([]int) map[int]uint64
	// HasStarts is false on platforms whose Table cannot supply start times,
	// which is what triggers the second pass.
	HasStarts bool
	// EnrichStarts fills start times for a bounded set of PIDs.
	EnrichStarts func([]ProcessEntry, []int) []ProcessEntry
	// Sampled reports whether CPU is a delta over our window rather than a
	// kernel average. Carried to the dialog, which says so.
	Sampled bool
}

// DefaultSources wires the current platform's implementations.
func DefaultSources(rss func([]int) map[int]uint64) Sources {
	return Sources{
		Table:        readTable,
		CPU:          readCPU,
		RSS:          rss,
		HasStarts:    tableHasStarts,
		EnrichStarts: enrichStarts,
		Sampled:      cpuIsSampled,
	}
}

// Collect enumerates the process table and returns decorated trees for the
// given root PIDs.
//
// On a platform whose table carries start times this is one pass. Where it does
// not — Windows, where GetProcessTimes needs a handle per process — a TENTATIVE
// tree is built from parent links alone, its PIDs are the only ones enriched
// with start times, and the tree is rebuilt so the splice rejection can run.
// The handle count is bounded by one workspace's descendants rather than by the
// size of the machine.
func (s *Sampler) Collect(now time.Time, rootPIDs []int, src Sources) (map[int]*Node, error) {
	table, err := src.Table()
	if err != nil {
		return nil, err
	}

	trees := Build(table, rootPIDs)
	if !src.HasStarts && src.EnrichStarts != nil {
		table = src.EnrichStarts(table, PIDs(trees))
		trees = Build(table, rootPIDs)
	}

	pids := PIDs(trees)
	starts := make(map[int]time.Time, len(pids))
	for _, root := range trees {
		Walk(root, func(n *Node) { starts[n.PID] = n.Start })
	}

	var rss map[int]uint64
	if src.RSS != nil {
		rss = src.RSS(pids)
	}
	cpu := s.Update(now, src.CPU(pids), starts)

	Decorate(trees, rss, cpu)
	return trees, nil
}

// ProcessEntry is one row of the OS process table.
//
// Start is zero when the platform could not read it — a process that refuses
// OpenProcess, most commonly. Callers must treat a zero Start as "unknown",
// never as "epoch": Build and the kill path each pick the safe direction for
// that case, and they are not the same direction.
type ProcessEntry struct {
	PID   int
	PPID  int
	Name  string
	Start time.Time
}

// Node is one process in a pane's tree.
//
// Depth 1 is the pane's direct child — the shell or agent the PTY spawned.
// Everything below it is a descendant, and Depth >= 2 is what the kill path is
// permitted to touch.
type Node struct {
	PID      int
	PPID     int
	Name     string
	Start    time.Time
	RSSBytes uint64
	CPUPct   float64
	Depth    int
	Children []*Node
}

// UnknownCPU is the CPUPct value meaning "no answer".
//
// Deliberately negative rather than zero. A first sample, a process that
// appeared this tick, a platform with no CPU source and a negative delta from
// PID reuse all mean the same thing: we do not know. Zero is a confident claim
// of "idle", which is the precise wrong answer in a dialog whose whole purpose
// is finding something that is spinning.
const UnknownCPU = -1.0

// Build returns one tree per root PID, the root at Depth 1.
//
// A root PID absent from the table yields no tree, so a pane whose child exited
// between enumeration and this call is simply missing rather than empty.
func Build(table []ProcessEntry, rootPIDs []int) map[int]*Node {
	byPID := make(map[int]ProcessEntry, len(table))
	for _, e := range table {
		byPID[e.PID] = e
	}

	children := make(map[int][]ProcessEntry, len(table))
	for _, e := range table {
		if e.PID == e.PPID {
			// A self-parent is the degenerate cycle. Dropping the link here
			// keeps the walk below from needing a special case for it.
			continue
		}
		parent, ok := byPID[e.PPID]
		if ok && spliced(parent, e) {
			continue
		}
		children[e.PPID] = append(children[e.PPID], e)
	}

	out := make(map[int]*Node, len(rootPIDs))
	for _, root := range rootPIDs {
		e, ok := byPID[root]
		if !ok {
			continue
		}
		// One visited set PER ROOT, not one shared across roots: two panes can
		// legitimately be ancestors of the same process only in a corrupt
		// table, but sharing the set would silently truncate the second pane's
		// tree in that case rather than showing both.
		visited := map[int]bool{root: true}
		out[root] = walk(e, 1, children, visited)
	}
	return out
}

// spliced reports whether a parent -> child link must be rejected because the
// child is older than its parent.
//
// A process cannot predate its own parent, so this link is a PID that was
// recycled: the table's PPID now points at a process that never spawned it.
// Left in, it splices an unrelated process into a pane's tree, where the dialog
// renders it as a descendant and the kill path treats it as killable.
//
// Requires BOTH start times. Where either is unknown the link is KEPT, because
// a missed splice shows one extra row while a false splice hides a real child —
// and the kill path refuses an unknown Start separately, so nothing becomes
// killable on the strength of a link this function could not check.
func spliced(parent, child ProcessEntry) bool {
	if parent.Start.IsZero() || child.Start.IsZero() {
		return false
	}
	return child.Start.Before(parent.Start)
}

// walk builds one subtree breadth-first, refusing to revisit a PID.
//
// The visited set is not defensive tidiness. A process table is a parent map,
// and a racing or corrupt one can contain a cycle — PID reuse mid-enumeration
// will produce one. Without the set this recursion does not terminate, and it
// runs on the daemon's collector goroutine.
func walk(e ProcessEntry, depth int, children map[int][]ProcessEntry, visited map[int]bool) *Node {
	n := &Node{
		PID:    e.PID,
		PPID:   e.PPID,
		Name:   e.Name,
		Start:  e.Start,
		Depth:  depth,
		CPUPct: UnknownCPU,
	}
	for _, c := range children[e.PID] {
		if visited[c.PID] {
			continue
		}
		visited[c.PID] = true
		n.Children = append(n.Children, walk(c, depth+1, children, visited))
	}
	return n
}

// PIDs returns every PID in the given trees, for batching the RSS and CPU reads
// that decorate them.
func PIDs(trees map[int]*Node) []int {
	var out []int
	for _, root := range trees {
		Walk(root, func(n *Node) { out = append(out, n.PID) })
	}
	return out
}

// Walk calls fn for every node in a tree, parents before children.
func Walk(n *Node, fn func(*Node)) {
	if n == nil {
		return
	}
	fn(n)
	for _, c := range n.Children {
		Walk(c, fn)
	}
}

// Decorate fills RSS and CPU from batch reads. A PID missing from rss keeps a
// zero RSS; a PID missing from cpu keeps UnknownCPU, which is why Walk sets it
// at construction rather than leaving Go's zero value in place.
func Decorate(trees map[int]*Node, rss map[int]uint64, cpu map[int]float64) {
	for _, root := range trees {
		Walk(root, func(n *Node) {
			if v, ok := rss[n.PID]; ok {
				n.RSSBytes = v
			}
			if v, ok := cpu[n.PID]; ok {
				n.CPUPct = v
			}
		})
	}
}

// Find returns the node with the given PID in a tree, or nil.
//
// The kill path uses this to re-derive a target from a FRESH tree rather than
// trusting the PID and depth a client sent.
func Find(root *Node, pid int) *Node {
	var found *Node
	Walk(root, func(n *Node) {
		if found == nil && n.PID == pid {
			found = n
		}
	})
	return found
}

// Flatten returns a node and all its descendants, ordered so that every node
// appears BEFORE its own ancestors.
//
// That, not strict depth ordering, is the property the kill path needs: a
// parent must not be signalled before its children, or it exits and reparents
// them into the middle of the sweep. Reversing a parents-first walk guarantees
// it, while a sort by Depth would only imply it.
func Flatten(n *Node) []*Node {
	var out []*Node
	Walk(n, func(x *Node) { out = append(out, x) })
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}
