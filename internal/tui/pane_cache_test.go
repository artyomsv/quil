package tui

import "testing"

// TestPaneView_CachedUntilContentChanges: View() must not rebuild the frame
// when nothing changed — every Update triggers View for every visible pane.
func TestPaneView_CachedUntilContentChanges(t *testing.T) {
	p := NewPaneModel("pane-cache-test", 1024)
	defer p.Dispose()
	p.Width, p.Height = 40, 12

	first := p.View()
	renders := p.renderCount
	if second := p.View(); second != first {
		t.Error("identical state rendered differently")
	}
	if p.renderCount != renders {
		t.Errorf("clean View() recomputed (renderCount %d -> %d)", renders, p.renderCount)
	}

	p.AppendOutput([]byte("hello"))
	_ = p.View()
	if p.renderCount == renders {
		t.Error("View() after AppendOutput served stale cache")
	}

	renders = p.renderCount
	p.Active = true
	_ = p.View()
	if p.renderCount == renders {
		t.Error("View() after Active change served stale cache")
	}

	// Push enough lines through the VT (80x24) to create real scrollback so
	// ScrollUp actually moves the viewport (it clamps to ScrollbackLen).
	for i := 0; i < 40; i++ {
		p.AppendOutput([]byte("line\r\n"))
	}
	_ = p.View()
	renders = p.renderCount
	p.ScrollUp(3)
	_ = p.View()
	if p.renderCount == renders {
		t.Error("View() after scroll served stale cache")
	}

	// ScrollUp that clamps to a no-op (already at the top) must NOT
	// invalidate — scrollBack is unchanged.
	p.ScrollUp(1000000)
	_ = p.View()
	renders = p.renderCount
	p.ScrollUp(5)
	_ = p.View()
	if p.renderCount != renders {
		t.Error("clamped no-op scroll invalidated the cache")
	}
}

// TestPaneView_SelectionInvalidatesCache: the active selection lives on Model
// and is passed into the pane per frame — its VALUE is snapshotted into the
// render key, so moving the selection cursor must invalidate the cache.
func TestPaneView_SelectionInvalidatesCache(t *testing.T) {
	p := NewPaneModel("pane-sel-test", 1024)
	defer p.Dispose()
	p.Width, p.Height = 40, 12
	p.AppendOutput([]byte("hello selection world"))

	_ = p.View()
	renders := p.renderCount

	// Selection appears on this pane → re-render.
	sel := &Selection{
		PaneID: p.ID,
		Anchor: SelectionAnchor{Col: 0, Line: 0},
		Cursor: SelectionAnchor{Col: 4, Line: 0},
	}
	p.activeSel = sel
	_ = p.View()
	if p.renderCount == renders {
		t.Error("View() after selection start served stale cache")
	}

	// Selection cursor moves (mutated in place by Model) → re-render.
	renders = p.renderCount
	sel.Cursor.Col = 8
	_ = p.View()
	if p.renderCount == renders {
		t.Error("View() after selection extension served stale cache")
	}

	// Same selection, nothing changed → cached.
	renders = p.renderCount
	_ = p.View()
	if p.renderCount != renders {
		t.Error("View() with unchanged selection recomputed")
	}

	// Selection moves to ANOTHER pane: this pane's highlight disappears —
	// exactly one re-render, after which foreign-selection churn is invisible
	// (renderContent ignores selections whose PaneID doesn't match).
	sel.PaneID = "some-other-pane"
	_ = p.View()
	if p.renderCount == renders {
		t.Error("selection leaving the pane served stale cache (highlight not cleared)")
	}

	renders = p.renderCount
	sel.Cursor.Col = 2 // foreign selection mutates — renders identically here
	_ = p.View()
	if p.renderCount != renders {
		t.Error("foreign-pane selection mutation invalidated the cache")
	}

	// Selection cleared (was foreign, already rendered unselected) → cached.
	p.activeSel = nil
	_ = p.View()
	if p.renderCount != renders {
		t.Error("clearing a foreign selection invalidated the cache")
	}
}

// TestPaneView_EveryKeyFieldInvalidates pins that each remaining fingerprint
// field invalidates the cache — a future key refactor that drops one would
// otherwise regress silently as stale frames. Each case mutates exactly one
// visual input. The workFrame/spinnerFrame cases pre-set their gating flag so
// the frame index is actually rendered; the assertion is about key
// sensitivity, which holds either way since both fields are in the key.
func TestPaneView_EveryKeyFieldInvalidates(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(p *PaneModel)
	}{
		{"mcpHighlight", func(p *PaneModel) { p.mcpHighlight = true }},
		{"focusMode", func(p *PaneModel) { p.focusMode = true }},
		{"working", func(p *PaneModel) { p.working = true }},
		{"workFrame", func(p *PaneModel) { p.working = true; p.workFrame++ }},
		{"ghost", func(p *PaneModel) { p.ghost = true }},
		{"resuming", func(p *PaneModel) { p.resuming = true }},
		{"preparing", func(p *PaneModel) { p.preparing = true }},
		{"pending", func(p *PaneModel) { p.Pending = true }},
		{"sessionID", func(p *PaneModel) { p.resuming = true; p.SessionID = "deadbeef" }},
		{"historyLines", func(p *PaneModel) { p.resuming = true; p.HistoryLines = 7 }},
		{"paneType", func(p *PaneModel) { p.resuming = true; p.Type = "claude-code" }},
		{"liveOutputSeen", func(p *PaneModel) { p.resuming = true; p.liveOutputSeen = true }},
		{"spinnerFrame", func(p *PaneModel) { p.resuming = true; p.spinnerFrame++ }},
		{"name", func(p *PaneModel) { p.Name = "renamed" }},
		{"muted", func(p *PaneModel) { p.Muted = true }},
		{"cwd", func(p *PaneModel) { p.CWD = "/elsewhere" }},
		{"cursorVisible", func(p *PaneModel) { p.cursorVisible = false }},
		{"width", func(p *PaneModel) { p.Width++ }},
		{"height", func(p *PaneModel) { p.Height++ }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := NewPaneModel("pane-key-test", 1024)
			defer p.Dispose()
			p.Width, p.Height = 40, 12
			_ = p.View()
			before := p.renderCount
			tc.mutate(p)
			_ = p.View()
			if p.renderCount == before {
				t.Errorf("%s change served a stale cached frame", tc.name)
			}
		})
	}
}

// TestPaneView_ResetAndResizeInvalidate: VT-grid mutations that do not go
// through AppendOutput must also bump contentGen.
func TestPaneView_ResetAndResizeInvalidate(t *testing.T) {
	p := NewPaneModel("pane-gen-test", 1024)
	defer p.Dispose()
	p.Width, p.Height = 40, 12
	p.AppendOutput([]byte("content"))

	_ = p.View()
	renders := p.renderCount

	p.ResizeVT(60, 20)
	_ = p.View()
	if p.renderCount == renders {
		t.Error("View() after ResizeVT served stale cache")
	}

	// No-op resize (same dims) must NOT invalidate.
	renders = p.renderCount
	p.ResizeVT(60, 20)
	_ = p.View()
	if p.renderCount != renders {
		t.Error("no-op ResizeVT invalidated the cache")
	}

	p.ResetVT()
	_ = p.View()
	if p.renderCount == renders {
		t.Error("View() after ResetVT served stale cache")
	}
}
