package tui

import (
	"errors"
	"image/color"
	"io"
	"log"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	uv "github.com/charmbracelet/ultraviolet"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"

	"github.com/artyomsv/quil/internal/ringbuf"
)

// spinnerFrames are braille characters cycled for the resuming indicator.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

type PaneModel struct {
	ID             string
	Type           string // plugin type ("terminal", "claude-code", etc.)
	Name           string // user-given name (empty if not set)
	CWD            string // current working directory from daemon
	Muted          bool   // notification mute (daemon-authoritative; mirrored here for border rendering)
	Eager          bool   // eager-restore flag (daemon-authoritative; mirrored for the tab marker)
	vt             *vt.SafeEmulator
	vtDrain        *vtDrain // drain goroutine tracker for p.vt (see closeVT)
	Width          int
	Height         int
	Active         bool
	scrollBack     int
	rawBuf         *ringbuf.RingBuffer // raw PTY bytes for resize replay
	cursorVisible  bool                // tracks shell's DECTCEM state
	ghost          bool                // true while showing restored content
	resuming       bool                // true while waiting for first live output after restore
	preparing      bool                // true for newly created panes (not restored)
	resumeStart    time.Time           // when resuming/preparing started (minimum display duration)
	spinnerFrame   int                 // current frame index in spinnerFrames
	activeSel      *Selection          // set by Model before View() for selection rendering
	focusMode      bool                // set by Model before View() when in focus mode
	mcpHighlight   bool                // set by Model before View() when MCP is interacting
	liveOutputSeen bool                // first live (non-ghost) output received — settle repaints scheduled
	working        bool                // true while a claude/opencode turn is in progress (hook-driven)
	unseen         bool                // work finished/parked while this pane was not focused; cleared on focus
	workFrame      int                 // shared spinner frame index, mirrored here for top-border render

	// Render cache: View() output is reused while renderKey() is unchanged.
	// contentGen covers VT-grid/raw-buffer mutations (the grid itself has no
	// public change counter; PaneModel mediates all writes via AppendOutput/
	// ResetVT/ResizeVT). Selection is snapshotted by VALUE into the key (it
	// lives on Model and is mutated there), so no selection generation is
	// needed. renderCount is test observability — incremented on real renders
	// only. invalidateRenderCache() is the explicit escape hatch (redraw key).
	contentGen  uint64
	cachedKey   paneRenderKey
	cachedView  string
	hasCache    bool
	renderCount int
}

// paneRenderKey is the comparable fingerprint of everything View() reads,
// directly or transitively (renderContent, renderScrollback,
// renderWithSelection, insertCursor, buildTopBorder). Adding a new visual
// input to any of those REQUIRES adding it here — a missing field means
// stale frames. The redraw key (alt+shift+l) clears the cache as the
// user-facing escape hatch.
//
// Notes on coverage:
//   - contentGen stands in for everything derived from the VT emulator:
//     screen cells, scrollback cells, ScrollbackLen, CursorPosition, and the
//     emulator's own width/height (only PaneModel methods mutate the VT).
//   - cursorVisible and cwd are written by VT callbacks during vt.Write
//     (same Update goroutine); they are plain fields here.
//   - selActive/sel snapshot the Model-owned *Selection by value, already
//     resolved against this pane's ID — a selection on another pane renders
//     identically to no selection, so it is normalized to the zero value.
//   - spinnerFrame is only advanced while resuming/preparing, workFrame only
//     while working (guarded at the call sites in model.go/workstate.go), so
//     including them raw does not churn the key for idle panes.
type paneRenderKey struct {
	contentGen                     uint64
	width, height, scrollBack      int
	active, cursorVisible          bool
	ghost, resuming, preparing     bool
	mcpHighlight, muted, focusMode bool
	working                        bool
	unseen                         bool
	spinnerFrame, workFrame        int
	name, cwd                      string
	selActive                      bool
	sel                            Selection
}

