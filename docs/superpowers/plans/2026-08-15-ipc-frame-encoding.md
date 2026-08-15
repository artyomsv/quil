# IPC Frame Encoding Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the redundant `encoding/json` scan from the IPC encode and decode hot paths, producing byte-identical wire output, for ~40× encode and ~10× decode on `pane_output` frames.

**Architecture:** Three fast paths in a new `internal/ipc/fastframe.go`, each returning a bool that means "I declined — use the existing `encoding/json` path". `EncodeFrame`, `ReadMessage` and `DecodePayload` each gain one two-line branch; all existing behaviour is the fallback. The decode fast path is gated on `pane_output`, which is both the only type where it pays and the only shape whose payload span can be validated without scanning it.

**Tech Stack:** Go 1.25, stdlib only (`encoding/json`, `encoding/base64`, `bytes`). No new dependencies. Build and test via `./scripts/dev.sh` (Docker — the host has no Go).

**Spec:** [`docs/superpowers/specs/2026-08-15-ipc-frame-encoding-design.md`](../specs/2026-08-15-ipc-frame-encoding-design.md)

## Global Constraints

- **Byte-identical wire output.** Any input the fast encoder accepts must produce exactly what `json.Marshal` produces. When in doubt, decline to the fallback. There is no version bump and no negotiation — the MCP bridge performs no version handshake (`cmd/quil/mcp.go:231`), so an old bridge can attach to a new daemon.
- **Fall back, never guess.** Every fast path returns `bool`. `false` means the caller runs the pre-existing `encoding/json` code unchanged, including its exact error values.
- **No buffer pooling, ever.** `EncodeFrame` must return a freshly allocated slice — `Broadcast` fans the same slice out to every conn read-only (`internal/ipc/server.go:610`). `ReadMessage` must allocate `data` fresh per call — the decoded `Payload` now aliases it.
- **Payload is inserted verbatim.** Correct only because `NewMessage` (`internal/ipc/protocol.go:1130`) is the sole production producer of `Message.Payload` and always uses `json.Marshal`, so payloads are pre-compacted and pre-HTML-escaped.
- Go conventions: tabs, `MixedCaps`, acronyms uppercase (`ID`, `JSON`), stdlib assertions only (`t.Errorf`/`t.Fatalf`), table-driven tests, `t.Helper()` in helpers.
- Run tests with `./scripts/dev.sh test internal/ipc`. Race check with `./scripts/dev.sh test-race internal/ipc`.

---

### Task 1: Fast encode path

**Files:**
- Create: `internal/ipc/fastframe.go`
- Create: `internal/ipc/fastframe_test.go`
- Modify: `internal/ipc/protocol.go:1150-1162` (`EncodeFrame`)

**Interfaces:**
- Consumes: `Message`, `maxFrameSize` (both `internal/ipc/protocol.go`).
- Produces: `appendEnvelope(msg *Message) ([]byte, bool)` — a complete length-prefixed frame, or `false` to decline. `fastStringSafe(s string) bool`. `payloadInlinable(p []byte) bool`.

- [ ] **Step 1: Write the failing tests**

Create `internal/ipc/fastframe_test.go`:

```go
package ipc

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

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
```

Add `"reflect"` to that file's imports.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `./scripts/dev.sh test internal/ipc`
Expected: FAIL — `undefined: appendEnvelope`, `undefined: EncodeFrameSlow`.

- [ ] **Step 3: Write the implementation**

Create `internal/ipc/fastframe.go`:

