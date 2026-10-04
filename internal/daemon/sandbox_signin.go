package daemon

import (
	"errors"
	"fmt"
	"log"
	"os"
	"slices"
	"sync"

	"github.com/artyomsv/quil/internal/claudetoken"
	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/plugin"
	"github.com/artyomsv/quil/internal/userenv"
)

// Signing in ON BEHALF of a sandbox pane, so opening one is the whole
// interaction.
//
// Without this the token flow is the default, finds no token, and every pane
// falls through to signing in inside its own container — which the user has to
// do again for every pane, and which no amount of documentation makes not feel
// broken. Telling them to run a command first is the same manual step wearing a
// different hat.

// The two seams that make this drivable without a browser or a `claude` on
// PATH. Package vars for the same reason sandboxMappingFn and runDocker are:
// the interesting failure is "nothing calls this", and a direct-call test
// cannot see that.
var (
	findClaudeFn   = claudetoken.Find
	userenvGet     = userenv.Get
	userenvSetFn   = userenv.Set
	captureTokenFn = claudetoken.Capture
	// signInSpawnFn is NIL by default, and the nil is load-bearing rather than
	// lazy: initialising it to a closure over spawnPane is an initialisation
	// CYCLE, because spawnPane calls beginSandboxSignIn, which reaches this
	// var. Resolving it at call time breaks the cycle without an init().
	signInSpawnFn func(*Daemon, *Pane) error
)

// sandboxSignIn is the daemon-wide single-flight for the sign-in.
//
// Engine-wide rather than per-pane because the thing being guarded is a BROWSER
// WINDOW and a user's attention: opening a tab in a workspace with two sandbox
// panes creates both at once, and two competing OAuth flows is a worse failure
// than a short wait. The waiters do not queue — they are told the sign-in is
// running and left to be respawned by the winner.
type sandboxSignIn struct {
	mu      sync.Mutex
	running bool
	// owner is the pane that started the flight. Held so it cannot also enter
	// its own waiter list — see begin.
	owner string
	// waiting holds the panes that asked while a sign-in was already in
	// flight, so the winner can spawn them too rather than leaving them
	// sitting with a message and no child.
	waiting []string
	// inFlight tracks the sign-in goroutine so a caller can wait for it to
	// finish reading everything it touches.
	//
	// Shutdown deliberately does NOT wait on this: the flight is a human in a
	// browser with a five-minute budget, and holding the stop path open for
	// that would turn every quit during a sign-in into a SIGKILL. The guard
	// against acting late is respawnAfterSignIn's own d.stopping() check.
	//
	// Its real caller is a test. beginSandboxSignIn returns as soon as the
	// goroutine is launched, so a test that stubs captureTokenFn and returns
	// restores that package var while the goroutine is still reading it —
	// a data race that CI's `go test -race ./...` catches and `dev.sh test`
	// does not.
	inFlight sync.WaitGroup
}

// wait blocks until any in-flight sign-in goroutine has finished.
func (s *sandboxSignIn) wait() { s.inFlight.Wait() }

// begin claims the sign-in. It reports whether the caller OWNS it; a caller
// told false has been recorded as a waiter and must not start its own.
//
// A pane is recorded AT MOST ONCE, and never when it is the owner. Alt+R on a
// pane that stood down is an ordinary thing to do — it sits showing "a browser
// window is opening" for minutes — and it re-enters here through
// handleRestartPaneReq → spawnPane. Appending unconditionally meant that pane
// was spawned TWICE when the token landed, and the second prepareSandbox runs
// `docker rm -f` on the pane's own container: it killed the container the
// first respawn had just started, mid-task, and left the first docker CLI and
// its output goroutine leaked behind an overwritten pane.PTY.
func (s *sandboxSignIn) begin(paneID string) (owner bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		if paneID != s.owner && !slices.Contains(s.waiting, paneID) {
			s.waiting = append(s.waiting, paneID)
		}
		return false
	}
	s.running, s.owner = true, paneID
	return true
}

// end releases the claim and hands back the panes that queued behind it.
func (s *sandboxSignIn) end() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running, s.owner = false, ""
	w := s.waiting
	s.waiting = nil
	return w
}

