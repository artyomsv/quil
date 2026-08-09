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

// Send queues a must-deliver frame, WAITING for room rather than tripping the
// slow-peer overflow.
//
// Conn's overflow→Close policy is a server defense: a daemon fanning out to
// many clients has to be able to drop one wedged peer instead of letting it
// block the rest. A client has no fan-out to protect, so applying that policy
// to its own send path means a backed-up outbound queue terminates the session
// — which is how a TUI holding 33 tabs and 36 panes killed itself three times
// in 70 seconds on 2026-08-09: one broadcast made it enqueue 69 must-deliver
// frames onto a 64-slot queue.
//
// Blocking is bounded, not indefinite: a genuinely wedged daemon still trips
// sendLoop's 30 s writeDeadline, which closes the conn, which makes this return
// ErrConnClosed and surfaces as a link loss. Callers are tea.Cmd goroutines and
// the input forwarder, where a brief park is strictly better than a disconnect.
func (c *Client) Send(msg *Message) error {
	return c.conn.SendBlocking(msg, nil)
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

// Flush waits for queued must-deliver frames to reach the socket. See
// (*Conn).Flush — Send is non-blocking, so closing straight after it discards
// frames the caller was told were accepted.
func (c *Client) Flush(timeout time.Duration) bool {
	return c.conn.Flush(timeout)
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