```go
package ipc

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
)

// Fast paths for the IPC hot path.
//
// Every function here returns a bool meaning "I handled it". False means the
// caller must run the pre-existing encoding/json path, which stays the sole
// definition of correct behaviour — including its exact error values. When a
// shape is not certainly identical, DECLINE; a slower correct frame costs
// microseconds, a wrong one corrupts a length-prefixed stream.
//
// Why this exists: EncodeFrame used to marshal a Message whose Payload was
// already-encoded JSON, and encoding/json compacts and validates every
// RawMessage on the way out — walking an 8 KB pane_output's ~11 KB of base64
// through its scanner at ~10 ns/byte. That second pass was 99% of encode time
// and cost 7x the base64 encode that produced the bytes it re-scanned.

// fastStringSafe reports whether s can be written between two quotes verbatim
// and still match what encoding/json emits.
//
// The HTML trio is the non-obvious part and was a bug in the first draft of the
// design: json.Marshal escapes <, > and & to <, > and & by
// default (SetEscapeHTML defaults true), and respondTo echoes whatever ID an
// IPC client sent. Bytes above 0x7E are excluded not because valid non-ASCII
// needs escaping — it does not, "café" survives verbatim — but because INVALID
// UTF-8 is silently replaced with U+FFFD by json.Marshal, and rejecting all
// non-ASCII is cheaper than checking validity.
func fastStringSafe(s string) bool {
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c < 0x20, c > 0x7E:
			return false
		case c == '"', c == '\\', c == '<', c == '>', c == '&':
			return false
		}
	}
	return true
}

// payloadInlinable rejects payloads that json.Marshal would normalise on the
// way out. It is a cheap sanity check, not validation: the real guarantee is
// that NewMessage is the only production producer of Payload and always uses
// json.Marshal, so payloads are already compact and already HTML-escaped.
// Deeper checking would re-introduce the scan this whole change removes.
func payloadInlinable(p []byte) bool {
	switch p[0] {
	case '{', '[', '"', 't', 'f', 'n', '-':
	default:
		if p[0] < '0' || p[0] > '9' {
			return false
		}
	}
	switch p[len(p)-1] {
	case ' ', '\t', '\n', '\r':
		return false
	}
	return true
}

// appendEnvelope builds a complete length-prefixed frame by concatenation,
// skipping encoding/json's compact()/scanner pass over the already-encoded
// payload. Returns false to decline, in which case the caller must fall back.
//
// The returned slice is freshly allocated and MUST NOT come from a pool:
// Broadcast fans the same slice out to every conn's sendLoop read-only
// (internal/ipc/server.go:610), and ReadMessage's decoded Payload aliases its
// own read buffer. Pooling either would break both at once.
func appendEnvelope(msg *Message) ([]byte, bool) {
	if !fastStringSafe(msg.Type) || !fastStringSafe(msg.ID) {
		return nil, false
	}
	if len(msg.Payload) > 0 && !payloadInlinable(msg.Payload) {
		return nil, false
	}

	n := len(`{"type":"`) + len(msg.Type) + len(`"`)
	if msg.ID != "" {
		n += len(`,"id":"`) + len(msg.ID) + len(`"`)
	}
	if len(msg.Payload) > 0 {
		n += len(`,"payload":`) + len(msg.Payload)
	}
	n += len(`}`)

	// Decline rather than error, so the oversize message produces the SAME
	// error from the same place it always has (EncodeFrame's own check).
	if n > maxFrameSize {
		return nil, false
	}

	buf := make([]byte, 4, 4+n)
	buf = append(buf, `{"type":"`...)
	buf = append(buf, msg.Type...)
	buf = append(buf, '"')
	if msg.ID != "" {
		buf = append(buf, `,"id":"`...)
		buf = append(buf, msg.ID...)
		buf = append(buf, '"')
	}
	if len(msg.Payload) > 0 {
		buf = append(buf, `,"payload":`...)
		buf = append(buf, msg.Payload...)
	}
	buf = append(buf, '}')
	binary.BigEndian.PutUint32(buf[:4], uint32(len(buf)-4))
	return buf, true
}
```

- [ ] **Step 4: Wire it into `EncodeFrame`**

In `internal/ipc/protocol.go`, rename the existing body to `EncodeFrameSlow` and add the branch. Replace lines 1146-1162 with:

```go
// EncodeFrame marshals msg into a single length-prefixed wire frame in one
// allocation. Shared by WriteMessage and the per-conn send queues — replaces
// the marshal → bytes.Buffer → clone chain that copied every broadcast frame
// up to four times.
//
// Tries appendEnvelope first, which builds the same bytes by concatenation and
// skips encoding/json's redundant pass over the already-encoded payload. Any
// shape it declines falls through to EncodeFrameSlow, which is the sole
// definition of correct output — the fast path is measured against it in tests.
func EncodeFrame(msg *Message) ([]byte, error) {
	if frame, ok := appendEnvelope(msg); ok {
		return frame, nil
	}
	return EncodeFrameSlow(msg)
}

