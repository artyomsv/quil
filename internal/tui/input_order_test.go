package tui

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// inputOrderTestModel builds a minimal Model with one project holding one tab
// holding one active pane. The caller decides whether to wire an inputCh.
func inputOrderTestModel(t *testing.T, paneID string, withChannel bool) (*Model, *fakeSender) {
	t.Helper()
	pane := NewPaneModel(paneID, 1024)
	pane.Active = true
	tab := NewTabModel("tab-1", "t")
	tab.Root = NewLeaf(pane)
	tab.ActivePane = pane.ID
	fake := &fakeSender{}
	m := &Model{
		projects: oneProject(tab),
		client:   fake,
	}
	if withChannel {
		m.inputCh = make(chan paneInput, inputForwardBuffer)
	}
	return m, fake
}

// drainQueued pops n entries the test expects to already be buffered and
// returns them concatenated. It never blocks: an empty channel is the failure
// this whole file exists to catch (a send deferred to a racing goroutine), so
// it must fail loudly rather than wait for one to catch up.
func drainQueued(t *testing.T, m *Model, n int) string {
	t.Helper()
	got := ""
	for i := 0; i < n; i++ {
		select {
		case in := <-m.inputCh:
			got += string(in.data)
		default:
			t.Fatalf("entry %d: input channel empty — input was not enqueued synchronously", i)
		}
	}
	return got
}

// syncSender is a thread-safe tuiClient for tests that exercise the
// inputForwarder goroutine: it records payloads under a mutex and signals each
// send on a channel so the test can wait deterministically instead of sleeping.
type syncSender struct {
	mu   sync.Mutex
	data [][]byte
	got  chan struct{}
}

func (s *syncSender) Send(m *ipc.Message) error {
	var p ipc.PaneInputPayload
	if err := json.Unmarshal(m.Payload, &p); err == nil {
		s.mu.Lock()
		s.data = append(s.data, p.Data)
		s.mu.Unlock()
	}
	if s.got != nil {
		s.got <- struct{}{}
	}
	return nil
}

func (s *syncSender) Receive() (*ipc.Message, error) { return nil, nil }

// TestForwardInputBytes_EnqueuesSynchronouslyInOrder is the regression guard for
// the keystroke-transposition bug: forwardInputBytes must fix wire order on the
// (single-threaded) Update goroutine, NOT defer the send into a racing tea.Cmd.
// We assert it returns a nil cmd (no follow-up send) and that the bytes land on
// inputCh in typed order.
func TestForwardInputBytes_EnqueuesSynchronouslyInOrder(t *testing.T) {
	t.Parallel()
	m, _ := inputOrderTestModel(t, "p1", true)

	want := []string{"h", "e", "l", "l", "o"}
	for _, s := range want {
		if cmd := m.forwardInputBytes([]byte(s)); cmd != nil {
			t.Fatalf("forwardInputBytes(%q) must enqueue synchronously and return nil cmd; "+
				"a non-nil cmd means the send is deferred to a racing goroutine", s)
		}
	}

	for i, s := range want {
		select {
		case in := <-m.inputCh:
			if in.paneID != "p1" {
				t.Errorf("entry %d: paneID = %q, want p1", i, in.paneID)
			}
			if string(in.data) != s {
				t.Errorf("entry %d: data = %q, want %q", i, string(in.data), s)
			}
		default:
			t.Fatalf("entry %d (%q): input channel empty — keystroke was not enqueued synchronously", i, s)
		}
	}
}

// TestEnqueueInput_ResolvesDestOnTheCallingGoroutine pins the multi-daemon half
// of the fix. destOfPane walks m.projects, which the Update goroutine rebuilds
// on every workspace broadcast, so the owning daemon must be resolved at enqueue
// time and ride the queue entry. Resolving it on the forwarder goroutine would
// be a data race, and a stale answer would type into another machine's pane.
func TestEnqueueInput_ResolvesDestOnTheCallingGoroutine(t *testing.T) {
	t.Parallel()
	m, _ := inputOrderTestModel(t, "p1", true)
	m.projects[0].Dest = "gpu01"

	m.enqueueInput("p1", []byte("x"))

	select {
	case in := <-m.inputCh:
		if in.dest != "gpu01" {
			t.Errorf("queued dest = %q, want %q — dest must be resolved before queueing", in.dest, "gpu01")
		}
	default:
		t.Fatal("nothing enqueued")
	}
}

