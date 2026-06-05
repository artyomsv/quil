package tui

import (
	"reflect"
	"testing"
)

func TestKbMatches(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		key        string
		configured string
		want       bool
	}{
		{"single binding exact match", "alt+f2", "alt+f2", true},
		{"single binding mismatch", "alt+f2", "alt+f3", false},
		{"empty key", "", "alt+f2", false},
		{"empty configured", "alt+f2", "", false},
		{"both empty", "", "", false},
		{"multi binding first match", "alt+f2", "alt+f2,alt+shift+r", true},
		{"multi binding second match", "alt+shift+r", "alt+f2,alt+shift+r", true},
		{"multi binding none match", "alt+q", "alt+f2,alt+shift+r", false},
		{"multi binding with spaces", "alt+shift+r", "alt+f2, alt+shift+r", true},
		{"trailing comma tolerated", "alt+f2", "alt+f2,", true},
		{"leading comma tolerated", "alt+f2", ",alt+f2", true},
		{"only comma never matches", "alt+f2", ",", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := kbMatches(tc.key, tc.configured)
			if got != tc.want {
				t.Errorf("kbMatches(%q, %q) = %v, want %v", tc.key, tc.configured, got, tc.want)
			}
		})
	}
}

func TestKbBindings(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		configured string
		want       []string
	}{
		{"empty returns nil", "", nil},
		{"single binding", "alt+f2", []string{"alt+f2"}},
		{"two bindings", "alt+f2,alt+shift+r", []string{"alt+f2", "alt+shift+r"}},
		{"three bindings with spaces", "a, b ,  c", []string{"a", "b", "c"}},
		{"trailing comma dropped", "alt+f2,", []string{"alt+f2"}},
		{"empty entries dropped", ",,alt+f2,,", []string{"alt+f2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := kbBindings(tc.configured)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("kbBindings(%q) = %v, want %v", tc.configured, got, tc.want)
			}
		})
	}
}

func TestKbDisplay(t *testing.T) {
	t.Parallel()
	cases := []struct {
		configured string
		want       string
	}{
		{"", ""},
		{"alt+f2", "alt+f2"},
		{"alt+f2,alt+shift+r", "alt+f2 / alt+shift+r"},
		{"a, b, c", "a / b / c"},
	}
	for _, tc := range cases {
		t.Run(tc.configured, func(t *testing.T) {
			t.Parallel()
			got := kbDisplay(tc.configured)
			if got != tc.want {
				t.Errorf("kbDisplay(%q) = %q, want %q", tc.configured, got, tc.want)
			}
		})
	}
}
