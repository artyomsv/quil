package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/gitworktree"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/plugin"
	apty "github.com/artyomsv/quil/internal/pty"
	"github.com/artyomsv/quil/internal/sbx"
)

// The sbx backend: a sandbox pane inside a Docker Sandboxes microVM.
//
// The docker backend builds its cage itself from the pane's checkout. This one
// does not build anything: the cage is the sbx sandbox a project profile
// (.quil-sandbox.toml) declares, and the profile is pinned by the user's own
// approval (`quil sandbox trust`). So the spawn is three steps:
//
//  1. Find and load the profile, and refuse it unless its digest is the
//     approved one. Synchronous — local file reads only.
//  2. Make sure the named sandbox exists — reuse it, or run the host step,
//     `sbx env create` and the set-up steps. MINUTES, so it runs on a
//     goroutine while the pane stands down, exactly like the sandbox sign-in:
//     the IPC dispatch goroutine must never block. Its output is written into
//     the pane so the user sees what ran and why it failed.
//  3. Start the agent as `sbx exec -it [-w <cwd>] <name> <cmd> <args>`.
//
// Why `sbx exec` and not `sbx run --name`: run attaches whatever agent the
// sandbox was created with, with arguments Quil cannot set. exec runs the
// pane's OWN plugin command — the same binary and toggles a host pane gets —
// and several panes can share one sandbox, one process each, which is Quil's
// pane model.
//
// What an sbx pane does NOT get, deliberately: Quil's hook integration
// (notifications, work state, session-id tracking of /clear and /resume).
// Those depend on a hook binary and a spool directory mounted into the cage,
// and the mounts belong to the user's environment file, not to Quil. The
// claude session itself IS kept — its id on the host, its transcript in the
// VM — so a restart resumes the conversation; see sandbox_sbx_resume.go.
//
// Closing a pane never removes the sandbox: it is named, shared by every pane
// that names it, and holds whatever the set-up steps installed. Recreating it
// is `sbx rm --force <name>`; the next pane then creates it again.
//
// Attach mode skips steps 1 and 2: a pane with a SandboxName runs in an
// existing sandbox another tool created and owns. Quil checks that sbx lists
// it and that the pane folder is inside a folder it mounts writable, then
// goes straight to step 3. It never creates, mounts into or removes that
// sandbox.

// Seams so the call-site tests need no sbx installed.
var (
	sbxBinaryFn = func() (string, error) { return sbx.Binary(runtime.GOOS) }
	sbxRunner   = sbx.Runner(sbx.ExecRunner{})
	// sbxHostOSFn is the daemon's OS, for the two places a host path cannot
	// be handed to the Linux VM: the exec's -w and the attach folder probe.
	sbxHostOSFn = func() string { return runtime.GOOS }
	// sbxSpawnFn is nil by default and resolved at call time, for the reason
	// signInSpawnFn is: a closure over spawnPane here is an initialisation
	// cycle, because spawnPane reaches this file.
	sbxSpawnFn func(d *Daemon, p *Pane, wrap func(apty.Session) apty.Session) error
)

// applySbxSpec records an sbx create. Everything else about the cage comes from
// the profile or from the existing sandbox, so the spec may carry nothing
// else — an image or a sign-in mode next to backend "sbx" is a confused
// client, refused rather than half-applied.
//
// A Name attaches to an existing sandbox. It reaches sbx argv as a
// positional, so a name sbx could read as a flag is refused here, at the
// boundary, rather than at the spawn.
func applySbxSpec(pane *Pane, spec *ipc.SandboxSpec) error {
	if spec.Image != "" || spec.Auth != "" || spec.ClaudeConfig != "" {
		log.Printf("pane %s: refusing an sbx sandbox spec that also names a docker image or sign-in mode", pane.ID)
		return fmt.Errorf("an sbx sandbox takes no image or sign-in fields")
	}
	if spec.Name != "" && !sbx.ValidName(spec.Name) {
		log.Printf("pane %s: refusing an sbx sandbox name of length %d", pane.ID, len(spec.Name))
		return fmt.Errorf("invalid sbx sandbox name")
	}
	pane.PluginMu.Lock()
	pane.SandboxBackend = config.SandboxBackendSbx
	pane.SandboxImage = ""
	pane.SandboxName = spec.Name
	pane.PluginMu.Unlock()
	return nil
}

