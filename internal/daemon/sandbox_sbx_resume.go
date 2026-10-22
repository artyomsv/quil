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
// claude session, or nil for a plugin that does not use claude sessions. It
// runs on the preparation goroutine — the probe is a VM round trip — and
// never under PluginMu.
//
// A fresh pane gets a new id and the plugin's start arguments, with no
// probe: an id minted this instant cannot have a transcript. A pane that
// already has one asks the VM; a transcript there means `--resume`, none
// means the same id to `--session-id` again. A probe failure is an error,
// not a fresh start: guessing would either refuse the start (129) or
// silently abandon the conversation, and the exec that follows would most
// likely fail the same way.
func (d *Daemon) sbxSessionArgs(ctx context.Context, pane *Pane, p *plugin.PanePlugin, c sbx.Client, name string) ([]string, error) {
	if !p.UsesClaudeSessions() || p.Persistence.Strategy != "preassign_id" {
		return nil, nil
	}
	pane.PluginMu.Lock()
	if pane.PluginState == nil {
		pane.PluginState = make(map[string]string)
	}
	id := pane.PluginState["session_id"]
	resumeID := pane.PluginState["resume_session_id"]
	typed := instanceArgsNameSession(handStartBase(p.Command.Cmd), pane.InstanceArgs)
	pane.PluginMu.Unlock()
	if typed {
		// The pane's own arguments name a session; a second one would be
		// refused by claude outright (see resolveSpawnArgs).
		return nil, nil
	}

	// A persisted id Quil did not write — a hand-edited snapshot — is
	// dropped, never handed to the VM.
	if id != "" && !sbx.ValidSessionID(id) {
		log.Printf("sbx: pane %s: ignoring a malformed session id (len=%d)", pane.ID, len(id))
		id = ""
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

	found, err := c.HasTranscript(ctx, name, id)
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
