package remoteinstall

import "testing"

func TestPlanTarget(t *testing.T) {
	tests := []struct {
		name         string
		probe        Probe
		wantDir      string
		wantShadowed string
	}{
		{
			name:    "fresh install goes to the user directory",
			probe:   Probe{Home: "/home/a"},
			wantDir: "/home/a/.local/bin",
		},
		{
			// Replacing in place keeps a hand-installed /usr/local/bin copy
			// authoritative instead of silently leaving two binaries, one of
			// which a bare `ssh host quil` would still find.
			name:    "writable existing install is replaced in place",
			probe:   Probe{Home: "/home/a", ExistingPath: "/usr/local/bin/quil", ExistingDirWritable: true},
			wantDir: "/usr/local/bin",
		},
		{
			// Falling back rather than escalating: sudo cannot be answered over
			// a non-tty ssh channel.
			name:         "read-only existing install falls back and reports shadowing",
			probe:        Probe{Home: "/home/a", ExistingPath: "/usr/local/bin/quil", ExistingDirWritable: false},
			wantDir:      "/home/a/.local/bin",
			wantShadowed: "/usr/local/bin/quil",
		},
		{
			name:    "existing install already in the user directory",
			probe:   Probe{Home: "/home/a", ExistingPath: "/home/a/.local/bin/quil", ExistingDirWritable: true},
			wantDir: "/home/a/.local/bin",
		},
		{
			name:    "root home",
			probe:   Probe{Home: "/root"},
			wantDir: "/root/.local/bin",
		},
		{
			// Remote paths are always POSIX. Using path/filepath here would
			// split on backslashes in a Windows build of the TUI.
			name:    "home with a trailing slash",
			probe:   Probe{Home: "/home/a/"},
			wantDir: "/home/a/.local/bin",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PlanTarget(tt.probe)
			if got.Dir != tt.wantDir {
				t.Errorf("Dir = %q, want %q", got.Dir, tt.wantDir)
			}
			if got.Shadowed != tt.wantShadowed {
				t.Errorf("Shadowed = %q, want %q", got.Shadowed, tt.wantShadowed)
			}
		})
	}
}

// The binary path is what gets persisted and used as the ssh remote command, so
// it must be the full path to quil rather than the directory.
func TestTarget_BinaryPath(t *testing.T) {
	tgt := PlanTarget(Probe{Home: "/home/a"})
	if got, want := tgt.BinaryPath(), "/home/a/.local/bin/quil"; got != want {
		t.Errorf("BinaryPath() = %q, want %q", got, want)
	}
}
