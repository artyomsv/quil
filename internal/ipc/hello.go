package ipc

// Protocol base (phase 3a, #237): hello, error replies and state_req.
//
// hello is the one message a client uses to say what it is. It carries an ID
// and is answered with hello_resp. A conn whose hello registered proto >= 1 is
// "non-legacy": only such a conn is sent error replies, because an older
// client (a v1.81.0 MCP bridge) hands whatever frame carries its request ID to
// the caller without checking the type, and would read an error as an empty
// success.

const (
	MsgHello     = "hello"      // client → daemon (HelloPayload), carries an ID
	MsgHelloResp = "hello_resp" // daemon → client (HelloRespPayload)
	MsgError     = "error"      // daemon → client (ErrorPayload), same ID as the request
	MsgStateReq  = "state_req"  // client → daemon (no payload); answered with workspace_state + same ID
)

// ProtocolVersion is the protocol a 3a build speaks. A hello with Proto < 1
// is refused; later phases raise this and gate behaviour on it.
const ProtocolVersion = 1

// Error codes carried in ErrorPayload.Code. Stable strings: a script (#143)
// maps them to exit statuses.
const (
	ErrCodeUnknownType = "unknown_type"
	ErrCodeBadPayload  = "bad_payload"
	// ErrCodeRefused is reserved for phase 4's rights checks. Nothing in 3a
	// sends it.
	ErrCodeRefused = "refused"
)

// Capabilities a daemon lists in HelloRespPayload.Caps, beside GatedRequests.
const (
	CapError    = "error"     // sends error replies to hello'd conns
	CapStateRev = "state_rev" // numbers workspace_state frames (rev, run_id)
	CapStateReq = "state_req" // answers state_req
)

// HelloPayload is a client's self-description. Kind is "tui", "bridge", "web"
// or "script"; an unknown kind is stored as-is.
type HelloPayload struct {
	Kind     string   `json:"kind"`
	Proto    int      `json:"proto"`
	ClientID string   `json:"client_id"`
	Version  string   `json:"version"`
	PID      int      `json:"pid"`
	ExeName  string   `json:"exe"`
	UptimeMS int64    `json:"uptime_ms"`
	Caps     []string `json:"caps,omitempty"`
}

// HelloRespPayload is the daemon's answer to hello.
type HelloRespPayload struct {
	Version string   `json:"version"`
	Proto   int      `json:"proto"`
	RunID   string   `json:"run_id"`
	Caps    []string `json:"caps"`
}

// ErrorPayload answers an id-bearing request the daemon did not handle.
type ErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Type    string `json:"type"`
}

// DaemonCaps returns a fresh copy of what a 3a daemon advertises.
func DaemonCaps() []string {
	out := []string{CapError, CapStateRev, CapStateReq}
	return append(out, GatedRequests...)
}
