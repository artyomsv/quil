// Package clientauth is client authentication and rights for the daemon's
// loopback TCP listener (#238): the token format, the SCRAM-shaped
// mutual proof, the rights table every request is checked against, the token
// store, and the client side of the login.
//
// It imports the standard library, internal/ipc and internal/textsafe only, so
// every rule here tests without a daemon. Tests that prove ENFORCEMENT live in
// internal/daemon and drive real conns; a test of this package proves the
// table, not that anything consults it.
package clientauth

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/artyomsv/quil/internal/ipc"
)

// Level is a token's rights level.
type Level string

const (
	LevelReadOnly Level = ipc.RightsReadOnly
	LevelStandard Level = ipc.RightsStandard
	LevelFull     Level = ipc.RightsFull
)

// ParseLevel accepts the three level names; "" is the default, standard.
func ParseLevel(s string) (Level, error) {
	switch Level(s) {
	case "":
		return LevelStandard, nil
	case LevelReadOnly, LevelStandard, LevelFull:
		return Level(s), nil
	}
	return "", fmt.Errorf("unknown rights %q (want read-only, standard or full)", s)
}

const (
	tokenPrefix = "qtk_"
	idHexLen    = 8
	secretBytes = 32
)

// ErrBadToken is returned for a string that is not shaped like a token.
var ErrBadToken = errors.New("not a quil token (want qtk_<8 hex>_<43 base64url characters>)")

var b64 = base64.RawURLEncoding

// NewToken mints qtk_<id>_<secret>: the id is 8 lowercase hex digits so a
// client can name its token in hello without a lookup; the secret is 32 bytes
// from crypto/rand. The WHOLE string is the HMAC key; the daemon never
// stores it.
func NewToken() (token, id string, err error) {
	idRaw := make([]byte, idHexLen/2)
	if _, err := rand.Read(idRaw); err != nil {
		return "", "", fmt.Errorf("token id: %w", err)
	}
	secret := make([]byte, secretBytes)
	if _, err := rand.Read(secret); err != nil {
		return "", "", fmt.Errorf("token secret: %w", err)
	}
	id = hex.EncodeToString(idRaw)
	return tokenPrefix + id + "_" + b64.EncodeToString(secret), id, nil
}

// ParseToken checks a token's shape and returns its id. The secret may itself
// contain '_' (base64url), so only the FIRST '_' after the prefix separates.
func ParseToken(token string) (string, error) {
	rest, ok := strings.CutPrefix(token, tokenPrefix)
	if !ok {
		return "", ErrBadToken
	}
	id, secret, ok := strings.Cut(rest, "_")
	if !ok || !ValidID(id) {
		return "", ErrBadToken
	}
	raw, err := b64.DecodeString(secret)
	if err != nil || len(raw) != secretBytes {
		return "", ErrBadToken
	}
	return id, nil
}

// ValidID reports whether id is 8 lowercase hex digits.
func ValidID(id string) bool {
	if len(id) != idHexLen {
		return false
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
