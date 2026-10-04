package webgw

import (
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func testAuth() (*authStore, *[]time.Duration) {
	var slept []time.Duration
	var mu sync.Mutex
	a := newAuthStore(rand.Reader, func(d time.Duration) {
		mu.Lock()
		slept = append(slept, d)
		mu.Unlock()
	})
	return a, &slept
}

func TestLogin_CodeWorksOnce(t *testing.T) {
	a, _ := testAuth()
	code, err := a.NewCode()
	if err != nil {
		t.Fatal(err)
	}
	s, err := a.Login(code)
	if err != nil || s == "" {
		t.Fatalf("first login: %q %v", s, err)
	}
	if !a.Valid(s) {
		t.Fatal("session not valid")
	}
	if _, err := a.Login(code); !errors.Is(err, ErrWrongCode) {
		t.Fatalf("second use: %v", err)
	}
}

// Two logins racing with the same code: exactly one wins.
func TestLogin_ParallelReuseOneWins(t *testing.T) {
	a, _ := testAuth()
	code, _ := a.NewCode()
	var wins int32
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := a.Login(code); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d logins won with one code", wins)
	}
}

// Wrong codes cost a growing delay but never invalidate the real code:
// another local account can reach the port, and a lockout would let it keep
// the owner out.
func TestLogin_WrongCodesDelayButNeverInvalidate(t *testing.T) {
	a, slept := testAuth()
	code, _ := a.NewCode()
	for i := 0; i < 50; i++ {
		if _, err := a.Login("AAAAA-AAAAA"); !errors.Is(err, ErrWrongCode) {
			t.Fatalf("wrong code %d: %v", i, err)
		}
	}
	want := []time.Duration{250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second, 2 * time.Second}
	for i, d := range want {
		if (*slept)[i] != d {
			t.Fatalf("delay %d = %v, want %v", i, (*slept)[i], d)
		}
	}
	if _, err := a.Login(code); err != nil {
		t.Fatalf("the real code stopped working after wrong tries: %v", err)
	}
}

func TestLogin_NewCodeReplacesTheUnusedOne(t *testing.T) {
	a, _ := testAuth()
	old, _ := a.NewCode()
	fresh, _ := a.NewCode()
	if _, err := a.Login(old); !errors.Is(err, ErrWrongCode) {
		t.Fatalf("old code: %v", err)
	}
	if _, err := a.Login(fresh); err != nil {
		t.Fatalf("new code: %v", err)
	}
}

func TestLogin_AtMostEightInProgress(t *testing.T) {
	block := make(chan struct{})
	a := newAuthStore(rand.Reader, func(time.Duration) { <-block })
	_, _ = a.NewCode()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = a.Login("WRONG") }()
	}
	deadline := time.Now().Add(5 * time.Second)
	for a.inFlight() < 8 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if _, err := a.Login("WRONG"); !errors.Is(err, ErrLoginBusy) {
		t.Fatalf("ninth login: %v", err)
	}
	close(block)
	wg.Wait()
}

func loginRequest(method, origin, body string) *http.Request {
	req := httptest.NewRequest(method, "/login", strings.NewReader(body))
	req.Host = "127.0.0.1:7880"
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	return req
}

func TestLoginHandler(t *testing.T) {
	a, _ := testAuth()
	code, _ := a.NewCode()
	var logged []string
	h := loginHandler(a, func(f string, _ ...any) { logged = append(logged, f) })
	const own = "http://127.0.0.1:7880"
	body := `{"code":"` + code + `"}`

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, loginRequest(http.MethodGet, own, ""))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, loginRequest(http.MethodPost, "http://evil.example", body))
	if rec.Code != http.StatusForbidden || rec.Header().Get("Set-Cookie") != "" {
		t.Fatalf("foreign origin: %d %q", rec.Code, rec.Header().Get("Set-Cookie"))
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, loginRequest(http.MethodPost, own, `{"code":"`+strings.Repeat("A", 2048)+`"}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized body: %d", rec.Code)
	}

	// The refused attempts above must not have spent the code.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, loginRequest(http.MethodPost, own, body))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("right code: %d", rec.Code)
	}
	cookie := rec.Header().Get("Set-Cookie")
	if !strings.Contains(cookie, SessionCookie+"=") || !strings.Contains(cookie, "HttpOnly") ||
		!strings.Contains(cookie, "SameSite=Strict") || !strings.Contains(cookie, "Path=/") {
		t.Fatalf("cookie: %q", cookie)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, loginRequest(http.MethodPost, own, body))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("reused code: %d", rec.Code)
	}

	for _, l := range logged {
		if strings.Contains(l, code) {
			t.Fatalf("the code reached a log line: %q", l)
		}
	}
}
