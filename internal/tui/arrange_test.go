package tui

import (
	"math"
	"reflect"
	"strings"
	"testing"
)

// arrPanes returns n test panes named a, b, c, … in reading order.
func arrPanes(n int) []*PaneModel {
	out := make([]*PaneModel, n)
	for i := range out {
		out[i] = newTestPane(string(rune('a' + i)))
	}
	return out
}

// arrTree renders a tree as (a|(b/c)) — layoutString over SerializeLayout.
func arrTree(n *LayoutNode) string { return layoutString(SerializeLayout(n)) }

// arrIDs lists a tree's panes in reading (Leaves) order.
func arrIDs(n *LayoutNode) string {
	var ids []string
	for _, p := range n.Leaves() {
		ids = append(ids, p.ID)
	}
	return strings.Join(ids, ",")
}

// arrPaneIDs lists a pane slice the way arrIDs lists a tree.
func arrPaneIDs(panes []*PaneModel) string {
	ids := make([]string, len(panes))
	for i, p := range panes {
		ids[i] = p.ID
	}
	return strings.Join(ids, ",")
}

// arrAreas maps each pane to the fraction of the tab it covers, from the
// ratios alone — what a layout promises before any cell rounding.
func arrAreas(n *LayoutNode, share float64, out map[string]float64) {
	if n == nil {
		return
	}
	if n.IsLeaf() {
		out[n.Pane.ID] = share
		return
	}
	arrAreas(n.Left, share*n.Ratio, out)
	arrAreas(n.Right, share*(1-n.Ratio), out)
}

func arrAssertEqualAreas(t *testing.T, root *LayoutNode, n int) {
	t.Helper()
	areas := map[string]float64{}
	arrAreas(root, 1, areas)
	if len(areas) != n {
		t.Fatalf("tree %s holds %d panes, want %d", arrTree(root), len(areas), n)
	}
	want := 1 / float64(n)
	for id, got := range areas {
		if math.Abs(got-want) > 1e-9 {
			t.Errorf("tree %s: pane %s covers %.6f of the tab, want %.6f", arrTree(root), id, got, want)
		}
	}
}

// arrGridRows splits a grid into its rows: the Left of every top/bottom split
// down the chain, then the last Right.
func arrGridRows(n *LayoutNode) []*LayoutNode {
	var rows []*LayoutNode
	for n != nil && !n.IsLeaf() && n.Split == SplitVertical {
		rows = append(rows, n.Left)
		n = n.Right
	}
	return append(rows, n)
}

func TestEvenOut_GivesEveryPaneTheSameArea(t *testing.T) {
	t.Parallel()
	a := arrPanes(5)
	chain := &LayoutNode{Split: SplitHorizontal, Ratio: 0.5, Left: NewLeaf(a[0]),
		Right: &LayoutNode{Split: SplitHorizontal, Ratio: 0.5, Left: NewLeaf(a[1]),
			Right: &LayoutNode{Split: SplitHorizontal, Ratio: 0.5, Left: NewLeaf(a[2]), Right: NewLeaf(a[3])}}}
	balanced := &LayoutNode{Split: SplitHorizontal, Ratio: 0.2,
		Left:  &LayoutNode{Split: SplitVertical, Ratio: 0.9, Left: NewLeaf(a[0]), Right: NewLeaf(a[1])},
		Right: &LayoutNode{Split: SplitVertical, Ratio: 0.6, Left: NewLeaf(a[2]), Right: NewLeaf(a[3])}}
	lopsided := &LayoutNode{Split: SplitHorizontal, Ratio: 0.5, Left: NewLeaf(a[0]),
		Right: &LayoutNode{Split: SplitVertical, Ratio: 0.5, Left: NewLeaf(a[1]),
			Right: &LayoutNode{Split: SplitHorizontal, Ratio: 0.5, Left: NewLeaf(a[2]),
				Right: &LayoutNode{Split: SplitVertical, Ratio: 0.5, Left: NewLeaf(a[3]), Right: NewLeaf(a[4])}}}}
	for _, tc := range []struct {
		name string
		root *LayoutNode
		n    int
	}{{"chain", chain, 4}, {"balanced", balanced, 4}, {"lopsided", lopsided, 5}} {
		t.Run(tc.name, func(t *testing.T) {
			before := arrTree(tc.root)
			got := evenOut(tc.root)
			if arrTree(got) != before {
				t.Errorf("even out changed the shape: %s -> %s", before, arrTree(got))
			}
			arrAssertEqualAreas(t, got, tc.n)
		})
	}
}

