package ipc

import "net"

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

func (c *Client) Close() error {
	return c.conn.Close()
}
