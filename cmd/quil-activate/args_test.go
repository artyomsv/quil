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
			// Parsing STOPS at the URI. This case previously asserted the
			// opposite — that flags after the URI are honoured — which
			// encoded an injection as intended behaviour: %1 is the last
			// token setup writes, so anything following it came from the
			// clicked URI, and an injected --home steers a file path.
			name:   "flags after the URI are NOT honoured",
			args:   []string{"quil://activate?pid=2&pane=pane-11111111", "--home", `\\attacker\share`, "--scheme", "evil"},
			scheme: "",
			home:   "",
			raw:    "quil://activate?pid=2&pane=pane-11111111",
		},
		{
			name:   "flags before the URI are honoured, in any order",
			args:   []string{"--home", "/h", "--scheme", "quil", "quil://activate?pid=2&pane=pane-11111111"},
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
			// The whole injection shape in one case: a quote surviving into
			// the URI closes the one setup wrote around %1, and everything
			// after it arrives as argv.
			name:   "an injected --home cannot redirect the log path",
			args:   []string{"--scheme", "quil", "--home", `C:\real\.quil`, "quil://activate?pid=7&pane=pane-66666666", "--home", `\\attacker\share`},
			scheme: "quil",
			home:   `C:\real\.quil`,
			raw:    "quil://activate?pid=7&pane=pane-66666666",
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
