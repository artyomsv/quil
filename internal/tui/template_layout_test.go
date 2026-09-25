package tui

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

func templateShape(n *LayoutNode) string {
	if n == nil {
		return "nil"
	}
	if n.IsLeaf() {
		return n.Pane.ID
	}
	dir := "V"
	if n.Split == SplitHorizontal {
		dir = "H"
	}
	return dir + "(" + templateShape(n.Left) + "," + templateShape(n.Right) + ")"
}

func TestTemplateLayout_AllKeywordsAndPaneCounts_ExpectedShape(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	stacks := []string{"nil", "0", "V(0,1)", "V(0,V(1,2))", "V(0,V(1,V(2,3)))", "V(0,V(1,V(2,V(3,4))))", "V(0,V(1,V(2,V(3,V(4,5)))))", "V(0,V(1,V(2,V(3,V(4,V(5,6))))))", "V(0,V(1,V(2,V(3,V(4,V(5,V(6,7)))))))"}
	grids := []string{"nil", "0", "H(0,1)", "V(H(0,1),2)", "H(V(0,1),V(2,3))", "V(H(V(0,1),V(2,3)),4)", "H(V(0,V(1,2)),V(3,V(4,5)))", "V(H(V(0,V(1,2)),V(3,V(4,5))),6)", "H(V(0,V(1,V(2,3))),V(4,V(5,V(6,7))))"}
	for _, keyword := range []string{"rows", "columns", "main-left", "main-top", "grid", "", "unknown"} {
		for n := 1; n <= 8; n++ {
			for _, main := range []int{0, n - 1, -1, n + 1} {
				t.Run(fmt.Sprintf("%s/%d/main%d", keyword, n, main), func(t *testing.T) {
					panes := make([]*PaneModel, n)
					for i := range panes {
						panes[i] = &PaneModel{ID: strconv.Itoa(i)}
					}
					want := stacks[n]
					anchor := main
					if anchor < 0 || anchor >= n {
						anchor = 0
					}
					switch keyword {
					case "columns":
						want = strings.ReplaceAll(want, "V", "H")
					case "grid":
						want = grids[n]
					case "main-left", "main-top":
						if n > 1 {
							var pairs []string
							index := 0
							for i := 0; i < n; i++ {
								if i != anchor {
									pairs = append(pairs, strconv.Itoa(index), strconv.Itoa(i))
									index++
								}
							}
							rest := strings.NewReplacer(pairs...).Replace(stacks[n-1])
							dir := "H"
							if keyword == "main-top" {
								dir = "V"
								rest = strings.ReplaceAll(rest, "V", "H")
							}
							want = fmt.Sprintf("%s(%d,%s)", dir, anchor, rest)
						}
					}
					root := templateLayout(keyword, panes, main)
					if got := templateShape(root); got != want {
						t.Fatalf("shape=%s want=%s", got, want)
					}
					if len(root.PaneIDs()) != n {
						t.Fatal("lost or duplicated panes")
					}
					for i, p := range panes {
						if p.ID != strconv.Itoa(i) {
							t.Fatal("mutated input order")
						}
					}
				})
			}
		}
	}
	if templateLayout("grid", nil, 0) != nil {
		t.Fatal("empty layout should be nil")
	}
}

func templateWorkspace(ids []string, main string) WorkspaceStateMsg {
	state := WorkspaceStateMsg{ActiveProject: "proj-local", ActiveTab: "tab-template",
		Projects: []ProjectInfo{{ID: "proj-local", Name: "Local", TabIDs: []string{"tab-template"}}},
		Tabs:     []TabInfo{{ID: "tab-template", Name: "Template", Panes: ids, TemplateLayout: "main-left", TemplateMain: main}}}
	for _, id := range ids {
		state.Panes = append(state.Panes, PaneInfo{ID: id, TabID: "tab-template", Type: "terminal"})
	}
	return state
}

