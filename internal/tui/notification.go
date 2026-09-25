package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/artyomsv/quil/internal/ipc"
)

// NotificationCenter manages the notification sidebar state.
//
// It stores EVERY event it is given and filters at READ time. Storing the
// filtered set instead would make turning a group back on silently useless —
// the events that arrived while it was off would already be gone — and the
// filter is a display preference, not a subscription.
type NotificationCenter struct {
	events []ipc.PaneEventPayload
	// cursor indexes visibleEvents(), NOT events. Every read path resolves
	// through the same accessor so none of them can disagree about which
	// event position N is.
	cursor int
	// scroll is the first visible LINE of the card viewport. Lines, not
	// cards: cards vary in height, so a card-indexed window cannot show a
	// partially-scrolled one.
	scroll    int
	visible   bool
	focused   bool
	width     int
	maxEvents int
	// groups is the display filter. NIL shows everything, which is what a
	// Model built directly by a test gets.
	groups eventGroupFilter
	// showAll is the sidebar's 'a' override: display every stored event
	// regardless of the configured groups. Session-only and never persisted —
	// a debugging affordance, not a setting.
	showAll bool
}

// NewNotificationCenter creates a notification center with the given sidebar width and max events.
func NewNotificationCenter(width, maxEvents int) *NotificationCenter {
	if width <= 0 {
		width = 30
	}
	if maxEvents <= 0 {
		maxEvents = 50
	}
	return &NotificationCenter{width: width, maxEvents: maxEvents}
}

// SetGroups installs the display filter. Called at construction and again
// whenever the user toggles a group in F1 -> Settings -> Notifications, so the
// change applies live — a visible control that did nothing until relaunch reads
// as a broken dialog, the same rule the Sidebar width row states.
//
// The cursor is clamped rather than reset: hiding a group must not throw away
// a selection that is still visible.
func (nc *NotificationCenter) SetGroups(f eventGroupFilter) {
	nc.preserveSelection(func() { nc.groups = f })
}

// preserveSelection runs fn and then puts the cursor back on the same LOGICAL
// event it was on, found by ID.
//
// Every mutation that can resize or reorder the visible list goes through this.
// The cursor is an INDEX into a list whose contents just changed, so clamping
// alone leaves it naming a different card — and the next Enter or `d` then acts
// on that one. Revealing a hidden group, hiding the group the cursor was in,
// pressing `a`, and a new event prepending are all that shape.
//
// When the event is gone (its group was hidden, it was dismissed) the cursor
// clamps and the selection legitimately moves — there is nothing to keep.
func (nc *NotificationCenter) preserveSelection(fn func()) {
	var id string
	if vis := nc.visibleEvents(); nc.cursor >= 0 && nc.cursor < len(vis) {
		id = vis[nc.cursor].ID
	}
	anchorID, anchorOff := nc.viewportAnchor()

	fn()

	if id != "" {
		for i, e := range nc.visibleEvents() {
			if e.ID == id {
				nc.cursor = i
				break
			}
		}
	}
	nc.clampCursor()
	nc.restoreViewportAnchor(anchorID, anchorOff)
}

// viewportAnchor records WHAT the viewport is showing, as an event id plus the
// line offset into that event's card, rather than as the bare line number.
//
// nc.scroll is an index into a list that the mutation is about to resize from
// the TOP: a new card is five lines, and inserting them above a scrolled
// viewport leaves the same number naming content five lines newer. The user is
// reading history and the text slides out from under them on every arrival.
//
// An empty id means "anchored at the newest edge" — scroll 0 follows the top,
// the same contract the cursor has.
func (nc *NotificationCenter) viewportAnchor() (id string, offset int) {
	if nc.scroll <= 0 {
		return "", 0
	}
	vis := nc.visibleEvents()
	owners := notificationLineOwners(vis)
	// The anchored line may be a separator, which belongs to no card. Walk
	// forward to the first line that has an owner and measure back to it, so
	// the offset can legitimately be negative by one.
	for i := nc.scroll; i < len(owners); i++ {
		idx := owners[i]
		if idx < 0 || idx >= len(vis) {
			continue
		}
		first := i
		for first > 0 && owners[first-1] == idx {
			first--
		}
		return vis[idx].ID, nc.scroll - first
	}
	return "", 0
}