// sbxTarget is the sandbox one pane spawn runs in: a profile's, which Quil
// creates when it is missing, or an existing one the pane attaches to, which
// Quil only checks.
type sbxTarget struct {
	name   string
	attach bool
	prof   sbx.Approved // profile mode only
}

// key is the preparation's single-flight key. Attach and profile panes on
// one name never share a preparation: each runs its own checks, and a
// profile preparation may create the sandbox an attach check would refuse.
func (t sbxTarget) key() string {
	if t.attach {
		return "attach:" + t.name
	}
	return t.name
}

// sbxTargetFor resolves the pane's target. A name that is not valid any more
// (a hand-edited snapshot) is refused, never dropped: dropping it would move
// the pane into the profile's sandbox, a cage the user did not pick.
func sbxTargetFor(name, cwd string) (sbxTarget, error) {
	if name != "" {
		if !sbx.ValidName(name) {
			return sbxTarget{}, errors.New("sbx sandbox: the pane names an invalid sandbox")
		}
		return sbxTarget{name: name, attach: true}, nil
	}
	prof, err := loadSbxProfile(cwd)
	if err != nil {
		return sbxTarget{}, err
	}
	return sbxTarget{name: prof.Name, prof: prof}, nil
}

// loadSbxProfile finds the profile for a pane directory (sbx.FindProfileFor),
// loads it, and refuses it unless the user approved exactly these files. The
// answer carries the bytes the approval was checked on, so the preparation
// runs what was approved even if the repository changes meanwhile.
func loadSbxProfile(cwd string) (sbx.Approved, error) {
	path, err := sbx.FindProfileFor(cwd)
	if err != nil {
		if errors.Is(err, sbx.ErrNoProfile) {
			return sbx.Approved{}, fmt.Errorf("sbx sandbox: no %s at or above %s — add one that names your "+
				"sbx environment file (see docs/sandbox-panes.md)", sbx.ProfileFile, cwd)
		}
		return sbx.Approved{}, fmt.Errorf("sbx sandbox: %w", err)
	}
	prof, err := sbx.LoadProfile(path)
	if err != nil {
		return sbx.Approved{}, fmt.Errorf("sbx sandbox: %w", err)
	}
	approved, err := sbx.Approve(sbx.TrustStorePath(config.QuilDir()), prof)
	if err != nil {
		return sbx.Approved{}, fmt.Errorf("sbx sandbox: %w", err)
	}
	return approved, nil
}

