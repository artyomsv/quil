package tui

import (
	"reflect"
	"testing"
)

func cmd(label string, kw ...string) paletteCommand {
	return paletteCommand{action: palActNone, label: label, keywords: kw, enabled: true}
}

func TestFuzzyScore_Subsequence(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		query, target string
		wantMatch     bool
	}{
		{"empty query matches", "", "anything", true},
		{"exact substring", "split", "Split horizontal", true},
		{"scattered subsequence", "sph", "Split horizontal", true},
		{"case insensitive", "SPLIT", "split horizontal", true},
		{"not a subsequence", "xyz", "Split horizontal", false},
		{"subsequence across words", "sh", "Split horizontal", true},
		{"reverse order fails", "hs", "Split horizontal", false},
		{"reverse order fails short", "ts", "st", false},
	} {
		_, ok := fuzzyScore(tc.query, tc.target)
		if ok != tc.wantMatch {
			t.Errorf("%s: matched=%v, want %v", tc.name, ok, tc.wantMatch)
		}
	}
}

func TestFuzzyScore_Ranking(t *testing.T) {
	t.Parallel()
	// Consecutive/prefix beats scattered.
	pre, _ := fuzzyScore("spl", "Split pane")
	scat, _ := fuzzyScore("spl", "special loop")
	if pre <= scat {
		t.Errorf("prefix-consecutive %d should beat scattered %d", pre, scat)
	}
	// Word-boundary match scores positively.
	boundary, _ := fuzzyScore("h", "Split horizontal")
	if boundary == 0 {
		t.Error("boundary match should have positive score")
	}
}

func TestCommandScore_BestOfLabelAndKeywords(t *testing.T) {
	t.Parallel()
	c := cmd("Split horizontal", "hsplit", "wide")
	if _, ok := commandScore("hsplit", c); !ok {
		t.Error("should match on keyword the label lacks")
	}
	if _, ok := commandScore("zzz", c); ok {
		t.Error("should not match")
	}
}

func TestFilterPalette_EmptyReturnsAllInOrder(t *testing.T) {
	t.Parallel()
	in := []paletteCommand{cmd("Alpha"), cmd("Beta"), cmd("Gamma")}
	got := filterPalette("", in)
	if !reflect.DeepEqual(got, in) {
		t.Errorf("empty query must return all in registry order, got %v", got)
	}
}

func TestFilterPalette_RanksAndStableTies(t *testing.T) {
	t.Parallel()
	in := []paletteCommand{
		cmd("Close pane"),       // 'close' matches at start
		cmd("Close tab"),        // 'close' matches at start, registry order after pane
		cmd("Split horizontal"), // no match
	}
	got := filterPalette("close", in)
	if len(got) != 2 {
		t.Fatalf("want 2 matches, got %d", len(got))
	}
	if got[0].label != "Close pane" || got[1].label != "Close tab" {
		t.Errorf("stable tie order broken: %q, %q", got[0].label, got[1].label)
	}
}
