package notify

import (
	"fmt"
	"strings"
	"testing"
)

// recordLog captures what a click handler reported. The handler is a windowless
// process with no stdout, so this sink IS its only output — which makes it the
// thing to assert on.
func recordLog() (func(string, ...any), *[]string) {
	var lines []string
	return func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	}, &lines
}

// RunActivation is what BOTH entry points call — `quil activate` and the
// windowless quil-activate helper — so it is the single place a toast click is
// interpreted. It had no test at all until this file: the seam existed and
// nothing called through it, which is the exact shape of gap this feature has
// already shipped once.
//
// Two of its three paths are platform-neutral and are asserted here. The third,
// a successful route, needs a live named pipe and is covered on Windows by
// TestListenSendActivate_RoundTrip.
func TestRunActivation_RefusesAMalformedURI(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{"not a URI at all", "definitely-not-a-uri"},
		{"wrong scheme", "evil://activate?pid=1&pane=pane-0a1b2c3d"},
		{"wrong host", "quil://execute?pid=1&pane=pane-0a1b2c3d"},
		{"pane id is not a pane id", "quil://activate?pid=1&pane=../../etc/passwd"},
		{"no pane at all", "quil://activate?pid=1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logf, lines := recordLog()

			RunActivation("quil", tt.raw, logf)

			if len(*lines) != 1 {
				t.Fatalf("logged %v, want exactly one line", *lines)
			}
			if !strings.Contains((*lines)[0], "refused") {
				t.Errorf("logged %q, want a refusal", (*lines)[0])
			}
			// A refusal must never be silent. This URI is reachable by any
			// local process, so "nothing happened and nothing was written" is
			// indistinguishable from a handler that never ran.
			if strings.Contains((*lines)[0], "routed") {
				t.Errorf("logged %q — a malformed URI must not report a route", (*lines)[0])
			}
		})
	}
}

// A toast can outlive the Quil that raised it. Clicking one then is ordinary,
// expected, and must produce a log line rather than silence — but it must NOT
// report having routed anything.
func TestRunActivation_ReportsWhenNoListenerAnswers(t *testing.T) {
	logf, lines := recordLog()

	// A well-formed URI naming a pid that has no listener. On Linux SendActivate
	// is unsupported outright; on Windows there is no pipe for this pid. Both
	// arrive at the same branch, which is why this test is not platform-gated.
	uri := BuildActivateURI("quil", 4321, "pane-0a1b2c3d")

	RunActivation("quil", uri, logf)

	if len(*lines) != 1 {
		t.Fatalf("logged %v, want exactly one line", *lines)
	}
	got := (*lines)[0]
	if !strings.Contains(got, "no listener") {
		t.Errorf("logged %q, want it to say no listener answered", got)
	}
	if !strings.Contains(got, "pane-0a1b2c3d") {
		t.Errorf("logged %q, want it to name the pane so the line is actionable", got)
	}
}

// The URI is the trust boundary, and RunActivation is where it is enforced for
// real. A pane id that ParseActivateURI would reject must never reach the send,
// whatever the rest of the URI looks like.
func TestRunActivation_NeverRoutesARejectedPaneID(t *testing.T) {
	for _, pane := range []string{
		"pane-XXXXXXXX",     // not hex
		"pane-0a1b2c3",      // too short
		"pane-0a1b2c3d9",    // too long
		"pane-0a1b2c3d\x00", // embedded NUL
		"",
	} {
		logf, lines := recordLog()

		RunActivation("quil", "quil://activate?pid=1&pane="+pane, logf)

		if len(*lines) != 1 || !strings.Contains((*lines)[0], "refused") {
			t.Errorf("pane %q: logged %v, want a single refusal", pane, *lines)
		}
	}
}
