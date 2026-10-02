package daemon

import (
	"context"
	"log"
	"sync"
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
	inflight *sync.WaitGroup
}

// get returns a fresh-enough answer, probing at most once across concurrent
// callers.
func (c *sandboxCap) get(ctx context.Context) ipc.SandboxCapRespPayload {
	c.mu.Lock()
	if time.Since(c.fetched) < sandboxCapTTL && !c.fetched.IsZero() {
		answer := c.answer
		c.mu.Unlock()
		return answer
	}
	if wg := c.inflight; wg != nil {
		// Someone else is probing. Wait for their answer rather than
		// starting a second probe against the same engine.
		c.mu.Unlock()
		wg.Wait()
		c.mu.Lock()
		answer := c.answer
		c.mu.Unlock()
		return answer
	}
	wg := &sync.WaitGroup{}
	wg.Add(1)
	c.inflight = wg
	c.mu.Unlock()

	// The release runs in a defer, and that is not tidiness: this goroutine
	// holds the ONLY thing that can wake every waiter, so a panic anywhere in
	// the probe would leave inflight set and the WaitGroup at 1 — and every
	// later caller, including the spawn path, would block on it forever. A
	// daemon-wide deadlock behind one failed probe.
	var answer ipc.SandboxCapRespPayload
	defer func() {
		c.mu.Lock()
		c.answer = answer
		c.fetched = time.Now()
		c.inflight = nil
		c.mu.Unlock()
		wg.Done()
	}()
	answer = probeSandbox(ctx)
	return answer
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
func (d *Daemon) handleSandboxCapReq(conn *ipc.Conn, msg *ipc.Message, release func()) {
	id := msg.ID
	go func() {
		answer := d.sandboxCap.get(context.Background())
		if answer.Error != "" {
			log.Printf("sandbox: capability probe: %s", answer.Error)
		}
		release()
		respondTo(conn, id, ipc.MsgSandboxCapResp, answer)
	}()
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
