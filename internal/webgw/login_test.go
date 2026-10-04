package webgw

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
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
	s, _, err := a.Login(code)
	if err != nil || s == "" {
		t.Fatalf("first login: %q %v", s, err)
	}
	if !a.Valid(s) {
		t.Fatal("session not valid")
	}
	if _, _, err := a.Login(code); !errors.Is(err, ErrWrongCode) {
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
			if _, _, err := a.Login(code); err == nil {
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
		if _, _, err := a.Login("AAAAA-AAAAA"); !errors.Is(err, ErrWrongCode) {
			t.Fatalf("wrong code %d: %v", i, err)
		}
	}
	want := []time.Duration{250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second, 2 * time.Second}
	for i, d := range want {
		if (*slept)[i] != d {
			t.Fatalf("delay %d = %v, want %v", i, (*slept)[i], d)
		}
	}
	if _, _, err := a.Login(code); err != nil {
		t.Fatalf("the real code stopped working after wrong tries: %v", err)
	}
}

func TestLogin_NewCodeReplacesTheUnusedOne(t *testing.T) {
	a, _ := testAuth()
	old, _ := a.NewCode()
	fresh, _ := a.NewCode()
	if _, _, err := a.Login(old); !errors.Is(err, ErrWrongCode) {
		t.Fatalf("old code: %v", err)
	}
	if _, _, err := a.Login(fresh); err != nil {
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
		go func() { defer wg.Done(); _, _, _ = a.Login("WRONG") }()
	}
	deadline := time.Now().Add(5 * time.Second)
	for a.inFlight() < 8 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if _, _, err := a.Login("WRONG"); !errors.Is(err, ErrLoginBusy) {
		t.Fatalf("ninth login: %v", err)
	}
	close(block)
	wg.Wait()
}

// A person may type the code lower case, with I, L and O for 1 and 0, and
// with spaces or dashes anywhere.
func TestLogin_TypedCodeForms(t *testing.T) {
	for _, typed := range []string{"1011abcdef", "io-il ab cdef", "I0 1L-ABC DEF"} {
		a, _ := testAuth()
		a.code = "1011ABCDEF"
		if _, _, err := a.Login(typed); err != nil {
			t.Errorf("typed %q refused: %v", typed, err)
		}
	}
}

func TestKeyValid(t *testing.T) {
	a, _ := testAuth()
	code, _ := a.NewCode()
	s1, k1, err := a.Login(code)
	if err != nil || k1 == "" {
		t.Fatalf("login: %q %v", k1, err)
	}
	code, _ = a.NewCode()
	s2, k2, err := a.Login(code)
	if err != nil {
		t.Fatal(err)
	}
	if k1 == k2 {
		t.Fatal("two sessions share a key")
	}
	if !a.KeyValid(s1, k1) || !a.KeyValid(s2, k2) {
		t.Fatal("the right pair was refused")
	}
	cases := map[string][2]string{
		"wrong key":         {s1, "AAAA"},
		"another's key":     {s1, k2},
		"empty key":         {s1, ""},
		"unknown session":   {"nope", k1},
		"empty both":        {"", ""},
		"empty session key": {"", k1},
	}
	for name, c := range cases {
		if a.KeyValid(c[0], c[1]) {
			t.Errorf("%s was accepted", name)
		}
	}
}

func loginRequest(method, origin, body string) *http.Request {
	req := httptest.NewRequest(method, "/login", strings.NewReader(body))
	req.Host = "127.0.0.1:7880"
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestLoginHandler(t *testing.T) {
	a, _ := testAuth()
	code, _ := a.NewCode()
	var logged []string
	h := loginHandler(a, func(f string, args ...any) { logged = append(logged, fmt.Sprintf(f, args...)) })
	const own = "http://127.0.0.1:7880"
	body := `{"code":"` + code + `"}`

	serve := func(req *http.Request) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%d answer lacks no-store", rec.Code)
		}
		return rec
	}

	rec := serve(loginRequest(http.MethodGet, own, ""))
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("GET: %d allow=%q", rec.Code, rec.Header().Get("Allow"))
	}

	rec = serve(loginRequest(http.MethodPost, "http://evil.example", body))
	if rec.Code != http.StatusForbidden || rec.Header().Get("Set-Cookie") != "" {
		t.Fatalf("foreign origin: %d %q", rec.Code, rec.Header().Get("Set-Cookie"))
	}

	rec = serve(loginRequest(http.MethodPost, "", body))
	if rec.Code != http.StatusForbidden || rec.Header().Get("Set-Cookie") != "" {
		t.Fatalf("missing origin: %d %q", rec.Code, rec.Header().Get("Set-Cookie"))
	}

	req := loginRequest(http.MethodPost, own, body)
	req.Header.Set("Content-Type", "text/plain")
	if rec = serve(req); rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain: %d", rec.Code)
	}
	req = loginRequest(http.MethodPost, own, body)
	req.Header.Del("Content-Type")
	if rec = serve(req); rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("no content type: %d", rec.Code)
	}

	rec = serve(loginRequest(http.MethodPost, own, `{"code":"`+strings.Repeat("A", 2048)+`"}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized body: %d", rec.Code)
	}

	// The refused attempts above must not have spent the code.
	rec = serve(loginRequest(http.MethodPost, own, body))
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("right code: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	var got struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Key == "" {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	cookie := rec.Header().Get("Set-Cookie")
	if !strings.Contains(cookie, SessionCookie+"=") || !strings.Contains(cookie, "HttpOnly") ||
		!strings.Contains(cookie, "SameSite=Strict") || !strings.Contains(cookie, "Path=/") ||
		strings.Contains(cookie, "Secure") {
		t.Fatalf("cookie: %q", cookie)
	}
	sessionID := strings.TrimPrefix(strings.SplitN(cookie, ";", 2)[0], SessionCookie+"=")
	if !a.KeyValid(sessionID, got.Key) {
		t.Fatal("the returned key does not match the cookie's session")
	}

	if rec = serve(loginRequest(http.MethodPost, own, body)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("reused code: %d", rec.Code)
	}

	for _, l := range logged {
		if strings.Contains(l, code) || strings.Contains(l, got.Key) || strings.Contains(l, sessionID) {
			t.Fatalf("a secret reached a log line: %q", l)
		}
	}
}
