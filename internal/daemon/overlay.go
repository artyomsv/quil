package daemon

import (
	"log"
	"sort"
	"sync"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

// overlayPolicyState holds the live retention settings. It is mutable at
// runtime because F1 → Settings pushes changes (MsgOverlayPolicy) and those
// must apply without a daemon restart.
type overlayPolicyState struct {
	mu     sync.RWMutex
	policy ipc.OverlayPolicyPayload
}

func (s *overlayPolicyState) get() ipc.OverlayPolicyPayload {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.policy
}

func (s *overlayPolicyState) set(p ipc.OverlayPolicyPayload) {
	s.mu.Lock()
	s.policy = p
	s.mu.Unlock()
}

func (d *Daemon) overlayPolicy() ipc.OverlayPolicyPayload { return d.overlayPolicyState.get() }

func (d *Daemon) setOverlayPolicy(p ipc.OverlayPolicyPayload) {
	d.overlayPolicyState.set(p)
	log.Printf("overlay policy: idle_timeout=%dm max_live=%d", p.IdleTimeoutMinutes, p.MaxLive)
}

// sweepIdleOverlays destroys every overlay hidden for longer than the policy
// allows, returning the ids it evicted.
//
// A VISIBLE overlay is never evicted however long it idles: lazygit writes
// nothing while the user reads it, so "no output" and "not on screen" are
// indistinguishable from the daemon's side — which is exactly why visibility is
// reported by the client rather than inferred here.
//
// The snapshot-then-destroy shape is the lock discipline this package
// documents: collecting under the read lock and destroying after it means
// DestroyPane (which closes a PTY via releasePanes) never runs under sm.mu.
func (d *Daemon) sweepIdleOverlays(now time.Time) []string {
	timeout := time.Duration(d.overlayPolicy().IdleTimeoutMinutes) * time.Minute
	if timeout <= 0 {
		return nil
	}

	var expired []string
	for _, p := range d.session.AllPanes() {
		p.PluginMu.Lock()
		isOverlay, hiddenAt := p.Overlay, p.OverlayHiddenAt
		p.PluginMu.Unlock()
		if !isOverlay || hiddenAt.IsZero() {
			continue
		}
		if now.Sub(hiddenAt) >= timeout {
			expired = append(expired, p.ID)
		}
	}

	var evicted []string
	for _, id := range expired {
		if !d.destroyOverlay(id) {
			continue
		}
		log.Printf("overlay evict: %s idle for %s", id, timeout)
		evicted = append(evicted, id)
	}
	d.publishOverlayEvictions(evicted)
	return evicted
}

// enforceOverlayCap evicts least-recently-shown overlays until at most
// MaxLive remain live, skipping exclude (the overlay currently being
// admitted, which must never evict itself). Returns the ids it evicted.
//
// MaxLive means "at most N live", not "fewer than N" — sitting exactly at the
// cap with no admission in progress evicts nothing.
//
// Enforced at CREATION (from createPaneAt) rather than in the idle sweep so
// opening one past the cap is instant and deterministic instead of
// "eventually". The cap is global rather than per tab: it exists to bound
// total process count.
//
// Eviction order is LEAST RECENTLY SHOWN (OverlayShownAt), not oldest-created
// — a FIFO policy would evict the overlay the user opens constantly simply
// because they opened it first.
func (d *Daemon) enforceOverlayCap(exclude string) []string {
	max := d.overlayPolicy().MaxLive
	if max <= 0 {
		return nil
	}

	type entry struct {
		id    string
		shown time.Time
	}
	var live []entry
	for _, p := range d.session.AllPanes() {
		if p.ID == exclude {
			continue
		}
		p.PluginMu.Lock()
		isOverlay, shown := p.Overlay, p.OverlayShownAt
		p.PluginMu.Unlock()
		if isOverlay {
			live = append(live, entry{id: p.ID, shown: shown})
		}
	}

	// A pane being ADMITTED occupies the slot the caller is about to fill, so
	// every other overlay is capped at max-1. With no admission the cap is
	// simply max.
	keep := max
	if exclude != "" {
		keep = max - 1
	}
	if len(live) <= keep {
		return nil
	}

	// Least recently shown first. A zero OverlayShownAt sorts oldest, which is
	// right: it has never been shown at all.
	sort.Slice(live, func(i, j int) bool { return live[i].shown.Before(live[j].shown) })

	var evicted []string
	for i := 0; i < len(live)-keep; i++ {
		if !d.destroyOverlay(live[i].id) {
			continue
		}
		log.Printf("overlay evict: %s (cap %d reached, least recently shown)", live[i].id, max)
		evicted = append(evicted, live[i].id)
	}
	d.publishOverlayEvictions(evicted)
	return evicted
}

// setOverlayClaim records (or withdraws) ONE client's assertion that it has an
// overlay on screen.
//
// Only an ATTACHED conn can hold a claim. A conn that never sent MsgAttach is
// not a client — an MCP bridge holds one for its whole lifetime — so letting it
// register a durable claim would let a socket client pin an overlay forever
// while never displaying anything. Its report still moves the timestamps (that
// is unchanged partial-update behaviour); it simply does not survive as a claim.
func (d *Daemon) setOverlayClaim(conn *ipc.Conn, paneID string, visible bool) {
	if conn == nil {
		return
	}
	d.attachedMu.Lock()
	defer d.attachedMu.Unlock()
	claims, ok := d.attachedConns[conn]
	if !ok {
		return
	}
	if visible {
		claims[paneID] = true
		return
	}
	delete(claims, paneID)
}

// overlayClaimed reports whether ANY attached client currently has this overlay
// on screen. The attached set holds one entry per client (one, occasionally
// two), so the walk is trivially cheap.
func (d *Daemon) overlayClaimed(paneID string) bool {
	d.attachedMu.Lock()
	defer d.attachedMu.Unlock()
	for _, claims := range d.attachedConns {
		if claims[paneID] {
			return true
		}
	}
	return false
}

// forgetOverlayClaimsFor drops every client's claim on one pane. Called from
// cleanupPaneArtifacts, the choke point every pane-destruction path already
// owes, so a destroyed overlay cannot leave its id in a live client's claim set
// for the life of a daemon that runs for weeks.
func (d *Daemon) forgetOverlayClaimsFor(paneID string) {
	d.attachedMu.Lock()
	defer d.attachedMu.Unlock()
	for _, claims := range d.attachedConns {
		delete(claims, paneID)
	}
}

// applyOverlayVisibility folds one client's report into the pane's retention
// timestamps, returning nothing because the timestamps are never on the wire.
//
// A hidden report from a client that is not the one displaying the overlay
// changes nothing: with the claim withdrawn, the pane is hidden only if no
// OTHER client still has it on screen. That is the whole of the per-client
// model — two TUIs used to mean the second one's tab switch started a
// five-minute countdown on the first one's visible lazygit.
//
// The claim is resolved BEFORE PluginMu is taken. attachedMu must never nest
// inside a pane lock.
func (d *Daemon) applyOverlayVisibility(conn *ipc.Conn, pane *Pane, visible bool) {
	d.setOverlayClaim(conn, pane.ID, visible)
	claimed := d.overlayClaimed(pane.ID)

	pane.PluginMu.Lock()
	defer pane.PluginMu.Unlock()
	switch {
	case visible:
		pane.OverlayHiddenAt = time.Time{}
		pane.OverlayShownAt = time.Now()
	case claimed:
		// Someone else still has it on screen.
	case pane.OverlayHiddenAt.IsZero():
		// Only the FIRST hide stamps: a client re-sending hidden must not keep
		// pushing the eviction deadline out.
		pane.OverlayHiddenAt = time.Now()
	}
}

// hideUnclaimedOverlays stamps every overlay that no client has on screen and
// that has no hidden timestamp yet, returning how many it stamped.
//
// Run on every client disconnect. This is what makes the idle timer run down
// while the user is away — the case the whole feature exists for, since a TUI
// exit used to leave every overlay running indefinitely — and it needs no
// last-client special case: a departed client's claims went with it, so "no
// clients attached" is just the strongest form of "nothing claims this".
//
// It also covers an overlay no client ever reported on (one created straight
// over the socket, say): unclaimed means no TUI has it on screen, because a
// TUI that does have one reports it.
//
// An overlay that is ALREADY hidden keeps its original timestamp; re-stamping
// would push the deadline out on every disconnect.
func (d *Daemon) hideUnclaimedOverlays(now time.Time) int {
	var n int
	for _, p := range d.session.AllPanes() {
		p.PluginMu.Lock()
		isOverlay, unstamped := p.Overlay, p.OverlayHiddenAt.IsZero()
		p.PluginMu.Unlock()
		if !isOverlay || !unstamped || d.overlayClaimed(p.ID) {
			continue
		}
		p.PluginMu.Lock()
		if p.OverlayHiddenAt.IsZero() {
			p.OverlayHiddenAt = now
			n++
		}
		p.PluginMu.Unlock()
	}
	return n
}

// destroyOverlay removes one overlay through the ordinary pane-destroy path so
// it gets the same artifact cleanup as any other destroy — the TUI then
// reconciles it with machinery that already exists. Reports whether the pane
// was actually there to destroy.
//
// It does NOT broadcast. An eviction PASS emits exactly one broadcast for
// however many overlays it reclaimed (publishOverlayEvictions): a broadcast per
// pane put N back-to-back full workspace-state frames onto a 64-slot
// must-deliver queue, which is the 2026-08-09 force-disconnect shape, and every
// one of them drives a full applyWorkspaceState reconciliation on every
// attached client.
func (d *Daemon) destroyOverlay(paneID string) bool {
	if err := d.session.DestroyPane(paneID); err != nil {
		log.Printf("overlay evict %s: %v", paneID, err)
		return false
	}
	d.cleanupPaneArtifacts(paneID)
	return true
}

// publishOverlayEvictions announces one eviction pass. Silent when the pass
// reclaimed nothing — the sweep runs at 1 Hz for the life of the daemon and
// almost always evicts nothing at all.
func (d *Daemon) publishOverlayEvictions(evicted []string) {
	if len(evicted) == 0 {
		return
	}
	d.broadcastState()
	d.requestSnapshot()
}