// restoreViewportAnchor puts the recorded card back at the top of the viewport.
func (nc *NotificationCenter) restoreViewportAnchor(id string, offset int) {
	if id == "" {
		nc.scroll = 0
		return
	}
	vis := nc.visibleEvents()
	owners := notificationLineOwners(vis)
	for i, idx := range owners {
		if idx < 0 || idx >= len(vis) || vis[idx].ID != id {
			continue
		}
		if i > 0 && owners[i-1] == idx {
			continue // not the card's first line
		}
		nc.scroll = i + offset
		if nc.scroll < 0 {
			nc.scroll = 0
		}
		return
	}
	// The anchored card is gone (dismissed, or its group was just hidden).
	// Leave the offset where it is and let the next clamp bound it.
}

// visibleEvents returns the events the configured groups allow, newest first.
//
// EVERY read path resolves through this — cursor, selection, dismissal, the
// status-bar badge and the renderer — so none of them can disagree about which
// event position N is.
func (nc *NotificationCenter) visibleEvents() []ipc.PaneEventPayload {
	if nc.showAll || nc.groups == nil {
		return nc.events
	}
	out := make([]ipc.PaneEventPayload, 0, len(nc.events))
	for _, e := range nc.events {
		if nc.groups.shows(e.Type) {
			out = append(out, e)
		}
	}
	return out
}

// clampCursor keeps the cursor inside the visible list after anything that can
// shrink it: a filter change, a dismissal, an eviction at maxEvents.
func (nc *NotificationCenter) clampCursor() {
	if n := len(nc.visibleEvents()); nc.cursor >= n {
		nc.cursor = n - 1
	}
	if nc.cursor < 0 {
		nc.cursor = 0
	}
}

// AddEvent prepends an event. When an event with the same ID is already
// queued, the entry is updated in place AND moved to the front — this is
// the echo of the daemon's eventQueue.Push aggregation, where a repeat
// (PaneID, Title) event reuses the prior event's ID and bumps Data["count"].
// Without the move-to-front the sidebar would silently drop bumps and the
// user would never see the ×N count grow.
//
// Cursor invariant: the cursor follows the LOGICAL event the user is on,
// not the index. If the move-to-front shifts other events past the cursor's
// position, we rewrite cursor to point at the event with the same ID it had
// before — so a user staring at "claude-code (×3)" does not silently jump to
// a different card when "claude-code (×4)" arrives.
func (nc *NotificationCenter) AddEvent(e ipc.PaneEventPayload) {
	for i, existing := range nc.events {
		if existing.ID != e.ID {
			continue
		}
		// The aggregated event itself is allowed to move to the front — what is
		// protected is the selection of OTHER events. preserveSelection chases
		// it through the VISIBLE list, which is what the cursor indexes.
		idx := i
		nc.preserveSelection(func() {
			nc.events = append(nc.events[:idx], nc.events[idx+1:]...)
			nc.events = append([]ipc.PaneEventPayload{e}, nc.events...)
		})
		return
	}

	prepend := func() {
		nc.events = append([]ipc.PaneEventPayload{e}, nc.events...)
		nc.evictOverCap()
	}

	// Cursor 0 is the one position that follows the LIST rather than an event:
	// the legacy contract is "cursor 0 = newest", so a fresh event landing at
	// index 0 becomes the selection. That is also what a user who has not
	// navigated expects.
	//
	// Anywhere else the cursor names a card the user chose, and a visible event
	// prepending shifts every index by one — so leaving the cursor alone walks
	// the selection one card further from the one they are reading with every
	// arrival. On a busy workspace that is continuous drift.
	if nc.cursor == 0 {
		// The CURSOR follows the newest here, but the VIEWPORT must not: the
		// wheel scrolls without moving the cursor, so "cursor 0, scrolled deep
		// into history" is the ordinary state of someone reading back through
		// the list. Letting the lines insert above an unchanged offset slides
		// the text out from under them on every arrival.
		anchorID, anchorOff := nc.viewportAnchor()
		prepend()
		nc.clampCursor()
		nc.restoreViewportAnchor(anchorID, anchorOff)
		return
	}
	nc.preserveSelection(prepend)
}

