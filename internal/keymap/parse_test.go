package keymap

import "testing"

func TestParseSpec(t *testing.T) {
	tests := []struct {
		name, input string
		want        []string
	}{
		{"single chord", "ctrl+q", []string{"ctrl+q"}},
		{"two alternatives", "alt+f2,alt+shift+r", []string{"alt+f2", "alt+shift+r"}},
		{"alternatives spaced", "alt+f2, alt+shift+r", []string{"alt+f2", "alt+shift+r"}},
		{"trailing space tolerated", "ctrl+b ", []string{"ctrl+b"}},
		{"sequence", "ctrl+b c", []string{"ctrl+b c"}},
		{"sequence and chord", "ctrl+b c, ctrl+t", []string{"ctrl+b c", "ctrl+t"}},
		{"three steps", "ctrl+b x y", []string{"ctrl+b x y"}},
		{"canonicalizes members", "shift+ctrl+a", []string{"ctrl+shift+a"}},
		{"empty means unbound", "", nil},
		{"whitespace only unbound", "   ", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseSpec(tt.input)
			if err != nil {
				t.Fatalf("ParseSpec(%q) error: %v", tt.input, err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("= %d sequences, want %d", len(got), len(tt.want))
			}
			for i := range got {
				if s := got[i].String(); s != tt.want[i] {
					t.Errorf("sequence %d = %q, want %q", i, s, tt.want[i])
				}
			}
		})
	}
}

func TestParseSpec_Rejects(t *testing.T) {
	for _, in := range []string{"ctrl+", "a,,b", ",a", "ctrl+b  c"} {
		t.Run(in, func(t *testing.T) {
			if _, err := ParseSpec(in); err == nil {
				t.Errorf("ParseSpec(%q) = nil error, want error", in)
			}
		})
	}
}
