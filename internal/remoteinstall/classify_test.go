package remoteinstall

import "testing"

func TestClassifyExit(t *testing.T) {
	tests := []struct {
		name        string
		exitCode    int
		established bool
		want        Remedy
	}{
		{name: "command not found", exitCode: 127, want: RemedyInstall},
		{name: "found but not executable", exitCode: 126, want: RemedyReinstall},

		// 255 is ssh's own exit: auth, host key, DNS, refused connect. Nothing
		// about the far side's filesystem, so nothing to install.
		{name: "ssh own failure", exitCode: 255, want: RemedyNone},

		// -1 means the child has not been reaped — still connecting, or the
		// caller read the code before Close. Never an install signal.
		{name: "not exited", exitCode: -1, want: RemedyNone},

		{name: "quil ran and exited cleanly", exitCode: 0, want: RemedyNone},
		{name: "quil ran and failed", exitCode: 1, want: RemedyNone},

		// Established is the override: if a byte arrived, quil ran over there,
		// so whatever the exit code says happened afterwards is not a missing
		// binary. This is what stops a remote crash from being misread as
		// "not installed" and triggering an install offer.
		{name: "bytes arrived then 127", exitCode: 127, established: true, want: RemedyNone},
		{name: "bytes arrived then 126", exitCode: 126, established: true, want: RemedyNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyExit(tt.exitCode, tt.established)
			if got != tt.want {
				t.Errorf("ClassifyExit(%d, %v) = %v, want %v", tt.exitCode, tt.established, got, tt.want)
			}
		})
	}
}

func TestRemedy_String(t *testing.T) {
	// The value reaches log lines and test failures; an unnamed integer there
	// is a diagnosis nobody can read.
	tests := []struct {
		remedy Remedy
		want   string
	}{
		{RemedyNone, "none"},
		{RemedyInstall, "install"},
		{RemedyReinstall, "reinstall"},
	}
	for _, tt := range tests {
		if got := tt.remedy.String(); got != tt.want {
			t.Errorf("Remedy(%d).String() = %q, want %q", int(tt.remedy), got, tt.want)
		}
	}
}
