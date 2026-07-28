package remoteinstall

import "testing"

func TestShellSingleQuote(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"plain path", "/usr/local/bin", `'/usr/local/bin'`},
		{"space", "/opt/my apps/quil", `'/opt/my apps/quil'`},
		{"apostrophe in home dir", "/home/o'brien/bin", `'/home/o'\''brien/bin'`},
		{"command separator", "/tmp; rm -rf /", `'/tmp; rm -rf /'`},
		{"command substitution", "/tmp/$(id -u)", `'/tmp/$(id -u)'`},
		{"backtick", "/tmp/`id`", "'/tmp/`id`'"},
		{"variable", "$HOME/bin", `'$HOME/bin'`},
		{"double quote", `/tmp/"x"`, `'/tmp/"x"'`},
		{"backslash", `C:\bin`, `'C:\bin'`},
		{"newline", "a\nb", "'a\nb'"},
		{"empty", "", `''`},
		{"only an apostrophe", "'", `''\'''`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ShellSingleQuote(tt.in); got != tt.want {
				t.Errorf("ShellSingleQuote(%q) = %s, want %s", tt.in, got, tt.want)
			}
		})
	}
}