// evictOverCap trims the store to maxEvents, dropping HIDDEN events first.
//
// The store is unfiltered and bounded, so hidden events compete for slots with
// the ones the user asked to see. That is not theoretical: command_complete
// carries a distinct title per command, so it never aggregates and takes a
// fresh slot every time — an ordinary shell session would evict the agent
// notifications the user actually wants, and pressing `a` would then reveal a
// history made entirely of events they chose never to see.
//
// Oldest-first within each class, so the newest hidden event still outlives the
// oldest hidden one and turning a group back on shows something recent.
func (nc *NotificationCenter) evictOverCap() {
	if len(nc.events) <= nc.maxEvents {
		return
	}
	for len(nc.events) > nc.maxEvents {
		drop := -1
		for i := len(nc.events) - 1; i >= 0; i-- {
			if nc.groups != nil && !nc.showAll && !nc.groups.shows(nc.events[i].Type) {
				drop = i
				break
			}
		}
		if drop < 0 {
			// Nothing hidden left to give up: fall back to the oldest event.
			drop = len(nc.events) - 1
		}
		nc.events = append(nc.events[:drop], nc.events[drop+1:]...)
	}
}

// DismissSelected removes the selected event and returns its ID.
//
// It resolves the cursor through visibleEvents() and then deletes BY ID from
// the stored slice. Slicing nc.events by the cursor directly — which is what
// this did before the filter existed — dismisses a different event than the one
// under the cursor as soon as anything is hidden.
func (nc *NotificationCenter) DismissSelected() string {
	vis := nc.visibleEvents()
	if nc.cursor < 0 || nc.cursor >= len(vis) {
		return ""
	}
	id := vis[nc.cursor].ID
	for i, e := range nc.events {
		if e.ID == id {
			nc.events = append(nc.events[:i], nc.events[i+1:]...)
			break
		}
	}
	nc.clampCursor()
	return id
}

// DismissByID removes the event with the given id, or every event when id is
// empty — the client-side application of the daemon's event_dismissed
// broadcast (spec §8.4), which mirrors DismissEventPayload's own "" = all
// convention. Unlike DismissSelected/DismissAll, this is driven by a REPORT
// of what was already dismissed (this client's own action, or another
// attached client's), so it must never send anything back — that would echo
// the dismissal the broadcast just delivered.
func (nc *NotificationCenter) DismissByID(id string) {
	if id == "" {
		nc.DismissAll()
		return
	}
	for i, e := range nc.events {
		if e.ID == id {
			nc.events = append(nc.events[:i], nc.events[i+1:]...)
			break
		}
	}
	nc.clampCursor()
}

// DismissAll removes all events.
//
// Every stored event, not just the visible ones: the key is documented as
// "dismiss all", the daemon-side MsgDismissEvent with an empty ID clears the
// whole queue, and leaving hidden events behind would make the client and the
// daemon disagree about what is still pending.
func (nc *NotificationCenter) DismissAll() {
	nc.events = nil
	nc.cursor = 0
	nc.scroll = 0
}

// SelectedEvent returns the currently selected event, or nil.
func (nc *NotificationCenter) SelectedEvent() *ipc.PaneEventPayload {
	vis := nc.visibleEvents()
	if nc.cursor < 0 || nc.cursor >= len(vis) {
		return nil
	}
	return &vis[nc.cursor]
}

// Count returns the number of events the user can currently see.
//
// The status-bar badge reads this, so a workspace holding nothing but hidden
// telemetry shows no badge — which is the point of the filter. Safe for the
// existing callers: all of them build a center or a Model without ever calling
// SetGroups, and a nil filter shows everything.
func (nc *NotificationCenter) Count() int {
	return len(nc.visibleEvents())
}

