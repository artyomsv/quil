package main

import (
	"strings"
	"testing"
)

func TestParseWebFlags(t *testing.T) {
	f, err := parseWebFlags([]string{"--port", "7880", "--no-open"})
	if err != nil || f.Listen != "127.0.0.1:7880" || !f.NoOpen {
		t.Fatalf("%+v %v", f, err)
	}
	f, err = parseWebFlags(nil)
	if err != nil || f.Listen != "127.0.0.1:0" {
		t.Fatalf("default: %+v %v", f, err)
	}
	f, err = parseWebFlags([]string{"--listen", "localhost:7881"})
	if err != nil || f.Listen != "127.0.0.1:7881" {
		t.Fatalf("loopback --listen: %+v %v", f, err)
	}
	if _, err := parseWebFlags([]string{"--listen", "0.0.0.0:7880"}); err == nil {
		t.Fatal("a non-loopback --listen was accepted")
	}
	if _, err := parseWebFlags([]string{"--port"}); err == nil {
		t.Fatal("--port without a value was accepted")
	}
	if _, err := parseWebFlags([]string{"--port", "abc"}); err == nil {
		t.Fatal("a non-numeric --port was accepted")
	}
	if _, err := parseWebFlags([]string{"--bogus"}); err == nil || !strings.Contains(err.Error(), "--bogus") {
		t.Fatalf("unknown flag: %v", err)
	}
}

// The code never reaches the browser-opening command: that command's
// arguments are readable by other local accounts while it runs.
func TestWebStartURLCarriesNoSecret(t *testing.T) {
	u := webStartURL("127.0.0.1:7880")
	if u != "http://127.0.0.1:7880/" {
		t.Fatalf("start URL = %q", u)
	}
	if strings.ContainsAny(u, "#?") {
		t.Fatal("start URL carries a fragment or query")
	}
}

func TestWebVersionMismatch(t *testing.T) {
	cases := []struct {
		name string
		res  handshakeResult
		want bool
	}{
		{"matched", handshakeResult{Matched: true, DaemonVersion: "1.2.3"}, false},
		{"older daemon", handshakeResult{DaemonVersion: "1.0.0", Cmp: 1}, true},
		{"daemon did not answer", handshakeResult{DaemonUnknown: true}, false},
		{"dev client", handshakeResult{ClientSkipped: true}, false},
	}
	for _, c := range cases {
		if got := webVersionMismatch(c.res); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