// startSbxPane is spawnPane's sbx branch. It reports whether it STARTED the
// pane's child; false with a nil error means the pane stood down while its
// sandbox is prepared, and a goroutine will spawn it again. A started pane
// gets back the session to install: ptySession wrapped so that closing it
// also stops the agent inside the sandbox.
func (d *Daemon) startSbxPane(pane *Pane, ptySession apty.Session, p *plugin.PanePlugin) (apty.Session, bool, error) {
	// Only the preparation's own respawn may start the agent, and it says so
	// on the session it passes, never on the pane: a flag on the pane could
	// be taken by a restart racing the respawn, leaving two spawns running.
	rs, ready := ptySession.(*sbxReadySession)
	if ready {
		ptySession = rs.Session
	}
	pane.PluginMu.Lock()
	gen := pane.ptyGen
	cwd := pane.CWD
	name := pane.SandboxName
	args := append([]string{}, p.Command.Args...)
	if len(pane.InstanceArgs) > 0 {
		args = append([]string{}, pane.InstanceArgs...)
	}
	pane.PluginMu.Unlock()

	// Checked on EVERY spawn, the ready respawn included: the profile names
	// the sandbox, and a profile edited after approval must not be able to
	// point a pane at a different cage. An attached pane names its sandbox
	// itself, and the preparation has just checked it.
	t, err := sbxTargetFor(name, cwd)
	if err != nil {
		return nil, false, err
	}
	if !ready {
		d.beginSbxPrepare(sbxWaiter{paneID: pane.ID, gen: gen}, t)
		return nil, false, nil
	}

	bin, err := sbxBinaryFn()
	if err != nil {
		return nil, false, fmt.Errorf("sbx sandbox: %w", err)
	}
	// The pane's directory is passed as the working directory inside the
	// sandbox, which assumes the environment file mounts the workspace at the
	// SAME absolute path — the sbx convention. Omitted on a Windows host,
	// whose paths mean nothing inside the Linux VM; sbx then starts in the
	// sandbox's primary workspace.
	workdir := cwd
	if sbxHostOSFn() == "windows" {
		workdir = ""
	}
	// The pane's claude session: minted at the first start, resumed after
	// it when the VM still holds its transcript (sandbox_sbx_resume.go).
	// Asked here, on the preparation goroutine, because it is a VM round
	// trip, with the folder and environment the agent itself gets, so the
	// probe looks where the agent will. The pane is then checked again:
	// it may have been closed while the VM answered.
	ctx, cancel := d.sbxPrepareContext()
	defer cancel()
	sessionArgs, err := d.sbxSessionArgs(ctx, pane, p, sbx.Client{Bin: bin, Runner: sbxRunner}, t.name, workdir, p.Command.Env, args)
	if err != nil {
		return nil, false, fmt.Errorf("sbx sandbox: %w", err)
	}
	if err := d.sbxSpawnStillWanted(pane, gen); err != nil {
		return nil, false, fmt.Errorf("sbx sandbox: %w", err)
	}
	args = append(args, sessionArgs...)
	// A fresh tag per spawn: closing this session stops exactly the
	// processes carrying it (see sbx.TagEnv).
	tag := sbx.NewTag(pane.ID)
	env := append(slices.Clone(p.Command.Env), sbx.TagEnv+"="+tag)
	// Hooks, when the preparation could share them: the settings file is
	// written into the shared tree and named by the same path, which is
	// where claude reads it inside the VM (the sbx convention). First in
	// argv, as on the host.
	if h, ok := d.sbxHooksFor(pane.ID); ok && p.UsesClaudeSessions() {
		hp := hookPaths{HostDir: h.tree, RefDir: h.tree, ExePath: h.quild}
		settingsArgs, hookEnv := claudeHookSpawnPrep(hp, pane.ID, d.cfg.Notification.Hooks.Claude, args)
		args = append(settingsArgs, args...)
		env = append(env, hookEnv...)
	}
	argv := sbx.ExecArgs(t.name, workdir, env, p.Command.Cmd, args)
	ptySession.SetCWD(cwd)
	log.Printf("spawn: pane %s sbx sandbox=%s cmd=%s args=%v cwd=%s", pane.ID, t.name, bin, sbx.RedactEnv(argv), cwd)
	if err := ptySession.Start(bin, argv...); err != nil {
		return ptySession, true, err
	}
	return &sbxSession{Session: ptySession, name: t.name, stop: func() { d.stopSbxTagAsync(t.name, tag) }}, true, nil
}

// sbxSession is an sbx pane's PTY. Ending the host `sbx exec` does not end
// the agent it started inside the VM, so Close also stops the processes
// carrying this spawn's tag. Every way a pane loses its process — close,
// replace, restart, daemon stop — goes through Close.
type sbxSession struct {
	apty.Session
	name string // the sandbox the agent runs in
	once sync.Once
	stop func()
}

func (s *sbxSession) Close() error {
	err := s.Session.Close()
	s.once.Do(s.stop)
	return err
}

// sbxStopWait bounds how long daemon shutdown waits for those stops.
const sbxStopWait = 5 * time.Second

