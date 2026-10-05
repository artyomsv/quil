package layouttree

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

type layoutVector struct {
	Name           string   `json:"name"`
	Stored         *Node    `json:"stored"`
	Panes          []string `json:"panes"`
	TemplateLayout string   `json:"template_layout"`
	TemplateMain   string   `json:"template_main"`
	Want           *Node    `json:"want"`
}

func loadVectors[T any](t *testing.T, path string) []T {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var vs []T
	if err := json.Unmarshal(b, &vs); err != nil {
		t.Fatal(err)
	}
	if len(vs) == 0 {
		t.Fatal("no vectors")
	}
	return vs
}

// The vectors are the contract the TUI (TestDisplayLayout_MatchesTheTUI) and
// the browser's TypeScript port (web/src/lib/layout.test.ts) are both held to.
func TestNormalize_Vectors(t *testing.T) {
	for _, v := range loadVectors[layoutVector](t, "testdata/layout_vectors.json") {
		t.Run(v.Name, func(t *testing.T) {
			var before []byte
			if v.Stored != nil {
				before, _ = json.Marshal(v.Stored)
			}
			got := Normalize(v.Stored, v.Panes, v.TemplateLayout, v.TemplateMain)
			if !reflect.DeepEqual(got, v.Want) {
				g, _ := json.Marshal(got)
				w, _ := json.Marshal(v.Want)
				t.Fatalf("got %s\nwant %s", g, w)
			}
			if v.Stored != nil {
				after, _ := json.Marshal(v.Stored)
				if string(after) != string(before) {
					t.Fatalf("stored tree was mutated: %s -> %s", before, after)
				}
			}
		})
	}
}

func TestParse_EmptyAndNullAreNoTree(t *testing.T) {
	for _, raw := range []string{"", "  ", "null", " null "} {
		n, err := Parse(json.RawMessage(raw))
		if n != nil || err != nil {
			t.Errorf("Parse(%q) = %v, %v; want nil, nil", raw, n, err)
		}
	}
	if _, err := Parse(json.RawMessage(`{"pane_id":`)); err == nil {
		t.Error("truncated JSON parsed without an error")
	}
}

func TestMarshal_NilIsEmpty(t *testing.T) {
	raw, err := Marshal(nil)
	if raw != nil || err != nil {
		t.Errorf("Marshal(nil) = %q, %v; want nil, nil", raw, err)
	}
}

func TestTemplate_EmptyAndOutOfRangeMain(t *testing.T) {
	if Template("grid", nil, 0) != nil {
		t.Error("an empty pane list built a tree")
	}
	// main out of range falls back to the first pane, as the TUI's templateLayout does.
	got := Template("main-left", []string{"x", "y"}, 9)
	h := Horizontal
	want := &Node{Split: &h, Ratio: 0.5, Left: &Node{PaneID: "x"}, Right: &Node{PaneID: "y"}}
	if !reflect.DeepEqual(got, want) {
		g, _ := json.Marshal(got)
		t.Errorf("got %s", g)
	}
}
