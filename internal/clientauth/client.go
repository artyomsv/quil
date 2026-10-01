package clientauth

import (
	"errors"
	"fmt"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

// ErrServerUnproven: the listener answered but could not show it knows the
// token's verifier — a port squatter, or a stale daemon with another store.
var ErrServerUnproven = errors.New("the listener could not prove it is your daemon")

// RefusedError is the daemon's `error refused` during login.
type RefusedError struct{ Reason string }

func (e *RefusedError) Error() string { return "refused: " + e.Reason }

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
		return none, fmt.Errorf("login: unexpected %s", ch.Type)
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
		return none, fmt.Errorf("login: unexpected %s", resp.Type)
	}
	var hr ipc.HelloRespPayload
	if err := resp.DecodePayload(&hr); err != nil {
		return none, fmt.Errorf("login: malformed hello_resp: %w", err)
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
	_ = m.DecodePayload(&p)
	if p.Code == ipc.ErrCodeRefused {
		return &RefusedError{Reason: p.Message}
	}
	return fmt.Errorf("login: daemon error %s: %s", p.Code, p.Message)
}