// adoptPersistedToken loads a previously saved token into this process when it
// did not inherit one.
//
// It is what stops the sign-in repeating on every daemon restart, and the
// repeat was not merely annoying: each `claude setup-token` mints a NEW
// long-lived token, so signing in again while other panes are running leaves
// them holding a credential that has just been superseded. Observed three
// times in one session.
//
// The gap is real rather than theoretical. A daemon's environment comes from
// whatever spawned it — normally the TUI — and the TUI's own environment is a
// snapshot from ITS start. A token written to the OS store after that point is
// invisible to both, however many times the daemon is restarted, because
// Windows builds a child's environment from its parent's and never re-reads
// the registry.
//
// Best-effort: a platform with no per-user store answers ErrUnsupported and
// the sign-in runs, which is the pre-existing behaviour.
func (d *Daemon) adoptPersistedToken() {
	if os.Getenv(oauthTokenEnv) != "" {
		return
	}
	v, err := userenvGet(oauthTokenEnv)
	if err != nil || v == "" {
		return
	}
	if err := os.Setenv(oauthTokenEnv, v); err != nil {
		log.Printf("sandbox: adopting the saved %s: %v", oauthTokenEnv, err)
		return
	}
	log.Printf("sandbox: using the %s saved in your user environment", oauthTokenEnv)
}

// sandboxTokenAvailable reports whether a container spawned now would be handed
// a credential.
//
// The SAME condition sandboxIdentity uses to set ForwardOAuthToken, asked one
// step earlier so the config seed can depend on it. They must agree: seeding a
// pane that receives no token suppresses the sign-in screen and leaves the user
// with a prompt that cannot authenticate.
func (d *Daemon) sandboxTokenAvailable(pane *Pane, pluginName string) bool {
	// A codex or opencode container has no use for a Claude token, and seeding
	// its Claude config would answer first-run screens it never shows.
	if !plugin.UsesClaudeAuthName(pluginName) {
		return false
	}
	if d.paneAuthMode(pane) != config.SandboxAuthToken {
		return false
	}
	d.adoptPersistedToken()
	return os.Getenv(oauthTokenEnv) != ""
}

// paneAuthMode is the sign-in mode for ONE pane: its own choice when it made
// one, otherwise the configured default.
//
// The single reader of Pane.SandboxAuth. Everything that decides how a
// container authenticates goes through here, so the dialog's radio and the
// config key cannot disagree about a pane — which would be either a pane that
// cannot authenticate at all, or one that silently loses the model the user
// opened it for.
//
// An unrecognised stored value falls back to the CONFIG rather than to a
// literal, so a snapshot written by a future version does not pin a pane to a
// mode this build does not implement.
func (d *Daemon) paneAuthMode(pane *Pane) config.SandboxAuthMode {
	if pane != nil {
		pane.PluginMu.Lock()
		chosen := pane.SandboxAuth
		pane.PluginMu.Unlock()

		switch config.SandboxAuthMode(chosen) {
		case config.SandboxAuthToken:
			return config.SandboxAuthToken
		case config.SandboxAuthBrowser:
			return config.SandboxAuthBrowser
		}
	}
	mode, _ := d.cfg.Sandbox.ResolveAuth()
	return mode
}

// needsSandboxSignIn reports whether this pane would fall through to the
// per-pane container sign-in, which is the state this flow exists to remove.
func (d *Daemon) needsSandboxSignIn(pane *Pane) bool {
	// The PANE's mode, not the config's: a pane the user opened as a browser
	// pane must sign in inside its own container, and hijacking that with a
	// host token would hand it the "Claude API" credential it was chosen to
	// avoid — no Fable, no Remote Control.
	if d.paneAuthMode(pane) != config.SandboxAuthToken {
		return false
	}
	// Checked BEFORE concluding a sign-in is needed, or a restarted daemon
	// mints a second token and supersedes the one its live panes are using.
	d.adoptPersistedToken()
	return os.Getenv(oauthTokenEnv) == ""
}

