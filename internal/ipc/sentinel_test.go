package ipc

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

// ErrSendOverflow's contract says future Sends short-circuit with the SAME
// error. Once the timeout path CAS-sets overflow, SendBlocking's own guard
// returns ErrConnClosed instead, so two callers hitting one cause got two
// different sentinels — the one that timed out and everyone behind it.
//
// Nothing branches on these today, which is exactly why it is worth pinning
// now: the first caller that does would see a distinction that describes
// scheduling rather than anything real.
func TestClientSend_PostOverflowSendsReturnTheSameSentinel(t *testing.T) {
	prev := clientSendTimeout
	clientSendTimeout = 100 * time.Millisecond
	t.Cleanup(func() { clientSendTimeout = prev })

	local, remote := net.Pipe()
	defer remote.Close() // never read — wedge the peer

	cl, err := NewClientWithDialer(context.Background(),
		func(context.Context) (net.Conn, error) { return local, nil })
	if err != nil {
		t.Fatalf("NewClientWithDialer: %v", err)
	}
	defer cl.Close()

	// Drive it into overflow.
	var first error
	for first == nil {
		msg, err := NewMessage(MsgStateUpdate, map[string]string{"x": "y"})
		if err != nil {
			t.Fatalf("NewMessage: %v", err)
		}
		first = cl.Send(msg)
	}
	if !errors.Is(first, ErrSendOverflow) {
		t.Fatalf("first failing Send = %v, want ErrSendOverflow", first)
	}

	// Every send after the flag is set must report the same cause.
	for i := 0; i < 5; i++ {
		msg, err := NewMessage(MsgStateUpdate, map[string]string{"x": "y"})
		if err != nil {
			t.Fatalf("NewMessage: %v", err)
		}
		if err := cl.Send(msg); !errors.Is(err, ErrSendOverflow) {
			t.Fatalf("post-overflow Send %d = %v, want ErrSendOverflow — a "+
				"caller behind the one that timed out sees a different "+
				"sentinel for the same event", i, err)
		}
	}
}

// The same thing from the concurrent direction: senders racing the timeout must
// not be able to observe two different causes for one overflow.
func TestClientSend_ConcurrentSendersAgreeOnTheSentinel(t *testing.T) {
	prev := clientSendTimeout
	clientSendTimeout = 100 * time.Millisecond
	t.Cleanup(func() { clientSendTimeout = prev })

	local, remote := net.Pipe()
	defer remote.Close()

	cl, err := NewClientWithDialer(context.Background(),
		func(context.Context) (net.Conn, error) { return local, nil })
	if err != nil {
		t.Fatalf("NewClientWithDialer: %v", err)
	}
	defer cl.Close()

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				msg, err := NewMessage(MsgStateUpdate, map[string]string{"x": "y"})
				if err != nil {
					errs <- err
					return
				}
				if err := cl.Send(msg); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if !errors.Is(err, ErrSendOverflow) {
			t.Errorf("concurrent Send = %v, want ErrSendOverflow", err)
		}
	}
}
