package tui

import (
	"testing"

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
}
