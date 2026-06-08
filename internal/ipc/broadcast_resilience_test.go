package ipc_test

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

// TestBroadcast_SlowConnDoesNotBlockFastConn proves the wedge defense: when
// one client stops reading from its socket, the daemon's broadcast loop must
// continue serving the healthy clients without delay. The Bubble Tea event
// loop on a connected TUI cannot be allowed to stall the entire daemon.
func TestBroadcast_SlowConnDoesNotBlockFastConn(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "slow-vs-fast.sock")

	srv := ipc.NewServer(sockPath, func(*ipc.Conn, *ipc.Message) {}, nil)
	if err := srv.Start(); err != nil {
		t.Fatalf("server start: %v", err)
	}
	defer srv.Stop()

	fast, err := ipc.NewClient(sockPath)
	if err != nil {
		t.Fatalf("fast client connect: %v", err)
	}
	defer fast.Close()

	slow, err := ipc.NewClient(sockPath)
	if err != nil {
		t.Fatalf("slow client connect: %v", err)
	}
	defer slow.Close()

	// Wait for the server to register both connections.
	time.Sleep(100 * time.Millisecond)

	// Slow client deliberately never reads — its kernel socket buffer fills up,
	// then the daemon's 64-slot per-conn queue overflows, then the daemon
	// closes the slow conn. Meanwhile the fast client must keep receiving.

	const broadcasts = 200
	const fastReceives = 50

	// Drain fast client in the background to a counter, so we can assert it
	// got messages quickly without blocking.
	gotFast := make(chan int, broadcasts)
	go func() {
		count := 0
		for {
			if _, err := fast.Receive(); err != nil {
				close(gotFast)
				return
			}
			count++
			gotFast <- count
		}
	}()

	// Build a 4 KiB-ish payload so each broadcast meaningfully exercises the
	// per-conn send queue. Pure echo of an arbitrary string.
	payload := map[string]string{"data": string(make([]byte, 4000))}

	broadcastStart := time.Now()
	for i := 0; i < broadcasts; i++ {
		msg, _ := ipc.NewMessage(ipc.MsgStateUpdate, payload)
		srv.Broadcast(msg)
	}
	broadcastDur := time.Since(broadcastStart)

	// All Broadcast calls must return promptly even though one peer is
	// stalled. 1s gives plenty of headroom for CI jitter; the actual cost
	// is microseconds.
	if broadcastDur > time.Second {
		t.Errorf("Broadcast loop blocked: %d broadcasts took %v (want < 1s) — slow client wedged the fan-out", broadcasts, broadcastDur)
	}

	// Fast client must drain enough messages to demonstrate it's still being
	// served. Healthy peers never stall.
	timeout := time.After(3 * time.Second)
	for {
		select {
		case n, ok := <-gotFast:
			if !ok {
				t.Fatal("fast client got an error before reaching the expected message count")
			}
			if n >= fastReceives {
				return // success
			}
		case <-timeout:
			t.Fatalf("fast client only drained partway within 3s — broadcast fan-out may be wedged")
		}
	}
}

// TestBroadcast_ContinuesAfterSlowConnDisconnects covers the post-overflow
// state: after the slow conn is torn down, broadcasts to remaining conns
// continue normally. Uses small payloads + a 1 ms pacing gap so the fast
// client's drain goroutine reliably keeps up even under race-detector
// slowdown.
func TestBroadcast_ContinuesAfterSlowConnDisconnects(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "post-overflow.sock")

	srv := ipc.NewServer(sockPath, func(*ipc.Conn, *ipc.Message) {}, nil)
	if err := srv.Start(); err != nil {
		t.Fatalf("server start: %v", err)
	}
	defer srv.Stop()

	fast, err := ipc.NewClient(sockPath)
	if err != nil {
		t.Fatalf("fast client: %v", err)
	}
	defer fast.Close()

	slow, err := ipc.NewClient(sockPath)
	if err != nil {
		t.Fatalf("slow client: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	var fastCount int
	var fastMu sync.Mutex
	go func() {
		for {
			if _, err := fast.Receive(); err != nil {
				return
			}
			fastMu.Lock()
			fastCount++
			fastMu.Unlock()
		}
	}()

	// Paced burst: enough to overflow the non-reading slow conn but slow
	// enough that the fast drain goroutine never falls behind.
	smallPayload := map[string]string{"kind": "tick"}
	for i := 0; i < 150; i++ {
		msg, _ := ipc.NewMessage(ipc.MsgStateUpdate, smallPayload)
		srv.Broadcast(msg)
		time.Sleep(time.Millisecond)
	}

	// Let the overflow tear down the slow conn.
	time.Sleep(200 * time.Millisecond)
	slow.Close()
	time.Sleep(100 * time.Millisecond)

	fastMu.Lock()
	pre := fastCount
	fastMu.Unlock()

	// Issue NEW broadcasts after the slow conn is torn down. Fast must still
	// see them — the absence of slow in the broadcast fan-out is the
	// post-overflow invariant we care about.
	for i := 0; i < 50; i++ {
		msg, _ := ipc.NewMessage(ipc.MsgStateUpdate, smallPayload)
		srv.Broadcast(msg)
		time.Sleep(time.Millisecond)
	}

	time.Sleep(300 * time.Millisecond)

	fastMu.Lock()
	post := fastCount
	fastMu.Unlock()

	if post-pre < 30 {
		t.Errorf("after slow conn disconnect, fast client only got %d new messages (want ≥ 30)", post-pre)
	}
}
