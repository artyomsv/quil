package webgw

import (
	"crypto/rand"
	"regexp"
	"strings"
	"sync"
	"testing"
)

func TestLeases(t *testing.T) {
	l := newLeases(rand.Reader)
	a := l.acquire("", "s")
	if !regexp.MustCompile(`^web-` + l.prefix() + `-[0-9a-f]{12}$`).MatchString(a) {
		t.Fatalf("id %q is not the minted shape", a)
	}
	// A duplicated browser tab presents a live id: it gets a new one.
	if b := l.acquire(a, "s"); b == a {
		t.Fatal("a live id was leased twice")
	}
	// A reload after the tab closed gets its id back.
	l.release(a)
	if c := l.acquire(a, "s"); c != a {
		t.Fatalf("free own id not returned: %q", c)
	}
	// An id from another gateway is never adopted.
	if d := l.acquire("web-ffffffff-123", "s"); d == "web-ffffffff-123" {
		t.Fatal("a foreign id was adopted")
	}
	// renew frees the old id and mints a new one.
	e := l.renew(a, "s")
	if e == a {
		t.Fatal("renew returned the same id")
	}
	if f := l.acquire(a, "s"); f != a {
		t.Fatal("renew did not free the old id")
	}
}

func TestLeases_OnlyMintedIDsAreAdopted(t *testing.T) {
	l := newLeases(rand.Reader)
	good := regexp.MustCompile(`^web-` + l.prefix() + `-[0-9a-f]{12}$`)
	for name, hint := range map[string]string{
		"well formed, never minted": "web-" + l.prefix() + "-0123456789ab",
		"over long":                 "web-" + l.prefix() + "-" + strings.Repeat("a", 1<<20),
		"control rune":              "web-" + l.prefix() + "-\x1b[31mabcd",
		"bidi override":             "web-" + l.prefix() + "-" + string(rune(0x202e)) + "abcd",
		"shares first 64 bytes":     "web-" + l.prefix() + "-" + strings.Repeat("b", 60) + "x",
	} {
		got := l.acquire(hint, "s")
		if got == hint || !good.MatchString(got) {
			t.Errorf("%s: acquire returned %q", name, got)
		}
	}
}

func TestLeases_ConcurrentAcquireReleaseNeverSharesALiveID(t *testing.T) {
	l := newLeases(rand.Reader)
	var mu sync.Mutex
	holders := map[string]int{}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			hint := ""
			for i := 0; i < 200; i++ {
				id := l.acquire(hint, "s")
				mu.Lock()
				holders[id]++
				n := holders[id]
				mu.Unlock()
				if n != 1 {
					t.Errorf("id %q held by %d tabs at once", id, n)
				}
				mu.Lock()
				holders[id]--
				mu.Unlock()
				l.release(id)
				hint = id // reload: offer the id just freed
			}
		}()
	}
	wg.Wait()
}

func TestLeases_AnotherSessionsReleasedIDIsNotAdopted(t *testing.T) {
	l := newLeases(rand.Reader)
	a := l.acquire("", "owner")
	l.release(a)
	if got := l.acquire(a, "other"); got == a {
		t.Fatal("another session adopted a released id")
	}
	if got := l.acquire(a, "owner"); got != a {
		t.Fatalf("the minting session did not get its id back: %q", got)
	}
}