// EncodeFrameSlow is the reference encoder: plain json.Marshal plus the length
// prefix. Exported so the fast path can be differentially tested against it,
// and kept as the fallback for every shape appendEnvelope declines.
func EncodeFrameSlow(msg *Message) ([]byte, error) {
	data, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshal message: %w", err)
	}
	if len(data) > maxFrameSize {
		return nil, fmt.Errorf("frame too large: %d bytes (max %d)", len(data), maxFrameSize)
	}
	frame := make([]byte, 4+len(data))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(data)))
	copy(frame[4:], data)
	return frame, nil
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `./scripts/dev.sh test internal/ipc`
Expected: PASS, including the pre-existing `protocol_test.go` suite unchanged.

- [ ] **Step 6: Verify the tests can fail (mutation check)**

Temporarily delete `case c == '"', c == '\\', c == '<', c == '>', c == '&':` from `fastStringSafe`.
Run: `./scripts/dev.sh test internal/ipc`
Expected: FAIL in `TestAppendEnvelope_DeclinesValuesNeedingEscaping`. **Restore the line.**

A byte-identity test that passes against a broken encoder is worth nothing — confirm it fails before trusting it.

- [ ] **Step 7: Commit**

```bash
git add internal/ipc/fastframe.go internal/ipc/fastframe_test.go internal/ipc/protocol.go
git commit -F - <<'EOF'
perf(ipc): build the wire envelope by concatenation

EncodeFrame marshalled a Message whose Payload was already encoded JSON,
and encoding/json compacts and validates every RawMessage on the way out.
For an 8 KB pane_output frame that meant re-scanning ~11 KB of base64
through the scanner state machine: 104 us of the 112 us total, and 7x the
cost of the base64 encode that produced those bytes.

appendEnvelope writes {"type":..,"id":..,"payload":..} directly. Output is
byte-identical, so nothing on the wire changes and no version negotiation
is needed - which matters because the MCP bridge performs no version
handshake and an old one can attach to a new daemon.

It declines anything it cannot guarantee, falling back to the renamed
EncodeFrameSlow. Type and ID must be plain ASCII with no ", \, <, > or &:
the HTML trio is not optional, since encoding/json escapes those by
default and respondTo echoes any client-supplied ID. Bytes above 0x7E are
declined because invalid UTF-8 is silently replaced with U+FFFD by
json.Marshal. Oversize frames decline too, so maxFrameSize keeps producing
the same error from the same place.

Inlining the payload verbatim is correct because NewMessage is the only
production producer of Message.Payload and always uses json.Marshal, so
payloads arrive pre-compacted and pre-escaped.
EOF
```

---

### Task 2: Fast envelope decode

**Files:**
- Modify: `internal/ipc/fastframe.go` (add `parseEnvelope`, `fastString`, `flatBracedObject`)
- Modify: `internal/ipc/protocol.go:1176-1198` (`ReadMessage`)
- Modify: `internal/ipc/fastframe_test.go`

**Interfaces:**
- Consumes: `appendEnvelope` (Task 1), `Message`, `MsgPaneOutput`.
- Produces: `parseEnvelope(body []byte, m *Message) bool`, `fastString(b []byte) (string, []byte, bool)`, `flatBracedObject(p []byte) bool`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/ipc/fastframe_test.go`:

```go
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

// The finding-2 class: non-conformance living INSIDE the span the fast path
// does not scan. json.Unmarshal accepts all of these today (key order is free,
// duplicate keys are last-wins), so accepting-and-mis-slicing would be a
// behaviour change. Each must be declined.
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
		{"escape in type", `{"type":"pane_output","payload":{}}`},
		{"escape in id", `{"type":"pane_output","id":"aAb","payload":{}}`},
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

