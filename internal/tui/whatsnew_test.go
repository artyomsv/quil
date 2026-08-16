package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/artyomsv/quil/internal/changelog"
	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/plugin"
	"github.com/artyomsv/quil/internal/update"
)

// No test in this file may call t.Parallel: several mutate QUIL_HOME-derived
// marker files, and the What's New state lives on a shared Model.

func testWindow(fixes int) changelog.Window {
	entries := []changelog.Entry{
		{Kind: changelog.KindAdded, Text: "Keybindings are now fully remappable"},
		{Kind: changelog.KindChanged, Text: "F1 → Shortcuts is derived from your keymap"},
		{Kind: changelog.KindRemoved, Text: "The legacy migration prompt is gone"},
		{Kind: changelog.KindSecurity, Text: "Tokens are no longer written to the MCP log"},
	}
	for i := 0; i < fixes; i++ {
		entries = append(entries, changelog.Entry{
			Kind: changelog.KindFixed,
			Text: fmt.Sprintf("Fix number %d", i),
		})
	}
	return changelog.Window{
		From: "1.48.0", To: "1.60.0", Total: 12,
		Releases: []changelog.Release{
			{Version: "1.60.0", Date: "2026-08-15", Entries: entries},
		},
	}
}

func whatsNewModel(w changelog.Window) Model {
	m := Model{cfg: config.Default(), version: "1.60.0", lastWidth: 100, lastHeight: 40}
	m.openWhatsNew(w)
	return m
}

func TestSplitEntries_FoldsRemovedAndDeprecatedIntoChanged(t *testing.T) {
	added, changed, security, fixed := splitEntries(testWindow(3))
	if len(added) != 1 {
		t.Errorf("added = %v, want 1", added)
	}
	if len(changed) != 2 {
		t.Errorf("changed = %v, want 2 (Changed + Removed folded)", changed)
	}
	if len(security) != 1 {
		t.Errorf("security = %v, want 1", security)
	}
	if len(fixed) != 3 {
		t.Errorf("fixed = %v, want 3", fixed)
	}
}

func TestRenderWhatsNew_ShowsBothVersionsAndTheReleaseCount(t *testing.T) {
	out := whatsNewModel(testWindow(23)).renderWhatsNewDialog()
	for _, want := range []string{"v1.48.0", "v1.60.0", "12"} {
		if !strings.Contains(out, want) {
			t.Errorf("render does not contain %q:\n%s", want, out)
		}
	}
}

func TestRenderWhatsNew_CollapsesManyFixesAndNeverCollapsesSecurity(t *testing.T) {
	out := whatsNewModel(testWindow(23)).renderWhatsNewDialog()
	if !strings.Contains(out, "23 fixes") {
		t.Errorf("many fixes must collapse to a count:\n%s", out)
	}
	if strings.Contains(out, "Fix number 0") {
		t.Errorf("collapsed fixes must not be listed:\n%s", out)
	}
	if !strings.Contains(out, "Tokens are no longer written to the MCP log") {
		t.Errorf("security must never collapse:\n%s", out)
	}
}

func TestRenderWhatsNew_AutoExpandsFiveOrFewerFixes(t *testing.T) {
	out := whatsNewModel(testWindow(4)).renderWhatsNewDialog()
	if !strings.Contains(out, "Fix number 0") {
		t.Errorf("four fixes must render expanded:\n%s", out)
	}
	if strings.Contains(out, "4 fixes") {
		t.Errorf("an auto-expanded list must not also show the count row:\n%s", out)
	}
}

// A one-release window built by the F1 path has no From version.
func TestRenderWhatsNew_SingleReleaseWindowOmitsTheArrowAndCount(t *testing.T) {
	w := testWindow(2)
	w.From, w.Total = "", 1
	out := whatsNewModel(w).renderWhatsNewDialog()
	if strings.Contains(out, "→ v") {
		t.Errorf("a window with no From must not render an upgrade arrow:\n%s", out)
	}
	if !strings.Contains(out, "v1.60.0") {
		t.Errorf("render does not name the release:\n%s", out)
	}
}

// Driven through Update, not by calling the handler: a direct-call test can
// pass against code the call site makes unreachable.
func TestWhatsNew_RightExpandsAndLeftCollapsesThroughUpdate(t *testing.T) {
	m := whatsNewModel(testWindow(23))
	got, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	expanded := got.(Model)
	if !expanded.whatsNewExpanded {
		t.Fatal("right did not expand the fixes list")
	}
	if !strings.Contains(expanded.renderWhatsNewDialog(), "Fix number 0") {
		t.Error("expanded render still hides the fixes")
	}
	got, _ = expanded.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if got.(Model).whatsNewExpanded {
		t.Error("left did not collapse the fixes list")
	}
}

func TestWhatsNew_EnterAndEscBothDismissThroughUpdate(t *testing.T) {
	for _, tc := range []struct {
		name string
		code rune
	}{
		{"enter", tea.KeyEnter},
		{"esc", tea.KeyEscape},
	} {
		m := whatsNewModel(testWindow(23))
		got, _ := m.Update(tea.KeyPressMsg{Code: tc.code})
		if got.(Model).dialog != dialogNone {
			t.Errorf("%s did not close the dialog", tc.name)
		}
	}
}

