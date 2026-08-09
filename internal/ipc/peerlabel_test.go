package ipc

import (
	"net"
	"strings"
	"testing"
	"time"
)

// stubAddr is a net.Addr with whatever network and string we want, including
// the empty string a Unix socket reports for the far end.
type stubAddr struct {
	network string
	addr    string
}

func (s stubAddr) Network() string { return s.network }
func (s stubAddr) String() string  { return s.addr }

// stubConn supplies only the two methods peerLabel calls. The rest satisfy
// net.Conn and panic, so a future peerLabel that reaches for anything else
// fails loudly here rather than at a log site in production.
type stubConn struct {
	remote net.Addr
	local  net.Addr
}

func (c stubConn) RemoteAddr() net.Addr { return c.remote }
func (c stubConn) LocalAddr() net.Addr  { return c.local }

func (stubConn) Read([]byte) (int, error)         { panic("peerLabel must not read") }
func (stubConn) Write([]byte) (int, error)        { panic("peerLabel must not write") }
func (stubConn) Close() error                     { panic("peerLabel must not close") }
func (stubConn) SetDeadline(time.Time) error      { panic("peerLabel must not set deadlines") }
func (stubConn) SetReadDeadline(time.Time) error  { panic("peerLabel must not set deadlines") }
func (stubConn) SetWriteDeadline(time.Time) error { panic("peerLabel must not set deadlines") }

// peerLabel only ever feeds a log line, so a bug here cannot corrupt behaviour
// — but the line exists to answer "which side emitted this?", a question that
// previously took reasoning about which binaries initialise the logger. A label
// that silently degrades to "unknown" would put that cost straight back.
func TestPeerLabel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		conn net.Conn
		want string
	}{
		{
			name: "nil conn",
			conn: nil,
			want: "unknown",
		},
		{
			name: "remote address present",
			conn: stubConn{remote: stubAddr{"unix", "/tmp/quild.sock"}},
			want: "unix:/tmp/quild.sock",
		},
		{
			// The Unix-socket shape: the accepting side reports an empty
			// remote address, which is exactly when the local one identifies
			// the socket.
			name: "empty remote address falls back to local",
			conn: stubConn{
				remote: stubAddr{"unix", ""},
				local:  stubAddr{"unix", "/tmp/quild.sock"},
			},
			want: "unix:/tmp/quild.sock (local end)",
		},
		{
			name: "nil remote address falls back to local",
			conn: stubConn{local: stubAddr{"pipe", "pipe"}},
			want: "pipe:pipe (local end)",
		},
		{
			name: "both addresses absent",
			conn: stubConn{},
			want: "unknown",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := peerLabel(tt.conn); got != tt.want {
				t.Errorf("peerLabel = %q, want %q", got, tt.want)
			}
		})
	}
}

// A real connection must produce something identifying, not the "unknown"
// fallback — the table above uses stubs, so nothing in it would catch
// peerLabel disagreeing with an actual net.Conn.
func TestPeerLabel_RealPipeIsIdentified(t *testing.T) {
	t.Parallel()
	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()

	got := peerLabel(local)
	if got == "unknown" || !strings.Contains(got, "pipe") {
		t.Errorf("peerLabel(net.Pipe) = %q, want something naming the network", got)
	}
}