// renderKey computes the current fingerprint of every View() input.
func (p *PaneModel) renderKey() paneRenderKey {
	k := paneRenderKey{
		contentGen:    p.contentGen,
		width:         p.Width,
		height:        p.Height,
		scrollBack:    p.scrollBack,
		active:        p.Active,
		cursorVisible: p.cursorVisible,
		ghost:         p.ghost,
		resuming:      p.resuming,
		preparing:     p.preparing,
		mcpHighlight:  p.mcpHighlight,
		muted:         p.Muted,
		focusMode:     p.focusMode,
		working:       p.working,
		unseen:        p.unseen,
		spinnerFrame:  p.spinnerFrame,
		workFrame:     p.workFrame,
		name:          p.Name,
		cwd:           p.CWD,
	}
	// renderContent only honors a selection whose PaneID matches this pane;
	// foreign or absent selections render identically, so both normalize to
	// the zero value (no spurious invalidation while another pane is being
	// selected).
	if p.activeSel != nil && p.activeSel.PaneID == p.ID {
		k.selActive = true
		k.sel = *p.activeSel
	}
	return k
}

// invalidateRenderCache drops the cached frame so the next View() rebuilds
// it unconditionally. Wired to the redraw keybinding as the user-facing
// escape hatch for a hypothetical stale-cache bug. Also releases the cached
// string so the escape hatch doubles as a memory release.
func (p *PaneModel) invalidateRenderCache() {
	p.hasCache = false
	p.cachedView = ""
}

// vtDrain tracks the drain goroutine of one emulator so teardown can be
// sequenced: upstream x/vt's Emulator.Close races Emulator.Read on an
// unsynchronized closed flag (SafeEmulator wraps neither), so Close may only
// run after the drain goroutine has exited.
type vtDrain struct {
	stop atomic.Bool
	done chan struct{}
}

// newVTEmulator builds a SafeEmulator for this pane and starts a goroutine
// that drains the emulator's response pipe. The caller installs the returned
// pair into p.vt / p.vtDrain (newVTEmulator deliberately does NOT write p's
// fields itself — installVT must close the OLD emulator via the OLD vtDrain
// before the new pair is assigned).
//
// The charmbracelet/x/vt emulator answers queries like CSI c (Primary Device
// Attributes, DA1), DSR (Device Status Report), and OSC 10/11/12 by writing
// the response to an internal io.Pipe. That pipe blocks writers until a
// reader drains it. Without a drain, any TUI app that queries terminal
// capabilities — Claude Code 2.1.110 sends DA1 on startup — deadlocks the
// entire TUI inside vt.Write(). The drain goroutine terminates via the
// stop-flag protocol in closeVT(); only after it exits is Emulator.Close()
// safe to call.
func (p *PaneModel) newVTEmulator(w, h int) (*vt.SafeEmulator, *vtDrain) {
	em := vt.NewSafeEmulator(w, h)
	em.SetScrollbackSize(10000)
	em.SetCallbacks(vt.Callbacks{
		CursorVisibility: func(visible bool) {
			p.cursorVisible = visible
		},
		WorkingDirectory: func(dir string) {
			p.CWD = parseOSC7Path(dir)
		},
	})
	d := &vtDrain{done: make(chan struct{})}
	go drainVTResponses(em, d)
	return em, d
}

// drainVTResponses continuously reads and discards the emulator's query
// responses. After each successful read it checks the stop flag so closeVT
// can retire it without calling Emulator.Close while a Read is in flight.
// Exits cleanly on EOF/closed-pipe (emulator closed); any other read error
// leaves a breadcrumb so a future library regression that re-introduces a
// deadlock isn't silent.
func drainVTResponses(em *vt.SafeEmulator, d *vtDrain) {
	defer close(d.done)
	buf := make([]byte, 256)
	for {
		if _, err := em.Read(buf); err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
				log.Printf("pane: VT drain exited unexpectedly: %v", err)
			}
			return
		}
		if d.stop.Load() {
			return
		}
	}
}