// HandleKey processes a key press when the sidebar is focused.
// Returns: action ("navigate", "dismiss", "dismiss_all", "unfocus", "none"),
// eventID (for dismiss), paneID (for navigate).
func (nc *NotificationCenter) HandleKey(key string) (action, eventID, paneID string) {
	switch key {
	case "up", "k":
		if nc.cursor > 0 {
			nc.cursor--
		}
		return "none", "", ""
	case "down", "j":
		if nc.cursor < len(nc.visibleEvents())-1 {
			nc.cursor++
		}
		return "none", "", ""
	case "a":
		// Reveal every stored event regardless of the configured groups, for
		// as long as the user wants it. Not persisted: F1 -> Settings ->
		// Notifications is where a lasting choice is made, and this is the
		// affordance for looking at what the filter is currently hiding.
		//
		// Through preserveSelection because the list it indexes changes size
		// under it — the card the user was reading must still be the selection
		// afterwards, or the next Enter jumps somewhere they did not choose.
		nc.preserveSelection(func() { nc.showAll = !nc.showAll })
		return "none", "", ""
	case "enter":
		if e := nc.SelectedEvent(); e != nil {
			return "navigate", e.ID, e.PaneID
		}
		return "none", "", ""
	case "d":
		id := nc.DismissSelected()
		return "dismiss", id, ""
	case "D":
		nc.DismissAll()
		return "dismiss_all", "", ""
	case "esc", "escape":
		return "unfocus", "", ""
	default:
		return "none", "", ""
	}
}

// notifyViewportOffset is the screen row of the card viewport's first line.
//
//	row 0   tab bar (never the sidebar)
//	row 1   box top border
//	row 2   " Notifications " title
//	row 3   card viewport line 0   <- nc.scroll indexes from here
//
// The arithmetic: the sidebar box is composited onto tabContent by
// overlayRight, which aligns overlay line i with tabContent line i, and
// tabContent is joined BELOW the one-row tab bar. So box line 0 (its top
// border) lands on screen row 1, and the two interior rows above the viewport
// push its first line to row 3.
//
// Named rather than spelled 3 at each site: the renderer and the mouse hit test
// must agree about it, and a literal in two places is how they drift.
const notifyViewportOffset = 3

// paneSource is what the sidebar needs to know about the pane a card came from.
//
// One lookup produces all of it, and the parts are decided together: a card
// whose pane is gone must be rendered differently AND must not offer a jump,
// and whether the tab's name identifies the pane depends on the same tab the
// label names.
type paneSource struct {
	// Name is what the card shows as its source, or "" to fall back to the
	// event's own PaneName.
	Name string
	// Label is the second line — where a click will land.
	Label string
	// Alive is false when the pane no longer exists.
	Alive bool
}

// paneLocator resolves one pane id. Supplied by the Model, which owns the
// project/tab tree; the NotificationCenter deliberately does not reach into it.
//
// A nil locator means "do not know": every pane reads as alive with no label
// and the event's own name, which is what a test that does not care wants.
type paneLocator func(paneID string) paneSource

// renderedLine is one screen line of the card viewport, tagged with the index
// (into the VISIBLE event list) of the card it belongs to. eventIdx is -1 for
// chrome — the separators between cards.
type renderedLine struct {
	text     string
	eventIdx int
}

