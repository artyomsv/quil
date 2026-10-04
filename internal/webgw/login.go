package webgw

import (
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
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
type authStore struct {
	rand  io.Reader
	sleep func(time.Duration)

	mu       sync.Mutex
	code     string // normalized; "" once used
	failures int
	sessions map[string]bool

	slots chan struct{}
}

func newAuthStore(rand io.Reader, sleep func(time.Duration)) *authStore {
	return &authStore{rand: rand, sleep: sleep, sessions: map[string]bool{}, slots: make(chan struct{}, maxLoginsInFlight)}
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

// Login consumes the code atomically and returns a new session. A wrong code
// waits loginDelayBase doubling to loginDelayCap before it is answered.
func (a *authStore) Login(code string) (string, error) {
	select {
	case a.slots <- struct{}{}:
	default:
		return "", ErrLoginBusy
	}
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
		return "", ErrWrongCode
	}
	raw := make([]byte, 32)
	if _, err := io.ReadFull(a.rand, raw); err != nil {
		return "", err
	}
	s := hex.EncodeToString(raw)
	a.mu.Lock()
	a.sessions[s] = true
	a.mu.Unlock()
	return s, nil
}

// Valid reports whether session names a live session.
func (a *authStore) Valid(session string) bool {
	if session == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sessions[session]
}

func sessionOf(r *http.Request) string {
	c, err := r.Cookie(SessionCookie)
	if err != nil {
		return ""
	}
	return c.Value
}

// loginHandler is POST /login with {"code": "..."}. Origin must be this page
// (blocks login CSRF); the body is bounded in size and time per request. The
// code never reaches logf.
func loginHandler(a *authStore, logf func(string, ...any)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if !originAllowed(r.Header.Get("Origin"), r.Host) {
			logf("login refused: foreign origin")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(loginBodyDeadline))
		var body struct {
			Code string `json:"code"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, loginBodyMax)).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		session, err := a.Login(body.Code)
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
		w.WriteHeader(http.StatusNoContent)
	}
}