// beginSandboxSignIn starts the sign-in for a pane that needs one, and reports
// whether the pane's spawn should stand down.
//
// A true return means the pane has NO CHILD and is waiting: a goroutine will
// respawn it when the token lands. That is a supported pane state — the
// worktree placeholder is the same shape — and it is what keeps this off the
// IPC dispatch goroutine, which must never block. A human authorising in a
// browser is minutes, not milliseconds.
func (d *Daemon) beginSandboxSignIn(pane *Pane, p *plugin.PanePlugin) bool {
	// Only the plugin whose credentials this flow actually mints. See
	// plugin.UsesClaudeAuth.
	if !p.UsesClaudeAuth() {
		return false
	}
	if !d.needsSandboxSignIn(pane) {
		return false
	}
	claude, err := findClaudeFn()
	if err != nil {
		// Nothing to drive, so let the ordinary spawn proceed and fall through
		// to the in-container sign-in. Refusing here would turn "you have no
		// claude on PATH" into a pane that cannot open at all, on a path where
		// the container has its own claude and does not need the host's.
		log.Printf("sandbox: pane %s: cannot sign in for you (%v); the pane will "+
			"ask you to sign in inside the container", pane.ID, err)
		return false
	}

	if !d.sandboxSignIn.begin(pane.ID) {
		d.announce(pane.ID, "Waiting for the Claude sign-in already in progress…\r\n")
		return true
	}

	d.announce(pane.ID, "Signing in to Claude Code.\r\n"+
		"A browser window is opening — click Authorize there.\r\n"+
		"This happens once; every Token sandbox pane after this is signed in.\r\n"+
		"(Browser and Shared panes sign in inside their own container.)\r\n\r\n")

	// Add BEFORE the go statement, never inside it: a Wait racing an Add that
	// has not run yet returns immediately and the handle guarantees nothing.
	d.sandboxSignIn.inFlight.Add(1)
	go func() {
		defer d.sandboxSignIn.inFlight.Done()
		d.runSandboxSignIn(claude, pane.ID)
	}()
	return true
}

// announce is tellPane for a pane the client may not know about yet.
//
// Both spawn paths call spawnPane BEFORE their broadcastState, so a pane_output
// frame emitted from inside a spawn names a pane id no attached client has
// seen — and the client drops it. That is not theoretical: it is why the first
// version of this flow left the user looking at a black rectangle after the
// browser step, with the message sitting in the daemon's buffer.
//
// Broadcasting the state first costs one frame on a path that runs once per
// sign-in, which is nothing next to the alternative of the explanation being
// invisible exactly when it is the only thing on screen.
func (d *Daemon) announce(paneID, text string) {
	d.broadcastState()
	d.tellPane(paneID, text)
}

// runSandboxSignIn does the capture and then spawns everything that was
// waiting on it. Runs on its own goroutine; nothing here may assume it is
// alone with the session.
func (d *Daemon) runSandboxSignIn(claudePath, ownerPaneID string) {
	// Mirror is nil DELIBERATELY: the only mirror available here is a pane,
	// and a pane's output is written to a ghost buffer on disk — so mirroring
	// would persist the token. Progress is reported in our own words instead.
	token, err := captureTokenFn(claudePath, claudetoken.Options{})

	if err != nil {
		// Released HERE on the failure path: nothing was published, so a pane
		// created now SHOULD start its own attempt.
		panes := append([]string{ownerPaneID}, d.sandboxSignIn.end()...)
		log.Printf("sandbox: sign-in failed: %v", err)
		for _, id := range panes {
			d.tellPane(id, fmt.Sprintf("\r\nSign-in did not complete: %v\r\n"+
				"Restart this pane to try again, or set [sandbox] auth = \"browser\" "+
				"to sign in inside each container instead.\r\n", err))
			d.failSandboxSignIn(id, err)
		}
		return
	}

	// The daemon's OWN environment BEFORE the claim is released, and that
	// order is the whole point. Releasing first left a window where the flight
	// was over and the environment was still empty, so a pane created in it
	// passed needsSandboxSignIn, won begin(), and started a SECOND
	// `claude setup-token` — which mints a new long-lived token that
	// supersedes the one the panes about to spawn are holding. That is exactly
	// the failure adoptPersistedToken exists for, observed three times in one
	// session.
	//
	// It is what dockerCLIEnv reads and what every pane spawned from here
	// inherits; the persistent write below is what makes the NEXT daemon start
	// find it.
	if err := os.Setenv(oauthTokenEnv, token); err != nil {
		// Nothing downstream can work without this: every respawn would find
		// an empty environment and start its own sign-in, one per pane.
		// Reported and abandoned rather than half-applied.
		log.Printf("sandbox: sign-in: setting %s failed, abandoning: %v", oauthTokenEnv, err)
		panes := append([]string{ownerPaneID}, d.sandboxSignIn.end()...)
		for _, id := range panes {
			d.tellPane(id, "\r\nSigned in, but the token could not be published to this "+
				"daemon. Restart the pane to try again.\r\n")
			d.failSandboxSignIn(id, err)
		}
		return
	}
	switch err := userenvSetFn(oauthTokenEnv, token); {
	case err == nil:
		log.Printf("sandbox: signed in; %s saved to the user environment", oauthTokenEnv)
	case errors.Is(err, userenv.ErrUnsupported):
		// Not a failure of the sign-in: the token is live in this daemon and
		// every pane works now. It simply will not survive a restart on a
		// platform with no per-user environment store.
		log.Printf("sandbox: signed in; this platform has no per-user environment store, "+
			"so %s lives only in this daemon — export it where the daemon starts to "+
			"make it persist", oauthTokenEnv)
	default:
		log.Printf("sandbox: signed in, but persisting %s failed: %v", oauthTokenEnv, err)
	}

	// Released only now, with the token published: a pane created from here on
	// finds it and never starts a second flight.
	for _, id := range append([]string{ownerPaneID}, d.sandboxSignIn.end()...) {
		d.tellPane(id, "Signed in. Starting the container…\r\n")
		d.respawnAfterSignIn(id)
	}
}

