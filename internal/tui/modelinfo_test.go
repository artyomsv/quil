package tui

import "testing"

func TestModelStatusSegment(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		model  string
		tokens int64
		want   string
	}{
		{"empty model", "", 500, ""},
		{"claude prefix stripped", "claude-opus-4-8", 601002, "opus-4-8 · 601k ctx"},
		{"non-claude model kept", "gpt-5.2-codex", 92938, "gpt-5.2-codex · 92k ctx"},
		{"zero tokens shows model only", "claude-sonnet-5", 0, "sonnet-5"},
		{"millions", "claude-opus-4-8", 1_200_000, "opus-4-8 · 1.2M ctx"},
		{"exact million", "claude-opus-4-8", 1_000_000, "opus-4-8 · 1M ctx"},
		{"small count", "claude-haiku-4-5", 950, "haiku-4-5 · 950 ctx"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := modelStatusSegment(tt.model, tt.tokens); got != tt.want {
				t.Errorf("modelStatusSegment(%q, %d) = %q, want %q", tt.model, tt.tokens, got, tt.want)
			}
		})
	}
}

func TestSyncPaneMeta_ModelPreservedWhenSnapshotOmitsIt(t *testing.T) {
	t.Parallel()
	pane := &PaneModel{ID: "p1", Model: "claude-opus-4-8", ContextTokens: 601002}
	// Snapshot raced between spawn and first turn: no model key broadcast.
	syncPaneMeta(pane, &PaneInfo{ID: "p1", CWD: "/tmp"})
	if pane.Model != "claude-opus-4-8" || pane.ContextTokens != 601002 {
		t.Fatalf("live-event model wiped by snapshot: model=%q tokens=%d", pane.Model, pane.ContextTokens)
	}
	// A snapshot that DOES carry values updates them.
	syncPaneMeta(pane, &PaneInfo{ID: "p1", Model: "claude-sonnet-5", ContextTokens: 42})
	if pane.Model != "claude-sonnet-5" || pane.ContextTokens != 42 {
		t.Fatalf("snapshot values not applied: model=%q tokens=%d", pane.Model, pane.ContextTokens)
	}
}
