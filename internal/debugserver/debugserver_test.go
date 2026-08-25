package debugserver

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// The whole point of this package is that a debug surface compiled into a
// RELEASED binary can never be reachable from another machine. Every test below
// is about that promise, not about pprof itself.

func TestAddr_UnsetMeansDisabled(t *testing.T) {
	addr, ok, err := Addr("")
	if err != nil {
		t.Fatalf("Addr(%q) error = %v, want nil", "", err)
	}
	if ok {
		t.Errorf("Addr(%q) ok = true, want false — an unset env var must not start a listener", "")
	}
	if addr != "" {
		t.Errorf("Addr(%q) addr = %q, want empty", "", addr)
	}
}

func TestAddr_BarePortBindsLoopback(t *testing.T) {
	addr, ok, err := Addr("6060")
	if err != nil {
		t.Fatalf("Addr(\"6060\") error = %v, want nil", err)
	}
	if !ok {
		t.Fatal("Addr(\"6060\") ok = false, want true")
	}
	if addr != "127.0.0.1:6060" {
		t.Errorf("Addr(\"6060\") = %q, want %q — a bare port must not become an "+
			"all-interfaces bind", addr, "127.0.0.1:6060")
	}
}

// A host that is not literally loopback is refused rather than bound. Refusing
// is the only safe direction: binding 0.0.0.0 exposes heap and goroutine dumps
// to the network, and ":6060" is the form that does it by accident.
func TestAddr_RejectsNonLoopbackHost(t *testing.T) {
	for _, v := range []string{
		":6060",             // empty host = all interfaces
		"0.0.0.0:6060",      // explicit all interfaces
		"192.168.6.12:6060", // a real LAN address
		"[::]:6060",         // all interfaces, v6
		"example.com:6060",  // a name we refuse to resolve
	} {
		if _, ok, err := Addr(v); err == nil {
			t.Errorf("Addr(%q) error = nil (ok=%v), want a refusal — this binds a "+
				"heap dump to the network", v, ok)
		}
	}
}

func TestAddr_AcceptsExplicitLoopback(t *testing.T) {
	for _, v := range []string{
		"127.0.0.1:6060",
		"localhost:6060",
		"[::1]:6060",
		"127.0.0.2:6060", // the whole 127/8 block is loopback
	} {
		if _, ok, err := Addr(v); err != nil || !ok {
			t.Errorf("Addr(%q) = ok %v, err %v; want ok with no error", v, ok, err)
		}
	}
}

func TestAddr_RejectsMalformedValue(t *testing.T) {
	for _, v := range []string{
		"abc",
		"99999",           // above the port range
		"-1",              //
		"127.0.0.1:abc",   //
		"127.0.0.1",       // no port at all
		"127.0.0.1:65536", // one past the top
	} {
		if _, ok, err := Addr(v); err == nil {
			t.Errorf("Addr(%q) error = nil (ok=%v), want a refusal", v, ok)
		}
	}
}

// Port 0 is how a test asks the OS for a free port; it must survive parsing.
func TestAddr_AllowsEphemeralPort(t *testing.T) {
	addr, ok, err := Addr("127.0.0.1:0")
	if err != nil || !ok {
		t.Fatalf("Addr(\"127.0.0.1:0\") = ok %v, err %v; want ok", ok, err)
	}
	if addr != "127.0.0.1:0" {
		t.Errorf("Addr = %q, want %q", addr, "127.0.0.1:0")
	}
}

func TestStartFromEnv_UnsetStartsNothing(t *testing.T) {
	s, err := StartFromEnv("")
	if err != nil {
		t.Fatalf("StartFromEnv(\"\") error = %v, want nil", err)
	}
	if s != nil {
		s.Close()
		t.Error("StartFromEnv(\"\") returned a server; an unset env var must start nothing")
	}
}

func TestStartFromEnv_RefusesNonLoopbackWithoutListening(t *testing.T) {
	s, err := StartFromEnv("0.0.0.0:0")
	if s != nil {
		s.Close()
	}
	if err == nil {
		t.Fatal("StartFromEnv(\"0.0.0.0:0\") error = nil, want a refusal")
	}
	if s != nil {
		t.Error("StartFromEnv refused the address but still returned a server")
	}
}

func TestServer_ServesHeapProfile(t *testing.T) {
	s, err := StartFromEnv("127.0.0.1:0")
	if err != nil {
		t.Fatalf("StartFromEnv error = %v", err)
	}
	if s == nil {
		t.Fatal("StartFromEnv returned no server for a valid address")
	}
	defer s.Close()

	resp, err := http.Get("http://" + s.Addr() + "/debug/pprof/heap?debug=1")
	if err != nil {
		t.Fatalf("GET heap profile: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("heap profile status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(body), "heap profile") {
		t.Errorf("heap profile body does not look like a profile: %.120q", body)
	}
}

func TestServer_ServesGoroutineProfile(t *testing.T) {
	s, err := StartFromEnv("127.0.0.1:0")
	if err != nil {
		t.Fatalf("StartFromEnv error = %v", err)
	}
	defer s.Close()

	resp, err := http.Get("http://" + s.Addr() + "/debug/pprof/goroutine?debug=1")
	if err != nil {
		t.Fatalf("GET goroutine profile: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("goroutine profile status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "goroutine profile") {
		t.Errorf("goroutine body does not look like a profile: %.120q", body)
	}
}

// Addr() must report the REAL port, or a caller that asked for :0 has no way to
// reach the server it just started — and the startup log line would lie.
func TestServer_AddrReportsBoundPort(t *testing.T) {
	s, err := StartFromEnv("127.0.0.1:0")
	if err != nil {
		t.Fatalf("StartFromEnv error = %v", err)
	}
	defer s.Close()

	if strings.HasSuffix(s.Addr(), ":0") {
		t.Errorf("Addr() = %q, want the port the OS actually assigned", s.Addr())
	}
	if !strings.HasPrefix(s.Addr(), "127.0.0.1:") {
		t.Errorf("Addr() = %q, want a loopback address", s.Addr())
	}
}

func TestServer_CloseIsIdempotent(t *testing.T) {
	s, err := StartFromEnv("127.0.0.1:0")
	if err != nil {
		t.Fatalf("StartFromEnv error = %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("first Close error = %v, want nil", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("second Close error = %v, want nil — main() may close on more "+
			"than one shutdown path", err)
	}
}
