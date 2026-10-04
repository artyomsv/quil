package webgw

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHostAllowed(t *testing.T) {
	ok := []string{"127.0.0.1:7880", "localhost:9000", "[::1]:7880", "127.0.0.1", "LOCALHOST:1", "127.5.6.7:80"}
	bad := []string{"attacker.example:7880", "127.0.0.1.attacker.example", "0.0.0.0:7880", "192.168.1.5:7880", "", "[::]:1", "localhost.:7880"}
	for _, h := range ok {
		if !hostAllowed(h) {
			t.Errorf("refused %q", h)
		}
	}
	for _, h := range bad {
		if hostAllowed(h) {
			t.Errorf("allowed %q", h)
		}
	}
}

func TestOriginAllowed(t *testing.T) {
	if !originAllowed("http://127.0.0.1:7880", "127.0.0.1:7880") {
		t.Error("same origin refused")
	}
	for _, o := range []string{"", "null", "http://127.0.0.1:7881", "https://127.0.0.1:7880", "http://evil.example", "http://127.0.0.1:7880/x"} {
		if originAllowed(o, "127.0.0.1:7880") {
			t.Errorf("allowed origin %q", o)
		}
	}
}

func TestWithSecurity_RefusesForeignHostAndSetsHeaders(t *testing.T) {
	h := withSecurity(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "attacker.example:7880"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || rec.Body.Len() != 0 {
		t.Fatalf("foreign host: %d %q", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest("GET", "/", nil)
	req.Host = "127.0.0.1:7880"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("loopback host: %d", rec.Code)
	}
	for k, v := range map[string]string{
		"Content-Security-Policy": CSP,
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
	} {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}
