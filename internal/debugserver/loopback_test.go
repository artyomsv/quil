package debugserver

import (
	"strings"
	"testing"
)

func TestLoopbackAddr(t *testing.T) {
	tests := []struct {
		in, want string
		ok, err  bool
	}{
		{"", "", false, false},
		{"7878", "127.0.0.1:7878", true, false},
		{"localhost:7878", "127.0.0.1:7878", true, false},
		{"127.0.0.1:7878", "127.0.0.1:7878", true, false},
		{"[::1]:7878", "[::1]:7878", true, false},
		{":7878", "", false, true},
		{"0.0.0.0:7878", "", false, true},
		{"example.com:7878", "", false, true},
		{"127.0.0.1:99999", "", false, true},
	}
	const why = "the test's own reason"
	for _, tt := range tests {
		got, ok, err := LoopbackAddr("[listener] tcp", why, tt.in)
		if got != tt.want || ok != tt.ok || (err != nil) != tt.err {
			t.Errorf("LoopbackAddr(%q) = %q %v %v", tt.in, got, ok, err)
		}
		if err != nil && !strings.Contains(err.Error(), "[listener] tcp") {
			t.Errorf("error %q does not name the setting", err)
		}
	}
	// A non-loopback host is refused with the CALLER's reason, not pprof's.
	_, _, err := LoopbackAddr("[listener] tcp", why, "0.0.0.0:7878")
	if err == nil || !strings.Contains(err.Error(), why) || strings.Contains(err.Error(), "profiles") {
		t.Errorf("non-loopback error = %v, want the caller's reason only", err)
	}
}

// Ruling P-15: extracting LoopbackAddr leaves every QUIL_PPROF message
// byte-identical (these strings are what a user greps for).
func TestAddr_PprofMessageUnchanged(t *testing.T) {
	for in, want := range map[string]string{
		"0.0.0.0:6060": `QUIL_PPROF="0.0.0.0:6060" would bind "0.0.0.0", which is not loopback; ` +
			`profiles expose argv and goroutine state, so only 127.0.0.1, ::1 or localhost are accepted`,
		"127.0.0.1": `QUIL_PPROF port "127.0.0.1" is not a number ` +
			`(a value with no colon is treated as a port; use host:port)`,
		"127.0.0.1:70000": `QUIL_PPROF port 70000 is out of range 0-65535`,
	} {
		if _, _, err := Addr(in); err == nil || err.Error() != want {
			t.Errorf("Addr(%q) error = %v\nwant %s", in, err, want)
		}
	}
}