func mustMessage(t *testing.T, typ string, payload any) *Message {
	t.Helper()
	msg, err := NewMessage(typ, payload)
	if err != nil {
		t.Fatalf("NewMessage(%s): %v", typ, err)
	}
	return msg
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `./scripts/dev.sh test internal/ipc`
Expected: FAIL — `undefined: parseEnvelope`.

- [ ] **Step 3: Write the implementation**

Append to `internal/ipc/fastframe.go`:

```go
// fastString reads a JSON string body up to its closing quote, returning the
// contents and the remainder after the quote.
//
// Declines on backslash (an escape we would otherwise hand back un-decoded) and
// on any byte below 0x20, which encoding/json rejects inside a string. That
// second check is what stops the fast decoder from being MORE lenient than the
// fallback. Bytes above 0x7E are fine here, unlike in fastStringSafe: decoding
// valid non-ASCII needs no special handling, it is only ENCODING invalid UTF-8
// that diverges.
func fastString(b []byte) (string, []byte, bool) {
	for i := 0; i < len(b); i++ {
		switch c := b[i]; {
		case c == '"':
			return string(b[:i]), b[i+1:], true
		case c == '\\', c < 0x20:
			return "", nil, false
		}
	}
	return "", nil, false
}

// flatBracedObject reports whether p is a JSON object with no nested braces.
//
// This is what makes slicing the payload without parsing it safe, and it works
// ONLY because the caller has already established the type is pane_output. That
// payload is flat — {"pane_id":"…","data":"…"[,"ghost":true]} — and neither
// field can contain a brace, since base64's alphabet has none and pane IDs are
// "pane-" plus hex. So exactly one '{' at the start and one '}' at the end
// proves the span did not swallow a sibling envelope key: given
// {"type":"pane_output","payload":{},"x":{}} the naive span is `{},"x":{}`,
// which has two of each and is rejected here.
//
// Two IndexByte calls are SIMD-accelerated — about 0.4 us over 11 KB, against
// the ~131 us this path removes.
func flatBracedObject(p []byte) bool {
	if len(p) < 2 || p[0] != '{' || p[len(p)-1] != '}' {
		return false
	}
	return bytes.IndexByte(p[1:], '{') < 0 && bytes.IndexByte(p[:len(p)-1], '}') < 0
}

// parseEnvelope fills m from a frame body without running the JSON scanner over
// the payload, returning false to decline. m is written only on success, so a
// declined call leaves the caller's Message untouched for the fallback.
//
// Restricted to pane_output deliberately, and it is a correctness requirement
// rather than a scope choice: "slice the payload without scanning it" and "fall
// back on anything non-conforming" are contradictory in general, because
// non-conformance can live inside the unscanned span. flatBracedObject resolves
// that for this one flat shape. It also costs nothing to restrict — the scan is
// expensive only in proportion to the payload, and no other message type has a
// large one.
//
// LIFETIME: the returned Payload ALIASES body rather than copying it, unlike
// the json.Unmarshal fallback, where RawMessage.UnmarshalJSON copies. Safe
// because ReadMessage allocates its buffer fresh per call and never reuses it.
// The three-index slice caps capacity so a later append to a retained Payload
// reallocates instead of overwriting the frame's trailing bytes.
func parseEnvelope(body []byte, m *Message) bool {
	const prefix = `{"type":"`
	if len(body) < len(prefix) || body[len(body)-1] != '}' {
		return false
	}
	if !bytes.HasPrefix(body, []byte(prefix)) {
		return false
	}

	typ, rest, ok := fastString(body[len(prefix):])
	if !ok || typ != MsgPaneOutput {
		return false
	}

	var id string
	const idKey = `,"id":"`
	if bytes.HasPrefix(rest, []byte(idKey)) {
		if id, rest, ok = fastString(rest[len(idKey):]); !ok {
			return false
		}
	}

	const payloadKey = `,"payload":`
	if !bytes.HasPrefix(rest, []byte(payloadKey)) {
		return false
	}
	span := rest[len(payloadKey) : len(rest)-1]
	if !flatBracedObject(span) {
		return false
	}

	m.Type = typ
	m.ID = id
	m.Payload = span[:len(span):len(span)]
	return true
}
```

- [ ] **Step 4: Wire it into `ReadMessage`**

In `internal/ipc/protocol.go`, replace the tail of `ReadMessage` (the `var msg Message` block) with:

```go
	// data is freshly allocated above and never reused. That is load-bearing:
	// parseEnvelope's Payload ALIASES it rather than copying, so a pooled or
	// reused read buffer here would become a use-after-reuse bug surfacing as
	// intermittently corrupted payloads.
	var msg Message
	if parseEnvelope(data, &msg) {
		return &msg, nil
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("unmarshal message: %w", err)
	}
	return &msg, nil
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `./scripts/dev.sh test internal/ipc`
Expected: PASS.

- [ ] **Step 6: Verify the mis-slice guard can fail**

Temporarily change `flatBracedObject` to `return len(p) >= 2 && p[0] == '{' && p[len(p)-1] == '}'`.
Run: `./scripts/dev.sh test internal/ipc`
Expected: FAIL in `TestParseEnvelope_DeclinesKeysAfterPayload` (the "object after payload" case). **Restore the function.**

- [ ] **Step 7: Run the full suite and the race detector**

Run: `./scripts/dev.sh test` then `./scripts/dev.sh test-race internal/ipc`
Expected: both green.

- [ ] **Step 8: Commit**

```bash
git add internal/ipc/fastframe.go internal/ipc/fastframe_test.go internal/ipc/protocol.go
git commit -F - <<'EOF'
perf(ipc): parse the wire envelope without scanning the payload

ReadMessage ran json.Unmarshal over the whole frame, which walks an 8 KB
pane_output's ~11 KB of base64 through the scanner just to find the
envelope's three keys.

parseEnvelope pattern-matches the shape the encoder produces and slices
the payload out. It is restricted to pane_output for correctness, not
scope: "slice without scanning" and "fall back on non-conforming input"
are contradictory in general, because non-conformance can live inside the
unscanned span - {"type":"pane_output","payload":{},"x":{}} matches the
prefix grammar and naively slices to `{},"x":{}`, which json.Unmarshal
accepts today. A pane_output payload is flat and neither field can contain
a brace, so two IndexByte calls prove the span swallowed no sibling key.
Restricting costs nothing, since the scan is expensive only in proportion
to the payload and no other type has a large one.

The string scan declines backslash escapes and any byte below 0x20, so the
fast decoder is never more lenient than the fallback it replaces.

The decoded Payload now aliases the read buffer instead of copying it, so
ReadMessage's fresh per-call allocation is load-bearing and the slice caps
its capacity to keep a later append from reaching the frame's tail.
EOF
```

---

### Task 3: Fast `PaneOutputPayload` decode

**Files:**
- Modify: `internal/ipc/fastframe.go` (add `decodePaneOutput`)
- Modify: `internal/ipc/protocol.go:1202-1204` (`DecodePayload`)
- Modify: `internal/ipc/fastframe_test.go`

**Interfaces:**
- Consumes: `fastString` (Task 2), `PaneOutputPayload`.
- Produces: `decodePaneOutput(p []byte, out *PaneOutputPayload) bool`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/ipc/fastframe_test.go`:

```go
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
// produced before this change — an error where encoding/json errors, and the
// fallback's result where the pattern does not match.
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

// A nil/empty Payload must keep erroring: internal/tui/model.go:5955 IGNORES
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `./scripts/dev.sh test internal/ipc`
Expected: FAIL — the "invalid base64" and "unknown extra key" cases pass only once the fast path exists and declines correctly; before that, `TestDecodePayload_PaneOutputMatchesJSON` compiles and passes trivially. Confirm at least one new test fails; if none do, the tests are not yet exercising new behaviour and Step 3 must still be written to keep them passing.

- [ ] **Step 3: Write the implementation**

Append to `internal/ipc/fastframe.go` (add `"encoding/base64"` to the imports if not already present from Task 1):

```go
// decodePaneOutput base64-decodes the data field directly instead of running
// the JSON scanner over the encoded string, returning false to decline.
//
// The trailing key set is matched EXACTLY rather than searched: Ghost is
// `json:"ghost,omitempty"`, so a conforming producer emits either `}` or
// `,"ghost":true}` and nothing else. An unknown extra key, a reordered pair or
// a duplicate therefore declines to encoding/json, which is what keeps this
// agreeing with the fallback on shapes it was never designed for.
//
// Every failure declines rather than erroring, so the error a caller sees is
// always encoding/json's own — invalid base64 included. That matters because
// internal/tui/model.go:5955 ignores DecodePayload's error, so a fast path
// inventing a nil-success would silently hand it a zero-valued payload.
func decodePaneOutput(p []byte, out *PaneOutputPayload) bool {
	const idKey = `{"pane_id":"`
	if !bytes.HasPrefix(p, []byte(idKey)) {
		return false
	}
	paneID, rest, ok := fastString(p[len(idKey):])
	if !ok {
		return false
	}

	const dataKey = `,"data":"`
	if !bytes.HasPrefix(rest, []byte(dataKey)) {
		return false
	}
	rest = rest[len(dataKey):]
	q := bytes.IndexByte(rest, '"')
	if q < 0 {
		return false
	}
	enc := rest[:q]
	// An empty data field decodes to an empty slice whose nil-ness is not
	// worth matching by hand; let encoding/json settle it.
	if len(enc) == 0 {
		return false
	}

	var ghost bool
	switch string(rest[q+1:]) {
	case `}`:
	case `,"ghost":true}`:
		ghost = true
	default:
		return false
	}

	buf := make([]byte, base64.StdEncoding.DecodedLen(len(enc)))
	n, err := base64.StdEncoding.Decode(buf, enc)
	if err != nil {
		return false
	}

	out.PaneID = paneID
	out.Data = buf[:n]
	out.Ghost = ghost
	return true
}
```

- [ ] **Step 4: Wire it into `DecodePayload`**

In `internal/ipc/protocol.go`, replace `DecodePayload`:

```go
// DecodePayload unmarshals the message payload into the given target.
//
// pane_output is special-cased because it is the only high-frequency type: at
// up to 500 frames/s/pane, running the JSON scanner over ~11 KB of base64 to
// reach one []byte field cost ~90 us a frame. The fast path is inside this
// method rather than a new exported one so no call site changes, and it
// declines to the same json.Unmarshal below for every shape it does not
// recognise — including the error cases, so callers keep seeing exactly the
// errors encoding/json produces.
func (m *Message) DecodePayload(target any) error {
	if out, ok := target.(*PaneOutputPayload); ok && decodePaneOutput(m.Payload, out) {
		return nil
	}
	return json.Unmarshal(m.Payload, target)
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `./scripts/dev.sh test internal/ipc`
Expected: PASS.

- [ ] **Step 6: Verify the strict-tail guard can fail**

Temporarily replace the `switch string(rest[q+1:])` block with `ghost := bytes.Contains(rest[q+1:], []byte(`"ghost":true`))`.
Run: `./scripts/dev.sh test internal/ipc`
Expected: FAIL in `TestDecodePayload_PaneOutputFailureShapes` ("unknown extra key" / "duplicate key"). **Restore the switch.**

- [ ] **Step 7: Commit**

```bash
git add internal/ipc/fastframe.go internal/ipc/fastframe_test.go internal/ipc/protocol.go
git commit -F - <<'EOF'
perf(ipc): base64-decode pane output without the JSON scanner

DecodePayload ran json.Unmarshal over ~11 KB of base64 to reach one
[]byte field, about 90 us per 8 KB frame at up to 500 frames/s/pane.

decodePaneOutput matches the payload shape and decodes the data field
directly. The trailing key set is matched exactly rather than searched -
Ghost is omitempty, so a conforming producer emits `}` or `,"ghost":true}`
and nothing else - which makes an extra, reordered or duplicated key
decline to encoding/json instead of being silently mis-read.

Every failure declines rather than erroring, so callers keep seeing
encoding/json's own errors, invalid base64 included. That is load-bearing:
internal/tui/model.go:5955 ignores this error, so a fast path inventing a
nil-success would hand it a zero-valued payload without a trace.

The special case lives inside DecodePayload rather than in a new exported
method, so no call site changes.
EOF
```

