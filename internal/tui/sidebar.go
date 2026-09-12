package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/rivo/uniseg"
)

// minWidthForSidebar auto-collapses the project sidebar rather than
// squeezing panes into unusability on a narrow terminal.
const (
	minWidthForSidebar  = 100
	defaultSidebarWidth = 22
	// minSidebarWidth is the narrowest an edge DRAG may take the strip. Below
	// this the rows carry no information — minPaneLabelCells alone is 8, and
	// the two-cell markers and the state glyph sit beside it — while the edge
	// stays grabbable, so a user who drags too far can drag back. Zero is
	// deliberately unreachable by drag: collapsing is what the toggle is for,
	// and a sidebar dragged to nothing leaves no edge to grab it back by.
	minSidebarWidth = 12
	// minPaneLabelCells is the floor a pane's own name keeps in its row, so a
	// long blocked-reason cannot crowd out the thing that identifies which
	// pane the row is about. Eight cells is enough for the id suffix quil
	// generates ("pane-b16e" of "pane-b16e3850") when a pane has no name.
	minPaneLabelCells = 8
)

// sidebarWidth returns the layout width the project sidebar reserves: 0 when
// closed or the terminal is too narrow to spare it, otherwise the configured
// width (falling back to defaultSidebarWidth when unset/invalid, and capped
// so at least minTermWidth columns always remain for panes). Model's
// paneAreaWidth/resizeTabs read this — it is the single source of truth for
// how much screen estate the sidebar takes from the panes, so this is the one
// place that has to bound it: an unclamped configured value larger than the
// terminal would drive paneAreaWidth() negative and reach tab.Resize and
// lipgloss.Width() with it.
func sidebarWidth(total int, open bool, configured int) int {
	if !open || total < minWidthForSidebar {
		return 0
	}
	if configured <= 0 {
		configured = defaultSidebarWidth
	}
	if max := total - minTermWidth; configured > max {
		configured = max
	}
	return configured
}

// displayName appends the destination for a remote project so two projects
// called "api" on different hosts stay distinguishable.
func (p *ProjectModel) displayName() string {
	if p.Dest == "" {
		return p.Name
	}
	return p.Name + "@" + p.Dest
}

// paneStateCounts is what one project's panes add up to, for its summary row.
//
// A struct rather than five returns: the first three are one ORDERED
// classification while `pinned` and `marked` are two further independent axes,
// so a bare (working, blocked, done, pinned, marked) tuple invites a caller to
// read them as five of a kind. It also keeps projectRow's parameter list from
// growing to nine.
type paneStateCounts struct {
	working int
	blocked int
	done    int
	// pinned counts panes the USER marked by hand. Independent of the three
	// above, deliberately — see counts().
	pinned int
	// marked counts panes the user marked for deletion. Independent of the
	// three states for the same reason as `pinned`, and mutually exclusive
	// with it on the daemon — so the two badges never both count the same
	// pane, but they are tallied separately because they say opposite things.
	marked int
}

// counts aggregates the pane states a project row summarises. `done` counts
// panes that finished while unfocused: without it a turn completing in a
// BACKGROUND project is invisible at the project level, so the one place the
// user is looking when they are not in that project never tells them the work
// is ready — which is most of the reason to group panes by project at all.
//
// `pinned` is counted OUTSIDE the switch, and that is the whole difference
// between it and the other three. Those are one state a pane is in, ranked, so
// a pane contributes to exactly one; the pin is orthogonal — a pinned pane is
// usually also working or blocked, and folding it into the ranking would make
// a mark that exists to be un-loseable disappear the moment the pane got busy.
// paneRow makes the same distinction by keeping ◆ as a suffix when a live
// state outranks it.
//
// It also counts a pin on the FOCUSED pane, where tabPinnedAttention
// (workstate.go) deliberately does not — so focusing your only pinned pane
// drops the tab's ◆ while this row still reads ◆1. That is two functions
// answering two questions rather than a disagreement: the tab bar says "which
// tab should I go to", and the tab you are already on is not an answer, while
// this row says "how many marks does this project hold", which does not change
// with where you are looking. paneRow agrees with THIS one — it shows ◆ for a
// focused pinned pane — because the sidebar's pane list is an inventory too.
func (p *ProjectModel) counts() paneStateCounts {
	var c paneStateCounts
	for _, tab := range p.tabs {
		if tab.Root == nil {
			continue
		}
		for _, pane := range tab.Leaves() {
			switch {
			// Ordered, not independent: a pane parked for input has also
			// finished its turn, and "needs you" outranks "is ready".
			case !pane.blockedSince.IsZero():
				c.blocked++
			case pane.working:
				c.working++
			case pane.unseen:
				c.done++
			}
			if pane.pinnedAttention {
				c.pinned++
			}
			if pane.markedForDeletion {
				c.marked++
			}
		}
	}
	return c
}

// The link-health glyphs, deliberately kept OUT of the state-glyph const block
// below rather than added to it.
//
// That block requires every member to be a codepoint with no emoji
// presentation available, and ⚡ is U+26A1 — one of the codepoints
// TestSidebarGlyphs_OneCellAndNotEmojiCapable lists as exactly the kind a font
// may answer with a wide colour face. It is safe HERE and only here for two
// reasons the block's members cannot rely on: lipgloss already measures it as
// two cells (truncateCells' comment names it for that), so the budget
// arithmetic accounts for its real width instead of being surprised by it, and
// it is the LAST thing on the row, so a font drawing it wider still has only
// padding to paint over. Putting it in the block would fail that test,
// correctly.
const (
	glyphLinkParked = "⚡" // the reconnect ladder gave up; nothing happens until the user acts
	glyphLinkRetry  = "⟳" // reconnecting on its own
)

// linkGlyphStyles is the ONE pairing of link glyph to colour, read by
// linkGlyphStyle and swept by TestLinkGlyph_EveryStateHasItsOwnColour.
//
// A map rather than a switch so the test can enumerate it: linkGlyphStyle's
// fallback has to be *some* style, and every candidate lies about a state it
// was not written for — a third link state added to linkGlyph without a case
// here would render in the fallback's colour and read as one of the states that
// does have one. Enumerating lets the test assert that every glyph linkGlyph
// can actually produce has an entry, which a switch statement cannot express.
//
// Link health describes the DESTINATION rather than any pane, so these come
// from outside the pane-state palette rather than reusing one and saying
// something false with it. Parked takes the red spawnErrorStyle spends on a
// pane that failed to start — the link is dead and stays dead until the user
// acts. Retrying takes the 208 orange projectFormMsgBusy uses for "the machine
// is working": amber 214 is reserved for blocked-on-user, and a link healing
// itself is the opposite of that.
var linkGlyphStyles = map[string]lipgloss.Style{
	glyphLinkParked: sidebarLinkParkedStyle,
	glyphLinkRetry:  sidebarLinkRetryStyle,
}

// linkGlyph reports the connection health of a destination: ⟳ reconnecting,
// ⚡ parked, empty when healthy. Reads through linkOf, never linkFor — linkFor
// has a pointer receiver and CREATES a map entry on first use, and this runs
// on every render of every project row, so calling it here would allocate a
// reconnectState for every destination that has never dropped, once per
// frame, only to throw it away (a value-receiver View can't keep the
// mutation anyway).
//
// Every non-empty value it can return needs an entry in linkGlyphStyles.
//
// It takes the offline state as well as the destination because the two carry
// different halves of the answer: the ladder's own ⟳/⚡ come from reconnectState,
// but a destination that needs an install or an upgrade never enters the ladder,
// so its state stays zero and a link-only reading would render nothing at all
// for exactly the two cases the user has to act on.
func (m *Model) linkGlyph(dest string, off *OfflineState) string {
	ls := m.linkOf(dest)
	switch {
	case ls.parked:
		return glyphLinkParked
	case ls.active:
		return glyphLinkRetry
	case off != nil && (off.Kind == offlineNeedsInstall || off.Kind == offlineNeedsUpgrade):
		// glyphLinkParked rather than a bare "⚡": every glyph this returns
		// needs an entry in linkGlyphStyles, and a host waiting on an install
		// or an upgrade IS what that constant names — the ladder will not act
		// on its own, so nothing happens until the user does.
		return glyphLinkParked
	default:
		return ""
	}
}

