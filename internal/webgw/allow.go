package webgw

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

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
// is the page's whole power: token management, shutdown, process kill, the
// raw create types (create_pane, create_pane_req, create_tab) and the
// fire-and-forget destroy_pane are absent on purpose. Add a type only with
// the UI that uses it (spec 5b §4.1).
var forwardable = map[string]bool{
	ipc.MsgHello: true, ipc.MsgAttach: true, ipc.MsgDetach: true, ipc.MsgStateReq: true,
	ipc.MsgPaneInput: true, ipc.MsgResizePanes: true, ipc.MsgClientGeometry: true,
	ipc.MsgTakeControl: true, ipc.MsgSwitchTab: true, ipc.MsgSwitchProject: true,
	ipc.MsgListClientsReq: true, ipc.MsgVersionReq: true, ipc.MsgListPanesReq: true,
	// 5b: workspace editing.
	ipc.MsgSplitPaneReq: true, ipc.MsgDestroyTab: true, ipc.MsgUpdateTab: true,
	ipc.MsgDestroyPaneReq: true, ipc.MsgUpdatePane: true, ipc.MsgUpdateLayout: true,
	ipc.MsgMovePane: true, ipc.MsgRestartPaneReq: true,
	// 5b: notifications.
	ipc.MsgDismissEvent: true, ipc.MsgGetNotificationsReq: true,
	// 5b: the create-pane dialog's daemon lists.
	ipc.MsgPluginListReq: true, ipc.MsgBrowseDirReq: true, ipc.MsgGitReposReq: true,
	ipc.MsgKubeCtxReq: true, ipc.MsgClaudeSessionsReq: true, ipc.MsgWorktreeListReq: true,
	ipc.MsgSandboxCapReq: true, ipc.MsgDirsExistReq: true,
}

// idless types are sent without an ID whatever the page set: the daemon
// answers an id-bearing frame of these on a 64-slot must-deliver queue.
// pane_input is handled apart: a keystroke goes id-less, a paste chunk keeps
// its id (capped, pasteCap).
var idless = map[string]bool{ipc.MsgResizePanes: true, ipc.MsgClientGeometry: true}

// needsID are the types whose only answer is to an id-bearing request; the
// page must be able to end its wait on the answer (spec 5b §5.1).
var needsID = map[string]bool{ipc.MsgDestroyTab: true, ipc.MsgUpdateTab: true}

// updatePaneFields are the update_pane fields the page may set. The others
// (cwd, eager, pinned_attention, marked_for_deletion) are the TUI's own
// reports or features the browser does not have.
var updatePaneFields = map[string]bool{"pane_id": true, "name": true, "muted": true, "overlay_visible": true, "unseen": true}

// pasteCap bounds the id-bearing pane_input frames one socket may have
// unanswered. The page keeps one in flight (spec 5b §4.3); two leaves room
// for a resend racing a late answer.
const pasteCap = 2

// ErrCodeBusy answers a paste chunk beyond pasteCap. The page waits and
// resends the same chunk, as for a full pane queue.
const ErrCodeBusy = "busy"

var errBadFirstMessage = errors.New("the first message must be hello of kind web with the leased client id and an ID")

// forwardGate checks one tab's messages on their way to the daemon.
type forwardGate struct {
	leasedID  string
	helloSeen bool
	// exeName and version describe the gateway process. They replace whatever
	// the page put in its hello, so list_clients shows the real process.
	exeName string
	version string
	// expand resolves a page's saved-instance id into its name and args from
	// the gateway's own disk (Server.expandInstance). Nil: an id is refused.
	expand func(pluginType, instanceID string) (name string, args []string, err error)
	// pastes holds the ids of unanswered paste chunks.
	pastes map[string]bool
}

// newForwardGate builds the gate for one tab; version is the gateway build's.
func newForwardGate(leasedID, version string, expand func(string, string) (string, []string, error)) *forwardGate {
	exe := "quil"
	if p, err := os.Executable(); err == nil {
		exe = filepath.Base(p)
	}
	return &forwardGate{leasedID: leasedID, exeName: exe, version: version, expand: expand}
}

// ownHello returns a copy of a validated hello whose process fields are the
// gateway's, not the page's. A login's token id and nonce are never the
// page's to send: they are blanked.
func (g *forwardGate) ownHello(m *ipc.Message, h ipc.HelloPayload) (*ipc.Message, error) {
	h.PID = os.Getpid()
	h.ExeName = g.exeName
	h.Version = g.version
	h.TokenID, h.Nonce = "", ""
	return withPayload(m, h)
}

// ownAttach returns a copy of a validated attach without a working
// directory: the daemon's default directory for new panes must never come
// from a browser page.
func ownAttach(m *ipc.Message, a ipc.AttachPayload) (*ipc.Message, error) {
	a.CWD = ""
	return withPayload(m, a)
}