// TestSendPaneInput_StampsTheResolvedDest checks the other end of that contract:
// the forwarder stamps the dest it was handed, so the frame reaches the daemon
// that owns the pane rather than whichever project happens to be active.
func TestSendPaneInput_StampsTheResolvedDest(t *testing.T) {
	t.Parallel()
	m, fake := inputOrderTestModel(t, "p1", false)

	m.sendPaneInput("gpu01", "p1", []byte("x"))

	if len(fake.sent) != 1 {
		t.Fatalf("want 1 send, got %d", len(fake.sent))
	}
	if got := fake.sent[0].Origin; got != "gpu01" {
		t.Errorf("Origin = %q, want %q", got, "gpu01")
	}
}

// TestForwardInputBytes_NilChannelFallsBackToOrderedSend covers the literal-Model
// path (unit tests, pre-Init): with no inputCh, forwardInputBytes sends directly
// and synchronously, still in typed order.
func TestForwardInputBytes_NilChannelFallsBackToOrderedSend(t *testing.T) {
	t.Parallel()
	m, fake := inputOrderTestModel(t, "p1", false)

	for _, s := range []string{"a", "b", "c"} {
		if cmd := m.forwardInputBytes([]byte(s)); cmd != nil {
			t.Fatalf("forwardInputBytes(%q) must send synchronously and return nil cmd", s)
		}
	}

	if len(fake.sent) != 3 {
		t.Fatalf("want 3 synchronous sends, got %d", len(fake.sent))
	}
	got := ""
	for i, msg := range fake.sent {
		if msg.Type != ipc.MsgPaneInput {
			t.Fatalf("sent[%d].Type = %q, want %q", i, msg.Type, ipc.MsgPaneInput)
		}
		got += string(paneInputData(t, msg))
	}
	if got != "abc" {
		t.Errorf("reassembled input = %q, want %q", got, "abc")
	}
}

// TestInputForwarder_DrainsToClientInTypedOrder exercises the production drain
// path end-to-end: the goroutine reads inputCh and forwards to client.Send in
// FIFO order. A small buffer forces the forwarder to drain mid-flight (it cannot
// rely on the whole burst fitting in the buffer), so a second draining goroutine
// or any reordering would surface here.
func TestInputForwarder_DrainsToClientInTypedOrder(t *testing.T) {
	t.Parallel()
	want := []string{"q", "u", "i", "l", "!"}
	sink := &syncSender{got: make(chan struct{}, len(want))}
	pane := NewPaneModel("p1", 1024)
	pane.Active = true
	tab := NewTabModel("tab-1", "t")
	tab.Root = NewLeaf(pane)
	tab.ActivePane = pane.ID
	m := &Model{
		projects:  oneProject(tab),
		client:    sink,
		inputCh:   make(chan paneInput, 4), // smaller than len(want): forces mid-flight drain
		inputDone: make(chan struct{}),
	}
	go m.inputForwarder()
	t.Cleanup(m.StopInputForwarder)

	for _, s := range want {
		if cmd := m.forwardInputBytes([]byte(s)); cmd != nil {
			t.Fatalf("forwardInputBytes(%q) returned non-nil cmd", s)
		}
	}

	deadline := time.After(2 * time.Second)
	for range want {
		select {
		case <-sink.got:
		case <-deadline:
			t.Fatalf("timed out waiting for inputForwarder to drain")
		}
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.data) != len(want) {
		t.Fatalf("forwarded %d entries, want %d", len(sink.data), len(want))
	}
	got := ""
	for _, d := range sink.data {
		got += string(d)
	}
	if got != "quil!" {
		t.Errorf("forwarded order = %q, want %q", got, "quil!")
	}
}

// TestHandleKey_PlainRunes_EnqueueInTypedOrder drives the real keystroke entry
// point (handleKey default branch → keyToBytes → forwardInputBytes), not just
// forwardInputBytes in isolation, and asserts ordered enqueue.
func TestHandleKey_PlainRunes_EnqueueInTypedOrder(t *testing.T) {
	t.Parallel()
	m, _ := inputOrderTestModel(t, "p1", true)

	for _, r := range []string{"a", "b", "c"} {
		m.handleKey(tea.KeyPressMsg{Text: r})
	}

	if got := drainQueued(t, m, 3); got != "abc" {
		t.Errorf("enqueued order = %q, want %q", got, "abc")
	}
}

