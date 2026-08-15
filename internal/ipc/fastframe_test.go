package ipc

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// bs is a single backslash. Spelled this way because a literal one inside a
// JSON-in-Go test table is easy to lose to an editing pass, and losing it turns
// a "must decline" row into a valid frame that silently asserts the opposite.
var bs = string(rune(92))

// appendEnvelope must produce exactly what json.Marshal produces, or decline.
func TestAppendEnvelope_MatchesJSONMarshal(t *testing.T) {
	big := make([]byte, 8*1024)
	for i := range big {
		big[i] = byte('a' + i%26)
	}
	bigPayload, err := json.Marshal(PaneOutputPayload{PaneID: "pane-1a2b3c4d", Data: big})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	tests := []struct {
		name string
		msg  Message
	}{
		{"type only", Message{Type: MsgHeartbeat}},
		{"type and payload", Message{Type: MsgPaneOutput, Payload: json.RawMessage(`{"a":1}`)}},
		{"type id payload", Message{Type: MsgListPanesResp, ID: "req-42", Payload: json.RawMessage(`{"panes":[]}`)}},
		{"type and id", Message{Type: MsgDetach, ID: "x"}},
		{"empty type", Message{Type: "", Payload: json.RawMessage(`{}`)}},
		{"nil payload", Message{Type: "x", Payload: nil}},
		{"empty payload slice", Message{Type: "x", Payload: json.RawMessage{}}},
		{"literal null payload", Message{Type: "x", Payload: json.RawMessage(`null`)}},
		{"array payload", Message{Type: "x", Payload: json.RawMessage(`[1,2]`)}},
		{"8k pane output", Message{Type: MsgPaneOutput, Payload: bigPayload}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want, err := EncodeFrameSlow(&tt.msg)
			if err != nil {
				t.Fatalf("EncodeFrameSlow: %v", err)
			}
			got, ok := appendEnvelope(&tt.msg)
			if !ok {
				t.Fatalf("appendEnvelope declined a message it should handle")
			}
			if !bytes.Equal(want, got) {
				t.Errorf("frame mismatch\n want %q\n  got %q", want, got)
			}
		})
	}
}

// Anything needing JSON escaping must be declined, so the fallback renders it.
// The <, > and & rows are the ones that matter: encoding/json HTML-escapes them
// by default, and respondTo echoes any client-supplied ID.
func TestAppendEnvelope_DeclinesValuesNeedingEscaping(t *testing.T) {
	tests := []struct {
		name string
		id   string
	}{
		{"double quote", `a"b`},
		{"backslash", `a\b`},
		{"less than", "a<b"},
		{"greater than", "a>b"},
		{"ampersand", "a&b"},
		{"newline", "a\nb"},
		{"tab", "a\tb"},
		{"del", "a\x7fb"},
		{"nul", "a\x00b"},
		{"valid non-ascii", "café"},
		{"invalid utf8", "a\xffb"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := &Message{Type: "x", ID: tt.id}
			if _, ok := appendEnvelope(msg); ok {
				t.Fatalf("appendEnvelope accepted ID %q; it must decline", tt.id)
			}
			// And the public entry point must still produce the right bytes.
			want, err := EncodeFrameSlow(msg)
			if err != nil {
				t.Fatalf("EncodeFrameSlow: %v", err)
			}
			got, err := EncodeFrame(msg)
			if err != nil {
				t.Fatalf("EncodeFrame: %v", err)
			}
			if !bytes.Equal(want, got) {
				t.Errorf("frame mismatch\n want %q\n  got %q", want, got)
			}
		})
	}
}

// The same guard applies to Type, which is a constant today but need not stay one.
func TestAppendEnvelope_DeclinesTypeNeedingEscaping(t *testing.T) {
	if _, ok := appendEnvelope(&Message{Type: "a<b"}); ok {
		t.Fatal("appendEnvelope accepted a Type containing '<'")
	}
}

