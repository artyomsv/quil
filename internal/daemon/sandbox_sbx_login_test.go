package daemon

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/sbx"
)

// The sbx "Sign in" button: a preparation that fails because sbx is not
// signed in to Docker offers it, the request runs `sbx login` in the pane
// through the fix machinery, and a zero exit starts every pane that stopped
// for the same reason. Driven through handleMessage with a dialed client,
// like the plugin fixes, because the refusals are the contract.

// makeSbxPane turns a fixme pane into an sbx pane in its own folder, so the
// restart after the sign-in fails at the profile lookup instead of reaching
// any sbx call. waiting marks it as stopped on the sign-in: flagged, with no
// live child (ExitCode set, as after a child that exited).
func makeSbxPane(t *testing.T, pane *Pane, waiting bool) {
	t.Helper()
	pane.PluginMu.Lock()
	pane.SandboxBackend = config.SandboxBackendSbx
	pane.CWD = t.TempDir()
	pane.sbxLoginNeeded = waiting
	if waiting {
		code := 1
		pane.ExitCode = &code
	}
	pane.PluginMu.Unlock()
}

func sbxLoginNeededOf(pane *Pane) bool {
	pane.PluginMu.Lock()
	defer pane.PluginMu.Unlock()
	return pane.sbxLoginNeeded
}

// stubSbxLookup makes the bounded PATH lookup find sbx at a fixed path.
func stubSbxLookup(t *testing.T) {
	t.Helper()
	prev := lookPathFn
	lookPathFn = func(string) (string, error) { return "/fake/sbx", nil }
	t.Cleanup(func() { lookPathFn = prev })
}

func signInRequest(t *testing.T, c *ipc.Client, pane *Pane, id string) ipc.PluginFixRespPayload {
	t.Helper()
	resp, _ := fixRequest(t, c, ipc.MsgPluginFixReq, id, ipc.PluginFixReqPayload{PaneID: pane.ID, Fix: ipc.PluginFixSbxLogin})
	return resp
}

func TestFailSbxPrepare_NotSignedInOffersTheSignIn(t *testing.T) {
	d, pane, _, c := fixHarness(t)
	makeSbxPane(t, pane, false)

	d.failSbxPrepare(pane.ID, fmt.Errorf("sbx env create failed: %w", sbx.ErrNotSignedIn))

	if !sbxLoginNeededOf(pane) {
		t.Fatal("sbxLoginNeeded not set by a wrapped ErrNotSignedIn")
	}
	got := readUntil(t, c, "plugin_error", func(m *ipc.Message) bool { return m.Type == ipc.MsgPluginError })
	var p ipc.PluginErrorPayload
	if err := got[len(got)-1].DecodePayload(&p); err != nil {
		t.Fatalf("decode plugin_error: %v", err)
	}
	want := []ipc.PluginFixInfo{{Name: ipc.PluginFixSbxLogin, Label: "Sign in"}}
	if p.PaneID != pane.ID || !reflect.DeepEqual(p.Fixes, want) {
		t.Fatalf("plugin_error = %+v, want pane %s with fixes %+v", p, pane.ID, want)
	}
	// The state frame carrying the SpawnError went out BEFORE the dialog, or
	// a client reconciling on it would drop the dialog it was just sent.
	var sawFailedState bool
	for _, m := range got[:len(got)-1] {
		if m.Type != ipc.MsgWorkspaceState {
			continue
		}
		var st ipc.WorkspaceState
		if err := m.DecodePayload(&st); err != nil {
			t.Fatalf("decode workspace_state: %v", err)
		}
		for _, ps := range st.Panes {
			if ps.ID == pane.ID && ps.SpawnError != "" {
				sawFailedState = true
			}
		}
	}
	if !sawFailedState {
		t.Error("no workspace_state with the SpawnError arrived before the dialog")
	}
}

func TestFailSbxPrepare_OtherErrorOffersNothing(t *testing.T) {
	d, pane, _, _ := fixHarness(t)
	makeSbxPane(t, pane, false)

	d.failSbxPrepare(pane.ID, errors.New("sbx ls: exit status 1"))

	if sbxLoginNeededOf(pane) {
		t.Fatal("sbxLoginNeeded set for a failure that is not the sign-in")
	}
}

