package ringbuf

import (
	"bytes"
	"testing"
)

func TestRingBufferBasicWrite(t *testing.T) {
	rb := NewRingBuffer(100)
	rb.Write([]byte("hello"))
	if got := rb.Bytes(); !bytes.Equal(got, []byte("hello")) {
		t.Fatalf("expected %q, got %q", "hello", got)
	}
	if rb.Len() != 5 {
		t.Fatalf("expected len 5, got %d", rb.Len())
	}
}

func TestRingBufferOverflow(t *testing.T) {
	rb := NewRingBuffer(10)
	rb.Write([]byte("12345"))
	rb.Write([]byte("67890"))
	// Buffer is exactly at capacity
	if got := rb.Bytes(); !bytes.Equal(got, []byte("1234567890")) {
		t.Fatalf("expected %q, got %q", "1234567890", got)
	}

	// One more byte pushes oldest out
	rb.Write([]byte("X"))
	if got := rb.Bytes(); !bytes.Equal(got, []byte("234567890X")) {
		t.Fatalf("expected %q, got %q", "234567890X", got)
	}
}

func TestRingBufferLargeWrite(t *testing.T) {
	rb := NewRingBuffer(5)
	rb.Write([]byte("abcdefghij")) // 10 bytes, cap is 5
	if got := rb.Bytes(); !bytes.Equal(got, []byte("fghij")) {
		t.Fatalf("expected %q, got %q", "fghij", got)
	}
	if rb.Len() != 5 {
		t.Fatalf("expected len 5, got %d", rb.Len())
	}
}

func TestRingBufferMultipleWrites(t *testing.T) {
	rb := NewRingBuffer(8)
	rb.Write([]byte("aaa"))
	rb.Write([]byte("bbb"))
	rb.Write([]byte("ccc"))
	// Total 9 bytes, cap 8 -> oldest byte trimmed
	if got := rb.Bytes(); !bytes.Equal(got, []byte("aabbbccc")) {
		t.Fatalf("expected %q, got %q", "aabbbccc", got)
	}
}

func TestRingBufferReset(t *testing.T) {
	rb := NewRingBuffer(100)
	rb.Write([]byte("data"))
	rb.Reset()
	if rb.Len() != 0 {
		t.Fatalf("expected len 0 after reset, got %d", rb.Len())
	}
	if got := rb.Bytes(); got != nil {
		t.Fatalf("expected nil after reset, got %q", got)
	}
}

func TestRingBufferBytesReturnsCopy(t *testing.T) {
	rb := NewRingBuffer(100)
	rb.Write([]byte("original"))
	got := rb.Bytes()
	// Mutate the returned slice
	got[0] = 'X'
	// Buffer should be unchanged
	if buf := rb.Bytes(); buf[0] != 'o' {
		t.Fatalf("Bytes() did not return a copy; buffer was mutated")
	}
}

func TestRingBufferEmptyWrite(t *testing.T) {
	rb := NewRingBuffer(10)
	rb.Write(nil)
	rb.Write([]byte{})
	if rb.Len() != 0 {
		t.Fatalf("expected len 0 after empty writes, got %d", rb.Len())
	}
}

func TestRingBuffer_TailReturnsLastN(t *testing.T) {
	rb := NewRingBuffer(8)
	rb.Write([]byte("abcdefghij")) // wraps: buffer holds "cdefghij"

	tests := []struct {
		n    int
		want string
	}{
		{4, "ghij"},
		{8, "cdefghij"},
		{20, "cdefghij"}, // n > Len → everything
		{0, ""},
	}
	for _, tt := range tests {
		if got := string(rb.Tail(tt.n)); got != tt.want {
			t.Errorf("Tail(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

func TestRingBuffer_GenIncrementsOnMutation(t *testing.T) {
	rb := NewRingBuffer(8)
	g0 := rb.Gen()
	rb.Write([]byte("ab"))
	if rb.Gen() == g0 {
		t.Error("Gen unchanged after Write")
	}
	g1 := rb.Gen()
	rb.Reset()
	if rb.Gen() == g1 {
		t.Error("Gen unchanged after Reset")
	}
	g2 := rb.Gen()
	if rb.Gen() != g2 {
		t.Error("Gen changed without mutation")
	}
}

// TestRingBuffer_WriteSteadyStateZeroAllocs pins the entire point of the
// rewrite: the old implementation reallocated + copied the full buffer on
// every write once full.
func TestRingBuffer_WriteSteadyStateZeroAllocs(t *testing.T) {
	rb := NewRingBuffer(4096)
	rb.Write(make([]byte, 4096)) // reach steady state (full)
	chunk := make([]byte, 512)
	allocs := testing.AllocsPerRun(100, func() {
		rb.Write(chunk)
	})
	if allocs != 0 {
		t.Errorf("steady-state Write allocates %.1f times per call, want 0", allocs)
	}
}

func TestRingBuffer_WrapContentMatchesNaive(t *testing.T) {
	rb := NewRingBuffer(100)
	var naive []byte
	for i := 0; i < 50; i++ {
		chunk := bytes.Repeat([]byte{byte('a' + i%26)}, 7+i%13)
		rb.Write(chunk)
		naive = append(naive, chunk...)
		if len(naive) > 100 {
			naive = naive[len(naive)-100:]
		}
		if !bytes.Equal(rb.Bytes(), naive) {
			t.Fatalf("iteration %d: ring %q != naive %q", i, rb.Bytes(), naive)
		}
	}
}
