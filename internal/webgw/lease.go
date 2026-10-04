package webgw

import (
	"encoding/hex"
	"io"
	"sync"
)

// leases hands each browser tab a client id. A page offers the id it had
// (sessionStorage); it gets it back only if this gateway minted that exact id
// and no other live tab holds it. Anything else — a duplicated browser tab's
// copy of a live id, an id from another gateway or another session, a made-up, over-long or
// control-character string — gets a freshly minted id, so every id a daemon
// sees has the minted shape and two live tabs never share one (they would
// replace each other in the daemon).
type leases struct {
	rand   io.Reader
	pfx    string
	mu     sync.Mutex
	minted map[string]string // id -> the session that minted it
	live   map[string]bool
}

func newLeases(r io.Reader) *leases {
	return &leases{rand: r, pfx: randHex(r, 4), minted: map[string]string{}, live: map[string]bool{}}
}

func (l *leases) prefix() string { return l.pfx }

func (l *leases) acquire(hint, session string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if owner, ok := l.minted[hint]; ok && owner == session && !l.live[hint] {
		l.live[hint] = true
		return hint
	}
	id := "web-" + l.pfx + "-" + randHex(l.rand, 6)
	l.minted[id] = session
	l.live[id] = true
	return id
}

func (l *leases) release(id string) {
	l.mu.Lock()
	delete(l.live, id)
	l.mu.Unlock()
}

func (l *leases) renew(old, session string) string {
	l.mu.Lock()
	delete(l.live, old)
	l.mu.Unlock()
	return l.acquire("", session)
}

func randHex(r io.Reader, n int) string {
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		panic(err) // crypto/rand does not fail on supported platforms
	}
	return hex.EncodeToString(b)
}
