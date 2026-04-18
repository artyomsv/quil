package version

import "testing"

func TestParsed(t *testing.T) {
	cases := []struct {
		in            string
		wantMaj       int
		wantMin       int
		wantPatch     int
		wantErr       bool
	}{
		{"1.2.3", 1, 2, 3, false},
		{"v1.2.3", 1, 2, 3, false},
		{"  v1.2.3  ", 1, 2, 3, false}, // trimmed
		{"1.10.0", 1, 10, 0, false},    // multi-digit component
		{"10.0.0", 10, 0, 0, false},
		{"1.2.3-rc1", 1, 2, 3, false},     // pre-release stripped
		{"1.2.3-rc.1+build.5", 1, 2, 3, false}, // pre-release + build stripped
		{"1.2.3+build", 1, 2, 3, false},
		{"0.0.0", 0, 0, 0, false},
		// Error cases
		{"", 0, 0, 0, true},
		{"dev", 0, 0, 0, true},
		{"1.2", 0, 0, 0, true},        // too few
		{"1.2.3.4", 0, 0, 0, true},    // too many
		{"1.2.x", 0, 0, 0, true},      // non-numeric
		{"-1.0.0", 0, 0, 0, true},     // negative
		{"1..3", 0, 0, 0, true},       // empty component
		{"v", 0, 0, 0, true},          // bare v
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			maj, min, patch, err := Parsed(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got maj=%d min=%d patch=%d", maj, min, patch)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if maj != tc.wantMaj || min != tc.wantMin || patch != tc.wantPatch {
				t.Errorf("got %d.%d.%d, want %d.%d.%d", maj, min, patch, tc.wantMaj, tc.wantMin, tc.wantPatch)
			}
		})
	}
}

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b    string
		want    int
		wantErr bool
	}{
		// Equal
		{"1.2.3", "1.2.3", 0, false},
		{"v1.2.3", "1.2.3", 0, false},                 // v prefix
		{"1.2.3-rc1", "1.2.3", 0, false},              // suffix ignored
		{"1.2.3-rc1", "1.2.3-rc2", 0, false},          // both suffixes ignored — same core
		// Major wins
		{"2.0.0", "1.99.99", 1, false},
		{"1.0.0", "2.0.0", -1, false},
		// Minor wins (lexical trap: "1.10.0" > "1.9.0")
		{"1.10.0", "1.9.0", 1, false},
		{"1.9.0", "1.10.0", -1, false},
		// Patch wins
		{"1.2.10", "1.2.9", 1, false},
		{"1.2.0", "1.2.1", -1, false},
		// Errors propagate
		{"dev", "1.2.3", 0, true},
		{"1.2.3", "dev", 0, true},
		{"", "1.2.3", 0, true},
	}
	for _, tc := range cases {
		name := tc.a + "_vs_" + tc.b
		t.Run(name, func(t *testing.T) {
			got, err := Compare(tc.a, tc.b)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %d", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("Compare(%q,%q) = %d, want %d", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestSetCurrent_EmptyFallsBackToDev(t *testing.T) {
	orig := current
	t.Cleanup(func() { current = orig })

	SetCurrent("1.7.0")
	if Current() != "1.7.0" {
		t.Errorf("after SetCurrent(1.7.0): Current() = %q, want 1.7.0", Current())
	}

	SetCurrent("  v2.0.0  ") // trimmed (leading/trailing whitespace)
	if Current() != "v2.0.0" {
		t.Errorf("after whitespace SetCurrent: Current() = %q, want v2.0.0", Current())
	}

	SetCurrent("")
	if Current() != fallback {
		t.Errorf("after SetCurrent(\"\"): Current() = %q, want %q (fallback)", Current(), fallback)
	}
}

func TestIsRelease(t *testing.T) {
	orig := current
	t.Cleanup(func() { current = orig })

	SetCurrent("1.7.0")
	if !IsRelease() {
		t.Error("1.7.0 should be release")
	}

	SetCurrent("dev")
	if IsRelease() {
		t.Error("dev should NOT be release")
	}

	SetCurrent("")
	if IsRelease() {
		t.Error("empty (fallback to dev) should NOT be release")
	}

	SetCurrent("1.2.3-rc1")
	if !IsRelease() {
		t.Error("1.2.3-rc1 should be release (pre-release tag is still semver)")
	}
}
