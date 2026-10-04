package webgw

import (
	"encoding/binary"
	"errors"

	"github.com/artyomsv/quil/internal/ipc"
)

const (
	frameKindPaneOutput = 1
	frameFlagGhost      = 1
	frameHeaderLen      = 11 // kind, flags, generation (8), id length
)

// ErrBadPaneID is returned for an empty pane id or one over 255 bytes, which
// the one-byte length field cannot carry.
var ErrBadPaneID = errors.New("pane id must be 1..255 bytes")

// EncodePaneOutput turns a pane_output payload into the browser's binary
// frame: the raw terminal bytes travel as they are, without the base64 the
// JSON form needs. Layout: kind, flags (bit 0 = ghost replay), generation as
// big-endian uint64, pane id length, pane id, data.
func EncodePaneOutput(p ipc.PaneOutputPayload) ([]byte, error) {
	n := len(p.PaneID)
	if n == 0 || n > 255 {
		return nil, ErrBadPaneID
	}
	buf := make([]byte, frameHeaderLen+n+len(p.Data))
	buf[0] = frameKindPaneOutput
	if p.Ghost {
		buf[1] = frameFlagGhost
	}
	binary.BigEndian.PutUint64(buf[2:10], p.Generation)
	buf[10] = byte(n)
	copy(buf[frameHeaderLen:], p.PaneID)
	copy(buf[frameHeaderLen+n:], p.Data)
	return buf, nil
}
