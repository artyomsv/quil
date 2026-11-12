package daemon

import (
	"errors"
	"fmt"
	"log"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/plugin"
)

// The Docker sign-in of the sbx backend.
//
// sbx keeps its own Docker sign-in, and it expires. Every sbx call then
// answers "not signed in" (sbx.ErrNotSignedIn), so a preparation fails and
// the pane is left with an error and Alt+R — which fails the same way until
// the user runs `sbx login` by hand somewhere else.
//
// Quil cannot sign in on its own: a non-interactive `sbx login` needs a
// Docker password or access token on stdin, and Quil keeps no credential of
// its own (the posture of `claude setup-token`, see .claude/rules/sandbox.md).
// What it can do is the Stripe shape (plugin fixes, pluginfix.go): a dialog
// with a "Sign in" button that runs `sbx login` IN THE PANE, so the user sees
// its device code and browser step and answers them, and a successful exit
// starts the pane again.
//
// It is the daemon's own button, not a plugin fix: the error comes from the
// preparation, before any child writes output a handler could match, and the
// fix is the same for every agent plugin. It reuses the fix machinery
// (startFix, onFixExit) so the pane shows "running fix", refuses a restart
// over a half-done sign-in, and restarts on a zero exit.

// sbxLoginLabel is the fix label of the sign-in: the pane's "running fix"
// text, and the marker onFixExit and the single flight look for.
const sbxLoginLabel = "Sign in to Docker"

// offerSbxLogin marks the pane as waiting for a Docker sign-in and sends the
// dialog with the "Sign in" button, plus its sidebar card.
func (d *Daemon) offerSbxLogin(paneID string) {
	pane := d.session.Pane(paneID)
	if pane == nil {
		return
	}
	pane.PluginMu.Lock()
	pane.sbxLoginNeeded = true
	pane.PluginMu.Unlock()
	d.sendPluginError(pane, sbxLoginOffer(pane))
}

// sbxLoginMessage is the dialog's text.
const sbxLoginMessage = "sbx is not signed in to Docker, so this sandbox cannot start. " +
	"Sign in runs sbx login in this pane. When it succeeds, every sbx pane " +
	"that stopped for this reason starts again."

// sbxLoginOffer is the dialog for one pane.
func sbxLoginOffer(pane *Pane) ipc.PluginErrorPayload {
	pane.PluginMu.Lock()
	typ := pane.Type
	pane.PluginMu.Unlock()
	typ, _ = splitSandboxType(typ)
	return ipc.PluginErrorPayload{
		PaneID:  pane.ID,
		Title:   "Docker sign-in needed",
		Message: sbxLoginMessage,
		Plugin:  typ,
		Fixes:   []ipc.PluginFixInfo{{Name: ipc.PluginFixSbxLogin, Label: "Sign in"}},
	}
}

// replaySbxLoginOffers sends an attaching client the dialog of every pane
// still stopped on the sign-in error. The offer went out when the
// preparation failed, which can be while no client was attached — during a
// restore before the socket opens, say — and the flag is runtime-only, so
// without this a later client sees only the error and Alt+R. Only panes
// that still show the error: the TUI drops a sign-in dialog whose pane has
// no SpawnError (reconcilePluginDialogs). No sidebar card — the event
// replay already carried the one sent with the offer.
func (d *Daemon) replaySbxLoginOffers(conn *ipc.Conn) {
	for _, p := range d.session.AllPanes() {
		p.PluginMu.Lock()
		offer := sbxLoginStoppedLocked(p) && p.SpawnError != ""
		p.PluginMu.Unlock()
		if !offer {
			continue
		}
		msg, _ := ipc.NewMessage(ipc.MsgPluginError, sbxLoginOffer(p))
		if err := conn.SendBlocking(msg, d.shutdown); err != nil {
			return // client disconnected or daemon shutting down
		}
	}
}

// handleSbxLoginReq answers the "Sign in" button: it runs `sbx login` in the
// pane. Refused, with the pane untouched, unless the pane is an sbx pane whose
// last preparation failed for the sign-in — so no client can make an ordinary
// pane run sbx — and unless no other pane is already signing in: one Docker
// account, one browser step.
func (d *Daemon) handleSbxLoginReq(conn *ipc.Conn, msg *ipc.Message, paneID string) {
	if err := d.startSbxLogin(paneID); err != nil {
		refuseFix(conn, msg, ipc.MsgPluginFixResp, err.Error())
		return
	}
	respondTo(conn, msg.ID, ipc.MsgPluginFixResp, ipc.PluginFixRespPayload{OK: true})
}

