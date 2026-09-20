package daemon

import (
	"context"
	"log"
	"time"

	"github.com/google/uuid"

	"github.com/artyomsv/quil/internal/logger"

	"github.com/artyomsv/quil/internal/claudesessions"
	"github.com/artyomsv/quil/internal/plugin"
)

// Adoption and the cards. Parts C and E of the design.
//
// Adoption is what the daemon can do for an agent it is NOT going to restart:
// record which session it is in, so the pane resumes that conversation after a
// daemon restart. Nothing is killed and the running process is never contacted
// — it does not know Quil is watching. This is deliberately NOT called
// "attaching": Quil never attaches to a running agent process, and the hook
// events that a properly spawned pane gets (work state, notifications,
// permission prompts, input history) are not available here at any price.

const (
	// handStartAdoptWindow bounds how recently a transcript must have been
	// written to be the session the user just started. A file older than this
	// belongs to some earlier conversation in the same directory.
	handStartAdoptWindow = 30 * time.Second
	// handStartScanTimeout bounds the directory walk.
	handStartScanTimeout = 2 * time.Second
)

// handStartAdoptDelayVar is how long to wait before looking. claude writes its
// transcript once the session is under way, not at exec, so scanning
// immediately finds nothing. A var so a test need not sleep for it.
var handStartAdoptDelayVar = 3 * time.Second

// listSessionsFn is the seam tests replace instead of writing transcript trees.
var listSessionsFn = func(ctx context.Context, cwd string) ([]claudesessions.Session, error) {
	sessions, _, err := claudesessions.List(ctx, cwd)
	return sessions, err
}

// adoptClaudeSession records the session a hand-started claude is running in,
// so a daemon restart resumes that conversation rather than starting a fresh
// one.
//
// Claude-only, deliberately. Codex and opencode keep no directory-indexed
// session store that a third party can read — their records are files Quil
// itself writes from their hooks, and a hand-started one writes none. That
// asymmetry is real and is named rather than papered over: for those two, a
// hand-started agent gets the card and nothing else.
func (d *Daemon) adoptClaudeSession(pane *Pane, target *plugin.PanePlugin, m handStartMarker) {
	if target == nil || !target.UsesClaudeSessions() {
		return
	}
	cwd := d.resolveHandStartCWD(pane, m)
	if cwd == "" {
		return
	}
	started := time.Now()

	// Off the output goroutine: this sleeps and then walks a directory, and
	// that goroutine is on the path of every byte the pane produces.
	go func() {
		time.Sleep(handStartAdoptDelayVar)
		ctx, cancel := context.WithTimeout(context.Background(), handStartScanTimeout)
		defer cancel()
		sessions, err := listSessionsFn(ctx, cwd)
		if err != nil {
			logger.Debug("pane %s: adopt scan: %v", pane.ID, err)
			return
		}
		id, ok := newestSessionSince(sessions, started.Add(-handStartAdoptWindow))
		if !ok {
			logger.Debug("pane %s: no transcript appeared in %s to adopt", pane.ID, cwd)
			return
		}
		if !d.claimAdoptedSession(pane, id) {
			return
		}
		log.Printf("pane %s: adopted hand-started claude session %s", pane.ID, id)
		d.emitHandStartCard(pane, "agent_adopted", "info",
			"Tracking this session",
			"This pane will resume this conversation after a restart. Work-in-progress "+
				"indicators and notifications need a Claude Code pane.")
		d.broadcastState()
		d.requestSnapshot()
	}()
}

// claimAdoptedSession writes the session onto the pane, refusing if another
// pane already holds it.
//
// Two shells in the same directory scan the same project tree and would
// otherwise adopt each other's conversation. Refusing is an acceptable outcome;
// adopting the wrong session is not, because the next restart would then resume
// someone else's work into this pane.
func (d *Daemon) claimAdoptedSession(pane *Pane, id string) bool {
	if holder := d.paneHoldingSession(id, pane.ID); holder != "" {
		log.Printf("pane %s: not adopting %s — pane %s already holds it", pane.ID, id, holder)
		return false
	}
	pane.PluginMu.Lock()
	defer pane.PluginMu.Unlock()
	// The pane stays a terminal. Its type is what it runs, and it is running a
	// shell — the session id is a note about what is inside that shell.
	if pane.PluginState == nil {
		pane.PluginState = map[string]string{}
	}
	pane.PluginState["session_id"] = id
	pane.Adopted = true
	return true
}

// paneHoldingSession answers the id of another pane already carrying this
// session, or "".
func (d *Daemon) paneHoldingSession(sessionID, except string) string {
	for _, tab := range d.session.Tabs() {
		for _, p := range d.session.Panes(tab.ID) {
			if p.ID == except {
				continue
			}
			p.PluginMu.Lock()
			held := p.PluginState["session_id"]
			p.PluginMu.Unlock()
			if held == sessionID {
				return p.ID
			}
		}
	}
	return ""
}

// newestSessionSince picks the most recently written transcript, provided it
// was written after the launch. An older one is a previous conversation in the
// same directory, not the session that just started.
func newestSessionSince(sessions []claudesessions.Session, since time.Time) (string, bool) {
	best := ""
	var bestAt time.Time
	for _, s := range sessions {
		if s.ID == "" || s.Modified.Before(since) {
			continue
		}
		if best == "" || s.Modified.After(bestAt) {
			best, bestAt = s.ID, s.Modified
		}
	}
	return best, best != ""
}

// emitHandStartCard puts one line on the notification timeline.
//
// A refusal the user cannot see the reason for is indistinguishable from the
// feature being broken, which is the whole lesson of #221: the daemon knew
// perfectly well that those panes had no hook, and said nothing.
func (d *Daemon) emitHandStartCard(pane *Pane, kind, severity, title, message string) {
	d.emitEvent(PaneEvent{
		ID:        uuid.New().String(),
		PaneID:    pane.ID,
		TabID:     pane.TabID,
		PaneName:  pane.Name,
		Type:      kind,
		Title:     title,
		Message:   message,
		Severity:  severity,
		Timestamp: time.Now(),
	})
}

// returnToShellOnCleanExit puts the shell back when a converted pane's agent
// exits normally.
//
// Without it a user whose loop is shell -> claude -> /exit -> shell ends every
// cycle looking at a dead agent pane and reaching for Ctrl+N — a regression
// against the terminal pane they started with. A non-zero exit is left alone:
// a pane whose agent CRASHED must show that it did, and replacing the evidence
// with a fresh prompt would hide it.
//
// Answers whether it took the pane over, so the caller can skip the ordinary
// exit card for a pane that is already back at a prompt.
func (d *Daemon) returnToShellOnCleanExit(pane *Pane, code int) bool {
	if code != 0 {
		return false
	}
	pane.PluginMu.Lock()
	prev := pane.ConvertedFromTerminal
	if prev != "" {
		pane.Type = prev
		pane.ConvertedFromTerminal = ""
		pane.InstanceArgs = nil
		pane.Adopted = false
	}
	pane.PluginMu.Unlock()
	if prev == "" {
		return false
	}
	log.Printf("pane %s: agent exited cleanly, returning to %s", pane.ID, prev)
	if !d.restartPaneInPlace(pane) {
		return false
	}
	d.broadcastState()
	d.requestSnapshot()
	return true
}
