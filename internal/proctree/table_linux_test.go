//go:build linux

package proctree

import (
	"math"
	"testing"
	"time"
)

// The recorded overflow, asserted at the uptimes where it bites.
//
// The removed code computed ticks x 1e9 before dividing, which passes int64's
// ceiling at ~2.9 years of uptime. A machine up that long silently got wrapped
// start times, and both Build's splice rejection and the kill path's identity
// check are defined against those.
func TestTicksToDuration_NoOverflowAtLongUptimes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		years float64
	}{
		{"one year", 1},
		{"just under the old ceiling", 2.8},
		{"just over the old ceiling", 3},
		{"ten years", 10},
		{"fifty years", 50},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seconds := tc.years * 365 * 24 * 3600
			ticks := int64(seconds * clockTicksPerSecond)

			got := ticksToDuration(ticks)

			if got < 0 {
				t.Fatalf("ticksToDuration(%d) = %v — negative, the arithmetic wrapped", ticks, got)
			}
			want := time.Duration(seconds) * time.Second
			// Allow a tick of slack for the integer split.
			if diff := math.Abs(float64(got - want)); diff > float64(time.Second) {
				t.Errorf("ticksToDuration(%d) = %v, want ~%v", ticks, got, want)
			}
		})
	}
}

func TestTicksToDuration_KeepsSubSecondPrecision(t *testing.T) {
	// 250 ticks at 100 Hz = 2.5s. The kill path compares start times with a
	// one-second tolerance, so truncating to whole seconds here would eat most
	// of that budget before the comparison even runs.
	if got, want := ticksToDuration(250), 2500*time.Millisecond; got != want {
		t.Errorf("ticksToDuration(250) = %v, want %v", got, want)
	}
}

// A real /proc/<pid>/stat line, including the two things that break naive
// parsers: a comm containing spaces, and a comm containing parentheses.
func TestParseStat(t *testing.T) {
	boot := time.Unix(1_700_000_000, 0)

	for _, tc := range []struct {
		name     string
		stat     string
		wantName string
		wantPPID int
	}{
		{
			name:     "plain comm",
			stat:     "42 (bash) S 7 42 42 0 -1 4194304 100 0 0 0 1 2 0 0 20 0 1 0 12345 4096 100 18446744073709551615",
			wantName: "bash",
			wantPPID: 7,
		},
		{
			name:     "comm with spaces",
			stat:     "43 (Google Chrome He) S 7 43 43 0 -1 4194304 100 0 0 0 1 2 0 0 20 0 1 0 12345 4096 100 18446744073709551615",
			wantName: "Google Chrome He",
			wantPPID: 7,
		},
		{
			// Splitting on the FIRST ')' loses everything after it.
			name:     "comm with parentheses",
			stat:     "44 (weird(name)here) S 9 44 44 0 -1 4194304 100 0 0 0 1 2 0 0 20 0 1 0 12345 4096 100 18446744073709551615",
			wantName: "weird(name)here",
			wantPPID: 9,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseStat(1, tc.stat, boot)
			if !ok {
				t.Fatal("parseStat refused a well-formed line")
			}
			if got.Name != tc.wantName {
				t.Errorf("Name = %q, want %q", got.Name, tc.wantName)
			}
			if got.PPID != tc.wantPPID {
				t.Errorf("PPID = %d, want %d", got.PPID, tc.wantPPID)
			}
			if got.Start.IsZero() {
				t.Error("Start is zero; every kill on this platform would be refused")
			}
		})
	}
}

func TestParseStat_RejectsMalformed(t *testing.T) {
	boot := time.Unix(1_700_000_000, 0)
	for _, tc := range []struct{ name, stat string }{
		{"empty", ""},
		{"no closing paren", "42 (bash"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := parseStat(1, tc.stat, boot); ok {
				t.Error("accepted a malformed stat line")
			}
		})
	}
}
