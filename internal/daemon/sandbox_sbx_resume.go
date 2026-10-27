package daemon

import (
	"context"
	"fmt"
	"log"

	"github.com/google/uuid"

	"github.com/artyomsv/quil/internal/plugin"
	"github.com/artyomsv/quil/internal/sbx"
)

// The claude session of an sbx pane.
//
// A host claude pane learns its session from the SessionStart hook, and a
// docker sandbox pane from the same hook run inside its container. An sbx
// pane has neither: the cage's mounts belong to the user's environment file
// and Quil puts nothing of its own inside. Without a session id every start
// was a fresh conversation, and a restart — Alt+R, a daemon restart, a
// restored workspace — lost the one the user was in, although its transcript
// was still in the VM.
//
// So Quil keeps the session id itself, on the host, in the pane's
// PluginState (persisted in workspace.json): minted at the first start and
// handed to claude as `--session-id`, exactly the preassign_id strategy a
// host pane uses. Every later start asks the VM whether that id has a
// transcript (sbx.Client.HasTranscript) and resumes it when it does. The
// probe is what makes the choice safe: claude refuses `--resume` for an id
// it never persisted (a restart on the trust screen) and `--session-id` for
// one it did (exit 129), so neither argument can be the default.
//
// What this cannot see: a /clear or /resume inside the agent rotates the
// session, and with no hook the pane keeps the id it started with. A
// restart then reopens the conversation from before the rotation. The hook
// integration that fixes this for host panes is the open item named in
// docs/sandbox-panes.md.

// sbxSessionArgs returns the arguments that give or give back the pane its
// claude session, or nil for a plugin that does not use claude sessions.
// args is the argument list the agent WILL run with (the pane's instance
// arguments, else the plugin's own); workdir and env are the folder and
// environment of that exec, which the probe shares. It runs on the
// preparation goroutine — the probe is a VM round trip — and never under
// PluginMu.
//
// A fresh pane gets a new id and the plugin's start arguments, with no
// probe: an id minted this instant cannot have a transcript. A pane that
// already has one asks the VM; a transcript there means `--resume`, none
// means the same id to `--session-id` again. A probe failure is an error,
// not a fresh start: guessing would either refuse the start (129) or
// silently abandon the conversation, and the exec that follows would most
// likely fail the same way.
func (d *Daemon) sbxSessionArgs(ctx context.Context, pane *Pane, p *plugin.PanePlugin, c sbx.Client, name, workdir string, env, args []string) ([]string, error) {
	if !p.UsesClaudeSessions() || p.Persistence.Strategy != "preassign_id" {
		return nil, nil
	}
	// The EFFECTIVE arguments decide, not only the instance ones: a plugin
	// whose own args carry `--resume`/`--continue` would otherwise get a
	// second session selector, which claude refuses (PR #272 review, M-2).
	if instanceArgsNameSession(handStartBase(p.Command.Cmd), args) {
		return nil, nil
	}
	pane.PluginMu.Lock()
	if pane.PluginState == nil {
		pane.PluginState = make(map[string]string)
	}
	id := pane.PluginState["session_id"]
	resumeID := pane.PluginState["resume_session_id"]
	pane.PluginMu.Unlock()

	// A persisted id Quil did not write — a hand-edited snapshot — is
	// dropped, never handed to the VM.
	if id != "" && !sbx.ValidSessionID(id) {
		log.Printf("sbx: pane %s: ignoring a malformed session id (len=%d)", pane.ID, len(id))
		id = ""
	}
	// With hooks (sandbox_sbx_hooks.go) the VM's claude reports the session
	// it is in, and a /clear or /resume inside it rotated that id away from
	// the one recorded here. The hook record wins — gated, as on the host, on
	// the pane having had a session: a record under a brand-new pane's id is
	// a leftover of a destroyed pane that drew the same id.
	if id != "" {
		if rec, err := readHookSessionFn(pane.ID); err == nil && sbx.ValidSessionID(rec.ID) && rec.ID != id {
			log.Printf("sbx: pane %s: the hook recorded session %q (was %q); following it", pane.ID, rec.ID, id)
			id = rec.ID
		}
	}
	if id == "" && resumeID != "" && sbx.ValidSessionID(resumeID) {
		// Created to resume a session: that id is the pane's session from
		// the first instant, as on the host. Whether the VM holds its
		// transcript is the probe's question, like any other restart.
		id = resumeID
	}
	if id == "" {
		return d.sbxFreshSession(pane, p, "first start"), nil
	}

	found, err := c.HasTranscript(ctx, name, workdir, env, id)
	if err != nil {
		return nil, fmt.Errorf("could not check the pane's claude session in sandbox %s: %w", name, err)
	}
	if !found {
		log.Printf("sbx: pane %s: session %q has no transcript in sandbox %s; starting it under that id", pane.ID, id, name)
		sbxRecordSession(pane, id)
		return plugin.ExpandResumeArgs(p.Persistence.StartArgs, map[string]string{"session_id": id}), nil
	}
	// The occupancy guard every resume goes through: two panes must never
	// run one conversation. The claim records the id on the pane.
	chosen, holder, ok := d.claimResumeSession(pane, []resumeCandidate{{id: id, source: "sbx", state: candidateLocated}})
	if !ok {
		return d.sbxFreshSession(pane, p, "session "+id+" is open in pane "+holder), nil
	}
	log.Printf("sbx: pane %s: resuming session %q in sandbox %s", pane.ID, chosen.id, name)
	return []string{"--resume", chosen.id}, nil
}