func TestEvenOut_PlaceholderSideKeepsItsRatio(t *testing.T) {
	t.Parallel()
	a := arrPanes(2)
	inner := &LayoutNode{Split: SplitVertical, Ratio: 0.3, Left: NewLeaf(a[0]), Right: &LayoutNode{Ratio: 0.5}}
	root := &LayoutNode{Split: SplitHorizontal, Ratio: 0.2, Left: inner, Right: NewLeaf(a[1])}

	got := evenOut(root)

	if got.Ratio != 0.5 {
		t.Errorf("root ratio = %v, want 0.5: one pane on each side, the placeholder counts as none", got.Ratio)
	}
	if got.Left.Ratio != 0.3 {
		t.Errorf("split beside a placeholder = %v, want its ratio kept (0.3)", got.Left.Ratio)
	}
	if ph := got.Left.Right; ph.Pane != nil || ph.Left != nil || ph.Right != nil {
		t.Error("the placeholder must survive as a placeholder")
	}
}

func TestArrange_NeverMutatesItsInput(t *testing.T) {
	t.Parallel()
	a := arrPanes(4)
	build := func() *LayoutNode {
		return &LayoutNode{Split: SplitHorizontal, Ratio: 0.3, Left: NewLeaf(a[0]),
			Right: &LayoutNode{Split: SplitVertical, Ratio: 0.7, Left: NewLeaf(a[1]),
				Right: &LayoutNode{Split: SplitHorizontal, Ratio: 0.4, Left: NewLeaf(a[2]), Right: NewLeaf(a[3])}}}
	}
	for _, tc := range []struct {
		name string
		run  func(*LayoutNode) *LayoutNode
	}{
		{"evenOut", evenOut},
		{"moveLeafBeside", func(r *LayoutNode) *LayoutNode { return moveLeafBeside(r, a[0], a[2], zoneTop) }},
		{"swapLeaves", func(r *LayoutNode) *LayoutNode { return swapLeaves(r, a[0], a[3]) }},
		{"arrangeLayout grid", func(r *LayoutNode) *LayoutNode { return arrangeLayout(layoutGrid, r, a[1]) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := build()
			want := SerializeLayout(root)
			got := tc.run(root)
			if got == nil {
				t.Fatal("nil result")
			}
			if got == root {
				t.Error("returned the input root itself")
			}
			if !reflect.DeepEqual(SerializeLayout(root), want) {
				t.Errorf("input mutated: %s, want %s", arrTree(root), layoutString(want))
			}
		})
	}
}

func TestColumnsAndRows_EqualSharesInReadingOrder(t *testing.T) {
	t.Parallel()
	for n := 1; n <= 9; n++ {
		p := arrPanes(n)
		arrAssertEqualAreas(t, columnsLayout(p), n)
		arrAssertEqualAreas(t, rowsLayout(p), n)
		if got := arrIDs(columnsLayout(p)); got != arrPaneIDs(p) {
			t.Errorf("n=%d: columns order %s, want %s", n, got, arrPaneIDs(p))
		}
	}
	if got := arrTree(columnsLayout(arrPanes(3))); got != "(a|(b|c))" {
		t.Errorf("columns = %s, want (a|(b|c))", got)
	}
	if got := arrTree(rowsLayout(arrPanes(3))); got != "(a/(b/c))" {
		t.Errorf("rows = %s, want (a/(b/c))", got)
	}
	if columnsLayout(nil) != nil || rowsLayout(nil) != nil {
		t.Error("no panes must build no tree")
	}
}

// Cell rounding: int(w*1/(n-i)) down a chain keeps every column within one
// cell of every other, and every column at least minPaneW once w >= n*minPaneW.
func TestColumns_CellWidthsDifferByAtMostOne(t *testing.T) {
	t.Parallel()
	for n := 2; n <= 16; n++ {
		root := columnsLayout(arrPanes(n))
		for w := n * minPaneW; w <= 400; w++ {
			var rects []PaneRect
			root.CollectRects(0, 0, w, 20, &rects)
			lo, hi := rects[0].W, rects[0].W
			for _, r := range rects {
				lo, hi = min(lo, r.W), max(hi, r.W)
			}
			if hi-lo > 1 {
				t.Fatalf("n=%d w=%d: column widths span %d..%d", n, w, lo, hi)
			}
			if !fitsMinSize(root, w, 20) {
				t.Fatalf("n=%d w=%d: %d columns must fit %d cells", n, w, n, w)
			}
		}
		if fitsMinSize(root, n*minPaneW-1, 20) {
			t.Errorf("n=%d: %d cells cannot hold %d columns of %d", n, n*minPaneW-1, n, minPaneW)
		}
	}
}

