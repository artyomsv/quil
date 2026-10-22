package daemon

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/gitworktree"
	"github.com/artyomsv/quil/internal/ipc"
	apty "github.com/artyomsv/quil/internal/pty"
	"github.com/artyomsv/quil/internal/sbx"
)

func TestApplySandboxSpec_Sbx(t *testing.T) {
	tests := []struct {
		name        string
		spec        ipc.SandboxSpec
		wantErr     bool
		wantBackend string
		wantImage   string
	}{
		{"sbx alone", ipc.SandboxSpec{Backend: "sbx"}, false, "sbx", ""},
		{"sbx with an image", ipc.SandboxSpec{Backend: "sbx", Image: "img:1"}, true, "", ""},
		{"sbx with a sign-in mode", ipc.SandboxSpec{Backend: "sbx", Auth: "token"}, true, "", ""},
		{"unknown backend", ipc.SandboxSpec{Backend: "vm", Image: "img:1"}, true, "", ""},
		{"docker spelled out", ipc.SandboxSpec{Backend: "docker", Image: "img:1"}, false, "", "img:1"},
		{"docker by default", ipc.SandboxSpec{Image: "img:1"}, false, "", "img:1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pane := &Pane{ID: "p1"}
			err := applySandboxSpec(pane, &tt.spec)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if pane.SandboxBackend != tt.wantBackend || pane.SandboxImage != tt.wantImage {
				t.Errorf("backend=%q image=%q, want %q %q", pane.SandboxBackend, pane.SandboxImage,
					tt.wantBackend, tt.wantImage)
			}
		})
	}
}

// An sbx create on a daemon configured for the token flow + shared directory
// must not be given a Claude config choice: that cage is the profile's.
func TestApplySandboxSpecFor_SbxGetsNoSignInSettlement(t *testing.T) {
	d := &Daemon{cfg: tokenFlowConfig()}
	d.cfg.Sandbox.SharedClaudeConfig = true
	pane := &Pane{ID: "p1"}
	if err := d.applySandboxSpecFor(pane, &ipc.SandboxSpec{Backend: "sbx"}); err != nil {
		t.Fatal(err)
	}
	if pane.SandboxClaudeConfig != "" || pane.SandboxAuth != "" {
		t.Errorf("an sbx pane got sign-in state: auth=%q config=%q", pane.SandboxAuth, pane.SandboxClaudeConfig)
	}
}

// scriptRunner answers sbx calls and records them.
type scriptRunner struct {
	mu       sync.Mutex
	calls    [][]string
	exists   bool
	mountErr error
	// lsJSON is the answer to `sbx ls --json`.
	lsJSON string
	// writable is the in-sandbox folder probe's answer.
	writable bool
	// transcript is the in-sandbox session probe's answer; probeErr fails
	// that probe instead.
	transcript bool
	probeErr   error
	// probeGate, when set, holds the session probe until it is closed.
	probeGate chan struct{}
}

func (r *scriptRunner) Run(_ context.Context, _ string, w io.Writer, argv ...string) error {
	r.mu.Lock()
	r.calls = append(r.calls, append([]string(nil), argv...))
	exists, mountErr, lsJSON, writable := r.exists, r.mountErr, r.lsJSON, r.writable
	transcript, probeErr, probeGate := r.transcript, r.probeErr, r.probeGate
	r.mu.Unlock()
	if len(argv) > 1 {
		switch argv[1] {
		case "version":
			io.WriteString(w, "sbx version 0.43.0\n")
		case "ls":
			if slices.Contains(argv, "--json") {
				io.WriteString(w, lsJSON)
				return nil
			}
			io.WriteString(w, "SANDBOX   AGENT   STATUS   PORTS   WORKSPACE\n")
			if exists {
				io.WriteString(w, "my-app    claude  running          /work\n")
			}
		case "mount":
			return mountErr
		case "exec":
			if writable && slices.ContainsFunc(argv, func(a string) bool { return strings.Contains(a, "QUIL_SBX_WRITABLE") }) {
				io.WriteString(w, "QUIL_SBX_WRITABLE\n")
			}
			if slices.ContainsFunc(argv, func(a string) bool { return strings.Contains(a, "QUIL_SBX_TRANSCRIPT") }) {
				if probeGate != nil {
					<-probeGate
				}
				if probeErr != nil {
					return probeErr
				}
				if transcript {
					io.WriteString(w, "QUIL_SBX_TRANSCRIPT\n")
				}
			}
		}
	}
	return nil
}