// A payload that is not compact json.Marshal output is declined rather than
// inlined, because json.Marshal would compact and re-escape it.
func TestAppendEnvelope_DeclinesNonCompactPayload(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{"leading space", ` {"a":1}`},
		{"trailing newline", "{\"a\":1}\n"},
		{"trailing space", `{"a":1} `},
		// Truncated and unterminated values. These are what a hand-built
		// payload realistically looks like when it is wrong, and inlining one
		// would put a malformed frame on a length-prefixed stream — the case
		// TestBroadcast_MarshalErrorLogsAndReturns guards.
		{"truncated object", `{not valid json`},
		{"truncated array", `[1,2`},
		{"unterminated string", `"abc`},
		{"partial null", `nul`},
		{"partial true", `tru`},
		{"mismatched delimiters", `{"a":1]`},
		{"bare word", `garbage`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := &Message{Type: "x", Payload: json.RawMessage(tt.payload)}
			if _, ok := appendEnvelope(msg); ok {
				t.Fatalf("appendEnvelope accepted non-compact payload %q", tt.payload)
			}
		})
	}
}

// maxFrameSize must be enforced identically by both paths.
func TestEncodeFrame_RejectsOversizeOnBothPaths(t *testing.T) {
	// A raw payload just over the limit. Valid compact JSON so only the size
	// check can reject it.
	body := strings.Repeat("a", maxFrameSize)
	payload := json.RawMessage(`"` + body + `"`)
	msg := &Message{Type: "x", Payload: payload}

	if _, ok := appendEnvelope(msg); ok {
		t.Fatal("appendEnvelope accepted an oversize frame")
	}
	_, fastErr := EncodeFrame(msg)
	_, slowErr := EncodeFrameSlow(msg)
	if fastErr == nil || slowErr == nil {
		t.Fatalf("expected both paths to error; fast=%v slow=%v", fastErr, slowErr)
	}
	if fastErr.Error() != slowErr.Error() {
		t.Errorf("error mismatch\n fast %v\n slow %v", fastErr, slowErr)
	}
}

// The hand-built encoder cannot see fields it fails to write, so the field set,
// ORDER and tags are pinned. Order is what makes type,id,payload the wire order;
// Origin's json:"-" is why Origin never reaches the wire.
func TestMessage_FieldLayoutIsPinned(t *testing.T) {
	want := []struct{ name, tag string }{
		{"Type", `json:"type"`},
		{"ID", `json:"id,omitempty"`},
		{"Payload", `json:"payload,omitempty"`},
		{"Origin", `json:"-"`},
	}
	typ := reflect.TypeOf(Message{})
	if typ.NumField() != len(want) {
		t.Fatalf("Message has %d fields, expected %d — update appendEnvelope and this test together",
			typ.NumField(), len(want))
	}
	for i, w := range want {
		f := typ.Field(i)
		if f.Name != w.name {
			t.Errorf("field %d: name %q, want %q", i, f.Name, w.name)
		}
		if got := string(f.Tag); got != w.tag {
			t.Errorf("field %s: tag %q, want %q", f.Name, got, w.tag)
		}
	}
}

// Round trip through both encoders and both decoders, in all four combinations.
// The cross pairs are what "no version bump needed" means.
func TestParseEnvelope_RoundTripsAllCombinations(t *testing.T) {
	big := make([]byte, 8*1024)
	for i := range big {
		big[i] = byte(i % 251)
	}
	msg, err := NewMessage(MsgPaneOutput, PaneOutputPayload{PaneID: "pane-1a2b3c4d", Data: big, Ghost: true})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}

	fast, ok := appendEnvelope(msg)
	if !ok {
		t.Fatal("appendEnvelope declined a plain pane_output message")
	}
	slow, err := EncodeFrameSlow(msg)
	if err != nil {
		t.Fatalf("EncodeFrameSlow: %v", err)
	}

	for _, enc := range []struct {
		name  string
		frame []byte
	}{{"fast encoder", fast}, {"slow encoder", slow}} {
		body := enc.frame[4:]

		var viaJSON Message
		if err := json.Unmarshal(body, &viaJSON); err != nil {
			t.Fatalf("%s: json.Unmarshal: %v", enc.name, err)
		}
		var viaFast Message
		if !parseEnvelope(body, &viaFast) {
			t.Fatalf("%s: parseEnvelope declined its own shape", enc.name)
		}
		if viaFast.Type != viaJSON.Type || viaFast.ID != viaJSON.ID {
			t.Errorf("%s: envelope mismatch: %q/%q vs %q/%q",
				enc.name, viaFast.Type, viaFast.ID, viaJSON.Type, viaJSON.ID)
		}
		if !bytes.Equal(viaFast.Payload, viaJSON.Payload) {
			t.Errorf("%s: payload mismatch", enc.name)
		}
	}
}