func TestGridLayout_ShapeTable(t *testing.T) {
	t.Parallel()
	want := map[int][]int{2: {2}, 3: {2, 1}, 4: {2, 2}, 5: {3, 2}, 6: {3, 3}, 7: {3, 3, 1}, 8: {3, 3, 2}, 9: {3, 3, 3}}
	for n := 2; n <= 9; n++ {
		p := arrPanes(n)
		root := gridLayout(p)
		rows := arrGridRows(root)
		var counts []int
		for _, row := range rows {
			counts = append(counts, len(row.Leaves()))
			arrAssertEqualAreas(t, row, len(row.Leaves())) // equal widths within a row
		}
		if !reflect.DeepEqual(counts, want[n]) {
			t.Errorf("n=%d: rows %v, want %v (tree %s)", n, counts, want[n], arrTree(root))
		}
		if got := arrIDs(root); got != arrPaneIDs(p) {
			t.Errorf("n=%d: grid order %s, want %s", n, got, arrPaneIDs(p))
		}
		// Equal heights: walk the top/bottom chain.
		share, node := 1.0, root
		var heights []float64
		for node != nil && !node.IsLeaf() && node.Split == SplitVertical {
			heights = append(heights, share*node.Ratio)
			share *= 1 - node.Ratio
			node = node.Right
		}
		heights = append(heights, share)
		for i, h := range heights {
			if math.Abs(h-1/float64(len(heights))) > 1e-9 {
				t.Errorf("n=%d: row %d height %.6f, want %.6f", n, i, h, 1/float64(len(heights)))
			}
		}
	}
}

func TestMainStackLayout(t *testing.T) {
	t.Parallel()
	p := arrPanes(4)
	root := mainStackLayout(p, p[2])
	if got := arrTree(root); got != "(c|(a/(b/d)))" {
		t.Errorf("main + stack = %s, want (c|(a/(b/d)))", got)
	}
	if root.Ratio != 0.5 {
		t.Errorf("main ratio = %v, want 0.5", root.Ratio)
	}
	arrAssertEqualAreas(t, root.Right, 3)
	if got := arrTree(mainStackLayout(p[:2], p[1])); got != "(b|a)" {
		t.Errorf("two panes = %s, want (b|a)", got)
	}
	if got := arrTree(mainStackLayout(p, newTestPane("zz"))); got != "(a|(b/(c/d)))" {
		t.Errorf("a main outside the list = %s, want the first pane as main", got)
	}
}

func TestSpiralLayout(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		n    int
		want string
	}{{1, "a"}, {2, "(a|b)"}, {4, "(a|(b/(c|d)))"}, {5, "(a|(b/(c|(d/e))))"}} {
		root := spiralLayout(arrPanes(tc.n))
		if got := arrTree(root); got != tc.want {
			t.Errorf("n=%d: spiral = %s, want %s", tc.n, got, tc.want)
		}
		arrAssertEqualAreas(t, root, tc.n)
	}
	if spiralLayout(nil) != nil {
		t.Error("no panes must build no tree")
	}
}

func TestArrangeLayout_DispatchesEveryKind(t *testing.T) {
	t.Parallel()
	p := arrPanes(4)
	// a|(b/(c/d)) at halves: every kind gives a DIFFERENT tree from it.
	root := &LayoutNode{Split: SplitHorizontal, Ratio: 0.5, Left: NewLeaf(p[0]),
		Right: &LayoutNode{Split: SplitVertical, Ratio: 0.5, Left: NewLeaf(p[1]),
			Right: &LayoutNode{Split: SplitVertical, Ratio: 0.5, Left: NewLeaf(p[2]), Right: NewLeaf(p[3])}}}
	for _, tc := range []struct {
		kind layoutKind
		want string
	}{
		{layoutColumns, "(a|(b|(c|d)))"},
		{layoutRows, "(a/(b/(c/d)))"},
		{layoutGrid, "((a|b)/(c|d))"},
		{layoutMain, "(c|(a/(b/d)))"},
		{layoutSpiral, "(a|(b/(c|d)))"},
	} {
		if got := arrTree(arrangeLayout(tc.kind, root, p[2])); got != tc.want {
			t.Errorf("kind %d = %s, want %s", tc.kind, got, tc.want)
		}
	}
	even := arrangeLayout(layoutEven, root, p[2])
	if arrTree(even) != "(a|(b/(c/d)))" || math.Abs(even.Ratio-0.25) > 1e-9 {
		t.Errorf("even out = %s (root ratio %v), want the same shape at 0.25", arrTree(even), even.Ratio)
	}
	if arrangeLayout(layoutNone, root, p[2]) != nil {
		t.Error("layoutNone must build nothing")
	}
}

