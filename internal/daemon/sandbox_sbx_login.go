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
	typ := pane.Type
	pane.PluginMu.Unlock()
	typ, _ = splitSandboxType(typ)
	d.sendPluginError(pane, ipc.PluginErrorPayload{
		PaneID: paneID,
		Title:  "Docker sign-in needed",
		Message: "sbx is not signed in to Docker, so this sandbox cannot start. " +
			"Sign in runs sbx login in this pane. When it succeeds, every sbx pane " +
			"that stopped for this reason starts again.",
		Plugin: typ,
		Fixes:  []ipc.PluginFixInfo{{Name: ipc.PluginFixSbxLogin, Label: "Sign in"}},
	})
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
	needed := pane.sbxLoginNeeded
	pane.PluginMu.Unlock()
	if !isSbx {
		return errors.New("this pane is not an sbx sandbox pane")
	}
	if !needed {
		return errors.New("this pane is not waiting for a Docker sign-in (Alt+R restarts it)")
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
	return d.startFix(pane, []string{bin, "login"}, sbxLoginLabel, plugin.FixThenRestart)
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
	return p.sbxLoginNeeded && p.PTY == nil && p.FixLabel == ""
}
