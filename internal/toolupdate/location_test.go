package toolupdate

import "testing"

// TestSystemLocation_Table pins the spec's list: package-manager directories
// are refused, the manual-install directories (/usr/local, /opt, a user's
// own folder) are not. Both arms run on whichever OS hosts the test because
// the decision is textual.
func TestSystemLocation_Table(t *testing.T) {
	winEnv := func(k string) string {
		switch k {
		case "ProgramFiles":
			return `C:\Program Files`
		case "ProgramFiles(x86)":
			return `C:\Program Files (x86)`
		case "WINDIR":
			return `C:\Windows`
		}
		return ""
	}
	noEnv := func(string) string { return "" }

	cases := []struct {
		path, goos string
		env        func(string) string
		want       bool
	}{
		// Linux / macOS
		{"/usr/bin/stripe", "linux", nil, true},
		{"/usr/sbin/stripe", "linux", nil, true},
		{"/usr/lib/stripe/stripe", "linux", nil, true},
		{"/usr/lib64/stripe", "linux", nil, true},
		{"/usr/libexec/stripe", "linux", nil, true},
		{"/bin/stripe", "linux", nil, true},
		{"/sbin/stripe", "linux", nil, true},
		{"/snap/bin/stripe", "linux", nil, true},
		{"/nix/store/abc-stripe/bin/stripe", "linux", nil, true},
		{"/usr/bin/../bin/stripe", "linux", nil, true}, // cleaned before the compare
		{"/usr/local/bin/stripe", "linux", nil, false},
		{"/opt/stripe/stripe", "linux", nil, false},
		{"/home/u/bin/stripe", "linux", nil, false},
		{"/home/u/.local/bin/stripe", "darwin", nil, false},
		{"/opt/homebrew/bin/stripe", "darwin", nil, false},
		{"/usr/binary-tools/stripe", "linux", nil, false}, // "/usr/bin" is a segment, not a prefix

		// Windows, env-derived roots
		{`C:\Program Files\Stripe\stripe.exe`, "windows", winEnv, true},
		{`c:\program files (x86)\stripe\stripe.exe`, "windows", winEnv, true},
		{`C:\Windows\System32\stripe.exe`, "windows", winEnv, true},
		{`C:/Program Files/Stripe/stripe.exe`, "windows", winEnv, true}, // forward slashes
		{`C:\Users\u\AppData\Local\Microsoft\WindowsApps\x.exe`, "windows", winEnv, true},
		{`E:\Tools\stripe\stripe.exe`, "windows", winEnv, false},
		{`C:\Program Filesystem\stripe.exe`, "windows", winEnv, false}, // segment boundary
		{`C:\Users\u\scoop\apps\stripe\current\stripe.exe`, "windows", winEnv, false},

		// Windows, extended-length and NT prefixes name the same directories
		{`\\?\C:\Program Files\Stripe\stripe.exe`, "windows", winEnv, true},
		{`\??\C:\Windows\System32\stripe.exe`, "windows", winEnv, true},
		{`\\?\C:\Users\u\AppData\Local\Microsoft\WindowsApps\x.exe`, "windows", winEnv, true},
		{`\\?\E:\Tools\stripe\stripe.exe`, "windows", winEnv, false},
		{`\\?\UNC\server\share\stripe.exe`, "windows", winEnv, false},
		{`\\server\share\stripe.exe`, "windows", winEnv, false},

		// Windows, stripped environment: the usual roots still count
		{`C:\Program Files\Stripe\stripe.exe`, "windows", noEnv, true},
		{`C:\Windows\stripe.exe`, "windows", noEnv, true},
		{`D:\Program Files\Stripe\stripe.exe`, "windows", noEnv, false},
		{`C:\Program Files\Stripe\stripe.exe`, "windows", nil, true}, // nil env never panics
	}
	for _, tc := range cases {
		if got := SystemLocation(tc.path, tc.goos, tc.env); got != tc.want {
			t.Errorf("SystemLocation(%q, %s) = %v, want %v", tc.path, tc.goos, got, tc.want)
		}
	}
}
