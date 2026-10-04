package webgw

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

func msg(t *testing.T, typ, id string, payload any) *ipc.Message {
	t.Helper()
	m, err := ipc.NewMessage(typ, payload)
	if err != nil {
		t.Fatal(err)
	}
	m.ID = id
	return m
}

func helloFor(t *testing.T, id, kind string) *ipc.Message {
	return msg(t, ipc.MsgHello, "h1", ipc.HelloPayload{Kind: kind, Proto: ipc.ProtocolVersion, ClientID: id})
}

func TestCheckForward_FirstMustBeWebHelloWithLeasedID(t *testing.T) {
	for name, m := range map[string]*ipc.Message{
		"not hello":   msg(t, ipc.MsgAttach, "a", ipc.AttachPayload{ClientID: "web-p-1"}),
		"kind tui":    helloFor(t, "web-p-1", "tui"),
		"other id":    helloFor(t, "web-p-2", "web"),
		"hello no id": msg(t, ipc.MsgHello, "", ipc.HelloPayload{Kind: "web", ClientID: "web-p-1"}),
	} {
		g := &forwardGate{leasedID: "web-p-1"}
		if _, _, fatal := g.check(m); fatal == nil {
			t.Errorf("%s: accepted as the first message", name)
		}
	}
	g := &forwardGate{leasedID: "web-p-1"}
	fwd, refuse, fatal := g.check(helloFor(t, "web-p-1", "web"))
	if fatal != nil || refuse != nil || fwd == nil {
		t.Fatalf("valid hello: %v %v %v", fwd, refuse, fatal)
	}
}

func TestCheckForward_RefusesUnlistedTypes(t *testing.T) {
	g := &forwardGate{leasedID: "web-p-1", helloSeen: true}
	for _, typ := range []string{"token_create_req", ipc.MsgShutdown, "create_pane", "destroy_pane", "reload_plugins", "kill_process_req", "update_layout", "subscribe", MsgWebWelcome, "invented_type"} {
		fwd, refuse, fatal := g.check(msg(t, typ, "r1", struct{}{}))
		if fwd != nil || fatal != nil || refuse == nil {
			t.Fatalf("%s: fwd=%v refuse=%v fatal=%v", typ, fwd, refuse, fatal)
		}
		var p ipc.ErrorPayload
		if err := json.Unmarshal(refuse.Payload, &p); err != nil || p.Code != ipc.ErrCodeRefused || refuse.ID != "r1" {
			t.Fatalf("%s: refusal %+v id %q", typ, p, refuse.ID)
		}
	}
}

func TestCheckForward_AttachMustCarryTheLeasedID(t *testing.T) {
	g := &forwardGate{leasedID: "web-p-1", helloSeen: true}
	if _, refuse, _ := g.check(msg(t, ipc.MsgAttach, "a1", ipc.AttachPayload{ClientID: "web-p-9"})); refuse == nil {
		t.Fatal("attach with another tab's id was forwarded")
	}
	if fwd, _, _ := g.check(msg(t, ipc.MsgAttach, "a2", ipc.AttachPayload{ClientID: "web-p-1"})); fwd == nil {
		t.Fatal("attach with the leased id refused")
	}
}

// Input and size messages go to the daemon id-less, whatever the page sent:
// an id-bearing pane_input is answered on the daemon's 64-slot must-deliver
// queue, one frame per keystroke.
func TestCheckForward_StripsIDsFromInputAndSize(t *testing.T) {
	g := &forwardGate{leasedID: "web-p-1", helloSeen: true}
	for _, typ := range []string{ipc.MsgPaneInput, ipc.MsgResizePanes, ipc.MsgClientGeometry} {
		fwd, _, _ := g.check(msg(t, typ, "x", struct{}{}))
		if fwd == nil || fwd.ID != "" {
			t.Fatalf("%s forwarded as %+v", typ, fwd)
		}
	}
}

func TestCheckForward_LargePasteFitsTheLimit(t *testing.T) {
	g := &forwardGate{leasedID: "web-p-1", helloSeen: true}
	big := strings.Repeat("x", 200<<10)
	fwd, refuse, fatal := g.check(msg(t, ipc.MsgPaneInput, "", ipc.PaneInputPayload{PaneID: "p", Data: []byte(big)}))
	if fwd == nil || refuse != nil || fatal != nil {
		t.Fatal("a 200 KB paste was not forwarded")
	}
}

func TestWebOpenPayload_DecodesHintAndKey(t *testing.T) {
	var p WebOpenPayload
	if err := json.Unmarshal([]byte(`{"client_id_hint":"web-ab-1","key":"k-1"}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.ClientIDHint != "web-ab-1" || p.Key != "k-1" {
		t.Fatalf("decoded %+v", p)
	}
}
