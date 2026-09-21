package daemon

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/claudetoken"
	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/plugin"
	"github.com/artyomsv/quil/internal/sandbox"
)

// tokenFlowConfig is config.Default() with the token flow NAMED.
//
// The shipped default is the browser flow, deliberately — the token flow saves
// a credential into the user's environment that every later process inherits,
// so it is reachable only by asking for it (see config.ResolveAuth). A test
// about token behaviour therefore has to say so. Leaving it implicit is what
// let nine tests here describe "the default" while their names claimed to
// describe the token mode.
func tokenFlowConfig() config.Config {
	c := config.Default()
	c.Sandbox.Auth = string(config.SandboxAuthToken)
	return c
}

func TestNeedsSandboxSignIn(t *testing.T) {
	tests := []struct {
		name  string
		auth  string
		token string
		want  bool
	}{
		{"token flow explicit, no token", "token", "", true},
		{"token flow explicit, token present", "token", "sk-ant-x", false},
		// The default is the per-pane sign-in, so an untouched config must
		// never drive `claude setup-token` on the user's behalf — that writes
		// a credential into their environment which every later process
		// inherits, including the ones running ORDINARY panes.
		{"unset follows the browser default", "", "", false},
		{"unset with a token present still does not", "", "sk-ant-x", false},
		// The user chose the per-pane sign-in. Hijacking that with a browser
		// window would override an explicit decision.
		{"browser chosen, no token", "browser", "", false},
		{"browser chosen, token present", "browser", "sk-ant-x", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(oauthTokenEnv, tt.token)
			// Without this, adoptPersistedToken reads the DEVELOPER's own HKCU
			// token on Windows and every "no token → want true" row fails.
			// Invisible on Linux CI, which has no per-user store at all.
			stubNoSavedToken(t)
			d := &Daemon{cfg: config.Default()}
			d.cfg.Sandbox.Auth = tt.auth
			if got := d.needsSandboxSignIn(nil); got != tt.want {
				t.Errorf("needsSandboxSignIn = %v, want %v", got, tt.want)
			}
		})
	}
}

// Two panes created at once must not each open a browser. The loser stands
// down and is recorded, so the winner spawns it too — a waiter that is merely
// refused would sit with a message and never get a child.
func TestSandboxSignIn_SingleFlightRecordsWaiters(t *testing.T) {
	var s sandboxSignIn

	if !s.begin("pane-1") {
		t.Fatal("the first caller did not get ownership")
	}
	if s.begin("pane-2") {
		t.Error("a second caller also got ownership; two browser flows would open")
	}
	if s.begin("pane-3") {
		t.Error("a third caller also got ownership")
	}

	waiters := s.end()
	if len(waiters) != 2 || waiters[0] != "pane-2" || waiters[1] != "pane-3" {
		t.Errorf("waiters = %v, want both panes that stood down — otherwise they never spawn", waiters)
	}

	// end() must also release the claim, or the next sign-in can never start.
	if !s.begin("pane-4") {
		t.Error("ownership was not released")
	}
	if w := s.end(); len(w) != 0 {
		t.Errorf("waiters = %v after a fresh round; the list was not cleared", w)
	}
}

// --- the on switch ---
//
// Everything above tests the sign-in's LOGIC. None of it reaches the wiring,
// and the wiring is where this feature has broken four separate times.
// Disabling the call in spawnPane leaves every logic test green while a user
// opening a sandbox pane is asked to sign in inside the container again — the
// exact state the flow exists to remove.