---

### Task 4: Differential fuzz tests

**Files:**
- Create: `internal/ipc/fastframe_fuzz_test.go`

**Interfaces:**
- Consumes: `appendEnvelope`, `EncodeFrameSlow` (Task 1), `parseEnvelope` (Task 2).
- Produces: nothing consumed by later tasks.

These are the only tests that can find the class of bug the design's finding 2 describes. A generated-corpus property test emits only well-formed messages; fuzzing probes keys after payload, duplicate keys, raw control bytes, and truncation.

- [ ] **Step 1: Write the fuzz targets**

Create `internal/ipc/fastframe_fuzz_test.go`:

```go
package ipc

import (
	"bytes"
	"encoding/json"
	"testing"
)

// FuzzAppendEnvelope pins the byte-identity guarantee against arbitrary input.
// This target would have caught the HTML-escaping bug in the design's first
// draft on its first few hundred executions.
func FuzzAppendEnvelope(f *testing.F) {
	f.Add("pane_output", "", `{"a":1}`)
	f.Add("pane_output", "req-1", `{"pane_id":"p","data":"aGk="}`)
	f.Add("x", "a<b>c&d", `{}`)
	f.Add("x", "a\"b", `{}`)
	f.Add("café", "日本", `{}`)
	f.Add("", "", ``)

	f.Fuzz(func(t *testing.T, typ, id, payload string) {
		msg := &Message{Type: typ, ID: id}
		if payload != "" {
			msg.Payload = json.RawMessage(payload)
		}
		frame, ok := appendEnvelope(msg)
		if !ok {
			return // declined — the fallback handles it, nothing to compare
		}
		want, err := EncodeFrameSlow(msg)
		if err != nil {
			t.Fatalf("fast path accepted a message the reference encoder rejects: %v", err)
		}
		if !bytes.Equal(frame, want) {
			t.Fatalf("byte mismatch\n type=%q id=%q payload=%q\n want %q\n  got %q",
				typ, id, payload, want, frame)
		}
	})
}

// FuzzParseEnvelope pins that the fast decoder is never wrong and never more
// lenient than encoding/json.
func FuzzParseEnvelope(f *testing.F) {
	f.Add(`{"type":"pane_output","payload":{"pane_id":"p","data":"aGk="}}`)
	f.Add(`{"type":"pane_output","id":"r1","payload":{}}`)
	f.Add(`{"type":"pane_output","payload":{},"id":"x"}`)
	f.Add(`{"type":"pane_output","payload":{},"x":{}}`)
	f.Add(`{"type":"pane_output","payload":{"a":{"b":1}}}`)
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
			t.Fatalf("fast path accepted what encoding/json rejects: %q (%v)", body, err)
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
```

