package layouttree

import (
	"encoding/json"
	"reflect"
	"testing"
)

func leaf(id string) *Node { return &Node{PaneID: id} }

func split(dir SplitDir, ratio float64, l, r *Node) *Node {
	return &Node{Split: &dir, Ratio: ratio, Left: l, Right: r}
}

func js(n *Node) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func mustEqual(t *testing.T, got, want *Node) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %s\nwant %s", js(got), js(want))
	}
}

func TestSplitLeaf_PutsTheNewPaneRightOrBelow(t *testing.T) {
	in := split(Horizontal, 0.3, leaf("a"), split(Vertical, 0.7, leaf("b"), leaf("c")))
	before := js(in)
	got, ok := SplitLeaf(in, "b", Horizontal, "n")
	if !ok {
		t.Fatal("target b not found")
	}
	mustEqual(t, got, split(Horizontal, 0.3, leaf("a"),
		split(Vertical, 0.7, split(Horizontal, 0.5, leaf("b"), leaf("n")), leaf("c"))))
	if js(in) != before {
		t.Fatal("input mutated")
	}
	if _, ok := SplitLeaf(in, "zzz", Vertical, "n"); ok {
		t.Error("an unknown target reported success")
	}
}

// AC-13: a replace keeps the old leaf's position, orientation and ratio.
func TestSubstitute_KeepsANestedNon5050Slot(t *testing.T) {
	in := split(Horizontal, 0.3, leaf("a"), split(Vertical, 0.7, leaf("b"), leaf("c")))
	got, ok := Substitute(in, "b", "n")
	if !ok {
		t.Fatal("b not found")
	}
	mustEqual(t, got, split(Horizontal, 0.3, leaf("a"), split(Vertical, 0.7, leaf("n"), leaf("c"))))
	if _, ok := Substitute(in, "zzz", "n"); ok {
		t.Error("an unknown id reported success")
	}
}

func TestPrune_PromotesTheSibling(t *testing.T) {
	in := split(Horizontal, 0.3, leaf("a"), split(Vertical, 0.7, leaf("b"), leaf("c")))
	got, changed := Prune(in, "b")
	if !changed {
		t.Fatal("b was in the tree")
	}
	mustEqual(t, got, split(Horizontal, 0.3, leaf("a"), leaf("c")))

	got, changed = Prune(leaf("a"), "a")
	if !changed || got != nil {
		t.Fatalf("pruning the only leaf = %s, %v; want nil, true", js(got), changed)
	}
	if _, changed := Prune(in, "zzz"); changed {
		t.Error("pruning an absent id reported a change")
	}
}

type placeVector struct {
	Name string `json:"name"`
	Tree *Node  `json:"tree"`
	Pane string `json:"pane"`
	Want *Node  `json:"want"`
}

func TestPlaceMoved_Vectors(t *testing.T) {
	for _, v := range loadVectors[placeVector](t, "testdata/place_moved_vectors.json") {
		t.Run(v.Name, func(t *testing.T) {
			before := js(v.Tree)
			mustEqual(t, PlaceMoved(v.Tree, v.Pane), v.Want)
			if js(v.Tree) != before {
				t.Fatal("input mutated")
			}
		})
	}
}

func TestSanitize_DropsRepeatedLeavesAndBadRatios(t *testing.T) {
	in := split(Horizontal, 1.5, leaf("a"), split(Vertical, 0.4, leaf("a"), leaf("b")))
	mustEqual(t, Sanitize(in), split(Horizontal, 0.5, leaf("a"), leaf("b")))
	ok := split(Horizontal, 0.3, leaf("a"), leaf("b"))
	if !Equal(Sanitize(ok), ok) {
		t.Error("a valid tree was changed")
	}
	if Sanitize(nil) != nil {
		t.Error("nil became a tree")
	}
}

func TestPaneIDs_TreeOrder(t *testing.T) {
	in := split(Horizontal, 0.5, split(Vertical, 0.5, leaf("a"), leaf("b")), leaf("c"))
	if got := PaneIDs(in); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("PaneIDs = %v", got)
	}
	if !Contains(in, "b") || Contains(in, "z") {
		t.Error("Contains is wrong")
	}
}
