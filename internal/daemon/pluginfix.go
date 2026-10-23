package daemon

import (
	"errors"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/plugin"
	apty "github.com/artyomsv/quil/internal/pty"
)

// A plugin dialog's buttons run in the pane: a handler fix (`stripe login`)
// or an update of the plugin's binary. The client sends a fix NAME or the
// offered VERSION, never an argv — the daemon looks the fix up in its own
// plugin file, the way toggles are resolved (resolveToggles), so no IPC
// client can run a command of its choosing under a plugin's name. The fix
// replaces the pane's child for exactly one spawn (Pane.fixArgv) and its
// exit is handled by onFixExit, ahead of the ordinary exit path.

func (d *Daemon) handlePluginFixReq(conn *ipc.Conn, msg *ipc.Message) {
	var req ipc.PluginFixReqPayload
	if err := msg.DecodePayload(&req); err != nil {
		refuseFix(conn, msg, ipc.MsgPluginFixResp, "malformed plugin_fix_req")
		return
	}
	pane, p, err := d.pluginFixTarget(req.PaneID)
	if err != nil {
		refuseFix(conn, msg, ipc.MsgPluginFixResp, err.Error())
		return
	}
	// Not now only postpones the update dialog: nothing stops, nothing runs.
	// It rides this act-class type on purpose — a standard-token user must
	// be able to dismiss a dialog they can see.
	if req.Fix == ipc.PluginFixNotNow {
		version, err := pluginOfferedVersion(pane, req.Version)
		if err != nil {
			refuseFix(conn, msg, ipc.MsgPluginFixResp, err.Error())
			return
		}
		if err := d.pluginUpdateSkips().Skip(p.Name, version); err != nil {
			refuseFix(conn, msg, ipc.MsgPluginFixResp, fmt.Sprintf("could not record Not now: %v", err))
			return
		}
		log.Printf("pane %s: %s update %s declined", pane.ID, p.Name, version)
		respondTo(conn, msg.ID, ipc.MsgPluginFixResp, ipc.PluginFixRespPayload{OK: true})
		return
	}
	// By name, in the daemon's OWN definition. A fix the file no longer has
	// — edited between the dialog and the press, or the reserved "update"
	// name — is refused, never guessed at.
	fix, ok := p.FindFix(req.Fix)
	if !ok {
		refuseFix(conn, msg, ipc.MsgPluginFixResp, fmt.Sprintf("%s has no fix named %q", p.DisplayName, req.Fix))
		return
	}
	then := fix.Then
	if then == "" {
		then = plugin.FixThenRestart
	}
	argv, err := resolvedFixArgv(p, fix.Run)
	if err != nil {
		refuseFix(conn, msg, ipc.MsgPluginFixResp, err.Error())
		return
	}
	if err := d.startFix(pane, argv, fix.Label, then); err != nil {
		refuseFix(conn, msg, ipc.MsgPluginFixResp, err.Error())
		return
	}
	respondTo(conn, msg.ID, ipc.MsgPluginFixResp, ipc.PluginFixRespPayload{OK: true})
}

// pluginVersionRe is the shape a version from a client may have: digits and
// dots with an optional pre-release or build suffix. The version becomes a
// release TAG and an asset NAME on the update argv, and it is a client's
// string — so anything else is refused before the pane is touched, however
// it would have been quoted.
var pluginVersionRe = regexp.MustCompile(`^\d+(\.\d+){0,3}([-+][0-9A-Za-z.-]+)?$`)

// pluginVersionArg validates a request's version, without its leading v.
func pluginVersionArg(raw string) (string, error) {
	version := strings.TrimPrefix(raw, "v")
	if version == "" {
		return "", errors.New("no version given")
	}
	if !pluginVersionRe.MatchString(version) {
		return "", fmt.Errorf("%q is not a version", raw)
	}
	return version, nil
}

// pluginOfferedVersion validates a request's version AND requires it to be
// the one the daemon's own update dialog offered for this pane in its
// current run (Pane.pluginOffered, recorded as the frame left). The shape
// check alone let any act-class client decline "999.0.0" and so hide every
// future notice, or ask for an update to a version the tool never named.
func pluginOfferedVersion(pane *Pane, raw string) (string, error) {
	version, err := pluginVersionArg(raw)
	if err != nil {
		return "", err
	}
	pane.PluginMu.Lock()
	offered := strings.TrimPrefix(pane.pluginOffered, "v")
	pane.PluginMu.Unlock()
	if offered == "" {
		return "", errors.New("no update was offered for this pane")
	}
	if version != offered {
		return "", fmt.Errorf("%s is not the version offered for this pane (%s)", raw, offered)
	}
	return version, nil
}

