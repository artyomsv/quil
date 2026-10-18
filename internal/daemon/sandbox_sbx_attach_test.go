package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	apty "github.com/artyomsv/quil/internal/pty"
	"github.com/artyomsv/quil/internal/sbx"
)

// lsJSON is an `sbx ls --json` answer in sbx 0.48's shape: one sandbox with
// these workspaces, ":ro" marking a read-only one.
func lsJSON(t *testing.T, name string, workspaces ...string) string {
	t.Helper()
	b, err := json.Marshal([]map[string]any{{
		"name": name, "agent": "claude", "status": "stopped", "workspaces": workspaces,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

func TestApplySandboxSpec_SbxName(t *testing.T) {
	tests := []struct {
		name     string
		spec     ipc.SandboxSpec
		wantErr  bool
		wantName string
	}{
		{"attach by name", ipc.SandboxSpec{Backend: "sbx", Name: "team-box"}, false, "team-box"},
		{"a name sbx would read as a flag", ipc.SandboxSpec{Backend: "sbx", Name: "-v"}, true, ""},
		{"a name with a control character", ipc.SandboxSpec{Backend: "sbx", Name: "box\x1b[2J"}, true, ""},
		{"a name next to docker", ipc.SandboxSpec{Image: "img:1", Name: "team-box"}, true, ""},
		{"no name uses the profile", ipc.SandboxSpec{Backend: "sbx"}, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pane := &Pane{ID: "p1"}
			err := applySandboxSpec(pane, &tt.spec)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if pane.SandboxName != tt.wantName {
				t.Errorf("SandboxName = %q, want %q", pane.SandboxName, tt.wantName)
			}
			if tt.wantErr && (pane.SandboxBackend != "" || pane.SandboxImage != "") {
				t.Errorf("a refused spec left sandbox state: backend=%q image=%q", pane.SandboxBackend, pane.SandboxImage)
			}
		})
	}
}

// attachRun spawns an attached pane and waits for the preparation. It returns
// the session the respawn started, or nil when the pane was not respawned.
func attachRun(t *testing.T, d *Daemon, pane *Pane) *fakeSession {
	t.Helper()
	respawned := make(chan *fakeSession, 1)
	prevSpawn := sbxSpawnFn
	sbxSpawnFn = func(d *Daemon, p *Pane, wrap func(apty.Session) apty.Session) error {
		s := &fakeSession{}
		err := d.spawnPane(p, wrap(s), false)
		respawned <- s
		return err
	}
	t.Cleanup(func() { sbxSpawnFn = prevSpawn })

	first := &fakeSession{}
	if err := d.spawnPane(pane, first, false); err != nil {
		t.Fatalf("spawnPane = %v; standing down while the sandbox is checked is not a failure", err)
	}
	if first.startCmd != "" {
		t.Fatalf("the first spawn started %q before the sandbox was checked", first.startCmd)
	}
	d.sbxPrep.wait()
	select {
	case s := <-respawned:
		return s
	case <-time.After(100 * time.Millisecond):
		return nil
	}
}

// The whole attach path: no profile approval, no create, no mount, no host
// step — only `sbx exec` into the sandbox the pane names. The fixture's
// profile is deliberately NOT approved, so a pass proves attach never reads it.
func TestSpawnPane_SbxAttachExecsInTheExistingSandbox(t *testing.T) {
	d, pane, r := sbxFixture(t, false)
	pane.SandboxName = "team-box"
	r.lsJSON = lsJSON(t, "team-box", pane.CWD, filepath.Join(pane.CWD, "env.yaml")+":ro")

	s := attachRun(t, d, pane)
	if s == nil {
		t.Fatalf("the pane was not started; calls = %v, error = %q", r.calls, pane.SpawnError)
	}
	for _, step := range [][]string{{"/fake/sbx", "env", "create"}, {"/fake/sbx", "mount"}, {"/fake/sbx", "rm"}} {
		if r.ran(step...) {
			t.Errorf("attach ran %v, which changes a sandbox Quil does not own; calls = %v", step, r.calls)
		}
	}
	wd := pane.CWD
	if runtime.GOOS == "windows" {
		wd = ""
	}
	p := d.registry.Get("claude-code")
	tag := spawnTag(t, s.startArgs)
	want := sbx.ExecArgs("team-box", wd, append(append([]string{}, p.Command.Env...), sbx.TagEnv+"="+tag), p.Command.Cmd, nil)
	if len(s.startArgs) < len(want) || !reflect.DeepEqual(s.startArgs[:len(want)], want) {
		t.Errorf("args = %v, want prefix %v", s.startArgs, want)
	}
	// The leftover stop runs in a foreign sandbox too, so it must match this
	// pane's own tag and nothing wider.
	if !r.ran(append([]string{"/fake/sbx"}, sbx.StopArgs("team-box", pane.ID+"-", true)...)...) {
		t.Errorf("the leftover stop did not use the pane's tag prefix; calls = %v", r.calls)
	}
}

// The refusals: a missing sandbox is never created, and a pane folder the
// sandbox does not mount writable is never started.
func TestSpawnPane_SbxAttachRefusals(t *testing.T) {
	tests := []struct {
		name    string
		answer  func(cwd string) string
		wantErr string
	}{
		{"missing sandbox", func(string) string { return "[]\n" }, "no such sbx sandbox"},
		{"folder outside the sandbox", func(string) string { return lsJSON(t, "team-box", t.TempDir()) }, "is not in a writable folder"},
		{"read-only folder inside a writable one", func(cwd string) string {
			return lsJSON(t, "team-box", filepath.Dir(cwd), cwd+":ro")
		}, "is not in a writable folder"},
		{"folder mounted read-only", func(cwd string) string { return lsJSON(t, "team-box", cwd+":ro") }, "mounts no writable folder"},
		{"answer that is not JSON", func(string) string { return "Error: something broke\n" }, "no JSON"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, pane, r := sbxFixture(t, false)
			pane.SandboxName = "team-box"
			r.lsJSON = tt.answer(pane.CWD)

			if s := attachRun(t, d, pane); s != nil {
				t.Fatalf("the pane was started (%s %v)", s.startCmd, s.startArgs)
			}
			pane.PluginMu.Lock()
			spawnErr := pane.SpawnError
			pane.PluginMu.Unlock()
			if !strings.Contains(spawnErr, tt.wantErr) {
				t.Errorf("SpawnError = %q, want it to contain %q", spawnErr, tt.wantErr)
			}
			if r.ran("/fake/sbx", "env", "create") || r.ran("/fake/sbx", "exec", "-it") {
				t.Errorf("a refused attach reached create or the agent's exec; calls = %v", r.calls)
			}
		})
	}
}

// A link inside the sandbox's folder that leads outside it is refused: the
// agent would run where the link leads.
func TestSpawnPane_SbxAttachRefusesALinkOut(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	d, pane, r := sbxFixture(t, false)
	outside := t.TempDir()
	link := filepath.Join(pane.CWD, "out")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	pane.CWD = link
	pane.SandboxName = "team-box"
	r.lsJSON = lsJSON(t, "team-box", filepath.Dir(link))

	if s := attachRun(t, d, pane); s != nil {
		t.Fatalf("a pane in a link out of the sandbox was started (%v)", s.startArgs)
	}
}

// A persisted name that is not valid any more is refused, never dropped:
// dropping it would move the pane into the profile's sandbox.
func TestSbxTargetFor_InvalidPersistedNameRefuses(t *testing.T) {
	if _, err := sbxTargetFor("-bad", t.TempDir()); err == nil {
		t.Fatal("an invalid persisted name was accepted")
	}
	got, err := sbxTargetFor("team-box", t.TempDir())
	if err != nil || !got.attach || got.name != "team-box" || got.key() != "attach:team-box" {
		t.Errorf("sbxTargetFor = %+v, %v", got, err)
	}
}

func TestSnapshot_SbxNameSurvivesTheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	d := newTestDaemonInDir(t, dir)
	tab := d.session.CreateTab("t")
	pane := mustPane(t, d, tab.ID)
	pane.Type = "claude-code"
	pane.SandboxBackend = config.SandboxBackendSbx
	pane.SandboxName = "team-box"
	d.snapshot()

	restored := newTestDaemonInDir(t, dir)
	if err := restored.restoreWorkspace(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	var got *Pane
	for _, tb := range restored.session.Tabs() {
		for _, p := range restored.session.Panes(tb.ID) {
			got = p
		}
	}
	if got == nil {
		t.Fatal("no pane survived the round trip")
	}
	if got.SandboxBackend != config.SandboxBackendSbx || got.SandboxName != "team-box" {
		t.Errorf("restored backend=%q name=%q", got.SandboxBackend, got.SandboxName)
	}
}

// The dialog's list carries both kinds of folder, apart: the most specific
// one decides, so the dialog needs the read-only ones too.
func TestListSbxSandboxes_SplitsWritableAndReadOnlyFolders(t *testing.T) {
	r := &scriptRunner{lsJSON: lsJSON(t, "team-box", "/w/ticket", "/w/app/.git:ro")}
	prevRunner, prevBin := sbxRunner, sbxBinaryFn
	sbxRunner = r
	sbxBinaryFn = func() (string, error) { return "/fake/sbx", nil }
	t.Cleanup(func() { sbxRunner, sbxBinaryFn = prevRunner, prevBin })

	got := listSbxSandboxes(context.Background())
	want := []ipc.SbxSandbox{{Name: "team-box", Status: "stopped", Folders: []string{"/w/ticket"},
		ReadOnlyFolders: []string{"/w/app/.git"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("listSbxSandboxes = %+v, want %+v", got, want)
	}
}

// `sbx ls` does not list a folder another tool added with `sbx mount`, so a
// folder it does not list is checked inside the sandbox before refusing —
// with the folder as an argument, never as script text.
func TestSpawnPane_SbxAttachAsksTheSandboxForAnUnlistedFolder(t *testing.T) {
	d, pane, r := sbxFixture(t, false)
	pane.SandboxName = "team-box"
	r.lsJSON = lsJSON(t, "team-box", t.TempDir())
	r.writable = true

	s := attachRun(t, d, pane)
	if s == nil {
		t.Fatalf("a folder the sandbox reports writable was refused: %q; calls = %v", pane.SpawnError, r.calls)
	}
	if !r.ran(append([]string{"/fake/sbx"}, sbx.WritableArgs("team-box", pane.CWD)...)...) {
		t.Errorf("the sandbox was not asked about the folder; calls = %v", r.calls)
	}
	for _, step := range [][]string{{"/fake/sbx", "env", "create"}, {"/fake/sbx", "mount"}} {
		if r.ran(step...) {
			t.Errorf("attach ran %v; calls = %v", step, r.calls)
		}
	}
}

// On a Windows host the folder probe is never run: a Windows path means
// nothing inside the Linux VM. The exec has no -w there, so the agent starts
// in the sandbox's main workspace, and the pane says so.
func TestSpawnPane_SbxAttachOnWindowsSkipsTheProbe(t *testing.T) {
	d, pane, r := sbxFixture(t, false)
	prevOS := sbxHostOS
	sbxHostOS = "windows"
	t.Cleanup(func() { sbxHostOS = prevOS })
	pane.SandboxName = "team-box"
	r.lsJSON = lsJSON(t, "team-box", t.TempDir())

	s := attachRun(t, d, pane)
	if s == nil {
		t.Fatalf("the pane was refused: %q; calls = %v", pane.SpawnError, r.calls)
	}
	if r.ran(append([]string{"/fake/sbx"}, sbx.WritableArgs("team-box", pane.CWD)...)...) {
		t.Errorf("the probe ran with a host path on Windows; calls = %v", r.calls)
	}
	if slices.Contains(s.startArgs, "-w") {
		t.Errorf("the exec passed -w on Windows: %v", s.startArgs)
	}
}