// closeVT stops the drain goroutine, then closes the emulator. The DA1 query
// makes the emulator emit a response into its pipe, waking the drain's
// blocked Read so it can observe the stop flag; only after it exits is
// Close safe (see vtDrain). The 1 s fallback guards a hypothetical
// non-responding emulator — closing then re-admits the benign upstream race
// rather than hanging the Update loop.
func (p *PaneModel) closeVT() {
	if p.vt == nil {
		return
	}
	if p.vtDrain != nil {
		p.vtDrain.stop.Store(true)
		_, _ = p.vt.Write([]byte("\x1b[c")) // DA1 — provokes a response
		select {
		case <-p.vtDrain.done:
		case <-time.After(time.Second):
			log.Printf("pane %s: VT drain did not stop within 1s — closing anyway", p.ID)
		}
	}
	_ = p.vt.Close()
}

// Dispose closes the VT emulator, stopping its drainVTResponses goroutine
// and releasing the scrollback grid. Must be called for every PaneModel
// removed from the layout tree — without it each closed pane leaks a parked
// goroutine plus up to a 10,000-line scrollback. The PaneModel must not be
// rendered or written to afterwards.
func (p *PaneModel) Dispose() {
	p.closeVT()
}

// installVT closes the current emulator (stopping its drain goroutine via
// the OLD vtDrain) and installs the new pair.
func (p *PaneModel) installVT(em *vt.SafeEmulator, d *vtDrain) {
	p.closeVT()
	p.vt, p.vtDrain = em, d
}

func NewPaneModel(id string, bufSize int) *PaneModel {
	p := &PaneModel{
		ID:            id,
		Name:          "",
		rawBuf:        ringbuf.NewRingBuffer(bufSize),
		cursorVisible: true, // visible by default (matches terminal default)
	}
	p.vt, p.vtDrain = p.newVTEmulator(80, 24)
	return p
}

func (p *PaneModel) AppendOutput(data []byte) {
	p.rawBuf.Write(data)
	p.vt.Write(data)
	p.contentGen++
}

// ResetVT creates a fresh VT emulator at the current dimensions, clearing
// ghost buffer state so live output starts with a clean cursor position.
func (p *PaneModel) ResetVT() {
	w, h := p.vt.Width(), p.vt.Height()
	p.installVT(p.newVTEmulator(w, h))
	p.rawBuf.Reset()
	p.cursorVisible = true
	p.contentGen++
}

func (p *PaneModel) ResizeVT(cols, rows int) {
	if cols <= 0 || rows <= 0 || (cols == p.vt.Width() && rows == p.vt.Height()) {
		return
	}
	// Resize the emulator in place instead of rebuilding it from the raw PTY
	// ring buffer. Historical bytes from TUI apps (Claude Code, vim, htop,
	// fzf) contain CUP / scroll-region sequences laid out for the previous
	// width; replaying them into a freshly-sized emulator stamps narrow-
	// column ghost rows into the new screen. The x/vt library's Resize
	// preserves the current screen state, and the PTY child will redraw via
	// SIGWINCH (triggered separately by MsgResizePane) into the new size.
	p.vt.Resize(cols, rows)
	p.contentGen++
}

func (p *PaneModel) ScrollUp(lines int) {
	p.scrollBack += lines
	if max := p.vt.ScrollbackLen(); p.scrollBack > max {
		p.scrollBack = max
	}
}

func (p *PaneModel) ScrollDown(lines int) {
	p.scrollBack -= lines
	if p.scrollBack < 0 {
		p.scrollBack = 0
	}
}

func (p *PaneModel) ResetScroll() {
	p.scrollBack = 0
}

