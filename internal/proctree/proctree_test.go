package proctree

import (
	"fmt"
	"testing"
	"time"
)

// base is an arbitrary fixed instant. Tests express start times as offsets from
// it so "older" and "newer" are readable at the call site.
var base = time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)

func at(sec int) time.Time { return base.Add(time.Duration(sec) * time.Second) }

// p builds a table entry with a known start time.
func p(pid, ppid int, name string, sec int) ProcessEntry {
	return ProcessEntry{PID: pid, PPID: ppid, Name: name, Start: at(sec)}
}

// pNoStart builds a table entry whose start time the platform could not read.
func pNoStart(pid, ppid int, name string) ProcessEntry {
	return ProcessEntry{PID: pid, PPID: ppid, Name: name}
}

// names returns the tree's PIDs with their depths, for compact assertions.
func shape(root *Node) []string {
	var out []string
	Walk(root, func(n *Node) { out = append(out, fmt.Sprintf("%d@%d", n.PID, n.Depth)) })
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestBuild_DepthAndShape(t *testing.T) {
	table := []ProcessEntry{
		p(100, 1, "zsh", 0),
		p(200, 100, "node", 10),
		p(300, 200, "esbuild", 20),
		p(400, 100, "vim", 15),
		p(999, 1, "unrelated", 5),
	}

	trees := Build(table, []int{100})
	root := trees[100]
	if root == nil {
		t.Fatal("no tree for root 100")
	}

	// The pane's direct child is Depth 1 — the value the kill path's
	// "Depth >= 2" rule is defined against.
	if root.Depth != 1 {
		t.Errorf("root depth = %d, want 1", root.Depth)
	}

	want := []string{"100@1", "200@2", "300@3", "400@2"}
	if got := shape(root); !equal(got, want) {
		t.Errorf("shape = %v, want %v", got, want)
	}

	// A process that is not a descendant must not appear.
	if Find(root, 999) != nil {
		t.Error("unrelated process 999 is in the tree")
	}
}

func TestBuild_MissingRootYieldsNoTree(t *testing.T) {
	table := []ProcessEntry{p(200, 100, "node", 10)}

	trees := Build(table, []int{100})
	if _, ok := trees[100]; ok {
		t.Error("a root absent from the table produced a tree; a pane whose " +
			"child exited between enumeration and Build must be missing, not empty")
	}
}

func TestBuild_MultipleRootsAreIndependent(t *testing.T) {
	table := []ProcessEntry{
		p(100, 1, "zsh", 0),
		p(101, 1, "claude", 0),
		p(200, 100, "node", 10),
		p(201, 101, "mcp", 10),
	}

	trees := Build(table, []int{100, 101})
	if len(trees) != 2 {
		t.Fatalf("got %d trees, want 2", len(trees))
	}
	if Find(trees[100], 201) != nil {
		t.Error("pane 100's tree contains pane 101's child")
	}
	if Find(trees[101], 200) != nil {
		t.Error("pane 101's tree contains pane 100's child")
	}
}

// The PID-reuse splice: a child whose start time PRECEDES its supposed parent's
// cannot really be its child, because the PPID now names a recycled PID.
//
// Asserted as a tree SHAPE rather than as spliced()'s return value, because the
// property that matters is what the dialog renders and what the kill path will
// accept — a helper can be right while its call site drops the result.
func TestBuild_RejectsChildOlderThanParent(t *testing.T) {
	table := []ProcessEntry{
		p(100, 1, "zsh", 100),
		// 200 started BEFORE 100, so 100 cannot be its parent.
		p(200, 100, "impostor", 50),
	}

	trees := Build(table, []int{100})
	if Find(trees[100], 200) != nil {
		t.Error("a process older than its supposed parent was spliced into the " +
			"tree; the dialog would render it as a descendant and the kill path " +
			"would treat it as killable")
	}
}

// The direction chosen when a start time is unknown: KEEP the link.
//
// A missed splice shows one extra row. A false splice hides a real child. The
// kill path refuses an unknown Start separately, so nothing becomes killable on
// the strength of a link that could not be checked here.
func TestBuild_KeepsLinkWhenStartUnknown(t *testing.T) {
	for _, tc := range []struct {
		name  string
		table []ProcessEntry
	}{
		{"child start unknown", []ProcessEntry{
			p(100, 1, "zsh", 100),
			pNoStart(200, 100, "child"),
		}},
		{"parent start unknown", []ProcessEntry{
			pNoStart(100, 1, "zsh"),
			p(200, 100, "child", 50),
		}},
		{"both unknown", []ProcessEntry{
			pNoStart(100, 1, "zsh"),
			pNoStart(200, 100, "child"),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			trees := Build(tc.table, []int{100})
			if Find(trees[100], 200) == nil {
				t.Error("link dropped on an unknown start time; a real child is " +
					"now invisible, which is the worse direction")
			}
		})
	}
}

// A cycle in the parent map must truncate, not hang. This runs on the daemon's
// collector goroutine, so non-termination is a wedged daemon, not a slow test.
func TestBuild_TerminatesOnCycles(t *testing.T) {
	for _, tc := range []struct {
		name  string
		table []ProcessEntry
	}{
		{"self parent", []ProcessEntry{
			p(100, 1, "zsh", 0),
			{PID: 200, PPID: 200, Name: "self", Start: at(10)},
		}},
		{"two cycle", []ProcessEntry{
			p(100, 1, "zsh", 0),
			pNoStart(200, 100, "a"),
			pNoStart(201, 202, "b"),
			pNoStart(202, 201, "c"),
		}},
		// The shape that actually reaches the visited set. A table maps each
		// PID to ONE parent, so a cycle among a root's descendants cannot be
		// expressed — the only reachable cycle is one the ROOT is part of.
		// Without the visited set this recurses forever.
		{"root inside a two cycle", []ProcessEntry{
			pNoStart(100, 200, "zsh"),
			pNoStart(200, 100, "child"),
		}},
		{"root inside a three cycle", []ProcessEntry{
			pNoStart(100, 202, "zsh"),
			pNoStart(201, 100, "b"),
			pNoStart(202, 201, "c"),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			done := make(chan map[int]*Node, 1)
			go func() { done <- Build(tc.table, []int{100}) }()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("Build did not terminate on a cyclic parent map")
			}
		})
	}
}

