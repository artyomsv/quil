package ringbuf

import "sync"

// RingBuffer is a thread-safe circular byte buffer that keeps the most
// recent data when capacity is exceeded. Used to buffer PTY output for
// replay on TUI reconnect.
//
// The backing array is allocated once at construction and never reallocated:
// steady-state Write is zero-allocation. (The previous implementation
// append-reallocated and fully recompacted on every write once full — ~768 KB
// of memcpy per coalesced flush per busy pane.)
type RingBuffer struct {
	buf   []byte // fixed backing array, len == capacity
	start int    // index of the oldest byte
	size  int    // bytes currently stored
	gen   uint64 // bumped on every mutation; snapshot change detection
	mu    sync.Mutex
}

// NewRingBuffer creates a ring buffer with the given byte capacity.
func NewRingBuffer(capacity int) *RingBuffer {
	if capacity <= 0 {
		capacity = 1
	}
	return &RingBuffer{buf: make([]byte, capacity)}
}

// Write appends p, trimming the oldest bytes when capacity is exceeded.
func (rb *RingBuffer) Write(p []byte) {
	if len(p) == 0 {
		return
	}
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.gen++

	c := len(rb.buf)
	if len(p) >= c {
		copy(rb.buf, p[len(p)-c:])
		rb.start, rb.size = 0, c
		return
	}
	end := (rb.start + rb.size) % c
	n := copy(rb.buf[end:], p)
	if n < len(p) {
		copy(rb.buf, p[n:])
	}
	rb.size += len(p)
	if rb.size > c {
		rb.start = (rb.start + rb.size - c) % c
		rb.size = c
	}
}

// Bytes returns a copy of all buffered data.
func (rb *RingBuffer) Bytes() []byte {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.tailLocked(rb.size)
}

// Tail returns a copy of the last n bytes (all bytes when n >= Len).
// Replaces the "Bytes() then keep 4 KB" pattern that copied the full
// buffer to read an excerpt.
func (rb *RingBuffer) Tail(n int) []byte {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	if n > rb.size {
		n = rb.size
	}
	return rb.tailLocked(n)
}

// tailLocked copies the last n (<= rb.size) bytes. Caller holds rb.mu.
// The first copy source rb.buf[first:] may cover more than n bytes when the
// region is contiguous — copy stops at len(out), so both the contiguous and
// wrapped cases are handled by the m < n second copy.
func (rb *RingBuffer) tailLocked(n int) []byte {
	if n <= 0 {
		return nil
	}
	out := make([]byte, n)
	c := len(rb.buf)
	first := (rb.start + rb.size - n) % c
	m := copy(out, rb.buf[first:])
	if m < n {
		copy(out[m:], rb.buf)
	}
	return out
}

// Len returns the current number of bytes in the buffer.
func (rb *RingBuffer) Len() int {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.size
}

// Gen returns the mutation generation, monotonically increasing across
// Write/Reset. Equal generations guarantee identical contents.
func (rb *RingBuffer) Gen() uint64 {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.gen
}

// Reset clears all buffered data (the backing array is retained).
func (rb *RingBuffer) Reset() {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.start, rb.size = 0, 0
	rb.gen++
}
