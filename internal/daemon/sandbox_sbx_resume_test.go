package daemon

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	apty "github.com/artyomsv/quil/internal/pty"
	"github.com/artyomsv/quil/internal/sbx"
)

const sbxSessionA = "2db05609-f1d5-4576-b5b2-ff114519726b"

// sbxResumeFixture is sbxFixture in attach mode, which needs no approval
// and runs the shortest path to the exec.
func sbxResumeFixture(t *testing.T) (*Daemon, *Pane, *scriptRunner) {
	t.Helper()
	d, pane, r := sbxFixture(t, false)
	pane.SandboxName = "team-box"
	r.lsJSON = lsJSON(t, "team-box", pane.CWD)
	return d, pane, r
}

// sessionArgsOf returns what follows the agent command in the exec argv.
func sessionArgsOf(t *testing.T, d *Daemon, s *fakeSession) []string {
	t.Helper()
	cmd := d.registry.Get("claude-code").Command.Cmd
	for i, a := range s.startArgs {
		if a == cmd {
			return s.startArgs[i+1:]
		}
	}
	t.Fatalf("no %s in %v", cmd, s.startArgs)
	return nil
}

// probeCalls returns the session probes the sandbox was asked, in full.
func (r *scriptRunner) probeCalls() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	script := sbx.TranscriptArgs("x", "", nil, sbxSessionA)[4]
	var out [][]string
	for _, c := range r.calls {
		if len(c) > 2 && c[1] == "exec" && slices.Contains(c, script) {
			out = append(out, c)
		}
	}
	return out
}

func (r *scriptRunner) probed(id string) bool {
	for _, c := range r.probeCalls() {
		if c[len(c)-1] == id {
			return true
		}
	}
	return false
}

// The probe looks where the agent will: it runs in the pane's folder with
// the plugin's environment, so a CLAUDE_CONFIG_DIR the plugin sets is the
// folder searched — probing claude's default folder would miss the
// transcript and the restart would be refused.
func TestSpawnPane_SbxProbeSharesTheAgentsFolderAndEnvironment(t *testing.T) {
	d, pane, r := sbxResumeFixture(t)
	p := d.registry.Get("claude-code")
	p.Command.Env = append(slices.Clone(p.Command.Env), "CLAUDE_CONFIG_DIR=.claude-cfg")
	pane.PluginState = map[string]string{"session_id": sbxSessionA}
	r.transcript = true
	s := attachRun(t, d, pane)
	if s == nil {
		t.Fatalf("the pane was not started; error = %q", pane.SpawnError)
	}
	probes := r.probeCalls()
	if len(probes) != 1 {
		t.Fatalf("probes = %v, want one", probes)
	}
	want := append([]string{"/fake/sbx"}, sbx.TranscriptArgs("team-box", pane.CWD, p.Command.Env, sbxSessionA)...)
	if !reflect.DeepEqual(probes[0], want) {
		t.Errorf("probe =\n%v\nwant\n%v", probes[0], want)
	}
	// The exec that follows carries the same folder and environment (plus
	// the per-spawn tag).
	if !slices.Contains(s.startArgs, "CLAUDE_CONFIG_DIR=.claude-cfg") || slices.Index(s.startArgs, "-w") < 0 {
		t.Errorf("exec args = %v, want the plugin env and -w", s.startArgs)
	}
}

// A plugin whose OWN arguments already name a session (`--continue`, a
// configured `--resume`) gets no second selector, exactly as instance
// arguments naming one do.
func TestSpawnPane_SbxPluginDefaultSessionSelectorGetsNoSecondOne(t *testing.T) {
	d, pane, r := sbxResumeFixture(t)
	p := d.registry.Get("claude-code")
	p.Command.Args = append(slices.Clone(p.Command.Args), "--continue")
	s := attachRun(t, d, pane)
	if s == nil {
		t.Fatalf("the pane was not started; error = %q", pane.SpawnError)
	}
	if got := sessionArgsOf(t, d, s); !reflect.DeepEqual(got, []string{"--continue"}) {
		t.Errorf("args after the command = %v, want only the plugin's own [--continue]", got)
	}
	if len(r.probeCalls()) != 0 || pane.PluginState["session_id"] != "" {
		t.Errorf("a plugin-selected session was probed or recorded: probes=%v session_id=%q", r.probeCalls(), pane.PluginState["session_id"])
	}
}