// notificationLines renders the card viewport to a flat line list.
//
// It is the SINGLE source of truth for sidebar geometry: View slices its output
// by nc.scroll, and eventIndexAtRow looks up by index. Two independent answers
// to "which card owns screen row N" is how a click lands on the wrong card the
// first time a card changes height.
//
// Pure — no NotificationCenter receiver, no Model — so its tests assert against
// fixed expected output rather than against the other caller. A test that
// compares two callers of a shared helper is a self-comparison and stays green
// on broken geometry.
func notificationLines(events []ipc.PaneEventPayload, innerW, cursor int, focused bool, loc paneLocator) []renderedLine {
	if innerW < 5 {
		return nil
	}
	sepStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
	separator := sepStyle.Render(truncateCells(strings.Repeat("·", innerW), innerW))
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("243"))

	var out []renderedLine
	for i, e := range events {
		selected := i == cursor && focused

		src := paneSource{Alive: true}
		if loc != nil {
			src = loc(e.PaneID)
		}

		out = append(out, renderedLine{text: separator, eventIdx: -1})

		// Line 1: the card's SOURCE (severity-coloured, or grey when the pane is
		// gone) + right-aligned relative age.
		//
		// The locator's name wins when it has one: for a tab holding a single
		// pane it returns the TAB's name, which is the name the user actually
		// gave that work. An unnamed pane otherwise falls back to a truncated
		// id — "pane-fd2d33b" identifies nothing.
		name := src.Name
		if name == "" {
			name = e.PaneName
		}
		if name == "" {
			name = e.PaneID
			if len(name) > 12 {
				name = name[:12]
			}
		}
		nameStyle := severityNameStyle(e.Severity)
		if !src.Alive {
			// A card that cannot be jumped to must not wear an urgency colour.
			nameStyle = dim
		}
		if selected {
			nameStyle = nameStyle.Bold(true).Reverse(true)
		}
		// The age is fixed-cost and the name is the part that gives way, so
		// the name is budgeted against what is left AFTER the age and its
		// one-cell separator. Truncating the name to the full inner width and
		// then appending the age overflows the row by `1 + len(age)` cells for
		// any long pane name — reachable with plain ASCII, no wide glyph
		// needed.
		age := relativeTime(time.UnixMilli(e.Timestamp))
		ageW := lipgloss.Width(age)
		name = truncateCells(sanitizeRemoteText(name), innerW-ageW-1)
		gap := innerW - lipgloss.Width(name) - ageW
		if gap < 1 {
			gap = 1
		}
		out = append(out, renderedLine{
			text:     nameStyle.Render(name) + strings.Repeat(" ", gap) + dim.Render(age),
			eventIdx: i,
		})

		// Line 2: title + optional ×N aggregation badge.
		//
		// sanitizeRemoteText runs BEFORE truncation, and that order is
		// load-bearing: truncateCells cuts by display width with no idea what an escape
		// is, so sanitising afterwards would leave a cut sequence swallowing
		// the styling bytes that follow it. A title comes from a pane's own
		// child via the hook spool and reaches the terminal with no VT
		// emulator in between, so U+202E — printable, and therefore past any
		// C0-only filter — would reverse the rendered line.
		titleBody := "  " + sanitizeRemoteText(e.Title)
		if e.Data != nil {
			if n, err := strconv.Atoi(e.Data["count"]); err == nil && n > 1 {
				titleBody += "  ×" + e.Data["count"]
			}
		}
		titleText := truncateCells(titleBody, innerW)
		if selected {
			titleText = lipgloss.NewStyle().Reverse(true).Render(titleText)
		} else {
			titleText = lipgloss.NewStyle().Foreground(lipgloss.Color("250")).Render(titleText)
		}
		out = append(out, renderedLine{text: titleText, eventIdx: i})

		// Line 3: where the pane lives, so a click's destination is visible
		// before the click. A pane that is gone says so instead — the sidebar
		// carries events that outlive their pane (pane_destroyed is one), and
		// offering a jump that silently does nothing is worse than saying why.
		locText := "  " + sanitizeRemoteText(src.Label)
		if !src.Alive {
			locText = "  (closed)"
		}
		locStyle := dim
		if selected {
			locStyle = dim.Reverse(true)
		}
		out = append(out, renderedLine{
			text:     locStyle.Render(truncateCells(locText, innerW)),
			eventIdx: i,
		})

		// Line 4: excerpt — EMITTED ONLY WHEN THERE IS ONE. The old renderer
		// always emitted it, blank or not, to keep every card four lines so
		// the card-indexed pagination arithmetic stayed simple. Line-based
		// scrolling removes that constraint, and dropping the blank line is
		// most of the extra events now on screen.
		if e.Message != "" {
			preview := truncateCells("  "+sanitizeRemoteText(firstNonEmptyLine(e.Message)), innerW)
			st := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
			if selected {
				st = st.Reverse(true)
			}
			out = append(out, renderedLine{text: st.Render(preview), eventIdx: i})
		}
	}
	if len(out) > 0 {
		out = append(out, renderedLine{text: separator, eventIdx: -1})
	}
	return out
}