// stopSbxTagAsync stops one spawn's agent on a goroutine: Close is called on
// paths that must not wait several seconds for a VM round trip.
func (d *Daemon) stopSbxTagAsync(name, tag string) {
	d.sbxStops.Add(1)
	go func() {
		defer d.sbxStops.Done()
		bin, err := sbxBinaryFn()
		if err != nil {
			return
		}
		if err := (sbx.Client{Bin: bin, Runner: sbxRunner}).Stop(context.Background(), name, tag, false); err != nil {
			log.Printf("%v", err)
			return
		}
		log.Printf("sbx: sandbox %s: stopped the agent of %s", name, tag)
	}()
}

// waitSbxStops waits for in-flight stops, at most limit.
func (d *Daemon) waitSbxStops(limit time.Duration) {
	done := make(chan struct{})
	go func() {
		d.sbxStops.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(limit):
		log.Printf("sbx: shutdown did not wait for every agent stop to finish")
	}
}

// stopSbxLeftovers stops any earlier spawn of these panes still running in
// the sandbox, before they start again. A daemon that crashed or was killed
// never ran Close, so its panes' agents are still inside; the next start of
// the same pane finds them by the pane's tag prefix. The new spawn's tag is
// minted after this, so it cannot match.
func (d *Daemon) stopSbxLeftovers(ctx context.Context, name string, panes []string) {
	bin, err := sbxBinaryFn()
	if err != nil {
		return
	}
	c := sbx.Client{Bin: bin, Runner: sbxRunner}
	for _, id := range panes {
		if err := c.Stop(ctx, name, sbx.TagPrefix(id), true); err != nil {
			log.Printf("%v", err)
		}
	}
}

// sbxPrepare is the per-sandbox single-flight. Keyed by sandbox NAME
// (sbxTarget.key), because that is the resource: two panes on one profile
// must share one create.
//
// The claim is held until the LAST pane is started, not only until the
// sandbox exists: a pane restarted while the others are still being mounted
// or started joins the running preparation instead of starting a second one,
// which would spawn the same pane twice.
type sbxPrepare struct {
	mu sync.Mutex
	// panes holds, per sandbox name in preparation, the panes waiting to be
	// started, the owner first. A pane is recorded once — Alt+R on a waiting
	// pane re-enters here, and a second entry would spawn it twice.
	panes map[string][]sbxWaiter
	// inFlight lets a test wait for the goroutine; shutdown does not.
	inFlight sync.WaitGroup
}

// sbxWaiter is a pane waiting for its sandbox, with the PTY generation it had
// when it asked. A pane that got a new PTY since then was started by someone
// else, and is not started again.
type sbxWaiter struct {
	paneID string
	gen    uint64
}

func (s *sbxPrepare) begin(name string, w sbxWaiter) (owner bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.panes == nil {
		s.panes = map[string][]sbxWaiter{}
	}
	list, ok := s.panes[name]
	if !ok {
		s.panes[name] = []sbxWaiter{w}
		return true
	}
	for i := range list {
		if list[i].paneID == w.paneID {
			list[i].gen = w.gen // the newer ask is the one to answer
			return false
		}
	}
	s.panes[name] = append(list, w)
	return false
}

// next takes the panes waiting on name. When none is waiting it releases the
// claim and returns nil; the caller loops until then.
func (s *sbxPrepare) next(name string) []sbxWaiter {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.panes[name]
	if len(list) == 0 {
		delete(s.panes, name)
		return nil
	}
	s.panes[name] = []sbxWaiter{}
	return list
}

func (s *sbxPrepare) wait() { s.inFlight.Wait() }

// beginSbxPrepare starts (or joins) the preparation of the pane's sandbox.
func (d *Daemon) beginSbxPrepare(w sbxWaiter, t sbxTarget) {
	if !d.sbxPrep.begin(t.key(), w) {
		d.announce(w.paneID, fmt.Sprintf("Waiting for sbx sandbox %s, which another pane is preparing…\r\n", t.name))
		return
	}
	if t.attach {
		d.announce(w.paneID, fmt.Sprintf("Attaching to the existing sbx sandbox %s…\r\n", t.name))
	} else {
		d.announce(w.paneID, fmt.Sprintf("Preparing sbx sandbox %s (profile %s)…\r\n", t.name, t.prof.Path))
	}
	d.sbxPrep.inFlight.Add(1)
	go func() {
		defer d.sbxPrep.inFlight.Done()
		d.runSbxPrepare(w.paneID, t)
	}()
}