func (r *scriptRunner) ran(prefix ...string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.calls {
		if len(c) >= len(prefix) && reflect.DeepEqual(c[:len(prefix)], prefix) {
			return true
		}
	}
	return false
}

// sbxFixture builds a daemon, a claude-code sbx pane in a project with a
// profile, and the seams. trust says whether the profile is approved.
func sbxFixture(t *testing.T, trust bool) (*Daemon, *Pane, *scriptRunner) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("QUIL_HOME", home)

	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, sbx.ProfileFile),
		[]byte("[sbx]\nenv_file = \"sbxenv.yaml\"\nname = \"my-app\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "sbxenv.yaml"), []byte("schemaVersion: \"1\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if trust {
		prof, err := sbx.LoadProfile(filepath.Join(repo, sbx.ProfileFile))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := sbx.Trust(sbx.TrustStorePath(home), prof); err != nil {
			t.Fatal(err)
		}
	}

	r := &scriptRunner{}
	prevRunner, prevBin, prevOS := sbxRunner, sbxBinaryFn, sbxHostOSFn
	sbxRunner = r
	sbxBinaryFn = func() (string, error) { return "/fake/sbx", nil }
	// Pinned, so a test of the -w argument and the folder probe asserts the
	// same contract on every host; the Windows branch has its own test.
	sbxHostOSFn = func() string { return "linux" }

	d := &Daemon{session: NewSessionManager(1024), events: newEventQueue(50), cfg: config.Default()}
	withClaudePlugin(t, d)
	// Registered last so it runs first: the preparation goroutine reads the
	// seams, so they are restored only once it has finished.
	t.Cleanup(func() {
		d.sbxPrep.wait()
		sbxRunner, sbxBinaryFn, sbxHostOSFn = prevRunner, prevBin, prevOS
	})

	// Registered with the session manager: the preparation goroutine looks the
	// pane up again by id before it respawns it.
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, repo)
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	pane.Type = "claude-code"
	pane.SandboxBackend = config.SandboxBackendSbx
	return d, pane, r
}

// The refusal that must never soften: no profile means no cage, and the pane
// must not run the agent on the host instead.
func TestSpawnPane_SbxWithoutAProfileRefuses(t *testing.T) {
	d, pane, _ := sbxFixture(t, false)
	pane.CWD = t.TempDir()
	s := &fakeSession{}
	err := d.spawnPane(pane, s, false)
	if err == nil || !strings.Contains(err.Error(), sbx.ProfileFile) {
		t.Fatalf("err = %v, want a refusal naming %s", err, sbx.ProfileFile)
	}
	if s.startCmd != "" {
		t.Errorf("a child was started (%s) for a pane with no cage", s.startCmd)
	}
}

func TestSpawnPane_SbxUntrustedProfileRefuses(t *testing.T) {
	d, pane, r := sbxFixture(t, false)
	s := &fakeSession{}
	err := d.spawnPane(pane, s, false)
	if err == nil || !strings.Contains(err.Error(), "quil sandbox trust") {
		t.Fatalf("err = %v, want a refusal naming the approval command", err)
	}
	if s.startCmd != "" || len(r.calls) != 0 {
		t.Errorf("an unapproved profile reached sbx: cmd=%q calls=%v", s.startCmd, r.calls)
	}
}

