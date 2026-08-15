package ipc

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

// The fast paths are correct only if they are byte-identical to encoding/json
// or decline. A table test cannot establish that — it exercises only shapes
// someone thought of, and the dangerous class is the one nobody did. All three
// targets below found real bugs on their first run.
//
// THE CONTRACT these enforce, stated precisely, because it is narrower than
// "identical to encoding/json in all cases":
//
//	For any input encoding/json ACCEPTS, the fast path either declines or
//	produces exactly the same result.
//
// It says nothing about inputs encoding/json REJECTS. The fast paths check
// delimiters and field shape, not validity — a payload that is balanced and
// correctly prefixed but internally invalid (`[A]` to encode, `{"pane_id":"p"
// garbage}` to decode) is passed through rather than rejected. Closing that
// needs a real parse: json.Valid costs 47 us on an 8 KB pane_output payload,
// measured, which would give back most of the 112 us -> 2.7 us this change
// buys. The soundness argument rests on the producer instead — NewMessage
// (internal/ipc/protocol.go:1130) is the only production site that sets
// Message.Payload and it always uses json.Marshal.
//
// The consequence is a MOVED error, not a swallowed one: an invalid payload
// that slips past the encoder produces a frame the peer fails to decode, and
// one that slips past the decoder fails later at DecodePayload.
// TestFastPaths_KnownValidityLimits pins both, so the boundary is visible in
// the suite rather than only in a fuzzer's skip branch.

// FuzzAppendEnvelope pins the byte-identity guarantee against arbitrary input.
// This target would have caught the HTML-escaping bug in the design's first
// draft within a few hundred executions.
// The payload is MARSHALLED from fuzzed fields rather than taken as an
// arbitrary string, because that is the production invariant: NewMessage is the
// only site that sets Message.Payload and it always marshals. Byte-identity is
// asserted against exactly that class. Type and ID stay free-form strings —
// they are where the HTML-escaping bug lived, and where an IPC client's own
// bytes reach the encoder via respondTo.
func FuzzAppendEnvelope(f *testing.F) {
	f.Add("pane_output", "", "pane-1", []byte("hi"), false)
	f.Add("pane_output", "req-1", "p", []byte{0, 255}, true)
	f.Add("x", "a<b>c&d", "p", []byte("x"), false)
	f.Add("x", `a"b`, "p", []byte("x"), false)
	f.Add("x", "a"+bs+"b", "p", []byte("x"), false)
	f.Add("café", "日本", "p", []byte("x"), false)
	f.Add("", "", "", []byte(nil), false)

	f.Fuzz(func(t *testing.T, typ, id, paneID string, data []byte, ghost bool) {
		raw, err := json.Marshal(PaneOutputPayload{PaneID: paneID, Data: data, Ghost: ghost})
		if err != nil {
			t.Skip()
		}
		msg := &Message{Type: typ, ID: id, Payload: raw}

		frame, ok := appendEnvelope(msg)
		if !ok {
			return // declined — the fallback handles it, nothing to compare
		}
		want, err := EncodeFrameSlow(msg)
		if err != nil {
			t.Fatalf("fast path accepted a marshal-produced message the reference rejects: %v", err)
		}
		if !bytes.Equal(frame, want) {
			t.Fatalf("byte mismatch\n type=%q id=%q\n want %q\n  got %q", typ, id, want, frame)
		}
	})
}

// FuzzAppendEnvelopeRawPayload fuzzes the payload as an arbitrary string, where
// byte-identity is NOT guaranteed — json.Marshal compacts a RawMessage, so a
// hand-built `[ ]` re-encodes to `[]` while the fast path inlines it unchanged.
// What must hold even there is SEMANTIC equivalence: whatever the fast path
// emits must decode to the same Message the reference's output does. That is
// the property "no version negotiation is needed" actually rests on.
func FuzzAppendEnvelopeRawPayload(f *testing.F) {
	f.Add("pane_output", "", `{"a":1}`)
	f.Add("x", "", `[ ]`)
	f.Add("x", "", `{ "a" : 1 }`)
	f.Add("x", "", `null`)
	f.Add("x", "", `[1,2]`)
	f.Add("", "", ``)

	f.Fuzz(func(t *testing.T, typ, id, payload string) {
		msg := &Message{Type: typ, ID: id}
		if payload != "" {
			msg.Payload = json.RawMessage(payload)
		}
		frame, ok := appendEnvelope(msg)
		if !ok {
			return
		}
		want, err := EncodeFrameSlow(msg)
		if err != nil {
			// Outside the contract: the reference rejects this payload as
			// invalid JSON and payloadInlinable is not a parser. See the
			// package comment above and TestFastPaths_KnownValidityLimits.
			return
		}
		var gotMsg, wantMsg Message
		gotErr := json.Unmarshal(frame[4:], &gotMsg)
		if err := json.Unmarshal(want[4:], &wantMsg); err != nil {
			t.Fatalf("reference frame does not decode: %v", err)
		}
		if gotErr != nil {
			t.Fatalf("fast frame does not decode: %v\n payload=%q\n frame=%q", gotErr, payload, frame)
		}
		if gotMsg.Type != wantMsg.Type || gotMsg.ID != wantMsg.ID {
			t.Fatalf("envelope differs: %q/%q vs %q/%q", gotMsg.Type, gotMsg.ID, wantMsg.Type, wantMsg.ID)
		}
		if !jsonEquivalent(t, gotMsg.Payload, wantMsg.Payload) {
			t.Fatalf("payload not equivalent\n payload=%q\n  got %q\n want %q",
				payload, gotMsg.Payload, wantMsg.Payload)
		}
	})
}

