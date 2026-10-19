package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestSandboxKindOf(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, image, backend, want string
	}{
		{"host pane", "", "", ""},
		{"docker pane", "quil-sandbox:latest", "", sandboxKindDocker},
		{"sbx pane", "", "sbx", sandboxKindSbx},
		// The backend decides: an sbx pane is never drawn as docker.
		{"sbx wins over an image", "quil-sandbox:latest", "sbx", sandboxKindSbx},
		{"unknown backend, no image", "", "other", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sandboxKindOf(tt.image, tt.backend); got != tt.want {
				t.Errorf("sandboxKindOf(%q, %q) = %q, want %q", tt.image, tt.backend, got, tt.want)
			}
		})
	}
}

// TestParseWorkspaceState_ReadsTheSandboxWireKeys pins the client half of the
// wire contract, with the keys spelled out as the daemon writes them.
func TestParseWorkspaceState_ReadsTheSandboxWireKeys(t *testing.T) {
	t.Parallel()
	got := parseWorkspaceState(stateFromMap(t, map[string]any{
		"panes": []any{
			map[string]any{"id": "p1", "tab_id": "t1", "sandbox_image": "quil-sandbox:latest"},
			map[string]any{"id": "p2", "tab_id": "t1", "sandbox_backend": "sbx"},
			map[string]any{"id": "p3", "tab_id": "t1"},
		},
	}))
	if len(got.Panes) != 3 {
		t.Fatalf("parsed %d panes, want 3", len(got.Panes))
	}
	for i, want := range []string{sandboxKindDocker, sandboxKindSbx, ""} {
		if got.Panes[i].SandboxKind != want {
			t.Errorf("pane %d: SandboxKind = %q, want %q", i, got.Panes[i].SandboxKind, want)
		}
	}
}

// TestSyncPaneMeta_CopiesTheSandboxKind: unconditional, like the other
// daemon-authoritative fields, so a cleared value reaches the pane too.
func TestSyncPaneMeta_CopiesTheSandboxKind(t *testing.T) {
	t.Parallel()
	pane := &PaneModel{ID: "p1"}
	syncPaneMeta(pane, &PaneInfo{ID: "p1", SandboxKind: sandboxKindSbx}, false, 0, false, false)
	if pane.SandboxKind != sandboxKindSbx {
		t.Fatalf("SandboxKind = %q, want %q", pane.SandboxKind, sandboxKindSbx)
	}
	syncPaneMeta(pane, &PaneInfo{ID: "p1"}, false, 0, false, false)
	if pane.SandboxKind != "" {
		t.Errorf("an absent kind did not clear the pane's: %q", pane.SandboxKind)
	}
}

func TestBuildTopBorder_ShowsTheSandboxMark(t *testing.T) {
	t.Parallel()
	c := lipgloss.Color("238")
	for _, kind := range []string{sandboxKindDocker, sandboxKindSbx} {
		g := sandboxGlyph(kind)
		for _, cwd := range []string{"/home/user/proj", "", strings.Repeat("/long", 30)} {
			border := buildTopBorderCut(40, cwd, "claude", c,
				false, false, false, false, 0, true, 0, false, false, g)
			plain := ansi.Strip(border)
			if !strings.Contains(plain, g) {
				t.Errorf("kind %q, cwd %q: border %q has no %q", kind, cwd, plain, g)
			}
			// The mark sits in the fixed segment, so a long CWD never
			// pushes the border past its width.
			if w := lipgloss.Width(border); w != 40 {
				t.Errorf("kind %q, cwd %q: border is %d cells, want 40", kind, cwd, w)
			}
		}
	}
	host := ansi.Strip(buildTopBorder(40, "/home/user/proj", "claude", c,
		false, false, false, false, 0, false, 0))
	if strings.ContainsAny(host, glyphSandboxDocker+glyphSandboxSbx) {
		t.Errorf("host pane border %q shows a sandbox mark", host)
	}
}

