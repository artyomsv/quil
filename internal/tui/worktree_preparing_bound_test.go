package tui

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The branch name arrives from the daemon with no bound at either hop, and
// unlike every other remote string this one is re-rendered TEN TIMES A SECOND
// for as long as the checkout runs.
//
// renderPreparingWorktree sanitizes and elides the FULL string on every frame —
// four O(len) passes, one of which segments the whole string into graphemes —
// and spinnerFrame is part of the render key, so the cache cannot absorb any of
// it. Under --remote the daemon authors this value and a frame may carry
// megabytes, so an unbounded one is hundreds of MB/s of transient garbage on
// the single Update goroutine, indefinitely, with nothing that ends it.
//
// Bounded at INGEST rather than at render: that is the one place it cannot be
// forgotten by a future second render path, and it is the same shape as the
// project form's formMsgNameCap. gitworktree.maxBranchLen is 255, so no honest
// value is touched.
func TestParseWorkspaceState_BoundsThePreparingBranch(t *testing.T) {
	huge := strings.Repeat("x", 1<<20)
	state := map[string]any{
		"tabs": []any{map[string]any{"id": "tab-1", "name": "t", "panes": []any{"pane-1"}}},
		"panes": []any{map[string]any{
			"id":                 "pane-1",
			"tab_id":             "tab-1",
			"preparing_worktree": huge,
		}},
	}

	ws := parseWorkspaceState(state)
	var got string
	for _, p := range ws.Panes {
		if p.ID == "pane-1" {
			got = p.PreparingWorktree
		}
	}
	if got == "" {
		t.Fatal("preparing_worktree was dropped entirely")
	}
	if len(got) >= len(huge) {
		t.Errorf("PreparingWorktree kept %d bytes — an unbounded value is re-sanitized 10x/second forever", len(got))
	}
	if len(got) > preparingBranchCap {
		t.Errorf("PreparingWorktree = %d bytes, want at most preparingBranchCap (%d)", len(got), preparingBranchCap)
	}
}

// An ordinary branch name must survive byte-identically — the cap is a ceiling
// on a hostile value, not a reformatting of an honest one.
func TestParseWorkspaceState_KeepsAnOrdinaryBranchIntact(t *testing.T) {
	const branch = "fix/nationality-filter"
	state := map[string]any{
		"tabs": []any{map[string]any{"id": "tab-1", "name": "t", "panes": []any{"pane-1"}}},
		"panes": []any{map[string]any{
			"id":                 "pane-1",
			"tab_id":             "tab-1",
			"preparing_worktree": branch,
		}},
	}

	ws := parseWorkspaceState(state)
	for _, p := range ws.Panes {
		if p.ID == "pane-1" && p.PreparingWorktree != branch {
			t.Errorf("PreparingWorktree = %q, want %q untouched", p.PreparingWorktree, branch)
		}
	}
}

// The cap must cut on a RUNE boundary. A branch name may carry non-ASCII, and
// lopping a byte off a multi-byte rune leaves invalid UTF-8 for lipgloss to
// measure — the same reason the name field's backspace is rune-safe.
func TestParseWorkspaceState_CutsThePreparingBranchOnARuneBoundary(t *testing.T) {
	state := map[string]any{
		"tabs": []any{map[string]any{"id": "tab-1", "name": "t", "panes": []any{"pane-1"}}},
		"panes": []any{map[string]any{
			"id":                 "pane-1",
			"tab_id":             "tab-1",
			"preparing_worktree": strings.Repeat("é", preparingBranchCap),
		}},
	}

	ws := parseWorkspaceState(state)
	for _, p := range ws.Panes {
		if p.ID == "pane-1" && !utf8.ValidString(p.PreparingWorktree) {
			t.Error("the cap cut through a multi-byte rune, leaving invalid UTF-8")
		}
	}
}