// sbxFreshSession gives the pane a new session id and returns the plugin's
// start arguments for it. The old id, if any, is replaced on the pane: a pane
// must not advertise a session it is not in (see freshClaudeSession).
func (d *Daemon) sbxFreshSession(pane *Pane, p *plugin.PanePlugin, why string) []string {
	id := uuid.New().String()
	sbxRecordSession(pane, id)
	log.Printf("sbx: pane %s: starting a fresh session %q — %s", pane.ID, id, why)
	return plugin.ExpandResumeArgs(p.Persistence.StartArgs, map[string]string{"session_id": id})
}

// sbxRecordSession writes the pane's session id. No transcript path: the
// transcript is inside the VM, where no host path names it, and a stale one
// would vouch for a file nobody checked.
func sbxRecordSession(pane *Pane, id string) {
	pane.PluginMu.Lock()
	defer pane.PluginMu.Unlock()
	if pane.PluginState == nil {
		pane.PluginState = make(map[string]string)
	}
	pane.PluginState["session_id"] = id
	delete(pane.PluginState, "transcript_path")
}

// sbxSpawnStillWanted re-asks, after the session probe, the questions
// respawnSbxPane asked before it: is the daemon still running, does the pane
// still exist, and has nothing started it meanwhile. The probe can wait tens
// of seconds (it may start a stopped sandbox), and a pane closed in that
// window has already been torn down with no PTY to close — starting the
// agent then would leave it running in the VM with nothing owning it (PR
// #272 review, M-3). gen is the pane's PTY generation when the spawn began.
func (d *Daemon) sbxSpawnStillWanted(pane *Pane, gen uint64) error {
	if d.stopping() {
		return fmt.Errorf("the daemon is shutting down")
	}
	if d.session.Pane(pane.ID) != pane {
		return fmt.Errorf("the pane was closed while its claude session was checked")
	}
	pane.PluginMu.Lock()
	cur := pane.ptyGen
	pane.PluginMu.Unlock()
	if cur != gen {
		return fmt.Errorf("the pane was restarted while its claude session was checked")
	}
	return nil
}

// sbxPrepareContext is a context that ends with the daemon, for the VM round
// trips a spawn makes outside the preparation goroutine's own context.
func (d *Daemon) sbxPrepareContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	if d.shutdown != nil {
		go func() {
			select {
			case <-d.shutdown:
				cancel()
			case <-ctx.Done():
			}
		}()
	}
	return ctx, cancel
}