// TestBuildTopBorder_NeverWiderThanThePane sweeps every pane width from the
// layout minimum up, against every combination of the fixed segment (spinner,
// sandbox mark) and the labels that compete with it. A border wider than its
// pane pushes the sibling joined beside it out of its rect — the PR #265
// review case was width 20, a 14-rune name, CWD /tmp, working, sandboxed: 22.
func TestBuildTopBorder_NeverWiderThanThePane(t *testing.T) {
	t.Parallel()
	c := lipgloss.Color("238")
	names := []string{"", "sh", "bash", "abcdefghijklmn", strings.Repeat("n", 40)}
	cwds := []string{"", "/tmp", strings.Repeat("/long", 20)}
	for width := minPaneW; width <= 40; width++ {
		for _, name := range names {
			for _, cwd := range cwds {
				for _, working := range []bool{false, true} {
					for _, mark := range []string{"", glyphSandboxDocker} {
						for _, focus := range []bool{false, true} {
							border := buildTopBorderCut(width, cwd, name, c,
								false, false, false, focus, 0, working, 0, false, false, mark)
							if got := lipgloss.Width(border); got != width {
								t.Fatalf("width %d, name %q, cwd %q, working %v, mark %q, focus %v: border is %d cells",
									width, name, cwd, working, mark, focus, got)
							}
						}
					}
				}
			}
		}
	}
	// At the review's width the marks still win over the name.
	got := ansi.Strip(buildTopBorderCut(20, "/tmp", "abcdefghijklmn", c,
		false, false, false, false, 0, true, 0, false, false, glyphSandboxDocker))
	if !strings.Contains(got, glyphSandboxDocker) || !strings.Contains(got, workingGlyph(0)) {
		t.Errorf("narrow border %q dropped the spinner or the sandbox mark", got)
	}
}

// TestPaneView_DrawsTheSandboxMark drives the real render path, so a call
// site that stops passing the kind fails here.
func TestPaneView_DrawsTheSandboxMark(t *testing.T) {
	t.Parallel()
	pane := NewPaneModel("p1", 1024)
	defer pane.Dispose()
	pane.CWD = "/work/feat"
	pane.Width, pane.Height = 40, 10
	pane.SandboxKind = sandboxKindDocker
	if top := ansi.Strip(strings.SplitN(pane.View(), "\n", 2)[0]); !strings.Contains(top, glyphSandboxDocker) {
		t.Errorf("docker pane top border %q has no %q", top, glyphSandboxDocker)
	}
	// A kind change must not be served from the render cache.
	pane.SandboxKind = sandboxKindSbx
	if top := ansi.Strip(strings.SplitN(pane.View(), "\n", 2)[0]); !strings.Contains(top, glyphSandboxSbx) {
		t.Errorf("after a kind change the top border %q has no %q", top, glyphSandboxSbx)
	}
}

func TestPaneRow_ShowsTheSandboxMarkAndKeepsItsWidth(t *testing.T) {
	t.Parallel()
	for _, hl := range []rowHighlight{rowHighlightNone, rowHighlightPaneHover} {
		pane := &PaneModel{ID: "p1", Name: strings.Repeat("claude", 10), SandboxKind: sandboxKindSbx}
		pane.pinnedAttention = true
		pane.working = true
		row := paneRowHL(pane, false, 22, hl)
		if w := lipgloss.Width(row); w != 22 {
			t.Errorf("hl %v: row is %d cells, want 22", hl, w)
		}
		plain := ansi.Strip(row)
		// Reserved: a long name truncates, the mark survives.
		if !strings.Contains(plain, glyphSandboxSbx) || !strings.Contains(plain, glyphPinned) {
			t.Errorf("hl %v: row %q lost a reserved mark", hl, plain)
		}
	}
	host := ansi.Strip(paneRow(&PaneModel{ID: "p2", Name: "shell"}, false, 22))
	if strings.ContainsAny(host, glyphSandboxDocker+glyphSandboxSbx) {
		t.Errorf("host pane row %q shows a sandbox mark", host)
	}
}
