package webgw

import (
	"net"
	"net/http"
	"strings"
)

// CSP allows inline styles only because xterm.js's DOM renderer inserts
// generated <style> elements; scripts stay same-origin only.
const CSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// hostAllowed accepts a loopback name or literal with any port: an ssh -L
// tunnel arrives on its own local port. Anything else — including a domain
// that resolves to 127.0.0.1, the DNS-rebinding case — is refused.
func hostAllowed(host string) bool {
	h := host
	if hh, _, err := net.SplitHostPort(host); err == nil {
		h = hh
	}
	h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// originAllowed requires the page's own origin: the WebSocket and /login may
// be reached only from the page this server served.
func originAllowed(origin, host string) bool {
	return origin != "" && strings.EqualFold(origin, "http://"+host)
}

// withSecurity refuses a foreign Host before anything else runs and sets the
// security headers on every response.
func withSecurity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hostAllowed(r.Host) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		h := w.Header()
		h.Set("Content-Security-Policy", CSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		if r.URL.Path == "/" || strings.HasSuffix(r.URL.Path, ".html") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}
