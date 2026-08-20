package proctree

import (
	"testing"
	"time"
)

// These run on Linux CI even though the code they cover only executes on
// Darwin. That is the point: the parse is where this platform breaks, and
// behind a build tag it would never be compiled, let alone tested.

func TestParsePSLine(t *testing.T) {
	for _, tc := range []struct {
		name      string
		line      string
		wantPID   int
		wantPPID  int
		wantName  string
		wantStart bool
	}{
		{
			name:      "plain",
			line:      "  501     1 Wed Aug 20 12:00:00 2026 zsh",
			wantPID:   501,
			wantPPID:  1,
			wantName:  "zsh",
			wantStart: true,
		},
		{
			// The bug the removed code shipped: taking the first field of a
			// space-split comm truncated any name containing a space.
			name:      "comm with spaces",
			line:      "  777   501 Wed Aug 20 09:15:30 2026 Google Chrome Helper",
			wantPID:   777,
			wantPPID:  501,
			wantName:  "Google Chrome Helper",
			wantStart: true,
		},
		{
			name:      "path-like comm keeps its whole value",
			line:      "  888   501 Wed Aug 20 09:15:30 2026 /usr/libexec/some daemon",
			wantPID:   888,
			wantPPID:  501,
			wantName:  "/usr/libexec/some daemon",
			wantStart: true,
		},
		{
			name:      "single digit day",
			line:      "  901     1 Sun Aug 3 07:05:09 2026 launchd",
			wantPID:   901,
			wantPPID:  1,
			wantName:  "launchd",
			wantStart: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parsePSLine(tc.line)
			if !ok {
				t.Fatal("parsePSLine refused a well-formed line")
			}
			if got.PID != tc.wantPID || got.PPID != tc.wantPPID {
				t.Errorf("pid/ppid = %d/%d, want %d/%d", got.PID, got.PPID, tc.wantPID, tc.wantPPID)
			}
			if got.Name != tc.wantName {
				t.Errorf("Name = %q, want %q", got.Name, tc.wantName)
			}
			// The load-bearing assertion for this platform: without a start
			// time every kill is refused and every splice check is skipped.
			if got.Start.IsZero() == tc.wantStart {
				t.Errorf("Start zero = %v, want zero = %v — a zero Start here "+
					"disables both the splice rejection and the whole kill path "+
					"on Darwin", got.Start.IsZero(), !tc.wantStart)
			}
		})
	}
}

func TestParsePSLine_Rejects(t *testing.T) {
	for _, tc := range []struct{ name, line string }{
		{"empty", ""},
		{"header row", "  PID  PPID STARTED COMMAND"},
		{"too few fields", "  501     1 Wed Aug 20"},
		{"non-numeric pid", "  abc     1 Wed Aug 20 12:00:00 2026 zsh"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := parsePSLine(tc.line); ok {
				t.Errorf("accepted %q", tc.line)
			}
		})
	}
}

// An unparseable timestamp must leave Start ZERO rather than defaulting to
// something plausible — zero means "unknown" and every consumer handles it.
func TestParsePSLine_BadTimestampLeavesStartZero(t *testing.T) {
	got, ok := parsePSLine("  501     1 Xxx Yyy 99 99:99:99 abcd zsh")
	if !ok {
		t.Fatal("line should still parse for pid/ppid/comm")
	}
	if !got.Start.IsZero() {
		t.Errorf("Start = %v, want zero for an unparseable timestamp", got.Start)
	}
	if got.Name != "zsh" {
		t.Errorf("Name = %q, want zsh", got.Name)
	}
}

func TestParsePSTable_SkipsJunkKeepsRest(t *testing.T) {
	out := "  PID  PPID STARTED COMMAND\n" +
		"  501     1 Wed Aug 20 12:00:00 2026 zsh\n" +
		"\n" +
		"  502   501 Wed Aug 20 12:00:05 2026 node\n"

	got := parsePSTable(out)
	if len(got) != 2 {
		t.Fatalf("parsed %d entries, want 2: %+v", len(got), got)
	}
	if got[0].PID != 501 || got[1].PID != 502 {
		t.Errorf("pids = %d, %d, want 501, 502", got[0].PID, got[1].PID)
	}
}

