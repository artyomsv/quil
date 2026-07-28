package remoteinstall

import (
	"strings"
	"testing"
)

func TestParseProbe_FreshHost(t *testing.T) {
	got, err := ParseProbe("/home/artyom\nLinux\nx86_64\n-\n-\n")
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
			out:          "/root\nLinux\naarch64\n/usr/local/bin/quil\nrw\n",
			wantPath:     "/usr/local/bin/quil",
			wantWritable: true,
		},
		{
			name:         "read-only system directory",
			out:          "/home/u\nDarwin\narm64\n/usr/local/bin/quil\nro\n",
			wantPath:     "/usr/local/bin/quil",
			wantWritable: false,
		},
		{
			name:         "user directory",
			out:          "/home/u\nLinux\nx86_64\n/home/u/.local/bin/quil\nrw\n",
			wantPath:     "/home/u/.local/bin/quil",
			wantWritable: true,
		},
		{
			// Anything other than the literal "rw" means not writable. A probe
			// that could not answer must never be read as permission to write.
			name:         "unrecognised writability marker",
			out:          "/home/u\nLinux\nx86_64\n/opt/quil\n?\n",
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

func TestParseProbe_ToleratesTrailingOutput(t *testing.T) {
	// Only the first five lines are contractual. A remote rc file or MOTD that
	// appends to stdout must not break the parse — the whole point of the
	// five-line contract is that it survives a chatty host.
	got, err := ParseProbe("/h\nLinux\nx86_64\n-\n-\nWelcome to Ubuntu\nLast login: ...\n")
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
	got, err := ParseProbe("/h\r\nLinux\r\nx86_64\r\n-\r\n-\r\n")
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
		{"too few lines", "/h\nLinux\n", "output"},
		{"empty", "", "output"},
		{"unsupported arch", "/h\nLinux\narmv7l\n-\n-\n", "armv7l"},
		{"unsupported os", "/h\nFreeBSD\namd64\n-\n-\n", "FreeBSD"},
		// An unset HOME leaves no way to compute the ~/.local/bin fallback, so
		// it must fail loudly rather than resolve to "/.local/bin" at the root.
		{"empty home", "\nLinux\nx86_64\n-\n-\n", "HOME"},
		{"relative home", "relative/path\nLinux\nx86_64\n-\n-\n", "HOME"},
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
