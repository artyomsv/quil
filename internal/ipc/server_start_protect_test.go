package ipc

import (
	"errors"
	"path/filepath"
	"testing"
)

// Start must stop when the socket cannot be restricted to its owner: return
// the error and serve nothing. TestProtectSocket_ReportsChmodFailure proves
// protectSocket reports the failure; this proves Start acts on it.
//
// Swaps the package-level protectSocketFn, so it must not use t.Parallel().
func TestServerStart_ProtectFailureStopsStart(t *testing.T) {
	refused := errors.New("restrict socket: refused")
	prev := protectSocketFn
	protectSocketFn = func(string) error { return refused }
	t.Cleanup(func() { protectSocketFn = prev })

	sock := filepath.Join(t.TempDir(), "s")
	s := NewServer(sock, func(*Conn, *Message) {}, nil)
	err := s.Start()
	if !errors.Is(err, refused) {
		if err == nil {
			s.Stop()
		}
		t.Fatalf("Start() = %v, want the protect error", err)
	}
	if s.listener != nil {
		t.Error("Start kept a listener on a socket it could not restrict")
	}
	if c, err := NewClient(sock); err == nil {
		c.Close()
		t.Error("a client connected to a socket Start refused to serve")
	}
}
