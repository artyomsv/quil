package tui

import "testing"

func TestParsePaletteQuery(t *testing.T) {
	for _, tc := range []struct {
		in       string
		wantMode paletteMode
		wantTerm string
	}{
		{"", paletteModeCommand, ""},
		{"close", paletteModeCommand, "close"},
		{"/", paletteModeContent, ""},
		{"/refused", paletteModeContent, "refused"},
		{"/ two words", paletteModeContent, " two words"},
	} {
		mode, term := parsePaletteQuery(tc.in)
		if mode != tc.wantMode || term != tc.wantTerm {
			t.Errorf("parse(%q) = (%v,%q), want (%v,%q)", tc.in, mode, term, tc.wantMode, tc.wantTerm)
		}
	}
}