// The whole path: stand down, create the sandbox on a goroutine, then spawn
// the pane's own command through `sbx exec`.
func TestSpawnPane_SbxPreparesThenExecs(t *testing.T) {
	d, pane, r := sbxFixture(t, true)

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
		t.Fatalf("spawnPane = %v; standing down while the sandbox is prepared is not a failure", err)
	}
	if first.startCmd != "" {
		t.Fatalf("the first spawn started %q before the sandbox existed", first.startCmd)
	}

	var s *fakeSession
	select {
	case s = <-respawned:
	case <-time.After(5 * time.Second):
		t.Fatal("the pane was never respawned after the sandbox was prepared")
	}
	if !r.ran("/fake/sbx", "env", "create") {
		t.Errorf("the sandbox was not created; calls = %v", r.calls)
	}
	if s.startCmd != "/fake/sbx" {
		t.Errorf("started %q, want the sbx CLI", s.startCmd)
	}
	wd := pane.CWD // the fixture pins a non-Windows host
	p := d.registry.Get("claude-code")
	tag := spawnTag(t, s.startArgs)
	if !strings.HasPrefix(tag, pane.ID+"-") {
		t.Errorf("tag %q does not start with the pane id", tag)
	}
	want := sbx.ExecArgs("my-app", wd, append(slices.Clone(p.Command.Env), sbx.TagEnv+"="+tag), p.Command.Cmd, nil)
	// The plugin's own start arguments are not Quil's to pass into the cage
	// beyond what the plugin declares, so compare only the fixed prefix.
	if len(s.startArgs) < len(want) || !reflect.DeepEqual(s.startArgs[:len(want)], want) {
		t.Errorf("args = %v, want prefix %v", s.startArgs, want)
	}
	if slices.Contains(s.startArgs, "--settings") {
		t.Errorf("an sbx pane was handed --settings, which names a host path: %v", s.startArgs)
	}
	// The session id is Quil's own, kept on the pane so a restart can resume
	// it (sandbox_sbx_resume.go); a profile pane gets one like an attached one.
	if i := slices.Index(s.startArgs, "--session-id"); i < 0 || i+1 >= len(s.startArgs) || s.startArgs[i+1] != pane.PluginState["session_id"] {
		t.Errorf("args = %v, want --session-id %q", s.startArgs, pane.PluginState["session_id"])
	}
}

// An existing sandbox is reused: no create, no host step.
func TestSpawnPane_SbxReusesAnExistingSandbox(t *testing.T) {
	d, pane, r := sbxFixture(t, true)
	r.exists = true

	done := make(chan struct{}, 1)
	prevSpawn := sbxSpawnFn
	sbxSpawnFn = func(d *Daemon, p *Pane, wrap func(apty.Session) apty.Session) error {
		err := d.spawnPane(p, wrap(&fakeSession{}), false)
		done <- struct{}{}
		return err
	}
	t.Cleanup(func() { sbxSpawnFn = prevSpawn })

	if err := d.spawnPane(pane, &fakeSession{}, false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("never respawned")
	}
	if r.ran("/fake/sbx", "env", "create") {
		t.Error("an existing sandbox was created again")
	}
}

// Two panes on one profile share one preparation, and the claim is held until
// no pane is left waiting.
func TestSbxPrepare_SingleFlightPerName(t *testing.T) {
	var s sbxPrepare
	w := func(id string, gen uint64) sbxWaiter { return sbxWaiter{paneID: id, gen: gen} }
	if !s.begin("a", w("p1", 0)) {
		t.Fatal("first caller not owner")
	}
	if s.begin("a", w("p2", 0)) || s.begin("a", w("p2", 0)) || s.begin("a", w("p1", 3)) {
		t.Error("a second caller became owner")
	}
	if !s.begin("b", w("p3", 0)) {
		t.Error("another sandbox name was blocked")
	}
	if got := s.next("a"); !reflect.DeepEqual(got, []sbxWaiter{w("p1", 3), w("p2", 0)}) {
		t.Errorf("next = %v, want each pane once, owner first, with its newest ask", got)
	}
	// Still claimed while those panes are being started: a restart joins.
	if s.begin("a", w("p1", 4)) {
		t.Error("a pane restarted during the start became a second owner")
	}
	if got := s.next("a"); !reflect.DeepEqual(got, []sbxWaiter{w("p1", 4)}) {
		t.Errorf("next = %v, want the pane that joined", got)
	}
	if s.next("a") != nil {
		t.Error("next returned panes when none was waiting")
	}
	if !s.begin("a", w("p4", 0)) {
		t.Error("the claim was not released")
	}
}

