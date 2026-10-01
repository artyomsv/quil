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

	// TCP login (phase 4, §5.4). auth_challenge answers a login hello with
	// the daemon's nonce; auth_proof carries the client's proof. Both use the
	// hello's ID. auth_proof is read only by the login code, never dispatched.
	MsgAuthChallenge = "auth_challenge" // daemon → client (AuthChallengePayload)
	MsgAuthProof     = "auth_proof"     // client → daemon (AuthProofPayload)
)

// Rights levels (§7). The wire value of HelloRespPayload.Rights and the
// stored value in tokens.json. An empty Rights on the local socket is full.
const (
	RightsReadOnly = "read-only"
	RightsStandard = "standard"
	RightsFull     = "full"
)

// AuthChallengePayload is the daemon's half of the nonce pair.
type AuthChallengePayload struct {
	Nonce string `json:"nonce"`
}

// AuthProofPayload is ClientKey XOR HMAC(StoredKey, AuthMessage), base64url.
// It is never logged.
type AuthProofPayload struct {
	Proof string `json:"proof"`
}

// ProtocolVersion is the protocol a 3a build speaks. A hello with Proto < 1
// is refused; later phases raise this and gate behaviour on it.
const ProtocolVersion = 1

// Error codes carried in ErrorPayload.Code. Stable strings: a script (#143)
// maps them to exit statuses.
const (
	ErrCodeUnknownType = "unknown_type"
	ErrCodeBadPayload  = "bad_payload"
	// ErrCodeRefused answers a request (or a login) the conn's rights do not
	// allow (phase 4, §7.4). It replaces the type's usual response.
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
	// TokenID and Nonce open a TCP login (§5.3). omitempty: a unix-socket
	// hello never carries them, and an older daemon drops unknown keys. On a
	// second hello of a logged-in conn the daemon ignores both.
	TokenID string `json:"token_id,omitempty"`
	Nonce   string `json:"nonce,omitempty"`
}

// HelloRespPayload is the daemon's answer to hello.
type HelloRespPayload struct {
	Version string   `json:"version"`
	Proto   int      `json:"proto"`
	RunID   string   `json:"run_id"`
	Caps    []string `json:"caps"`
	// Rights and TokenName tell a TCP client what its token allows (§7.6);
	// empty on the local socket, which is full. ServerSig is the daemon's
	// proof that it knows the token's verifier (§5.4), sent once, on the
	// hello_resp that completes a login.
	Rights    string `json:"rights,omitempty"`
	TokenName string `json:"token_name,omitempty"`
	ServerSig string `json:"server_sig,omitempty"`
}

// ErrorPayload answers an id-bearing request the daemon did not handle.
type ErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Type    string `json:"type"`
}

// DaemonCaps returns a fresh copy of what this daemon advertises.
func DaemonCaps() []string {
	out := []string{CapError, CapStateRev, CapStateReq, CapSharedData}
	return append(out, GatedRequests...)
}
