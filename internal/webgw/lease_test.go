package webgw

import (
	"crypto/rand"
	"strings"
	"testing"
)

func TestLeases(t *testing.T) {
	l := newLeases(rand.Reader)
	a := l.acquire("")
	if !strings.HasPrefix(a, "web-"+l.prefix()+"-") {
		t.Fatalf("id %q lacks the gateway prefix", a)
	}
	// A duplicated browser tab presents a live id: it gets a new one.
	if b := l.acquire(a); b == a {
		t.Fatal("a live id was leased twice")
	}
	// A reload after the tab closed gets its id back.
	l.release(a)
	if c := l.acquire(a); c != a {
		t.Fatalf("free own id not returned: %q", c)
	}
	// An id from another gateway is never adopted.
	if d := l.acquire("web-ffffffff-123"); d == "web-ffffffff-123" {
		t.Fatal("a foreign id was adopted")
	}
	// renew frees the old id and mints a new one.
	e := l.renew(a)
	if e == a {
		t.Fatal("renew returned the same id")
	}
	if f := l.acquire(a); f != a {
		t.Fatal("renew did not free the old id")
	}
}