// A pane closed while the VM is still answering the probe is not started:
// its teardown found no PTY to close, so an agent started now would run in
// the VM with nothing owning it.
func TestSpawnPane_SbxPaneClosedDuringTheProbeIsNotStarted(t *testing.T) {
	d, pane, r := sbxResumeFixture(t)
	pane.PluginState = map[string]string{"session_id": sbxSessionA}
	r.transcript = true
	gate := make(chan struct{})
	r.probeGate = gate

	respawned := make(chan *fakeSession, 1)
	prevSpawn := sbxSpawnFn
	sbxSpawnFn = func(d *Daemon, p *Pane, wrap func(apty.Session) apty.Session) error {
		s := &fakeSession{}
		err := d.spawnPane(p, wrap(s), false)
		respawned <- s
		return err
	}
	t.Cleanup(func() { sbxSpawnFn = prevSpawn })
	if err := d.spawnPane(pane, &fakeSession{}, false); err != nil {
		t.Fatalf("spawnPane = %v", err)
	}
	// Wait until the probe is in flight, then close the pane under it.
	deadline := time.Now().Add(5 * time.Second)
	for len(r.probeCalls()) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the probe never ran; calls = %v", r.calls)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := d.session.DestroyPane(pane.ID); err != nil {
		t.Fatal(err)
	}
	close(gate)
	d.sbxPrep.wait()

	var s *fakeSession
	select {
	case s = <-respawned:
	case <-time.After(5 * time.Second):
		t.Fatal("the respawn never finished")
	}
	if s.startCmd != "" {
		t.Fatalf("the agent was started (%s %v) for a pane closed during the probe", s.startCmd, s.startArgs)
	}
	pane.PluginMu.Lock()
	pty := pane.PTY
	pane.PluginMu.Unlock()
	if pty != nil {
		t.Error("a PTY was installed on the closed pane")
	}
}

// A first start mints the session id, keeps it on the pane (so the host
// persists it) and hands it to claude. No probe: a new id has no transcript.
func TestSpawnPane_SbxFirstStartMintsASessionID(t *testing.T) {
	d, pane, r := sbxResumeFixture(t)
	s := attachRun(t, d, pane)
	if s == nil {
		t.Fatalf("the pane was not started; error = %q", pane.SpawnError)
	}
	id := pane.PluginState["session_id"]
	if !sbx.ValidSessionID(id) {
		t.Fatalf("session_id = %q, want a UUID", id)
	}
	got := sessionArgsOf(t, d, s)
	if len(got) != 2 || got[0] != "--session-id" || got[1] != id {
		t.Errorf("session args = %v, want [--session-id %s]", got, id)
	}
	if r.probed(id) {
		t.Error("a freshly minted id was probed in the sandbox")
	}
}

// A pane that already has a session resumes it when the VM holds its
// transcript — a restart, a daemon restart or a restored workspace all
// reach this.
func TestSpawnPane_SbxRestartResumesTheSessionTheVMHolds(t *testing.T) {
	d, pane, r := sbxResumeFixture(t)
	pane.PluginState = map[string]string{"session_id": sbxSessionA, "transcript_path": "/stale/host/path.jsonl"}
	r.transcript = true
	s := attachRun(t, d, pane)
	if s == nil {
		t.Fatalf("the pane was not started; error = %q", pane.SpawnError)
	}
	if got := sessionArgsOf(t, d, s); len(got) != 2 || got[0] != "--resume" || got[1] != sbxSessionA {
		t.Errorf("session args = %v, want [--resume %s]", got, sbxSessionA)
	}
	if !r.probed(sbxSessionA) {
		t.Errorf("the sandbox was not asked about %s; calls = %v", sbxSessionA, r.calls)
	}
	if pane.PluginState["session_id"] != sbxSessionA {
		t.Errorf("session_id = %q, want kept", pane.PluginState["session_id"])
	}
	if _, has := pane.PluginState["transcript_path"]; has {
		t.Error("a host transcript path survived on an sbx pane; nothing on the host names the VM's file")
	}
}

// The same id with NO transcript in the VM (a restart on the trust screen,
// or a sandbox recreated without its sessions) starts under that id again —
// --resume would be refused.
func TestSpawnPane_SbxRestartWithoutATranscriptKeepsTheID(t *testing.T) {
	d, pane, r := sbxResumeFixture(t)
	pane.PluginState = map[string]string{"session_id": sbxSessionA}
	r.transcript = false
	s := attachRun(t, d, pane)
	if s == nil {
		t.Fatalf("the pane was not started; error = %q", pane.SpawnError)
	}
	if got := sessionArgsOf(t, d, s); len(got) != 2 || got[0] != "--session-id" || got[1] != sbxSessionA {
		t.Errorf("session args = %v, want [--session-id %s]", got, sbxSessionA)
	}
	if !r.probed(sbxSessionA) {
		t.Errorf("the sandbox was not asked; calls = %v", r.calls)
	}
}

