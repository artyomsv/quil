package webgw

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

// DaemonConn is one browser tab's connection to the daemon (*ipc.Client
// satisfies it).
type DaemonConn interface {
	Send(*ipc.Message) error
	Receive() (*ipc.Message, error)
	Flush(time.Duration) bool
	Close() error
}

// pageConn is the WebSocket side, narrowed for tests. Close may block (a
// WebSocket close waits for the peer), so the bridge always calls it on its
// own goroutine, never on the one reading the daemon.
type pageConn interface {
	WriteText(ctx context.Context, b []byte) error
	WriteBinary(ctx context.Context, b []byte) error
	Close(code int, reason string)
}

// Close codes a page can see.
const (
	CloseResync            = 4001
	CloseTooSlow           = 4002
	CloseDaemonUnavailable = 4003
	CloseTokenRefused      = 4004
	CloseVersionMismatch   = 4005
	CloseByAgent           = 4006
	CloseGoingAway         = 1001
	// closeProtocolError is the WebSocket policy-violation code: the page
	// broke the protocol (a first message that is not its hello). It shows a
	// banner and does not reconnect, since a retry would do the same.
	closeProtocolError = 1008
	// maxLoggedType bounds a page-chosen message type written to the log.
	maxLoggedType = 64
)

type bridgeLimits struct {
	ControlMax     int
	LiveUnackedMax int64
	ReplayTabMax   int64
	WriteTimeout   time.Duration
	ResyncsPerMin  int
	// Version is the gateway build's version; the forward gate writes it into
	// every hello the page sends.
	Version string
}

func defaultBridgeLimits(version string) bridgeLimits {
	return bridgeLimits{
		ControlMax: 512, LiveUnackedMax: 2 << 20, ReplayTabMax: 64 << 20,
		WriteTimeout: 10 * time.Second, ResyncsPerMin: 3, Version: version,
	}
}

const detachFlushTimeout = time.Second

// replayBudget is shared by every tab of one gateway: it caps the buffered
// replay in total.
type replayBudget struct {
	mu   sync.Mutex
	used int64
	max  int64
}

func newReplayBudget(max int64) *replayBudget { return &replayBudget{max: max} }

func (r *replayBudget) take(n int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.used+n > r.max {
		return false
	}
	r.used += n
	return true
}

func (r *replayBudget) give(n int64) {
	r.mu.Lock()
	r.used -= n
	if r.used < 0 {
		r.used = 0
	}
	r.mu.Unlock()
}

// queued is one frame waiting for the page, in daemon receive order.
type queued struct {
	binary bool
	b      []byte
	output int64 // terminal bytes this frame carries (acked later); 0 for JSON
	ghost  bool
}

// sentOutput remembers, in order, what each acknowledged byte belongs to.
type sentOutput struct {
	n     int64
	ghost bool
}

// bridge is one browser tab: its daemon connection, the page it is writing
// to, and the accounting that keeps a slow page from holding unbounded data.
//
// One ordered queue carries both JSON and binary frames, so a size
// announcement can never be overtaken by the repaint that follows it. The
// daemon connection is read continuously and never paused: a paused reader
// lets the daemon's own must-deliver queue for this connection fill, and the
// daemon then closes it.
type bridge struct {
	d      DaemonConn
	lim    bridgeLimits
	budget *replayBudget
	now    func() time.Time
	logf   func(string, ...any)

	gateMu sync.Mutex
	gate   *forwardGate

	mu        sync.Mutex
	cond      *sync.Cond
	page      pageConn
	pageGen   uint64 // bumps on every attach/detach; frames queued for an older page are dropped
	queue     []queued
	controls  int
	inFlight  []sentOutput // written to the page, not yet acknowledged
	liveOut   int64        // live bytes queued or unacknowledged
	replayTab int64        // replay bytes queued or unacknowledged (this tab)

	replayPanes map[string]bool // panes with replay buffered for this page
	resyncs     []time.Time
	closed      bool
	// attachID is the message ID of the last attach forwarded to the daemon,
	// so a refusal answering it can be told from any other.
	attachID string
	// refused is the reason of an id-less refusal from the daemon, which it
	// sends just before it closes the connection (the token was revoked or
	// expired). The close that follows is then 4004, not 4003.
	refused string

	// onResync, set before run starts, is called after a resync detaches the
	// page and before the page is told, so the owner can start waiting for the
	// page to come back before the page can possibly reconnect.
	onResync func()
	// onIDInUse, set before run starts, is called on the daemon reader when
	// the daemon refuses the page's attach (its client id is held by another
	// principal), after the refusal was queued for the page.
	onIDInUse func()
}