// The real spawn path must hand a token-less sandbox pane to the sign-in,
// stand down, and leave the pane childless for the goroutine to spawn.
func TestSpawnPane_SandboxWithNoTokenStartsTheSignIn(t *testing.T) {
	d, pane, _ := sandboxCallsiteFixture(t)
	withClaudePlugin(t, d)
	pane.Type = "claude-code"
	t.Setenv(oauthTokenEnv, "")
	d.cfg = tokenFlowConfig()
	// Stubbed like every sibling in this file: without it adoptPersistedToken
	// reads the DEVELOPER's own HKCU token on Windows, concludes no sign-in is
	// needed, and this test fails deterministically — invisible on Linux CI,
	// where there is no per-user store to read.
	stubNoSavedToken(t)

	prevFind := findClaudeFn
	findClaudeFn = func() (string, error) { return "/fake/claude", nil }
	t.Cleanup(func() { findClaudeFn = prevFind })

	captured := make(chan string, 1)
	prevCap := captureTokenFn
	captureTokenFn = func(path string, _ claudetoken.Options) (string, error) {
		captured <- path
		return "", errors.New("stopped in the test")
	}
	t.Cleanup(func() { captureTokenFn = prevCap })

	// Available, so the spawn reaches the sign-in branch rather than refusing.
	prevProbe := sandboxProbeFn
	sandboxProbeFn = func(context.Context) (sandbox.Info, error) {
		return sandbox.Info{ServerVersion: "1", OSType: "linux"}, nil
	}
	t.Cleanup(func() { sandboxProbeFn = prevProbe })

	if err := d.spawnPane(pane, &fakeSpawnSession{}, false); err != nil {
		t.Fatalf("spawnPane = %v; standing down for a sign-in is not a failure", err)
	}

	select {
	case path := <-captured:
		if path != "/fake/claude" {
			t.Errorf("capture ran with %q, want the located claude", path)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the sign-in never started — a sandbox pane with no token asked the " +
			"user to sign in inside the container instead")
	}
}

// A pane that already has a token must take the ordinary path untouched, or
// every sandbox pane pays for a browser flow it does not need.
func TestSpawnPane_SandboxWithATokenSkipsTheSignIn(t *testing.T) {
	d, pane, _ := sandboxCallsiteFixture(t)
	withClaudePlugin(t, d)
	pane.Type = "claude-code"
	t.Setenv(oauthTokenEnv, "sk-ant-already-signed-in")
	d.cfg = config.Default()

	prevFind := findClaudeFn
	findClaudeFn = func() (string, error) {
		t.Error("the sign-in was started for a pane that already has a token")
		return "", errors.New("should not be reached")
	}
	t.Cleanup(func() { findClaudeFn = prevFind })

	if d.beginSandboxSignIn(pane, &plugin.PanePlugin{Name: "claude-code"}) {
		t.Error("beginSandboxSignIn took ownership with a token already present")
	}
}

// A restarted daemon must USE the saved token, not mint a new one.
//
// Its environment comes from whatever spawned it — normally the TUI — and the
// TUI's own environment is a snapshot from ITS start, so a token saved after
// that point is invisible to both. The sign-in therefore ran again on every
// restart, and each `claude setup-token` mints a NEW long-lived token, which
// supersedes the one live panes are already using. Observed three times in one
// session, with a running pane losing its credential.
func TestNeedsSandboxSignIn_AdoptsTheSavedToken(t *testing.T) {
	t.Setenv(oauthTokenEnv, "") // as a freshly spawned daemon has it

	prevGet := userenvGet
	userenvGet = func(string) (string, error) { return "sk-ant-saved-earlier", nil }
	t.Cleanup(func() { userenvGet = prevGet })

	d := &Daemon{cfg: tokenFlowConfig()}
	if d.needsSandboxSignIn(nil) {
		t.Error("a second sign-in was started while a saved token was available — " +
			"the new token supersedes the one running panes are using")
	}
	if os.Getenv(oauthTokenEnv) == "" {
		t.Error("the saved token was not adopted into the process environment, so " +
			"dockerCLIEnv has nothing to hand docker")
	}
}

// With nothing saved anywhere the sign-in must still run, or the feature is off.
func TestNeedsSandboxSignIn_NoSavedTokenStillSignsIn(t *testing.T) {
	t.Setenv(oauthTokenEnv, "")

	prevGet := userenvGet
	userenvGet = func(string) (string, error) { return "", nil }
	t.Cleanup(func() { userenvGet = prevGet })

	d := &Daemon{cfg: tokenFlowConfig()}
	if !d.needsSandboxSignIn(nil) {
		t.Error("no sign-in was started with no token anywhere")
	}
}