// linkGlyphStyle paints a link glyph by what it MEANS, keyed off the glyph
// rather than off reconnectState. projectRow is a pure function of already
// resolved strings — that is what lets the width sweep drive it directly with a
// literal link — so threading the style through as a second parameter would put
// the row builder's signature at the mercy of a state type it never sees.
//
// The fallback is the DIM style, which is the idle colour, so an unpaired glyph
// reads as "nothing to report" rather than borrowing a live state's colour. It
// is still wrong for any real state, which is why linkGlyphStyles is a swept
// map rather than an unchecked default.
func linkGlyphStyle(glyph string) lipgloss.Style {
	if style, ok := linkGlyphStyles[glyph]; ok {
		return style
	}
	return sidebarDimStyle
}

// sidebarRowProject / sidebarRowPane label an actionable sidebar row. The
// empty kind is chrome (the two headings, the spacer, per-tab headings):
// inert, but still inside the strip, so a click on one is swallowed rather
// than falling through to whatever pane the strip displaced.
const (
	sidebarRowProject = "project"
	sidebarRowPane    = "pane"
	// sidebarRowTab is a tab heading in the PANES section. A click switches to
	// the tab; a drag reorders it (reorder.go). index is the tab's ordinal.
	sidebarRowTab = "tab"
)

// sidebarRow is one rendered row of the project sidebar: the painted text
// plus what it points at. renderSidebar joins the text of this slice and
// sidebarRowAt indexes the same slice, so a row inserted into the paint
// cannot drift out of step with what a click on that row resolves to —
// hit-testing written as a second, independent copy of the row layout is
// exactly how that drift happens.
type sidebarRow struct {
	text   string
	kind   string
	index  int    // project index (kind project), tab index (kind tab) or pane ordinal (kind pane)
	tabIdx int    // rows inside a tab group (inTab): index into curTabs()
	paneID string // pane rows only
	// inTab marks every row of a tab's group — heading, pane rows, git rows —
	// so a tab drag can measure the group's height and tell which tab the
	// pointer is over from ANY of its rows, not just the heading.
	inTab bool
}

// sidebarRows builds the sidebar's rows in paint order at width w: every
// project with its aggregate working/blocked counts and link health (active
// project marked), then the active project's tabs and panes with per-pane
// agent-state glyphs.
//
// The second return value is the index of the first row after the PANES
// heading — where the pinned block ends and the scrollable body begins.
func (m *Model) sidebarRows(w int) ([]sidebarRow, int) {
	rows := []sidebarRow{{text: sidebarHeading("PROJECTS", w)}}
	for i, p := range m.projects {
		// The NAME alone on the first row. displayName's "name@dest" was
		// written for the picker, where a dialog is wide enough for it; at the
		// sidebar's 22 columns "Default@build@gpu01" leaves nothing of
		// either half, and the badges that say whether the project needs you
		// are what gets truncated away first.
		rows = append(rows, sidebarRow{
			text: projectRow(sanitizeRemoteText(p.Name), p.counts(), m.workSpinnerFrame,
				m.linkGlyph(p.Dest, p.Offline), i == m.activeProject, w, p.Offline),
			kind:  sidebarRowProject,
			index: i,
		})
		// The host gets its own row, and only a remote project has one — a
		// local project spending a line to say "this machine" would halve how
		// many projects fit for no information. Same kind and index, so a
		// click on either row selects the same project rather than falling
		// through to the pane underneath.
		if p.Dest != "" {
			rows = append(rows, sidebarRow{
				text:  projectDestRow(sanitizeRemoteText(p.Dest), w),
				kind:  sidebarRowProject,
				index: i,
			})
		}
	}

	rows = append(rows, sidebarRow{}, sidebarRow{text: sidebarHeading("PANES", w)})
	// Everything from here on is the scrollable body. The builder is the only
	// thing that knows where the section starts; deriving it later by matching
	// the heading text would be a second source of truth for one boundary.
	panesStart := len(rows)
	// The active tab and the pane inside it that holds focus are marked the
	// same way the active project is, so one glance answers "where am I"
	// at all three levels. Only the ACTIVE tab's focused pane is marked:
	// every tab carries an ActivePane, and marking all of them would say
	// "you are here" in several places at once.
	activeTabIdx := -1
	if p := m.cur(); p != nil {
		activeTabIdx = p.activeTab
	}
	ordinal := 0
	for ti, tab := range m.curTabs() {
		onTab := ti == activeTabIdx
		// A blank row between tab groups. Without it the whole PANES section
		// is one unbroken column and a tab heading reads as just another pane
		// row — the grouping the section exists to show is the first thing
		// lost. Not before the FIRST group: the section heading already
		// separates it, and a second blank there is just wasted height on a
		// strip that scrolls.
		if ti > 0 {
			rows = append(rows, sidebarRow{})
		}
		rows = append(rows, sidebarRow{
			text:   sidebarTabHeading(sanitizeRemoteText(tab.Name), ti, onTab, tab.Color, w),
			kind:   sidebarRowTab,
			index:  ti,
			tabIdx: ti,
			inTab:  true,
		})
		for _, pane := range tab.Leaves() {
			rows = append(rows, sidebarRow{
				text:   paneRow(pane, onTab && pane.ID == tab.ActivePane, w),
				kind:   sidebarRowPane,
				index:  ordinal,
				tabIdx: ti,
				paneID: pane.ID,
				inTab:  true,
			})
			ordinal++
			// Git state gets its own row rather than more suffix: at the
			// default 22 columns a branch name and a pane name cannot share
			// one. Non-interactive (no kind) — giving it the pane's ordinal
			// would put two rows on one index and desync every hit test from
			// the attention queue's numbering. It is still part of the tab's
			// group for a tab drag, hence inTab.
			if git := gitRow(pane, w); git != "" {
				rows = append(rows, sidebarRow{text: git, tabIdx: ti, inTab: true})
			}
		}
	}
	return rows, panesStart
}

// renderSidebar renders the project sidebar. height is the number of screen
// rows to fill, and callers pass sidebarContentHeight() — the strip spans the
// TAB BAR row too (the bar is joined inside the pane column, to the right of
// this block), so only the status bar is drawn separately by View().
//
// The width comes from projectSidebarWidth(), NOT the raw m.sidebarWidth
// field: that field is the CONFIGURED value, and sidebarWidth() is what
// clamps it against the terminal. Sizing the box off the raw field made the
// pane area clamp correctly while this box did not, so the
// lipgloss.JoinHorizontal in View() composited a frame wider than the
// terminal for any sidebar_width larger than it. The <= 0 fallback only
// covers callers with no window size yet (tests) — View() never draws the
// sidebar unless projectSidebarWidth() is already positive.
func (m *Model) renderSidebar(height int) string {
	w := m.projectSidebarWidth()
	if w <= 0 {
		w = defaultSidebarWidth
	}

	rows := m.sidebarVisibleRows(w, height)
	lines := make([]string, len(rows))
	for i, r := range rows {
		lines[i] = r.text
	}

	content := strings.Join(lines, "\n")
	// .Width/.Height is a final sizing pass over rows this file has already
	// padded to exactly w CELLS (padOrTrunc). It must never have anything
	// left to do horizontally: .Width WRAPS an over-wide line onto a new
	// one rather than truncating it, which would shift every row below it
	// while sidebarRowAt still maps screen row y to rows[y].
	return lipgloss.NewStyle().Width(w).Height(height).Render(content)
}

// minPaneRows is the floor of PANES rows sidebarVisibleRows will leave. When
// the pinned PROJECTS block alone would push the body below it, the whole strip
// reverts to the pre-scroll tail cap: a strip showing eight projects and no
// panes is worse than one showing a truncated list of both.
const minPaneRows = 3

// maxSidebarScrollFor is the largest offset that still paints content. One row
// of the window goes to the "N above" marker as soon as the body is scrolled at
// all, so the last page holds bodyH-1 rows.
func maxSidebarScrollFor(bodyLen, bodyH int) int {
	if bodyH <= 1 || bodyLen <= bodyH {
		return 0
	}
	// Provably positive here: the guard above already rules out bodyLen <=
	// bodyH, so bodyLen-(bodyH-1) is at least 2. Kept rather than inlined so
	// the property stays checked if the guard above is ever loosened.
	if limit := bodyLen - (bodyH - 1); limit > 0 {
		return limit
	}
	return 0
}

