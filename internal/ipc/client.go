package ipc

import (
	"context"
	"net"
	"time"
)

// Client connects to the daemon over a Unix socket.
type Client struct {
	conn *Conn
}

func NewClient(socketPath string) (*Client, error) {
	raw, err := net.Dial("unix", socketPath)
	if err != nil {
		return nil, err
	}
	return &Client{conn: newConn(raw)}, nil
}

func (c *Client) Send(msg *Message) error {
	return c.conn.Send(msg)
}

func (c *Client) Receive() (*Message, error) {
	return c.conn.Receive()
}

// SetReadDeadline installs a read deadline on the underlying socket.
// Pass the zero time to disable it. Used by the pre-attach version
// handshake to cap how long we wait for MsgVersionResp from daemons
// that may predate the version-negotiation protocol.
func (c *Client) SetReadDeadline(t time.Time) error {
	return c.conn.raw.SetReadDeadline(t)
}

func (c *Client) Close() error {
	return c.conn.Close()
}

// DialFunc establishes one transport-level connection to a daemon. It is the
// seam that lets a Client run over something other than a Unix socket (an SSH
// channel today, a TLS connection later) without the protocol layer knowing.
//
// CONTRACT: ctx bounds the dial only. Once a DialFunc returns a net.Conn, that
// conn owns any underlying process or socket and releases it on Close —
// cancelling ctx afterwards must not disturb a live connection. Reconnect loops
// depend on this: they dial under a per-attempt timeout with a deferred cancel,
// and would otherwise destroy each session as they created it.
type DialFunc func(ctx context.Context) (net.Conn, error)

// NewClientWithDialer builds a Client over whatever connection dial returns.
// NewClient remains the Unix-socket convenience wrapper used by every local
// call site.
func NewClientWithDialer(ctx context.Context, dial DialFunc) (*Client, error) {
	raw, err := dial(ctx)
	if err != nil {
		return nil, err
	}
	return &Client{conn: newConn(raw)}, nil
}
