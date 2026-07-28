package remoteinstall

import (
	"strings"
	"testing"
)

// probeOut builds well-formed probe output: the sentinel, then the five
// contractual lines.
func probeOut(fields ...string) string {
	return probeSentinel + "\n" + strings.Join(fields, "\n") + "\n"
}

func TestParseProbe_FreshHost(t *testing.T) {
	got, err := ParseProbe(probeOut("/home/artyom", "Linux", "x86_64", "-", "-"))
	if err != nil {
		t.Fatalf("ParseProbe error = %v", err)
	}
	want := Probe{
		Home:     "/home/artyom",
		Platform: Platform{"linux", "amd64"},
	}
	if got != want {
		t.Errorf("ParseProbe = %+v, want %+v", got, want)
	}
}

func TestParseProbe_ExistingInstall(t *testing.T) {
	tests := []struct {
		name         string
		out          string
		wantPath     string
		wantWritable bool
	}{
		{
			name:         "writable system directory",
			out:          probeOut("/root", "Linux", "aarch64", "/usr/local/bin/quil", "rw"),
			wantPath:     "/usr/local/bin/quil",
			wantWritable: true,
		},
		{
			name:         "read-only system directory",
			out:          probeOut("/home/u", "Darwin", "arm64", "/usr/local/bin/quil", "ro"),
			wantPath:     "/usr/local/bin/quil",
			wantWritable: false,
		},
		{
			name:         "user directory",
			out:          probeOut("/home/u", "Linux", "x86_64", "/home/u/.local/bin/quil", "rw"),
			wantPath:     "/home/u/.local/bin/quil",
			wantWritable: true,
		},
		{
			// Anything other than the literal "rw" means not writable. A probe
			// that could not answer must never be read as permission to write,
			// and neither must one reporting a group- or world-writable dir.
			name:         "unrecognised writability marker",
			out:          probeOut("/home/u", "Linux", "x86_64", "/opt/quil", "?"),
			wantPath:     "/opt/quil",
			wantWritable: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseProbe(tt.out)
			if err != nil {
				t.Fatalf("ParseProbe error = %v", err)
			}
			if got.ExistingPath != tt.wantPath {
				t.Errorf("ExistingPath = %q, want %q", got.ExistingPath, tt.wantPath)
			}
			if got.ExistingDirWritable != tt.wantWritable {
				t.Errorf("ExistingDirWritable = %v, want %v", got.ExistingDirWritable, tt.wantWritable)
			}
		})
	}
}

// The sentinel exists because the remote shell prints its own output FIRST
// often enough to matter — ~/.zshenv runs for every zsh invocation, an rc file
// touching stty emits a warning. Without anchoring, each of those shifts every
// field by one and yields a confident, wrong diagnosis.
func TestParseProbe_IgnoresOutputBeforeTheSentinel(t *testing.T) {
	noisy := "stty: standard input: Inappropriate ioctl for device\n" +
		"Welcome to gpu01!\n" +
		probeOut("/home/u", "Linux", "x86_64", "-", "-")

	got, err := ParseProbe(noisy)
	if err != nil {
		t.Fatalf("ParseProbe error = %v", err)
	}
	if got.Home != "/home/u" {
		t.Errorf("Home = %q, want the value after the sentinel", got.Home)
	}
}

// The LAST sentinel wins: a host echoing its own rc file could otherwise replay
// a forged block ahead of the real one.
func TestParseProbe_UsesTheLastSentinel(t *testing.T) {
	forged := probeOut("/forged", "Linux", "x86_64", "/forged/quil", "rw") +
		probeOut("/real", "Linux", "x86_64", "-", "-")

	got, err := ParseProbe(forged)
	if err != nil {
		t.Fatalf("ParseProbe error = %v", err)
	}
	if got.Home != "/real" || got.ExistingPath != "" {
		t.Errorf("parsed the forged block: %+v", got)
	}
}

