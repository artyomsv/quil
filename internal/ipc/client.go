package ipc

import (
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
