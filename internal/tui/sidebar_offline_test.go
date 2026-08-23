package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// 208 is the repo's orange (styles.go:62). 214 is deliberately NOT reused: it is
// the blocked badge's colour, and a project can be offline AND holding a blocked
// agent at once.
func TestProjectRow_OfflineRendersOrange(t *testing.T) {
	row := projectRow("cluster-management", paneStateCounts{}, 0, glyphLinkParked, false, 22, &OfflineState{Kind: offlineNeedsUpgrade})
	if !strings.Contains(row, "208") {
		t.Errorf("offline row carries no 208 foreground: %q", row)
	}
	live := projectRow("cluster-management", paneStateCounts{}, 0, "", false, 22, nil)
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

// The pane area for an OFFLINE project used to render the same "No tabs in X —
// Ctrl+T opens one" as an empty live one. That is the confidently-wrong answer:
// the host is up, its tabs exist, this client just refused to attach — and the
// Ctrl+T it invites cannot reach the daemon that would have to mint the tab.
func TestEmptyTabArea_OfflineProjectSaysWhyInsteadOfOfferingCtrlT(t *testing.T) {
	m := Model{
		width: 120, height: 30,
		sidebarOpen:  true,
		sidebarWidth: 22,
		projects: []*ProjectModel{{
			ID:   "proj-cluster",
			Name: "cluster-management",
			Dest: "artyom@gpu01",
			Offline: &OfflineState{
				Kind:   offlineNeedsUpgrade,
				Detail: "artyom@gpu01 runs 1.55.0, this client runs 1.63.2; run `quil remote setup artyom@gpu01`",
			},
		}},
		notifications: NewNotificationCenter(30, 50),
	}

	out := stripANSI(m.View().Content)
	if strings.Contains(out, "Ctrl+T opens one") {
		t.Errorf("an offline project still invites Ctrl+T, which cannot reach the host:\n%s", out)
	}
	if !strings.Contains(out, "1.55.0") {
		t.Errorf("the pane area does not report the version drift that blocked the attach:\n%s", out)
	}
	if !strings.Contains(out, "quil remote setup") {
		t.Errorf("the pane area names no way out:\n%s", out)
	}
	// The sidebar reserves layout width; an unwrapped message overflows the frame.
	for i, line := range strings.Split(out, "\n") {
		if got := lipgloss.Width(line); got > m.width {
			t.Errorf("line %d measured %d cells, wider than the %d-cell frame", i, got, m.width)
		}
	}
}

// A host merely unreachable is a different sentence: nothing to run, the ladder
// is already working on it.
func TestEmptyTabArea_RetryingProjectSaysReconnecting(t *testing.T) {
	m := Model{
		width: 120, height: 30,
		projects: []*ProjectModel{{
			ID: "proj-1", Name: "api", Dest: "gpu01",
			Offline: &OfflineState{Kind: offlineRetrying, Detail: "ssh: connect: no route to host"},
		}},
		notifications: NewNotificationCenter(30, 50),
	}
	out := stripANSI(m.View().Content)
	if strings.Contains(out, "Ctrl+T opens one") {
		t.Errorf("an unreachable project still invites Ctrl+T:\n%s", out)
	}
	if !strings.Contains(out, "gpu01") {
		t.Errorf("the pane area does not name the host it is waiting on:\n%s", out)
	}
}

// MEDIUM-2 from the review of PR #186. An offline row's NAME is not local text:
// it is seeded from the on-disk cache of what a remote daemon reported, bounded
// by LoadRemoteProjects at 4096 BYTES and never at display CELLS. lipgloss.Place
// does not clip — PlaceHorizontal computes `gap := width - contentWidth` and
// returns str unchanged when gap <= 0 — so an over-wide line leaves the pane
// area whole and the sidebar join then emits rows wider than the terminal.
//
// Asserted for BOTH branches: the live-project text has the identical shape and
// had the same gap before this test existed, so covering only the offline one
// would leave the sibling free to regress.
func TestEmptyTabArea_LongRemoteNameStaysInsideTheFrame(t *testing.T) {
	huge := strings.Repeat("wide-remote-project-name-", 160) // ~4000 cells
	for _, tc := range []struct {
		name    string
		offline *OfflineState
	}{
		{"offline row", &OfflineState{Kind: offlineNeedsUpgrade, Detail: "gpu01 runs 1.0.0, this client runs 2.0.0"}},
		{"live row with no tabs", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := Model{
				width: 100, height: 30,
				projects: []*ProjectModel{{
					ID: "proj-1", Name: huge, Dest: "gpu01", Offline: tc.offline,
				}},
				notifications: NewNotificationCenter(30, 50),
			}
			out := stripANSI(m.View().Content)
			// Non-vacuity: without this, a frame that never rendered the
			// message at all would satisfy every width assertion below.
			if !strings.Contains(out, "wide-remote-project-name") {
				t.Fatalf("the pane area never rendered the project name, so the "+
					"width assertions below prove nothing:\n%s", out)
			}
			for i, line := range strings.Split(out, "\n") {
				if got := lipgloss.Width(line); got > m.width {
					t.Fatalf("line %d measured %d cells against a %d-cell frame — "+
						"lipgloss.Place pads but never clips", i, got, m.width)
				}
			}
		})
	}
}