// cardLineCount is how many viewport lines one card occupies, INCLUDING its
// leading separator: separator + name + title + location, plus an excerpt when
// the event carries a message.
//
// It is the arithmetic model of notificationLines' loop, and the two must agree
// exactly — TestNotificationLineOwners_MatchesTheRenderer pins that against a
// fixture rather than by comparing the two functions to each other.
func cardLineCount(e ipc.PaneEventPayload) int {
	if e.Message != "" {
		return 5
	}
	return 4
}

// notificationLineOwners is notificationLines' geometry without its styling:
// one entry per viewport line, holding the index of the card that owns it, or
// -1 for a separator.
//
// It exists because three callers need only the shape — clampScroll,
// revealCursor and eventIndexAtRow — and the renderer styles every stored
// event. With max_events at 200 that was four full styling passes per frame,
// roughly 1600 lipgloss.Render calls, to put ~25 lines on screen.
func notificationLineOwners(events []ipc.PaneEventPayload) []int {
	if len(events) == 0 {
		return nil
	}
	out := make([]int, 0, len(events)*5+1)
	for i, e := range events {
		out = append(out, -1) // separator
		for n := cardLineCount(e) - 1; n > 0; n-- {
			out = append(out, i)
		}
	}
	return append(out, -1) // trailing separator
}

// notifyViewportHeight is how many card lines fit: the box interior less the
// title row and the hints row.
func notifyViewportHeight(height int) int {
	h := height - 2 /* borders */ - 2 /* title + hints */
	if h < 1 {
		h = 1
	}
	return h
}

// ScrollBy moves the card viewport by delta lines, clamped to the content.
//
// height is passed in rather than stored because the sidebar is drawn at the
// tab area's height, which changes with the terminal — a stored copy would be
// one frame stale exactly when the user resizes and scrolls together.
func (nc *NotificationCenter) ScrollBy(delta, height int) {
	nc.scroll += delta
	nc.clampScroll(height, nil)
}

// clampScroll bounds nc.scroll to the rendered content.
//
// loc may be nil: the locator changes a line's TEXT, never how many lines a
// card occupies, so the line count is the same either way.
func (nc *NotificationCenter) clampScroll(height int, loc paneLocator) {
	total := len(notificationLineOwners(nc.visibleEvents()))
	maxScroll := total - notifyViewportHeight(height)
	if maxScroll < 0 {
		maxScroll = 0
	}
	if nc.scroll > maxScroll {
		nc.scroll = maxScroll
	}
	if nc.scroll < 0 {
		nc.scroll = 0
	}
}

// eventIndexAtRow maps a screen row to the index (into visibleEvents()) of the
// card drawn there, or -1 for chrome and out-of-range rows.
func (nc *NotificationCenter) eventIndexAtRow(y, height int, loc paneLocator) int {
	// The same refusal View makes. Without it, shrinking the terminal below the
	// draw threshold leaves a non-zero scroll and an undrawn strip that still
	// resolves clicks — so a click on nothing selects, jumps, or dismisses.
	if nc.width-2 < 5 || height-2 < 3 {
		return -1
	}
	vy := y - notifyViewportOffset
	if vy < 0 || vy >= notifyViewportHeight(height) {
		return -1
	}
	owners := notificationLineOwners(nc.visibleEvents())
	idx := vy + nc.scroll
	if idx < 0 || idx >= len(owners) {
		return -1
	}
	return owners[idx]
}

// SelectIndex moves the cursor to a visible-list index and brings it into view.
func (nc *NotificationCenter) SelectIndex(i, height int) {
	if i < 0 || i >= len(nc.visibleEvents()) {
		return
	}
	nc.cursor = i
	nc.revealCursor(height)
}

// revealCursor scrolls the minimum distance needed to show the selected card
// whole.
//
// Called after keyboard navigation and after a click, never when an event
// arrives: a new event landing at index 0 must not yank a user reading history
// back to the top.
func (nc *NotificationCenter) revealCursor(height int) {
	owners := notificationLineOwners(nc.visibleEvents())
	first, last := -1, -1
	for i, owner := range owners {
		if owner != nc.cursor {
			continue
		}
		if first < 0 {
			first = i
		}
		last = i
	}
	if first < 0 {
		return
	}
	vh := notifyViewportHeight(height)
	if first < nc.scroll {
		nc.scroll = first
	} else if last >= nc.scroll+vh {
		nc.scroll = last - vh + 1
	}
	nc.clampScroll(height, nil)
}