// sidebarBodyGeometry derives the scrollable PANES body's length and window
// height from an already-built row list, its panesStart boundary, and the
// render height — the one place that arithmetic is written. sidebarVisibleRows
// and scrollSidebar both call it with the SAME (rows, panesStart) pair their
// own m.sidebarRows(w) call already produced, so a second, hand-derived copy
// (as scrollSidebar's first draft had) can never drift from the one the paint
// windows against — that drift would clamp a wheel notch against a bound the
// user cannot see, a dead-feeling scroll plateau rather than row drift, since
// the paint still re-clamps every render.
func sidebarBodyGeometry(rows []sidebarRow, panesStart, height int) (bodyLen, bodyH int) {
	return len(rows) - panesStart, height - panesStart
}

// sidebarBodyWindowed reports whether the PANES body is windowed at this
// height — i.e. whether an offset means anything at all. It is false both when
// the whole list fits and when the pinned PROJECTS head would leave the body
// below minPaneRows, because the degenerate strip reverts to the old tail cap
// and has no window to offset into.
//
// One definition for the same reason sidebarBodyGeometry has one: the paint and
// both writers ask this question, and a copy that drifts is invisible — the
// paint stays correct while the writers clamp against a different regime.
func sidebarBodyWindowed(rows []sidebarRow, panesStart, height int) bool {
	return height > 0 && len(rows) > height && panesStart <= height-minPaneRows
}

// clampSidebarScroll bounds an offset into a body of bodyLen rows shown through
// a bodyH-row window. Pure — callers that own the stored offset write it back
// themselves.
func clampSidebarScroll(off, bodyLen, bodyH int) int {
	if off < 0 {
		return 0
	}
	if max := maxSidebarScrollFor(bodyLen, bodyH); off > max {
		return max
	}
	return off
}

// sidebarVisibleRows caps sidebarRows to the rows that actually fit in
// height. lipgloss's .Height PADS but never CLIPS, so an uncapped list
// (1 + projects + 2 + Σ(1 + panes) rows, all unbounded) grows the composited
// block past the terminal and pushes the status bar off the bottom —
// the vertical twin of the width clamp, and the reason
// NotificationCenter.View computes its own maxVisible rather than trusting
// .Height. minWidthForSidebar gates columns only.
//
// The PROJECTS block is PINNED and the PANES body windows at m.sidebarScroll:
// that block is the navigation the sidebar exists for, and scrolling it away
// would leave a user with many panes unable to reach another project at all.
// Overflow on either side of the window is marked, so a body that continues
// past the strip never just stops.
//
// The offset is clamped from a LOCAL copy and nothing here writes model state.
// This runs on the render path, and a render that mutates is how a paint and a
// hit test come to disagree; scrollSidebar owns the stored value.
//
// Both the paint and the hit test call this, with the same height — a cap or an
// offset applied in only one of them is the row-drift bug in another form.
func (m *Model) sidebarVisibleRows(w, height int) []sidebarRow {
	rows, panesStart := m.sidebarRows(w)
	if height <= 0 || len(rows) <= height {
		return rows
	}
	head, body := rows[:panesStart], rows[panesStart:]

	// Degenerate strip: the pinned head alone would starve the body. Fall back
	// to the pre-scroll behaviour for the WHOLE list — the tail is dropped and
	// the last row says so. Asked through the same helper the two writers use;
	// the height and length halves of it are already known true here.
	if !sidebarBodyWindowed(rows, panesStart, height) {
		out := append([]sidebarRow(nil), rows[:height]...)
		out[height-1] = sidebarRow{text: sidebarDimStyle.Render(padOrTrunc(" …", w))}
		return out
	}

	_, bodyH := sidebarBodyGeometry(rows, panesStart, height)
	off := clampSidebarScroll(m.sidebarScroll, len(body), bodyH)

	// Markers cost a row each and appear only on the side that has more. They
	// carry no kind, so sidebarHit treats them as inert chrome.
	avail := bodyH
	top := off > 0
	if top {
		avail--
	}
	bottom := off+avail < len(body)
	if bottom {
		avail--
	}
	// Provably unreachable, like maxSidebarScrollFor's own limit>0 above: the
	// branch above this point only runs when panesStart <= height-minPaneRows,
	// so bodyH >= minPaneRows (3) and at most two of those rows go to markers
	// — avail cannot fall below 1. Kept as an explicit totality check rather
	// than trusted-by-construction, for the same reason the other two are.
	if avail < 0 {
		avail = 0
	}
	end := off + avail
	// Also provably unreachable: off is clamped to maxSidebarScrollFor(len(body),
	// bodyH), and avail is derived so off+avail never outruns len(body) — the
	// "bottom" test above IS that derivation. A third totality guard on the
	// same property, not a second independent bound.
	if end > len(body) {
		end = len(body)
	}

	out := make([]sidebarRow, 0, height)
	out = append(out, head...)
	if top {
		out = append(out, sidebarRow{text: sidebarDimStyle.Render(
			padOrTrunc(fmt.Sprintf(" %s %d above", glyphMore, off), w))})
	}
	out = append(out, body[off:end]...)
	if bottom {
		out = append(out, sidebarRow{text: sidebarDimStyle.Render(
			padOrTrunc(fmt.Sprintf(" %s %d below", glyphMore, len(body)-end), w))})
	}
	return out
}

// scrollSidebar moves the PANES section by one wheel notch. It owns the write
// to m.sidebarScroll — sidebarVisibleRows only ever clamps a local copy — and
// re-derives the body geometry from the same sidebarRows/height pair the paint
// uses via sidebarBodyGeometry, so the bound the user hits is the bound they
// can see.
func (m *Model) scrollSidebar(up bool) {
	w := m.projectSidebarWidth()
	if w <= 0 {
		return
	}
	height := m.sidebarContentHeight()
	rows, panesStart := m.sidebarRows(w)
	// Nothing to scroll, or the degenerate short strip that reverts to the
	// tail cap: in both cases the only correct offset is zero.
	if !sidebarBodyWindowed(rows, panesStart, height) {
		m.sidebarScroll = 0
		return
	}
	bodyLen, bodyH := sidebarBodyGeometry(rows, panesStart, height)
	lines := m.cfg.UI.MouseScrollLines
	// Floored AND capped. The value is a hand-editable config int, and nothing
	// downstream bounds it: off+lines overflows at the top of the int range, and
	// the negative sum then clamps to 0 — a wheel-DOWN notch that jumps the
	// strip to the top. bodyH is the natural ceiling anyway, since one notch
	// should never move further than the window it moves within (and bodyH >=
	// minPaneRows here, so a sane config is untouched).
	if lines < 1 {
		lines = 3
	} else if lines > bodyH {
		lines = bodyH
	}
	if up {
		lines = -lines
	}
	// The STORED offset is re-clamped before the notch is added to it, not just
	// after. Nothing clamps it when the geometry changes underneath — the paint
	// clamps a local copy, deliberately — so a vertical resize (bodyH moves with
	// sidebarContentHeight) or a closed pane (bodyLen) legitimately leaves it
	// past the current maximum. Adding to the stale value there clamps straight
	// back to the same visible maximum for as many notches as it is stale by:
	// the wheel does nothing while the strip is already showing the bound it is
	// being pushed against. That is the dead scroll plateau sidebarBodyGeometry
	// exists to prevent, reached by a route the shared geometry cannot cover.
	off := clampSidebarScroll(m.sidebarScroll, bodyLen, bodyH)
	m.sidebarScroll = clampSidebarScroll(off+lines, bodyLen, bodyH)
}