// tellPane writes a line of Quil's own text into a pane, so a pane with no
// child is not a black rectangle.
//
// It goes through the ordinary output path, so it reaches every attached
// client and lands in the pane's buffer like any other output — which is
// exactly why the CHILD's stream is not mirrored here.
func (d *Daemon) tellPane(paneID, text string) {
	d.flushPaneOutput(paneID, []byte(text))
}

// failSandboxSignIn records the failure on the pane so it is visible after the
// message has scrolled, and so a snapshot does not restore a pane that looks
// merely idle.
func (d *Daemon) failSandboxSignIn(paneID string, cause error) {
	pane := d.session.Pane(paneID)
	if pane == nil {
		return // the tab was closed while the browser flow ran
	}
	pane.PluginMu.Lock()
	pane.SpawnError = "sandbox sign-in failed: " + cause.Error()
	pane.PluginMu.Unlock()
	d.broadcastState()
}

// respawnAfterSignIn starts the child for a pane that stood down while the
// sign-in ran.
//
// It re-reads the pane rather than holding one across the browser flow: minutes
// pass, and the tab may be long closed. spawnPane's own sandbox branch runs
// again, and now finds the token.
// stopping reports whether the daemon has been told to stop.
//
// A nil channel answers false, which is what the many tests that build a
// Daemon literal need — they never call Start, so nothing closes it, and a
// receive on nil blocks forever rather than reporting "not stopping".
func (d *Daemon) stopping() bool {
	if d.shutdown == nil {
		return false
	}
	select {
	case <-d.shutdown:
		return true
	default:
		return false
	}
}

func (d *Daemon) respawnAfterSignIn(paneID string) {
	// The sign-in is an untracked goroutine holding a five-minute budget on a
	// human in a browser, so the daemon can be told to stop while it waits.
	// Spawning here after that point starts PTY children behind the final
	// snapshot — processes nothing will record, reap, or find again. The
	// window is small (the stop path's defers), which is exactly why it is
	// worth closing with a read rather than reasoning about.
	if d.stopping() {
		log.Printf("sandbox: pane %s: signed in during shutdown; not spawning", paneID)
		return
	}
	pane := d.session.Pane(paneID)
	if pane == nil {
		return
	}
	spawn := signInSpawnFn
	if spawn == nil {
		spawn = func(d *Daemon, p *Pane) error { return d.spawnPane(p, newSessionFn(0, 0), false) }
	}
	if err := spawn(d, pane); err != nil {
		log.Printf("sandbox: pane %s: spawn after sign-in: %v", paneID, err)
		pane.PluginMu.Lock()
		pane.SpawnError = err.Error()
		pane.PluginMu.Unlock()
	}
	d.broadcastState()
}