// Non-conformance living INSIDE the span the fast path does not scan.
// json.Unmarshal accepts all of these today (key order is free, duplicate keys
// are last-wins), so accepting-and-mis-slicing would be a behaviour change.
func TestParseEnvelope_DeclinesKeysAfterPayload(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"id after payload", `{"type":"pane_output","payload":{},"id":"x"}`},
		{"object after payload", `{"type":"pane_output","payload":{},"x":{}}`},
		{"nested object payload", `{"type":"pane_output","payload":{"a":{"b":1}}}`},
		{"duplicate type", `{"type":"pane_output","payload":{},"type":"other"}`},
		{"array payload", `{"type":"pane_output","payload":[1,2]}`},
		{"string payload", `{"type":"pane_output","payload":"x"}`},
		{"null payload", `{"type":"pane_output","payload":null}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m Message
			if parseEnvelope([]byte(tt.body), &m) {
				t.Fatalf("parseEnvelope accepted %s; it must decline", tt.body)
			}
		})
	}
}

// The fast decoder must never be MORE lenient than encoding/json.
func TestParseEnvelope_DeclinesMalformedStrings(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"control byte in type", "{\"type\":\"pane\x01output\",\"payload\":{}}"},
		// _ is an underscore, so encoding/json decodes this Type to exactly
		// "pane_output". The fast path must still decline: it does not process
		// escapes, so returning the raw bytes would yield a different Type.
		{"unicode escape in type", `{"type":"pane` + bs + `u005foutput","payload":{}}`},
		{"escape in id", `{"type":"pane_output","id":"a\\b","payload":{}}`},
		{"unterminated type", `{"type":"pane_output`},
		{"no trailing brace", `{"type":"pane_output","payload":{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m Message
			if parseEnvelope([]byte(tt.body), &m) {
				t.Fatalf("parseEnvelope accepted %q; it must decline", tt.body)
			}
		})
	}
}

// Gating: only pane_output takes the fast path, because it is the only type
// whose payload span can be validated without scanning it.
func TestParseEnvelope_DeclinesNonPaneOutput(t *testing.T) {
	var m Message
	if parseEnvelope([]byte(`{"type":"list_panes_resp","payload":{}}`), &m) {
		t.Fatal("parseEnvelope accepted a non-pane_output type")
	}
}

// A declined call must leave the caller's Message untouched for the fallback.
func TestParseEnvelope_LeavesMessageUntouchedOnDecline(t *testing.T) {
	m := Message{Type: "sentinel", ID: "keep", Payload: json.RawMessage(`{"a":1}`)}
	if parseEnvelope([]byte(`{"type":"pane_output","payload":[1,2]}`), &m) {
		t.Fatal("expected decline")
	}
	if m.Type != "sentinel" || m.ID != "keep" || string(m.Payload) != `{"a":1}` {
		t.Errorf("declined call mutated the message: %+v", m)
	}
}

// ReadMessage must return identical results whichever path ran.
func TestReadMessage_MatchesJSONForBothPaths(t *testing.T) {
	msgs := []*Message{
		mustMessage(t, MsgPaneOutput, PaneOutputPayload{PaneID: "pane-1", Data: []byte("hi"), Ghost: true}),
		mustMessage(t, MsgPaneOutput, PaneOutputPayload{PaneID: "pane-2", Data: []byte{0, 255, '\n'}}),
		mustMessage(t, MsgListPanesResp, ListPanesRespPayload{Panes: []PaneInfo{{ID: "p"}}}),
	}
	for _, want := range msgs {
		frame, err := EncodeFrame(want)
		if err != nil {
			t.Fatalf("EncodeFrame: %v", err)
		}
		got, err := ReadMessage(bytes.NewReader(frame))
		if err != nil {
			t.Fatalf("ReadMessage: %v", err)
		}
		if got.Type != want.Type || got.ID != want.ID {
			t.Errorf("envelope mismatch: %q/%q vs %q/%q", got.Type, got.ID, want.Type, want.ID)
		}
		if !bytes.Equal(got.Payload, want.Payload) {
			t.Errorf("payload mismatch for %s", want.Type)
		}
	}
}

// The aliased Payload must not be able to reach the frame's trailing bytes via
// append. The three-index slice caps capacity, so this reallocates.
func TestParseEnvelope_PayloadCapacityIsCapped(t *testing.T) {
	msg := mustMessage(t, MsgPaneOutput, PaneOutputPayload{PaneID: "p", Data: []byte("hi")})
	frame, err := EncodeFrame(msg)
	if err != nil {
		t.Fatalf("EncodeFrame: %v", err)
	}
	body := frame[4:]
	var m Message
	if !parseEnvelope(body, &m) {
		t.Fatal("parseEnvelope declined")
	}
	if cap(m.Payload) != len(m.Payload) {
		t.Errorf("payload capacity %d exceeds length %d — append could reach the frame tail",
			cap(m.Payload), len(m.Payload))
	}
	tailBefore := string(body[len(body)-1:])
	m.Payload = append(m.Payload, 'X')
	if got := string(body[len(body)-1:]); got != tailBefore {
		t.Errorf("append to Payload overwrote the frame tail: %q -> %q", tailBefore, got)
	}
}

func mustMessage(t *testing.T, typ string, payload any) *Message {
	t.Helper()
	msg, err := NewMessage(typ, payload)
	if err != nil {
		t.Fatalf("NewMessage(%s): %v", typ, err)
	}
	return msg
}

// The fast payload decoder must agree with encoding/json on every shape,
// including the ones where encoding/json ERRORS.
func TestDecodePayload_PaneOutputMatchesJSON(t *testing.T) {
	tests := []struct {
		name    string
		payload PaneOutputPayload
	}{
		{"plain", PaneOutputPayload{PaneID: "pane-1a2b3c4d", Data: []byte("hello")}},
		{"ghost", PaneOutputPayload{PaneID: "pane-1", Data: []byte("x"), Ghost: true}},
		{"binary", PaneOutputPayload{PaneID: "pane-1", Data: []byte{0x00, 0xFF, 0x1B, '\n'}}},
		{"empty data", PaneOutputPayload{PaneID: "pane-1", Data: []byte{}}},
		{"empty pane id", PaneOutputPayload{PaneID: "", Data: []byte("x")}},
		{"8k", PaneOutputPayload{PaneID: "pane-1a2b3c4d", Data: bytes.Repeat([]byte("ab"), 4096)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg, err := NewMessage(MsgPaneOutput, tt.payload)
			if err != nil {
				t.Fatalf("NewMessage: %v", err)
			}
			var viaJSON PaneOutputPayload
			jsonErr := json.Unmarshal(msg.Payload, &viaJSON)
			var viaFast PaneOutputPayload
			fastErr := msg.DecodePayload(&viaFast)

			if (jsonErr == nil) != (fastErr == nil) {
				t.Fatalf("error disagreement: json=%v fast=%v", jsonErr, fastErr)
			}
			if viaFast.PaneID != viaJSON.PaneID || viaFast.Ghost != viaJSON.Ghost {
				t.Errorf("field mismatch: %+v vs %+v", viaFast, viaJSON)
			}
			if !bytes.Equal(viaFast.Data, viaJSON.Data) {
				t.Errorf("data mismatch: %q vs %q", viaFast.Data, viaJSON.Data)
			}
		})
	}
}

// Failure and oddity shapes. Each must produce the same outcome DecodePayload
// produced before this change.
func TestDecodePayload_PaneOutputFailureShapes(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{"data null", `{"pane_id":"p","data":null}`},
		{"invalid base64", `{"pane_id":"p","data":"!!!!"}`},
		{"unknown extra key", `{"pane_id":"p","data":"aGk=","extra":1}`},
		{"duplicate key", `{"pane_id":"p","data":"aGk=","data":"Ynll"}`},
		{"ghost false explicit", `{"pane_id":"p","data":"aGk=","ghost":false}`},
		{"reordered keys", `{"data":"aGk=","pane_id":"p"}`},
		{"empty payload", ``},
		{"not an object", `"nope"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := &Message{Type: MsgPaneOutput, Payload: json.RawMessage(tt.payload)}
			var viaJSON PaneOutputPayload
			jsonErr := json.Unmarshal(msg.Payload, &viaJSON)
			var viaFast PaneOutputPayload
			fastErr := msg.DecodePayload(&viaFast)

			if (jsonErr == nil) != (fastErr == nil) {
				t.Fatalf("error disagreement for %q: json=%v fast=%v", tt.payload, jsonErr, fastErr)
			}
			if jsonErr != nil {
				return
			}
			if viaFast.PaneID != viaJSON.PaneID || viaFast.Ghost != viaJSON.Ghost || !bytes.Equal(viaFast.Data, viaJSON.Data) {
				t.Errorf("mismatch for %q: %+v vs %+v", tt.payload, viaFast, viaJSON)
			}
		})
	}
}