// resolvedFixArgv is run with its first element resolved to the program it
// names, so startFixChild spawns without a lookup of its own — the lookup is
// a PATH walk, which must neither run unbounded on the dispatch goroutine
// nor fail only after the pane was stopped. A program that cannot be found
// in time is a refusal with the pane untouched.
func resolvedFixArgv(p *plugin.PanePlugin, run []string) ([]string, error) {
	if len(run) == 0 {
		return nil, errors.New("fix names no command")
	}
	cmd := fixCommandWithin(p, run[0], pluginTargetTimeout)
	if cmd == "" {
		return nil, fmt.Errorf("could not locate %s", run[0])
	}
	argv := append([]string{cmd}, run[1:]...)
	return argv, nil
}

func (d *Daemon) handlePluginUpdateReq(conn *ipc.Conn, msg *ipc.Message) {
	var req ipc.PluginUpdateReqPayload
	if err := msg.DecodePayload(&req); err != nil {
		refuseFix(conn, msg, ipc.MsgPluginUpdateResp, "malformed plugin_update_req")
		return
	}
	pane, p, err := d.pluginFixTarget(req.PaneID)
	if err != nil {
		refuseFix(conn, msg, ipc.MsgPluginUpdateResp, err.Error())
		return
	}
	// Every refusal below lands BEFORE anything stops the pane: a pane whose
	// update cannot even be described must keep whatever it was showing.
	if p.Update == nil {
		refuseFix(conn, msg, ipc.MsgPluginUpdateResp, p.DisplayName+" declares no update")
		return
	}
	version, err := pluginOfferedVersion(pane, req.Version)
	if err != nil {
		refuseFix(conn, msg, ipc.MsgPluginUpdateResp, err.Error())
		return
	}
	// The binary's path picks the method and is the manual path's target.
	// Resolved again here rather than trusted from the dialog's frame, and
	// BOUNDED: it is a PATH walk and a symlink resolution on this conn's
	// dispatch goroutine, and a PATH entry on a dead mount must cost this
	// request, not every message after it (pluginBinaryTargetWithin).
	binPath := pluginBinaryTargetWithin(p.Command.Path, p.Command.Cmd, pluginTargetTimeout, resolvePluginBinaryFn)
	if binPath == "" {
		refuseFix(conn, msg, ipc.MsgPluginUpdateResp, fmt.Sprintf("could not locate the %s binary", p.DisplayName))
		return
	}
	run, err := d.updateArgv(p, binPath, version)
	if err != nil {
		refuseFix(conn, msg, ipc.MsgPluginUpdateResp, err.Error())
		return
	}
	argv, err := resolvedFixArgv(p, run)
	if err != nil {
		refuseFix(conn, msg, ipc.MsgPluginUpdateResp, err.Error())
		return
	}
	if err := d.startFix(pane, argv, "Update to "+version, plugin.FixThenRestart); err != nil {
		refuseFix(conn, msg, ipc.MsgPluginUpdateResp, err.Error())
		return
	}
	respondTo(conn, msg.ID, ipc.MsgPluginUpdateResp, ipc.PluginFixRespPayload{OK: true})
}

// refuseFix answers a fix or update request that started nothing.
func refuseFix(conn *ipc.Conn, msg *ipc.Message, respType, why string) {
	respondTo(conn, msg.ID, respType, ipc.PluginFixRespPayload{Error: why})
}

// pluginFixTarget resolves the pane a request names and the plugin it runs.
// A sandbox pane is refused outright: its binary is inside the container,
// so a fix would run the host's copy (or nothing) and an update would
// replace a file the pane never executes.
func (d *Daemon) pluginFixTarget(paneID string) (*Pane, *plugin.PanePlugin, error) {
	pane := d.session.Pane(paneID)
	if pane == nil {
		return nil, nil, fmt.Errorf("no such pane: %s", paneID)
	}
	pane.PluginMu.Lock()
	typ := pane.Type
	sandboxed := pane.SandboxImage != "" || pane.SandboxBackend != ""
	overlay := pane.Overlay
	pane.PluginMu.Unlock()
	typ, prefixed := splitSandboxType(typ)
	if sandboxed || prefixed {
		return nil, nil, errors.New("fix actions are not available in a sandbox pane: its binary is inside the container")
	}
	// An overlay is destroyed the moment its child exits (onPaneExitGeneration),
	// so a fix there would take the pane with it on a non-zero exit and a
	// zero one would restart a slot the TUI expects to be gone.
	if overlay {
		return nil, nil, errors.New("fix actions are not available in an overlay pane")
	}
	p := d.registry.Get(typ)
	if p == nil {
		return nil, nil, fmt.Errorf("pane %s runs no plugin this daemon knows (%s)", paneID, typ)
	}
	return pane, p, nil
}