func (d *Daemon) startSbxLogin(paneID string) error {
	pane := d.session.Pane(paneID)
	if pane == nil {
		return fmt.Errorf("no such pane: %s", paneID)
	}
	pane.PluginMu.Lock()
	isSbx := pane.SandboxBackend == config.SandboxBackendSbx
	pane.PluginMu.Unlock()
	if !isSbx {
		return errors.New("this pane is not an sbx sandbox pane")
	}
	// Checked again where the fix is reserved, not only here: between this
	// request and the reservation another client can restart the pane, and
	// the sign-in must not replace the agent that restart started.
	eligible := func() error {
		if pane.SandboxBackend != config.SandboxBackendSbx {
			return errors.New("this pane is not an sbx sandbox pane")
		}
		// Teardown has begun (StopInput). The registry check after the fix
		// starts (startFixIf) covers a destroy that has not reached this yet.
		if pane.inputStopped {
			return errors.New("this pane is being closed")
		}
		if !sbxLoginStoppedLocked(pane) {
			return errors.New("this pane is not waiting for a Docker sign-in (Alt+R restarts it)")
		}
		return nil
	}
	pane.PluginMu.Lock()
	err := eligible()
	pane.PluginMu.Unlock()
	if err != nil {
		return err
	}
	// The check and the start are one step: two presses on two panes must
	// not both pass the check.
	d.sbxLoginMu.Lock()
	defer d.sbxLoginMu.Unlock()
	if other := d.sbxLoginRunningIn(); other != "" {
		return fmt.Errorf("a Docker sign-in is already running in pane %s", other)
	}
	// Bounded: a PATH walk on this conn's dispatch goroutine.
	bin := lookPathWithin("sbx", d.pluginUpdateCfg().targetTimeout)
	if bin == "" {
		return errors.New("could not locate sbx on this machine")
	}
	return d.startFixIf(pane, []string{bin, "login"}, sbxLoginLabel, plugin.FixThenRestart, eligible)
}

// sbxLoginRunningIn names a pane whose current child is the sign-in, or "".
// Read from the panes themselves rather than a daemon flag, so a pane closed
// in the middle of a sign-in cannot leave the flight claimed.
func (d *Daemon) sbxLoginRunningIn() string {
	for _, p := range d.session.AllPanes() {
		p.PluginMu.Lock()
		running := p.FixLabel == sbxLoginLabel && p.SandboxBackend == config.SandboxBackendSbx
		p.PluginMu.Unlock()
		if running {
			return p.ID
		}
	}
	return ""
}

// retrySbxLoginWaiters starts every other sbx pane that stopped because sbx
// was not signed in, after a sign-in succeeded. A pane that has a process
// again, or runs a fix, is left alone; the check is repeated under the
// pane's lifecycleMu so a restart the user started meanwhile is not doubled.
func (d *Daemon) retrySbxLoginWaiters(except string) {
	for _, p := range d.session.AllPanes() {
		if p.ID == except || !sbxLoginWaiting(p) {
			continue
		}
		p.lifecycleMu.Lock()
		if sbxLoginWaiting(p) {
			log.Printf("pane %s: Docker sign-in succeeded in pane %s, starting again", p.ID, except)
			if !d.restartPaneInPlace(p) {
				log.Printf("pane %s: start after Docker sign-in failed", p.ID)
			}
		}
		p.lifecycleMu.Unlock()
	}
}

// sbxLoginWaiting reports whether a pane is stopped on a missing sign-in.
func sbxLoginWaiting(p *Pane) bool {
	p.PluginMu.Lock()
	defer p.PluginMu.Unlock()
	return sbxLoginStoppedLocked(p)
}

// sbxLoginStoppedLocked is sbxLoginWaiting with PluginMu held: flagged, no
// fix running, and no live child. "No live child" is a PTY that is gone (the
// preparation stood down and failed) OR one whose child has exited (a failed
// `sbx login` leaves its PTY installed with ExitCode set, and that pane must
// still start when a sign-in from another pane succeeds).
func sbxLoginStoppedLocked(p *Pane) bool {
	return p.sbxLoginNeeded && p.FixLabel == "" && (p.PTY == nil || p.ExitCode != nil)
}
