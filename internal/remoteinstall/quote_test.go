package remoteinstall

import "testing"

func TestQuoteCommand(t *testing.T) {
	cases := []struct {
		shell, bin string
		args       []string
		want       string
	}{
		{ShellPOSIX, "/home/a/.local/bin/quil", []string{"--stdio"}, "'/home/a/.local/bin/quil' --stdio"},
		{ShellPOSIX, "/home/o'brien/bin/quil", []string{"--stdio"}, `'/home/o'\''brien/bin/quil' --stdio`},
		{ShellCmd, `C:\Users\Jürgen Müller\AppData\Local\Programs\quil\quil.exe`, []string{"--stdio"},
			`"C:\Users\Jürgen Müller\AppData\Local\Programs\quil\quil.exe" --stdio`},
		{ShellCmd, `C:\q\quil.exe`, []string{"daemon", "stop"}, `"C:\q\quil.exe" daemon stop`},
		{ShellPowerShell, `C:\Program Files\quil\quil.exe`, []string{"--stdio"}, `& 'C:\Program Files\quil\quil.exe' --stdio`},
	}
	for _, c := range cases {
		got, err := QuoteCommand(c.shell, c.bin, c.args...)
		if err != nil || got != c.want {
			t.Errorf("QuoteCommand(%q, %q) = %q, %v; want %q", c.shell, c.bin, got, err, c.want)
		}
	}
}

// Byte-identical to the command every existing POSIX record produced.
func TestQuoteCommand_POSIXMatchesLegacyShape(t *testing.T) {
	bin := "/opt/q uil/quil"
	got, _ := QuoteCommand(ShellPOSIX, bin, "--stdio")
	if want := ShellSingleQuote(bin) + " --stdio"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestQuoteCommand_WindowsRefusals(t *testing.T) {
	bad := []string{
		`\\srv\share\quil.exe`, `quil.exe`, `C:\a"b\quil.exe`, `C:\o'b\quil.exe`,
		"C:\\a\u2019b\\quil.exe", `C:\a%PATH%\quil.exe`, `C:\a^b\quil.exe`, `C:\a&b\quil.exe`,
		`C:\a|b\quil.exe`, `C:\a<b\quil.exe`, `C:\a>b\quil.exe`, `C:\a!b\quil.exe`,
		"C:\\a\nb\\quil.exe", "C:\\a\u202eb\\quil.exe",
	}
	for _, shell := range []string{ShellCmd, ShellPowerShell} {
		for _, b := range bad {
			if got, err := QuoteCommand(shell, b, "--stdio"); err == nil {
				t.Errorf("QuoteCommand(%q, %q) accepted: %q", shell, b, got)
			}
		}
	}
}

func TestQuoteCommand_UnknownShell_Errors(t *testing.T) {
	if _, err := QuoteCommand("fish", "/x", "--stdio"); err == nil {
		t.Error("unknown shell accepted")
	}
}
