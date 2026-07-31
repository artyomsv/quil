package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

func TestApplyHistoryList(t *testing.T) {
	mk := func(paneID string, cursor int) Model {
		return Model{history: historyState{paneID: paneID, cursor: cursor, loading: true}}
	}
	threeEntries := []ipc.HistoryEntryMeta{{TsMs: 3}, {TsMs: 2}, {TsMs: 1}}

	t.Run("stale response ignored", func(t *testing.T) {
		m := mk("pane-a", 2)
		got := m.applyHistoryList(ipc.PaneHistoryRespPayload{PaneID: "pane-b", Entries: threeEntries})
		if len(got.history.entries) != 0 || !got.history.loading {
			t.Fatalf("stale response should be ignored: entries=%d loading=%v", len(got.history.entries), got.history.loading)
		}
	})

	t.Run("cursor clamped when list shorter", func(t *testing.T) {
		m := mk("pane-a", 5)
		got := m.applyHistoryList(ipc.PaneHistoryRespPayload{PaneID: "pane-a", Entries: threeEntries})
		if got.history.cursor != 2 {
			t.Fatalf("want cursor 2, got %d", got.history.cursor)
		}
		if len(got.history.entries) != 3 || got.history.loading {
			t.Fatalf("entries=%d loading=%v", len(got.history.entries), got.history.loading)
		}
	})

	t.Run("empty list resets cursor to zero", func(t *testing.T) {
		m := mk("pane-a", 4)
		got := m.applyHistoryList(ipc.PaneHistoryRespPayload{PaneID: "pane-a", Entries: nil})
		if got.history.cursor != 0 || got.history.loading {
			t.Fatalf("want cursor 0 loading false, got cursor=%d loading=%v", got.history.cursor, got.history.loading)
		}
	})

	t.Run("normal apply", func(t *testing.T) {
		m := mk("pane-a", 0)
		got := m.applyHistoryList(ipc.PaneHistoryRespPayload{PaneID: "pane-a", Entries: threeEntries})
		if len(got.history.entries) != 3 || got.history.loading {
			t.Fatalf("entries=%d loading=%v", len(got.history.entries), got.history.loading)
		}
	})

	// A replacement list must not leave the window scrolled past the end —
	// the entries the cursor was clamped onto would render as blank rows.
	t.Run("scroll re-synced against the new list", func(t *testing.T) {
		m := mk("pane-a", 90)
		m.width, m.height = 100, 40
		m.history.scroll = 80
		got := m.applyHistoryList(ipc.PaneHistoryRespPayload{PaneID: "pane-a", Entries: threeEntries})
		if got.history.scroll != 0 {
			t.Fatalf("want scroll 0 for a 3-entry list, got %d", got.history.scroll)
		}
	})
}

func TestHistoryWindow(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                       string
		total, cursor, scroll, vis int
		wantStart, wantEnd         int
	}{
		{"empty list", 0, 0, 0, 10, 0, 0},
		{"zero budget", 5, 0, 0, 0, 0, 0},
		{"fits entirely", 4, 2, 0, 10, 0, 4},
		{"cursor inside window", 100, 5, 0, 10, 0, 10},
		{"cursor below window scrolls down", 100, 12, 0, 10, 3, 13},
		{"cursor above window scrolls up", 100, 2, 40, 10, 2, 12},
		{"cursor at last entry", 100, 99, 0, 10, 90, 100},
		// A stored scroll can outlive the list it was computed for (the pane's
		// history was compacted, or a smaller pane's list replaced it).
		{"stale scroll past end pulled back", 12, 11, 500, 10, 2, 12},
		{"stale scroll with fitting list", 4, 0, 900, 10, 0, 4},
		{"cursor out of range clamps", 5, 99, 0, 3, 2, 5},
		{"negative cursor clamps", 5, -3, 2, 3, 0, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			start, end := historyWindow(tt.total, tt.cursor, tt.scroll, tt.vis)
			if start != tt.wantStart || end != tt.wantEnd {
				t.Fatalf("historyWindow(%d,%d,%d,%d) = (%d,%d), want (%d,%d)",
					tt.total, tt.cursor, tt.scroll, tt.vis, start, end, tt.wantStart, tt.wantEnd)
			}
			if start < 0 || end < start || end > tt.total {
				t.Fatalf("window (%d,%d) is not a valid slice of %d entries", start, end, tt.total)
			}
		})
	}
}