// A nil payload must keep erroring: internal/tui/model.go:5955 IGNORES
// DecodePayload's error, so a fast path returning nil-success here would
// silently hand that call site a zero-valued payload.
func TestDecodePayload_EmptyPayloadStillErrors(t *testing.T) {
	msg := &Message{Type: MsgPaneOutput}
	var p PaneOutputPayload
	if err := msg.DecodePayload(&p); err == nil {
		t.Fatal("expected an error for a nil payload, got nil")
	}
}

// Other payload types are untouched by the type switch.
func TestDecodePayload_OtherTypesUnaffected(t *testing.T) {
	msg, err := NewMessage(MsgListPanesResp, ListPanesRespPayload{Panes: []PaneInfo{{ID: "p1"}}})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	var got ListPanesRespPayload
	if err := msg.DecodePayload(&got); err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if len(got.Panes) != 1 || got.Panes[0].ID != "p1" {
		t.Errorf("unexpected result: %+v", got)
	}
}

// TestFastPaths_KnownValidityLimits pins the boundary of the fast-path contract
// so it is visible in the suite rather than only in a fuzzer's skip branch.
//
// The fast paths check delimiters and field shape, not validity. Closing that
// needs a real parse — json.Valid costs 47 us on an 8 KB pane_output payload,
// which would give back most of what this change buys — so the soundness
// argument rests on NewMessage being the only production producer of
// Message.Payload. These cases are reachable ONLY by hand-constructing a
// Message, which is what tests do and no production path does.
//
// If one of these ever starts failing because the fast path got stricter, that
// is an improvement: delete the row.
func TestFastPaths_KnownValidityLimits(t *testing.T) {
	t.Run("encoder inlines a balanced but invalid payload", func(t *testing.T) {
		msg := &Message{Type: "x", Payload: json.RawMessage(`[A]`)}
		if _, err := EncodeFrameSlow(msg); err == nil {
			t.Fatal("precondition: the reference encoder should reject this payload")
		}
		frame, err := EncodeFrame(msg)
		if err != nil {
			t.Skip("fast path now rejects this — delete this row")
		}
		// The error is MOVED, not removed: the frame is detectably bad.
		if _, err := ReadMessage(bytes.NewReader(frame)); err == nil {
			t.Error("a frame built from an invalid payload decoded cleanly — the badness was swallowed")
		}
	})

	t.Run("decoder passes through an invalid payload interior", func(t *testing.T) {
		body := []byte(`{"type":"pane_output","payload":{"pane_id":"p" garbage}}`)
		if err := json.Unmarshal(body, new(Message)); err == nil {
			t.Fatal("precondition: encoding/json should reject this frame")
		}
		var m Message
		if !parseEnvelope(body, &m) {
			t.Skip("fast path now rejects this — delete this row")
		}
		// The error is MOVED to DecodePayload, which every handler checks or
		// safely ignores. It must not decode cleanly.
		var p PaneOutputPayload
		if err := m.DecodePayload(&p); err == nil {
			t.Error("an invalid payload decoded cleanly — the badness was swallowed")
		}
	})
}