// The pane's OWN choice must beat the config, or the dialog's radio is
// decoration. Both directions, because each failure is different: a browser
// pane hijacked by a host token silently loses Fable and Remote Control, and a
// token pane forced to sign in is the per-pane login the choice exists to
// avoid.
func TestPaneAuthMode_ThePaneChoiceBeatsTheConfig(t *testing.T) {
	tests := []struct {
		name       string
		configAuth string
		paneAuth   string
		want       config.SandboxAuthMode
	}{
		{"pane picks browser against a token config", "token", "browser", config.SandboxAuthBrowser},
		{"pane picks token against a browser config", "browser", "token", config.SandboxAuthToken},
		{"untouched pane follows the config", "browser", "", config.SandboxAuthBrowser},
		{"untouched pane follows the config, token", "token", "", config.SandboxAuthToken},
		{"untouched pane and untouched config get the browser default", "", "", config.SandboxAuthBrowser},
		// A snapshot from a future version must not pin a pane to a mode this
		// build cannot honour.
		{"unknown stored value falls back to the config", "browser", "future-mode", config.SandboxAuthBrowser},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &Daemon{cfg: config.Default()}
			d.cfg.Sandbox.Auth = tt.configAuth
			pane := &Pane{ID: "p1", SandboxAuth: tt.paneAuth}
			if got := d.paneAuthMode(pane); got != tt.want {
				t.Errorf("paneAuthMode = %q, want %q", got, tt.want)
			}
		})
	}
}

// A browser pane must NOT be handed the host token: that is the credential it
// was chosen to avoid.
func TestNeedsSandboxSignIn_ABrowserPaneNeverUsesTheHostToken(t *testing.T) {
	t.Setenv(oauthTokenEnv, "")
	d := &Daemon{cfg: tokenFlowConfig()} // the host token IS available here
	pane := &Pane{ID: "p1", SandboxAuth: "browser"}

	if d.needsSandboxSignIn(pane) {
		t.Error("a browser pane started the HOST sign-in; it must sign in inside its own container")
	}
	if d.sandboxTokenAvailable(pane, "claude-code") {
		t.Error("a browser pane reports a token available; its config would be seeded and " +
			"the in-container sign-in screen hidden")
	}
}

// And a token pane still gets the automated sign-in even when the config says
// browser, or picking "Token" in the dialog buys nothing.
func TestNeedsSandboxSignIn_ATokenPaneSignsInAgainstABrowserConfig(t *testing.T) {
	t.Setenv(oauthTokenEnv, "")
	prevGet := userenvGet
	userenvGet = func(string) (string, error) { return "", nil }
	t.Cleanup(func() { userenvGet = prevGet })

	d := &Daemon{cfg: config.Default()}
	d.cfg.Sandbox.Auth = "browser"
	pane := &Pane{ID: "p1", SandboxAuth: "token"}

	if !d.needsSandboxSignIn(pane) {
		t.Error("a token pane did not start the sign-in; the dialog's choice was ignored")
	}
}

// applySandboxSpec is the ONE place a wire spec becomes pane state, and it
// must record the auth as well as the image. Dropping it here is invisible
// everywhere else: the pane simply follows the config, so the dialog's radio
// silently does nothing.
func TestApplySandboxSpec_RecordsTheAuthChoice(t *testing.T) {
	tests := []struct {
		name, wire, want string
	}{
		{"browser is recorded", "browser", "browser"},
		{"token is recorded", "token", "token"},
		{"absent follows the config", "", ""},
		// Any IPC client can set this, and the two modes hand the container
		// different credentials — so an unknown value is dropped to "follow
		// the config" rather than stored and acted on.
		{"unknown is refused", "superuser", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pane := &Pane{ID: "p1"}
			if err := applySandboxSpec(pane, &ipc.SandboxSpec{Image: "img:1", Auth: tt.wire}); err != nil {
				t.Fatalf("applySandboxSpec: %v", err)
			}
			pane.PluginMu.Lock()
			got := pane.SandboxAuth
			pane.PluginMu.Unlock()
			if got != tt.want {
				t.Errorf("SandboxAuth = %q, want %q — the dialog's choice never reaches the spawn", got, tt.want)
			}
		})
	}
}

