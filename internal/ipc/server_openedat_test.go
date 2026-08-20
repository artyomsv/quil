package ipc

import (
	"net"
	"testing"
	"time"
)

// The daemon stubs OpenedAt in its own tests so it can age a connection without
// a real socket. This is the other half of that seam: production must actually
// populate the field, or the daemon's unidentified-client count silently treats
// every connection as ancient and reports probes as stale bridges.
func TestNewConn_PopulatesOpenedAt(t *testing.T) {
	c1, c2 := net.Pipe()
	t.Cleanup(func() {
		_ = c1.Close()
		_ = c2.Close()
	})

	before := time.Now()
	conn := newConn(c1)
	t.Cleanup(func() { _ = conn.Close() })
	after := time.Now()

	got := conn.OpenedAt()
	if got.IsZero() {
		t.Fatal("OpenedAt is zero — every connection would look infinitely old, " +
			"and short-lived probes would be counted as clients that predate " +
			"MsgClientHello")
	}
	if got.Before(before) || got.After(after) {
		t.Errorf("OpenedAt = %v, want between %v and %v", got, before, after)
	}
}
