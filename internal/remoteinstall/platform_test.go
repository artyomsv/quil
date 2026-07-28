package remoteinstall

import (
	"strings"
	"testing"
)

func TestPlatformFor(t *testing.T) {
	tests := []struct {
		name, unameS, unameM string
		want                 Platform
		wantErr              bool
	}{
		{name: "ubuntu x86", unameS: "Linux", unameM: "x86_64", want: Platform{"linux", "amd64"}},
		{name: "ubuntu arm", unameS: "Linux", unameM: "aarch64", want: Platform{"linux", "arm64"}},
		{name: "linux reporting amd64", unameS: "Linux", unameM: "amd64", want: Platform{"linux", "amd64"}},
		{name: "apple silicon", unameS: "Darwin", unameM: "arm64", want: Platform{"darwin", "arm64"}},
		{name: "intel mac", unameS: "Darwin", unameM: "x86_64", want: Platform{"darwin", "amd64"}},
		{name: "case insensitive", unameS: "linux", unameM: "X86_64", want: Platform{"linux", "amd64"}},
		{name: "surrounding whitespace", unameS: " Linux ", unameM: " x86_64 ", want: Platform{"linux", "amd64"}},

		// 32-bit ARM is the live failure: a 64-bit-kernel Raspberry Pi OS can
		// report aarch64 while its userland loader is armhf, so guessing here
		// produces a binary that will not exec.
		{name: "32-bit pi", unameS: "Linux", unameM: "armv7l", wantErr: true},
		{name: "older pi", unameS: "Linux", unameM: "armv6l", wantErr: true},
		{name: "32-bit x86", unameS: "Linux", unameM: "i686", wantErr: true},
		{name: "no release target", unameS: "FreeBSD", unameM: "amd64", wantErr: true},
		{name: "git bash on windows", unameS: "MINGW64_NT-10.0", unameM: "x86_64", wantErr: true},
		{name: "probe could not run uname", unameS: "-", unameM: "-", wantErr: true},
		{name: "empty", unameS: "", unameM: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PlatformFor(tt.unameS, tt.unameM)
			if (err != nil) != tt.wantErr {
				t.Fatalf("PlatformFor(%q, %q) error = %v, wantErr %v", tt.unameS, tt.unameM, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("PlatformFor(%q, %q) = %+v, want %+v", tt.unameS, tt.unameM, got, tt.want)
			}
		})
	}
}

// The error has to name the offending value: it is the only thing telling a
// user why their host was refused, and "unsupported platform" alone sends them
// to the issue tracker instead of to a supported host.
func TestPlatformFor_ErrorNamesTheValue(t *testing.T) {
	_, err := PlatformFor("Linux", "armv7l")
	if err == nil {
		t.Fatal("error = nil, want error")
	}
	if !strings.Contains(err.Error(), "armv7l") {
		t.Errorf("error %q does not name the architecture", err)
	}

	_, err = PlatformFor("FreeBSD", "amd64")
	if err == nil {
		t.Fatal("error = nil, want error")
	}
	if !strings.Contains(err.Error(), "FreeBSD") {
		t.Errorf("error %q does not name the OS", err)
	}
}

func TestPlatform_String(t *testing.T) {
	if got := (Platform{"linux", "arm64"}).String(); got != "linux/arm64" {
		t.Errorf("String() = %q, want %q", got, "linux/arm64")
	}
}