// scrollSidebarToPane moves the PANES section the minimum distance that brings
// paneID's row into the visible window, and does nothing when it is already
// there. The visible span used to pick a NEW offset is computed as bodyH-2 —
// the worst case, both markers present — so the target can never land
// underneath one.
func (m *Model) scrollSidebarToPane(paneID string) {
	if paneID == "" {
		return
	}
	w := m.projectSidebarWidth()
	if w <= 0 {
		return
	}
	height := m.sidebarContentHeight()
	rows, panesStart := m.sidebarRows(w)
	if !sidebarBodyWindowed(rows, panesStart, height) {
		m.sidebarScroll = 0
		return
	}
	// Already on screen under the CURRENT offset: nothing to do, checked
	// against the REAL painted window rather than the conservative one below.
	// The two differ by a row exactly when only one marker is showing (real
	// avail is bodyH-1, one more than bodyH-2) — a caller whose paneID is the
	// last visible row in that state would otherwise be scrolled off a click
	// on a row the paint already shows. sidebarVisibleRows is pure, so this
	// costs a second row-list build and writes nothing.
	for _, r := range m.sidebarVisibleRows(w, height) {
		if r.kind == sidebarRowPane && r.paneID == paneID {
			return
		}
	}
	idx := -1
	for i := panesStart; i < len(rows); i++ {
		if rows[i].kind == sidebarRowPane && rows[i].paneID == paneID {
			idx = i - panesStart
			break
		}
	}
	if idx < 0 {
		return
	}
	bodyLen, bodyH := sidebarBodyGeometry(rows, panesStart, height)
	visible := bodyH - 2
	if visible < 1 {
		visible = 1
	}
	// Re-clamped before the arithmetic, exactly as scrollSidebar does and for
	// the reason its comment gives: nothing bounds the stored value when the
	// geometry moves underneath, so it can legitimately be past the current
	// maximum. Today the two branches below happen to be safe against a
	// stale-high offset — the idx >= off+visible branch is unreachable and the
	// other assigns before clamping — but that is a proof standing next to a
	// function whose comment says not to rely on one, and the early
	// already-visible check above already runs against the CLAMPED painted
	// window. Same input, same reading.
	off := clampSidebarScroll(m.sidebarScroll, bodyLen, bodyH)
	switch {
	case idx < off:
		off = idx
	case idx >= off+visible:
		off = idx - visible + 1
	}
	m.sidebarScroll = clampSidebarScroll(off, bodyLen, bodyH)
}

// sidebarRowAt resolves the project-sidebar row under a SCREEN coordinate.
// View() joins the sidebar to the LEFT of the pane column — tab bar included
// — so the strip starts at screen row 0 and ends before the status bar, and
// screen row y is sidebar row y. Its first row is the PROJECTS heading,
// which is why the design puts that heading level with the tab names.
func (m *Model) sidebarRowAt(x, y int) (sidebarRow, bool) {
	w := m.projectSidebarWidth()
	if w <= 0 || x < 0 || x >= w {
		return sidebarRow{}, false
	}
	if y < 0 || y >= m.height-1 {
		return sidebarRow{}, false
	}
	// Same height View() passes renderSidebar, so paint and hit test cap
	// at the identical row.
	rows := m.sidebarVisibleRows(w, m.sidebarContentHeight())
	if y < len(rows) {
		return rows[y], true
	}
	return sidebarRow{}, false
}

// sidebarHit maps a screen coordinate to the sidebar row under it, as a
// kind ("project" / "tab" / "pane") and that kind's index. Returns ("", -1) for any
// x at or beyond the reserved width — the panes begin exactly there — and
// for inert chrome rows inside the strip.
func (m *Model) sidebarHit(x, y int) (kind string, index int) {
	row, ok := m.sidebarRowAt(x, y)
	if !ok || row.kind == "" {
		return "", -1
	}
	return row.kind, row.index
}

// projectSidebarSwallowsMouse reports whether a press or wheel at (x, y)
// lands on the project sidebar's strip. Deliberately wider than sidebarHit:
// chrome rows resolve to no action but must still be swallowed, because the
// pane area now starts at column projectSidebarWidth() and letting the press
// fall through would arm a drag-selection at a column the user never
// clicked.
//
// Row 0 is INCLUDED: the tab bar no longer spans the frame, it starts where
// the sidebar ends, so the sidebar's own first row occupies row 0 in these
// columns. Excluding it let a click on the PROJECTS heading reach the
// Y==0 tab-bar branch in Update and switch tabs. Only the last row (the
// status bar, still drawn full width beneath the sidebar) is exempt.
func (m Model) projectSidebarSwallowsMouse(x, y int) bool {
	w := m.projectSidebarWidth()
	return w > 0 && x >= 0 && x < w && y >= 0 && y < m.height-1
}

// sidebarDragRule is the glyph painted down the pending edge while the sidebar
// is being dragged, in the same bright blue a split-border drag highlights
// with (pane.go's splitDragHighlight) so the two drags read as one gesture
// vocabulary rather than two conventions.
//
// A box-drawing vertical rather than a block, so it reads as a boundary rather
// than as content — and, like every sidebar glyph, a single cell with no emoji
// presentation available: an emoji-capable codepoint can be drawn two cells
// wide while advancing one, painting over its neighbour.
const sidebarDragRule = "│"

var sidebarDragRuleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))

// sidebarDragRuleBlock builds the one-column, rows-tall block the drag preview
// composites onto the frame.
//
// Returned as a BOX for overlayAt rather than cut in by hand: that function
// already solves the ANSI boundary problem — a truncate that lands mid-glyph,
// an SGR left open at the cut, a wide glyph straddling the seam — and a second
// cutter written here would have to solve all three again to be correct at one
// column.
func sidebarDragRuleBlock(rows int) string {
	if rows <= 0 {
		return ""
	}
	line := sidebarDragRuleStyle.Render(sidebarDragRule)
	return strings.Repeat(line+"\n", rows-1) + line
}

// sidebarEdgeHitPadding widens the drag zone to the sidebar's own last column
// as well as the first pane column, mirroring the split border's "both drawn
// glyphs grab the line" rule. A one-column target on a boundary the user aims
// at with a mouse is needlessly precise, and neither column is claimed by
// anything else: there is no split at the sidebar boundary, so
// hitTestSplitBorder never returns it.
const sidebarEdgeHitPadding = 1

// hitTestSidebarEdge reports whether (x, y) lands on the project sidebar's
// draggable right edge.
//
// Width 0 — a closed sidebar, or a terminal below minWidthForSidebar — offers
// NO edge. Both states arrive through projectSidebarWidth() as 0, and
// answering true there would arm a drag against a strip that is not painted.
// Reading the accessor rather than m.sidebarWidth is also what keeps the zone
// on the RENDERED edge: sidebarWidth() clamps, so a configured value the
// terminal cannot afford is drawn narrower than it is stored.
//
// Row 0 is included and the last row excluded, matching
// projectSidebarSwallowsMouse: the sidebar's own first row occupies row 0 in
// these columns, while the status bar is still drawn full width beneath it.
// Row 0 takes the sidebar's OWN column only. The tab bar starts at screen
// column projectSidebarWidth() (hitTestTab documents this), so extending the
// pane-side padding column into row 0 would swallow the first cell of tab 1 —
// a click there would arm a sidebar drag and, released without motion, do
// nothing at all. The sidebar's own last column is still grabbable on every
// row including this one.
func (m Model) hitTestSidebarEdge(x, y int) bool {
	w := m.projectSidebarWidth()
	if w <= 0 || y < 0 || y >= m.height-1 {
		return false
	}
	if y == 0 {
		return x == w-1
	}
	return x >= w-sidebarEdgeHitPadding && x <= w
}

