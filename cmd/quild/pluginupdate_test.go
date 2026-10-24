package main

import "testing"

// TestRunPluginUpdate_ArgvRefusals: the subcommand takes named flags only.
// A stray word, an unknown flag and a missing required flag each exit 1
// before any network or disk I/O (there is no target to touch here);
// --help is not a failure.
func TestRunPluginUpdate_ArgvRefusals(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"positional", []string{"--target", "x", "stray"}, 1},
		{"unknown flag", []string{"--nope"}, 1},
		{"missing flags", []string{"--target", "x"}, 1},
		{"help", []string{"--help"}, 0},
	}
	for _, tc := range cases {
		if got := runPluginUpdate(tc.args); got != tc.want {
			t.Errorf("%s: exit %d, want %d", tc.name, got, tc.want)
		}
	}
}