// The Claude sign-in must never fire for another vendor's agent.
//
// It runs `claude setup-token` and opens a browser. Codex keeps its own
// ~/.codex credentials and opencode its own again, so for those panes this is
// a browser window for an account the user did not ask about — and it shipped
// that way: beginSandboxSignIn took no plugin at all.
func TestBeginSandboxSignIn_OnlyForClaudeCode(t *testing.T) {
	t.Setenv(oauthTokenEnv, "")
	prevGet := userenvGet
	userenvGet = func(string) (string, error) { return "", nil }
	t.Cleanup(func() { userenvGet = prevGet })

	prevFind := findClaudeFn
	findClaudeFn = func() (string, error) { return "/fake/claude", nil }
	t.Cleanup(func() { findClaudeFn = prevFind })

	for _, name := range []string{"codex", "opencode", "terminal"} {
		t.Run(name, func(t *testing.T) {
			d := &Daemon{cfg: config.Default(), session: NewSessionManager(1024), events: newEventQueue(50)}
			pane := &Pane{ID: "p1", SandboxImage: "img"}
			if d.beginSandboxSignIn(pane, &plugin.PanePlugin{Name: name}) {
				t.Errorf("a %s pane started the CLAUDE sign-in — a browser window for "+
					"an account it does not use", name)
			}
		})
	}
}

// And it must still fire for claude-code, or the gate turned the feature off.
func TestBeginSandboxSignIn_StillFiresForClaudeCode(t *testing.T) {
	t.Setenv(oauthTokenEnv, "")
	prevGet := userenvGet
	userenvGet = func(string) (string, error) { return "", nil }
	t.Cleanup(func() { userenvGet = prevGet })

	prevFind := findClaudeFn
	findClaudeFn = func() (string, error) { return "/fake/claude", nil }
	t.Cleanup(func() { findClaudeFn = prevFind })

	prevCap := captureTokenFn
	captureTokenFn = func(string, claudetoken.Options) (string, error) {
		return "", errors.New("stopped in the test")
	}
	t.Cleanup(func() { captureTokenFn = prevCap })

	d := &Daemon{cfg: tokenFlowConfig(), session: NewSessionManager(1024), events: newEventQueue(50)}
	// Registered BEFORE the call, so it runs AFTER the cleanups above are
	// queued and therefore BEFORE them (t.Cleanup is LIFO). The sign-in runs
	// on its own goroutine and reads captureTokenFn there, so returning
	// without waiting lets the restore above race that read — which is a
	// failure only under `go test -race`, i.e. only in CI.
	t.Cleanup(d.sandboxSignIn.wait)

	pane := &Pane{ID: "p1", SandboxImage: "img"}
	if !d.beginSandboxSignIn(pane, &plugin.PanePlugin{Name: "claude-code"}) {
		t.Error("the sign-in no longer fires for claude-code")
	}
}

// A codex or opencode container must not be handed a Claude token, and its
// Claude config must not be seeded — those first-run screens are not the ones
// it shows.
func TestSandboxTokenAvailable_OnlyForClaudeCode(t *testing.T) {
	t.Setenv(oauthTokenEnv, "sk-ant-present")
	d := &Daemon{cfg: tokenFlowConfig()}
	pane := &Pane{ID: "p1"}

	if !d.sandboxTokenAvailable(pane, "claude-code") {
		t.Error("claude-code lost its token")
	}
	for _, name := range []string{"codex", "opencode"} {
		if d.sandboxTokenAvailable(pane, name) {
			t.Errorf("a %s pane reports a Claude token available", name)
		}
	}
}

// A pane that asks TWICE must be spawned once.
//
// Alt+R on a pane standing down for a sign-in is ordinary — it shows "a
// browser window is opening" for minutes — and re-enters through
// handleRestartPaneReq → spawnPane. Recorded twice, it was respawned twice
// when the token landed, and the second prepareSandbox runs `docker rm -f` on
// the pane's own container: it killed the container the first respawn had just
// started, mid-task, and leaked that docker CLI behind an overwritten PTY.
func TestSandboxSignIn_APaneIsNeverRecordedTwice(t *testing.T) {
	var s sandboxSignIn

	if !s.begin("owner") {
		t.Fatal("the first caller did not get ownership")
	}
	// The owner asking again — Alt+R on the pane that started the flight.
	s.begin("owner")
	s.begin("waiter")
	s.begin("waiter") // and a waiter asking again
	s.begin("owner")

	waiters := s.end()
	if len(waiters) != 1 || waiters[0] != "waiter" {
		t.Errorf("waiters = %v, want exactly [waiter] — a duplicate respawn force-removes "+
			"the container the first one started", waiters)
	}
}

