package clientauth

import (
	"errors"
	"strings"
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

	// refuseReason overrides the default refusal message, when non-empty —
	// used to smuggle control/bidi runes into a RefusedError.Reason.
	refuseReason string
	// challengeNonce overrides the auth_challenge's nonce, when non-nil.
	challengeNonce *string
	// challengeType overrides the auth_challenge frame's Type, when non-empty
	// — a squatter picks this string, not us.
	challengeType string
	// errCodeAtHello, when non-empty, answers hello with an `error` frame of
	// this code (never ipc.ErrCodeRefused) instead of a challenge.
	errCodeAtHello string
	// malformedErrorAtHello answers hello with an `error` frame whose payload
	// will not decode into ipc.ErrorPayload.
	malformedErrorAtHello bool
	// malformedHelloResp answers a verified proof with a hello_resp whose
	// payload will not decode into ipc.HelloRespPayload.
	malformedHelloResp bool
}

func (f *fakeDaemon) Send(m *ipc.Message) error {
	f.sent = append(f.sent, m)
	switch m.Type {
	case ipc.MsgHello:
		_ = m.DecodePayload(&f.hello)
		if f.refuseAt == "hello" {
			f.reply(m.ID, ipc.MsgError, ipc.ErrorPayload{Code: ipc.ErrCodeRefused, Message: f.refusalText("login required")})
			return nil
		}
		if f.errCodeAtHello != "" {
			f.reply(m.ID, ipc.MsgError, ipc.ErrorPayload{Code: f.errCodeAtHello, Message: "rate limited"})
			return nil
		}
		if f.malformedErrorAtHello {
			f.reply(m.ID, ipc.MsgError, []string{"not", "an", "object"})
			return nil
		}
		f.nonceS, _ = NewNonce()
		if f.challengeNonce != nil {
			f.nonceS = *f.challengeNonce
		}
		typ := ipc.MsgAuthChallenge
		if f.challengeType != "" {
			typ = f.challengeType
		}
		f.reply(m.ID, typ, ipc.AuthChallengePayload{Nonce: f.nonceS})
	case ipc.MsgAuthProof:
		var p ipc.AuthProofPayload
		_ = m.DecodePayload(&p)
		am := AuthMessage(f.hello.TokenID, f.hello.Nonce, f.nonceS)
		if f.refuseAt == "proof" || !VerifyProof(f.verifier, am, p.Proof) {
			f.reply(m.ID, ipc.MsgError, ipc.ErrorPayload{Code: ipc.ErrCodeRefused, Message: f.refusalText("token refused")})
			return nil
		}
		if f.malformedHelloResp {
			f.reply(m.ID, ipc.MsgHelloResp, []int{1, 2, 3})
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

// refusalText returns the configured override, or def when none was set.
func (f *fakeDaemon) refusalText(def string) string {
	if f.refuseReason != "" {
		return f.refuseReason
	}
	return def
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

// TestClientLogin_ChallengeGates drives the fake daemon's FIRST reply through
// every way it can fail to be a usable auth_challenge: a nonce that cannot be
// a real one, a frame of the wrong type entirely, and a daemon error whose
// code is something other than "refused" (which must never surface as
// *RefusedError — that type means specifically a login the daemon refused).
func TestClientLogin_ChallengeGates(t *testing.T) {
	tok, _, _ := NewToken()
	shortNonce := b64.EncodeToString(make([]byte, 31)) // valid base64url, wrong length

	cases := []struct {
		name      string
		configure func(f *fakeDaemon)
		check     func(t *testing.T, err error)
	}{
		{
			name: "empty nonce",
			configure: func(f *fakeDaemon) {
				n := ""
				f.challengeNonce = &n
			},
			check: func(t *testing.T, err error) {
				if err == nil {
					t.Fatal("want an error for an empty challenge nonce")
				}
			},
		},
		{
			name: "31-byte nonce",
			configure: func(f *fakeDaemon) {
				f.challengeNonce = &shortNonce
			},
			check: func(t *testing.T, err error) {
				if err == nil {
					t.Fatal("want an error for a short challenge nonce")
				}
			},
		},
		{
			name: "not base64url",
			configure: func(f *fakeDaemon) {
				n := "not base64url!!"
				f.challengeNonce = &n
			},
			check: func(t *testing.T, err error) {
				if err == nil {
					t.Fatal("want an error for a malformed challenge nonce")
				}
			},
		},
		{
			name: "hello_resp instead of challenge",
			configure: func(f *fakeDaemon) {
				f.challengeType = ipc.MsgHelloResp
			},
			check: func(t *testing.T, err error) {
				if err == nil {
					t.Fatal("want an error when the daemon skips the challenge")
				}
			},
		},
		{
			name: "error code is not refused",
			configure: func(f *fakeDaemon) {
				f.errCodeAtHello = ipc.ErrCodeBadPayload
			},
			check: func(t *testing.T, err error) {
				var refused *RefusedError
				if err == nil || errors.As(err, &refused) {
					t.Fatalf("a non-refused error code must not become *RefusedError: %v", err)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t, tok)
			tc.configure(f)
			_, err := ClientLogin(f, tok, testHello, time.Second)
			tc.check(t, err)
		})
	}
}

// TestClientLogin_MaliciousChallengeTypeSanitized: the message type on an
// unauthenticated conn is a squatter's choice, and it lands in "login:
// unexpected %s" before CheckServerSignature ever runs. ESC and a bidi
// override must not survive into the error text a later task prints.
func TestClientLogin_MaliciousChallengeTypeSanitized(t *testing.T) {
	tok, _, _ := NewToken()
	f := newFake(t, tok)
	f.challengeType = "weird" + string(rune(0x1b)) + string(rune(0x202e)) + "-type"

	_, err := ClientLogin(f, tok, testHello, time.Second)
	if err == nil {
		t.Fatal("want an error for an unexpected challenge type")
	}
	msg := err.Error()
	if strings.ContainsRune(msg, 0x1b) || strings.ContainsRune(msg, 0x202e) {
		t.Fatalf("error text carries a raw control/bidi rune from the peer: %q", msg)
	}
}

// TestClientLogin_MaliciousRefusalReasonSanitized: same attack, through the
// daemon's chosen refusal reason — it must be cleaned before it is ever
// stored in RefusedError.Reason, since a later task puts that text in a park
// reason as well as an error message.
func TestClientLogin_MaliciousRefusalReasonSanitized(t *testing.T) {
	tok, _, _ := NewToken()
	f := newFake(t, tok)
	f.refuseAt = "hello"
	f.refuseReason = "login required" + string(rune(0x1b)) + string(rune(0x202e))

	_, err := ClientLogin(f, tok, testHello, time.Second)
	var refused *RefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("want *RefusedError, got %v", err)
	}
	if strings.ContainsRune(refused.Reason, 0x1b) || strings.ContainsRune(refused.Reason, 0x202e) {
		t.Fatalf("RefusedError.Reason carries a raw control/bidi rune: %q", refused.Reason)
	}
	if strings.ContainsRune(refused.Error(), 0x1b) || strings.ContainsRune(refused.Error(), 0x202e) {
		t.Fatalf("RefusedError.Error() carries a raw control/bidi rune: %q", refused.Error())
	}
}

// TestClientLogin_MalformedHelloRespIsServerUnproven: a hello_resp this
// process cannot even parse is exactly as unproven as one with a missing or
// wrong server_sig — the caller must still see the squatter warning, not a
// generic decode failure.
func TestClientLogin_MalformedHelloRespIsServerUnproven(t *testing.T) {
	tok, _, _ := NewToken()
	f := newFake(t, tok)
	f.malformedHelloResp = true

	if _, err := ClientLogin(f, tok, testHello, time.Second); !errors.Is(err, ErrServerUnproven) {
		t.Fatalf("err = %v, want ErrServerUnproven", err)
	}
}

// TestClientLogin_MalformedErrorPayloadIsNotRefusal: an `error` frame whose
// payload will not decode is not a refusal (there is no reason to read) and
// not a success either — it must surface as its own error, never silently
// become a *RefusedError with an empty Reason.
func TestClientLogin_MalformedErrorPayloadIsNotRefusal(t *testing.T) {
	tok, _, _ := NewToken()
	f := newFake(t, tok)
	f.malformedErrorAtHello = true

	_, err := ClientLogin(f, tok, testHello, time.Second)
	if err == nil {
		t.Fatal("want an error for a malformed error payload")
	}
	var refused *RefusedError
	if errors.As(err, &refused) {
		t.Fatalf("a malformed error payload must not be read as a refusal: %v", err)
	}
}

func TestCleanPeerText_StripsControlAndBidiRunes(t *testing.T) {
	evil := "safe" + string(rune(0x1b)) + string(rune(0x202e)) + "text"
	got := cleanPeerText(evil)
	if strings.ContainsRune(got, 0x1b) || strings.ContainsRune(got, 0x202e) {
		t.Fatalf("cleanPeerText left a control/bidi rune: %q", got)
	}
}

func TestCleanPeerText_CapsAt256Bytes(t *testing.T) {
	got := cleanPeerText(strings.Repeat("x", 1000))
	if len(got) > 256 {
		t.Fatalf("got %d bytes, want <= 256", len(got))
	}
}
