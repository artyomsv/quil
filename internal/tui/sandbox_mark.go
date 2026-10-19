package tui

import (
	"charm.land/lipgloss/v2"

	"github.com/artyomsv/quil/internal/config"
)

// The two sandbox kinds a pane can run in. Values are TUI-local: the wire
// carries sandbox_image (docker) and sandbox_backend ("sbx"), and
// sandboxKindOf is the one place that reads them.
const (
	sandboxKindDocker = "docker"
	sandboxKindSbx    = "sbx"
)

// Sandbox glyphs, drawn on the pane's top border and its sidebar row. Bound by
// the sidebar's glyph rule (TestSidebarGlyphs_OneCellAndNotEmojiCapable): one
// rune, one cell, never emoji-capable, because both places budget it as one
// cell and a font drawing it wide would paint over the next character.
const (
	glyphSandboxDocker = "▣" // U+25A3: a Docker container
	glyphSandboxSbx    = "⧉" // U+29C9: a Docker Sandboxes (sbx) microVM
)

// sidebarSandboxStyle colours the sidebar's sandbox mark. Blue 75 is used by
// no state glyph, so the mark reads as a fact about the pane, not a state.
var sidebarSandboxStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("75"))

// sandboxKindOf maps the daemon's sandbox fields to a kind. The backend is
// checked first: an sbx pane has no image, and a docker pane has no backend.
func sandboxKindOf(image, backend string) string {
	switch {
	case backend == config.SandboxBackendSbx:
		return sandboxKindSbx
	case image != "":
		return sandboxKindDocker
	}
	return ""
}

// sandboxGlyph is the glyph for a kind, "" for a host pane.
func sandboxGlyph(kind string) string {
	switch kind {
	case sandboxKindDocker:
		return glyphSandboxDocker
	case sandboxKindSbx:
		return glyphSandboxSbx
	}
	return ""
}
