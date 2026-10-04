package webgw

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"sync"
	"time"
)

var (
	// ErrWrongCode answers any code that is not the current one, including a
	// code already used.
	ErrWrongCode = errors.New("wrong login code")
	// ErrLoginBusy answers a login past the in-progress limit.
	ErrLoginBusy = errors.New("too many logins in progress")
)

const (
	maxLoginsInFlight = 8
	loginDelayBase    = 250 * time.Millisecond
	loginDelayCap     = 2 * time.Second
	loginBodyMax      = 1 << 10
	loginBodyDeadline = 5 * time.Second
	// SessionCookie names the browser's session. HttpOnly and SameSite=Strict;
	// it lives in the gateway's memory only.
	SessionCookie = "quil_web_session"
)

// authStore holds the one unused login code and the live sessions. Wrong
// codes only add delay: they never invalidate the code, because another
// local account can reach the port and could otherwise lock the owner out.
//
// Each session also has a key. Cookies ignore ports, so a page another local
// account serves on a different 127.0.0.1 port is sent the session cookie
// too; the key is held by this origin's page only and is sent in the
// WebSocket's first message, so the cookie alone opens nothing.
type authStore struct {
	rand  io.Reader // must be safe for concurrent use (crypto/rand is)
	sleep func(time.Duration)

	mu       sync.Mutex
	code     string // normalized; "" once used
	failures int
	sessions map[string]string // session -> key

	slots chan struct{}
}

func newAuthStore(rand io.Reader, sleep func(time.Duration)) *authStore {
	return &authStore{rand: rand, sleep: sleep, sessions: map[string]string{}, slots: make(chan struct{}, maxLoginsInFlight)}
}

// NewCode replaces any unused code with a fresh one and returns it formatted.
func (a *authStore) NewCode() (string, error) {
	c, err := newLoginCode(a.rand)
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	a.code = c
	a.mu.Unlock()
	return FormatCode(c), nil
}

func (a *authStore) inFlight() int { return len(a.slots) }

// Login consumes the code atomically and returns a new session and its key.
// A wrong code waits loginDelayBase doubling to loginDelayCap before it is
// answered.
func (a *authStore) Login(code string) (session, key string, err error) {
	select {
	case a.slots <- struct{}{}:
	default:
		return "", "", ErrLoginBusy
	}
	// The slot stays held across the wrong-code delay below. That is what caps
	// guessing: releasing it first would let a client queue attempts without
	// the delay counting against the limit, and a fast refusal would be a
	// timing oracle. The price is that a flood of wrong codes can occupy every
	// slot and lock the owner out for as long as it lasts, by design.
	defer func() { <-a.slots }()

	given := normalizeCode(code)
	a.mu.Lock()
	ok := a.code != "" && len(given) == len(a.code) &&
		subtle.ConstantTimeCompare([]byte(given), []byte(a.code)) == 1
	var delay time.Duration
	if ok {
		a.code = ""
		a.failures = 0
	} else {
		delay = loginDelayBase << a.failures
		if delay > loginDelayCap || delay <= 0 {
			delay = loginDelayCap
		}
		if a.failures < 16 {
			a.failures++
		}
	}
	a.mu.Unlock()
	if !ok {
		a.sleep(delay)
		return "", "", ErrWrongCode
	}
	raw := make([]byte, 64)
	if _, err := io.ReadFull(a.rand, raw); err != nil {
		return "", "", err
	}
	session = hex.EncodeToString(raw[:32])
	key = base64.RawURLEncoding.EncodeToString(raw[32:])
	a.mu.Lock()
	a.sessions[session] = key
	a.mu.Unlock()
	return session, key, nil
}

// Valid reports whether session names a live session.
func (a *authStore) Valid(session string) bool {
	if session == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.sessions[session]
	return ok
}

// KeyValid reports whether key is the one issued with session. An unknown
// session or an empty key never matches.
func (a *authStore) KeyValid(session, key string) bool {
	if key == "" {
		return false
	}
	a.mu.Lock()
	want, ok := a.sessions[session]
	a.mu.Unlock()
	return ok && subtle.ConstantTimeCompare([]byte(key), []byte(want)) == 1
}

func sessionOf(r *http.Request) string {
	c, err := r.Cookie(SessionCookie)
	if err != nil {
		return ""
	}
	return c.Value
}

// sessionHandler is GET /session: 204 when the request carries a live session
// cookie, 401 otherwise. The page asks it on load to choose between the login
// form and the workspace.
func sessionHandler(a *authStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if !a.Valid(sessionOf(r)) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// loginHandler is POST /login with {"code": "..."} as application/json (a
// cross-site form cannot send that type without a preflight). Origin must be
// this page (blocks login CSRF); the body is bounded in size and time per
// request. On success it answers {"key": "..."} and sets the session cookie.
// Neither the code nor the key reaches logf.
func loginHandler(a *authStore, logf func(string, ...any)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if !originAllowed(r.Header.Get("Origin"), r.Host) {
			logf("login refused: foreign origin")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
			w.WriteHeader(http.StatusUnsupportedMediaType)
			return
		}
		if err := http.NewResponseController(w).SetReadDeadline(time.Now().Add(loginBodyDeadline)); err != nil {
			logf("login body deadline not set: %v", err)
		}
		var body struct {
			Code string `json:"code"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, loginBodyMax)).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		session, key, err := a.Login(body.Code)
		switch {
		case errors.Is(err, ErrLoginBusy):
			logf("login refused: too many in progress")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		case err != nil:
			logf("login failed: wrong code")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		logf("login ok")
		http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: session, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Key string `json:"key"`
		}{key})
	}
}