func TestFlatten_EveryNodeBeforeItsAncestors(t *testing.T) {
	table := []ProcessEntry{
		p(100, 1, "zsh", 0),
		p(200, 100, "node", 10),
		p(300, 200, "esbuild", 20),
		p(400, 100, "vim", 15),
	}
	root := Build(table, []int{100})[100]

	order := Flatten(root)
	pos := map[int]int{}
	for i, n := range order {
		pos[n.PID] = i
	}

	// The property the kill sweep depends on: a parent is never signalled
	// before its own children, or it exits and reparents them mid-sweep.
	for _, n := range order {
		for _, c := range n.Children {
			if pos[c.PID] > pos[n.PID] {
				t.Errorf("child %d comes after parent %d in the kill order",
					c.PID, n.PID)
			}
		}
	}
	if len(order) != 4 {
		t.Errorf("flattened %d nodes, want 4", len(order))
	}
}

func TestDecorate_MissingPIDKeepsUnknownCPU(t *testing.T) {
	table := []ProcessEntry{
		p(100, 1, "zsh", 0),
		p(200, 100, "node", 10),
	}
	trees := Build(table, []int{100})

	Decorate(trees, map[int]uint64{100: 4096}, map[int]float64{100: 12.5})

	root := trees[100]
	if root.RSSBytes != 4096 || root.CPUPct != 12.5 {
		t.Errorf("root not decorated: rss=%d cpu=%v", root.RSSBytes, root.CPUPct)
	}

	child := Find(root, 200)
	if child.RSSBytes != 0 {
		t.Errorf("missing RSS should stay 0, got %d", child.RSSBytes)
	}
	// The load-bearing half: a PID with no CPU answer must read UNKNOWN, not
	// 0. Zero renders as "0%" — a confident claim of idle about a process we
	// know nothing about, in a dialog for finding things that spin.
	if child.CPUPct != UnknownCPU {
		t.Errorf("missing CPU = %v, want UnknownCPU (%v)", child.CPUPct, UnknownCPU)
	}
}

func TestPIDs_CoversWholeTree(t *testing.T) {
	table := []ProcessEntry{
		p(100, 1, "zsh", 0),
		p(200, 100, "node", 10),
		p(300, 200, "esbuild", 20),
	}
	trees := Build(table, []int{100})

	got := map[int]bool{}
	for _, pid := range PIDs(trees) {
		got[pid] = true
	}
	for _, want := range []int{100, 200, 300} {
		if !got[want] {
			t.Errorf("PID %d missing from the batch read set; it would never "+
				"get RSS or CPU", want)
		}
	}
}
