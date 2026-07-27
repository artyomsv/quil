package ipc

import (
	"context"
	"errors"
	"net"
	"testing"
)

// TestNewClientWithDialer_UsesDialer_ReturnsWorkingClient proves the seam
// carries an arbitrary net.Conn, not just a Unix socket.
func TestNewClientWithDialer_UsesDialer_ReturnsWorkingClient(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	t.Cleanup(func() { clientSide.Close(); serverSide.Close() })

	called := false
	dial := func(ctx context.Context) (net.Conn, error) {
		called = true
		return clientSide, nil
	}

	c, err := NewClientWithDialer(context.Background(), dial)
	if err != nil {
		t.Fatalf("NewClientWithDialer: %v", err)
	}
	if !called {
		t.Fatal("dialer was not called")
	}

	// Server reads what the client sends.
	go func() {
		msg, _ := NewMessage(MsgHeartbeat, nil)
		_ = c.Send(msg)
	}()

	got, err := ReadMessage(serverSide)
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if got.Type != MsgHeartbeat {
		t.Errorf("Type = %q, want %q", got.Type, MsgHeartbeat)
	}
}

// TestNewClientWithDialer_DialError_Propagates checks the error is returned
// unwrapped enough for errors.Is to work at the call site.
func TestNewClientWithDialer_DialError_Propagates(t *testing.T) {
	sentinel := errors.New("boom")
	dial := func(ctx context.Context) (net.Conn, error) { return nil, sentinel }

	_, err := NewClientWithDialer(context.Background(), dial)
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want errors.Is(err, sentinel)", err)
	}
}