// keyPressFor builds the KeyPressMsg whose String() is name — the form
// handleCommandHistoryKey switches on.
func keyPressFor(name string) tea.KeyPressMsg {
	codes := map[string]rune{
		"up":     tea.KeyUp,
		"down":   tea.KeyDown,
		"home":   tea.KeyHome,
		"end":    tea.KeyEnd,
		"pgup":   tea.KeyPgUp,
		"pgdown": tea.KeyPgDown,
		"enter":  tea.KeyEnter,
		"esc":    tea.KeyEscape,
	}
	code, ok := codes[name]
	if !ok {
		panic("keyPressFor: unmapped key " + name)
	}
	return tea.KeyPressMsg{Code: code}
}

// ansiStripForTest removes styling so assertions read the visible text.
func ansiStripForTest(s string) string { return ansi.Strip(s) }

// historyModel builds a Model showing n history rows whose previews are far
// wider than any dialog box, so truncation is always exercised.
func historyModel(n, width, height int) Model {
	entries := make([]ipc.HistoryEntryMeta, n)
	for i := range entries {
		entries[i] = ipc.HistoryEntryMeta{
			TsMs:    int64(i),
			Preview: fmt.Sprintf("entry%03d ", i) + strings.Repeat("wide ", 60),
		}
	}
	return Model{
		width:  width,
		height: height,
		cfg:    config.Default(),
		history: historyState{
			paneID:    "pane-a",
			paneType:  "claude-code",
			supported: true,
			entries:   entries,
		},
	}
}

// The shipped bug: the row budget subtracted the padding but not the border, so
// every row sat two cells past the wrap limit and lipgloss reflowed it onto a
// second line. Assert against the box lipgloss actually draws.
func TestRenderCommandHistory_RowsNeverWrapTheBox(t *testing.T) {
	t.Parallel()
	for _, termW := range []int{200, 100, 60, 40} {
		t.Run(fmt.Sprintf("term%d", termW), func(t *testing.T) {
			t.Parallel()
			m := historyModel(5, termW, 40)
			boxW := historyDialogWidth
			if m.width > 2 && boxW > m.width-2 {
				boxW = m.width - 2
			}
			box := dialogBorder.Width(boxW).Render(m.renderCommandHistory())
			for i, line := range strings.Split(box, "\n") {
				if w := lipgloss.Width(line); w != boxW {
					t.Fatalf("box line %d width = %d, want %d (row wrapped or overflowed): %q",
						i, w, boxW, ansiStripForTest(line))
				}
			}
		})
	}
}

func TestRenderCommandHistory_OneRowPerEntry(t *testing.T) {
	t.Parallel()
	m := historyModel(5, 120, 40)
	body := m.renderCommandHistory()
	rows := 0
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(ansiStripForTest(line), "entry") {
			rows++
		}
	}
	if rows != 5 {
		t.Fatalf("want 5 rows for 5 entries, got %d in:\n%s", rows, body)
	}
}

// With 200 entries against a ~30-row window, everything past the fold used to
// render off-screen with no way to reach it.
func TestRenderCommandHistory_WindowFollowsCursor(t *testing.T) {
	t.Parallel()
	m := historyModel(100, 120, 30)
	m.history.cursor = 99
	m.syncHistoryScroll()
	body := ansiStripForTest(m.renderCommandHistory())

	if !strings.Contains(body, "entry099") {
		t.Fatalf("cursor entry not in the rendered window:\n%s", body)
	}
	if strings.Contains(body, "entry000") {
		t.Fatalf("window did not scroll — first entry still drawn:\n%s", body)
	}
	// The footer reports the position only when something is off-screen.
	if !strings.Contains(body, "/100") {
		t.Fatalf("want a position counter in the footer:\n%s", body)
	}
}

func TestRenderCommandHistory_NoCounterWhenListFits(t *testing.T) {
	t.Parallel()
	m := historyModel(3, 120, 40)
	if body := ansiStripForTest(m.renderCommandHistory()); strings.Contains(body, "/3") {
		t.Fatalf("counter should be omitted when the whole list fits:\n%s", body)
	}
}

