// Package debugserver exposes Go's pprof profiles over a loopback-only HTTP
// listener, started on demand from an environment variable.
//
// This is compiled into RELEASED binaries, not only dev ones, because the
// workloads worth profiling are the ones that have been running for days in
// production — a dev build points at an empty workspace and profiles a healthy
// process. That decision is what every guard in this file exists to pay for:
//
//   - Nothing listens unless QUIL_PPROF is set. No goroutine, no port, no cost.
//   - The address is refused unless it is LITERALLY loopback, and "localhost"
//     is rewritten to 127.0.0.1 before it reaches net.Listen so the resolver
//     never chooses the bind address. A hostname is not resolved, because a
//     name that resolves to a LAN address today can resolve somewhere else
//     tomorrow.
//   - The handlers are registered on a PRIVATE mux, so they are reachable only
//     through this listener.
//   - seconds= is clamped, so one request cannot pin the profiler indefinitely.
//
// What a profile actually exposes, since the guards are only worth what the
// threat model is: Go's heap profile is a SAMPLED ALLOCATION profile — call
// stacks and byte counts, not memory contents — so it does not carry terminal
// buffer contents, and net/http/pprof exposes no endpoint that dumps heap
// memory. What does leak is narrower and real: /debug/pprof/cmdline is the full
// argv, which for `quil --remote` names the destination host, and
// /debug/pprof/goroutine?debug=2 is every goroutine's stack with pointer words
// and absolute source paths.
//
// The listener is UNAUTHENTICATED, and loopback is a machine boundary rather
// than a user boundary — quil's IPC socket is chmod 0600, and there is no
// equivalent for a loopback TCP socket on either platform. So while the port is
// open, any local account can read the above. That is the reason to set
// QUIL_PPROF for an investigation rather than leaving it in a shell profile.
//
// The private mux does not make http.DefaultServeMux pristine: importing
// net/http/pprof runs its init, which registers the same handlers there, and
// there is no way to import the package without that. It is inert only because
// nothing in quil serves on DefaultServeMux. If that ever changes, this package
// must switch to runtime/pprof and hand-written handlers.
package debugserver

import (
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"strconv"
	"strings"
	"sync"
	"time"
)

// EnvVar names the environment variable that enables profiling.
const EnvVar = "QUIL_PPROF"

// Addr resolves a QUIL_PPROF value into a listen address.
//
// ok is false with a nil error only for the unset case — "profiling was not
// asked for" is not a failure. Every other rejection returns an error, because
// a value the user deliberately set and that silently did nothing is worse than
// a startup warning.
//
// A value with no colon is a bare port and gets the loopback host prepended.
// A value WITH a colon must name a loopback host explicitly: ":6060" is the
// form that binds every interface by accident, so it is refused rather than
// helpfully corrected.
func Addr(v string) (string, bool, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", false, nil
	}

	if !strings.Contains(v, ":") {
		if err := checkPort(v); err != nil {
			return "", false, err
		}
		return net.JoinHostPort("127.0.0.1", v), true, nil
	}

	host, port, err := net.SplitHostPort(v)
	if err != nil {
		return "", false, fmt.Errorf("%s=%q is not a valid address: %w", EnvVar, v, err)
	}
	if err := checkPort(port); err != nil {
		return "", false, err
	}
	if !isLoopbackHost(host) {
		return "", false, fmt.Errorf(
			"%s=%q would bind %q, which is not loopback; profiles expose argv and "+
				"goroutine state, so only 127.0.0.1, ::1 or localhost are accepted",
			EnvVar, v, host)
	}
	// Resolve "localhost" HERE rather than letting net.Listen do it. It is the
	// one accepted host that is a name, and passing a name to net.Listen hands
	// the bind-address choice to the resolver — which is exactly what this
	// package's refusal to resolve hostnames exists to prevent. A container
	// image with no localhost entry in /etc/hosts and a search domain in
	// resolv.conf is enough for that lookup to leave the machine. Rewriting it
	// makes the invariant structural instead of documented.
	if host == "localhost" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port), true, nil
}

// isLoopbackHost reports whether host is literally a loopback address.
//
// "localhost" is accepted as the one name, since it is loopback by convention
// everywhere quil runs. No other name is resolved: DNS is not a property of
// this process, and a resolver answer is not a promise about the next lookup.
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// checkPort accepts 0 (ask the OS for a free port) through 65535.
func checkPort(p string) error {
	n, err := strconv.Atoi(p)
	if err != nil {
		// Names the convention as well as the failure: a colon-less value is
		// read as a bare port, so QUIL_PPROF=127.0.0.1 lands here complaining
		// about a "port" the user thinks they supplied as a host.
		return fmt.Errorf("%s port %q is not a number "+
			"(a value with no colon is treated as a port; use host:port)", EnvVar, p)
	}
	if n < 0 || n > 65535 {
		return fmt.Errorf("%s port %d is out of range 0-65535", EnvVar, n)
	}
	return nil
}

