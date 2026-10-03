package daemon

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/sandbox"
)

// sandboxProbeFn is the seam the handler tests drive, so no test needs Docker.
var sandboxProbeFn = sandbox.Probe

// sandboxCapTTL is how long one capability answer is trusted.
//
// A cache is not an optimisation here, it is a correctness requirement in both
// directions. A daemon runs for weeks while Docker Desktop is started and
// stopped underneath it, so an answer cached forever would be wrong for most
// of that time; and the probe is a blocking external call, so answering every
// keystroke in the setup dialog with a fresh one would park a worker goroutine
// on a hung engine over and over.
const sandboxCapTTL = 30 * time.Second

// sandboxCap holds the last capability answer and the single-flight that
// guards the probe.
//
// A mutex rather than the atomic.Bool pattern the other dialog RPCs use,
// because this one caches: those handlers reject a concurrent request outright
// (a second directory listing has nothing useful to say), whereas a second
// capability request wants the SAME answer the first is fetching. Rejecting it
// would make the dialog's own two asks — one at attach, one at open — fail
// each other exactly when they overlap.
type sandboxCap struct {
	mu       sync.Mutex
	answer   ipc.SandboxCapRespPayload
	fetched  time.Time
	inflight chan struct{} // closed when the running probe has stored its answer
	// waiting counts callers parked on another caller's probe (a test reads it).
	waiting atomic.Int32
}

// errSandboxCapCanceled is the answer a waiter gets when its own ctx ends
// before the shared probe does. It is never cached.
const errSandboxCapCanceled = "sandbox check canceled"

// get returns a fresh-enough answer, probing at most once across concurrent
// callers.
//
// ctx bounds only THIS caller's wait, and every caller waits the same way —
// including the one whose request found no probe running and started it. A
// caller whose ctx ends (its conn closed, or its own deadline passed, like
// the plugin catalog's 2 s) returns at once with the canceled answer, so a
// client cannot park goroutines here by reconnecting, and a request never
// holds its conn's dispatch goroutine for longer than it asked to.
//
// The probe runs on its OWN goroutine with its own timeout (sandbox.Probe's),
// started from context.Background: it is shared, and a probe cut short by
// one caller would cache "docker not available" for every caller for
// sandboxCapTTL. At most one runs daemon-wide.
func (c *sandboxCap) get(ctx context.Context) ipc.SandboxCapRespPayload {
	c.mu.Lock()
	if time.Since(c.fetched) < sandboxCapTTL && !c.fetched.IsZero() {
		answer := c.answer
		c.mu.Unlock()
		return answer
	}
	done := c.inflight
	if done == nil {
		done = make(chan struct{})
		c.inflight = done
		go c.probe(done)
	}
	c.mu.Unlock()

	c.waiting.Add(1)
	defer c.waiting.Add(-1)
	select {
	case <-done:
	case <-ctx.Done():
		return ipc.SandboxCapRespPayload{Error: errSandboxCapCanceled}
	}
	c.mu.Lock()
	answer := c.answer
	c.mu.Unlock()
	return answer
}

// probe runs the one shared probe and wakes every waiter.
//
// The release runs in a defer, and that is not tidiness: this goroutine holds
// the ONLY thing that can wake every waiter, so a probe that returned early
// without it would leave inflight set and the channel open — and every later
// caller without a deadline, including the spawn path, would block on it
// forever.
func (c *sandboxCap) probe(done chan struct{}) {
	var answer ipc.SandboxCapRespPayload
	defer func() {
		c.mu.Lock()
		c.answer = answer
		c.fetched = time.Now()
		c.inflight = nil
		c.mu.Unlock()
		close(done)
	}()
	answer = probeSandbox(context.Background())
}

// probeSandbox asks the engine what it is and turns that into a wire answer.
//
// Available requires linux containers, not merely a reachable engine. Docker
// Desktop in Windows-containers mode answers `docker info` perfectly well and
// then fails every linux image at `run`, so gating on reachability alone would
// offer the user a pane that cannot start.
func probeSandbox(ctx context.Context) ipc.SandboxCapRespPayload {
	info, err := sandboxProbeFn(ctx)
	if err != nil {
		return ipc.SandboxCapRespPayload{Error: dockerUnavailableMessage(err)}
	}
	out := ipc.SandboxCapRespPayload{
		Available:     info.Usable(),
		ServerVersion: info.ServerVersion,
		OSType:        info.OSType,
		Arch:          info.Arch,
	}
	if !out.Available {
		out.Error = "docker is running " + info.OSType + " containers; sandbox panes need linux"
	}
	return out
}

// dockerUnavailableMessage keeps the daemon's own error text short and
// actionable. The underlying error is logged in full; what reaches the dialog
// is one line the user can act on.
func dockerUnavailableMessage(err error) string {
	const cap = 200
	msg := err.Error()
	if len(msg) > cap {
		msg = msg[:cap] + "…"
	}
	return "docker not available: " + msg
}

// handleSandboxCapReq answers "can THIS machine run a sandbox pane".
//
// The question is about the daemon, never the client: the container runs where
// the daemon runs, and the client may be a laptop attached to a remote host.
// The client files the answer against the destination that sent it for the
// same reason.
//
// On a worker goroutine like every other dialog RPC, because the probe blocks.
//
// release returns the conn's waiting-request slot (admitParked); it runs
// before the answer, so a client may ask again the moment it reads one. The
// worker keeps the request's ID only, never the message: a client-padded
// payload would otherwise stay in memory for as long as the probe waits.
//
// The wait ends with the conn: a worker whose client is gone answers nobody,
// and one that outlived it would let a client stack waiters by reconnecting,
// since a new conn starts with an empty slot count.
func (d *Daemon) handleSandboxCapReq(conn *ipc.Conn, msg *ipc.Message, release func()) {
	id := msg.ID
	go func() {
		ctx, cancel := connContext(conn)
		defer cancel()
		answer := d.sandboxCap.get(ctx)
		if answer.Error == errSandboxCapCanceled {
			release()
			return
		}
		if answer.Error != "" {
			log.Printf("sandbox: capability probe: %s", answer.Error)
		}
		release()
		respondTo(conn, id, ipc.MsgSandboxCapResp, answer)
	}()
}

// connContext is canceled when conn closes (or when cancel is called, which
// also ends the watcher goroutine). A nil conn — tests — never cancels.
func connContext(conn *ipc.Conn) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	if conn == nil {
		return ctx, cancel
	}
	go func() {
		select {
		case <-conn.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

// sandboxAvailable reports whether this daemon can start a sandbox pane right
// now, using the same cached answer the dialog sees.
//
// Spawn consults it so a create that arrives when the engine is down fails
// with a clear reason instead of a raw docker error in the pane — and, more
// importantly, never falls back to a host spawn.
func (d *Daemon) sandboxAvailable(ctx context.Context) (bool, string) {
	answer := d.sandboxCap.get(ctx)
	return answer.Available, answer.Error
}