// ScrollToRelY positions the scrollback so that the scrollbar thumb's TOP
// row lands at relY (relative to the content area, 0..innerH-1). Inverse
// of the thumb-position formula in renderScrollback — a click at row R
// puts the thumb's top at R, matching standard GUI scrollbar UX.
//
// CONTRACT (must stay in sync with renderScrollback):
//
//	renderScrollback:  thumbSize = max(1, h*h/totalLines)
//	                   thumbPos  = viewStart * (h - thumbSize) / scrollRange
//	                              where scrollRange = totalLines - h = sbLen
//	this fn (inverse): viewStart = relY * sbLen / (innerH - thumbSize)
//
// Drift between the two is a silent UX bug. The integer math is safe on
// every supported quil platform (Go int is 64-bit on amd64 and arm64);
// even a million-line scrollback with a thousand-row pane multiplies to
// well under 2^63.
//
// Out-of-range relY clamps to the valid scroll extent. Returns silently
// (no-op) when there's no scrollback to scroll into or the visible area
// is large enough to hold every line (no scrollable range).
func (p *PaneModel) ScrollToRelY(relY, innerH int) {
	sbLen := p.vt.ScrollbackLen()
	if sbLen <= 0 || innerH <= 0 {
		return
	}
	totalLines := sbLen + innerH
	thumbSize := innerH * innerH / totalLines
	if thumbSize < 1 {
		thumbSize = 1
	}
	maxThumbPos := innerH - thumbSize
	if maxThumbPos <= 0 {
		return
	}
	if relY < 0 {
		relY = 0
	}
	if relY > maxThumbPos {
		relY = maxThumbPos
	}
	viewStart := relY * sbLen / maxThumbPos
	p.scrollBack = sbLen - viewStart
}

func (p *PaneModel) View() string {
	key := p.renderKey()
	if p.hasCache && key == p.cachedKey {
		return p.cachedView
	}
	p.renderCount++

	borderColor := lipgloss.Color("238")
	if p.unseen {
		borderColor = lipgloss.Color("28") // green — finished/parked, awaiting focus
	}
	if p.Active {
		borderColor = lipgloss.Color("57")
	}
	if p.ghost || p.resuming || p.preparing {
		borderColor = lipgloss.Color("95") // muted purple — distinct but not jarring
	}
	if p.mcpHighlight {
		borderColor = lipgloss.Color("208") // orange — MCP interaction
	}

	innerW := p.Width - 2
	innerH := p.Height - 2
	if innerW < 1 {
		innerW = 1
	}
	if innerH < 1 {
		innerH = 1
	}

	content := p.renderContent(p.activeSel)

	// Render content with left, right, bottom borders (no top).
	// Lipgloss v2: Width/Height include borders in the budget (v1 was additive).
	// +2 width for left+right borders, +1 height for bottom border (top removed).
	bodyStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderTop(false).
		BorderForeground(borderColor).
		Width(innerW + 2).
		Height(innerH + 1)

	body := bodyStyle.Render(content)

	// Manual top border: CWD on the left, pane name on the right.
	// Muted panes prefix the right label so it's visible at a glance — the
	// border colour stays the same (no risk of confusion with ghost / mcp /
	// active states, each of which already owns a colour slot).
	rightLabel := p.Name
	if p.Muted {
		if rightLabel == "" {
			rightLabel = "[muted]"
		} else {
			rightLabel = "[muted] " + rightLabel
		}
	}
	topLine := buildTopBorder(p.Width, p.CWD, rightLabel, borderColor, p.ghost, p.resuming, p.preparing, p.focusMode, p.spinnerFrame, p.working, p.workFrame)

	out := topLine + "\n" + body
	p.cachedKey, p.cachedView, p.hasCache = key, out, true
	return out
}