// The sidebar's state vocabulary, shared by the per-pane rows and the project
// row that rolls them up — one notation, so the summary reads as a roll-up
// rather than a second convention.
//
// Every one of these is a single rune with NO emoji presentation available
// (Emoji=No), and that is a hard requirement rather than a preference. An
// emoji-CAPABLE codepoint is subject to font fallback: the terminal picks a
// colour emoji face, draws it about two cells wide, and advances one — so it
// overpaints whatever follows. In the project badge what follows is the count,
// which is how "⚠1 ◐2" rendered as a warning sign with its number hidden
// underneath it. Where the terminal instead ADVANCES two cells the damage is
// worse and quieter: lipgloss measures U+26A0 as one (measured — U+26A1 ⚡
// measures two, which is why the truncateCells comment names that one), so the
// row is painted one cell wider than every helper here believes, and
// renderSidebar's closing .Width(w) wraps the excess onto a new line.
//
// U+26A0 WARNING SIGN was the single emoji-capable glyph in this set, and the
// only one that misbehaved. Forcing text presentation with U+FE0E was tried and
// rejected: it depends on the terminal honouring a variation selector, and the
// emoji-presentation alternative (U+FE0F, which lipgloss does measure as two)
// walks straight into the per-rune truncation overflow documented on
// truncateCells. A codepoint that was never emoji needs neither.
//
// The WORKING state is the one member of this vocabulary that is NOT a
// constant — it animates, and lives in workingGlyph below. Every frame it can
// return is bound by the same rule as the constants here.
const (
	glyphBlocked = "▲" // parked waiting on the user — needs you
	glyphDone    = "✓" // finished while you were away
	glyphIdle    = "○" // nothing happening
	glyphPinned  = "◆" // attention pinned by hand — never auto-cleared
	// glyphDeletion is the user's "done with this pane, safe to close" mark.
	// U+232B ERASE TO THE LEFT: one rune, one cell, Emoji=No, and it belongs to
	// no other state in this vocabulary. Deliberately not ✗ or × — this is not
	// a failure, and every other member of this set describes what the pane IS
	// rather than what went wrong with it.
	//
	// Worth knowing rather than a problem: ⌫ is also the Backspace KEY symbol,
	// and Alt+Backspace is a bound pane action (pane-history back). It collides
	// with no other STATE glyph, which is what this block is a vocabulary for,
	// but if the keybinding docs ever grow a glyph column the two conventions
	// will meet.
	glyphDeletion = "⌫" // marked for deletion by hand — never auto-cleared
	// glyphMore marks rows the PANES window is hiding above or below itself.
	// U+22EF is already what paneRow uses for the subagent count, so it is
	// proven against the rule this block states. Deliberately NOT ▲/▼: ▲ is
	// glyphBlocked, and a scroll marker wearing the blocked glyph is exactly
	// the state-vocabulary confusion the rest of this file exists to remove.
	glyphMore = "⋯" // more rows in this direction (U+22EF)
)

// workingGlyph is the WORKING state's glyph: an ANIMATED braille frame, taken
// from the same spinnerFrames the tab label and the pane's top border already
// cycle, rather than a static rune of the sidebar's own.
//
// One fact, one indicator. The sidebar spent a still ◐ on the state the tab bar
// two rows above was spinning ⠹ for, and the sidebar is the level that lists
// every pane at once — so it is exactly where a second notation for "this agent
// is running" reads as a second STATE. Motion is also what separates working
// from the four states that are not: ▲/✓/◆/○ all describe something that has
// stopped, and a static ◐ among them says "in progress" only if you already
// know the vocabulary.
//
// Every frame satisfies the const block's rule above — one rune, one cell,
// Emoji=No — and TestSidebarGlyphs_OneCellAndNotEmojiCapable sweeps them all
// rather than the single value a constant would have had.
//
// The frame is a PARAMETER rather than a read of the Model, because the callers
// hold different copies of the same counter: paneRow passes the pane's own
// mirrored workFrame (what buildTopBorder renders, so a pane's row and its
// border cannot disagree), and projectRow passes the shared workSpinnerFrame it
// is derived from. The tick writes both in one pass, so every level animates in
// lockstep.
//
// It is the ONE definition of the index→glyph mapping for the work spinner —
// all four renderers of it go through here (tabLabel in model.go, buildTopBorder
// in pane.go, and this file's two rows). Sidebar-only centralisation was the
// first version and it is not enough to keep the promise the paragraph above
// makes: with the other two hand-rolling `spinnerFrames[f%len(f)]`, any change
// here — a guard, an offset, a different subset of frames — moves the sidebar
// and leaves the border behind, which is the "one fact, two notations" state
// this function exists to remove, reappearing one level down. The tab label is
// cross-checked against the sidebar by
// TestSidebar_WorkingIndicatorAnimatesWithTheTabSpinner; the border is checked
// by nothing, since border_test.go passes frame 0 — the single value where every
// implementation agrees by construction.
//
// NOT shared with the RESTORE spinner (pane.go's p.spinnerFrame), deliberately:
// that is a different counter with a different lifecycle, describing a pane
// coming back rather than an agent working, and the two are free to diverge.
//
// The double modulo is defence for a RENDER path. Both work counters are
// client-local and monotonic from zero, so a negative frame is unreachable
// today — but Go's % keeps the sign of the dividend, so if one ever arrives the
// index panics, and a panic in View() takes down the whole multiplexer rather
// than one glyph. One line, and it is free for every value that actually
// occurs; it is only worth having because this is now the single site.
func workingGlyph(frame int) string {
	n := len(spinnerFrames)
	return spinnerFrames[((frame%n)+n)%n]
}

// sidebarHeadingStyle / sidebarDimStyle / the state-glyph styles mirror the
// palette already used for tab/pane state elsewhere (styles.go): amber for
// blocked-on-user, blue for active work, green for done-unseen, dim grey for
// idle and section chrome.
var (
	sidebarHeadingStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("244"))
	sidebarDimStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
	sidebarTabNameStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("255"))
	sidebarActiveStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230"))
	sidebarProjectStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
	sidebarBlockedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	// 208, not the 214 sidebarBlockedStyle spends on the ▲ badge: a project can
	// be offline AND holding a blocked agent at the same time, and one colour
	// for both makes "this host is gone" and "an agent wants you" the same
	// signal.
	sidebarOfflineStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("208"))
	sidebarWorkingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	sidebarUnseenStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("28"))
	sidebarPinnedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("141"))
	// 160, and it has to avoid three neighbours rather than one: 214 is the
	// blocked ▲, 208 the offline host, 203 the parked link. All three say
	// "something is wrong or waiting"; this one says the opposite — the pane is
	// finished with — so it takes the darkest red left, which reads as a
	// terminal state rather than an alarm.
	sidebarDeletionStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("160"))
	sidebarGitStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	sidebarGitStaleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Italic(true)
	// Link health, paired to its glyphs by linkGlyphStyles — see there for why
	// these come from outside the pane-state palette above.
	sidebarLinkParkedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	sidebarLinkRetryStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("208"))
)

// styledSegment is one run of a row's PLAIN text paired with the style that
// paints it. Rows that mix states (the project badge rolls up three at once)
// cannot be built as a single styled string: every style.Render emits its own
// reset, so wrapping an already-styled line closes the outer colour at the
// first inner segment and leaves the rest of the row unpainted.
type styledSegment struct {
	text  string
	style lipgloss.Style
}

// renderStyledSegments paints each segment with its own style and returns
// exactly w cells.
//
// The budget is spent on PLAIN text, segment by segment, and styling is applied
// only to the piece that survived the cut. That ordering is the whole point:
// truncateCells segments on grapheme clusters and lipgloss.Width measures an
// escape sequence as ZERO cells, so a single pass over an already-styled string
// would both mis-measure the row and cut through the middle of an SGR sequence
// — emitting a fragment like "38;5;214m" as literal text. Every other row
// builder in this file gets to use padOrTrunc directly because it has one style
// for the whole line; this is that same guarantee for rows that do not.
//
// Segments are spent in order, so an earlier one is never truncated to make
// room for a later one, and a segment that does not fit ENDS the row rather
// than yielding its place to the next — projectRow relies on the first half to
// give its name the leftover cells before the badge takes the tail, and the
// second is what stops a dropped segment from silently putting one state's
// glyph in another's position.
//
// # Preconditions
//
// Each segment's text must be PLAIN (no escape sequences — see above), must
// contain no tabs (Style.Render expands one to four spaces after this function
// has measured it), and must begin at a GRAPHEME CLUSTER boundary. Its style
// must set no Width/Padding/Border, all of which pad inside Render.
//
// The cluster-boundary precondition is the subtle one, and it is what makes
// summing independently measured segments exact. A rune can change the width of
// the one before it — U+FE0F measures 0 alone and makes the pair before it
// measure 2, which is the trap truncateCells' own comment documents — so a
// segment STARTING with a combining mark joins the previous segment's last
// cluster and the sum understates the row. Worse, whether it does so depends on
// the styles involved rather than on the text: an SGR sequence emitted between
// the two runes separates them and the row measures as summed, while two
// property-free styles emit nothing between them and it does not. Measured, at
// w=3 over segments {"⚠", "️"}: 3 cells with either style coloured, 4 with
// both plain. No measurement strategy fixes that — the two cases genuinely
// differ — so the boundary is a requirement on callers, not something this
// function can repair. Every caller satisfies it by construction: projectRow's
// badge segments each begin with a space, and its head ends wherever
// truncateCells cut, which is a cluster boundary by definition.
func renderStyledSegments(segs []styledSegment, w int) string {
	var b strings.Builder
	used := 0
	for i := range segs {
		// Indexed rather than ranged by value: lipgloss.Style is 648 bytes, so
		// a value range copies one per iteration on a per-frame render path.
		s := &segs[i]
		// An EMPTY segment is not a budget event — it contributes nothing and
		// must not be mistaken for one that did not fit, which is why the two
		// cases are separated rather than sharing one t == "" test.
		if s.text == "" {
			continue
		}
		if used >= w {
			break
		}
		t := truncateCells(s.text, w-used)
		if t == "" {
			// Not even this segment's first cluster fits. Later segments are
			// narrower than nothing, so the row is done and the pad below
			// finishes it.
			break
		}
		used += lipgloss.Width(t)
		b.WriteString(s.style.Render(t))
	}
	// The same backstop padOrTrunc gives every other row: renderSidebar's
	// closing .Width(w) WRAPS a short line's neighbour rather than padding
	// predictably, and a row that stopped short would leave that pass work to do
	// on a strip whose y->row mapping depends on it having none.
	//
	// These cells are the one part of the row emitted OUTSIDE any Render, which
	// is invisible for the foreground-only styles this file uses and would show
	// as an unpainted gap the day a sidebar style grows a background.
	if pad := w - used; pad > 0 {
		b.WriteString(strings.Repeat(" ", pad))
	}
	return b.String()
}