// runSbxPrepare makes the sandbox exist (or, attaching, checks that it
// does), then starts every pane that waited — including any that asked while
// the others were being started.
func (d *Daemon) runSbxPrepare(ownerID string, t sbxTarget) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if d.shutdown != nil {
		go func() {
			select {
			case <-d.shutdown:
				cancel()
			case <-ctx.Done():
			}
		}()
	}

	var listed sbx.Listed
	var err error
	if t.attach {
		listed, err = d.findSbxSandbox(ctx, t.name)
	} else {
		err = d.ensureSbxSandbox(ctx, ownerID, t.prof)
	}
	if err != nil {
		log.Printf("sbx: sandbox %s: %v", t.name, err)
	}
	for {
		waiters := d.sbxPrep.next(t.key())
		if waiters == nil {
			return
		}
		if err != nil {
			for _, w := range waiters {
				d.failSbxPrepare(w.paneID, err)
			}
			continue
		}
		d.startSbxWaiters(ctx, t, listed, waiters)
	}
}

// startSbxWaiters shares the panes' worktrees folders (profile mode) or
// checks each pane's folder is one the sandbox mounts (attach mode), stops
// what earlier spawns of them left running, and starts them.
func (d *Daemon) startSbxWaiters(ctx context.Context, t sbxTarget, listed sbx.Listed, waiters []sbxWaiter) {
	// A pane started since it asked is dropped HERE, before the leftover
	// stop: its prefix stop would kill the agent that start just made.
	waiters = slices.DeleteFunc(waiters, func(w sbxWaiter) bool { return !d.sbxWaiterCurrent(w) })
	ids := make([]string, len(waiters))
	for i, w := range waiters {
		ids[i] = w.paneID
	}
	var failed map[string]error
	if t.attach {
		// Never a mount: the sandbox belongs to another tool, and Quil does
		// not widen a cage it did not build.
		failed = d.checkSbxAttach(ctx, listed, ids)
	} else {
		failed = d.mountSbxWorktrees(ctx, t.prof, ids)
	}
	var starting []sbxWaiter
	var startingIDs []string
	for _, w := range waiters {
		if err := failed[w.paneID]; err != nil {
			d.failSbxPrepare(w.paneID, err)
			continue
		}
		starting = append(starting, w)
		startingIDs = append(startingIDs, w.paneID)
	}
	// Claude hooks: the shared tree and the hook binary, for the claude
	// panes about to start (sandbox_sbx_hooks.go). Never a failure.
	if want := d.sbxPanesWantingHooks(startingIDs); len(want) > 0 {
		if bin, err := sbxBinaryFn(); err == nil {
			d.prepareSbxHooks(ctx, sbx.Client{Bin: bin, Runner: sbxRunner}, t.name, want)
		}
	}
	// Safe in a sandbox Quil does not own: the stop matches only processes
	// carrying this pane's own tag.
	d.stopSbxLeftovers(ctx, t.name, startingIDs)
	for _, w := range starting {
		d.respawnSbxPane(w)
	}
}

// findSbxSandbox checks sbx can run here and that the named sandbox exists.
// Nothing is created: a missing sandbox is the owning tool's to make.
func (d *Daemon) findSbxSandbox(ctx context.Context, name string) (sbx.Listed, error) {
	if answer := d.sbxCapGet(ctx); !answer.SbxAvailable {
		if answer.SbxError == "" {
			return sbx.Listed{}, errors.New("sbx check did not finish")
		}
		return sbx.Listed{}, errors.New(answer.SbxError)
	}
	bin, err := sbxBinaryFn()
	if err != nil {
		return sbx.Listed{}, err
	}
	list, err := sbx.Client{Bin: bin, Runner: sbxRunner}.ListSandboxes(ctx)
	if err != nil {
		return sbx.Listed{}, err
	}
	return sbx.FindListed(list, name)
}

