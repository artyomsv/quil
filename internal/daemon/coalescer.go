package daemon

import "time"

// coalesceMaxBytes caps a single coalesced flush. The 2 ms timer is a
// debounce: a PTY that streams without 2 ms gaps would otherwise grow the
// accumulator (and the resulting broadcast frame) without bound.
const coalesceMaxBytes = 64 * 1024

// runCoalescer batches chunks from dataCh and calls onFlush with batches of
// at most coalesceMaxBytes, flushing early when the cap is reached and
// otherwise 2 ms after the last chunk. onFirstChunk fires once, before the
// first batch (resize-kick hook). Returns after a final flush when dataCh
// closes.
//
// onFlush must not retain the slice after returning — the accumulator backing
// array is reused. All current callers (flushPaneOutput) are synchronous, so
// this holds; do not add async callees without copying the slice first.
func runCoalescer(dataCh <-chan []byte, onFirstChunk func(), onFlush func([]byte)) {
	const coalesceDelay = 2 * time.Millisecond
	var acc []byte

	flushTimer := time.NewTimer(0)
	if !flushTimer.Stop() {
		<-flushTimer.C
	}
	stopTimer := func() {
		if !flushTimer.Stop() {
			select {
			case <-flushTimer.C:
			default:
			}
		}
	}
	flush := func() {
		if len(acc) == 0 {
			return
		}
		onFlush(acc)
		acc = acc[:0]
	}

	first := true
	for {
		select {
		case chunk, ok := <-dataCh:
			if !ok {
				stopTimer()
				flush()
				return
			}
			if first {
				first = false
				onFirstChunk()
			}
			acc = append(acc, chunk...)
			if len(acc) >= coalesceMaxBytes {
				stopTimer()
				flush()
				continue
			}
			stopTimer()
			flushTimer.Reset(coalesceDelay)
		case <-flushTimer.C:
			flush()
		}
	}
}