// maxProfileSeconds bounds the ?seconds= window on the two sampling endpoints.
//
// net/http/pprof applies NO ceiling of its own: it parses seconds, and calls
// configureWriteDeadline, which is a no-op unless Server.WriteTimeout is set —
// and WriteTimeout is deliberately zero here. So without this clamp any local
// process could ask for a profile lasting years. That is worse than a slow
// request: runtime/pprof refuses a SECOND concurrent CPU profile, so one held
// request denies the operator the profiling this package exists to provide,
// while the profiler's own overhead runs on the process being investigated.
//
// Five minutes is far beyond the documented workflow (30 s) and far short of
// indefinite.
const maxProfileSeconds = 300

// clampSeconds bounds ?seconds= before delegating to a pprof handler.
//
// Rewrites the value rather than rejecting the request, so an over-long ask
// still returns a usable profile instead of an error the caller has to
// interpret. An absent, malformed or non-positive value is left alone for
// pprof's own default (30 s) to handle.
func clampSeconds(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if raw := r.FormValue("seconds"); raw != "" {
			if sec, err := strconv.ParseInt(raw, 10, 64); err == nil && sec > maxProfileSeconds {
				q := r.URL.Query()
				q.Set("seconds", strconv.Itoa(maxProfileSeconds))
				r.URL.RawQuery = q.Encode()
				// FormValue cached the parsed form on the first call above, so
				// the rewritten URL alone would not be seen. Clearing it forces
				// a re-parse from the updated RawQuery.
				r.Form = nil
			}
		}
		next(w, r)
	}
}

// Server is a running pprof listener.
type Server struct {
	ln   net.Listener
	srv  *http.Server
	once sync.Once
}

// StartFromEnv starts a profiling listener for a QUIL_PPROF value.
//
// Returns (nil, nil) when the variable is unset — the caller's "profiling off"
// case is not an error and needs no branch of its own.
func StartFromEnv(v string) (*Server, error) {
	addr, ok, err := Addr(v)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return Start(addr)
}

// Start listens on addr and serves pprof.
//
// addr is assumed to have come from Addr; it is re-checked anyway, so a caller
// that builds an address by hand cannot bypass the loopback rule.
func Start(addr string) (*Server, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("pprof address %q is not valid: %w", addr, err)
	}
	if !isLoopbackHost(host) {
		return nil, fmt.Errorf("refusing to serve pprof on non-loopback host %q", host)
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("pprof listen on %s: %w", addr, err)
	}

	mux := http.NewServeMux()
	// Index also serves the named profiles (heap, goroutine, allocs, block,
	// mutex, threadcreate) from the path suffix; the four below need their own
	// entries because they are not runtime/pprof profile names.
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", clampSeconds(pprof.Profile))
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", clampSeconds(pprof.Trace))

	s := &Server{
		ln: ln,
		srv: &http.Server{
			Handler: mux,
			// WriteTimeout stays ZERO deliberately: a CPU profile is a
			// long-lived response by design and any write deadline would cut
			// off exactly the profile worth taking. maxProfileSeconds is what
			// bounds that duration instead.
			//
			// The other three are set, because none of them limits how long a
			// RESPONSE may take. Without them a local peer can hold connections
			// open indefinitely, each pinning a goroutine, on a listener with
			// no authentication in front of it.
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       60 * time.Second,
			MaxHeaderBytes:    1 << 16,
		},
	}
	// Serve always returns a non-nil error, and the only one reachable here is
	// the ErrServerClosed that Close causes deliberately. There is no caller to
	// return it to — main has already moved on — and profiling failing must
	// never take the process with it.
	go func() { _ = s.srv.Serve(ln) }()
	return s, nil
}

// Addr returns the address actually bound, including the port the OS assigned
// when the request was for port 0.
func (s *Server) Addr() string {
	if s == nil || s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}

// Close stops the listener. Safe to call more than once, since main has more
// than one shutdown path and neither should have to know about the other.
func (s *Server) Close() error {
	if s == nil {
		return nil
	}
	var err error
	s.once.Do(func() {
		// Close, never Shutdown. http.Server.Close does NOT wait for in-flight
		// handlers — it closes the listener and every connection immediately,
		// which cancels request contexts and unblocks the sleep inside a
		// running CPU profile. That is what makes it safe to call on quil's
		// exit path with no deadline around it.
		//
		// Shutdown is the one that waits, and switching to it for "graceful"
		// behaviour would block exit for the remainder of any profile window —
		// up to maxProfileSeconds. A truncated profile is the right trade when
		// the process is going away.
		err = s.srv.Close()
	})
	return err
}
