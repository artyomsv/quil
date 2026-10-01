package clientauth

import (
	"errors"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

// fakeDaemon plays the daemon side in memory. It verifies the client's proof
// with the REAL verifier, so a passing login proves both sides built the same
// AuthMessage bytes.
type fakeDaemon struct {
	t         *testing.T
	signToken string // token whose ServerKey signs (a squatter uses another)
	verifier  Verifier
	refuseAt  string // "hello" or "proof" to refuse there
	omitSig   bool
	sent      []*ipc.Message
	queue     []*ipc.Message
	nonceS    string
	hello     ipc.HelloPayload
}

func (f *fakeDaemon) Send(m *ipc.Message) error {
	f.sent = append(f.sent, m)
	switch m.Type {
	case ipc.MsgHello:
		_ = m.DecodePayload(&f.hello)
		if f.refuseAt == "hello" {
			f.reply(m.ID, ipc.MsgError, ipc.ErrorPayload{Code: ipc.ErrCodeRefused, Message: "login required"})
			return nil
		}
		f.nonceS, _ = NewNonce()
		f.reply(m.ID, ipc.MsgAuthChallenge, ipc.AuthChallengePayload{Nonce: f.nonceS})
	case ipc.MsgAuthProof:
		var p ipc.AuthProofPayload
		_ = m.DecodePayload(&p)
		am := AuthMessage(f.hello.TokenID, f.hello.Nonce, f.nonceS)
		if f.refuseAt == "proof" || !VerifyProof(f.verifier, am, p.Proof) {
			f.reply(m.ID, ipc.MsgError, ipc.ErrorPayload{Code: ipc.ErrCodeRefused, Message: "token refused"})
			return nil
		}
		resp := ipc.HelloRespPayload{Rights: ipc.RightsReadOnly, TokenName: "laptop"}
		if !f.omitSig {
			resp.ServerSig = ServerSignature(DeriveVerifier(f.signToken), am)
		}
		f.reply(m.ID, ipc.MsgHelloResp, resp)
	}
	return nil
}

func (f *fakeDaemon) reply(id, typ string, payload any) {
	m, err := ipc.NewMessage(typ, payload)
	if err != nil {
		f.t.Fatal(err)
	}
	m.ID = id
	f.queue = append(f.queue, m)
}

func (f *fakeDaemon) ReceiveByID(id string, _ time.Duration) (*ipc.Message, error) {
	for i, m := range f.queue {
		if m.ID == id {
			f.queue = append(f.queue[:i], f.queue[i+1:]...)
			return m, nil
		}
	}
	return nil, errors.New("timeout")
}

func newFake(t *testing.T, token string) *fakeDaemon {
	return &fakeDaemon{t: t, signToken: token, verifier: DeriveVerifier(token)}
}

var testHello = ipc.HelloPayload{Kind: "tui", Proto: ipc.ProtocolVersion, ClientID: "c"}

func TestClientLogin_Succeeds(t *testing.T) {
	tok, id, _ := NewToken()
	f := newFake(t, tok)
	resp, err := ClientLogin(f, tok, testHello, time.Second)
	if err != nil || resp.Rights != ipc.RightsReadOnly || resp.TokenName != "laptop" {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
	if f.hello.TokenID != id || !ValidNonce(f.hello.Nonce) {
		t.Fatalf("login hello = %+v", f.hello)
	}
	if f.hello.Kind != "tui" {
		t.Fatal("the caller's hello fields were not sent")
	}
}

// A listener that does not know the keys cannot produce server_sig, and
// the client sends nothing after its proof.
func TestClientLogin_WrongServerSigRefused(t *testing.T) {
	tok, _, _ := NewToken()
	squatter, _, _ := NewToken()
	f := newFake(t, tok)
	f.signToken = squatter
	if _, err := ClientLogin(f, tok, testHello, time.Second); !errors.Is(err, ErrServerUnproven) {
		t.Fatalf("err = %v, want ErrServerUnproven", err)
	}
	if len(f.sent) != 2 {
		t.Fatalf("client sent %d frames, want exactly hello + proof", len(f.sent))
	}
}

func TestClientLogin_MissingServerSigRefused(t *testing.T) {
	tok, _, _ := NewToken()
	f := newFake(t, tok)
	f.omitSig = true
	if _, err := ClientLogin(f, tok, testHello, time.Second); !errors.Is(err, ErrServerUnproven) {
		t.Fatalf("err = %v", err)
	}
}

func TestClientLogin_RefusalsCarryReason(t *testing.T) {
	tok, _, _ := NewToken()
	for _, at := range []string{"hello", "proof"} {
		f := newFake(t, tok)
		f.refuseAt = at
		_, err := ClientLogin(f, tok, testHello, time.Second)
		var refused *RefusedError
		if !errors.As(err, &refused) || refused.Reason == "" {
			t.Fatalf("refused at %s: err = %v", at, err)
		}
	}
	// A wrong token is refused by the daemon's verification.
	other, _, _ := NewToken()
	f := newFake(t, other)
	var refused *RefusedError
	if _, err := ClientLogin(f, tok, testHello, time.Second); !errors.As(err, &refused) {
		t.Fatalf("wrong token: err = %v", err)
	}
}

func TestClientLogin_BadTokenSendsNothing(t *testing.T) {
	f := newFake(t, "x")
	if _, err := ClientLogin(f, "not-a-token", testHello, time.Second); !errors.Is(err, ErrBadToken) {
		t.Fatalf("err = %v", err)
	}
	if len(f.sent) != 0 {
		t.Fatal("a malformed token reached the wire")
	}
}