func TestRenderWhatsNew_NoRowExceedsTheWidthOnANarrowTerminal(t *testing.T) {
	m := whatsNewModel(testWindow(2))
	m.lastWidth = 60
	width := whatsNewWidth(m.lastWidth)
	for _, line := range strings.Split(m.renderWhatsNewDialog(), "\n") {
		if w := lipgloss.Width(line); w > width {
			t.Errorf("row %q is %d cells wide, limit %d", line, w, width)
		}
	}
}

func TestWhatsNewWidth_ClampsToTheTerminal(t *testing.T) {
	if got := whatsNewWidth(200); got != whatsNewMaxWidth {
		t.Errorf("whatsNewWidth(200) = %d, want %d", got, whatsNewMaxWidth)
	}
	if got := whatsNewWidth(50); got != 46 {
		t.Errorf("whatsNewWidth(50) = %d, want 46", got)
	}
	// NewModel builds the dialog before any WindowSizeMsg arrives.
	if got := whatsNewWidth(0); got != whatsNewMaxWidth {
		t.Errorf("whatsNewWidth(0) = %d, want %d (unknown size falls back)", got, whatsNewMaxWidth)
	}
}

func TestResolveWhatsNew_FirstRunShowsNothingButRecordsTheVersion(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	if w := resolveWhatsNew("1.60.0"); w != nil {
		t.Errorf("first run returned a window: %+v", w)
	}
	if got := update.LoadLastRunVersion(config.LastRunPath()); got != "1.60.0" {
		t.Errorf("marker = %q, want 1.60.0 — a first run must still record", got)
	}
}

func TestResolveWhatsNew_SameVersionShowsNothing(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	if err := update.SaveLastRunVersion(config.LastRunPath(), "1.60.0"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if w := resolveWhatsNew("1.60.0"); w != nil {
		t.Errorf("unchanged version returned a window: %+v", w)
	}
}

func TestResolveWhatsNew_DowngradeShowsNothingAndRecords(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	if err := update.SaveLastRunVersion(config.LastRunPath(), "1.60.0"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if w := resolveWhatsNew("1.59.0"); w != nil {
		t.Errorf("downgrade returned a window: %+v", w)
	}
	if got := update.LoadLastRunVersion(config.LastRunPath()); got != "1.59.0" {
		t.Errorf("marker = %q, want 1.59.0", got)
	}
}

// Recording "dev" would make the next real launch look like a downgrade.
func TestResolveWhatsNew_DevBuildIsSkippedAndWritesNothing(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	if w := resolveWhatsNew("dev"); w != nil {
		t.Errorf("dev build returned a window: %+v", w)
	}
	if got := update.LoadLastRunVersion(config.LastRunPath()); got != "" {
		t.Errorf("dev build wrote the marker (%q); it must not", got)
	}
}

func TestNewModel_MigrationOutranksWhatsNew(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	stale := []plugin.StalePlugin{{Name: "old"}}
	m := NewModel(nil, config.Default(), "1.60.0", plugin.NewRegistry(), stale)
	if m.dialog == dialogWhatsNew {
		t.Error("what's-new opened while a plugin migration was pending")
	}
}

// The update notice must not consume its once-per-version marker while
// suppressed, or the offer is lost for good.
func TestMaybeShowUpdateNotice_YieldsToWhatsNewWithoutBurningItsMarker(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	m := Model{cfg: config.Default(), version: "1.60.0", dialog: dialogWhatsNew}
	m.updateInfos = map[string]*ipc.UpdateInfo{
		"": {LatestVersion: "1.61.0", InstallWritable: true},
	}
	m.maybeShowUpdateNotice("")
	if m.dialog != dialogWhatsNew {
		t.Errorf("dialog = %v, want dialogWhatsNew — the notice must yield", m.dialog)
	}
	if got := update.LoadNotifiedVersion(config.UpdateNotifiedPath()); got != "" {
		t.Errorf("the suppressed notice wrote its marker (%q); the offer is now lost", got)
	}
}

// The F1 path must work on the very release that ships this feature, when the
// embedded file holds exactly one record and there is no earlier version to
// derive a range from.
func TestLatestWindow_WorksWithASingleRecordedRelease(t *testing.T) {
	if changelog.Latest() == nil {
		t.Skip("the embedded highlights file records no releases yet")
	}
	w, ok := latestWindow()
	if !ok {
		t.Skip("the newest recorded release carries no entries")
	}
	if w.From != "" || w.Total != 1 || len(w.Releases) != 1 {
		t.Errorf("want a one-release window with no From, got %+v", w)
	}
}

func TestAboutMenu_WhatsNewRowIsLabelledCorrectly(t *testing.T) {
	m := Model{cfg: config.Default(), version: "1.60.0", dialogCursor: aboutWhatsNewIndex}
	var selected string
	for _, line := range strings.Split(m.renderAboutDialog(), "\n") {
		if strings.HasPrefix(line, "> ") {
			selected = line
		}
	}
	if !strings.Contains(selected, "What's New") {
		t.Errorf("cursor at aboutWhatsNewIndex=%d highlights %q, want the What's New row",
			aboutWhatsNewIndex, selected)
	}
}