// A remote-supplied detail is capped too, and on the same budget.
func TestEmptyTabArea_LongRemoteDetailStaysInsideTheFrame(t *testing.T) {
	m := Model{
		width: 100, height: 30,
		projects: []*ProjectModel{{
			ID: "proj-1", Name: "api", Dest: "gpu01",
			Offline: &OfflineState{Kind: offlineNeedsUpgrade, Detail: strings.Repeat("A", 9000)},
		}},
		notifications: NewNotificationCenter(30, 50),
	}
	for i, line := range strings.Split(stripANSI(m.View().Content), "\n") {
		if got := lipgloss.Width(line); got > m.width {
			t.Fatalf("line %d measured %d cells against a %d-cell frame", i, got, m.width)
		}
	}
}

// ssh writes multi-line stderr, and sanitizeRemoteText drops \n as a C0 control
// with NO separator — so without firstErrLine the two sentences fuse into one
// word. reconnect.go already made this call for the same class of text.
func TestEmptyTabArea_MultiLineStderrKeepsOnlyTheFirstLine(t *testing.T) {
	m := Model{
		width: 100, height: 30,
		projects: []*ProjectModel{{
			ID: "proj-1", Name: "api", Dest: "gpu01",
			Offline: &OfflineState{
				Kind:   offlineRetrying,
				Detail: "ssh: connect to host gpu01 port 22: Connection refused\nHost key verification failed.",
			},
		}},
		notifications: NewNotificationCenter(30, 50),
	}
	out := stripANSI(m.View().Content)
	if strings.Contains(out, "refusedHost") {
		t.Errorf("two stderr lines fused into one word:\n%s", out)
	}
	if !strings.Contains(out, "Connection refused") {
		t.Errorf("the first stderr line is missing:\n%s", out)
	}
	if strings.Contains(out, "Host key verification") {
		t.Errorf("the second stderr line survived; only the first belongs on one row:\n%s", out)
	}
}

// The needsInstall arm had no coverage: a host that never had quil at all reads
// differently from one whose daemon is merely the wrong version.
func TestEmptyTabArea_NeedsInstallSaysNotInstalled(t *testing.T) {
	m := Model{
		width: 100, height: 30,
		projects: []*ProjectModel{{
			ID: "proj-1", Name: "api", Dest: "gpu01",
			Offline: &OfflineState{Kind: offlineNeedsInstall, Detail: "quil: command not found"},
		}},
		notifications: NewNotificationCenter(30, 50),
	}
	out := stripANSI(m.View().Content)
	if !strings.Contains(out, "not installed") {
		t.Errorf("a host with no quil is not told apart from a version drift:\n%s", out)
	}
	if strings.Contains(out, "different version") {
		t.Errorf("needsInstall borrowed the version-drift wording:\n%s", out)
	}
}

// An empty Detail must not leave a dangling blank line where the reason should be.
func TestEmptyTabArea_EmptyDetailAppendsNothing(t *testing.T) {
	withDetail := Model{}.offlineTabAreaMsg(&ProjectModel{
		Name: "api", Dest: "gpu01",
		Offline: &OfflineState{Kind: offlineNeedsUpgrade, Detail: "gpu01 runs 1.0.0"},
	}, 100, 30)
	without := Model{}.offlineTabAreaMsg(&ProjectModel{
		Name: "api", Dest: "gpu01",
		Offline: &OfflineState{Kind: offlineNeedsUpgrade},
	}, 100, 30)

	// head, blank, two reason lines, blank, retry hint = 6 lines / 5 newlines.
	if got := strings.Count(without, "\n"); got != 5 {
		t.Errorf("an empty detail produced %d newlines, want 5 (a 6-line block):\n%q", got, without)
	}
	if strings.HasSuffix(without, "\n") {
		t.Errorf("an empty detail left a trailing blank line: %q", without)
	}
	if strings.Count(withDetail, "\n") != 7 {
		t.Errorf("a detail should add exactly two lines: %q", withDetail)
	}
}

