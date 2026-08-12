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

	for _, id := range expired {
		log.Printf("overlay evict: %s idle for %s", id, timeout)
		d.destroyOverlay(id)
	}
	return expired
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
		log.Printf("overlay evict: %s (cap %d reached, least recently shown)", live[i].id, max)
		d.destroyOverlay(live[i].id)
		evicted = append(evicted, live[i].id)
	}
	return evicted
}

// destroyOverlay removes one overlay through the ordinary pane-destroy path so
// it emits the same broadcast, snapshot and artifact cleanup as any other
// destroy — the TUI then reconciles it with machinery that already exists.
func (d *Daemon) destroyOverlay(paneID string) {
	if err := d.session.DestroyPane(paneID); err != nil {
		log.Printf("overlay evict %s: %v", paneID, err)
		return
	}
	d.cleanupPaneArtifacts(paneID)
	d.broadcastState()
	d.requestSnapshot()
}
