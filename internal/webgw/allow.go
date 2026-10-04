package webgw

import (
	"encoding/json"
	"errors"

	"github.com/artyomsv/quil/internal/ipc"
)

// Gateway-local messages: handled by the gateway, never sent to the daemon.
const (
	MsgWebOpen    = "web_open"
	MsgWebWelcome = "web_welcome"
	MsgWebAck     = "web_ack"
)

// WebOpenPayload is the first frame a page sends. Key is the port-scoped key
// the login returned; the server checks it before any daemon dial.
type WebOpenPayload struct {
	ClientIDHint string `json:"client_id_hint"`
	Key          string `json:"key"`
}

type WebWelcomePayload struct {
	ClientID string `json:"client_id"`
	Rights   string `json:"rights"`
	Version  string `json:"version"`
}

type WebAckPayload struct {
	Bytes int64 `json:"bytes"`
}

// forwardable is everything the browser client may send to the daemon. In
// the default mode each tab is a local client with full rights, so this list
// is the page's whole power: token management, shutdown, process kill and
// every create or destroy type are absent on purpose. Add a type only with
// the UI that uses it.
var forwardable = map[string]bool{
	ipc.MsgHello: true, ipc.MsgAttach: true, ipc.MsgDetach: true, ipc.MsgStateReq: true,
	ipc.MsgPaneInput: true, ipc.MsgResizePanes: true, ipc.MsgClientGeometry: true,
	ipc.MsgTakeControl: true, ipc.MsgSwitchTab: true, ipc.MsgSwitchProject: true,
	ipc.MsgListClientsReq: true, ipc.MsgVersionReq: true, ipc.MsgListPanesReq: true,
}

// idless types are sent without an ID whatever the page set: the daemon
// answers an id-bearing pane_input per keystroke on a 64-slot queue.
var idless = map[string]bool{ipc.MsgPaneInput: true, ipc.MsgResizePanes: true, ipc.MsgClientGeometry: true}

var errBadFirstMessage = errors.New("the first message must be hello of kind web with the leased client id and an ID")

// forwardGate checks one tab's messages on their way to the daemon.
type forwardGate struct {
	leasedID  string
	helloSeen bool
}

// check returns the message to forward, or a refusal to send back to the
// page, or a fatal error that closes the tab.
func (g *forwardGate) check(m *ipc.Message) (fwd, refuse *ipc.Message, fatal error) {
	if !g.helloSeen {
		if m.Type != ipc.MsgHello || m.ID == "" {
			return nil, nil, errBadFirstMessage
		}
		var h ipc.HelloPayload
		if err := json.Unmarshal(m.Payload, &h); err != nil || h.Kind != "web" || h.ClientID != g.leasedID {
			return nil, nil, errBadFirstMessage
		}
		g.helloSeen = true
		return m, nil, nil
	}
	if !forwardable[m.Type] {
		return nil, refusal(m, "not available in the web client"), nil
	}
	switch m.Type {
	case ipc.MsgHello:
		var h ipc.HelloPayload
		if err := json.Unmarshal(m.Payload, &h); err != nil || h.Kind != "web" || h.ClientID != g.leasedID {
			return nil, refusal(m, "hello must name this tab's client id"), nil
		}
	case ipc.MsgAttach:
		var a ipc.AttachPayload
		if err := json.Unmarshal(m.Payload, &a); err != nil || a.ClientID != g.leasedID {
			return nil, refusal(m, "attach must name this tab's client id"), nil
		}
	}
	if idless[m.Type] {
		c := *m
		c.ID = ""
		return &c, nil, nil
	}
	return m, nil, nil
}

func refusal(m *ipc.Message, reason string) *ipc.Message {
	r, _ := ipc.NewMessage(ipc.MsgError, ipc.ErrorPayload{Code: ipc.ErrCodeRefused, Message: reason, Type: m.Type})
	r.ID = m.ID
	return r
}