// A pane started since it asked — a restart that raced the preparation — is
// not started a second time.
func TestRespawnSbxPane_SkipsAPaneStartedSinceItAsked(t *testing.T) {
	d, pane, _ := sbxFixture(t, true)
	calls := 0
	prevSpawn := sbxSpawnFn
	sbxSpawnFn = func(*Daemon, *Pane, func(apty.Session) apty.Session) error {
		calls++
		return nil
	}
	t.Cleanup(func() { sbxSpawnFn = prevSpawn })

	pane.PluginMu.Lock()
	pane.ptyGen = 2
	pane.PluginMu.Unlock()
	d.respawnSbxPane(sbxWaiter{paneID: pane.ID, gen: 1})
	if calls != 0 {
		t.Fatal("a pane that already has a newer PTY was spawned again")
	}
	d.respawnSbxPane(sbxWaiter{paneID: pane.ID, gen: 2})
	if calls != 1 {
		t.Errorf("spawned %d times, want 1", calls)
	}
}

// Only the preparation's respawn starts the agent. A plain spawn — a restart —
// stands down and joins, even right after a respawn was asked for.
func TestSpawnPane_SbxPlainSpawnNeverStartsTheAgent(t *testing.T) {
	d, pane, _ := sbxFixture(t, true)
	prevSpawn := sbxSpawnFn
	sbxSpawnFn = func(*Daemon, *Pane, func(apty.Session) apty.Session) error { return nil }
	t.Cleanup(func() { sbxSpawnFn = prevSpawn })
	s := &fakeSession{}
	if err := d.spawnPane(pane, s, false); err != nil {
		t.Fatal(err)
	}
	d.sbxPrep.wait() // before the seam is restored
	if s.startCmd != "" {
		t.Errorf("a plain spawn started %q", s.startCmd)
	}
}

// A repository the approved profile does not cover is never shared, whatever
// a pane's git files say.
func TestMountSbxWorktrees_OnlyRepositoriesUnderTheProfile(t *testing.T) {
	d, pane, r := sbxFixture(t, true)
	prof, err := loadSbxProfile(pane.CWD)
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "other")
	for _, dir := range []string{filepath.Join(other, ".git"), gitworktree.WorktreesDir(other)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	pane.PluginMu.Lock()
	pane.CWD = other
	pane.PluginMu.Unlock()
	if failed := d.mountSbxWorktrees(context.Background(), prof, []string{pane.ID}); len(failed) != 0 {
		t.Errorf("failed = %v", failed)
	}
	if r.ran("/fake/sbx", "mount") {
		t.Errorf("shared a repository outside the profile; calls = %v", r.calls)
	}
}

func TestCRLF(t *testing.T) {
	if got := crlf("a\nb\r\nc"); got != "a\r\nb\r\nc" {
		t.Errorf("crlf = %q", got)
	}
}