// TestPaneOutputPayload_FastPathStaysEngaged guards the coupling between
// PaneOutputPayload's wire shape and the two places that hard-code it:
// paneOutputSpan's `{"pane_id":"` prefix, and decodePaneOutput's `,"data":"`
// key and exact trailing-key set.
//
// This failure mode is SILENT without a test. Add a field, reorder two, or
// change a tag, and both fast paths simply stop matching — they decline, the
// encoding/json fallback runs, everything stays correct, and the ~40x encode
// and ~9x receive improvement evaporates with nothing going red. That is worse
// than a break, because nobody goes looking.
//
// The reflection half names what to update; the round-trip half is the one that
// actually proves the fast path is still being taken.
func TestPaneOutputPayload_FastPathStaysEngaged(t *testing.T) {
	t.Run("wire shape is pinned", func(t *testing.T) {
		want := []struct{ name, tag string }{
			{"PaneID", `json:"pane_id"`},
			{"Data", `json:"data"`},
			{"Ghost", `json:"ghost,omitempty"`},
		}
		typ := reflect.TypeOf(PaneOutputPayload{})
		if typ.NumField() != len(want) {
			t.Fatalf("PaneOutputPayload has %d fields, expected %d — paneOutputSpan and decodePaneOutput hard-code this shape and must be updated with it",
				typ.NumField(), len(want))
		}
		for i, w := range want {
			f := typ.Field(i)
			if f.Name != w.name {
				t.Errorf("field %d: name %q, want %q", i, f.Name, w.name)
			}
			if got := string(f.Tag); got != w.tag {
				t.Errorf("field %s: tag %q, want %q", f.Name, got, w.tag)
			}
		}
	})

	t.Run("fast paths still engage on a real message", func(t *testing.T) {
		for _, ghost := range []bool{false, true} {
			msg, err := NewMessage(MsgPaneOutput, PaneOutputPayload{
				PaneID: "pane-1a2b3c4d", Data: []byte("hello"), Ghost: ghost,
			})
			if err != nil {
				t.Fatalf("NewMessage: %v", err)
			}
			frame, ok := appendEnvelope(msg)
			if !ok {
				t.Fatalf("ghost=%v: appendEnvelope declined a NewMessage-produced pane_output frame", ghost)
			}
			var got Message
			if !parseEnvelope(frame[4:], &got) {
				t.Fatalf("ghost=%v: parseEnvelope declined a frame this package just produced", ghost)
			}
			var p PaneOutputPayload
			if !decodePaneOutput(got.Payload, &p) {
				t.Fatalf("ghost=%v: decodePaneOutput declined a payload NewMessage just marshalled", ghost)
			}
			if p.PaneID != "pane-1a2b3c4d" || string(p.Data) != "hello" || p.Ghost != ghost {
				t.Errorf("ghost=%v: round trip changed the value: %+v", ghost, p)
			}
		}
	})
}