// startFix runs argv (first element already resolved) in the pane in place
// of its normal command, once. label is what the pane shows meanwhile and
// then what a zero exit leads to. The marker and the argv are set in ONE
// PluginMu span with the two refusals, so two presses racing from two
// clients start one fix: the second sees FixLabel set. restartPaneInPlace
// then consumes the argv through spawnPane. A restart that does not happen
// clears the one-shot again, or the next Alt+R would run the fix the user
// never saw start.
func (d *Daemon) startFix(pane *Pane, argv []string, label, then string) error {
	if len(argv) == 0 {
		return errors.New("fix names no command")
	}
	// A deferred pane is spawned FIRST, while nothing is armed. restartPaneInPlace
	// does the same call, but by then the argv is set, and the lazy spawn
	// would consume it: the fix would run as the pane's first child and the
	// restart would replace it with the normal command, leaving the marker
	// on a pane that runs no fix and refusing every restart after.
	d.ensurePaneSpawned(pane)
	pane.PluginMu.Lock()
	if branch := pane.PreparingWorktree; branch != "" {
		pane.PluginMu.Unlock()
		return fmt.Errorf("pane is still creating worktree %s", branch)
	}
	if running := pane.FixLabel; running != "" {
		pane.PluginMu.Unlock()
		return fmt.Errorf("a fix is already running in this pane: %s", running)
	}
	pane.FixLabel = label
	pane.fixArgv = append([]string(nil), argv...)
	pane.fixThen = then
	pane.PluginMu.Unlock()

	log.Printf("pane %s: running fix %q: %v", pane.ID, label, argv)
	if !d.restartPaneInPlace(pane) {
		pane.PluginMu.Lock()
		pane.FixLabel, pane.fixArgv, pane.fixThen = "", nil, ""
		pane.PluginMu.Unlock()
		d.broadcastState()
		return errors.New("the fix could not be started (see quild.log)")
	}
	d.broadcastState()
	d.requestSnapshot()
	return nil
}

// startFixChild is spawnPane's arm for a consumed fix argv: the resolved
// command, the plugin's environment, the pane's directory, and nothing
// else — no shell integration, hooks, MCP or container, which are the
// normal command's. No lookup here: resolvedFixArgv did it, bounded,
// before the pane was stopped.
func (d *Daemon) startFixChild(pane *Pane, s apty.Session, p *plugin.PanePlugin, cwd string, argv []string) error {
	if len(p.Command.Env) > 0 {
		s.SetEnv(append([]string{}, p.Command.Env...))
	}
	s.SetCWD(cwd)
	log.Printf("spawn: pane %s fix cmd=%s args=%v cwd=%s", pane.ID, argv[0], argv[1:], cwd)
	return s.Start(argv[0], argv[1:]...)
}

// fixCommandWithin resolves a fix's argv[0] the way [command] cmd is
// resolved: the plugin's path override, or the location the availability
// search wrote back into Cmd, when the fix names the plugin's own tool —
// no filesystem call — else a PATH lookup under the process-wide
// blocking-FS budget (pluginBinaryTargetWithin's shape): the walk runs on
// a worker that keeps its permit for as long as the syscalls take, and the
// caller gets "" when no permit is free, the name is not on PATH, or the
// worker has not answered within d.
func fixCommandWithin(p *plugin.PanePlugin, name string, d time.Duration) string {
	path, cmd := p.Command.Path, p.Command.Cmd
	if sameTool(name, path) || sameTool(name, cmd) {
		if path != "" {
			return path
		}
		if filepath.IsAbs(cmd) {
			return cmd
		}
	}
	if !claimBlockingFSCall() {
		return ""
	}
	// Read on this goroutine, not the worker's: a test that swaps lookPathFn
	// restores it after the response, which this goroutine's send orders but
	// a worker still parked in the stub does not.
	lookPath := lookPathFn
	ch := make(chan string, 1)
	go func() {
		defer releaseBlockingFSCall()
		found, err := lookPath(name)
		if err != nil {
			found = ""
		}
		ch <- found
	}()
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case found := <-ch:
		return found
	case <-timer.C:
		return ""
	}
}

// lookPathFn is exec.LookPath; a test replaces it with one that never
// answers.
var lookPathFn = exec.LookPath

// sameTool reports whether two command spellings name one program: equal
// base names, case-insensitively and without a Windows .exe.
func sameTool(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	base := func(s string) string {
		return strings.TrimSuffix(strings.ToLower(filepath.Base(s)), ".exe")
	}
	return base(a) == base(b)
}