// The claim must outlive the token's publication.
//
// Released first, there is a window where the flight is over and the
// environment is still empty: a pane created in it passes needsSandboxSignIn,
// wins begin(), and mints a SECOND long-lived token that supersedes the one
// the panes about to spawn are holding — the failure adoptPersistedToken
// exists for, observed three times in one session.
//
// Observed from inside the PUBLISH, which is the only place the ordering is
// visible: by the time the token is being written, the claim must still be
// held, and the environment must already carry it.
func TestRunSandboxSignIn_ClaimIsHeldUntilTheTokenIsPublished(t *testing.T) {
	t.Setenv(oauthTokenEnv, "")
	prevGet := userenvGet
	userenvGet = func(string) (string, error) { return "", nil }
	t.Cleanup(func() { userenvGet = prevGet })

	d := &Daemon{cfg: config.Default(), session: NewSessionManager(1024), events: newEventQueue(50)}

	var claimHeldAtPublish, envSetAtPublish bool
	prevSet := userenvSetFn
	userenvSetFn = func(string, string) error {
		// If the claim were already released, a newcomer would win it here
		// and start a second `claude setup-token`.
		claimHeldAtPublish = !d.sandboxSignIn.begin("late-pane")
		envSetAtPublish = os.Getenv(oauthTokenEnv) != ""
		return nil
	}
	t.Cleanup(func() { userenvSetFn = prevSet })

	prevCap := captureTokenFn
	captureTokenFn = func(string, claudetoken.Options) (string, error) { return "sk-ant-fresh", nil }
	t.Cleanup(func() { captureTokenFn = prevCap })

	prevSpawn := signInSpawnFn
	signInSpawnFn = func(*Daemon, *Pane) error { return nil }
	t.Cleanup(func() { signInSpawnFn = prevSpawn })

	if !d.sandboxSignIn.begin("owner") {
		t.Fatal("precondition: the owner must hold the claim")
	}
	d.runSandboxSignIn("/fake/claude", "owner")

	if !claimHeldAtPublish {
		t.Error("the claim was released BEFORE the token was published — a pane created in " +
			"that window mints a second token and supersedes the one the spawning panes hold")
	}
	if !envSetAtPublish {
		t.Error("the process environment did not carry the token at publish time")
	}
	// And released afterwards, or no later sign-in could ever start.
	if !d.sandboxSignIn.begin("after") {
		t.Error("the claim was never released")
	}
}

// stubNoSavedToken makes adoptPersistedToken find nothing in the OS store.
//
// Required by every test that asserts a sign-in IS or is NOT needed. Without
// it the code reads the developer's real per-user environment — on Windows,
// HKCU — so a machine where `quil sandbox login` has ever run reports "already
// signed in" and the assertion fails for a reason that has nothing to do with
// the code under test. Linux CI has no such store, which is exactly why this
// class of failure is invisible there.
func stubNoSavedToken(t *testing.T) {
	t.Helper()
	prev := userenvGet
	userenvGet = func(string) (string, error) { return "", nil }
	t.Cleanup(func() { userenvGet = prev })
}

// The sign-in is an untracked goroutine with a five-minute budget on a human in
// a browser, so a daemon told to stop can still be holding one. Spawning after
// that point starts PTY children behind the final snapshot.
func TestRespawnAfterSignIn_DoesNotSpawnDuringShutdown(t *testing.T) {
	d := &Daemon{session: NewSessionManager(1024), events: newEventQueue(10)}
	d.shutdown = make(chan struct{})

	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}

	var spawned int
	prev := signInSpawnFn
	signInSpawnFn = func(*Daemon, *Pane) error { spawned++; return nil }
	t.Cleanup(func() { signInSpawnFn = prev })

	// The control: an open channel means running, and the pane spawns.
	d.respawnAfterSignIn(pane.ID)
	if spawned != 1 {
		t.Fatalf("spawned %d times while running, want 1 — the fixture never reaches the spawn", spawned)
	}

	close(d.shutdown)
	d.respawnAfterSignIn(pane.ID)
	if spawned != 1 {
		t.Errorf("spawned %d times, want 1 — a pane was started after the daemon was told to stop", spawned)
	}
}
