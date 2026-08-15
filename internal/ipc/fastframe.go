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
// shape is not certainly identical, DECLINE: a slower correct frame costs
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

// payloadInlinable reports whether p may be written into the envelope verbatim.
//
// It checks that p LOOKS like a complete, compact JSON value: matching outer
// delimiters, or one of the three exact literals, or a number made only of the
// bytes json.Marshal emits for one. Composite values are O(1) — only the two
// ends are examined.
//
// This is deliberately NOT full validation, and the boundary matters. Full
// validation via json.Valid costs 47 us on an 8 KB pane_output payload, which
// would give back most of the 112 us -> 2.7 us this change buys. So the
// soundness argument rests on the producer instead: NewMessage
// (internal/ipc/protocol.go:1130) is the only production site that sets
// Message.Payload and it always uses json.Marshal, so payloads arrive compact,
// HTML-escaped and valid.
//
// What this check adds on top is a cheap backstop against a payload built by
// hand, which is what TestBroadcast_MarshalErrorLogsAndReturns injects and what
// EncodeFrame's error path exists for. It catches unterminated and truncated
// values — the realistic shapes — but a hand-built payload that is
// delimiter-balanced and still internally invalid (`{not valid}`) would be
// inlined and reach the peer as a malformed frame. No production path can
// produce one; a future producer that bypasses NewMessage must not assume it is
// checked here.
func payloadInlinable(p []byte) bool {
	switch p[0] {
	case '{':
		return p[len(p)-1] == '}'
	case '[':
		return p[len(p)-1] == ']'
	case '"':
		return len(p) >= 2 && p[len(p)-1] == '"'
	case 't':
		return string(p) == "true"
	case 'f':
		return string(p) == "false"
	case 'n':
		return string(p) == "null"
	}
	// A number. json.Marshal emits only these bytes for one, and they are
	// short enough that a full scan is free.
	for i := 0; i < len(p); i++ {
		switch c := p[i]; {
		case c >= '0' && c <= '9':
		case c == '-', c == '+', c == '.', c == 'e', c == 'E':
		default:
			return false
		}
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

	// Decline rather than error, so an oversize message produces the SAME
	// error from the same place it always has (EncodeFrameSlow's own check).
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

// fastString reads a JSON string body up to its closing quote, returning the
// contents and the remainder after the quote.
//
// Declines on backslash (an escape we would otherwise hand back un-decoded), on
// any byte below 0x20 — which encoding/json rejects inside a string, so
// accepting one would make this MORE lenient than the fallback — and on any
// byte above 0x7E.
//
// That last check is not symmetry with fastStringSafe for its own sake; the
// first version omitted it and FuzzDecodePaneOutput found the bug in seconds
// with {"pane_id":"\xd8",...}. encoding/json replaces invalid UTF-8 with U+FFFD
// when decoding into a string, so returning the raw bytes yields a DIFFERENT
// PaneID than the fallback for the same input. Validating UTF-8 instead would
// cost a scan; every string this reads (message types, request IDs, pane IDs)
// is ASCII by construction, so declining is free.
func fastString(b []byte) (string, []byte, bool) {
	for i := 0; i < len(b); i++ {
		switch c := b[i]; {
		case c == '"':
			return string(b[:i]), b[i+1:], true
		case c == '\\', c < 0x20, c > 0x7E:
			return "", nil, false
		}
	}
	return "", nil, false
}

// paneOutputSpan reports whether p is the payload span of a conforming
// pane_output frame: a flat object, no nested braces, opening with the field
// PaneOutputPayload always marshals first.
//
// This is what makes slicing the payload without parsing it safe, and it works
// ONLY because the caller has already established the type is pane_output. That
// payload is flat — {"pane_id":"…","data":"…"[,"ghost":true]} — and neither
// field can contain a brace, since base64's alphabet has none and pane IDs are
// "pane-" plus hex.
//
// The brace count is what proves the span did not swallow a sibling envelope
// key: given {"type":"pane_output","payload":{},"x":{}} the naive span is
// `{},"x":{}`, which has two of each. The prefix is what rejects a payload that
// is brace-flat but not JSON at all — FuzzParseEnvelope found `{0}` within
// seconds of the first version, which encoding/json rejects and this accepted.
// `pane_id` is unconditionally first because PaneOutputPayload declares it
// first and it carries no omitempty.
//
// Both checks together are still not a parser: `{"pane_id":"p" garbage}` would
// pass. See the contract note in fastframe_fuzz_test.go for why that is
// accepted rather than closed with json.Valid.
//
// Two IndexByte calls plus a prefix compare are SIMD-accelerated — about 0.4 us
// over 11 KB, against the ~131 us this path removes.
func paneOutputSpan(p []byte) bool {
	if len(p) < 2 || p[0] != '{' || p[len(p)-1] != '}' {
		return false
	}
	if !bytes.HasPrefix(p, []byte(`{"pane_id":"`)) {
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
	if !paneOutputSpan(span) {
		return false
	}

	m.Type = typ
	m.ID = id
	m.Payload = span[:len(span):len(span)]
	return true
}

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
	// An empty data field decodes to an empty slice whose nil-ness is not worth
	// matching by hand; let encoding/json settle it.
	if len(enc) == 0 {
		return false
	}
	// CR and LF are the ONLY bytes encoding/json rejects inside a string that
	// base64.Decode does not: it skips line breaks silently, while every other
	// control byte and every byte above 0x7E is outside the alphabet and errors
	// out below. So two IndexByte calls close the whole divergence, where
	// scanning the span for all illegal bytes would cost ~11 us on an 8 KB
	// frame. FuzzParseEnvelope found this with {"pane_id":"0","data":"\r"},
	// which encoding/json rejects and this decoded to an empty payload.
	if bytes.IndexByte(enc, '\r') >= 0 || bytes.IndexByte(enc, '\n') >= 0 {
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
