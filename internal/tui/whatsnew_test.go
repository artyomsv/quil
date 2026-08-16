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
	m.openWhatsNew(w, dialogNone)
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

// Even handed a window, a pending migration wins — it blocks startup until
// every stale plugin is resolved.
//
// The other half of this invariant lives in cmd/quil/main.go, which only calls
// ResolveWhatsNew when no migration is pending: resolving WRITES the last-run
// marker, so resolving here and suppressing the dialog would consume the
// upgrade's highlights and never show them.
func TestNewModel_MigrationOutranksWhatsNew(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	w := testWindow(2)
	stale := []plugin.StalePlugin{{Name: "old"}}
	m := NewModel(nil, config.Default(), "1.60.0", plugin.NewRegistry(), stale, &w)
	if m.dialog == dialogWhatsNew {
		t.Error("what's-new opened while a plugin migration was pending")
	}
}

// The constructor must not touch disk: a Model built without QUIL_HOME set
// would otherwise write the developer's real ~/.quil/update/lastrun.json, and
// dev.sh test's throwaway /root hides that in Docker.
func TestNewModel_WritesNoMarker(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	w := testWindow(2)
	_ = NewModel(nil, config.Default(), "1.60.0", plugin.NewRegistry(), nil, &w)
	if got := update.LoadLastRunVersion(config.LastRunPath()); got != "" {
		t.Errorf("NewModel wrote the last-run marker (%q); resolution belongs to the caller", got)
	}
}