func TestMoveLeafBeside(t *testing.T) {
	t.Parallel()
	p := arrPanes(3) // a, b, c
	base := func() *LayoutNode {
		return &LayoutNode{Split: SplitHorizontal, Ratio: 0.3, Left: NewLeaf(p[0]),
			Right: &LayoutNode{Split: SplitVertical, Ratio: 0.7, Left: NewLeaf(p[1]), Right: NewLeaf(p[2])}}
	}
	for _, tc := range []struct {
		name      string
		x, y      *PaneModel
		zone      dropZone
		want      string
		wantRatio float64 // the root's ratio after the move
	}{
		{"left of b", p[0], p[1], zoneLeft, "((a|b)/c)", 0.7},
		{"right of b", p[0], p[1], zoneRight, "((b|a)/c)", 0.7},
		{"top of b", p[0], p[1], zoneTop, "((a/b)/c)", 0.7},
		{"bottom of b", p[0], p[1], zoneBottom, "((b/a)/c)", 0.7},
		// b's sibling c is promoted into their parent, then split.
		{"sibling: left of c", p[1], p[2], zoneLeft, "(a|(b|c))", 0.3},
		{"sibling: right of c", p[1], p[2], zoneRight, "(a|(c|b))", 0.3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := moveLeafBeside(base(), tc.x, tc.y, tc.zone)
			if got == nil {
				t.Fatal("nil result")
			}
			if s := arrTree(got); s != tc.want {
				t.Errorf("tree = %s, want %s", s, tc.want)
			}
			if got.Ratio != tc.wantRatio {
				t.Errorf("root ratio = %v, want %v (the promoted split keeps its ratio)", got.Ratio, tc.wantRatio)
			}
			if split := got.FindLeaf(tc.x.ID); split == nil {
				t.Fatal("the moved pane is missing")
			}
		})
	}
	// The new split holding x and y sits at 0.5.
	if got := moveLeafBeside(base(), p[0], p[1], zoneLeft); got.Left.Ratio != 0.5 {
		t.Errorf("new split ratio = %v, want 0.5", got.Left.Ratio)
	}
	for _, tc := range []struct {
		name string
		root *LayoutNode
		x, y *PaneModel
		zone dropZone
	}{
		{"centre is not an edge", base(), p[0], p[1], zoneCenter},
		{"no zone", base(), p[0], p[1], zoneNone},
		{"onto itself", base(), p[0], p[0], zoneLeft},
		{"x not in the tree", base(), newTestPane("zz"), p[1], zoneLeft},
		{"y not in the tree", base(), p[0], newTestPane("zz"), zoneLeft},
		{"single-pane tree", NewLeaf(p[0]), p[0], p[1], zoneLeft},
	} {
		if got := moveLeafBeside(tc.root, tc.x, tc.y, tc.zone); got != nil {
			t.Errorf("%s: got %s, want nil", tc.name, arrTree(got))
		}
	}
}

func TestSwapLeaves(t *testing.T) {
	t.Parallel()
	p := arrPanes(3)
	root := &LayoutNode{Split: SplitHorizontal, Ratio: 0.3, Left: NewLeaf(p[0]),
		Right: &LayoutNode{Split: SplitVertical, Ratio: 0.7, Left: NewLeaf(p[1]), Right: NewLeaf(p[2])}}
	got := swapLeaves(root, p[0], p[2])
	if s := arrTree(got); s != "(c|(b/a))" {
		t.Errorf("swap = %s, want (c|(b/a))", s)
	}
	if got.Ratio != 0.3 || got.Right.Ratio != 0.7 {
		t.Errorf("ratios = %v/%v, want 0.3/0.7 unchanged", got.Ratio, got.Right.Ratio)
	}
	if swapLeaves(root, p[0], p[0]) != nil || swapLeaves(root, p[0], newTestPane("zz")) != nil {
		t.Error("a swap with itself or with an absent pane must return nil")
	}
}