func TestHandleCommandHistoryKey_Navigation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		keys       []string
		startAt    int
		wantCursor int
	}{
		{"down moves one", []string{"down"}, 0, 1},
		{"up stops at the top", []string{"up", "up"}, 0, 0},
		{"end jumps to the last entry", []string{"end"}, 0, 99},
		{"home returns to the first", []string{"end", "home"}, 0, 0},
		{"pgdown does not overshoot the end", []string{"end", "pgdown"}, 0, 99},
		{"pgup does not undershoot the start", []string{"pgup"}, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := historyModel(100, 120, 30)
			m.history.cursor = tt.startAt
			var mdl tea.Model = m
			for _, k := range tt.keys {
				mdl, _ = mdl.(Model).handleCommandHistoryKey(keyPressFor(k))
			}
			got := mdl.(Model)
			if got.history.cursor != tt.wantCursor {
				t.Fatalf("cursor = %d, want %d", got.history.cursor, tt.wantCursor)
			}
			// Whatever the cursor did, the window must contain it — that is the
			// whole point of syncing scroll on every move.
			start, end := historyWindow(len(got.history.entries), got.history.cursor,
				got.history.scroll, got.historyVisibleRows())
			if got.history.cursor < start || got.history.cursor >= end {
				t.Fatalf("cursor %d outside window [%d,%d)", got.history.cursor, start, end)
			}
		})
	}
}

func TestHandleCommandHistoryKey_PageDownAdvancesByAWindow(t *testing.T) {
	t.Parallel()
	m := historyModel(100, 120, 30)
	page := m.historyVisibleRows() - 1
	mdl, _ := m.handleCommandHistoryKey(keyPressFor("pgdown"))
	if got := mdl.(Model).history.cursor; got != page {
		t.Fatalf("pgdown cursor = %d, want %d (one window minus an overlap row)", got, page)
	}
}

func TestScrollHistoryList(t *testing.T) {
	t.Parallel()
	m := historyModel(100, 120, 30)
	step := m.cfg.UI.MouseScrollLines
	if step < 1 {
		step = 3
	}

	m.scrollHistoryList(tea.MouseWheelDown)
	if m.history.cursor != step {
		t.Fatalf("cursor = %d, want %d after one wheel-down", m.history.cursor, step)
	}
	for i := 0; i < 200; i++ {
		m.scrollHistoryList(tea.MouseWheelUp)
	}
	if m.history.cursor != 0 || m.history.scroll != 0 {
		t.Fatalf("want cursor/scroll clamped at 0, got %d/%d", m.history.cursor, m.history.scroll)
	}
	for i := 0; i < 200; i++ {
		m.scrollHistoryList(tea.MouseWheelDown)
	}
	if m.history.cursor != 99 {
		t.Fatalf("cursor = %d, want 99 (clamped at the end)", m.history.cursor)
	}
	start, end := historyWindow(100, m.history.cursor, m.history.scroll, m.historyVisibleRows())
	if m.history.cursor < start || m.history.cursor >= end {
		t.Fatalf("cursor %d outside window [%d,%d)", m.history.cursor, start, end)
	}
}

// A prompt is often one long logical line; without wrapping, everything past
// the right edge is unreachable — including unreachable to the selection the
// user opened the entry to copy.
func TestOpenReadonlyText_SoftWrapsFromTheTop(t *testing.T) {
	t.Parallel()
	m := Model{width: 100, height: 30, cfg: config.Default()}
	mdl, _ := m.openReadonlyText("Input @ now", strings.Repeat("long ", 400)+"\ntail")
	got := mdl.(Model)

	if got.dialog != dialogLogViewer {
		t.Fatalf("dialog = %v, want dialogLogViewer", got.dialog)
	}
	if got.logViewerReturn != dialogCommandHistory {
		t.Fatalf("logViewerReturn = %v, want dialogCommandHistory", got.logViewerReturn)
	}
	e := got.tomlEditor
	if e == nil {
		t.Fatal("editor not created")
	}
	if !e.SoftWrap {
		t.Error("want SoftWrap enabled for a history entry")
	}
	if !e.ReadOnly {
		t.Error("want ReadOnly")
	}
	if e.CursorRow != 0 || e.CursorCol != 0 || e.ScrollTop != 0 {
		t.Errorf("want the buffer opened at the top, got row=%d col=%d scroll=%d",
			e.CursorRow, e.CursorCol, e.ScrollTop)
	}
}

// The log viewers keep truncation: their lines are already short and the
// interesting end is the bottom.
func TestOpenLogViewer_KeepsTruncationAndTail(t *testing.T) {
	t.Parallel()
	m := Model{width: 100, height: 30, cfg: config.Default()}
	mdl, _ := m.openLogViewer("client log", "/definitely/not/a/real/log")
	e := mdl.(Model).tomlEditor
	if e == nil {
		t.Fatal("editor not created")
	}
	if e.SoftWrap {
		t.Error("log viewer should not soft-wrap")
	}
	if e.CursorRow != len(e.Lines)-1 {
		t.Errorf("want the cursor at the last line, got %d of %d", e.CursorRow, len(e.Lines))
	}
}