func TestParsePSTable_FeedsBuild(t *testing.T) {
	// End to end through the pure path: ps output -> table -> tree.
	out := "  501     1 Wed Aug 20 12:00:00 2026 zsh\n" +
		"  502   501 Wed Aug 20 12:00:05 2026 node\n" +
		"  503   502 Wed Aug 20 12:00:06 2026 esbuild\n"

	trees := Build(parsePSTable(out), []int{501})
	root := trees[501]
	if root == nil {
		t.Fatal("no tree built from ps output")
	}
	if n := Find(root, 503); n == nil || n.Depth != 3 {
		t.Errorf("esbuild node = %+v, want depth 3", n)
	}
}

func TestParsePSPercent(t *testing.T) {
	got := parsePSPercent("  501   12.5\n  502    0.0\n  503   -1\n  bad  x\n")

	if got[501] != 12.5 {
		t.Errorf("501 = %v, want 12.5", got[501])
	}
	if v, ok := got[502]; !ok || v != 0 {
		t.Errorf("502 = %v present=%v, want a real 0", v, ok)
	}
	// A negative pcpu means ps had no answer; it must not become a percentage.
	if _, ok := got[503]; ok {
		t.Error("negative pcpu was accepted")
	}
}

func TestPSLStartLayout_RoundTrips(t *testing.T) {
	// Guards the layout constant itself: if it drifts from what ps emits, every
	// Darwin start time silently becomes zero and the platform loses its kill
	// path without any test failing elsewhere.
	want := time.Date(2026, 8, 20, 12, 0, 0, 0, time.Local)
	got, err := time.ParseInLocation(psLStartLayout, want.Format(psLStartLayout), time.Local)
	if err != nil {
		t.Fatalf("layout does not round-trip: %v", err)
	}
	if !got.Equal(want) {
		t.Errorf("round trip = %v, want %v", got, want)
	}
}

// parsePSStart is the whole Darwin kill path's identity check, and it has its
// OWN field arithmetic: `ps -o lstart= -p <pid>` yields the five lstart fields
// with nothing before them, where parsePSLine skips a pid and a ppid first.
// Sharing the layout constant does not make the offsets shared, and mutating
// this function to always fail left the package green.
func TestParsePSStart(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  string
		ok   bool
	}{
		{"plain", "Wed Aug 20 12:00:00 2026", true},
		{"leading and trailing space", "   Wed Aug 20 12:00:00 2026\n", true},
		{"single digit day", "Sun Aug  3 07:05:09 2026", true},
		{"too few fields", "Wed Aug 20", false},
		{"empty", "", false},
		{"unparseable", "Xxx Yyy 99 99:99:99 abcd", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parsePSStart(tc.out)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if tc.ok && got.IsZero() {
				t.Error("reported success with a zero time; every Darwin kill " +
					"refuses an unknown start, so this would disable the platform")
			}
			if !tc.ok && !got.IsZero() {
				t.Errorf("reported failure but returned %v", got)
			}
		})
	}
}

// The two ps parsers must agree on the same instant, or a kill validated
// against one and signalled against the other refuses every time.
func TestParsePSStart_AgreesWithParsePSLine(t *testing.T) {
	stamp := "Wed Aug 20 12:00:00 2026"

	fromLine, ok := parsePSLine("  501     1 " + stamp + " zsh")
	if !ok {
		t.Fatal("parsePSLine refused a well-formed line")
	}
	fromStart, ok := parsePSStart(stamp)
	if !ok {
		t.Fatal("parsePSStart refused the same timestamp")
	}
	if !fromLine.Start.Equal(fromStart) {
		t.Errorf("parsers disagree: line=%v start=%v — the kill path validates "+
			"with one and verifies with the other", fromLine.Start, fromStart)
	}
}