// PlaceVertical has the same no-clip escape hatch as PlaceHorizontal, so on a
// frame too short for six lines the DETAIL is what gets dropped — not the
// status bar the overflow would otherwise push off screen.
func TestEmptyTabArea_ShortFrameDropsTheDetail(t *testing.T) {
	p := &ProjectModel{
		Name: "api", Dest: "gpu01",
		Offline: &OfflineState{Kind: offlineNeedsUpgrade, Detail: "gpu01 runs 1.0.0, this client runs 2.0.0"},
	}
	// The optional tail goes on by priority while rows remain: the retry hint
	// (the way OUT) before the detail (merely informative). The reason itself
	// always survives, because PlaceVertical does not clip and an overflowing
	// block pushes the status bar off screen.
	for _, tc := range []struct {
		h        int
		newlines int
		what     string
	}{
		{4, 3, "reason only — neither hint nor detail fits"},
		{5, 3, "still no room for the hint's blank line plus the hint"},
		{6, 5, "hint fits, detail does not"},
		{7, 5, "still no room for the detail"},
		{8, 7, "both fit"},
		{30, 7, "everything fits"},
	} {
		if got := strings.Count(Model{}.offlineTabAreaMsg(p, 100, tc.h), "\n"); got != tc.newlines {
			t.Errorf("h=%d got %d newlines, want %d (%s):\n%q",
				tc.h, got, tc.newlines, tc.what, Model{}.offlineTabAreaMsg(p, 100, tc.h))
		}
		// Whatever was kept must fit the frame it was measured against.
		if got := strings.Count(Model{}.offlineTabAreaMsg(p, 100, tc.h), "\n") + 1; got > tc.h {
			t.Errorf("h=%d produced %d rows — PlaceVertical will not clip them", tc.h, got)
		}
	}
}

// offlineRetrying does NOT imply reconnecting. model.go:1305 parks a dest with
// no dialer and starts no ladder, while Kind stays offlineRetrying — and
// renderReconnectBanner returns "" for a link that is not active, so the pane
// area is the ONLY thing on screen. "Reconnecting…" there is the same
// confidently-wrong answer the function exists to remove, and it contradicts
// bannerCandidates' own "unreachable — stopped, r retries" for the same state.
func TestEmptyTabArea_ParkedLinkDoesNotClaimToBeReconnecting(t *testing.T) {
	newModel := func() Model {
		return Model{
			width: 100, height: 30,
			projects: []*ProjectModel{{
				ID: "proj-1", Name: "api", Dest: "gpu01",
				Offline: &OfflineState{Kind: offlineRetrying, Detail: "ssh: connect: no route to host"},
			}},
			notifications: NewNotificationCenter(30, 50),
		}
	}

	parked := newModel()
	parked.linkFor("gpu01").parked = true
	out := stripANSI(parked.View().Content)
	if strings.Contains(out, "Reconnecting…") {
		t.Errorf("a parked link claims to be reconnecting; nothing is, and nothing will be:\n%s", out)
	}
	if !strings.Contains(out, reconnectResumeKey) || !strings.Contains(out, "stopped") {
		t.Errorf("a parked link does not say it stopped or how to resume:\n%s", out)
	}

	// A link genuinely mid-ladder keeps the original wording.
	active := newModel()
	active.linkFor("gpu01").active = true
	if out := stripANSI(active.View().Content); !strings.Contains(out, "Reconnecting…") {
		t.Errorf("an active ladder no longer says it is reconnecting:\n%s", out)
	}
}

// ErrRemoteQuilMissing is literally "quil is not installed on that host", and
// cmd/quil seeds Detail from the dial error's own text — so rendering the fixed
// line AND the detail printed one sentence twice, once capitalised and once
// behind a "dial <host>:" prefix. The sentinel is the single source.
func TestEmptyTabArea_NeedsInstallDoesNotSayItTwice(t *testing.T) {
	m := Model{
		width: 100, height: 30,
		projects: []*ProjectModel{{
			ID: "proj-1", Name: "api", Dest: "gpu01",
			Offline: &OfflineState{
				Kind:   offlineNeedsInstall,
				Detail: "dial gpu01: " + ErrRemoteQuilMissing.Error(),
			},
		}},
		notifications: NewNotificationCenter(30, 50),
	}
	out := stripANSI(m.View().Content)
	if n := strings.Count(strings.ToLower(out), "not installed on that host"); n != 1 {
		t.Errorf("the same sentence renders %d times, want 1:\n%s", n, out)
	}

	// A detail that says something ELSE is still worth showing.
	m.projects[0].Offline.Detail = "ssh: permission denied (publickey)"
	if out := stripANSI(m.View().Content); !strings.Contains(out, "permission denied") {
		t.Errorf("a detail that adds information was suppressed:\n%s", out)
	}
}