// TestSendInputToPane_SharesTheKeystrokeQueue pins the wheel-forwarding path to
// the same ordered queue. A mouse-tracking app reads wheel escapes off the same
// PTY stdin as typed keys, so a direct send here could overtake keystrokes still
// waiting in the queue — the same transposition bug, wearing a different hat.
func TestSendInputToPane_SharesTheKeystrokeQueue(t *testing.T) {
	t.Parallel()
	m, fake := inputOrderTestModel(t, "p1", true)

	m.forwardInputBytes([]byte("a"))
	m.sendInputToPane("p1", []byte("\x1b[<64;1;1M")) // one wheel-up notch
	m.forwardInputBytes([]byte("b"))

	if got := drainQueued(t, m, 3); got != "a\x1b[<64;1;1Mb" {
		t.Errorf("enqueued order = %q, want keystroke/wheel/keystroke interleave preserved", got)
	}
	if len(fake.sent) != 0 {
		t.Errorf("wheel input must go through the queue, not straight to the client; got %d direct sends", len(fake.sent))
	}
}

// TestSendClipboardToPane_SharesTheKeystrokeQueue pins the paste path to the
// queue for the same reason: paste is PTY input like any other, and a paste that
// jumps ahead of queued keystrokes lands mid-word.
func TestSendClipboardToPane_SharesTheKeystrokeQueue(t *testing.T) {
	t.Parallel()
	m, fake := inputOrderTestModel(t, "p1", true)

	m.forwardInputBytes([]byte("a"))
	m.sendClipboardToPane("PASTED")
	m.forwardInputBytes([]byte("b"))

	if got := drainQueued(t, m, 3); got != "aPASTEDb" {
		t.Errorf("enqueued order = %q, want %q", got, "aPASTEDb")
	}
	if len(fake.sent) != 0 {
		t.Errorf("paste must go through the queue, not straight to the client; got %d direct sends", len(fake.sent))
	}
}

// TestForwardInputBytes_EmptyData_NoEnqueue: zero-length input is dropped at the
// entry point — no enqueue, no zero-length frame.
func TestForwardInputBytes_EmptyData_NoEnqueue(t *testing.T) {
	t.Parallel()
	m, fake := inputOrderTestModel(t, "p1", true)

	for _, data := range [][]byte{{}, nil} {
		if cmd := m.forwardInputBytes(data); cmd != nil {
			t.Fatalf("forwardInputBytes(empty) must return nil cmd")
		}
	}
	select {
	case in := <-m.inputCh:
		t.Fatalf("empty data must not enqueue; got %q", string(in.data))
	default:
	}
	if len(fake.sent) != 0 {
		t.Errorf("empty data must not send; got %d sends", len(fake.sent))
	}
}

// TestForwardInputBytes_NilTabOrPane_NoEnqueue pins the early-return guards: with
// no active tab or no active pane, forwardInputBytes silently does nothing.
func TestForwardInputBytes_NilTabOrPane_NoEnqueue(t *testing.T) {
	t.Parallel()

	t.Run("nil tab", func(t *testing.T) {
		t.Parallel()
		m := &Model{client: &fakeSender{}, inputCh: make(chan paneInput, 4)}
		if cmd := m.forwardInputBytes([]byte("a")); cmd != nil {
			t.Fatalf("want nil cmd")
		}
		select {
		case <-m.inputCh:
			t.Fatal("nil tab must not enqueue")
		default:
		}
	})

	t.Run("nil active pane", func(t *testing.T) {
		t.Parallel()
		tab := NewTabModel("tab-1", "t") // no Root / ActivePane set
		m := &Model{
			projects: oneProject(tab),
			client:   &fakeSender{},
			inputCh:  make(chan paneInput, 4),
		}
		if cmd := m.forwardInputBytes([]byte("a")); cmd != nil {
			t.Fatalf("want nil cmd")
		}
		select {
		case <-m.inputCh:
			t.Fatal("nil active pane must not enqueue")
		default:
		}
	})
}