- [ ] **Step 2: Run the seed corpus**

Run: `./scripts/dev.sh test internal/ipc`
Expected: PASS (`go test` runs fuzz targets against their seeds only).

- [ ] **Step 3: Fuzz both targets for 60 seconds each**

Run:
```bash
PROJECT_DIR="$(pwd -W 2>/dev/null || pwd)"
docker run --rm -v "${PROJECT_DIR}":/src -v quil-gomod:/go/pkg/mod \
  -v quil-gocache:/root/.cache/go-build -w //src golang:1.25-alpine \
  sh -c "go test ./internal/ipc/ -run '^\$' -fuzz FuzzAppendEnvelope -fuzztime 60s"
```
Then the same with `-fuzz FuzzParseEnvelope`.
Expected: both report `PASS` with no new corpus entries written to `testdata/`.

If a target fails, it has found a real divergence — fix the fast path to decline that input, commit the crasher under `internal/ipc/testdata/fuzz/`, and re-run.

- [ ] **Step 4: Commit**

```bash
git add internal/ipc/fastframe_fuzz_test.go
git commit -F - <<'EOF'
test(ipc): fuzz the frame fast paths against encoding/json

The fast paths are correct only if they are byte-identical to
encoding/json or decline. A table test cannot establish that: it only ever
exercises shapes someone thought of, and the dangerous class is the one
nobody did - non-conformance living inside the payload span the decoder
deliberately does not scan.

Two differential targets. The encoder target asserts that anything
appendEnvelope accepts marshals identically; it would have caught the
HTML-escaping bug in the design's first draft within a few hundred
executions. The decoder target asserts that anything parseEnvelope accepts
is something encoding/json also accepts and agrees with, which pins the
"never more lenient than the fallback" property.

Seeds cover the mis-slice shapes: keys after payload, nested objects,
duplicate keys and truncation.
EOF
```