// A window handed to the constructor opens the dialog — the feature's primary
// path, which no test reached while resolution happened inside NewModel against
// an embedded corpus that is empty until the first release carrying headlines.
func TestNewModel_OpensTheDialogForAGivenWindow(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	w := testWindow(2)
	m := NewModel(nil, config.Default(), "1.60.0", plugin.NewRegistry(), nil, &w)
	if m.dialog != dialogWhatsNew {
		t.Fatalf("dialog = %v, want dialogWhatsNew", m.dialog)
	}
	if m.whatsNewReturn != dialogNone {
		t.Errorf("startup path must dismiss to nothing, got %v", m.whatsNewReturn)
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

// withLatestWindow swaps the F1 path's window source for one test. Until the
// first release cut after this feature lands, the embedded file holds no
// records, so without this seam only the no-op branch is reachable.
func withLatestWindow(t *testing.T, w changelog.Window, ok bool) {
	t.Helper()
	prev := latestWindow
	latestWindow = func() (changelog.Window, bool) { return w, ok }
	t.Cleanup(func() { latestWindow = prev })
}

// The F1 path must work on the very release that ships this feature, when the
// embedded file holds exactly one record and there is no earlier version to
// derive a range from.
func TestLatestWindow_BuildsAOneReleaseWindowWithNoFrom(t *testing.T) {
	w, ok := latestWindow()
	if changelog.Latest() == nil {
		if ok {
			t.Error("an empty corpus must not produce a window")
		}
		return
	}
	if !ok {
		return // newest release carried no entries; the no-op branch is correct
	}
	if w.From != "" || w.Total != 1 || len(w.Releases) != 1 {
		t.Errorf("want a one-release window with no From, got %+v", w)
	}
}

func TestAboutMenu_WhatsNewRowOpensTheDialogThroughUpdate(t *testing.T) {
	withLatestWindow(t, testWindow(2), true)
	m := Model{cfg: config.Default(), version: "1.60.0", dialog: dialogAbout,
		dialogCursor: aboutWhatsNewIndex, lastWidth: 100, lastHeight: 40}
	got, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if got.(Model).dialog != dialogWhatsNew {
		t.Errorf("dialog = %v, want dialogWhatsNew", got.(Model).dialog)
	}
}

// Before the first release carrying headlines there is nothing to show, and
// the row must leave the About menu exactly as it was rather than opening an
// empty dialog.
func TestAboutMenu_WhatsNewRowIsANoOpWhenThereIsNothingToShow(t *testing.T) {
	withLatestWindow(t, changelog.Window{}, false)
	m := Model{cfg: config.Default(), version: "1.60.0", dialog: dialogAbout,
		dialogCursor: aboutWhatsNewIndex, lastWidth: 100, lastHeight: 40}
	got, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if got.(Model).dialog != dialogAbout {
		t.Errorf("dialog = %v, want dialogAbout — the row must not open an empty dialog",
			got.(Model).dialog)
	}
}

// The disclaimer must still open when neither a migration nor a what's-new
// applies. "dev" is not parseable semver, so resolveWhatsNew declines it.
func TestNewModel_DisclaimerStillOpensWhenNothingElseApplies(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	cfg := config.Default()
	cfg.UI.ShowDisclaimer = true
	m := NewModel(nil, cfg, "dev", plugin.NewRegistry(), nil, nil)
	if m.dialog != dialogDisclaimer {
		t.Errorf("dialog = %v, want dialogDisclaimer", m.dialog)
	}
}

// The headline is authored in a PR and rendered here. The promoter's charset
// check is byte-wise, so a bidi override or a C1 introducer passes it as
// ordinary high bytes — the render must strip them, as every other
// externally-authored string in this package does.
func TestRenderWhatsNew_StripsBidiAndC1FromHeadlines(t *testing.T) {
	w := changelog.Window{
		From: "1.59.0", To: "1.60.0", Total: 1,
		Releases: []changelog.Release{{
			Version: "1.60.0",
			Entries: []changelog.Entry{
				{Kind: changelog.KindAdded, Text: "safe‮reversed"},
				{Kind: changelog.KindChanged, Text: "csi31mred"},
			},
		}},
	}
	out := whatsNewModel(w).renderWhatsNewDialog()
	for _, bad := range []string{"‮", ""} {
		if strings.Contains(out, bad) {
			t.Errorf("render leaked %q into the dialog:\n%q", bad, out)
		}
	}
	// The surrounding printable text must survive.
	if !strings.Contains(out, "safe") || !strings.Contains(out, "reversed") {
		t.Errorf("sanitizing dropped legitimate text:\n%s", out)
	}
}

// A BARE 0x9B — the 8-bit CSI introducer — is not valid UTF-8 and survives the
// promoter's byte-wise charset check. sanitizeRemoteText alone would return it
// untouched, because its ContainsFunc fast path decodes the invalid byte to
// U+FFFD and matches nothing. Only the ToValidUTF8 pass in sanitizeHeadline
// removes it; today the renderer happens to neutralise it via a []rune round
// trip, which is a dependency's behaviour rather than ours.
func TestSanitizeHeadline_DropsABareCSIByteThatIsNotValidUTF8(t *testing.T) {
	got := sanitizeHeadline("before\x9bafter")
	if strings.ContainsRune(got, 0x9b) || strings.ContainsRune(got, '�') {
		t.Errorf("sanitizeHeadline kept the bare 0x9b: %q", got)
	}
	if got != "beforeafter" {
		t.Errorf("sanitizeHeadline = %q, want %q", got, "beforeafter")
	}
}

func TestSanitizeHeadline_LeavesOrdinaryTextAlone(t *testing.T) {
	const in = "F1 → Shortcuts is derived from your keymap"
	if got := sanitizeHeadline(in); got != in {
		t.Errorf("sanitizeHeadline = %q, want it unchanged", got)
	}
}

func TestPadPair_DropsTheRightHalfWhenTheRowWouldOverflow(t *testing.T) {
	if got := padPair("left", "right", 20); lipgloss.Width(got) != 20 {
		t.Errorf("padPair width = %d, want 20 (%q)", lipgloss.Width(got), got)
	}
	// No room for both: the right half is dropped and the left is truncated to
	// the budget rather than overflowing the dialog border.
	got := padPair("a very long left half indeed", "right", 12)
	if w := lipgloss.Width(got); w > 12 {
		t.Errorf("padPair overflowed: width %d > 12 (%q)", w, got)
	}
	if strings.Contains(got, "right") {
		t.Errorf("the right half must be dropped when it does not fit: %q", got)
	}
}

// The scroll machinery only engages when the body overflows the terminal, so a
// short test model never reaches it.
func TestRenderWhatsNew_ScrollsAndShowsAPositionIndicatorWhenContentOverflows(t *testing.T) {
	m := whatsNewModel(testWindow(30))
	m.whatsNewExpanded = true
	m.lastHeight = 16
	out := m.renderWhatsNewDialog()
	if !strings.Contains(out, "↑↓ scroll") {
		t.Fatalf("overflowing content must show a position indicator:\n%s", out)
	}
	rows := len(strings.Split(out, "\n"))
	if rows > m.lastHeight-6+1 {
		t.Errorf("rendered %d rows, want at most %d (maxRows + indicator)", rows, m.lastHeight-6+1)
	}
	if !strings.Contains(out, "1/") {
		t.Errorf("indicator must start at position 1 before scrolling:\n%s", out)
	}
}

func TestWhatsNew_ScrollKeysMoveAndClampThroughUpdate(t *testing.T) {
	m := whatsNewModel(testWindow(30))
	m.whatsNewExpanded = true
	m.lastHeight = 16

	limit := m.whatsNewMaxScroll()
	if limit <= 0 {
		t.Fatalf("setup: content does not overflow, maxScroll = %d", limit)
	}

	got, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if got.(Model).whatsNewScroll != 1 {
		t.Errorf("down: scroll = %d, want 1", got.(Model).whatsNewScroll)
	}
	got, _ = got.(Model).Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if got.(Model).whatsNewScroll != 0 {
		t.Errorf("up: scroll = %d, want 0", got.(Model).whatsNewScroll)
	}
	// Already at the top: up must clamp rather than go negative, which would
	// panic the slice in applyWhatsNewScroll.
	got, _ = got.(Model).Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if got.(Model).whatsNewScroll != 0 {
		t.Errorf("up at top: scroll = %d, want 0", got.(Model).whatsNewScroll)
	}

	// The STORED offset must stop at the last real position, not keep counting.
	// applyWhatsNewScroll clamps only a local copy, so an unbounded handler
	// leaves the view pinned while the value runs away — every press in the
	// opposite direction is then dead until it unwinds.
	cur := got.(Model)
	for i := 0; i < 40; i++ {
		next, _ := cur.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		cur = next.(Model)
	}
	if cur.whatsNewScroll != limit {
		t.Errorf("40 x down: scroll = %d, want it clamped to %d", cur.whatsNewScroll, limit)
	}
	// One press back must move the view immediately.
	back, _ := cur.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if back.(Model).whatsNewScroll != limit-1 {
		t.Errorf("up after clamping: scroll = %d, want %d", back.(Model).whatsNewScroll, limit-1)
	}

	got, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if want := min(whatsNewPageRows, limit); got.(Model).whatsNewScroll != want {
		t.Errorf("pgdown: scroll = %d, want %d", got.(Model).whatsNewScroll, want)
	}
	got, _ = got.(Model).Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	if got.(Model).whatsNewScroll != 0 {
		t.Errorf("pgup: scroll = %d, want 0", got.(Model).whatsNewScroll)
	}
	// A stored scroll past the end must still render, clamped to the last page.
	far := got.(Model)
	far.whatsNewScroll = 9999
	if out := far.renderWhatsNewDialog(); !strings.Contains(out, "↑↓ scroll") {
		t.Errorf("a scroll past the end must clamp and still render:\n%s", out)
	}
}

// The F1 path returns to the About menu it was opened from; the startup path
// dismisses to the panes.
func TestWhatsNew_EscReturnsToWhicheverOpenedIt(t *testing.T) {
	withLatestWindow(t, testWindow(2), true)
	m := Model{cfg: config.Default(), version: "1.60.0", dialog: dialogAbout,
		dialogCursor: aboutWhatsNewIndex, lastWidth: 100, lastHeight: 40}
	opened, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if opened.(Model).dialog != dialogWhatsNew {
		t.Fatalf("setup: dialog = %v", opened.(Model).dialog)
	}
	closed, _ := opened.(Model).Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if closed.(Model).dialog != dialogAbout {
		t.Errorf("esc from the F1 path = %v, want dialogAbout", closed.(Model).dialog)
	}

	startup := whatsNewModel(testWindow(2))
	closed, _ = startup.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if closed.(Model).dialog != dialogNone {
		t.Errorf("esc from the startup path = %v, want dialogNone", closed.(Model).dialog)
	}
}

func TestWhatsNewWidth_FloorsOnAVeryNarrowTerminal(t *testing.T) {
	for _, tw := range []int{1, 10, 22, 23} {
		if got := whatsNewWidth(tw); got < whatsNewMinWidth {
			t.Errorf("whatsNewWidth(%d) = %d, below the floor %d", tw, got, whatsNewMinWidth)
		}
	}
	if got := whatsNewWidth(10); got != whatsNewMinWidth {
		t.Errorf("whatsNewWidth(10) = %d, want the floor %d", got, whatsNewMinWidth)
	}
}

func TestPadPair_HandlesAnEmptyRightHalf(t *testing.T) {
	got := padPair("left only", "", 20)
	if lipgloss.Width(got) != 20 {
		t.Errorf("width = %d, want 20 (%q)", lipgloss.Width(got), got)
	}
	if !strings.HasPrefix(got, "left only") {
		t.Errorf("got %q, want it to start with the left half", got)
	}
}

// Expanding and collapsing reset the scroll, or a collapsed short list renders
// from a row that no longer exists.
func TestWhatsNew_ExpandAndCollapseResetTheScroll(t *testing.T) {
	m := whatsNewModel(testWindow(30))
	m.whatsNewExpanded = true
	m.lastHeight = 16
	got, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if got.(Model).whatsNewScroll == 0 {
		t.Fatal("setup: pgdown did not move the scroll")
	}
	got, _ = got.(Model).Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if got.(Model).whatsNewScroll != 0 {
		t.Errorf("collapse: scroll = %d, want 0", got.(Model).whatsNewScroll)
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
