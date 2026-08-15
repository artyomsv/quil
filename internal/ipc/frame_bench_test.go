package ipc

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"testing"
)

// Frame encode/decode benchmarks.
//
// These exist because the IPC hot path was once 99% redundant work and nobody
// could see it: `EncodeFrame` marshalled a `Message` whose `Payload` was already
// encoded JSON, and encoding/json re-scanned that payload through its validator
// on every frame. At up to 500 frames/s/pane that was 5.9% of a core per busy
// pane on the daemon, and 207 µs per frame on the receiving client.
//
// That client cost is NOT on the Bubble Tea Update goroutine — ReadMessage runs
// on the router's per-destination pump (internal/tui/router.go:186) and
// DecodePayload on the listen tea.Cmd goroutine (internal/tui/model.go:5953).
// It bounds per-connection receive throughput (one pump drains a destination
// sequentially) and adds latency ahead of Update, rather than competing with
// keystroke handling.
//
// The component breakdown below is deliberately kept alongside the end-to-end
// numbers. The diagnosis was only possible because base64, payload marshal and
// envelope marshal could be timed separately — the envelope turned out to cost
// 7× the base64 encode that produced its contents. A future regression will be
// findable the same way.
//
// Run via `./scripts/dev.sh bench <label>`, which pins -cpu 1 and -count 6 so
// results are comparable across runs.

// benchPaneOutput builds a pane_output message with n bytes of payload, the
// shape flushPaneOutput produces.
func benchPaneOutput(tb testing.TB, n int) *Message {
	tb.Helper()
	data := make([]byte, n)
	for i := range data {
		data[i] = byte('a' + i%26)
	}
	msg, err := NewMessage(MsgPaneOutput, PaneOutputPayload{PaneID: "pane-1a2b3c4d", Data: data})
	if err != nil {
		tb.Fatalf("NewMessage: %v", err)
	}
	return msg
}

// benchListPanes stands in for the non-hot-path traffic: a small, structured
// payload rather than one large opaque blob, and carrying an ID (the
// request-response path). Guards against a fast path that helps pane_output and
// regresses everything else.
func benchListPanes(tb testing.TB) *Message {
	tb.Helper()
	msg, err := NewMessage(MsgListPanesResp, ListPanesRespPayload{
		Panes: []PaneInfo{
			{ID: "pane-1", TabID: "tab-1", TabName: "Shell", Type: "terminal", CWD: "/home/u/p", Running: true},
			{ID: "pane-2", TabID: "tab-2", TabName: "Build", Type: "claude-code", CWD: "/home/u/p", Running: true},
		},
	})
	if err != nil {
		tb.Fatalf("NewMessage: %v", err)
	}
	msg.ID = "req-7f3a1c2e"
	return msg
}

// --- encode ---

func BenchmarkEncodeFrame_PaneOutput8K(b *testing.B) {
	msg := benchPaneOutput(b, 8*1024)
	b.ReportAllocs()
	b.SetBytes(8 * 1024)
	for b.Loop() {
		if _, err := EncodeFrame(msg); err != nil {
			b.Fatal(err)
		}
	}
}

// 64 KB is coalesceMaxBytes — the largest frame a single flush can produce.
func BenchmarkEncodeFrame_PaneOutput64K(b *testing.B) {
	msg := benchPaneOutput(b, 64*1024)
	b.ReportAllocs()
	b.SetBytes(64 * 1024)
	for b.Loop() {
		if _, err := EncodeFrame(msg); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEncodeFrame_ListPanesResp(b *testing.B) {
	msg := benchListPanes(b)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := EncodeFrame(msg); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkClientReceive_ListPanesResp(b *testing.B) {
	msg := benchListPanes(b)
	frame, err := EncodeFrame(msg)
	if err != nil {
		b.Fatal(err)
	}
	r := bytes.NewReader(nil)
	b.ReportAllocs()
	for b.Loop() {
		r.Reset(frame)
		got, err := ReadMessage(r)
		if err != nil {
			b.Fatal(err)
		}
		var p ListPanesRespPayload
		if err := got.DecodePayload(&p); err != nil {
			b.Fatal(err)
		}
	}
}

// --- decode ---

// ReadMessage over an in-memory frame: the envelope half of what a client pays
// per received frame. Uses bytes.Reader so the measurement excludes syscalls.
func BenchmarkReadMessage_PaneOutput8K(b *testing.B) {
	msg := benchPaneOutput(b, 8*1024)
	frame, err := EncodeFrame(msg)
	if err != nil {
		b.Fatal(err)
	}
	r := bytes.NewReader(nil)
	b.ReportAllocs()
	b.SetBytes(8 * 1024)
	for b.Loop() {
		r.Reset(frame)
		if _, err := ReadMessage(r); err != nil {
			b.Fatal(err)
		}
	}
}

// The payload half.
func BenchmarkDecodePayload_PaneOutput8K(b *testing.B) {
	msg := benchPaneOutput(b, 8*1024)
	b.ReportAllocs()
	b.SetBytes(8 * 1024)
	for b.Loop() {
		var p PaneOutputPayload
		if err := msg.DecodePayload(&p); err != nil {
			b.Fatal(err)
		}
	}
}

// Both halves together — the number that matters, because it is the full cost a
// client pays to turn one received pane_output frame into usable bytes.
func BenchmarkClientReceive_PaneOutput8K(b *testing.B) {
	msg := benchPaneOutput(b, 8*1024)
	frame, err := EncodeFrame(msg)
	if err != nil {
		b.Fatal(err)
	}
	r := bytes.NewReader(nil)
	b.ReportAllocs()
	b.SetBytes(8 * 1024)
	for b.Loop() {
		r.Reset(frame)
		got, err := ReadMessage(r)
		if err != nil {
			b.Fatal(err)
		}
		var p PaneOutputPayload
		if err := got.DecodePayload(&p); err != nil {
			b.Fatal(err)
		}
		if len(p.Data) != 8*1024 {
			b.Fatalf("short payload: %d", len(p.Data))
		}
	}
}

// --- component breakdown ---
//
// Not redundant with the end-to-end numbers above: these are what make a
// regression diagnosable rather than merely visible.

// Cost of the base64 that PaneOutputPayload's []byte field implies.
func BenchmarkComponent_Base64Encode8K(b *testing.B) {
	data := make([]byte, 8*1024)
	out := make([]byte, base64.StdEncoding.EncodedLen(len(data)))
	b.ReportAllocs()
	b.SetBytes(8 * 1024)
	for b.Loop() {
		base64.StdEncoding.Encode(out, data)
	}
}

// Cost of marshalling the payload struct (includes the base64 above).
func BenchmarkComponent_MarshalPayload8K(b *testing.B) {
	data := make([]byte, 8*1024)
	p := PaneOutputPayload{PaneID: "pane-1a2b3c4d", Data: data}
	b.ReportAllocs()
	b.SetBytes(8 * 1024)
	for b.Loop() {
		if _, err := json.Marshal(p); err != nil {
			b.Fatal(err)
		}
	}
}

// Cost of wrapping an already-encoded payload in the Message envelope. This is
// the one that was 99% of encode time.
func BenchmarkComponent_MarshalEnvelope8K(b *testing.B) {
	msg := benchPaneOutput(b, 8*1024)
	b.ReportAllocs()
	b.SetBytes(8 * 1024)
	for b.Loop() {
		if _, err := json.Marshal(msg); err != nil {
			b.Fatal(err)
		}
	}
}