// projectDestRow renders a remote project's host under its name. Indented and
// dimmed so the column still reads as a list of projects rather than eight
// entries, and elided in the middle for the same reason a branch is: an ssh
// destination is user@host, and cutting either end alone leaves a column where
// every row looks the same.
func projectDestRow(dest string, w int) string {
	const prefix = "   @"
	avail := w - len(prefix)
	if avail < 1 {
		avail = 1
	}
	return sidebarGitStyle.Render(padOrTrunc(prefix+elideMiddle(dest, avail), w))
}

// minGitBranchCells is the floor a branch name keeps on its row, for the same
// reason paneRow has one: the branch is the answer, the divergence counts are
// detail about it. Subtracting the counts first would leave "fea…" beside a
// crisp "↑12↓3".
const minGitBranchCells = 8

// gitRow renders a pane's checkout beneath it: branch, linked-worktree marker,
// and divergence from upstream. Empty string when there is nothing to say —
// a pane outside a repository gets no row at all rather than a blank one.
//
// The counts are omitted when the branch has no upstream, because "↑0↓0" and
// "no upstream to compare against" are different facts and only one of them is
// true. A stale entry is dimmed and marked rather than hidden: the last branch
// we actually saw is more useful than nothing, as long as it does not claim to
// be current.
// worktreeNameIsRedundant reports whether naming the worktree would merely
// restate the branch already on the row.
//
// The near-universal convention is a worktree named after its branch with the
// path separators swapped for hyphens (feat/x → feat-x), and this row is 22
// cells by default: spending eight of them on a restatement pushes the branch
// into middle-elision for no information. Where the two genuinely differ — an
// agent creating "wt-1" on branch "feat/refactor-sidebar" — the name is what
// the branch cannot tell the user, so it is shown.
//
// An EMPTY name is redundant by definition (there is nothing to show, and the
// bare marker still has to say the pane is in a worktree). An empty BRANCH is
// not: a detached checkout has no branch to restate, so any name it has is the
// only thing the row can offer.
func worktreeNameIsRedundant(name, branch string) bool {
	if name == "" {
		return true
	}
	if branch == "" {
		return false
	}
	return name == strings.ReplaceAll(branch, "/", "-")
}

func gitRow(pane *PaneModel, w int) string {
	name := pane.GitBranch
	if name == "" && pane.GitDetached {
		name = "detached"
	}
	if name == "" && !pane.GitWorktree {
		return ""
	}
	name = sanitizeRemoteText(name)

	prefix := "  ⎇ "
	var suffix string
	if pane.GitWorktree {
		// Sanitized HERE, before the width math below measures it: the name
		// comes from a daemon the user may not control under --remote, and
		// lipgloss measures an escape as zero cells, so a truncation is not a
		// sanitiser. `name` is already sanitized above, so the comparison is
		// between two sanitized values.
		wtName := sanitizeRemoteText(pane.GitWorktreeName)
		if worktreeNameIsRedundant(wtName, name) {
			suffix += " wt"
		} else {
			suffix += " " + wtName
		}
	}
	if pane.GitUpstream {
		if pane.GitAhead > 0 {
			suffix += fmt.Sprintf(" ↑%d", pane.GitAhead)
		}
		if pane.GitBehind > 0 {
			suffix += fmt.Sprintf(" ↓%d", pane.GitBehind)
		}
	}
	if pane.GitStale {
		suffix += " ~"
	}

	avail := w - lipgloss.Width(prefix)
	if avail < 1 {
		avail = 1
	}
	nameW := avail - lipgloss.Width(suffix)
	if nameW < minGitBranchCells {
		nameW = minGitBranchCells
	}
	if nameW > avail {
		nameW = avail
	}
	name = elideMiddle(name, nameW)
	suffix = truncateCells(suffix, avail-lipgloss.Width(name))

	style := sidebarGitStyle
	if pane.GitStale {
		style = sidebarGitStaleStyle
	}
	return style.Render(padOrTrunc(prefix+name+suffix, w))
}

func sidebarHeading(title string, w int) string {
	return sidebarHeadingStyle.Render(truncateCells(title, w))
}

// sidebarTabHeading renders one tab's name above its panes. The active tab
// carries the same ▸ marker as the active project, in the same column, so the
// two read as one vocabulary rather than two conventions.
//
// White by default rather than dim: the heading shared sidebarDimStyle with an
// idle pane row, which made a tab heading indistinguishable from the panes
// under it. A user-chosen tab colour is applied as the FOREGROUND here — the
// tab bar uses the same value as a background, but a 22-column strip painting
// full-width colour blocks reads as noise rather than grouping.
//
// The 1-based ordinal matches the tab bar's "%d:%s" and the Alt+1..9 keys, and
// is placed before the name so a narrow strip elides the name and keeps the
// number.
//
// That promise is kept by the BUDGET ORDER rather than by the layout order.
// Summing marker + ordinal + name and letting one closing truncateCells
// arbitrate cut the ORDINAL instead, because the name's floor could push the
// line past w: a double-digit index spends three cells, so at w=4 "  10:name"
// came out "  10" — the colon gone, and at w=3 a digit with it. The ordinal is
// budgeted first, the marker takes what is left (levels cannot line up at a
// width where nothing else fits either), and the name — the one part a user can
// re-read from the tab bar — gives way first.
func sidebarTabHeading(name string, idx int, active bool, color string, w int) string {
	marker := "  "
	style := sidebarTabNameStyle
	if color != "" {
		style = lipgloss.NewStyle().Foreground(lipgloss.Color(color))
	}
	if active {
		marker = "▸ "
		style = sidebarActiveStyle
		if color != "" {
			style = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(color))
		}
	}
	ordinal := truncateCells(fmt.Sprintf("%d:", idx+1), w)
	marker = truncateCells(marker, w-lipgloss.Width(ordinal))
	avail := w - lipgloss.Width(marker) - lipgloss.Width(ordinal)
	// padOrTrunc, not truncateCells: every row this file hands the paint is
	// exactly w cells, and a heading that stopped short left the closing
	// .Width(w) to pad it — one more thing that pass has to get right on a row
	// whose arithmetic already has to be exact.
	return style.Render(padOrTrunc(marker+ordinal+elideMiddle(name, avail), w))
}

