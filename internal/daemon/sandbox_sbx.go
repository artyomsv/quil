package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
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
// What an sbx pane does NOT get, deliberately, in this first version: Quil's
// hook integration (notifications, session-id tracking, resume). Those depend
// on a hook binary and a spool directory mounted into the cage, and the mounts
// belong to the user's environment file, not to Quil. An sbx pane therefore
// starts the agent fresh on every spawn and never passes a session id it could
// not later resume.
//
// Closing a pane never removes the sandbox: it is named, shared by every pane
// that names it, and holds whatever the set-up steps installed. Recreating it
// is `sbx rm --force <name>`; the next pane then creates it again.

// Seams so the call-site tests need no sbx installed.
var (
	sbxBinaryFn = func() (string, error) { return sbx.Binary(runtime.GOOS) }
	sbxRunner   = sbx.Runner(sbx.ExecRunner{})
	// sbxSpawnFn is nil by default and resolved at call time, for the reason
	// signInSpawnFn is: a closure over spawnPane here is an initialisation
	// cycle, because spawnPane reaches this file.
	sbxSpawnFn func(*Daemon, *Pane) error
)

// applySbxSpec records an sbx create. Everything else about the cage comes from
// the profile, so the spec may carry nothing else — an image or a sign-in
// mode next to backend "sbx" is a confused client, refused rather than
// half-applied.
func applySbxSpec(pane *Pane, spec *ipc.SandboxSpec) error {
	if spec.Image != "" || spec.Auth != "" || spec.ClaudeConfig != "" {
		log.Printf("pane %s: refusing an sbx sandbox spec that also names a docker image or sign-in mode", pane.ID)
		return fmt.Errorf("an sbx sandbox takes no image or sign-in fields")
	}
	pane.PluginMu.Lock()
	pane.SandboxBackend = config.SandboxBackendSbx
	pane.SandboxImage = ""
	pane.PluginMu.Unlock()
	return nil
}

// findSbxProfile names the profile for a pane directory. A pane in a linked
// worktree uses its MAIN checkout's profile: the worktree then runs in the
// same sandbox as the main checkout (one VM, one sign-in, one approval), and
// a branch that edits its own copy cannot point its panes at another cage.
// The worktree's own copy is used only when the main checkout has none.
func findSbxProfile(cwd string) (string, error) {
	if co, ok := sbx.FindCheckout(cwd); ok && co.Linked {
		if path, err := sbx.FindProfile(co.Main); err == nil {
			return path, nil
		} else if !errors.Is(err, sbx.ErrNoProfile) {
			return "", err
		}
	}
	return sbx.FindProfile(cwd)
}

// loadSbxProfile finds the profile for a pane directory, loads it, and refuses
// it unless the user approved exactly these files.
func loadSbxProfile(cwd string) (sbx.Profile, error) {
	path, err := findSbxProfile(cwd)
	if err != nil {
		if errors.Is(err, sbx.ErrNoProfile) {
			return sbx.Profile{}, fmt.Errorf("sbx sandbox: no %s at or above %s — add one that names your "+
				"sbx environment file (see docs/sandbox-panes.md)", sbx.ProfileFile, cwd)
		}
		return sbx.Profile{}, fmt.Errorf("sbx sandbox: %w", err)
	}
	prof, err := sbx.LoadProfile(path)
	if err != nil {
		return sbx.Profile{}, fmt.Errorf("sbx sandbox: %w", err)
	}
	if err := sbx.CheckTrust(sbx.TrustStorePath(config.QuilDir()), prof); err != nil {
		return sbx.Profile{}, fmt.Errorf("sbx sandbox: %w", err)
	}
	return prof, nil
}