func TestFitsMinSize_Boundaries(t *testing.T) {
	t.Parallel()
	p := arrPanes(3)
	ph := &LayoutNode{Split: SplitHorizontal, Ratio: 0.5, Left: NewLeaf(p[0]), Right: &LayoutNode{Ratio: 0.5}}
	for _, tc := range []struct {
		name string
		root *LayoutNode
		w, h int
		want bool
	}{
		{"three columns in 30", columnsLayout(p), 30, 4, true},
		{"three columns in 29", columnsLayout(p), 29, 4, false},
		{"two rows in 8", rowsLayout(p[:2]), 10, 8, true},
		{"two rows in 7", rowsLayout(p[:2]), 10, 7, false},
		{"leaf at the minimum", NewLeaf(p[0]), minPaneW, minPaneH, true},
		{"leaf one column short", NewLeaf(p[0]), minPaneW - 1, minPaneH, false},
		{"leaf one row short", NewLeaf(p[0]), minPaneW, minPaneH - 1, false},
		{"a placeholder counts as a pane", ph, 19, 4, false},
		{"a placeholder that fits", ph, 20, 4, true},
		{"unknown geometry fits", columnsLayout(arrPanes(5)), 0, 0, true},
	} {
		if got := fitsMinSize(tc.root, tc.w, tc.h); got != tc.want {
			t.Errorf("%s: fitsMinSize(%dx%d) = %v, want %v", tc.name, tc.w, tc.h, got, tc.want)
		}
	}
}

// A clamp that widens a too-narrow child steals cells from its sibling
// instead of reducing the tab it fits in — the deficit must still surface as
// a refusal, not vanish into a sibling with room to spare.
func TestFitsMinSize_ClampOnOneSideCannotStealFromTheOther(t *testing.T) {
	t.Parallel()
	p := arrPanes(2)
	h := &LayoutNode{Split: SplitHorizontal, Ratio: 0.1, Left: NewLeaf(p[0]), Right: NewLeaf(p[1])}
	if fitsMinSize(h, 50, 20) {
		t.Error("left gets 10% of 50 = 5 cells, under minPaneW; a left clamp stealing width from the right must not hide that")
	}
	v := &LayoutNode{Split: SplitVertical, Ratio: 0.1, Left: NewLeaf(p[0]), Right: NewLeaf(p[1])}
	if fitsMinSize(v, 20, 30) {
		t.Error("top gets 10% of 30 = 3 rows, under minPaneH; a top clamp stealing height from the bottom must not hide that")
	}
}

// Review Focus 1: CollectRects clamps every child up to the minimum, so a
// check over its rects always passes.
func TestFitsMinSize_CatchesWhatCollectRectsClamps(t *testing.T) {
	t.Parallel()
	root := columnsLayout(arrPanes(5))
	var rects []PaneRect
	root.CollectRects(0, 0, 40, 20, &rects)
	for _, r := range rects {
		if r.W < minPaneW {
			t.Fatalf("fixture: CollectRects returned a %d-wide rect; it is expected to clamp every rect up to %d", r.W, minPaneW)
		}
	}
	if fitsMinSize(root, 40, 20) {
		t.Error("five columns in 40 cells need 50: fitsMinSize must refuse although every clamped rect reads as wide enough")
	}
}

func TestFitsMinSize_AgreesWithCollectRectsWhenItFits(t *testing.T) {
	t.Parallel()
	p := arrPanes(6)
	for name, root := range map[string]*LayoutNode{
		"columns": columnsLayout(p[:3]),
		"rows":    rowsLayout(p[:3]),
		"grid":    gridLayout(p[:5]),
		"main":    mainStackLayout(p[:4], p[1]),
		"spiral":  spiralLayout(p),
	} {
		fitted := 0
		for w := 10; w <= 130; w += 3 {
			for h := 4; h <= 44; h += 2 {
				if !fitsMinSize(root, w, h) {
					continue
				}
				fitted++
				var rects []PaneRect
				root.CollectRects(0, 0, w, h, &rects)
				area := 0
				for _, r := range rects {
					area += r.W * r.H
				}
				if area != w*h {
					t.Fatalf("%s at %dx%d: fitsMinSize said yes but CollectRects covers %d of %d cells — a clamp fired", name, w, h, area, w*h)
				}
			}
		}
		if fitted == 0 {
			t.Errorf("%s: no size fitted — the property was never exercised", name)
		}
	}
}
