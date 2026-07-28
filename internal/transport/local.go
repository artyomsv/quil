package transport

import (
	"context"
	"net"
)

// Local returns a dialer for a daemon listening on a Unix socket on this
// machine — the transport quil has always used.
func Local(socketPath string) func(context.Context) (net.Conn, error) {
	return func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", socketPath)
	}
}
