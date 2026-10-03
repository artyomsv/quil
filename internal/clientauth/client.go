package clientauth

import (
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/textsafe"
)

// ErrServerUnproven: the listener answered but could not show it knows the
// token's verifier — a port squatter, or a stale daemon with another store.
var ErrServerUnproven = errors.New("the listener could not prove it is your daemon")

// RefusedError is the daemon's `error refused` during login. Reason is
// cleaned with cleanPeerText before it is ever stored: it is printed to the
// owner's terminal and put in a destination's park reason, and the daemon
// that sent it is not yet proven to be the real one.
type RefusedError struct{ Reason string }

func (e *RefusedError) Error() string { return "refused: " + e.Reason }

// maxPeerTextBytes bounds a string from the other end of a conn that has not
// passed CheckServerSignature yet — a squatter's reply is just bytes it
// chose, and nothing else limits how long a message type or an error string
// can be before it ends up in a terminal.
const maxPeerTextBytes = 256

// cleanPeerText makes a string that arrived before the login is proven safe
// to put in an error message or a RefusedError.Reason. The message type and
// the error payload are both read and folded into error text BEFORE
// CheckServerSignature runs, so a port squatter controls their bytes
// completely: an ESC/CSI/OSC sequence (e.g. OSC 52, which can rewrite the
// terminal's clipboard) or a Unicode bidi override would otherwise reach
// whatever later prints that text — the owner's terminal, or a destination's
// stored park reason. textsafe.Strip is the repo's one rule for exactly this
// (drop C0/DEL/C1 and bidi controls, keep everything else byte-identical);
// the length cap is this function's own, since textsafe has no size opinion
// and nothing else bounds a daemon-chosen string here.
func cleanPeerText(s string) string {
	s = textsafe.Strip(s)
	if len(s) <= maxPeerTextBytes {
		return s
	}
	out := s[:maxPeerTextBytes]
	for len(out) > 0 && !utf8.ValidString(out) {
		out = out[:len(out)-1]
	}
	return out
}

// LoginClient is the slice of *ipc.Client the login needs. ReceiveByID runs
// before any receive loop owns the conn.
type LoginClient interface {
	Send(*ipc.Message) error
	ReceiveByID(id string, timeout time.Duration) (*ipc.Message, error)
}

// ClientLogin runs the client side of the TCP login: hello{token_id, nonce_c} →
// auth_challenge{nonce_s} → auth_proof → hello_resp, whose server_sig is
// checked BEFORE anything else is sent or accepted. The token never leaves
// this process. step bounds each wait (5 s in production).
func ClientLogin(c LoginClient, token string, hello ipc.HelloPayload, step time.Duration) (ipc.HelloRespPayload, error) {
	var none ipc.HelloRespPayload
	id, err := ParseToken(token)
	if err != nil {
		return none, err
	}
	nonceC, err := NewNonce()
	if err != nil {
		return none, err
	}
	reqID, err := NewNonce()
	if err != nil {
		return none, err
	}
	hello.TokenID, hello.Nonce = id, nonceC
	msg, err := ipc.NewMessage(ipc.MsgHello, hello)
	if err != nil {
		return none, err
	}
	msg.ID = "login-" + reqID[:16]
	if err := c.Send(msg); err != nil {
		return none, fmt.Errorf("send hello: %w", err)
	}

	ch, err := c.ReceiveByID(msg.ID, step)
	if err != nil {
		return none, fmt.Errorf("await auth_challenge: %w", err)
	}
	if err := asRefusal(ch); err != nil {
		return none, err
	}
	if ch.Type != ipc.MsgAuthChallenge {
		return none, fmt.Errorf("login: unexpected %s", cleanPeerText(ch.Type))
	}
	var cp ipc.AuthChallengePayload
	if err := ch.DecodePayload(&cp); err != nil || !ValidNonce(cp.Nonce) {
		return none, errors.New("login: malformed auth_challenge")
	}
	authMsg := AuthMessage(id, nonceC, cp.Nonce)

	proof, err := ipc.NewMessage(ipc.MsgAuthProof, ipc.AuthProofPayload{Proof: ClientProof(token, authMsg)})
	if err != nil {
		return none, err
	}
	proof.ID = msg.ID
	if err := c.Send(proof); err != nil {
		return none, fmt.Errorf("send auth_proof: %w", err)
	}

	resp, err := c.ReceiveByID(msg.ID, step)
	if err != nil {
		return none, fmt.Errorf("await hello_resp: %w", err)
	}
	if err := asRefusal(resp); err != nil {
		return none, err
	}
	if resp.Type != ipc.MsgHelloResp {
		return none, fmt.Errorf("login: unexpected %s", cleanPeerText(resp.Type))
	}
	var hr ipc.HelloRespPayload
	if err := resp.DecodePayload(&hr); err != nil {
		// A hello_resp this process cannot even parse is exactly as unproven
		// as one with a missing or wrong server_sig: wrap ErrServerUnproven
		// rather than a bare decode error so the caller still sees the
		// squatter warning instead of a generic failure.
		return none, fmt.Errorf("login: malformed hello_resp: %w: %w", ErrServerUnproven, err)
	}
	if !CheckServerSignature(token, authMsg, hr.ServerSig) {
		return none, ErrServerUnproven
	}
	return hr, nil
}

func asRefusal(m *ipc.Message) error {
	if m.Type != ipc.MsgError {
		return nil
	}
	var p ipc.ErrorPayload
	if err := m.DecodePayload(&p); err != nil {
		// An "error"-typed frame this process cannot parse is neither a
		// refusal (there is no reason to read) nor a success — surface the
		// decode failure rather than letting an unreadable payload fall
		// through as a refusal with an empty reason.
		return fmt.Errorf("login: malformed error payload: %w", err)
	}
	if p.Code == ipc.ErrCodeRefused {
		return &RefusedError{Reason: cleanPeerText(p.Message)}
	}
	return fmt.Errorf("login: daemon error %s: %s", cleanPeerText(p.Code), cleanPeerText(p.Message))
}
