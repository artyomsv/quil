package tui

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/artyomsv/quil/internal/config"
)

type layoutVector struct {
	Name           string          `json:"name"`
	Stored         *SerializedNode `json:"stored"`
	Panes          []string        `json:"panes"`
	TemplateLayout string          `json:"template_layout"`
	TemplateMain   string          `json:"template_main"`
	Want           *SerializedNode `json:"want"`
}

func loadLayoutVectors(t *testing.T) []layoutVector {
	t.Helper()
	b, err := os.ReadFile("../layouttree/testdata/layout_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vs []layoutVector
	if err := json.Unmarshal(b, &vs); err != nil {
		t.Fatal(err)
	}
	if len(vs) == 0 {
		t.Fatal("no vectors")
	}
	return vs
}

// The vectors must also be what the TUI itself builds on the same state, or
// the browser and the TUI would place panes differently. Drives the real
// applyWorkspaceState on a fresh model (no placeholders, no migration), for
// every vector whose input the TUI can receive.
func TestDisplayLayout_MatchesTheTUI(t *testing.T) {
	for _, v := range loadLayoutVectors(t) {
		if len(v.Panes) == 0 {
			continue // a tab with no panes never reaches layout placement
		}
		t.Run(v.Name, func(t *testing.T) {
			m := Model{
				cfg:            config.Default(),
				notifications:  NewNotificationCenter(30, 50),
				mcpHighlights:  make(map[string]bool),
				tabDragFromIdx: -1,
				sized:          true,
				width:          100,
				height:         40,
			}
			var raw json.RawMessage
			if v.Stored != nil {
				raw, _ = json.Marshal(v.Stored)
			}
			state := WorkspaceStateMsg{
				ActiveTab: "t1",
				Tabs: []TabInfo{{ID: "t1", Name: "T", Panes: v.Panes, Layout: raw,
					TemplateLayout: v.TemplateLayout, TemplateMain: v.TemplateMain}},
			}
			for _, id := range v.Panes {
				state.Panes = append(state.Panes, PaneInfo{ID: id, TabID: "t1"})
			}
			m.applyWorkspaceState(state, "")
			var tab *TabModel
			for _, p := range m.projects {
				for _, tb := range p.tabs {
					if tb.ID == "t1" {
						tab = tb
					}
				}
			}
			if tab == nil {
				t.Fatal("tab t1 not built")
			}
			got := SerializeLayout(tab.Root)
			if !reflect.DeepEqual(got, v.Want) {
				g, _ := json.Marshal(got)
				w, _ := json.Marshal(v.Want)
				t.Fatalf("TUI built %s\nvector says %s", g, w)
			}
		})
	}
}
