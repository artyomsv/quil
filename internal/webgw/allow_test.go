package webgw

import (
	"encoding/json"
	"os"
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

func TestCheckForward_HelloCarriesTheGatewaysOwnProcessFields(t *testing.T) {
	g := newForwardGate("web-p-1", "9.9.9")
	forged := msg(t, ipc.MsgHello, "h1", ipc.HelloPayload{
		Kind: "web", Proto: ipc.ProtocolVersion, ClientID: "web-p-1",
		PID: 1, ExeName: "quil.exe", Version: "0.0.1",
	})
	for i, m := range []*ipc.Message{forged, forged} { // first hello, then a repeat
		fwd, refuse, fatal := g.check(m)
		if fwd == nil || refuse != nil || fatal != nil {
			t.Fatalf("hello %d: %v %v %v", i, fwd, refuse, fatal)
		}
		var h ipc.HelloPayload
		if err := json.Unmarshal(fwd.Payload, &h); err != nil {
			t.Fatal(err)
		}
		if h.PID != os.Getpid() || h.ExeName != g.exeName || h.ExeName == "quil.exe" || h.Version != "9.9.9" {
			t.Fatalf("hello %d forwarded with page-supplied fields: %+v", i, h)
		}
		if h.Kind != "web" || h.ClientID != "web-p-1" || fwd.ID != "h1" {
			t.Fatalf("hello %d lost its pinned fields: %+v id %q", i, h, fwd.ID)
		}
	}
}

func TestCheckForward_SecondHelloIsPinnedToo(t *testing.T) {
	g := &forwardGate{leasedID: "web-p-1", helloSeen: true}
	for name, m := range map[string]*ipc.Message{
		"other id":   helloFor(t, "web-p-2", "web"),
		"other kind": helloFor(t, "web-p-1", "tui"),
		"no id":      msg(t, ipc.MsgHello, "", ipc.HelloPayload{Kind: "web", ClientID: "web-p-1"}),
	} {
		fwd, refuse, fatal := g.check(m)
		if fwd != nil || refuse == nil || fatal != nil {
			t.Errorf("%s: fwd=%v refuse=%v fatal=%v", name, fwd, refuse, fatal)
		}
	}
}

func TestCheckForward_AttachWithoutClientIDIsRefused(t *testing.T) {
	g := &forwardGate{leasedID: "web-p-1", helloSeen: true}
	if fwd, refuse, _ := g.check(msg(t, ipc.MsgAttach, "a1", ipc.AttachPayload{})); fwd != nil || refuse == nil {
		t.Fatal("attach without a client id was forwarded")
	}
}

// A login's token id and nonce, and a working directory for new panes, are
// never the page's to send: the forwarded hello and attach drop them and keep
// everything else.
func TestCheckForward_DropsLoginFieldsAndAttachCWD(t *testing.T) {
	g := newForwardGate("web-p-1", "9.9.9")
	hello := msg(t, ipc.MsgHello, "h1", ipc.HelloPayload{
		Kind: "web", Proto: ipc.ProtocolVersion, ClientID: "web-p-1", TokenID: "tok-1", Nonce: "n-1",
	})
	fwd, _, fatal := g.check(hello)
	if fwd == nil || fatal != nil {
		t.Fatalf("hello: %v %v", fwd, fatal)
	}
	var h ipc.HelloPayload
	if err := json.Unmarshal(fwd.Payload, &h); err != nil || h.TokenID != "" || h.Nonce != "" || h.ClientID != "web-p-1" {
		t.Fatalf("forwarded hello %+v (%v)", h, err)
	}

	attach := msg(t, ipc.MsgAttach, "a1", ipc.AttachPayload{
		ClientID: "web-p-1", Cols: 80, Rows: 24, WinCols: 100, WinRows: 30, Reattach: true, CWD: "/home/someone",
	})
	fwd, refuse, fatal := g.check(attach)
	if fwd == nil || refuse != nil || fatal != nil {
		t.Fatalf("attach: %v %v %v", fwd, refuse, fatal)
	}
	var a ipc.AttachPayload
	if err := json.Unmarshal(fwd.Payload, &a); err != nil {
		t.Fatal(err)
	}
	want := ipc.AttachPayload{ClientID: "web-p-1", Cols: 80, Rows: 24, WinCols: 100, WinRows: 30, Reattach: true}
	if a != want || fwd.ID != "a1" {
		t.Fatalf("forwarded attach %+v id %q, want %+v", a, fwd.ID, want)
	}
}
