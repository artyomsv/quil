package ipc

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/artyomsv/quil/internal/logger"
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

// clientSendTimeout bounds how long a client-side Send waits for room on the
// critical queue before it treats the peer as wedged.
//
// It is a grace period, not a deadline for delivery. A daemon that is merely
// busy drains 64 frames in microseconds, so anything on this scale absorbs the
// bursts this exists for. The bound is what keeps the TUI responsive: see Send.
//
// A var, not a const, so tests can shrink it — a real wedge is the only way to
// reach the branch, and nothing else about the path is fast.
var clientSendTimeout = 5 * time.Second

// Send queues a must-deliver frame, WAITING for room rather than declaring the
// peer dead the instant the queue is full.
//
// Conn's overflow→Close policy is a server defense: a daemon fanning out to
// many clients has to be able to drop one wedged peer instead of letting it
// block the rest. A client has no fan-out to protect, so applying that policy
// to its own send path means a backed-up outbound queue terminates the session
// — which is how a TUI holding 33 tabs and 36 panes killed itself three times
// in 70 seconds on 2026-08-09: one broadcast made it enqueue 69 must-deliver
// frames onto a 64-slot queue.
//
// The wait is BOUNDED, and that half is load-bearing rather than defensive.
// Waiting on sendLoop's 30 s write deadline instead would park the input
// forwarder for 30 s against a wedged daemon; inputForwardBuffer is 1024 deep
// and enqueueInput blocks rather than drop a keystroke, so a parked forwarder
// that fills the buffer blocks the Update goroutine — the whole TUI, not just
// input. Failing inside clientSendTimeout keeps that bound small.
//
// On expiry the connection is CLOSED and ErrSendOverflow returned, matching
// what the caller saw before this change. A must-deliver frame that could not
// be delivered must not be reported as accepted: taking the link down surfaces
// the loss as a link error instead of dropping a keystroke into a void.
func (c *Client) Send(msg *Message) error {
	cancel := make(chan struct{})
	timer := time.AfterFunc(clientSendTimeout, func() { close(cancel) })
	defer timer.Stop()

	err := c.conn.SendBlocking(msg, cancel)
	if errors.Is(err, ErrSendCanceled) {
		// Same shape as enqueue's overflow branch: flag synchronously so every
		// later Send short-circuits at once, close on its own goroutine because
		// Close waits on sendLoop and the caller here may be the Update
		// goroutine. The CAS keeps one Close per connection.
		//
		// Logged with the same detail as the daemon-side branch. This is the
		// CLIENT-side overflow — the 2026-08-09 shape — so a silent give-up
		// here would leave exactly the incident this code exists for with no
		// line naming the peer or the depth.
		if c.conn.overflow.CompareAndSwap(false, true) {
			logger.Warn("ipc: giving up on a wedged peer after %s (peer=%s queued=%d cap=%d)",
				clientSendTimeout, peerLabel(c.conn.raw), len(c.conn.critCh), sendBufSize)
			go c.conn.Close()
		}
		return ErrSendOverflow
	}
	// A caller that lost the race to the CAS is reporting the SAME event as the
	// one that timed out, so it must report the same cause. SendBlocking's own
	// guard answers ErrConnClosed for an overflowed conn, which would otherwise
	// split one overflow across two sentinels depending on scheduling —
	// contradicting ErrSendOverflow's documented "future Sends short-circuit
	// with the same error".
	if c.conn.overflow.Load() && errors.Is(err, ErrConnClosed) {
		return ErrSendOverflow
	}
	return err
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
// (*Conn).Flush — Send returns once a frame is QUEUED, not once it is written,
// so closing straight after it discards frames the caller was told were
// accepted. Send's own wait is for room on that queue, not for delivery.
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
