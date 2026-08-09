package tui

import (
	"strings"
	"testing"
)

// 208 is the repo's orange (styles.go:62). 214 is deliberately NOT reused: it is
// the blocked badge's colour, and a project can be offline AND holding a blocked
// agent at once.
func TestProjectRow_OfflineRendersOrange(t *testing.T) {
	row := projectRow("cluster-management", paneStateCounts{}, glyphLinkParked, false, 22, &OfflineState{Kind: offlineNeedsUpgrade})
	if !strings.Contains(row, "208") {
		t.Errorf("offline row carries no 208 foreground: %q", row)
	}
	live := projectRow("cluster-management", paneStateCounts{}, "", false, 22, nil)
	if strings.Contains(live, "208") {
		t.Errorf("a live row was painted orange: %q", live)
	}
}

// A kind that never enters the ladder leaves reconnectState zero, so a glyph
// read from the link alone would render nothing for the two repairable states.
func TestLinkGlyph_ReadsOfflineKindWhenTheLadderIsNotRunning(t *testing.T) {
	m := Model{}
	if got := m.linkGlyph("gpu01", &OfflineState{Kind: offlineNeedsUpgrade}); got == "" {
		t.Error("needsUpgrade rendered no glyph")
	}
	if got := m.linkGlyph("gpu01", &OfflineState{Kind: offlineNeedsInstall}); got == "" {
		t.Error("needsInstall rendered no glyph")
	}
	if got := m.linkGlyph("gpu01", nil); got != "" {
		t.Errorf("a live destination with no ladder rendered %q, want empty", got)
	}
}
