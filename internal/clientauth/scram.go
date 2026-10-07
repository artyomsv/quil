package clientauth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
)

// NonceBytes is the size of each side's login nonce, sent base64url.
const NonceBytes = 32

// authPrefix versions AuthMessage: a later scheme changes the prefix, so a
// proof made for one scheme can never verify under another.
const authPrefix = "quil-auth-v1,"

// Verifier is what the daemon stores per token INSTEAD of the token:
// StoredKey = SHA256(ClientKey), ServerKey = HMAC(token, "Server Key"). It can
// check a proof and sign as the daemon; it cannot produce a client proof, so a
// stolen tokens.json impersonates the daemon to a client but cannot log in.
type Verifier struct {
	StoredKey []byte
	ServerKey []byte
}

func hmacSHA256(key, msg []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(msg)
	return m.Sum(nil)
}

// clientKey is HMAC(token, "Client Key"). There is no password stretching: a
// token already carries 256 random bits (RFC 5802's PBKDF2 step is omitted).
func clientKey(token string) []byte {
	return hmacSHA256([]byte(token), []byte("Client Key"))
}

// DeriveVerifier computes the stored half of a token.
func DeriveVerifier(token string) Verifier {
	stored := sha256.Sum256(clientKey(token))
	return Verifier{
		StoredKey: stored[:],
		ServerKey: hmacSHA256([]byte(token), []byte("Server Key")),
	}
}

// AuthMessage is the exact byte string both sides sign:
// "quil-auth-v1," + token_id + "," + nonce_c + "," + nonce_s, the nonces as the
// base64url strings that crossed the wire. It binds both nonces and the id.
func AuthMessage(tokenID, nonceC, nonceS string) []byte {
	return []byte(authPrefix + tokenID + "," + nonceC + "," + nonceS)
}

// NewNonce returns NonceBytes random bytes, base64url without padding.
func NewNonce() (string, error) {
	b := make([]byte, NonceBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("nonce: %w", err)
	}
	return b64.EncodeToString(b), nil
}

// ValidNonce reports whether n is the canonical encoding of exactly
// NonceBytes: AuthMessage signs the string that crossed the wire, so a second
// spelling of the same bytes must not pass.
func ValidNonce(n string) bool {
	return canonicalB64(n, NonceBytes)
}

// ClientProof is ClientKey XOR HMAC(StoredKey, AuthMessage), base64url.
func ClientProof(token string, authMsg []byte) string {
	ck := clientKey(token)
	stored := sha256.Sum256(ck)
	return b64.EncodeToString(xorBytes(ck, hmacSHA256(stored[:], authMsg)))
}

// VerifyProof recovers ClientKey from a proof and checks
// SHA256(ClientKey) == StoredKey in constant time. A malformed proof does the
// same work as a wrong one, so the answer time does not tell them apart.
func VerifyProof(v Verifier, authMsg []byte, proofB64 string) bool {
	sig := hmacSHA256(v.StoredKey, authMsg)
	proof, err := b64.DecodeString(proofB64)
	wellFormed := err == nil && len(proof) == len(sig)
	if !wellFormed {
		proof = make([]byte, len(sig))
	}
	recovered := sha256.Sum256(xorBytes(proof, sig))
	match := subtle.ConstantTimeCompare(recovered[:], v.StoredKey) == 1
	return wellFormed && match
}

// ServerSignature is HMAC(ServerKey, AuthMessage), base64url — the daemon's
// proof that it holds this token's verifier.
func ServerSignature(v Verifier, authMsg []byte) string {
	return b64.EncodeToString(hmacSHA256(v.ServerKey, authMsg))
}

// CheckServerSignature is the client's check, in constant time. A missing or
// undecodable signature fails.
func CheckServerSignature(token string, authMsg []byte, sigB64 string) bool {
	got, err := b64.DecodeString(sigB64)
	if err != nil || len(got) == 0 {
		return false
	}
	return hmac.Equal(got, hmacSHA256(DeriveVerifier(token).ServerKey, authMsg))
}

// xorBytes XORs equal-length slices (both are SHA-256 sized here).
func xorBytes(a, b []byte) []byte {
	out := make([]byte, len(a))
	for i := range a {
		out[i] = a[i] ^ b[i]
	}
	return out
}
