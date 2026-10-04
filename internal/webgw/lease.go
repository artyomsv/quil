package webgw

import (
	"encoding/hex"
	"io"
	"strings"
	"sync"
)

// leases hands each browser tab a client id. A page offers the id it had
// (sessionStorage); it gets it back only if this gateway minted it and no
// other live tab holds it — a duplicated browser tab copies sessionStorage,
// and two live tabs with one id would replace each other in the daemon.
type leases struct {
	rand io.Reader
	pfx  string
	mu   sync.Mutex
	live map[string]bool
}

func newLeases(r io.Reader) *leases {
	return &leases{rand: r, pfx: randHex(r, 4), live: map[string]bool{}}
}

func (l *leases) prefix() string { return l.pfx }

func (l *leases) acquire(hint string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if strings.HasPrefix(hint, "web-"+l.pfx+"-") && !l.live[hint] {
		l.live[hint] = true
		return hint
	}
	id := "web-" + l.pfx + "-" + randHex(l.rand, 6)
	l.live[id] = true
	return id
}

func (l *leases) release(id string) {
	l.mu.Lock()
	delete(l.live, id)
	l.mu.Unlock()
}

func (l *leases) renew(old string) string {
	l.release(old)
	return l.acquire("")
}

func randHex(r io.Reader, n int) string {
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		panic(err) // crypto/rand does not fail on supported platforms
	}
	return hex.EncodeToString(b)
}