// startSbxPane is spawnPane's sbx branch. It reports whether it STARTED the
// pane's child; false with a nil error means the pane stood down while its
// sandbox is prepared, and a goroutine will spawn it again. A started pane
// gets back the session to install: ptySession wrapped so that closing it
// also stops the agent inside the sandbox.
func (d *Daemon) startSbxPane(pane *Pane, ptySession apty.Session, p *plugin.PanePlugin) (apty.Session, bool, error) {
	pane.PluginMu.Lock()
	ready := pane.sbxReady
	pane.sbxReady = false
	cwd := pane.CWD
	args := append([]string{}, p.Command.Args...)
	if len(pane.InstanceArgs) > 0 {
		args = append([]string{}, pane.InstanceArgs...)
	}
	pane.PluginMu.Unlock()

	// Checked on EVERY spawn, the ready respawn included: the profile names
	// the sandbox, and a profile edited after approval must not be able to
	// point a pane at a different cage.
	prof, err := loadSbxProfile(cwd)
	if err != nil {
		return nil, false, err
	}
	if !ready {
		d.beginSbxPrepare(pane.ID, prof)
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
	if runtime.GOOS == "windows" {
		workdir = ""
	}
	// A fresh tag per spawn: closing this session stops exactly the
	// processes carrying it (see sbx.TagEnv).
	tag := sbx.NewTag(pane.ID)
	env := append(slices.Clone(p.Command.Env), sbx.TagEnv+"="+tag)
	argv := sbx.ExecArgs(prof.Name, workdir, env, p.Command.Cmd, args)
	ptySession.SetCWD(cwd)
	log.Printf("spawn: pane %s sbx sandbox=%s cmd=%s args=%v cwd=%s", pane.ID, prof.Name, bin, argv, cwd)
	if err := ptySession.Start(bin, argv...); err != nil {
		return ptySession, true, err
	}
	return &sbxSession{Session: ptySession, stop: func() { d.stopSbxTagAsync(prof.Name, tag) }}, true, nil
}

// sbxSession is an sbx pane's PTY. Ending the host `sbx exec` does not end
// the agent it started inside the VM, so Close also stops the processes
// carrying this spawn's tag. Every way a pane loses its process — close,
// replace, restart, daemon stop — goes through Close.
type sbxSession struct {
	apty.Session
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

// sbxPrepare is the per-sandbox single-flight. Keyed by sandbox NAME, because
// that is the resource: two panes on one profile must share one create.
type sbxPrepare struct {
	mu sync.Mutex
	// panes holds, per sandbox name in preparation, the owner first and then
	// every pane that asked meanwhile. A pane is recorded once — Alt+R on a
	// waiting pane re-enters here, and a second entry would spawn it twice.
	panes map[string][]string
	// inFlight lets a test wait for the goroutine; shutdown does not.
	inFlight sync.WaitGroup
}

func (s *sbxPrepare) begin(name, paneID string) (owner bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.panes == nil {
		s.panes = map[string][]string{}
	}
	if list, ok := s.panes[name]; ok {
		if !slices.Contains(list, paneID) {
			s.panes[name] = append(list, paneID)
		}
		return false
	}
	s.panes[name] = []string{paneID}
	return true
}

func (s *sbxPrepare) end(name string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.panes[name]
	delete(s.panes, name)
	return list
}

func (s *sbxPrepare) wait() { s.inFlight.Wait() }

// beginSbxPrepare starts (or joins) the preparation of the pane's sandbox.
func (d *Daemon) beginSbxPrepare(paneID string, prof sbx.Profile) {
	if !d.sbxPrep.begin(prof.Name, paneID) {
		d.announce(paneID, fmt.Sprintf("Waiting for sbx sandbox %s, which another pane is preparing…\r\n", prof.Name))
		return
	}
	d.announce(paneID, fmt.Sprintf("Preparing sbx sandbox %s (profile %s)…\r\n", prof.Name, prof.Path))
	d.sbxPrep.inFlight.Add(1)
	go func() {
		defer d.sbxPrep.inFlight.Done()
		d.runSbxPrepare(paneID, prof)
	}()
}

// runSbxPrepare makes the sandbox exist, then spawns every pane that waited.
func (d *Daemon) runSbxPrepare(ownerID string, prof sbx.Profile) {
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

	err := d.ensureSbxSandbox(ctx, ownerID, prof)
	panes := d.sbxPrep.end(prof.Name)
	if err != nil {
		log.Printf("sbx: sandbox %s: %v", prof.Name, err)
		for _, id := range panes {
			d.failSbxPrepare(id, err)
		}
		return
	}
	failed := d.mountSbxWorktrees(ctx, prof.Name, panes)
	var starting []string
	for _, id := range panes {
		if err := failed[id]; err != nil {
			d.failSbxPrepare(id, err)
			continue
		}
		starting = append(starting, id)
	}
	d.stopSbxLeftovers(ctx, prof.Name, starting)
	for _, id := range starting {
		d.respawnSbxPane(id)
	}
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
// It returns the panes that cannot start. A mount failure stops only the
// panes whose directory is INSIDE that folder — they cannot work without it;
// a pane in the main checkout is told and starts anyway.
func (d *Daemon) mountSbxWorktrees(ctx context.Context, name string, panes []string) map[string]error {
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
		dir := gitworktree.WorktreesDir(co.Main)
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
		err := c.Mount(ctx, name, dir)
		if err == nil {
			log.Printf("sbx: sandbox %s: shared %s", name, dir)
			continue
		}
		log.Printf("sbx: sandbox %s: %v", name, err)
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
func (d *Daemon) ensureSbxSandbox(ctx context.Context, ownerID string, prof sbx.Profile) error {
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

// respawnSbxPane starts the child for a pane whose sandbox is now ready.
func (d *Daemon) respawnSbxPane(paneID string) {
	if d.stopping() {
		log.Printf("sbx: pane %s: sandbox ready during shutdown; not spawning", paneID)
		return
	}
	pane := d.session.Pane(paneID)
	if pane == nil {
		return // closed while the sandbox was prepared
	}
	pane.PluginMu.Lock()
	pane.sbxReady = true
	cols, rows := pane.Cols, pane.Rows
	pane.PluginMu.Unlock()

	spawn := sbxSpawnFn
	if spawn == nil {
		spawn = func(d *Daemon, p *Pane) error { return d.spawnPane(p, newSessionFn(cols, rows), false) }
	}
	if err := spawn(d, pane); err != nil {
		log.Printf("sbx: pane %s: spawn after preparation: %v", paneID, err)
		pane.PluginMu.Lock()
		pane.sbxReady = false
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
