package webgw

import (
	"context"

	"github.com/coder/websocket"
)

// wsPage adapts a WebSocket to pageConn. The bridge passes a context with the
// write timeout, so a page that stops reading fails the write instead of
// holding the tab.
type wsPage struct{ c *websocket.Conn }

func (w wsPage) WriteText(ctx context.Context, b []byte) error {
	return w.c.Write(ctx, websocket.MessageText, b)
}

func (w wsPage) WriteBinary(ctx context.Context, b []byte) error {
	return w.c.Write(ctx, websocket.MessageBinary, b)
}

func (w wsPage) Close(code int, reason string) {
	_ = w.c.Close(websocket.StatusCode(code), reason)
}
