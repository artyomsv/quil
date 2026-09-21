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

// handStartAdoptDelayVar is how long to wait before the FIRST look. claude
// writes its transcript once the session is under way, not at exec, so scanning
// immediately finds nothing. A var so a test need not sleep for it.
var handStartAdoptDelayVar = 3 * time.Second

// handStartAdoptSchedule is the wait before each attempt. Backing off rather
// than polling: the thing being waited for is a person composing a first
// message, and the cost of looking is a directory walk.
func handStartAdoptSchedule() []time.Duration {
	d := handStartAdoptDelayVar
	return []time.Duration{d, 2 * d, 4 * d, 8 * d}
}

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
	// The retries outlive the thing they are waiting for. Bind to the PTY run
	// this marker came from: if the intercepted process exits, or the pane is
	// restarted, a later scan would otherwise claim whatever session appeared
	// next in that directory — another terminal's conversation, adopted onto
	// this pane, and denied to the pane that actually owns it.
	pane.PluginMu.Lock()
	gen := pane.ptyGen
	pane.PluginMu.Unlock()

	// Both seams are READ ONCE, here, rather than inside the goroutine.
	//
	// They are package-level test seams, and this goroutine outlives the call
	// by design — up to the whole backoff. A test that swaps one and restores
	// it in t.Cleanup would then be writing it while a goroutine from an
	// earlier test is still reading, which is a data race that the race
	// detector is entitled to fail on and which no amount of local passing
	// disproves. Capturing them also makes the retry loop use one scan
	// function throughout rather than whichever is installed at each wake.
	scan := listSessionsFn
	schedule := handStartAdoptSchedule()

	// Off the output goroutine: this sleeps and then walks a directory, and
	// that goroutine is on the path of every byte the pane produces.
	go func() {
		// Claude writes its transcript once the session is under way, not at
		// exec, and "under way" is the user's typing speed. A single look after
		// three seconds missed every launch where the first message took
		// longer to compose — and missed it SILENTLY, leaving the pane neither
		// tracked nor told.
		var id string
		var ok bool
		for _, wait := range schedule {
			select {
			case <-d.shutdown:
				return
			case <-time.After(wait):
			}
			pane.PluginMu.Lock()
			live := pane.ptyGen == gen && pane.PTY != nil
			pane.PluginMu.Unlock()
			if !live {
				logger.Debug("pane %s: abandoning adoption, the run it was for is gone", pane.ID)
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), handStartScanTimeout)
			sessions, err := scan(ctx, cwd)
			cancel()
			if err != nil {
				logger.Debug("pane %s: adopt scan: %v", pane.ID, err)
				continue
			}
			if id, ok = newestSessionSince(sessions, started.Add(-handStartAdoptWindow)); ok {
				break
			}
		}
		if !ok {
			log.Printf("pane %s: no claude transcript appeared in %s — session not recorded", pane.ID, cwd)
			d.emitHandStartCard(pane, "agent_untracked", "info",
				"Session not tracked",
				untrackedMessage("claude", "no transcript appeared to identify the session"))
			return
		}
		if !d.claimAdoptedSession(pane, id) {
			return
		}
		log.Printf("pane %s: adopted hand-started claude session %s", pane.ID, id)
		// The card says what adoption ACTUALLY buys, which is less than the
		// first version claimed. The pane stays a terminal, and a terminal's
		// persistence strategy is cwd_only — restore spawns a shell and never
		// consults the recorded id. Promising a resume it cannot deliver is
		// worse than promising nothing: it is the #221 failure with a
		// reassuring label on it.
		d.emitHandStartCard(pane, "agent_adopted", "info",
			"Session recorded, not tracked",
			"Quil noted which session this is, but a terminal pane cannot resume "+
				"it after a restart, and work indicators and notifications need "+
				"the hooks only a Claude Code pane gets. Ctrl+N opens one.")
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
	// The test and the write are ONE step, under the same daemon-wide lock the
	// create path uses (applyResumeSessionID). Two same-directory adoption
	// goroutines scan the same project tree and would otherwise both find the
	// session free — neither has written yet, so neither is visible to the
	// other — and both would record it.
	d.resumeClaimMu.Lock()
	defer d.resumeClaimMu.Unlock()

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
		// The conversation is OVER — the user typed /exit. Leaving its id
		// behind made the next bare `claude` in this pane look like a pane
		// that already has a session, and the preassign path reopened the
		// conversation the user had just closed.
		delete(pane.PluginState, "session_id")
		delete(pane.PluginState, "resume_session_id")
		delete(pane.PluginState, "transcript_path")
	}
	pane.PluginMu.Unlock()
	if prev != "" {
		// The records are the other half of that state and outlive the pane
		// object; a stale one is read back by the next spawn's ownsRecord.
		retirePaneSessionRecords(pane.ID)
	}
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