func buildTopBorder(width int, cwd, name string, color color.Color, ghost, resuming, preparing, focus bool, spinnerFrame int, working bool, workFrame int) string {
	if ghost {
		if name == "" {
			name = "restored"
		} else {
			name = name + " · restored"
		}
	}

	// Spinner overrides the right label temporarily
	if resuming || preparing {
		frame := spinnerFrames[spinnerFrame%len(spinnerFrames)]
		label := "resuming..."
		if preparing {
			label = "preparing..."
		}
		name = frame + " " + label
	}

	style := lipgloss.NewStyle().Foreground(color)
	b := lipgloss.RoundedBorder()
	innerW := width - 2
	if innerW < 1 {
		return style.Render(b.TopLeft + b.TopRight)
	}

	// Right label: pane name or spinner (only if it fits with padding).
	rightLabel := ""
	rightLen := 0
	if name != "" && len([]rune(name))+4 <= innerW {
		rightLabel = " " + name + " "
		rightLen = len([]rune(rightLabel))
	}

	// Optional working spinner — a fixed leading segment drawn before the CWD.
	// Reserved width is excluded from the CWD truncation budget so the spinner
	// itself is never cut off (the CWD truncates from its left with "…tail").
	spin := ""
	spinLen := 0
	if working {
		spin = " " + spinnerFrames[workFrame%len(spinnerFrames)]
		spinLen = 2 // leading space + single-width braille glyph
	}

	// Left label: CWD, truncated with ellipsis if needed.
	leftLabel := ""
	leftLen := 0
	if cwd != "" {
		available := innerW - rightLen - 1 - spinLen // reserve 1 dash + spinner
		cwdLabel := " " + cwd + " "
		cwdLabelLen := len([]rune(cwdLabel))

		if available < 0 {
			available = 0
		}
		if cwdLabelLen <= available {
			leftLabel = cwdLabel
			leftLen = cwdLabelLen
		} else if available >= 6 {
			// Truncate CWD from the left: " …tail "
			maxCwd := available - 4 // 4 = len(" …") + len(" ")
			cwdRunes := []rune(cwd)
			leftLabel = " …" + string(cwdRunes[len(cwdRunes)-maxCwd:]) + " "
			leftLen = len([]rune(leftLabel))
		}
	} else if working {
		// No CWD but working: still show the spinner with a trailing space.
		leftLabel = " "
		leftLen = 1
	}

	// Prepend the spinner segment (never truncated).
	leftLabel = spin + leftLabel
	leftLen += spinLen

	dashes := innerW - leftLen - rightLen
	if dashes < 0 {
		dashes = 0
	}

	// Focus mode: center "* FOCUS *" relative to the full border width
	if focus {
		focusLabel := "* FOCUS *"
		focusLen := len([]rune(focusLabel))
		if dashes >= focusLen+2 {
			// Center position relative to full innerW, then subtract left/right label offsets
			centerPos := (innerW - focusLen) / 2
			leftDash := centerPos - leftLen
			if leftDash < 1 {
				leftDash = 1
			}
			rightDash := dashes - focusLen - leftDash
			if rightDash < 0 {
				rightDash = 0
			}
			return style.Render(b.TopLeft + leftLabel +
				strings.Repeat(b.Top, leftDash) + focusLabel + strings.Repeat(b.Top, rightDash) +
				rightLabel + b.TopRight)
		}
	}

	return style.Render(b.TopLeft + leftLabel + strings.Repeat(b.Top, dashes) + rightLabel + b.TopRight)
}

// parseOSC7Path extracts a filesystem path from an OSC 7 URI (file://host/path).
func parseOSC7Path(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "file" {
		if raw != "" {
			return raw // treat as plain path
		}
		return ""
	}
	path := u.Path
	// Windows: url.Parse("file:///C:/foo") gives Path="/C:/foo"; strip leading /.
	if len(path) >= 3 && path[0] == '/' && path[2] == ':' {
		path = path[1:]
	}
	return path
}

func (p *PaneModel) renderContent(sel *Selection) string {
	// If selection is active on this pane, use cell-by-cell rendering
	if sel != nil && sel.PaneID == p.ID {
		return p.renderWithSelection(sel)
	}

	if p.scrollBack == 0 {
		// Live view — use Render() for full color support
		content := p.vt.Render()
		// Software reverse-video caret at the VT cursor for every pane
		// type. Interactive apps (claude-code, opencode) position the VT
		// cursor at their input caret exactly like shells do. A real
		// hardware cursor (tea.View.Cursor) was tried and reverted:
		// repositioning it every frame desynced Bubble Tea's diff writer
		// on Windows — the first typed character after a fresh input line
		// landed one cell off ("Test" → "T est").
		if p.Active && p.cursorVisible {
			content = p.insertCursor(content)
		}
		return content
	}

	// Scrollback view — render from scrollback + screen cells
	return p.renderScrollback()
}

