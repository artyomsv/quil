package clientauth

import (
	"encoding/hex"
	"strings"
	"testing"
)

// Known-answer vectors. Inputs are fixed test values (secret bytes
// 0x00..0x1f, nonces of 32 x 0x11 and 32 x 0x22); outputs were computed
// independently with .NET HMACSHA256 and cross-checked with
// `openssl dgst -sha256 -hmac`. They are never written to any store.
const (
	katToken  = "qtk_0a1b2c3d_AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"
	katID     = "0a1b2c3d"
	katNonceC = "ERERERERERERERERERERERERERERERERERERERERERE"
	katNonceS = "IiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiI"
	katStored = "e22158056ad07a2f2d55198904be38f1efb66f745c584ad3d8267f16e2ce2ad9"
	katServer = "7213b703738343b8b982632ea8fb5777b71d414cc83ec6295e52068c9e84774f"
	katProof  = "JvyeXfSmfWv1QnsLQJjueDORQG0lYU3ioJzAmBC8ck8"
	katSig    = "oiNzGQEW1-W46eNOY5C7mxLTnNemvlUVv5B1gf4XDPw"
)

func katAuthMessage() []byte { return AuthMessage(katID, katNonceC, katNonceS) }

func TestAuthMessage_ExactBytes(t *testing.T) {
	want := "quil-auth-v1," + katID + "," + katNonceC + "," + katNonceS
	if got := string(katAuthMessage()); got != want {
		t.Fatalf("AuthMessage = %q, want %q", got, want)
	}
}

func TestDeriveVerifier_KnownAnswer(t *testing.T) {
	v := DeriveVerifier(katToken)
	if got := hex.EncodeToString(v.StoredKey); got != katStored {
		t.Errorf("StoredKey = %s, want %s", got, katStored)
	}
	if got := hex.EncodeToString(v.ServerKey); got != katServer {
		t.Errorf("ServerKey = %s, want %s", got, katServer)
	}
}

func TestClientProof_KnownAnswer(t *testing.T) {
	if got := ClientProof(katToken, katAuthMessage()); got != katProof {
		t.Fatalf("ClientProof = %s, want %s", got, katProof)
	}
}

func TestServerSignature_KnownAnswer(t *testing.T) {
	if got := ServerSignature(DeriveVerifier(katToken), katAuthMessage()); got != katSig {
		t.Fatalf("ServerSignature = %s, want %s", got, katSig)
	}
	if !CheckServerSignature(katToken, katAuthMessage(), katSig) {
		t.Error("client rejects the daemon's correct signature")
	}
	other, _ := mustNewToken(t)
	if CheckServerSignature(other, katAuthMessage(), katSig) {
		t.Error("a signature for another token verified")
	}
	if CheckServerSignature(katToken, katAuthMessage(), "") {
		t.Error("a MISSING signature verified")
	}
	if CheckServerSignature(katToken, katAuthMessage(), katSig[:len(katSig)-3]) {
		t.Error("a truncated signature verified")
	}
	if CheckServerSignature(katToken, katAuthMessage(), katSig+"AAAA") {
		t.Error("an over-long signature verified")
	}
}

func TestVerifyProof_AcceptsKnownProof(t *testing.T) {
	if !VerifyProof(DeriveVerifier(katToken), katAuthMessage(), katProof) {
		t.Fatal("the known-good proof was refused")
	}
}

// A squatter or eavesdropper that captured katProof cannot replay it: the
// daemon's next login uses a fresh nonce_s, so AuthMessage differs.
func TestVerifyProof_ReplayUnderNewNonceRefused(t *testing.T) {
	fresh, err := NewNonce()
	if err != nil {
		t.Fatal(err)
	}
	if VerifyProof(DeriveVerifier(katToken), AuthMessage(katID, katNonceC, fresh), katProof) {
		t.Fatal("a replayed proof verified under a new server nonce")
	}
}

func TestVerifyProof_RefusesGarbageAndWrongToken(t *testing.T) {
	v := DeriveVerifier(katToken)
	for _, p := range []string{"", "!!!", "AAAA", katProof + "AA"} {
		if VerifyProof(v, katAuthMessage(), p) {
			t.Errorf("garbage proof %q verified", p)
		}
	}
	other, _ := mustNewToken(t)
	if VerifyProof(v, katAuthMessage(), ClientProof(other, katAuthMessage())) {
		t.Error("another token's proof verified")
	}
}

func TestNonce_ShapeChecked(t *testing.T) {
	n, err := NewNonce()
	if err != nil || !ValidNonce(n) {
		t.Fatalf("NewNonce = %q, %v", n, err)
	}
	for _, bad := range []string{
		"", "abc", katNonceC + "A",
		// The decoder skips CR and LF: both decoded to katNonceC's 32 bytes.
		katNonceC + "\n", katNonceC[:10] + "\r\n" + katNonceC[10:],
		// 'F' sets a padding bit that 'E' leaves clear: same bytes, second spelling.
		katNonceC[:len(katNonceC)-1] + "F",
		strings.Repeat("*", len(katNonceC)),
	} {
		if ValidNonce(bad) {
			t.Errorf("ValidNonce(%q) = true", bad)
		}
	}
}