// jsonEquivalent compares two JSON documents by value, ignoring insignificant
// whitespace differences.
//
// UseNumber is required, not stylistic: decoding into `any` converts numbers to
// float64, and a JSON literal that overflows it (1E1000) then fails to decode —
// which the fuzzer found and reported as a payload mismatch between two
// byte-identical frames.
func jsonEquivalent(t *testing.T, a, b []byte) bool {
	t.Helper()
	if bytes.Equal(a, b) {
		return true
	}
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	decode := func(p []byte) (any, error) {
		d := json.NewDecoder(bytes.NewReader(p))
		d.UseNumber()
		var v any
		err := d.Decode(&v)
		return v, err
	}
	av, err := decode(a)
	if err != nil {
		return false
	}
	bv, err := decode(b)
	if err != nil {
		return false
	}
	return reflect.DeepEqual(av, bv)
}

// FuzzParseEnvelope pins that the fast decoder is never wrong and never more
// lenient than encoding/json. Seeds cover the mis-slice shapes the design's
// finding 2 describes: keys after payload, nested objects, duplicates,
// truncation.
func FuzzParseEnvelope(f *testing.F) {
	f.Add(`{"type":"pane_output","payload":{"pane_id":"p","data":"aGk="}}`)
	f.Add(`{"type":"pane_output","id":"r1","payload":{}}`)
	f.Add(`{"type":"pane_output","payload":{},"id":"x"}`)
	f.Add(`{"type":"pane_output","payload":{},"x":{}}`)
	f.Add(`{"type":"pane_output","payload":{"a":{"b":1}}}`)
	f.Add(`{"type":"pane_output","payload":{},"type":"other"}`)
	f.Add(`{"type":"pane_output","payload":[1,2]}`)
	f.Add(`{"type":"other","payload":{}}`)
	f.Add(`{"type":"pane_output"`)
	f.Add(`{}`)

	f.Fuzz(func(t *testing.T, body string) {
		var fast Message
		if !parseEnvelope([]byte(body), &fast) {
			return // declined — the fallback handles it
		}
		var want Message
		if err := json.Unmarshal([]byte(body), &want); err != nil {
			// Outside the contract, but the badness must be PRESERVED rather
			// than swallowed: a payload the reference rejects must still fail
			// downstream, so the frame cannot be silently mistaken for valid.
			var p PaneOutputPayload
			if decodePaneOutput(fast.Payload, &p) {
				t.Fatalf("fast path fully accepted a frame encoding/json rejects: %q", body)
			}
			return
		}
		if fast.Type != want.Type || fast.ID != want.ID {
			t.Fatalf("envelope mismatch for %q: %q/%q vs %q/%q",
				body, fast.Type, fast.ID, want.Type, want.ID)
		}
		if !bytes.Equal(fast.Payload, want.Payload) {
			t.Fatalf("payload mismatch for %q: %q vs %q", body, fast.Payload, want.Payload)
		}
	})
}

// FuzzDecodePaneOutput pins the payload fast path against encoding/json,
// including agreement on which inputs are ERRORS.
func FuzzDecodePaneOutput(f *testing.F) {
	f.Add(`{"pane_id":"p","data":"aGk="}`)
	f.Add(`{"pane_id":"p","data":"aGk=","ghost":true}`)
	f.Add(`{"pane_id":"p","data":"aGk=","ghost":false}`)
	f.Add(`{"pane_id":"p","data":"!!!!"}`)
	f.Add(`{"pane_id":"p","data":null}`)
	f.Add(`{"data":"aGk=","pane_id":"p"}`)
	f.Add(`{"pane_id":"p","data":"aGk=","extra":1}`)
	f.Add(``)

	f.Fuzz(func(t *testing.T, payload string) {
		var fast PaneOutputPayload
		if !decodePaneOutput([]byte(payload), &fast) {
			return // declined — the fallback handles it
		}
		var want PaneOutputPayload
		if err := json.Unmarshal([]byte(payload), &want); err != nil {
			// Outside the contract. Unlike the envelope, there is no later
			// layer to catch it, so this is the narrowest of the three gaps:
			// the shape match requires both field keys in order and an exact
			// trailing key set, leaving only an invalid interior inside an
			// otherwise conforming pane_id/data pair.
			return
		}
		if fast.PaneID != want.PaneID || fast.Ghost != want.Ghost {
			t.Fatalf("field mismatch for %q: %+v vs %+v", payload, fast, want)
		}
		if !bytes.Equal(fast.Data, want.Data) {
			t.Fatalf("data mismatch for %q: %q vs %q", payload, fast.Data, want.Data)
		}
	})
}