// checkSbxAttach returns the panes whose folder is not a writable folder of
// the sandbox. `sbx exec -w` would otherwise start the agent somewhere the
// user did not open the pane, or fail inside the VM with a less useful
// message. `sbx ls` is asked first; when it says no, the sandbox itself is
// asked, because `sbx ls` does not list a folder added with `sbx mount`.
func (d *Daemon) checkSbxAttach(ctx context.Context, listed sbx.Listed, panes []string) map[string]error {
	failed := map[string]error{}
	var c *sbx.Client
	for _, id := range panes {
		pane := d.session.Pane(id)
		if pane == nil {
			continue
		}
		pane.PluginMu.Lock()
		cwd := pane.CWD
		pane.PluginMu.Unlock()
		lsErr := sbx.CheckAttach(listed, cwd)
		if lsErr == nil {
			continue
		}
		if sbxHostOSFn() == "windows" {
			// A Windows path means nothing inside the Linux VM, so the probe
			// cannot ask about it — and it does not need to: the exec runs
			// without -w there, so the agent starts in the sandbox's main
			// workspace whatever folder the pane names. The cage is the
			// sandbox either way.
			d.tellPane(id, "Note: sbx does not list this folder for sandbox "+listed.Name+
				", and on Windows Quil cannot pass it into the sandbox. The agent starts in the sandbox's main workspace.\r\n")
			continue
		}
		if c == nil {
			bin, err := sbxBinaryFn()
			if err != nil {
				failed[id] = lsErr
				continue
			}
			c = &sbx.Client{Bin: bin, Runner: sbxRunner}
		}
		ok, err := c.WritableInside(ctx, listed.Name, cwd)
		switch {
		case err != nil:
			failed[id] = fmt.Errorf("%w (and the check inside the sandbox failed: %v)", lsErr, err)
		case !ok:
			failed[id] = lsErr
		default:
			log.Printf("sbx: sandbox %s: pane %s: its folder is writable inside the sandbox, though sbx ls does not list it", listed.Name, id)
		}
	}
	return failed
}

func (d *Daemon) failSbxPrepare(paneID string, err error) {
	d.tellPane(paneID, "\r\nThe sbx sandbox could not be prepared: "+crlf(err.Error())+"\r\n"+
		"Fix the cause, then restart this pane (Alt+R).\r\n")
	d.failSbxPane(paneID, err)
}

// mountSbxWorktrees shares each pane's worktrees folder with the sandbox.
//
// Quil puts every worktree of a repository in one sibling folder
// (gitworktree.WorktreesDir), which the user's environment file does not
// know about. Sharing the FOLDER rather than each worktree means a worktree
// made later is visible at once, with no further step. `sbx mount` changes
// the running sandbox in place and is a no-op when repeated, so it runs
// before every spawn: the first pane in a new worktrees folder adds it, and
// panes already running in the sandbox are not disturbed.
//
// Only a repository the approved profile covers is shared: its main checkout
// must be the profile's directory or below it. With FindCheckout requiring
// the main repository to list a linked worktree itself, a pane cannot widen
// the cage to another repository by writing git pointer files.
//
// It returns the panes that cannot start. A mount failure stops only the
// panes whose directory is INSIDE that folder — they cannot work without it;
// a pane in the main checkout is told and starts anyway.
func (d *Daemon) mountSbxWorktrees(ctx context.Context, prof sbx.Approved, panes []string) map[string]error {
	need := map[string][]string{} // folder → panes in that repository
	inside := map[string]bool{}   // pane → its directory is in the folder
	var order []string
	for _, id := range panes {
		pane := d.session.Pane(id)
		if pane == nil {
			continue
		}
		pane.PluginMu.Lock()
		cwd := pane.CWD
		pane.PluginMu.Unlock()
		co, ok := sbx.FindCheckout(cwd)
		if !ok {
			continue
		}
		// Compared and mounted as REAL paths: a link anywhere above the main
		// checkout (/w/app/link -> /elsewhere) keeps a path lexically inside
		// the profile while sbx mounts wherever it leads.
		realMain, err1 := filepath.EvalSymlinks(co.Main)
		realProf, err2 := filepath.EvalSymlinks(prof.Dir)
		if err1 != nil || err2 != nil || !pathWithin(realProf, realMain) {
			continue
		}
		if real, err := filepath.EvalSymlinks(cwd); err == nil {
			cwd = real
		}
		dir := gitworktree.WorktreesDir(realMain)
		// A real folder only. A symlink here would let one link widen the
		// cage to wherever it points, which nobody approved.
		if st, err := os.Lstat(dir); err != nil || !st.IsDir() {
			continue
		}
		if _, seen := need[dir]; !seen {
			order = append(order, dir)
		}
		need[dir] = append(need[dir], id)
		inside[id] = co.Linked && pathWithin(dir, cwd)
	}
	if len(order) == 0 {
		return nil
	}
	bin, err := sbxBinaryFn()
	if err != nil {
		return nil // the ensure step already ran this; it cannot fail here
	}
	c := sbx.Client{Bin: bin, Runner: sbxRunner}
	failed := map[string]error{}
	for _, dir := range order {
		err := c.Mount(ctx, prof.Name, dir)
		if err == nil {
			log.Printf("sbx: sandbox %s: shared %s", prof.Name, dir)
			continue
		}
		log.Printf("sbx: sandbox %s: %v", prof.Name, err)
		for _, id := range need[dir] {
			if inside[id] {
				failed[id] = fmt.Errorf("could not share the worktrees folder with the sandbox: %w", err)
				continue
			}
			d.tellPane(id, "Note: worktrees in "+dir+" are not visible in this sandbox: "+crlf(err.Error())+"\r\n")
		}
	}
	return failed
}