// gitLayout makes repo a main checkout with one linked worktree in Quil's
// worktrees folder, laid out the way git writes them, and returns the
// worktree. The worktree's own profile names ANOTHER sandbox: a worktree
// pane must use the main checkout's.
func gitLayout(t *testing.T, repo string) string {
	t.Helper()
	admin := filepath.Join(repo, ".git", "worktrees", "feat-x")
	wt := filepath.Join(gitworktree.WorktreesDir(repo), "feat-x")
	for _, dir := range []string{admin, wt} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		filepath.Join(admin, "commondir"):   "../..\n",
		filepath.Join(admin, "gitdir"):      filepath.Join(wt, ".git") + "\n",
		filepath.Join(wt, ".git"):           "gitdir: " + admin + "\n",
		filepath.Join(wt, sbx.ProfileFile):  "[sbx]\nenv_file = \"sbxenv.yaml\"\nname = \"other\"\n",
		filepath.Join(wt, "sbxenv.yaml"):    "schemaVersion: \"1\"\n",
		filepath.Join(repo, ".git", "HEAD"): "ref: refs/heads/main\n",
	}
	for path, body := range files {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return wt
}

// spawnAndCollect runs the first spawn of an sbx pane and returns the session
// of the respawn, or nil when the pane was not respawned.
func spawnAndCollect(t *testing.T, d *Daemon, pane *Pane) *fakeSession {
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
	if err := d.spawnPane(pane, &fakeSession{}, false); err != nil {
		t.Fatalf("spawnPane = %v", err)
	}
	d.sbxPrep.wait()
	select {
	case s := <-respawned:
		return s
	default:
		return nil
	}
}

func TestSpawnPane_SbxWorktreeUsesTheMainSandboxAndSharesTheFolder(t *testing.T) {
	d, pane, r := sbxFixture(t, true)
	repo := pane.CWD
	pane.CWD = gitLayout(t, repo)

	s := spawnAndCollect(t, d, pane)
	if s == nil {
		t.Fatal("the worktree pane was never started")
	}
	if !r.ran("/fake/sbx", "mount", "my-app", gitworktree.WorktreesDir(repo)) {
		t.Errorf("the worktrees folder was not shared; calls = %v", r.calls)
	}
	if !slices.Contains(s.startArgs, "my-app") || slices.Contains(s.startArgs, "other") {
		t.Errorf("args = %v, want the main checkout's sandbox my-app", s.startArgs)
	}
}

func TestSpawnPane_SbxMountFailureStopsOnlyAWorktreePane(t *testing.T) {
	t.Run("worktree pane fails", func(t *testing.T) {
		d, pane, r := sbxFixture(t, true)
		r.mountErr = errors.New("exit status 1")
		pane.CWD = gitLayout(t, pane.CWD)
		if s := spawnAndCollect(t, d, pane); s != nil {
			t.Fatal("a worktree pane started in a sandbox that cannot see its folder")
		}
		pane.PluginMu.Lock()
		msg := pane.SpawnError
		pane.PluginMu.Unlock()
		if !strings.Contains(msg, "worktrees folder") {
			t.Errorf("SpawnError = %q, want the mount failure", msg)
		}
	})
	t.Run("main checkout pane starts", func(t *testing.T) {
		d, pane, r := sbxFixture(t, true)
		r.mountErr = errors.New("exit status 1")
		gitLayout(t, pane.CWD)
		if s := spawnAndCollect(t, d, pane); s == nil {
			t.Fatal("a main checkout pane was stopped by a worktrees mount failure")
		}
	})
}