// projectRow renders one project's summary line: an active-project marker,
// its (already sanitized, dest-qualified) name, and a trailing badge of the
// five pane counts plus link health. name is expected pre-sanitized —
// every call site in renderSidebar routes the raw daemon-sourced value
// through sanitizeRemoteText before reaching here.
//
// The badge segments are painted in the PANE rows' state colours rather than
// inheriting the row's own, and that is what makes it a roll-up rather than
// three numbers. The whole line used to go through one style.Render, so ▲/◐/✓
// came out in the project row's grey while the panes they summarise were amber,
// blue and green two sections below — the glyph vocabulary matched and the
// colour vocabulary did not, which is the half a user reads first at 22
// columns. Same styles as paneRow, not copies of their values: a palette change
// has to move both sections together.
//
// Every badge segment begins with a SPACE, which is what satisfies
// renderStyledSegments' cluster-boundary precondition — see there for what
// breaks without it.
//
// The active project keeps sidebarActiveStyle even when offline: "where am I"
// outranks "what is wrong with it", and the link glyph beside the name says
// the rest.
func projectRow(name string, c paneStateCounts, workFrame int, link string, active bool, w int, offline *OfflineState) string {
	marker := "  "
	if active {
		marker = "▸ "
	}
	style := sidebarProjectStyle
	switch {
	case active:
		style = sidebarActiveStyle
	case offline != nil:
		style = sidebarOfflineStyle
	}
	// One allocation for the whole row, with slot 0 reserved for the head and
	// filled in below once its padding is known. lipgloss.Style is 648 bytes, so
	// a styledSegment is 664 — growing a nil slice through four appends and then
	// copying it into a fifth to prepend the head is some 8 KB of churn per
	// project per frame, on a strip repainted on every message including the
	// work-spinner tick (workSpinnerInterval).
	// 7, not 6: head + three states + both user marks + link. The two marks
	// cannot both be set on one PANE, but this counts across a whole project,
	// where one pane pinned and another marked is ordinary.
	segs := make([]styledSegment, 1, 7)
	// Badge order is urgency order, and it is the same glyph vocabulary the
	// pane rows use so the summary reads as a roll-up rather than a second
	// notation: needs you, still running, finished while you were away.
	if c.blocked > 0 {
		segs = append(segs, styledSegment{fmt.Sprintf(" %s%d", glyphBlocked, c.blocked), sidebarBlockedStyle})
	}
	// The badge's working glyph is the SAME frame the pane rows below are
	// drawing (workFrame is the counter theirs is mirrored from), so the roll-up
	// spins with the rows it rolls up rather than beside them out of phase.
	//
	// It keeps spinning for an OFFLINE destination, and that is a decision
	// rather than an oversight. resetWorkStateForReattach runs on reattach, so
	// a host that drops mid-turn leaves its panes `working` and the badge
	// asserts motion about a machine we cannot reach. Freezing it HERE is
	// cheap — `offline` is in hand — and that is exactly why it is wrong: the
	// pane rows, the tab label and the pane border have no offline state
	// threaded to them, so a lone frozen badge would put two answers about one
	// pane on one screen, which is the defect this whole change removes. The
	// honest signal for "unreachable" is the link glyph on this very row
	// (⚡/⟳) plus the reconnect banner; the spinner's job is to report the last
	// state the daemon gave us, and a still ◐ was equally stale before — just
	// stale-and-neutral rather than stale-and-confident. Revisit as ONE change
	// across all four renderers, or not at all.
	if c.working > 0 {
		segs = append(segs, styledSegment{fmt.Sprintf(" %s%d", workingGlyph(workFrame), c.working), sidebarWorkingStyle})
	}
	if c.done > 0 {
		segs = append(segs, styledSegment{fmt.Sprintf(" %s%d", glyphDone, c.done), sidebarUnseenStyle})
	}
	// The pin comes AFTER the three automatic states and before the link,
	// which is not urgency order and is not meant to be: those three rank
	// against each other because they describe what the agents are doing, and
	// this one is the user's own mark. Putting it after the three keeps their
	// ranking readable as a sequence, and it is also the one the user already
	// knows about — it is here to be found again, not to be noticed first.
	if c.pinned > 0 {
		segs = append(segs, styledSegment{fmt.Sprintf(" %s%d", glyphPinned, c.pinned), sidebarPinnedStyle})
	}
	// Last of the user's own marks, and last of the badges before the link:
	// this is the one thing on the row that asks for nothing. It is here so a
	// project accumulating disposable panes says so from the outside — which is
	// where forgotten panes actually pile up, in the project you are not in.
	if c.marked > 0 {
		segs = append(segs, styledSegment{fmt.Sprintf(" %s%d", glyphDeletion, c.marked), sidebarDeletionStyle})
	}
	if link != "" {
		segs = append(segs, styledSegment{" " + link, linkGlyphStyle(link)})
	}
	badgeW := 0
	for i := 1; i < len(segs); i++ {
		badgeW += lipgloss.Width(segs[i].text)
	}

	avail := w - lipgloss.Width(marker) - badgeW
	if avail < 1 {
		avail = 1
	}
	name = truncateCells(name, avail)

	// The gap belongs to the HEAD segment, so the badge stays flush right rather
	// than sitting behind unstyled cells. renderStyledSegments' own trailing pad
	// is then reached only when the LAST segment gives up a cell it could not
	// use — a wide glyph straddling the boundary, as in
	// projectRow("a", paneStateCounts{}, glyphLinkParked, false, 5), where " ⚡"
	// cuts to " " and leaves one cell to backfill.
	head := marker + name
	if gap := w - lipgloss.Width(head) - badgeW; gap > 0 {
		head += strings.Repeat(" ", gap)
	}
	segs[0] = styledSegment{head, style}
	return renderStyledSegments(segs, w)
}

