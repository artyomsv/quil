package ipc

import (
	"encoding/json"
	"reflect"
	"testing"
)

// testMsgType is a generic must-deliver message type for transport tests.
// The transport tests used state_update and heartbeat for this, which were
// never sent by anything and are now deleted.
const testMsgType = "test_msg"

func TestHelloPayload_JSONKeys_Stable(t *testing.T) {
	b, err := json.Marshal(HelloPayload{
		Kind: "tui", Proto: 1, ClientID: "c", Version: "1.81.0",
		PID: 7, ExeName: "quil.exe", UptimeMS: 5, Caps: []string{"x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"kind", "proto", "client_id", "version", "pid", "exe", "uptime_ms", "caps"} {
		if _, ok := got[k]; !ok {
			t.Errorf("key %q missing from %s", k, b)
		}
	}
}

func TestErrorPayload_JSONKeys_Stable(t *testing.T) {
	b, err := json.Marshal(ErrorPayload{Code: ErrCodeUnknownType, Message: "m", Type: "foo_req"})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"code":"unknown_type","message":"m","type":"foo_req"}`
	if string(b) != want {
		t.Errorf("got %s, want %s", b, want)
	}
}

func TestDaemonCaps_IncludesGatedRequestsAndProtocolCaps(t *testing.T) {
	caps := DaemonCaps()
	want := append([]string{CapError, CapStateRev, CapStateReq}, GatedRequests...)
	if !reflect.DeepEqual(caps, want) {
		t.Errorf("DaemonCaps() = %v, want %v", caps, want)
	}
	caps[0] = "mutated"
	if DaemonCaps()[0] != CapError {
		t.Error("DaemonCaps returned a shared slice; callers can corrupt it")
	}
}

func TestProtocolVersion_IsOne(t *testing.T) {
	if ProtocolVersion != 1 {
		t.Errorf("ProtocolVersion = %d, want 1 for phase 3a", ProtocolVersion)
	}
}