func newBridge(d DaemonConn, lim bridgeLimits, budget *replayBudget, now func() time.Time, logf func(string, ...any)) *bridge {
	b := &bridge{d: d, lim: lim, budget: budget, now: now, logf: logf, gate: newForwardGate("", lim.Version)}
	b.cond = sync.NewCond(&b.mu)
	return b
}

// setLease names the client id this tab may use and starts a fresh hello
// requirement: a page that re-attaches after a resync begins with a new hello.
func (b *bridge) setLease(id string) {
	b.gateMu.Lock()
	b.gate = newForwardGate(id, b.lim.Version)
	b.gateMu.Unlock()
}

// relabel moves the tab to a new client id after the daemon refused the old
// one as in use. The page's hello was already checked, so the gate keeps it;
// only the id the page's hello and attach must name changes.
func (b *bridge) relabel(id string) {
	b.gateMu.Lock()
	b.gate.leasedID = id
	b.gateMu.Unlock()
}

// attachPage starts writing to p and returns the generation that identifies
// this page: fromPage drops frames carrying any other. Anything queued for a
// previous page is discarded and the counters restart at zero; a writer still
// parked for the previous page exits. A closed bridge refuses, and the caller
// closes p.
func (b *bridge) attachPage(p pageConn) (uint64, error) { return b.attachPageFirst(p, nil) }

// attachPageFirst is attachPage with one JSON frame queued ahead of anything
// the daemon sends, so the page's welcome is always its first frame.
func (b *bridge) attachPageFirst(p pageConn, first []byte) (uint64, error) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return 0, errClosed
	}
	b.resetLocked()
	if first != nil {
		b.controls++
		b.queue = append(b.queue, queued{b: first})
	}
	b.page = p
	b.pageGen++
	gen := b.pageGen
	b.cond.Broadcast()
	b.mu.Unlock()
	go b.writeLoop(p, gen)
	return gen, nil
}

func (b *bridge) resetLocked() {
	b.budget.give(b.replayTab)
	b.queue, b.inFlight = nil, nil
	b.controls, b.liveOut, b.replayTab = 0, 0, 0
	b.replayPanes = nil
}

// detachPage closes the page. For a resync the daemon connection stays open
// so the page can re-attach on it and keep its place; any other code is a
// full close.
func (b *bridge) detachPage(code int, reason string) {
	if code != CloseResync {
		b.close(code, reason)
		return
	}
	b.mu.Lock()
	p := b.page
	b.page = nil
	b.pageGen++
	b.resetLocked()
	b.cond.Broadcast()
	b.mu.Unlock()
	if b.onResync != nil {
		b.onResync()
	}
	b.logf("resynced (%d)", code)
	if p != nil {
		go p.Close(code, reason)
	}
}

// closePage closes the bridge when gen is still the attached page: the page
// went away by itself. A page that was already replaced, resynced away or
// closed leaves the bridge alone.
func (b *bridge) closePage(gen uint64, code int, reason string) {
	if p := b.shut(func() bool { return b.page != nil && b.pageGen == gen }, code, reason); p != nil {
		go p.Close(code, reason)
	}
}

// close sends detach and flushes it before closing the daemon connection —
// a queued frame is discarded by a close — so a held master slot is released
// at once rather than after the daemon's grace time. Then the page closes, on
// its own goroutine: close runs on the daemon reader too, which must not wait
// for a page.
func (b *bridge) close(code int, reason string) {
	if p := b.shut(nil, code, reason); p != nil {
		go p.Close(code, reason)
	}
}

// closeWait is close that returns only once the page's close has finished,
// for the shutdown path, which must not let the process exit first.
func (b *bridge) closeWait(code int, reason string) {
	if p := b.shut(nil, code, reason); p != nil {
		p.Close(code, reason)
	}
}