func TestSbxLoginReq_Refusals(t *testing.T) {
	d, pane, _, c := fixHarness(t)
	stubSbxLookup(t)

	// Routed to the daemon's own handler, not to pluginFixTarget: an ordinary
	// pane is told it is not an sbx pane, never "no fix named sbx_login".
	if resp := signInRequest(t, c, pane, "not-sbx"); resp.OK || resp.Error != "this pane is not an sbx sandbox pane" {
		t.Fatalf("ordinary pane: %+v", resp)
	}

	makeSbxPane(t, pane, false)
	if resp := signInRequest(t, c, pane, "not-waiting"); resp.OK || resp.Error == "" {
		t.Fatalf("sbx pane not waiting for a sign-in: %+v", resp)
	}

	// One Docker account, one browser step: a second pane is refused while
	// another runs the sign-in.
	makeSbxPane(t, pane, true)
	other := newFixmePane(t, d)
	makeSbxPane(t, other, false)
	other.PluginMu.Lock()
	other.FixLabel = sbxLoginLabel
	other.PluginMu.Unlock()
	if resp := signInRequest(t, c, pane, "running"); resp.OK || resp.Error != "a Docker sign-in is already running in pane "+other.ID {
		t.Fatalf("second sign-in: %+v", resp)
	}
	if got := fixLabelOf(pane); got != "" {
		t.Fatalf("a refused request left FixLabel %q", got)
	}
}