// paneRow renders one pane's agent state: a spinning braille frame for working
// (workingGlyph, with ⋯N outstanding subagents when any are running),
// ▲ blocked-on-user (with the hook-reported
// tool name when present — never invented when blockedReason is empty), ◆
// pinned attention (outranked only by blocked/working, which then keep it as a
// trailing ◆ suffix so a pin never goes dark under a transient state), ✓ done
// and unseen, ○ idle. Every remote-sourced string (the pane's name/ID, the
// blocked reason) is sanitized here since this is a render path a
// remote-attached daemon's data reaches directly.
//
// `focused` marks the pane the user is actually typing into — with the ▸
// marker rather than a colour, because the row's colour already carries the
// pane's STATE (working, unseen, pinned) and that is the more urgent signal of
// the two.
//
// `focused` ALSO suppresses the blocked presentation, and that is the one place
// this row does more than report state. The blocked mark is deliberately NOT
// cleared when the user focuses the pane (ackFocusedPane, workstate.go, states
// why: it runs on every message including a spinner tick, so clearing there
// destroyed the mark before it could ever be seen). Keeping the state and
// dropping the glyph is what "you are looking straight at the prompt" costs —
// while tabBlocked, counts() and the attention queue keep reading the same
// blockedSince, so the tab stays amber, the project badge keeps counting it and
// the queue keeps offering it. Leaving the pane restores the ▲ by itself. An
// UNFOCUSED pane is blocked-visible always: that is the signal the whole
// feature exists for.
func paneRow(pane *PaneModel, focused bool, w int) string {
	var glyph string
	var style lipgloss.Style
	var suffix string
	switch {
	case !pane.blockedSince.IsZero() && !focused:
		glyph, style = glyphBlocked, sidebarBlockedStyle
		if pane.blockedReason != "" {
			suffix = " " + sanitizeRemoteText(pane.blockedReason)
		}
	case pane.working:
		// pane.workFrame, not the Model's counter: it is the value the tick
		// mirrors onto this pane and the one buildTopBorder renders, so the row
		// and the pane's own border always show the same frame.
		glyph, style = workingGlyph(pane.workFrame), sidebarWorkingStyle
		// "+" when the ledger overflowed: a refused start may still be live
		// with no entry to count, so the number is a floor. Marking it beats
		// printing a confidently low count — and the badge still appears when
		// the overflow is the ONLY reason the pane reads as working.
		if n := pane.outstandingSubagents(); n > 0 || pane.subagentsOverflow {
			mark := ""
			if pane.subagentsOverflow {
				mark = "+"
			}
			suffix = fmt.Sprintf(" ⋯%d%s", n, mark)
		}
	case pane.pinnedAttention:
		glyph, style = glyphPinned, sidebarPinnedStyle
	case pane.markedForDeletion:
		// Above unseen, below the live states. A finished turn the user has not
		// looked at is usually the very thing they were waiting for before
		// deciding the pane was disposable, so showing ✓ over the mark would
		// hide the decision behind its own cause.
		glyph, style = glyphDeletion, sidebarDeletionStyle
	case pane.unseen:
		glyph, style = glyphDone, sidebarUnseenStyle
	default:
		glyph, style = glyphIdle, sidebarDimStyle
	}

	// A pin outranked by a live state must still be visible — it is the mark
	// that deliberately survives focus, so losing it to a transient blocked or
	// working state would make "don't let me forget" forgettable.
	//
	// Kept as its OWN segment rather than appended to suffix, for two reasons.
	// It is painted in the pin's colour instead of inheriting the outranking
	// state's, which is the whole distinction between a mark the user set and
	// one the agent caused — a purple ◆ drawn amber reads as part of the
	// blocked state. And its width is RESERVED below, so a long blocked-reason
	// truncates into the label's budget rather than eating the pin: appending
	// it left it last in one string that gets cut from the end, so the mark
	// vanished exactly when the pane was busiest, which is when it matters.
	pinSuffix := ""
	if pane.pinnedAttention && glyph != glyphPinned {
		pinSuffix = " " + glyphPinned
	}
	// The deletion mark gets the same treatment, and it needs it more than the
	// pin does: the pane it lands on is typically one the user left running on
	// purpose, so `working` outranks it for as long as the reason to keep the
	// pane alive lasts. A mark that hid for exactly that window would be
	// invisible whenever it mattered.
	//
	// Its width is reserved alongside the pin's below, and the arithmetic
	// covers BOTH being present even though the daemon keeps them exclusive on
	// one pane. Not because a torn pair can arrive — it cannot, since both
	// daemon write paths enforce the exclusion and one PluginMu span publishes
	// both fields — but because the reservation is what makes each suffix
	// independent of the other. Sizing it for one would couple them, so the
	// next mark added here would silently take its width out of the label.
	delSuffix := ""
	if pane.markedForDeletion && glyph != glyphDeletion {
		delSuffix = " " + glyphDeletion
	}

	label := pane.Name
	if label == "" {
		label = pane.ID
	}
	label = sanitizeRemoteText(label)

	marker := "  "
	if focused {
		marker = "▸ "
	}
	prefix := marker + glyph + " "
	avail := w - lipgloss.Width(prefix) - lipgloss.Width(pinSuffix) - lipgloss.Width(delSuffix)
	if avail < 1 {
		avail = 1
	}
	// The label says WHICH pane; the suffix is secondary detail (the tool a
	// pane is blocked on, its subagent count). Subtracting the suffix first
	// inverts that: a long tool name like "AskUserQuestion" leaves two cells
	// for the name, so the row reads "▲ cl AskUserQuestion" and no longer
	// identifies the pane at all. Give the label a floor and truncate the
	// suffix into whatever is left instead.
	labelW := avail - lipgloss.Width(suffix)
	if labelW < minPaneLabelCells {
		labelW = minPaneLabelCells
	}
	if labelW > avail {
		labelW = avail
	}
	label = truncateCells(label, labelW)
	suffix = truncateCells(suffix, avail-lipgloss.Width(label))

	// TWO segments, not three: prefix+label and suffix carry the SAME style, so
	// splitting them emitted a second identical SGR pair on every pane row of
	// every frame — on a strip that repaints on every work-spinner tick (workSpinnerInterval). Only
	// the pin genuinely differs, and merging also drops one of the caller
	// obligations renderStyledSegments states.
	//
	// pinSuffix begins with a SPACE, which is what satisfies that function's
	// cluster-boundary precondition. Precisely: a space cannot change the width
	// of the cluster before it, whichever way uniseg segments the join — the
	// inflation the precondition guards against needs an Extend/ZWJ/emoji
	// codepoint, which measures 0 alone and ≥1 joined, where a space measures 1
	// either way. So the independently-measured sum stays exact even though a
	// preceding Prepend character (UAX #29 GB9b) would technically pull the
	// space into its cluster.
	return renderStyledSegments([]styledSegment{
		{prefix + label + suffix, style},
		{pinSuffix, sidebarPinnedStyle},
		{delSuffix, sidebarDeletionStyle},
	}, w)
}

// truncateCells cuts s to at most w CELLS, not runes.
//
// A rune count is not a width, and the difference is not theoretical here:
// linkGlyph's ⚡ (U+26A1) is one rune and two cells, and the CJK or emoji
// characters that reach these rows through project names, pane names and
// blockedReason are the same — all remote-sourced text that
// sanitizeRemoteText deliberately preserves non-ASCII in. Rune-counted
// padding therefore produced rows of w runes and MORE than w cells, and
// renderSidebar's closing .Width(w) WRAPS the excess onto a new painted
// line instead of truncating it. That shifts every row below it down by one
// while sidebarRowAt still maps screen row y to rows[y] — the user clicks
// project 3 and selects project 2.
//
// A wide glyph that would straddle the boundary is dropped whole (padOrTrunc
// backfills the odd cell with a space): emitting half of one is a different
// character. No ellipsis — unlike palette rows these are padded to an exact
// column count, and an ellipsis cell would come out of the content budget.
// elideMiddle shortens to w cells by removing the MIDDLE, keeping both ends.
//
// Branch names are the case this exists for. They are conventionally
// prefix/suffix pairs — feat/projects-sidebar, fix/ghost-replay — where the
// prefix says the kind of work and the tail says which work, so cutting either
// end alone throws away half the identity: a column of "feat/proje…" rows is
// indistinguishable, and a column of "…ts-sidebar" rows has lost the
// convention the user organises by. Falls back to plain truncation below the
// width where an elision would cost more than it saves.
func elideMiddle(s string, w int) string {
	if w <= 0 || lipgloss.Width(s) <= w {
		return truncateCells(s, w)
	}
	// Under this, "a…b" is mostly ellipsis — a tail-truncated string carries
	// more information than two one-character stubs.
	const minElide = 8
	if w < minElide {
		return truncateCells(s, w)
	}
	// Both halves are taken by CELL budget, not by rune count. Slicing []rune
	// with a cell-derived index is the mistake the surrounding truncateCells /
	// padOrTrunc comments exist to warn about: for wide glyphs a rune is two
	// cells, so the two halves both overrun and, once the rune count drops
	// below head+tail, they OVERLAP — the result repeats characters and is
	// about twice the requested width.
	head := (w - 1) / 2
	tail := w - 1 - head
	return truncateCells(s, head) + "…" + lastCellsToWidth(s, tail)
}

func truncateCells(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	// Cut on GRAPHEME CLUSTER boundaries, measuring each cluster whole.
	//
	// A rune is not the unit of width, because a rune can change the width of
	// the one before it. A variation selector is the case that reaches here:
	// U+FE0F measures 0 alone and U+26A0 measures 1, but the PAIR measures 2 —
	// so summing independently-measured runes admits a glyph that then
	// overflows, and this function used to return a string WIDER than the
	// budget it was handed (truncateCells("x⚠️", 2) was three cells).
	// renderSidebar's closing .Width(w) WRAPS that excess onto a new painted
	// line rather than cutting it, shifting every row below while
	// sidebarRowAt still maps screen row y to rows[y] — the user clicks
	// project 3 and selects project 2.
	//
	// Reachable without any emoji in quil's own strings: sanitizeRemoteText
	// preserves printable non-ASCII byte-identically (deliberately — it is a
	// control-character filter, not a bounding pass), so any project name,
	// pane name, branch or blocked-reason from a remote daemon can carry one.
	//
	// Clusters rather than a re-measured growing prefix, and that is a
	// performance property rather than a stylistic one: measuring
	// prefix+rune each step allocates a copy of the prefix, and a
	// ZERO-WIDTH cluster never advances the budget, so the loop cannot exit
	// early on one. A remote-supplied name that is a long run of printable
	// zero-width codepoints therefore walked the whole string reallocating
	// it — quadratic in len(s), on a render path, driven by another machine's
	// data. Per-cluster measurement is linear, and lipgloss stays the single
	// measurement authority so this can never disagree with the .Width(w)
	// that ultimately paints the row.
	var b strings.Builder
	used, state, rest := 0, -1, s
	for len(rest) > 0 {
		var cluster string
		cluster, rest, _, state = uniseg.FirstGraphemeClusterInString(rest, state)
		cw := lipgloss.Width(cluster)
		if used+cw > w {
			break
		}
		b.WriteString(cluster)
		used += cw
	}
	return b.String()
}

// padOrTrunc truncates or right-pads (with plain spaces) s to exactly w
// CELLS, so every sidebar row occupies the identical column count before
// styling is applied — see truncateCells for why cells and not runes, and
// renderSidebar's comment on why an exact count matters.
func padOrTrunc(s string, w int) string {
	s = truncateCells(s, w)
	if pad := w - lipgloss.Width(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}