// onFixExit is onPaneExitGeneration's first branch: the exit of a fix
// child (Pane.FixLabel set) is the fix's outcome, not the pane's. It emits
// no process_exit card and fails no delegated task. A zero exit with
// then = restart starts the pane's normal command again, which spawnPane
// runs because the one-shot argv is already consumed; otherwise the pane
// stays on the fix's output — a failed login or download is what the user
// needs to read — with one status line and Alt+R. generation is the fix
// child's run, which is still the installed PTY: the line is written into
// the pane's own stream like the child's bytes, so it is in OutputBuf for
// a reconnect and reaches every client as pane_output. Reports whether the
// exit was a fix's: the marker is set AND the exiting generation is the
// one the fix was spawned as (Pane.fixGen). The marker alone is not enough
// — the normal child can exit between startFix's marker span and the
// restart that follows it, and that exit is the pane's, card and all.
func (d *Daemon) onFixExit(pane *Pane, code int, generation uint64) bool {
	pane.PluginMu.Lock()
	label, then := pane.FixLabel, pane.fixThen
	if label == "" || generation == 0 || generation != pane.fixGen {
		pane.PluginMu.Unlock()
		return false
	}
	pane.FixLabel, pane.fixThen = "", ""
	pane.PluginMu.Unlock()
	if code == 0 && then == plugin.FixThenRestart {
		log.Printf("pane %s: fix %q succeeded, restarting", pane.ID, label)
		if !d.restartPaneInPlace(pane) {
			log.Printf("pane %s: restart after fix %q failed", pane.ID, label)
		}
		d.broadcastState()
		d.requestSnapshot()
		return true
	}
	line := "fix finished — Alt+R restarts"
	if code != 0 {
		line = fmt.Sprintf("fix failed (exit %d) — Alt+R restarts", code)
	}
	log.Printf("pane %s: fix %q: %s", pane.ID, label, line)
	d.flushPaneOutputGeneration(pane.ID, []byte("\r\n"+line+"\r\n"), generation)
	// The marker is gone: every client drops its "running fix" rendering.
	d.broadcastState()
	return true
}

// updateArgv is the argv that updates p's binary at binPath to version.
// binPath is the SYMLINK-RESOLVED path the bounded resolver found
// (pluginBinaryTarget), so no filesystem call happens here. The first
// [[update.managers]] whose match hits the path owns the binary and its
// own upgrade command is the argv — a brew- or scoop-owned file is never
// overwritten by a download. With no match the argv is this daemon's own
// quild running the hidden plugin-update subcommand (cmd/quild), with
// every decision as a flag: the release by TAG, the asset for this
// platform with {version} expanded, the checksums file, the binary name,
// and the plugin's detect command for the before/after version lines.
// Missing platform data is an error here, before anything stops the pane.
func (d *Daemon) updateArgv(p *plugin.PanePlugin, binPath, version string) ([]string, error) {
	u := p.Update
	if u == nil {
		return nil, fmt.Errorf("%s declares no update", p.DisplayName)
	}
	version = strings.TrimPrefix(version, "v")
	if version == "" {
		return nil, errors.New("no version to update to")
	}
	if m := matchUpdateManager(u.Managers, binPath); m != nil {
		if len(m.Run) == 0 {
			return nil, fmt.Errorf("update manager %q names no command", m.Name)
		}
		return append([]string(nil), m.Run...), nil
	}
	if u.GitHub == "" || u.Binary == "" {
		return nil, fmt.Errorf("%s is not installed by a package manager Quil knows, and its plugin names no download", p.DisplayName)
	}
	platform := runtime.GOOS + "_" + runtime.GOARCH
	asset := u.Assets[platform]
	if asset == "" {
		return nil, fmt.Errorf("%s publishes no %s build for %s", p.DisplayName, version, platform)
	}
	checksums := u.Checksums[runtime.GOOS]
	if checksums == "" {
		return nil, fmt.Errorf("%s publishes no checksums file for %s", p.DisplayName, runtime.GOOS)
	}
	exe, err := quildExeFn()
	if err != nil {
		return nil, fmt.Errorf("locate quild: %w", err)
	}
	argv := []string{exe, "plugin-update",
		"--target", binPath,
		"--repo", u.GitHub,
		"--tag", "v" + version,
		"--asset", strings.ReplaceAll(asset, "{version}", version),
		"--checksums", checksums,
		"--binary", u.Binary,
	}
	if p.Command.DetectCmd != "" {
		argv = append(argv, "--detect", p.Command.DetectCmd)
	}
	return argv, nil
}

// matchUpdateManager is the first [[update.managers]] entry whose match hits
// binPath — the symlink-resolved binary — or nil, for an empty path too.
// ONE function for the dialog (which names the manager in its question) and
// updateArgv (which runs it): two matchers would let the dialog promise brew
// and the update download.
func matchUpdateManager(managers []plugin.UpdateManager, binPath string) *plugin.UpdateManager {
	if binPath == "" {
		return nil
	}
	for i := range managers {
		if re := managers[i].Compiled(); re != nil && re.MatchString(binPath) {
			return &managers[i]
		}
	}
	return nil
}
