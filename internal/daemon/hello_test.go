package daemon

import (
	"os"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

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
	_, client := mcpTestDaemon(t)
	roundTrip(t, client, ipc.MsgHello, ipc.MsgHelloResp, ipc.HelloPayload{
		Kind: "tui", Proto: 1, PID: os.Getpid(),
	})
	sendNoID(t, client, ipc.MsgClientHello, ipc.ClientHelloPayload{Role: "tui", PID: os.Getpid()})
	// If client_hello following hello had reset the conn to legacy (proto 0),
	// this unknown type would get silence instead — replyError only answers
	// proto >= 1. Getting an error back is the on-the-wire proof that proto
	// survived the client_hello that came after the hello.
	resp := roundTrip(t, client, "no_such_req", ipc.MsgError, struct{}{})
	if e := decodeInto[ipc.ErrorPayload](t, resp); e.Code != ipc.ErrCodeUnknownType {
		t.Errorf("error = %+v, want unknown_type — client_hello after hello reset the conn to legacy", e)
	}
}

func TestHelloRegistry_PIDZero_NonLegacyButNotInDialog(t *testing.T) {
	_, client := mcpTestDaemon(t)
	// Baseline BEFORE the pid-less hello, on the same conn mcpTestDaemon
	// already registered via client_hello (role "bridge", a real PID) — this
	// is what "did not grow" below is measured against.
	before := roundTrip(t, client, ipc.MsgResourceReportReq, ipc.MsgResourceReportResp,
		ipc.ResourceReportReqPayload{WithTrees: true})
	baseline := decodeInto[ipc.ResourceReportRespPayload](t, before)

	roundTrip(t, client, ipc.MsgHello, ipc.MsgHelloResp, ipc.HelloPayload{
		Kind: "web", Proto: 1, PID: 0,
	})
	// Same on-the-wire proof as above: a PID-less hello must still register
	// the protocol, or this unknown type would get silence instead of an
	// error.
	resp := roundTrip(t, client, "no_such_req", ipc.MsgError, struct{}{})
	if e := decodeInto[ipc.ErrorPayload](t, resp); e.Code != ipc.ErrCodeUnknownType {
		t.Errorf("error = %+v, want unknown_type — a PID-less hello did not register the protocol", e)
	}

	report := roundTrip(t, client, ipc.MsgResourceReportReq, ipc.MsgResourceReportResp,
		ipc.ResourceReportReqPayload{WithTrees: true})
	got := decodeInto[ipc.ResourceReportRespPayload](t, report)
	// Asserting on row.Role == "web" is a check on a LABEL, not on the
	// invariant this test names — a future build that spells the role
	// differently (or never populates it) would pass whether or not a
	// PID-less row is actually excluded. The invariant is: no row reports
	// PID 0, and this conn's OWN row — already present in the baseline, from
	// mcpTestDaemon's client_hello — disappears rather than a second,
	// separate row for the same conn being added alongside it (describe()
	// skips a conn whose CURRENT record has PID<=0, so overwriting the
	// record removes the row rather than growing the list).
	for _, row := range got.Quil {
		if row.PID == 0 {
			t.Errorf("process dialog lists a PID-less row: %+v", row)
		}
	}
	if want := len(baseline.Quil) - 1; len(got.Quil) != want {
		t.Errorf("row count = %d after the web hello, want %d (this conn's own "+
			"row gone, everything else — including the daemon's own self row — "+
			"unchanged); a growing count means the pid-0 hello added a row "+
			"instead of only overwriting this conn's existing one",
			len(got.Quil), want)
	}
}

// drainNoFrameWithID sends a version_req with a fresh ID, then reads frames
// until that version_resp arrives, bounded by a 5 s deadline ON THE READ
// ITSELF (SetReadDeadline) rather than a bare time.After checked only
// between successful reads — Receive blocks with no timeout of its own, so
// only a real read deadline unparks one that never arrives, which is exactly
// the failure this helper exists to catch. It fails the test on any frame
// read before the version_resp whose ID == id — or, when id == "", whose
// Type == ipc.MsgError. The deadline is cleared before returning.
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
	if err := client.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	defer client.SetReadDeadline(time.Time{})
	for {
		resp, err := client.Receive()
		if err != nil {
			t.Fatalf("no %s arrived for probe %s: %v", ipc.MsgVersionResp, probe.ID, err)
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

// A malformed id-bearing pane_input is answered: the web gateway holds a
// paste place per id until the daemon answers it.
func TestHandleMessage_MalformedPaneInput_GetsBadPayloadError(t *testing.T) {
	_, client := mcpTestDaemon(t)
	roundTrip(t, client, ipc.MsgHello, ipc.MsgHelloResp, ipc.HelloPayload{Kind: "script", Proto: 1, PID: os.Getpid()})
	resp := roundTrip(t, client, ipc.MsgPaneInput, ipc.MsgError, map[string]any{"pane_id": 5})
	if e := decodeInto[ipc.ErrorPayload](t, resp); e.Code != ipc.ErrCodeBadPayload || e.Type != ipc.MsgPaneInput {
		t.Errorf("error = %+v, want bad_payload for pane_input", e)
	}
}