func TestTemplateWorkspace_PreparingSwapAndCompletion_BuildsAndReportsOnlyCompletedTree(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	m := paletteModelWithProjects(t)
	// Count this workspace's sends only; sendAllLayouts also legitimately
	// saves unrelated remote tabs in the shared palette fixture.
	m.projects = m.projects[:1]
	recorder := &echoRecorder{}
	m.client = recorder
	update := func(state WorkspaceStateMsg) {
		next, cmd := m.Update(state)
		m = next.(Model)
		runCmd(cmd)
	}
	preparing := templateWorkspace([]string{"placeholder"}, "placeholder")
	preparing.Panes[0].PreparingWorktree = "feat/template"
	update(preparing)
	runCmd(m.sendAllLayouts()) // Even an unrelated resize/action must not persist preparation.
	if layouts, _ := sentCounts(t, recorder); layouts != 0 {
		t.Fatal("saved preparing placeholder")
	}
	update(templateWorkspace([]string{"first"}, "placeholder"))
	if layouts, _ := sentCounts(t, recorder); layouts != 0 {
		t.Fatal("saved intermediate swap")
	}
	incomplete := templateWorkspace([]string{"first", "second", "main"}, "main")
	incomplete.Panes = incomplete.Panes[:2]
	update(incomplete)
	if layouts, _ := sentCounts(t, recorder); layouts != 0 {
		t.Fatal("saved before every declared pane existed")
	}
	complete := templateWorkspace([]string{"first", "second", "main"}, "main")
	update(complete)
	tab := m.tabByID("tab-template")
	root := tab.Root
	if got := templateShape(root); got != "H(main,V(first,second))" {
		t.Fatal(got)
	}
	if layouts, _ := sentCounts(t, recorder); layouts != 1 {
		t.Fatalf("layout sends=%d want=1", layouts)
	}
	var saved ipc.UpdateLayoutPayload
	for _, msg := range recorder.sent {
		if msg.Type == ipc.MsgUpdateLayout {
			if err := msg.DecodePayload(&saved); err != nil {
				t.Fatal(err)
			}
		}
	}
	if saved.TabID != tab.ID || !layoutAgrees(saved.Layout, root) {
		t.Fatal("reported wrong tree", saved)
	}
	complete.Tabs[0].Layout = saved.Layout
	update(complete)
	if tab.Root != root {
		t.Fatal("rebuilt the tree on the second broadcast")
	}
	if layouts, _ := sentCounts(t, recorder); layouts != 1 {
		t.Fatal("echoed the saved layout")
	}
	// User changes survive subsequent completed frames too.
	root.Ratio = 0.7
	update(complete)
	if tab.Root != root || root.Ratio != 0.7 {
		t.Fatal("template reset a user layout")
	}
	for _, p := range m.allTabs() {
		for _, pane := range p.Leaves() {
			pane.Dispose()
		}
	}
}

func TestTemplateWorkspace_StoredLayout_WinsOverKeyword(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	m := paletteModelWithProjects(t)
	state := templateWorkspace([]string{"a", "b"}, "b")
	want := templateLayout("rows", []*PaneModel{{ID: "a"}, {ID: "b"}}, 0)
	data, err := MarshalLayout(want)
	if err != nil {
		t.Fatal(err)
	}
	state.Tabs[0].Layout = data
	m = templateUpdate(t, m, state)
	tab := m.tabByID("tab-template")
	if templateShape(tab.Root) != "V(a,b)" {
		t.Fatal("ignored saved layout")
	}
	state.Tabs[0].Layout = nil // A stale broadcast must not revive the spent hint.
	m = templateUpdate(t, m, state)
	if templateShape(tab.Root) != "V(a,b)" {
		t.Fatal("revived template hint")
	}
	for _, tab := range m.allTabs() {
		for _, pane := range tab.Leaves() {
			pane.Dispose()
		}
	}
}

func TestParseWorkspaceState_TemplateFields_PreservesAnchorAndKeyword(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	state := parseWorkspaceState(map[string]any{"tabs": []any{map[string]any{"id": "t", "template_layout": "main-top", "template_main": "p"}}})
	if len(state.Tabs) != 1 || state.Tabs[0].TemplateLayout != "main-top" || state.Tabs[0].TemplateMain != "p" {
		t.Fatal(state.Tabs)
	}
}
