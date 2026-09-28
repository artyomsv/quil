package daemon

import (
	"os"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

func helloMsg(t *testing.T, id string, p ipc.HelloPayload) *ipc.Message {
	t.Helper()
	m, err := ipc.NewMessage(ipc.MsgHello, p)
	if err != nil {
		t.Fatal(err)
	}
	m.ID = id
	return m
}

func TestHandleMessage_Hello_AnswersAndRegistersProto(t *testing.T) {
	d, client := mcpTestDaemon(t)
	resp := roundTrip(t, client, ipc.MsgHello, ipc.MsgHelloResp, ipc.HelloPayload{
		Kind: "script", Proto: 1, ClientID: "c1", Version: "1.81.0", PID: os.Getpid(),
	})
	got := decodeInto[ipc.HelloRespPayload](t, resp)
	if got.Proto != ipc.ProtocolVersion || got.RunID != d.runID || len(got.Caps) == 0 {
		t.Errorf("hello_resp = %+v", got)
	}
}

func TestHandleMessage_BadHello_GetsBadPayloadError(t *testing.T) {
	_, client := mcpTestDaemon(t)
	resp := roundTrip(t, client, ipc.MsgHello, ipc.MsgError, ipc.HelloPayload{Kind: "", Proto: 1})
	if e := decodeInto[ipc.ErrorPayload](t, resp); e.Code != ipc.ErrCodeBadPayload || e.Type != ipc.MsgHello {
		t.Errorf("error = %+v, want bad_payload for hello", e)
	}
}

func TestHandleMessage_HelloedConn_UnknownType_GetsErrorWithSameID(t *testing.T) {
	_, client := mcpTestDaemon(t)
	roundTrip(t, client, ipc.MsgHello, ipc.MsgHelloResp, ipc.HelloPayload{Kind: "script", Proto: 1, PID: os.Getpid()})
	resp := roundTrip(t, client, "no_such_req", ipc.MsgError, struct{}{})
	if e := decodeInto[ipc.ErrorPayload](t, resp); e.Code != ipc.ErrCodeUnknownType || e.Type != "no_such_req" {
		t.Errorf("error = %+v", e)
	}
}

// Review focus 5: an old bridge never says hello and never checks reply
// types; it must never be sent an error frame.
func TestHandleMessage_LegacyConn_UnknownType_NoReply(t *testing.T) {
	_, client := mcpTestDaemon(t) // mcpTestDaemon sends only client_hello
	m, err := ipc.NewMessage("no_such_req", struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	m.ID = "legacy-1"
	if err := client.Send(m); err != nil {
		t.Fatal(err)
	}
	// Frames on one conn are answered in order, so an error for legacy-1
	// would arrive before the version_resp of this later request.
	// drainNoFrameWithID reads up to and including that version_resp.
	drainNoFrameWithID(t, client, "legacy-1")
}

func TestHandleMessage_HelloedConn_NoID_NoReply(t *testing.T) {
	_, client := mcpTestDaemon(t)
	roundTrip(t, client, ipc.MsgHello, ipc.MsgHelloResp, ipc.HelloPayload{Kind: "script", Proto: 1, PID: os.Getpid()})
	m, err := ipc.NewMessage("no_such_req", struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	// No ID set — deliberately.
	if err := client.Send(m); err != nil {
		t.Fatal(err)
	}
	drainNoFrameWithID(t, client, "")
}

func TestHandleMessage_ClientHelloAfterHello_KeepsProto(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	d := New(config.Default())
	conn := &ipc.Conn{}
	d.handleMessage(conn, helloMsg(t, "h1", ipc.HelloPayload{Kind: "tui", Proto: 1, PID: 9}))
	ch, err := ipc.NewMessage(ipc.MsgClientHello, ipc.ClientHelloPayload{Role: "tui", PID: 9})
	if err != nil {
		t.Fatal(err)
	}
	d.handleMessage(conn, ch)
	if d.hellos.protoOf(conn) != 1 {
		t.Error("client_hello after hello reset the conn to legacy")
	}
}

func TestHelloRegistry_PIDZero_NonLegacyButNotInDialog(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	d := New(config.Default())
	conn := &ipc.Conn{}
	d.handleMessage(conn, helloMsg(t, "h1", ipc.HelloPayload{Kind: "web", Proto: 1, PID: 0}))
	if d.hellos.protoOf(conn) != 1 {
		t.Error("a PID-less hello did not register the protocol")
	}
	rows, _ := d.hellos.describe([]*ipc.Conn{conn}, "1.81.0", time.Now())
	if len(rows) != 0 {
		t.Errorf("process dialog lists a PID-less client: %+v", rows)
	}
}

// drainNoFrameWithID sends a version_req with a fresh ID, then reads frames
// (5 s deadline) until that version_resp arrives. It fails the test on any
// frame read before it whose ID == id — or, when id == "", whose Type ==
// ipc.MsgError. The deadline is cleared before returning.
func drainNoFrameWithID(t *testing.T, client *ipc.Client, id string) {
	t.Helper()
	probe, err := ipc.NewMessage(ipc.MsgVersionReq, struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	probe.ID = "probe-" + time.Now().Format("150405.000000")
	if err := client.Send(probe); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("no %s arrived for probe %s", ipc.MsgVersionResp, probe.ID)
		default:
		}
		resp, err := client.Receive()
		if err != nil {
			t.Fatalf("receive: %v", err)
		}
		if resp.Type == ipc.MsgVersionResp && resp.ID == probe.ID {
			return
		}
		if id == "" {
			if resp.Type == ipc.MsgError {
				t.Errorf("unexpected error frame for id-less request: %+v", resp)
			}
			continue
		}
		if resp.ID == id {
			t.Errorf("unexpected frame for legacy request %s: %+v", id, resp)
		}
	}
}