// View renders the sidebar at the given height.
//
// loc resolves each card's project/tab label and liveness; pass nil in a test
// that does not care, and every pane then reads as alive with an empty label.
func (nc *NotificationCenter) View(height int, loc paneLocator) string {
	innerW := nc.width - 2
	innerH := height - 2
	if innerW < 5 || innerH < 3 {
		return ""
	}

	out := make([]string, 0, innerH)
	out = append(out, lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230")).
		Render(truncateCells(" Notifications ", innerW)))

	lines := notificationLines(nc.visibleEvents(), innerW, nc.cursor, nc.focused, loc)

	if len(lines) == 0 {
		// The separator is dropped at the minimum height. The title, this line,
		// the message and the hints are four rows, and innerH-1 is the budget
		// before the hints — at innerH == 3 the box would render one row taller
		// than it declares, and lipgloss does not clip.
		if innerH > 3 {
			out = append(out, lipgloss.NewStyle().Foreground(lipgloss.Color("238")).
				Render(truncateCells(strings.Repeat("·", innerW), innerW)))
		}
		// Naming the override matters only when something is actually being
		// hidden: on an unfiltered center there is nothing for it to reveal.
		empty := "No notifications"
		if !nc.showAll && nc.groups != nil && len(nc.events) > 0 {
			empty = "None shown (a: show all)"
		}
		out = append(out, lipgloss.NewStyle().Foreground(lipgloss.Color("243")).
			Render(truncateCells(empty, innerW)))
	} else {
		nc.clampScroll(height, loc)
		vh := notifyViewportHeight(height)
		for i := nc.scroll; i < nc.scroll+vh && i < len(lines); i++ {
			out = append(out, lines[i].text)
		}
	}

	for len(out) < innerH-1 {
		out = append(out, "")
	}

	hints := "^!N Focus  Click Go"
	if nc.focused {
		hints = "↑↓ ⏎go d/D a:all Esc"
	}
	if nc.showAll {
		hints = "SHOWING ALL  a:filter"
	}
	out = append(out, lipgloss.NewStyle().Foreground(lipgloss.Color("243")).
		Render(truncateCells(hints, innerW)))

	borderColor := lipgloss.Color("63")
	if nc.focused {
		borderColor = lipgloss.Color("57")
	}

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Width(nc.width).
		Height(height).
		Render(strings.Join(out, "\n"))
}

// firstNonEmptyLine returns the first non-empty trimmed line of s, or "".
// Used by the sidebar to render a one-line preview of a multi-line excerpt.
func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// truncateRunes truncates a string to maxWidth runes.
func truncateRunes(s string, maxWidth int) string {
	runes := []rune(s)
	if len(runes) <= maxWidth {
		return s
	}
	return string(runes[:maxWidth])
}

// Every line of a card is cut with sidebar.go's truncateCells, which measures
// DISPLAY CELLS on grapheme-cluster boundaries — not runes.
//
// That is a correctness requirement here, not a cosmetic one, and the project
// sidebar's own comment on that helper describes this exact failure: lipgloss
// is the sole width authority and its closing .Width() WRAPS the excess onto a
// new painted line rather than cutting it, shifting every row below while the
// hit test still maps screen row y to the logical row at y — so the user clicks
// one card and acts on another. 构建 is 2 runes and 4 cells.
//
// While this sidebar was keyboard-only an over-wide line was merely ugly. Now
// the display IS the decision, and pane names, titles and excerpts all arrive
// from a pane's own child or from a remote daemon.

// severityNameStyle returns a style for the pane name colored by severity.
func severityNameStyle(severity string) lipgloss.Style {
	switch severity {
	case "error":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("1")) // red
	case "warning":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("208")) // orange
	default:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("4")) // blue
	}
}

func relativeTime(t time.Time) string {
	d := time.Since(t)
	if d < 0 {
		return "now"
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