// shut detaches, flushes and closes the daemon side when cond, evaluated
// under the bridge's lock, holds (nil means always), and returns the page
// that was attached for the caller to close. The check and the close are one
// critical section, so a resync cannot slip between them.
func (b *bridge) shut(cond func() bool, code int, reason string) pageConn {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	if cond != nil && !cond() {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	p := b.page
	b.page = nil
	b.pageGen++
	b.resetLocked()
	b.cond.Broadcast()
	b.mu.Unlock()
	if detach, err := ipc.NewMessage(ipc.MsgDetach, struct{}{}); err == nil {
		_ = b.d.Send(detach)
		b.d.Flush(detachFlushTimeout)
	}
	_ = b.d.Close()
	b.logf("closed (%d %s)", code, reason)
	return p
}

func (b *bridge) resyncsInLastMinute() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.recentResyncsLocked()
}

func (b *bridge) recentResyncsLocked() int {
	cut := b.now().Add(-time.Minute)
	keep := b.resyncs[:0]
	for _, t := range b.resyncs {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	b.resyncs = keep
	return len(keep)
}

// resync drops the page with 4001, or with 4002 when this tab has already
// resynced too often within a minute.
func (b *bridge) resync() {
	b.mu.Lock()
	tooMany := b.recentResyncsLocked() >= b.lim.ResyncsPerMin
	if !tooMany {
		b.resyncs = append(b.resyncs, b.now())
	}
	b.mu.Unlock()
	if tooMany {
		b.logf("tab resynced %d times within a minute: closing as too slow", b.lim.ResyncsPerMin)
		b.close(CloseTooSlow, "too slow")
		return
	}
	b.detachPage(CloseResync, "resync")
}

// run reads the daemon connection until it fails. It never stops reading:
// with no page attached (between a resync and the re-attach) frames are
// dropped here instead.
func (b *bridge) run(ctx context.Context) {
	for {
		m, err := b.d.Receive()
		if err != nil {
			b.mu.Lock()
			closed, refused := b.closed, b.refused
			b.mu.Unlock()
			switch {
			case closed:
			case refused != "":
				b.close(CloseTokenRefused, reasonText(refused, CloseTokenRefused))
			default:
				b.close(CloseDaemonUnavailable, "daemon unavailable")
			}
			return
		}
		if ctx.Err() != nil {
			return
		}
		b.fromDaemon(m)
	}
}

func (b *bridge) fromDaemon(m *ipc.Message) {
	switch m.Type {
	case ipc.MsgCloseTUI:
		b.close(CloseByAgent, "closed by an agent")
		return
	case ipc.MsgPaneOutput:
		var p ipc.PaneOutputPayload
		if err := m.DecodePayload(&p); err != nil {
			return
		}
		frame, err := EncodePaneOutput(p)
		if err != nil {
			return
		}
		b.enqueueOutput(p.PaneID, frame, int64(len(p.Data)), p.Ghost)
		return
	}
	renew := m.Type == ipc.MsgError && b.noteRefusal(m)
	raw, err := json.Marshal(m)
	if err != nil {
		return
	}
	b.enqueueControl(raw)
	if renew && b.onIDInUse != nil {
		b.onIDInUse()
	}
}

// noteRefusal reads an error from the daemon for what it means to the
// bridge. An id-less refusal is the last frame before the daemon closes the
// connection: it is remembered for the close code. A refusal answering the
// page's attach means another principal holds the client id; noteRefusal
// reports true and the owner mints a new one.
func (b *bridge) noteRefusal(m *ipc.Message) bool {
	var p ipc.ErrorPayload
	if m.DecodePayload(&p) != nil || p.Code != ipc.ErrCodeRefused {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if m.ID == "" {
		b.refused = p.Message
		if b.refused == "" {
			b.refused = closeReason(CloseTokenRefused)
		}
		return false
	}
	if m.ID != b.attachID {
		return false
	}
	b.attachID = ""
	return true
}

// enqueueControl queues one JSON frame for the page, or closes the tab as too
// slow when the control queue is full. With no page attached it drops.
func (b *bridge) enqueueControl(raw []byte) {
	b.mu.Lock()
	if b.page == nil {
		b.mu.Unlock()
		return
	}
	if b.controls >= b.lim.ControlMax {
		b.mu.Unlock()
		b.logf("control queue over %d: closing as too slow", b.lim.ControlMax)
		b.close(CloseTooSlow, "too slow")
		return
	}
	b.controls++
	b.queue = append(b.queue, queued{b: raw})
	b.cond.Broadcast()
	b.mu.Unlock()
}

func (b *bridge) enqueueOutput(pane string, frame []byte, n int64, ghost bool) {
	b.mu.Lock()
	if b.page == nil {
		b.mu.Unlock()
		return
	}
	if ghost {
		if b.replayTab+n > b.lim.ReplayTabMax || !b.budget.take(n) {
			total, panes := b.replayTab+n, len(b.replayPanes)
			b.mu.Unlock()
			b.logf("replay over the buffer cap (%d bytes across %d panes): closing as too slow", total, panes)
			b.close(CloseTooSlow, "too slow")
			return
		}
		b.replayTab += n
		if b.replayPanes == nil {
			b.replayPanes = map[string]bool{}
		}
		b.replayPanes[pane] = true
	} else {
		if b.liveOut+n > b.lim.LiveUnackedMax {
			b.mu.Unlock()
			b.resync()
			return
		}
		b.liveOut += n
	}
	b.queue = append(b.queue, queued{binary: true, b: frame, output: n, ghost: ghost})
	b.cond.Broadcast()
	b.mu.Unlock()
}

// writeLoop writes queued frames to one page until that page is replaced or
// closed. A write over the timeout closes the tab as too slow.
func (b *bridge) writeLoop(p pageConn, gen uint64) {
	for {
		b.mu.Lock()
		for len(b.queue) == 0 && b.pageGen == gen && !b.closed {
			b.cond.Wait()
		}
		if b.pageGen != gen || b.closed {
			b.mu.Unlock()
			return
		}
		q := b.queue[0]
		b.queue = b.queue[1:]
		if !q.binary {
			b.controls--
		} else {
			b.inFlight = append(b.inFlight, sentOutput{n: q.output, ghost: q.ghost})
		}
		b.mu.Unlock()

		ctx, cancel := context.WithTimeout(context.Background(), b.lim.WriteTimeout)
		var err error
		if q.binary {
			err = p.WriteBinary(ctx, q.b)
		} else {
			err = p.WriteText(ctx, q.b)
		}
		cancel()
		if err != nil {
			b.mu.Lock()
			stale := b.pageGen != gen
			b.mu.Unlock()
			if !stale {
				b.logf("write to the page failed: %v", err)
				b.close(CloseTooSlow, "too slow")
			}
			return
		}
	}
}

// fromPage handles one text frame from the page that attachPage returned gen
// for: an acknowledgement, or a message for the daemon after the forward gate.
// A frame from any other page generation (a socket that was resynced away) is
// dropped. Acknowledgements are handled here, before the gate, because they
// are gateway-local and not forwardable.
func (b *bridge) fromPage(gen uint64, raw []byte) error {
	b.mu.Lock()
	closed, stale := b.closed, b.pageGen != gen
	b.mu.Unlock()
	if closed {
		return errClosed
	}
	if stale {
		return errStalePage
	}
	var m ipc.Message
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	if m.Type == MsgWebAck {
		var a WebAckPayload
		if err := json.Unmarshal(m.Payload, &a); err == nil && a.Bytes > 0 {
			b.ack(gen, a.Bytes)
		}
		return nil
	}
	b.gateMu.Lock()
	fwd, refuse, fatal := b.gate.check(&m)
	b.gateMu.Unlock()
	if fatal != nil {
		b.close(closeProtocolError, "protocol error")
		return fatal
	}
	if refuse != nil {
		b.logf("refused %s from the page", printable(m.Type, maxLoggedType))
		if out, err := json.Marshal(refuse); err == nil {
			b.enqueueControl(out)
		}
		return nil
	}
	if fwd.Type == ipc.MsgAttach && fwd.ID != "" {
		b.mu.Lock()
		b.attachID = fwd.ID
		b.mu.Unlock()
	}
	return b.d.Send(fwd)
}

// ack credits acknowledged terminal bytes in the order they were written. An
// ack from a stale page generation is ignored, and so is any part of an ack
// beyond what was written.
func (b *bridge) ack(gen uint64, n int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pageGen != gen {
		return
	}
	for n > 0 && len(b.inFlight) > 0 {
		head := &b.inFlight[0]
		take := head.n
		if take > n {
			take = n
		}
		head.n -= take
		n -= take
		if head.ghost {
			b.replayTab -= take
			b.budget.give(take)
		} else {
			b.liveOut -= take
		}
		if head.n == 0 {
			b.inFlight = b.inFlight[1:]
		}
	}
}

var (
	errClosed    = errors.New("bridge closed")
	errStalePage = errors.New("frame from a page that is no longer attached")
)