// renderWithSelection renders content cell-by-cell with selection highlighting.
func (p *PaneModel) renderWithSelection(sel *Selection) string {
	w := p.vt.Width()
	h := p.vt.Height()
	sbLen := p.vt.ScrollbackLen()

	viewStart := sbLen - p.scrollBack

	lines := make([]string, h)
	for i := 0; i < h; i++ {
		absLine := viewStart + i

		var getCell func(x int) *uv.Cell
		if absLine < 0 {
			getCell = func(x int) *uv.Cell { return nil }
		} else if absLine < sbLen {
			srcLine := absLine
			getCell = func(x int) *uv.Cell {
				return p.vt.ScrollbackCellAt(x, srcLine)
			}
		} else {
			screenLine := absLine - sbLen
			getCell = func(x int) *uv.Cell {
				return p.vt.CellAt(x, screenLine)
			}
		}

		selStart, selEnd := sel.ColRange(absLine, w)
		lines[i] = p.styledCellLineWithSelection(getCell, w, selStart, selEnd)
	}

	return strings.Join(lines, "\n")
}

func (p *PaneModel) renderScrollback() string {
	w := p.vt.Width()
	h := p.vt.Height()
	sbLen := p.vt.ScrollbackLen()

	// viewStart is the first line to show (in combined scrollback+screen space)
	viewStart := sbLen - p.scrollBack

	lines := make([]string, h)
	for i := 0; i < h; i++ {
		srcLine := viewStart + i

		if srcLine < 0 {
			lines[i] = ""
		} else if srcLine < sbLen {
			lines[i] = p.styledCellLine(func(x int) *uv.Cell {
				return p.vt.ScrollbackCellAt(x, srcLine)
			}, w)
		} else {
			screenLine := srcLine - sbLen
			lines[i] = p.styledCellLine(func(x int) *uv.Cell {
				return p.vt.CellAt(x, screenLine)
			}, w)
		}
	}

	// Add scrollbar on the right side
	totalLines := sbLen + h
	thumbSize := max(1, h*h/totalLines)
	scrollRange := totalLines - h
	thumbPos := 0
	if scrollRange > 0 {
		thumbPos = viewStart * (h - thumbSize) / scrollRange
	}
	if thumbPos < 0 {
		thumbPos = 0
	}

	for i, line := range lines {
		ch := "░"
		if i >= thumbPos && i < thumbPos+thumbSize {
			ch = "█"
		}
		// Ensure line is exactly w-1 columns, then append scrollbar character
		lineW := ansi.StringWidth(line)
		if lineW > w-1 {
			line = ansi.Truncate(line, w-1, "")
		} else if lineW < w-1 {
			line = line + strings.Repeat(" ", w-1-lineW)
		}
		lines[i] = line + "\x1b[90m" + ch + "\x1b[0m"
	}

	return strings.Join(lines, "\n")
}

