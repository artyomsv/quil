package textsafe

import "testing"

func TestStrip_TableOfRunes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain ascii", "hello", "hello"},
		{"non-ascii kept", "Ünïcödé 構築 👨‍👩‍👧", "Ünïcödé 構築 👨‍👩‍👧"},
		{"tab becomes space", "a\tb", "a b"},
		{"C0 dropped", "a\x1b[31mb\x00c", "a[31mbc"},
		{"DEL dropped", "a\x7fb", "ab"},
		{"C1 CSI dropped", "a\u009bb", "ab"},
		{"bidi override dropped", "a‮b", "ab"},
		{"bidi isolate dropped", "a⁦b⁩c", "abc"},
		{"zero width joiner kept", "a‍b", "a‍b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Strip(tt.in); got != tt.want {
				t.Errorf("Strip(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestStrip_CleanInput_ReturnsSameString(t *testing.T) {
	in := "clean/path/name"
	if got := Strip(in); got != in {
		t.Errorf("Strip changed a clean string: %q", got)
	}
}

func TestHasStripped_ReportsOnlyStrippedRunes(t *testing.T) {
	if HasStripped("plain") {
		t.Error("HasStripped(plain) = true")
	}
	if !HasStripped("a‮b") {
		t.Error("HasStripped(bidi) = false")
	}
	if !HasStripped("a\tb") {
		t.Error("HasStripped(tab) = false; tab is mapped, so it counts as stripped")
	}
}
