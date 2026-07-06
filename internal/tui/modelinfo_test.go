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

func TestSyncPaneMeta_ModelFollowsSnapshot(t *testing.T) {
	t.Parallel()
	pane := &PaneModel{ID: "p1"}
	// A snapshot carrying values applies them.
	syncPaneMeta(pane, &PaneInfo{ID: "p1", Model: "claude-sonnet-5", ContextTokens: 42}, false, 0)
	if pane.Model != "claude-sonnet-5" || pane.ContextTokens != 42 {
		t.Fatalf("snapshot values not applied: model=%q tokens=%d", pane.Model, pane.ContextTokens)
	}
	// A snapshot WITHOUT the model key clears the mirror — this is how the
	// daemon-side restart-clear (handleRestartPaneReq zeroes LastModel)
	// reaches the status bar; keeping the old value would show the
	// pre-restart model until the next completed turn.
	syncPaneMeta(pane, &PaneInfo{ID: "p1", CWD: "/tmp"}, false, 0)
	if pane.Model != "" || pane.ContextTokens != 0 {
		t.Fatalf("restart-clear did not propagate: model=%q tokens=%d", pane.Model, pane.ContextTokens)
	}
}
