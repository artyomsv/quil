package ipc

import (
	"sync"
	"sync/atomic"
	"time"
)

// Transports and the local principal.
const (
	TransportLocal = "local"
	TransportTCP   = "tcp"
	PrincipalLocal = "local"
)

// refusalAuditWindow limits refusal audit lines to one per (conn, type) per
// minute. maxRefusalTypes bounds the per-conn map: types are client-chosen
// strings.
const (
	refusalAuditWindow = time.Minute
	maxRefusalTypes    = 256
)

// AuthState is one conn's login: who it is and what it may do. Set once
// (MarkAuthenticated); revoked is the only field that changes afterwards.
type AuthState struct {
	Transport string
	TokenID   string
	TokenName string
	Level     string

	revoked atomic.Bool
	parked  atomic.Int32

	mu        sync.Mutex
	refusedAt map[string]time.Time
}

// newLocalAuth is a local-socket conn's state: full rights, no token. One
// per conn, never a shared singleton: the refusal-audit rate state below is
// per (conn, type) and must die with the conn.
func newLocalAuth() *AuthState {
	return &AuthState{Transport: TransportLocal, Level: RightsFull}
}

// NewTokenAuth is the state of a TCP conn that logged in with a token.
func NewTokenAuth(tokenID, tokenName, level string) *AuthState {
	return &AuthState{Transport: TransportTCP, TokenID: tokenID, TokenName: tokenName, Level: level}
}

// Principal is "local" for any local conn and "token <id>" for a TCP login.
// Client ids are bound to it.
func (a *AuthState) Principal() string {
	if a.Transport == TransportTCP {
		return "token " + a.TokenID
	}
	return PrincipalLocal
}

// ReadOnly is nil-safe: an unauthenticated conn is not a read-only viewer.
func (a *AuthState) ReadOnly() bool { return a != nil && a.Level == RightsReadOnly }

// Revoke silences this conn at once (the first phase of a revoke): Broadcast
// skips it and every send path drops everything but an error frame. Frames
// already queued before it — at most the conn's send buffer, each authorised
// when it was queued — may still be written; nothing new is queued after it.
func (a *AuthState) Revoke() { a.revoked.Store(true) }

// Revoked is nil-safe.
func (a *AuthState) Revoked() bool { return a != nil && a.revoked.Load() }

// TryPark claims one of limit slots for a request that parks a goroutine.
func (a *AuthState) TryPark(limit int32) bool {
	for {
		n := a.parked.Load()
		if n >= limit {
			return false
		}
		if a.parked.CompareAndSwap(n, n+1) {
			return true
		}
	}
}

// Unpark returns a slot TryPark claimed.
func (a *AuthState) Unpark() { a.parked.Add(-1) }

// ShouldAuditRefusal answers true at most once per message type per minute.
func (a *AuthState) ShouldAuditRefusal(msgType string, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.refusedAt == nil || len(a.refusedAt) >= maxRefusalTypes {
		a.refusedAt = make(map[string]time.Time)
	}
	if last, ok := a.refusedAt[msgType]; ok && now.Sub(last) < refusalAuditWindow {
		return false
	}
	a.refusedAt[msgType] = now
	return true
}