// ensureSbxSandbox checks sbx can run here and makes the sandbox exist,
// writing progress into the owner pane.
func (d *Daemon) ensureSbxSandbox(ctx context.Context, ownerID string, prof sbx.Approved) error {
	if answer := d.sbxCapGet(ctx); !answer.SbxAvailable {
		if answer.SbxError == "" {
			return errors.New("sbx check did not finish")
		}
		return errors.New(answer.SbxError)
	}
	bin, err := sbxBinaryFn()
	if err != nil {
		return err
	}
	c := sbx.Client{Bin: bin, Runner: sbxRunner}
	_, err = c.Ensure(ctx, prof, paneWriter{d: d, paneID: ownerID})
	return err
}

// sbxReadySession marks the session the preparation's respawn passes to
// spawnPane: the sandbox exists, so this spawn starts `sbx exec` instead of
// preparing again. startSbxPane unwraps it.
type sbxReadySession struct{ apty.Session }

// sbxWaiterCurrent reports whether a waiting pane still exists and has had no
// new PTY since it asked.
func (d *Daemon) sbxWaiterCurrent(w sbxWaiter) bool {
	pane := d.session.Pane(w.paneID)
	if pane == nil {
		return false
	}
	pane.PluginMu.Lock()
	defer pane.PluginMu.Unlock()
	if pane.ptyGen != w.gen {
		log.Printf("sbx: pane %s: already started; not starting it again", w.paneID)
		return false
	}
	return true
}

// respawnSbxPane starts the child for a pane whose sandbox is now ready. It
// checks the waiter again: the leftover stop ran in between.
func (d *Daemon) respawnSbxPane(w sbxWaiter) {
	if d.stopping() {
		log.Printf("sbx: pane %s: sandbox ready during shutdown; not spawning", w.paneID)
		return
	}
	pane := d.session.Pane(w.paneID)
	if pane == nil {
		return // closed while the sandbox was prepared
	}
	pane.PluginMu.Lock()
	gen := pane.ptyGen
	cols, rows := pane.Cols, pane.Rows
	pane.PluginMu.Unlock()
	if gen != w.gen {
		// Started since it asked — a restart that raced this preparation.
		log.Printf("sbx: pane %s: already started; not spawning again", w.paneID)
		return
	}

	wrap := func(s apty.Session) apty.Session { return &sbxReadySession{Session: s} }
	spawn := sbxSpawnFn
	if spawn == nil {
		spawn = func(d *Daemon, p *Pane, wrap func(apty.Session) apty.Session) error {
			return d.spawnPane(p, wrap(newSessionFn(cols, rows)), false)
		}
	}
	if err := spawn(d, pane, wrap); err != nil {
		log.Printf("sbx: pane %s: spawn after preparation: %v", w.paneID, err)
		pane.PluginMu.Lock()
		pane.SpawnError = err.Error()
		pane.PluginMu.Unlock()
	}
	d.broadcastState()
}