// styledCellLineWithSelection renders a row with optional selection highlighting.
// selStart/selEnd define the selected column range (-1 = no selection on this row).
func (p *PaneModel) styledCellLineWithSelection(getCell func(x int) *uv.Cell, width, selStart, selEnd int) string {
	var b strings.Builder
	var lastSGR string
	var pending int

	for x := 0; x < width; x++ {
		cell := getCell(x)
		// Wide-char continuation cell — the lead cell already spans this
		// column; emitting anything here drifts the rest of the row right.
		if cell != nil && cell.Width == 0 {
			continue
		}
		ch := " "
		styled := false
		var sgr string

		if cell != nil {
			if cell.Content != "" {
				ch = cell.Content
			}
			if !cell.Style.IsZero() {
				styled = true
				sgr = cell.Style.String()
			}
		}

		// Check if this cell is selected
		inSelection := selStart >= 0 && x >= selStart && x <= selEnd

		if inSelection {
			// Flush pending spaces before selection
			if pending > 0 {
				b.WriteString(strings.Repeat(" ", pending))
				pending = 0
			}
			if lastSGR != "" {
				b.WriteString("\x1b[m")
				lastSGR = ""
			}
			// Render with reverse video
			b.WriteString("\x1b[7m")
			b.WriteString(ch)
			b.WriteString("\x1b[m")
			continue
		}

		// Normal rendering (same as styledCellLine)
		if ch == " " && !styled {
			if lastSGR != "" {
				b.WriteString("\x1b[m")
				lastSGR = ""
			}
			pending++
			continue
		}

		if pending > 0 {
			b.WriteString(strings.Repeat(" ", pending))
			pending = 0
		}
		if sgr != lastSGR {
			if !styled && lastSGR != "" {
				b.WriteString("\x1b[m")
			} else if styled {
				b.WriteString(sgr)
			}
			lastSGR = sgr
		}
		b.WriteString(ch)
	}

	if lastSGR != "" {
		b.WriteString("\x1b[m")
	}
	return b.String()
}

// styledCellLine renders a row of cells with ANSI styles preserved.
// Trailing unstyled spaces are buffered and only flushed when followed by
// visible content, so the result is naturally right-trimmed.
func (p *PaneModel) styledCellLine(getCell func(x int) *uv.Cell, width int) string {
	var b strings.Builder
	var lastSGR string
	var pending int // buffered trailing unstyled spaces

	for x := 0; x < width; x++ {
		cell := getCell(x)
		// Wide-char continuation cell — the lead cell already spans this
		// column; emitting anything here drifts the rest of the row right.
		if cell != nil && cell.Width == 0 {
			continue
		}
		ch := " "
		styled := false
		var sgr string

		if cell != nil {
			if cell.Content != "" {
				ch = cell.Content
			}
			if !cell.Style.IsZero() {
				styled = true
				sgr = cell.Style.String()
			}
		}

		// Unstyled space — buffer (may be trailing)
		if ch == " " && !styled {
			if lastSGR != "" {
				b.WriteString("\x1b[m")
				lastSGR = ""
			}
			pending++
			continue
		}

		// Non-trivial cell: flush buffered spaces, then render
		if pending > 0 {
			b.WriteString(strings.Repeat(" ", pending))
			pending = 0
		}
		if sgr != lastSGR {
			if !styled && lastSGR != "" {
				b.WriteString("\x1b[m")
			} else if styled {
				b.WriteString(sgr)
			}
			lastSGR = sgr
		}
		b.WriteString(ch)
	}

	// Reset at end if style was active (trailing spaces already dropped)
	if lastSGR != "" {
		b.WriteString("\x1b[m")
	}
	return b.String()
}

func (p *PaneModel) insertCursor(content string) string {
	pos := p.vt.CursorPosition()
	lines := strings.Split(content, "\n")

	if pos.Y < 0 || pos.Y >= len(lines) {
		return content
	}

	// Rebuild cursor line from cell data to avoid ANSI string splitting issues.
	w := p.vt.Width()
	var b strings.Builder

	for x := 0; x < w; x++ {
		cell := p.vt.CellAt(x, pos.Y)
		// Wide-char continuation cell — the lead cell already spans this
		// column (cursor landing on one is a degenerate case; skip it too).
		if cell != nil && cell.Width == 0 {
			continue
		}
		ch := " "
		if cell != nil && cell.Content != "" {
			ch = cell.Content
		}

		if x == pos.X {
			// Cursor: reset style, render in reverse video
			b.WriteString("\x1b[0m\x1b[7m")
			b.WriteString(ch)
			b.WriteString("\x1b[27m")
		} else {
			// Non-cursor: render with cell's original style
			if cell != nil {
				if sgr := cell.Style.String(); sgr != "" {
					b.WriteString(sgr)
				}
			}
			b.WriteString(ch)
		}
	}
	b.WriteString("\x1b[0m")

	lines[pos.Y] = b.String()
	return strings.Join(lines, "\n")
}