// A probe that fails is a failed start with the reason on the pane, not a
// guess: either guess can refuse the start or abandon the conversation.
func TestSpawnPane_SbxProbeFailureDoesNotStartTheAgent(t *testing.T) {
	d, pane, r := sbxResumeFixture(t)
	pane.PluginState = map[string]string{"session_id": sbxSessionA}
	r.probeErr = errors.New("exit status 1")
	s := attachRun(t, d, pane)
	if s != nil && s.startCmd != "" {
		t.Fatalf("the agent was started (%s %v) although the session check failed", s.startCmd, s.startArgs)
	}
	if !strings.Contains(pane.SpawnError, "claude session") {
		t.Errorf("SpawnError = %q, want the failed session check", pane.SpawnError)
	}
	if pane.PluginState["session_id"] != sbxSessionA {
		t.Errorf("session_id = %q, want kept for the next try", pane.PluginState["session_id"])
	}
}

// A session another running pane holds is not resumed twice: the pane gets
// a fresh id instead.
func TestSpawnPane_SbxDoesNotResumeASessionAnotherPaneHolds(t *testing.T) {
	d, pane, r := sbxResumeFixture(t)
	other, err := d.session.CreatePane(pane.TabID, pane.CWD)
	if err != nil {
		t.Fatal(err)
	}
	other.Type = "claude-code"
	other.PluginState = map[string]string{"session_id": sbxSessionA}
	other.PTY = &fakeSession{} // running

	pane.PluginState = map[string]string{"session_id": sbxSessionA}
	r.transcript = true
	s := attachRun(t, d, pane)
	if s == nil {
		t.Fatalf("the pane was not started; error = %q", pane.SpawnError)
	}
	got := sessionArgsOf(t, d, s)
	id := pane.PluginState["session_id"]
	if id == sbxSessionA || !sbx.ValidSessionID(id) {
		t.Fatalf("session_id = %q, want a new id", id)
	}
	if len(got) != 2 || got[0] != "--session-id" || got[1] != id {
		t.Errorf("session args = %v, want [--session-id %s]", got, id)
	}
}

// A persisted id of the wrong shape never reaches the VM: a new one is
// minted and nothing is probed.
func TestSpawnPane_SbxMalformedPersistedIDIsReplaced(t *testing.T) {
	d, pane, r := sbxResumeFixture(t)
	pane.PluginState = map[string]string{"session_id": "../../etc; rm -rf /"}
	s := attachRun(t, d, pane)
	if s == nil {
		t.Fatalf("the pane was not started; error = %q", pane.SpawnError)
	}
	id := pane.PluginState["session_id"]
	if !sbx.ValidSessionID(id) {
		t.Fatalf("session_id = %q, want a fresh UUID", id)
	}
	for _, c := range r.calls {
		for _, a := range c {
			if strings.Contains(a, "rm -rf") {
				t.Fatalf("the malformed id reached sbx: %v", c)
			}
		}
	}
	if got := sessionArgsOf(t, d, s); len(got) != 2 || got[1] != id {
		t.Errorf("session args = %v, want [--session-id %s]", got, id)
	}
}

// A pane created to resume a session (resume_session_id) treats that id as
// its own from the first start: resumed when the VM has it.
func TestSpawnPane_SbxResumeTargetIsProbedLikeARestart(t *testing.T) {
	d, pane, r := sbxResumeFixture(t)
	pane.PluginState = map[string]string{"resume_session_id": sbxSessionA}
	r.transcript = true
	s := attachRun(t, d, pane)
	if s == nil {
		t.Fatalf("the pane was not started; error = %q", pane.SpawnError)
	}
	if got := sessionArgsOf(t, d, s); len(got) != 2 || got[0] != "--resume" || got[1] != sbxSessionA {
		t.Errorf("session args = %v, want [--resume %s]", got, sbxSessionA)
	}
	if pane.PluginState["session_id"] != sbxSessionA {
		t.Errorf("session_id = %q, want the resume target adopted", pane.PluginState["session_id"])
	}
}

// Arguments that already name a session (a template's frozen `--resume`)
// get no second one; claude refuses an argv naming two.
func TestSpawnPane_SbxTypedSessionGetsNoSecondOne(t *testing.T) {
	d, pane, r := sbxResumeFixture(t)
	pane.InstanceArgs = []string{"--resume", sbxSessionA}
	s := attachRun(t, d, pane)
	if s == nil {
		t.Fatalf("the pane was not started; error = %q", pane.SpawnError)
	}
	if got := sessionArgsOf(t, d, s); len(got) != 2 || got[0] != "--resume" || got[1] != sbxSessionA {
		t.Errorf("args after the command = %v, want only the pane's own [--resume %s]", got, sbxSessionA)
	}
	if r.probed(sbxSessionA) || pane.PluginState["session_id"] != "" {
		t.Errorf("a typed session was probed or recorded: probed=%v session_id=%q", r.probed(sbxSessionA), pane.PluginState["session_id"])
	}
}
