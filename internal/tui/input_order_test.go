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

// TestPasteClipboard_ReadsOnCmdButEnqueuesOnUpdate pins the fourth and last
// MsgPaneInput producer to the Update goroutine.
//
// Reading the clipboard blocks, so it has to happen in a tea.Cmd — but the Cmd
// must NOT write to the pane. Doing so made paste a second producer racing the
// keystroke path into inputCh (a key typed while the clipboard read was still
// running could overtake the paste), and read pane/dest state off its owning
// goroutine. So the Cmd returns clipboardPastedMsg and enqueues nothing; Update
// turns that into the ordered enqueue.
//
// Deliberately NOT parallel: it swaps the package-level clipboard readers.
func TestPasteClipboard_ReadsOnCmdButEnqueuesOnUpdate(t *testing.T) {
	origText, origImg := clipboardReadText, clipboardReadImage
	t.Cleanup(func() { clipboardReadText, clipboardReadImage = origText, origImg })
	clipboardReadText = func() (string, error) { return "PASTED", nil }
	clipboardReadImage = func() ([]byte, error) { return nil, nil }

	m, fake := inputOrderTestModel(t, "p1", true)

	cmd := m.pasteClipboard()
	if cmd == nil {
		t.Fatal("pasteClipboard returned a nil cmd; want the clipboard-read command")
	}

	m.forwardInputBytes([]byte("a"))
	queued := len(m.inputCh)
	msg := cmd() // clipboard read only — must not touch the queue
	if got := len(m.inputCh); got != queued {
		t.Fatalf("the clipboard tea.Cmd enqueued %d entries itself; the enqueue must happen on the Update goroutine", got-queued)
	}
	pasted, ok := msg.(clipboardPastedMsg)
	if !ok {
		t.Fatalf("cmd returned %T, want clipboardPastedMsg", msg)
	}
	if pasted.text != "PASTED" {
		t.Errorf("clipboard text = %q, want %q", pasted.text, "PASTED")
	}

	// Update turns the message into the ordered enqueue, between the keys.
	updated, _ := m.Update(pasted)
	m2 := updated.(Model)
	m2.forwardInputBytes([]byte("b"))

	if got := drainQueued(t, m, 3); got != "aPASTEDb" {
		t.Errorf("enqueued order = %q, want %q", got, "aPASTEDb")
	}
	if len(fake.sent) != 0 {
		t.Errorf("paste must go through the queue, not straight to the client; got %d direct sends", len(fake.sent))
	}
}

// TestPasteClipboard_TargetsThePaneActiveWhenRequested pins the paste target to
// the pane the user was looking at when they asked to paste, not whichever is
// active by the time the clipboard read finishes. The read can be slow — the
// image path decodes a DIB, encodes a PNG and writes it to disk — which is
// ample time to switch panes, and clipboard contents are the payload you least
// want delivered somewhere you did not intend.
//
// Deliberately NOT parallel: it swaps the package-level clipboard readers.
func TestPasteClipboard_TargetsThePaneActiveWhenRequested(t *testing.T) {
	origText, origImg := clipboardReadText, clipboardReadImage
	t.Cleanup(func() { clipboardReadText, clipboardReadImage = origText, origImg })
	clipboardReadText = func() (string, error) { return "SECRET", nil }
	clipboardReadImage = func() ([]byte, error) { return nil, nil }

	// Two panes in one tab; "p1" is active when the paste is requested.
	p1 := NewPaneModel("p1", 1024)
	p2 := NewPaneModel("p2", 1024)
	tab := NewTabModel("tab-1", "t")
	tab.Root = &LayoutNode{
		Split: SplitHorizontal,
		Ratio: 0.5,
		Left:  NewLeaf(p1),
		Right: NewLeaf(p2),
	}
	tab.ActivePane = "p1"
	m := &Model{
		projects: oneProject(tab),
		client:   &fakeSender{},
		inputCh:  make(chan paneInput, inputForwardBuffer),
	}

	cmd := m.pasteClipboard()
	if cmd == nil {
		t.Fatal("pasteClipboard returned a nil cmd")
	}

	// The user switches panes while the clipboard read is in flight.
	tab.ActivePane = "p2"

	msg := cmd()
	pasted, ok := msg.(clipboardPastedMsg)
	if !ok {
		t.Fatalf("cmd returned %T, want clipboardPastedMsg", msg)
	}
	if pasted.paneID != "p1" {
		t.Errorf("bound paneID = %q, want p1 — the target must be captured when the paste is requested", pasted.paneID)
	}

	m.Update(pasted)

	select {
	case in := <-m.inputCh:
		if in.paneID != "p1" {
			t.Errorf("paste delivered to pane %q, want p1 — clipboard contents landed in the wrong pane", in.paneID)
		}
		if string(in.data) != "SECRET" {
			t.Errorf("pasted data = %q, want %q", string(in.data), "SECRET")
		}
	default:
		t.Fatal("paste was never enqueued")
	}
}