func TestSbxLoginReq_RunsSbxLoginThenStartsEveryWaiter(t *testing.T) {
	d, pane, sessions, c := fixHarness(t)
	stubSbxLookup(t)
	makeSbxPane(t, pane, true)
	// A second pane stopped on the same sign-in, and a third that is not.
	waiter := newFixmePane(t, d)
	makeSbxPane(t, waiter, true)
	bystander := newFixmePane(t, d)
	makeSbxPane(t, bystander, false)

	if resp := signInRequest(t, c, pane, "sign-in"); !resp.OK {
		t.Fatalf("sign-in refused: %q", resp.Error)
	}
	login := sessions.waitFor(t, 2)
	if cmd, args, _ := login.argv(); cmd != "/fake/sbx" || !reflect.DeepEqual(args, []string{"login"}) {
		t.Fatalf("sign-in child = %s %q, want /fake/sbx [login]", cmd, args)
	}
	if got := fixLabelOf(pane); got != sbxLoginLabel {
		t.Fatalf("FixLabel = %q while the sign-in runs", got)
	}

	login.end(0)
	// Session 3 is this pane starting again, session 4 the waiter. The
	// bystander gets none.
	deadline := time.Now().Add(3 * time.Second)
	for sessions.count() < 4 {
		if time.Now().After(deadline) {
			t.Fatalf("after the sign-in %d sessions exist, want 4 (pane and waiter restarted)", sessions.count())
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if n := sessions.count(); n != 4 {
		t.Fatalf("%d sessions after the sign-in, want 4: the bystander must not restart", n)
	}
	if got := fixLabelOf(pane); got != "" {
		t.Fatalf("FixLabel = %q after the sign-in exited", got)
	}
}

func TestSbxLoginReq_FailedSignInStartsNothing(t *testing.T) {
	d, pane, sessions, c := fixHarness(t)
	stubSbxLookup(t)
	makeSbxPane(t, pane, true)
	waiter := newFixmePane(t, d)
	makeSbxPane(t, waiter, true)

	if resp := signInRequest(t, c, pane, "sign-in"); !resp.OK {
		t.Fatalf("sign-in refused: %q", resp.Error)
	}
	sessions.waitFor(t, 2).end(1)

	deadline := time.Now().Add(3 * time.Second)
	for fixLabelOf(pane) != "" {
		if time.Now().After(deadline) {
			t.Fatal("the failed sign-in's exit was never handled")
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if n := sessions.count(); n != 2 {
		t.Fatalf("%d sessions after a failed sign-in, want 2: nothing may restart", n)
	}
	// Still waiting: the user can press Alt+R or sign in again.
	if !sbxLoginNeededOf(waiter) {
		t.Error("the waiter lost its flag after a failed sign-in")
	}
}

// A press that arrives after another client restarted the pane must not
// replace the agent that restart started: a flagged pane with a LIVE child is
// refused (Greptile, PR #291).
func TestSbxLoginReq_RefusedForAPaneThatStartedAgain(t *testing.T) {
	_, pane, sessions, c := fixHarness(t)
	stubSbxLookup(t)
	makeSbxPane(t, pane, false)
	pane.PluginMu.Lock()
	pane.sbxLoginNeeded = true // flagged, but session 1 is still running
	pane.PluginMu.Unlock()

	if resp := signInRequest(t, c, pane, "late"); resp.OK {
		t.Fatal("sign-in started over a live child")
	}
	if n := sessions.count(); n != 1 {
		t.Fatalf("%d sessions, want 1: the live child must stay", n)
	}
}

// A pane whose own sign-in failed keeps its flag and its exited PTY. A later
// sign-in that succeeds in ANOTHER pane must start it too (Greptile, PR #291).
func TestSbxLoginReq_FailedPaneStartsAfterASignInElsewhere(t *testing.T) {
	d, pane, sessions, c := fixHarness(t)
	stubSbxLookup(t)
	makeSbxPane(t, pane, true)
	other := newFixmePane(t, d)
	makeSbxPane(t, other, true)

	if resp := signInRequest(t, c, pane, "first"); !resp.OK {
		t.Fatalf("first sign-in refused: %q", resp.Error)
	}
	sessions.waitFor(t, 2).end(1)
	deadline := time.Now().Add(3 * time.Second)
	for fixLabelOf(pane) != "" {
		if time.Now().After(deadline) {
			t.Fatal("the failed sign-in's exit was never handled")
		}
		time.Sleep(5 * time.Millisecond)
	}

	if resp := signInRequest(t, c, other, "second"); !resp.OK {
		t.Fatalf("second sign-in refused: %q", resp.Error)
	}
	sessions.waitFor(t, 3).end(0)
	// 4: the other pane starting again; 5: the pane whose sign-in failed.
	deadline = time.Now().Add(3 * time.Second)
	for sessions.count() < 5 {
		if time.Now().After(deadline) {
			t.Fatalf("%d sessions after the second sign-in, want 5: the failed pane was not restarted", sessions.count())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The offer can go out while no client is attached; an attaching client gets
// the dialog again for a pane that still shows the error (Greptile, PR #291).
func TestAttach_ReplaysTheSbxSignInOffer(t *testing.T) {
	_, pane, _, c := fixHarness(t)
	makeSbxPane(t, pane, true)
	pane.PluginMu.Lock()
	pane.SpawnError = sbx.ErrNotSignedIn.Error()
	pane.PluginMu.Unlock()

	attachOn(t, c, "sbx-login-replay")
	m := waitType(t, c, ipc.MsgPluginError, 3*time.Second)
	var p ipc.PluginErrorPayload
	if err := m.DecodePayload(&p); err != nil {
		t.Fatalf("decode plugin_error: %v", err)
	}
	if p.PaneID != pane.ID || len(p.Fixes) != 1 || p.Fixes[0].Name != ipc.PluginFixSbxLogin {
		t.Fatalf("replayed plugin_error = %+v", p)
	}
}

// A sign-in press and a restart on two connections: the press reads the pane
// as waiting, the restart then starts a new attempt (which clears the flag),
// and only after that does the press reach the reservation. It must be
// refused there, never replace the restart's attempt with `sbx login`
// (code-spire-beaver M-1, PR #291). Driven through restartGuardPassedFn, the
// seam TestRestartPaneReq_SerializedWithFix parks the restart on.
func TestSbxLoginReq_StaleAfterAConcurrentRestartRefused(t *testing.T) {
	d, pane, sessions, c := fixHarness(t)
	c2 := quietReader(t, d)
	stubSbxLookup(t)
	makeSbxPane(t, pane, true)

	entered := make(chan struct{}, 1)
	resume := make(chan struct{})
	prev := restartGuardPassedFn
	restartGuardPassedFn = func(p *Pane) {
		if p.ID == pane.ID {
			entered <- struct{}{}
			<-resume
		}
	}
	t.Cleanup(func() { restartGuardPassedFn = prev })

	// The restart passes its guard and parks, holding the pane's lifecycle.
	sendWithID(t, c, ipc.MsgRestartPaneReq, "r-park", ipc.RestartPaneReqPayload{PaneID: pane.ID})
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("the restart never reached its guard")
	}
	// The press reads the OLD state — flagged, no live child — and waits for
	// the lifecycle at the reservation.
	sendWithID(t, c2, ipc.MsgPluginFixReq, "s-stale", ipc.PluginFixReqPayload{PaneID: pane.ID, Fix: ipc.PluginFixSbxLogin})
	time.Sleep(200 * time.Millisecond)
	if got := fixLabelOf(pane); got != "" {
		t.Fatalf("FixLabel = %q while a restart held the pane", got)
	}

	close(resume)
	readUntilID(t, c, ipc.MsgRestartPaneResp, "r-park", 3*time.Second)
	got := readUntilID(t, c2, ipc.MsgPluginFixResp, "s-stale", 3*time.Second)
	var resp ipc.PluginFixRespPayload
	if err := got[len(got)-1].DecodePayload(&resp); err != nil {
		t.Fatalf("decode plugin_fix_resp: %v", err)
	}
	if resp.OK {
		t.Fatal("the stale sign-in was accepted after the restart began a new attempt")
	}
	if fixLabelOf(pane) != "" || sbxLoginNeededOf(pane) {
		t.Fatalf("after the refusal: FixLabel=%q sbxLoginNeeded=%v, want neither", fixLabelOf(pane), sbxLoginNeededOf(pane))
	}
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	for i, s := range sessions.made {
		if cmd, _, started := s.argv(); started && cmd == "/fake/sbx" {
			t.Fatalf("session %d ran sbx login", i+1)
		}
	}
}
