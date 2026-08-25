// Package debugserver exposes Go's pprof profiles over a loopback-only HTTP
// listener, started on demand from an environment variable.
//
// This is compiled into RELEASED binaries, not only dev ones, because the
// workloads worth profiling are the ones that have been running for days in
// production — a dev build points at an empty workspace and profiles a healthy
// process. That decision is what every guard in this file exists to pay for:
//
//   - Nothing listens unless QUIL_PPROF is set. No goroutine, no port, no cost.
//   - The address is refused unless it is LITERALLY loopback. A hostname is not
//     resolved, because a name that resolves to a LAN address today can resolve
//     somewhere else tomorrow, and the failure mode is a heap dump — which
//     carries live buffer contents — served to the network.
//   - The handlers are registered on a PRIVATE mux, so they are reachable only
//     through this listener.
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

// shutdownGrace bounds how long Close waits for in-flight requests. A CPU
// profile runs for its full ?seconds= window, so this is deliberately short:
// quil is exiting, and a half-written profile is a better outcome than a
// process that will not quit.
const shutdownGrace = 100 * time.Millisecond

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
			"%s=%q would bind %q, which is not loopback; profiles expose heap and "+
				"goroutine state, so only 127.0.0.1, ::1 or localhost are accepted",
			EnvVar, v, host)
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
		return fmt.Errorf("%s port %q is not a number", EnvVar, p)
	}
	if n < 0 || n > 65535 {
		return fmt.Errorf("%s port %d is out of range 0-65535", EnvVar, n)
	}
	return nil
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
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	s := &Server{
		ln: ln,
		// No timeouts: a CPU profile is a long-lived request by design, and a
		// ReadTimeout here would cut off exactly the profile worth taking.
		srv: &http.Server{Handler: mux},
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
		// Close runs off-goroutine under a deadline because it waits on active
		// connections, and a CPU profile holds one open for its whole window.
		// A profile in flight must not be able to hold up quil's exit.
		done := make(chan error, 1)
		go func() { done <- s.srv.Close() }()
		select {
		case err = <-done:
		case <-time.After(shutdownGrace):
			err = fmt.Errorf("pprof listener did not close within %s", shutdownGrace)
		}
	})
	return err
}
