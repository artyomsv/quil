package ipc

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
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
// It must establish that p is ONE COMPLETE JSON VALUE with nothing after it.
// That is a security property, not tidiness: the payload is concatenated
// between `,"payload":` and the envelope's closing brace, so trailing content
// becomes SIBLING ENVELOPE KEYS, and encoding/json resolves duplicates
// last-wins. A payload of
//
//	{},"type":"shutdown","x":{}
//
// produces {"type":"pane_output","payload":{},"type":"shutdown","x":{}}, which
// is not malformed — it decodes cleanly as Type "shutdown". A pane_output frame
// becomes a shutdown frame.
//
// An earlier version checked only the first and last byte and documented the
// residual risk as "reaches the peer as a malformed frame". That was measurably
// wrong, and the review that caught it is why this is now two checks:
//
//   - paneOutputSpan is the hot path and is already injection-proof: exactly one
//     '{' and one '}', at the two ends, so no sibling key can exist. Free.
//   - json.Valid for everything else. It costs ~5.8 ns/byte, which is why it is
//     NOT applied to pane_output (47 us on an 8 KB payload would give back most
//     of what this change buys) — but every other message type has a small
//     payload, measured at 1.4 us for a two-pane list_panes_resp, and that is
//     the right price for a property the first-byte check never actually had.
//
// json.Valid also subsumes the old hand-rolled number branch, which accepted
// eleven shapes json.Marshal never emits ("-", "+", ".", "1e", "1.2.3", "+5",
// ".5", "0123" among them) — several of which cannot begin a JSON value at all.
//
// Note this establishes VALIDITY, not compactness: json.Valid accepts `[ ]`,
// which json.Marshal would compact to `[]`, so byte-identity still holds only
// for marshal-produced payloads. Semantic equivalence holds for all of them,
// and that is what "no version negotiation needed" rests on. See the contract
// note in fastframe_fuzz_test.go.
func payloadInlinable(p []byte) bool {
	if paneOutputSpan(p) {
		return true
	}
	// Leading or trailing whitespace is VALID JSON that json.Marshal would
	// compact away, so inlining it would break byte-identity for no gain. Two
	// comparisons keep the property for the common case; interior whitespace
	// (`{ "a" : 1 }`) still passes and is the documented semantic-equivalence
	// case, covered by FuzzAppendEnvelopeRawPayload.
	if isJSONSpace(p[0]) || isJSONSpace(p[len(p)-1]) {
		return false
	}
	return json.Valid(p)
}

func isJSONSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
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
	// A nil Message reached json.Marshal as `null` before this fast path
	// existed. Declining restores that exactly, and costs one comparison to
	// avoid a nil dereference on a conn dispatch goroutine that has no recover.
	if msg == nil {
		return nil, false
	}
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
// The general problem this solves: "slice the payload without scanning it" and
// "fall back on anything non-conforming" are contradictory, because
// non-conformance can live inside the unscanned span. paneOutputSpan is what
// resolves it, by checking a shape flat enough to validate with a prefix
// compare and two IndexByte calls.
//
// The `typ != MsgPaneOutput` gate is NOT what makes that sound, and an earlier
// version of this comment claimed it was. A mutation test removing the gate
// survived the whole suite plus 4.4M fuzz executions — correctly, because
// parseEnvelope only ever hands back an opaque byte span, so for ANY type a
// span paneOutputSpan accepts slices to exactly what json.Unmarshal produces.
//
// The gate earns its place for two other reasons. It keeps paneOutputSpan's
// justification honest: "contains no brace" is an argument about base64 and hex
// pane ids, true only for this payload type, so without the gate those checks
// would be right by accident rather than by reasoning. And it bounds the blast
// radius of any future loosening of them to the one message type whose shape
// was actually analysed. Restricting costs nothing either way — the scan is
// expensive only in proportion to the payload, and no other type has a large
// one. TestParseEnvelope_DeclinesNonPaneOutput pins the gate so it cannot be
// silently dropped as dead code.
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
	// The explicit length guard is defence in depth, and it is cheap insurance
	// against a whole-daemon crash rather than a wasted branch.
	//
	// The slice below is already provably in range, but only through an
	// invariant nothing states: `rest` is always a SUFFIX of `body` (both
	// body[len(prefix):] and fastString's b[i+1:] return suffixes), and the
	// trailing-'}' check above already returned for anything else — so a
	// non-empty rest ends in '}', HasPrefix forces rest[10] == ':', and
	// len(rest) == 11 is a contradiction, giving len(rest) >= 12.
	//
	// That is three separate facts holding a slice expression in range. Reorder
	// the '}' check, or make fastString return something that is not a suffix,
	// and this becomes a one-byte out-of-range panic — on a conn dispatch
	// goroutine that has no recover (internal/ipc/server.go handleConn), from a
	// single frame, on a socket whose only authentication is its file mode.
	if len(rest) < len(payloadKey)+1 {
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
