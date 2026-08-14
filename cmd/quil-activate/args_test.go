package main

import "testing"

// The arguments come from the registry command `quil notify setup` wrote, so
// these cases are the contract between setup and the handler. They are asserted
// on Linux because that is where CI runs — the whole reason parseArgs lives in
// an untagged file.
func TestParseArgs(t *testing.T) {
	tests := []struct {
		name              string
		args              []string
		scheme, home, raw string
	}{
		{
			name:   "the command setup writes",
			args:   []string{"--scheme", "quil-dev", "--home", `E:\proj\.quil`, "quil-dev://activate?pid=1&pane=pane-0a1b2c3d"},
			scheme: "quil-dev",
			home:   `E:\proj\.quil`,
			raw:    "quil-dev://activate?pid=1&pane=pane-0a1b2c3d",
		},
		{
			name:   "order is not fixed",
			args:   []string{"quil://activate?pid=2&pane=pane-11111111", "--home", "/h", "--scheme", "quil"},
			scheme: "quil",
			home:   "/h",
			raw:    "quil://activate?pid=2&pane=pane-11111111",
		},
		{
			// An older registry entry, or a future version's flag: degrade
			// rather than die. A handler that exits on an unexpected argument
			// is a click that silently does nothing.
			name:   "unknown flags are ignored",
			args:   []string{"--future-thing", "--scheme", "quil", "quil://activate?pid=3&pane=pane-22222222"},
			scheme: "quil",
			raw:    "quil://activate?pid=3&pane=pane-22222222",
		},
		{
			// Windows substitutes exactly one URI for %1; anything after it is
			// not ours to interpret.
			name: "the first bare argument wins",
			args: []string{"quil://activate?pid=4&pane=pane-33333333", "quil://activate?pid=5&pane=pane-44444444"},
			raw:  "quil://activate?pid=4&pane=pane-33333333",
		},
		{
			// A flag with nothing after it must not consume the URI or panic.
			name: "a trailing flag with no value is survivable",
			args: []string{"quil://activate?pid=6&pane=pane-55555555", "--scheme"},
			raw:  "quil://activate?pid=6&pane=pane-55555555",
		},
		{
			name: "nothing at all",
			args: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme, home, raw := parseArgs(tt.args)

			if scheme != tt.scheme {
				t.Errorf("scheme = %q, want %q", scheme, tt.scheme)
			}
			if home != tt.home {
				t.Errorf("home = %q, want %q", home, tt.home)
			}
			if raw != tt.raw {
				t.Errorf("raw = %q, want %q", raw, tt.raw)
			}
		})
	}
}

// A flag value that looks like a URI must be treated as the flag's value, not
// adopted as the URI — otherwise a malformed registry entry could route a click
// somewhere the scheme never named.
func TestParseArgs_FlagValueIsNotMistakenForTheURI(t *testing.T) {
	_, home, raw := parseArgs([]string{"--home", "quil://activate?pid=9&pane=pane-99999999"})

	if home != "quil://activate?pid=9&pane=pane-99999999" {
		t.Errorf("home = %q, want the value that followed --home", home)
	}
	if raw != "" {
		t.Errorf("raw = %q, want empty — no bare argument was given", raw)
	}
}