---

### Task 5: Measure, verify at runtime, document

**Files:**
- Modify: `docs/superpowers/specs/2026-08-15-ipc-frame-encoding-design.md` (record the result)
- Create: `bench/after.txt` (gitignored artifact, for the PR description)

**Interfaces:**
- Consumes: everything from Tasks 1-4.
- Produces: the measured before/after table for the PR body.

- [ ] **Step 1: Run the benchmark comparison**

Run: `./scripts/dev.sh bench after`
Expected: benchstat prints the comparison against `bench/before.txt`.

Acceptance (spec success criterion 1):
- `EncodeFrame_PaneOutput8K` ≥30× faster
- `ClientReceive_PaneOutput8K` ≥8× faster
- `EncodeFrame_ListPanesResp` and `ClientReceive_ListPanesResp` show no regression

If the control benchmarks regressed, the fast path is being attempted and declined on the common small-message path — investigate before proceeding.

- [ ] **Step 2: Run the full test suite and the race detector**

Run: `./scripts/dev.sh test` then `./scripts/dev.sh test-race internal/ipc` then `./scripts/dev.sh vet`
Expected: all green.

- [ ] **Step 3: Build and run the dev daemon checklist**

Per `.claude/rules/dev-environment.md`, **dev mode only** — never touch `~/.quil/`.