func withPayload(m *ipc.Message, payload any) (*ipc.Message, error) {
	p, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	c := *m
	c.Payload = p
	return &c, nil
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
		c, err := g.ownHello(m, h)
		if err != nil {
			return nil, nil, errBadFirstMessage
		}
		g.helloSeen = true
		return c, nil, nil
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
		if m.ID == "" {
			return nil, refusal(m, "hello needs an ID"), nil
		}
		c, err := g.ownHello(m, h)
		if err != nil {
			return nil, refusal(m, "hello is malformed"), nil
		}
		return c, nil, nil
	case ipc.MsgAttach:
		var a ipc.AttachPayload
		if err := json.Unmarshal(m.Payload, &a); err != nil || a.ClientID != g.leasedID {
			return nil, refusal(m, "attach must name this tab's client id"), nil
		}
		c, err := ownAttach(m, a)
		if err != nil {
			return nil, refusal(m, "attach is malformed"), nil
		}
		return c, nil, nil
	}
	if needsID[m.Type] && m.ID == "" {
		return nil, refusal(m, m.Type+" needs an ID"), nil
	}
	switch m.Type {
	case ipc.MsgPaneInput:
		if m.ID == "" {
			return m, nil, nil
		}
		if len(g.pastes) >= pasteCap {
			return nil, busy(m), nil
		}
		if g.pastes == nil {
			g.pastes = map[string]bool{}
		}
		g.pastes[m.ID] = true
		return m, nil, nil
	case ipc.MsgUpdatePane:
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(m.Payload, &fields); err != nil {
			return nil, refusal(m, "update_pane is malformed"), nil
		}
		for k := range fields {
			if !updatePaneFields[k] {
				return nil, refusal(m, "update_pane may set only name, muted, overlay_visible and unseen"), nil
			}
		}
	case ipc.MsgUpdateLayout:
		var u ipc.UpdateLayoutPayload
		if err := json.Unmarshal(m.Payload, &u); err != nil || u.BaseRev == nil {
			return nil, refusal(m, "update_layout needs base_rev"), nil
		}
	case ipc.MsgSplitPaneReq:
		return g.ownSplit(m)
	}
	if idless[m.Type] {
		c := *m
		c.ID = ""
		return &c, nil, nil
	}
	return m, nil, nil
}

// pageSplit is split_pane_req as the PAGE sends it: the daemon's payload
// plus the gateway-only pane.instance_id. The outer Pane shadows the embedded
// one for JSON (Go's shallower-field rule).
type pageSplit struct {
	ipc.SplitPaneReqPayload
	Pane pageSplitPane `json:"pane"`
}

type pageSplitPane struct {
	ipc.SplitPaneSpec
	InstanceID string `json:"instance_id,omitempty"`
}

// ownSplit drops page-supplied instance args and fills them, with the
// instance's name, from the saved instance the page named by id, on the
// gateway's disk (spec 5b E7, Ruling R-B). The forwarded frame is re-encoded
// from the daemon's own type, so instance_id and unknown fields are gone.
func (g *forwardGate) ownSplit(m *ipc.Message) (fwd, refuse *ipc.Message, fatal error) {
	var in pageSplit
	if err := json.Unmarshal(m.Payload, &in); err != nil {
		return nil, refusal(m, "split_pane_req is malformed"), nil
	}
	r := in.SplitPaneReqPayload
	r.Pane = in.Pane.SplitPaneSpec
	r.Pane.InstanceArgs, r.Pane.InstanceName = nil, ""
	if id := in.Pane.InstanceID; id != "" {
		if g.expand == nil {
			return nil, refusal(m, "saved instances are not available"), nil
		}
		typ := r.Pane.Type
		if typ == "" {
			typ = "terminal"
		}
		name, args, err := g.expand(typ, id)
		if err != nil {
			return nil, refusal(m, err.Error()), nil
		}
		r.Pane.InstanceName, r.Pane.InstanceArgs = name, args
	}
	c, err := withPayload(m, r)
	if err != nil {
		return nil, refusal(m, "split_pane_req is malformed"), nil
	}
	return c, nil, nil
}

// answered frees a paste place when the daemon answers a chunk, with
// pane_input_resp or an error envelope carrying its id.
func (g *forwardGate) answered(m *ipc.Message) {
	if m.ID == "" || (m.Type != ipc.MsgPaneInputResp && m.Type != ipc.MsgError) {
		return
	}
	delete(g.pastes, m.ID)
}

func busy(m *ipc.Message) *ipc.Message {
	r, _ := ipc.NewMessage(ipc.MsgError, ipc.ErrorPayload{Code: ErrCodeBusy, Message: "two paste chunks are waiting for an answer", Type: m.Type})
	r.ID = m.ID
	return r
}

func refusal(m *ipc.Message, reason string) *ipc.Message {
	r, _ := ipc.NewMessage(ipc.MsgError, ipc.ErrorPayload{Code: ipc.ErrCodeRefused, Message: reason, Type: m.Type})
	r.ID = m.ID
	return r
}