// TestInputForwarder_DrainsQueuedInputOnStop pins the shutdown contract.
// enqueueInput blocks rather than drops so accepted input is never lost — a
// forwarder that returned the moment inputDone closed would reintroduce exactly
// that loss at exit, and only sometimes, because a select with two ready cases
// picks pseudo-randomly.
func TestInputForwarder_DrainsQueuedInputOnStop(t *testing.T) {
	t.Parallel()
	const queued = 8
	sink := &syncSender{got: make(chan struct{}, queued)}
	m, _ := inputOrderTestModel(t, "p1", true)
	m.client = sink
	m.inputDone = make(chan struct{})

	// Fill the queue BEFORE the forwarder starts, so every entry is already
	// buffered when the stop signal arrives.
	for i := 0; i < queued; i++ {
		m.forwardInputBytes([]byte{byte('0' + i)})
	}
	m.StopInputForwarder()
	go m.inputForwarder()

	deadline := time.After(2 * time.Second)
	for i := 0; i < queued; i++ {
		select {
		case <-sink.got:
		case <-deadline:
			sink.mu.Lock()
			n := len(sink.data)
			sink.mu.Unlock()
			t.Fatalf("forwarder stopped with input still queued: delivered %d of %d", n, queued)
		}
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	got := ""
	for _, d := range sink.data {
		got += string(d)
	}
	if got != "01234567" {
		t.Errorf("drained order = %q, want %q", got, "01234567")
	}
}

// panicSender panics on its first Send and records every later one. It exists to
// prove the drainer survives a panic; see TestForwardOne_PanicKeepsDrainerAlive.
type panicSender struct {
	mu    sync.Mutex
	calls int
	data  [][]byte
	got   chan struct{}
}

func (p *panicSender) Send(m *ipc.Message) error {
	p.mu.Lock()
	p.calls++
	first := p.calls == 1
	if !first {
		var pl ipc.PaneInputPayload
		if err := json.Unmarshal(m.Payload, &pl); err == nil {
			p.data = append(p.data, pl.Data)
		}
	}
	p.mu.Unlock()
	if first {
		panic("send exploded")
	}
	p.got <- struct{}{}
	return nil
}

func (p *panicSender) Receive() (*ipc.Message, error) { return nil, nil }

// TestForwardOne_PanicKeepsDrainerAlive pins the hardening that makes the
// BLOCKING enqueue safe. enqueueInput parks rather than dropping when the queue
// fills, which is only acceptable because the drainer cannot die: if a panic
// killed the sole forwarder goroutine, the 1024 buffer would fill and every
// later keystroke would block the Update loop — a silent, total input freeze.
// So a panicking send must be swallowed per entry and the NEXT entry must still
// be delivered.
func TestForwardOne_PanicKeepsDrainerAlive(t *testing.T) {
	t.Parallel()
	sink := &panicSender{got: make(chan struct{}, 1)}
	m, _ := inputOrderTestModel(t, "p1", true)
	m.client = sink
	m.inputDone = make(chan struct{})

	go m.inputForwarder()
	t.Cleanup(m.StopInputForwarder)

	m.forwardInputBytes([]byte("a")) // panics inside Send
	m.forwardInputBytes([]byte("b")) // must still be delivered

	select {
	case <-sink.got:
	case <-time.After(2 * time.Second):
		t.Fatal("drainer died on the panicking entry — a full queue would now deadlock every enqueue")
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.data) != 1 || string(sink.data[0]) != "b" {
		t.Errorf("delivered after panic = %q, want [\"b\"]", sink.data)
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