```bash
./scripts/dev.sh build
./scripts/quil-dev.ps1     # Windows; ./scripts/quil-dev.sh on Unix
```

Confirm `[dev]` is visible in the status bar, then walk the spec's success criterion 4:

- [ ] a `terminal` pane runs a build with sustained output; no garbled or lost text
- [ ] a `claude-code` pane accepts input and renders normally
- [ ] split a pane, drag the border to resize, close one
- [ ] quit the TUI and re-attach: ghost replay restores scrollback
- [ ] restart the dev daemon: workspace restores with layout intact (exercises `UpdateLayoutPayload`, the retained-`RawMessage` path)
- [ ] one MCP tool call against the dev daemon returns a correct result (exercises the `Message.ID` request/response path)
- [ ] `.quil/quil.log` and `.quil/quild.log` contain no decode or unmarshal errors

- [ ] **Step 4: Record the measured result in the spec**

Replace the spec's "Measured result" table with the real benchstat numbers, and change its Status line to `Implemented`.

- [ ] **Step 5: Commit**

```bash
git add docs/superpowers/specs/2026-08-15-ipc-frame-encoding-design.md
git commit -F - <<'EOF'
docs(ipc): record the measured frame encoding result

Replaces the design's projected numbers with the benchstat comparison from
bench/before.txt to bench/after.txt, and marks the spec implemented.
EOF
```

---

## Self-Review

**Spec coverage.** Every spec section maps to a task: fast encode + escape guard + `maxFrameSize` + payload invariant → Task 1; envelope decode + `pane_output` gating + aliasing + full-slice expression → Task 2; `DecodePayload` type switch + failure shapes → Task 3; differential fuzz → Task 4; benchmark acceptance + runtime checklist → Task 5. The no-pooling prohibition is carried as comments in Tasks 1 and 2 and in `.claude/rules/daemon-lifecycle.md` (already committed).

**Type consistency.** `appendEnvelope`, `parseEnvelope`, `decodePaneOutput`, `fastString`, `fastStringSafe`, `flatBracedObject`, `payloadInlinable`, `EncodeFrameSlow` are each defined once and referred to by the same name and signature throughout. `fastString` is introduced in Task 2 and reused in Task 3; `EncodeFrameSlow` is introduced in Task 1 and used by Tasks 1, 4.

**Known asymmetry, deliberate.** `fastStringSafe` (encode) rejects bytes above 0x7E while `fastString` (decode) accepts them. This is correct and is commented in both: encoding invalid UTF-8 diverges because `json.Marshal` substitutes U+FFFD, whereas decoding valid non-ASCII needs no special handling.