func TestParseProbe_ToleratesTrailingOutput(t *testing.T) {
	// Only the five lines after the sentinel are contractual; a MOTD appended
	// afterwards must not break the parse.
	got, err := ParseProbe(probeOut("/h", "Linux", "x86_64", "-", "-") +
		"Welcome to Ubuntu\nLast login: ...\n")
	if err != nil {
		t.Fatalf("ParseProbe error = %v", err)
	}
	if got.Home != "/h" {
		t.Errorf("Home = %q", got.Home)
	}
}

func TestParseProbe_ToleratesCRLF(t *testing.T) {
	// Our own script is LF (pinned by a test), but the remote shell's own
	// output is not under our control.
	got, err := ParseProbe(probeSentinel + "\r\n/h\r\nLinux\r\nx86_64\r\n-\r\n-\r\n")
	if err != nil {
		t.Fatalf("ParseProbe error = %v", err)
	}
	if got.Home != "/h" || got.Platform.GOARCH != "amd64" {
		t.Errorf("CR leaked into the parse: %+v", got)
	}
}

func TestParseProbe_Rejects(t *testing.T) {
	tests := []struct {
		name, out, wantMsg string
	}{
		{"no sentinel", "/h\nLinux\nx86_64\n-\n-\n", "marker"},
		{"empty", "", "marker"},
		{"too few lines after the sentinel", probeSentinel + "\n/h\nLinux\n", "want 5"},
		{"unsupported arch", probeOut("/h", "Linux", "armv7l", "-", "-"), "armv7l"},
		{"unsupported os", probeOut("/h", "FreeBSD", "amd64", "-", "-"), "FreeBSD"},
		// An unset HOME leaves no way to compute the ~/.local/bin fallback, so
		// it must fail loudly rather than resolve to "/.local/bin" at the root.
		{"empty home", probeOut("", "Linux", "x86_64", "-", "-"), "HOME"},
		{"relative home", probeOut("relative/path", "Linux", "x86_64", "-", "-"), "HOME"},
		// `command -v quil` resolves against the remote PATH, which can contain
		// a relative entry. A relative result would make the install directory
		// "." and be recorded verbatim as the ssh remote command.
		{"relative existing path", probeOut("/h", "Linux", "x86_64", "./quil", "rw"), "absolute"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseProbe(tt.out)
			if err == nil {
				t.Fatalf("ParseProbe(%q) error = nil, want error", tt.out)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("error %q does not mention %q", err, tt.wantMsg)
			}
		})
	}
}

// These paths are printed straight to the operator's terminal, including in the
// consent summary a dozen lines above the [y/N] prompt. An escape sequence
// smuggled through $HOME could repaint that block so the operator approves an
// install whose displayed target, version and warnings are all fabricated —
// and OSC 52 would reach the local clipboard. $HOME is controllable by anything
// that can write the remote's ~/.bashrc, not only by a fully compromised host.
func TestParseProbe_RejectsControlCharactersInPaths(t *testing.T) {
	tests := []struct {
		name string
		out  string
	}{
		{"escape in home", probeOut("/home/u\x1b[2J\x1b[H", "Linux", "x86_64", "-", "-")},
		{"osc52 in home", probeOut("/home/u\x1b]52;c;cGF3bmVk\x07", "Linux", "x86_64", "-", "-")},
		{"bell in home", probeOut("/home/u\x07", "Linux", "x86_64", "-", "-")},
		{"c1 csi in home", probeOut("/home/u2J", "Linux", "x86_64", "-", "-")},
		{"escape in existing path", probeOut("/home/u", "Linux", "x86_64", "/opt/\x1b[2Jquil", "rw")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseProbe(tt.out)
			if err == nil {
				t.Fatal("ParseProbe accepted a path carrying control characters")
			}
			if !strings.Contains(err.Error(), "control character") {
				t.Errorf("error %q does not explain the rejection", err)
			}
		})
	}
}
