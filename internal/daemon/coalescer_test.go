package daemon

import (
	"bytes"
	"testing"
	"time"
)

// TestRunCoalescer_CapsBatchSize: the coalescer is a debounce — without a
// size cap, chunks arriving faster than the 2 ms timer grow the accumulator
// without bound. Every flushed batch must respect coalesceMaxBytes and no
// byte may be lost or reordered.
func TestRunCoalescer_CapsBatchSize(t *testing.T) {
	const chunks, chunkLen = 200, 1024
	dataCh := make(chan []byte, chunks)

	// Build the expected concatenation while filling the channel.
	var want []byte
	for i := 0; i < chunks; i++ {
		b := bytes.Repeat([]byte{byte('a' + i%26)}, chunkLen)
		dataCh <- b
		want = append(want, b...)
	}
	close(dataCh)

	var got []byte
	var batches int
	runCoalescer(dataCh, func() {}, func(b []byte) {
		if len(b) > coalesceMaxBytes {
			t.Errorf("batch %d bytes exceeds cap %d", len(b), coalesceMaxBytes)
		}
		got = append(got, b...)
		batches++
	})

	if len(got) != chunks*chunkLen {
		t.Errorf("delivered %d bytes, want %d", len(got), chunks*chunkLen)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("byte content mismatch: order not preserved")
	}
	if batches < 2 {
		t.Errorf("expected multiple capped batches for %d KiB, got %d", chunks, batches)
	}
}

// TestRunCoalescer_FiresOnFirstChunk verifies the resize-kick hook runs
// exactly once, on the first chunk.
func TestRunCoalescer_FiresOnFirstChunk(t *testing.T) {
	dataCh := make(chan []byte, 2)
	dataCh <- []byte("a")
	dataCh <- []byte("b")
	close(dataCh)
	first := 0
	runCoalescer(dataCh, func() { first++ }, func([]byte) {})
	if first != 1 {
		t.Errorf("onFirstChunk fired %d times, want 1", first)
	}
}

// TestRunCoalescer_DebounceFlush exercises the timer path — the primary
// interactive flow where output stops and the 2 ms debounce fires.
func TestRunCoalescer_DebounceFlush(t *testing.T) {
	dataCh := make(chan []byte) // unbuffered, open — forces the timer path
	var got []byte
	done := make(chan struct{})
	go func() {
		runCoalescer(dataCh, func() {}, func(b []byte) {
			got = append(got, b...)
		})
		close(done)
	}()
	dataCh <- []byte("hello")
	time.Sleep(5 * time.Millisecond) // let the 2 ms debounce fire
	close(dataCh)
	<-done
	if string(got) != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}