// No worktrees folder yet: nothing to share, and no sbx mount call.
func TestSpawnPane_SbxNoWorktreesFolderMountsNothing(t *testing.T) {
	d, pane, r := sbxFixture(t, true)
	if err := os.MkdirAll(filepath.Join(pane.CWD, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if s := spawnAndCollect(t, d, pane); s == nil {
		t.Fatal("never started")
	}
	if r.ran("/fake/sbx", "mount") {
		t.Errorf("mounted a folder that does not exist; calls = %v", r.calls)
	}
}

// spawnTag returns the QUIL_SBX_PANE value an sbx exec argv carries.
func spawnTag(t *testing.T, argv []string) string {
	t.Helper()
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "-e" && strings.HasPrefix(argv[i+1], sbx.TagEnv+"=") {
			return strings.TrimPrefix(argv[i+1], sbx.TagEnv+"=")
		}
	}
	t.Fatalf("no %s in %v", sbx.TagEnv, argv)
	return ""
}

// Closing an sbx pane's PTY stops its agent inside the sandbox — exactly this
// spawn's tag, never the whole pane's, so a restart's new agent survives.
func TestSbxSession_CloseStopsThisSpawnsAgent(t *testing.T) {
	d, pane, r := sbxFixture(t, true)
	s := spawnAndCollect(t, d, pane)
	if s == nil {
		t.Fatal("never started")
	}
	tag := spawnTag(t, s.startArgs)
	pane.PluginMu.Lock()
	installed := pane.PTY
	pane.PluginMu.Unlock()
	if installed == nil {
		t.Fatal("no PTY installed")
	}
	installed.Close()
	installed.Close() // a second Close must not stop twice
	d.sbxStops.Wait()

	stop := append([]string{"/fake/sbx"}, sbx.StopArgs("my-app", tag, false)...)
	n := 0
	r.mu.Lock()
	for _, c := range r.calls {
		if reflect.DeepEqual(c, stop) {
			n++
		}
	}
	r.mu.Unlock()
	if n != 1 {
		t.Errorf("exact stop ran %d times, want 1; calls = %v", n, r.calls)
	}
}

// Before a pane starts, the agents of its earlier spawns are stopped — what a
// crashed daemon left behind — and only then is the new agent started.
func TestSpawnPane_SbxStopsLeftoversBeforeStarting(t *testing.T) {
	d, pane, r := sbxFixture(t, true)
	if s := spawnAndCollect(t, d, pane); s == nil {
		t.Fatal("never started")
	}
	// spawnAndCollect's respawn runs after the prepare goroutine's calls, so
	// a recorded prefix stop ran before the new agent started.
	if !r.ran(append([]string{"/fake/sbx"}, sbx.StopArgs("my-app", pane.ID+"-", true)...)...) {
		t.Errorf("leftovers were not stopped; calls = %v", r.calls)
	}
}

// A link above the main checkout keeps a path lexically inside the profile
// while sbx would mount wherever it leads. Containment is decided on real
// paths.
func TestMountSbxWorktrees_LinkAboveTheCheckoutIsNotFollowed(t *testing.T) {
	d, pane, r := sbxFixture(t, true)
	prof, err := loadSbxProfile(pane.CWD)
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	other := filepath.Join(outside, "other")
	for _, dir := range []string{filepath.Join(other, ".git"), gitworktree.WorktreesDir(other)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(prof.Dir, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("cannot create a symbolic link here: %v", err)
	}
	pane.PluginMu.Lock()
	pane.CWD = filepath.Join(link, "other")
	pane.PluginMu.Unlock()
	d.mountSbxWorktrees(context.Background(), prof, []string{pane.ID})
	if r.ran("/fake/sbx", "mount") {
		t.Errorf("followed a link out of the profile; calls = %v", r.calls)
	}
}

// A pane started since it asked is dropped before the leftover stop, which
// would otherwise kill the agent that start made.
func TestStartSbxWaiters_StaleWaiterIsNotStopped(t *testing.T) {
	d, pane, r := sbxFixture(t, true)
	prof, err := loadSbxProfile(pane.CWD)
	if err != nil {
		t.Fatal(err)
	}
	spawned := 0
	prevSpawn := sbxSpawnFn
	sbxSpawnFn = func(*Daemon, *Pane, func(apty.Session) apty.Session) error {
		spawned++
		return nil
	}
	t.Cleanup(func() { sbxSpawnFn = prevSpawn })
	pane.PluginMu.Lock()
	pane.ptyGen = 5
	pane.PluginMu.Unlock()

	d.startSbxWaiters(context.Background(), sbxTarget{name: prof.Name, prof: prof}, sbx.Listed{}, []sbxWaiter{{paneID: pane.ID, gen: 4}})
	if r.ran("/fake/sbx", "exec") || spawned != 0 {
		t.Errorf("a stale waiter was stopped or spawned; calls = %v, spawned = %d", r.calls, spawned)
	}
	d.startSbxWaiters(context.Background(), sbxTarget{name: prof.Name, prof: prof}, sbx.Listed{}, []sbxWaiter{{paneID: pane.ID, gen: 5}})
	if !r.ran(append([]string{"/fake/sbx"}, sbx.StopArgs("my-app", pane.ID+"-", true)...)...) || spawned != 1 {
		t.Errorf("a current waiter was not stopped then spawned; calls = %v, spawned = %d", r.calls, spawned)
	}
}

// The spawn log line never carries an environment value.
func TestSpawnPane_SbxLogRedactsEnvironmentValues(t *testing.T) {
	d, pane, _ := sbxFixture(t, true)
	p := d.registry.Get("claude-code")
	prevEnv := p.Command.Env
	p.Command.Env = append(slices.Clone(prevEnv), "TEST_SECRET=CANARY-sentinel-value")
	t.Cleanup(func() { p.Command.Env = prevEnv })
	// Locked: the pane's output goroutine keeps logging while the test reads.
	logged := &lockedBuffer{}
	prevOut := log.Writer()
	log.SetOutput(logged)
	t.Cleanup(func() { log.SetOutput(prevOut) })

	s := spawnAndCollect(t, d, pane)
	if s == nil {
		t.Fatal("never started")
	}
	if !slices.Contains(s.startArgs, "TEST_SECRET=CANARY-sentinel-value") {
		t.Errorf("the spawn lost the value itself: %v", s.startArgs)
	}
	if strings.Contains(logged.String(), "CANARY-sentinel-value") {
		t.Error("the log carries an environment value")
	}
}

// lockedBuffer is a log sink safe to read while other goroutines write.
type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// sbx not usable on this machine: the pane is told why and does not start,
// and never falls back to the host.
func TestSpawnPane_SbxUnavailableFailsThePane(t *testing.T) {
	d, pane, r := sbxFixture(t, true)
	d.sbxCap.probeFn = func(context.Context) ipc.SandboxCapRespPayload {
		return ipc.SandboxCapRespPayload{SbxError: "sbx not usable: daemon is not running"}
	}
	if s := spawnAndCollect(t, d, pane); s != nil {
		t.Fatalf("a pane started without a usable sbx: %q", s.startCmd)
	}
	pane.PluginMu.Lock()
	msg := pane.SpawnError
	pane.PluginMu.Unlock()
	if !strings.Contains(msg, "daemon is not running") {
		t.Errorf("SpawnError = %q, want sbx's reason", msg)
	}
	if r.ran("/fake/sbx", "env", "create") {
		t.Error("a create ran although sbx is not usable")
	}
}

// A pane whose sandbox another pane is already preparing joins that
// preparation and is told so; it does not start a second one.
func TestSpawnPane_SbxJoinsAPreparationInProgress(t *testing.T) {
	d, pane, _ := sbxFixture(t, true)
	if !d.sbxPrep.begin("my-app", sbxWaiter{paneID: "owner"}) {
		t.Fatal("could not take the claim")
	}
	if err := d.spawnPane(pane, &fakeSession{}, false); err != nil {
		t.Fatal(err)
	}
	waiting := d.sbxPrep.next("my-app")
	if len(waiting) != 2 || waiting[1].paneID != pane.ID {
		t.Fatalf("waiters = %+v, want the owner and this pane", waiting)
	}
	if d.sbxPrep.next("my-app") != nil {
		t.Fatal("the claim was not released")
	}
	if out := string(pane.OutputBuf.Bytes()); !strings.Contains(out, "which another pane is preparing") {
		t.Errorf("the pane was not told it is waiting: %q", out)
	}
}

// A spawn that fails after the preparation leaves its reason on the pane.
func TestRespawnSbxPane_FailureIsRecordedOnThePane(t *testing.T) {
	d, pane, _ := sbxFixture(t, true)
	prevSpawn := sbxSpawnFn
	sbxSpawnFn = func(*Daemon, *Pane, func(apty.Session) apty.Session) error {
		return errors.New("exec format error")
	}
	t.Cleanup(func() { sbxSpawnFn = prevSpawn })
	d.respawnSbxPane(sbxWaiter{paneID: pane.ID})
	pane.PluginMu.Lock()
	msg := pane.SpawnError
	pane.PluginMu.Unlock()
	if msg != "exec format error" {
		t.Errorf("SpawnError = %q", msg)
	}
}

// Nothing is spawned once the daemon is stopping, or for a pane closed while
// its sandbox was prepared.
func TestRespawnSbxPane_NotDuringShutdownOrForAClosedPane(t *testing.T) {
	d, pane, _ := sbxFixture(t, true)
	spawned := 0
	prevSpawn := sbxSpawnFn
	sbxSpawnFn = func(*Daemon, *Pane, func(apty.Session) apty.Session) error {
		spawned++
		return nil
	}
	t.Cleanup(func() { sbxSpawnFn = prevSpawn })
	d.respawnSbxPane(sbxWaiter{paneID: "gone"})
	if d.sbxWaiterCurrent(sbxWaiter{paneID: "gone"}) {
		t.Error("a closed pane counted as waiting")
	}
	d.shutdown = make(chan struct{})
	close(d.shutdown)
	d.respawnSbxPane(sbxWaiter{paneID: pane.ID})
	if spawned != 0 {
		t.Errorf("spawned %d times", spawned)
	}
}

// The pane's own instance arguments reach the agent inside the sandbox, as
// they would on the host.
func TestSpawnPane_SbxPassesInstanceArgs(t *testing.T) {
	d, pane, _ := sbxFixture(t, true)
	pane.InstanceArgs = []string{"--model", "opus"}
	s := spawnAndCollect(t, d, pane)
	if s == nil {
		t.Fatal("never started")
	}
	// After the agent command, and ahead of the session arguments Quil
	// appends (sandbox_sbx_resume.go), as resolveSpawnArgs orders them on
	// the host.
	cmd := d.registry.Get("claude-code").Command.Cmd
	ci := slices.Index(s.startArgs, cmd)
	mi := slices.Index(s.startArgs, "--model")
	si := slices.Index(s.startArgs, "--session-id")
	if ci < 0 || mi < ci || mi+1 >= len(s.startArgs) || s.startArgs[mi+1] != "opus" || si < mi {
		t.Errorf("args = %v, want the instance args after %s and before the session args", s.startArgs, cmd)
	}
}

// failRunner fails every call with output, as an sbx that is installed but
// not working does.
type failRunner struct{ out string }

func (f failRunner) Run(_ context.Context, _ string, w io.Writer, _ ...string) error {
	io.WriteString(w, f.out)
	return errors.New("exit status 1")
}

// A failed probe is reported in one short line, so the dialog can show it.
func TestProbeSbx_ReportsAShortReason(t *testing.T) {
	prevRunner, prevBin := sbxRunner, sbxBinaryFn
	t.Cleanup(func() { sbxRunner, sbxBinaryFn = prevRunner, prevBin })
	sbxBinaryFn = func() (string, error) { return "/fake/sbx", nil }
	sbxRunner = failRunner{out: strings.Repeat("x", 500)}
	got := probeSbx(context.Background())
	if got.SbxAvailable || !strings.HasPrefix(got.SbxError, "sbx not usable: ") || len(got.SbxError) > 260 {
		t.Errorf("probe = %+v", got)
	}
	sbxBinaryFn = func() (string, error) { return "", errors.New("sbx is not installed") }
	if got := probeSbx(context.Background()); got.SbxAvailable || got.SbxError != "sbx is not installed" {
		t.Errorf("probe without sbx = %+v", got)
	}
}