// failSbxPane records a preparation failure on the pane, so the reason stays
// visible after the output has scrolled.
func (d *Daemon) failSbxPane(paneID string, cause error) {
	pane := d.session.Pane(paneID)
	if pane == nil {
		return
	}
	pane.PluginMu.Lock()
	pane.SpawnError = cause.Error()
	pane.PluginMu.Unlock()
	d.broadcastState()
}

// paneWriter writes a step's output into a pane as terminal text.
type paneWriter struct {
	d      *Daemon
	paneID string
}

func (w paneWriter) Write(b []byte) (int, error) {
	w.d.tellPane(w.paneID, crlf(string(b)))
	return len(b), nil
}

// crlf turns bare line feeds into CR LF, so command output written into a
// pane starts each line at column 0.
func crlf(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}

// probeSbx answers the sbx half of the capability question.
func probeSbx(ctx context.Context) ipc.SandboxCapRespPayload {
	bin, err := sbxBinaryFn()
	if err != nil {
		return ipc.SandboxCapRespPayload{SbxError: err.Error()}
	}
	v, err := sbx.Client{Bin: bin, Runner: sbxRunner}.Version(ctx)
	if err != nil {
		return ipc.SandboxCapRespPayload{SbxError: sbxUnavailableMessage(err)}
	}
	return ipc.SandboxCapRespPayload{SbxAvailable: true, SbxVersion: v}
}

// sbxAvailable reports the cached sbx answer, as sandboxAvailable does for
// docker.
func (d *Daemon) sbxAvailable(ctx context.Context) (bool, string) {
	answer := d.sbxCapGet(ctx)
	return answer.SbxAvailable, answer.SbxError
}

// sbxUnavailableMessage keeps a failed probe to one short line.
func sbxUnavailableMessage(err error) string {
	const max = 200
	msg := err.Error()
	if len(msg) > max {
		msg = msg[:max] + "…"
	}
	return "sbx not usable: " + msg
}

// sbxCapGet is the one reader of the sbx capability cache. The probe is
// installed here rather than in New so a Daemon built as a literal — most
// tests — still probes sbx and never runs the DOCKER probe twice.
func (d *Daemon) sbxCapGet(ctx context.Context) ipc.SandboxCapRespPayload {
	d.sbxCapOnce.Do(func() {
		if d.sbxCap.probeFn == nil {
			d.sbxCap.probeFn = probeSbx
		}
	})
	return d.sbxCap.get(ctx)
}

// listSbxSandboxes is the create dialog's attach list: every existing sandbox
// with the folders `sbx ls` lists for it. Read fresh, never cached, since
// another tool creates and removes them; a failure is logged and offers
// nothing, which only hides the attach choice.
func listSbxSandboxes(ctx context.Context) []ipc.SbxSandbox {
	bin, err := sbxBinaryFn()
	if err != nil {
		return nil
	}
	list, err := sbx.Client{Bin: bin, Runner: sbxRunner}.ListSandboxes(ctx)
	if err != nil {
		log.Printf("sbx: listing sandboxes: %v", err)
		return nil
	}
	out := make([]ipc.SbxSandbox, 0, len(list))
	for _, l := range list {
		s := ipc.SbxSandbox{Name: l.Name, Status: l.Status}
		for _, w := range l.Workspaces {
			if w.ReadOnly {
				s.ReadOnlyFolders = append(s.ReadOnlyFolders, w.Path)
			} else {
				s.Folders = append(s.Folders, w.Path)
			}
		}
		out = append(out, s)
	}
	return out
}
