package daemon

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/opencodehook"
	"github.com/artyomsv/quil/internal/plugin"
	"github.com/artyomsv/quil/internal/sandbox"
)

// hookModeFor picks the per-plugin hook verbosity the container's env carries.
// Mirrors the switch spawnPane uses for a host pane, so a sandbox pane obeys
// the same [notification.hooks] settings.
func hookModeFor(cfg config.Config, p *plugin.PanePlugin) string {
	switch {
	case p.Name == "opencode":
		return cfg.Notification.Hooks.OpenCode
	case p.Name == plugin.CodexPluginName:
		return cfg.Notification.Hooks.Codex
	default:
		return cfg.Notification.Hooks.Claude
	}
}

// applySandboxSpec records the container image a create asked for, so
// spawnPane's sandbox branch fires.
//
// It is the ONE place a wire spec becomes pane state, and it must be called by
// every create path. The first version of this feature had no caller at all:
// the dialog sent the spec, three create paths applied Type and InstanceName
// beside it, and none of them touched Sandbox — so `spawnPane` gated on a
// field only RESTORE ever wrote, and "Run in a Docker container" spawned the
// agent on the HOST, un-isolated, with no error anywhere. That is precisely
// the silent-isolation failure this whole feature exists to prevent, and it
// shipped because no test drove a create with a spec through to a docker argv.
//
// The image is validated HERE rather than at the spawn, because this is the
// boundary between an untrusted wire value and daemon state. It reaches
// `docker run` as the first positional argument, and docker parses options up
// to the first operand — so an image beginning with "-" is read as docker
// OPTIONS, and any IPC client could inject --privileged, -v /:/host,
// --entrypoint or --network=host. A rejected image leaves the pane
// un-sandboxed, which the caller must then refuse rather than spawn.
func applySandboxSpec(pane *Pane, spec *ipc.SandboxSpec) error {
	if spec == nil {
		return nil
	}
	if !sandbox.ImageOK(spec.Image) {
		// Logged by LENGTH, never by value: the daemon log is rendered by the
		// F1 viewer, which does not pass through a VT emulator, so a hostile
		// string must not reach it. Same rule the resume-id guard follows.
		log.Printf("pane %s: refusing sandbox image of length %d — not a valid image reference",
			pane.ID, len(spec.Image))
		return fmt.Errorf("invalid container image reference")
	}
	// Validated like the image, and for the same reason: any IPC client can set
	// it, and the two modes hand the container DIFFERENT credentials. An
	// unknown value is dropped to empty — follow the config — rather than
	// guessed, because guessing wrong is either a pane that cannot
	// authenticate or one that silently loses the model it was opened for.
	auth := ""
	switch config.SandboxAuthMode(spec.Auth) {
	case config.SandboxAuthToken, config.SandboxAuthBrowser:
		auth = spec.Auth
	case "":
		// Absent: follow [sandbox] auth, which is what every older client and
		// every non-dialog producer sends.
	default:
		log.Printf("pane %s: ignoring unknown sandbox auth mode of length %d; using the configured default",
			pane.ID, len(spec.Auth))
	}

	pane.PluginMu.Lock()
	pane.SandboxImage = spec.Image
	pane.SandboxAuth = auth
	pane.PluginMu.Unlock()
	return nil
}

// sandboxTypePrefix marks a persisted pane type as sandboxed.
//
// The prefix, rather than a bare boolean field, is what makes a DOWNGRADE
// safe. Restore reads only the keys it knows, and auto-update has a rollback
// path, so a daemon that predates this feature would read a sandbox pane's
// type as plain "claude-code", point it at the worktree, and spawn an agent on
// the host — un-sandboxed, silently. An unknown type instead takes the
// existing fallback to "terminal": a shell, which is wrong but harmless and
// visible.
const sandboxTypePrefix = "sandbox/"

// sandboxPaneType is what goes on disk for a sandboxed pane of the given
// plugin.
func sandboxPaneType(plugin string) string { return sandboxTypePrefix + plugin }

// splitSandboxType reports the plugin a persisted type names, and whether that
// type was sandboxed.
func splitSandboxType(typ string) (plugin string, sandboxed bool) {
	if rest, ok := strings.CutPrefix(typ, sandboxTypePrefix); ok {
		return rest, true
	}
	return typ, false
}

// sandboxMappingFn is the seam that makes prepareSandbox drivable without a
// real git repository.
//
// It exists because the alternative was a test that calls writeOverlays
// directly — which passes even when nothing calls it, and a mutation deleting
// the call from prepareSandbox stayed green. The overlays are what stop a
// container-side `git worktree prune` deleting the host's worktree
// registration with uncommitted work in it, so "nobody proves it is written"
// is not an acceptable state for them.
var sandboxMappingFn = sandbox.NewMapping

// sandboxRoot is the directory holding every per-pane sandbox tree.
func sandboxRoot(quilDir string) string { return filepath.Join(quilDir, "sandbox") }

// sandboxEmptyDir is the permanently empty directory used to shadow the two
// objects/info directories read-only. Shared by every pane; nothing writes to
// it, which is the whole point.
func sandboxEmptyDir(quilDir string) string { return filepath.Join(sandboxRoot(quilDir), "empty") }

// prepareSandbox builds the pane's mapping and puts every host-side file in
// place, so `docker run` has something coherent to mount.
//
// Order matters and is not obvious: the overlays and the alternates line must
// exist BEFORE the container starts. A bind-mounted file cannot be replaced
// from inside afterwards — a host-side atomic write changes the inode and the
// container keeps seeing the old content — and a container that commits before
// the alternates line exists leaves the host answering "fatal: bad object
// HEAD" for that worktree until it does.
func (d *Daemon) prepareSandbox(ctx context.Context, pane *Pane, pluginName, image string) (sandbox.Mapping, error) {
	quilDir := config.QuilDir()

	m, err := sandboxMappingFn(ctx, quilDir, pane.CWD, pane.ID)
	if err != nil {
		return sandbox.Mapping{}, err
	}

	// The hook binary. Resolved BEFORE anything is created, because a
	// claude-code pane refuses to spawn without one and there is no point
	// laying out a tree for a pane that will not start.
	arch := d.sandboxCap.get(ctx).Arch
	quild, err := d.linuxQuildForPane(ctx, pluginName, arch)
	if err != nil {
		return sandbox.Mapping{}, err
	}
	m.HostQuild = quild
	if d.cfg.Sandbox.SharedClaudeConfig {
		m.SharedClaudeRoot = filepath.Join(sandboxRoot(quilDir), "claude")
	}

	// The per-pane tree. Every directory the hook writes into lives under
	// this one root, which is why the hook binary needs no change: it derives
	// all of them from QUIL_HOOK_HOME.
	for _, dir := range []string{
		m.HostPaneRoot,
		m.HostObjects(),
		m.HostClaudeConfig(),
		m.HostCodexHome(),
		// Created whether it is the per-pane dir or the shared one; docker
		// would otherwise create a root-owned directory for a missing mount
		// source and the container user could not sign in.
		filepath.Join(m.HostPaneRoot, "sessions"),
		filepath.Join(m.HostPaneRoot, "events"),
		filepath.Join(m.HostPaneRoot, "history"),
		filepath.Join(m.HostPaneRoot, "claudehook"),
		m.HostOverlayDir,
		sandboxEmptyDir(quilDir),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return sandbox.Mapping{}, fmt.Errorf("sandbox: create %s: %w", dir, err)
		}
	}

	// Re-validate now that HostQuild and SharedClaudeRoot are set. NewMapping
	// could not see either — they are filled in above, after it returned — and
	// both become `--mount` sources, so a comma in a QUIL_SANDBOX_QUILD path
	// or in the shared config directory would otherwise reach docker as a
	// mis-split argument.
	if err := m.Validate(); err != nil {
		return sandbox.Mapping{}, err
	}

	if err := ensureEmptyShadowFile(m.HostEmptyFile); err != nil {
		return sandbox.Mapping{}, fmt.Errorf("sandbox: %w", err)
	}

	// BEFORE the seed, and the order is load-bearing: the seed never
	// overwrites, so a stale answer it would decline to touch has to be
	// repaired first. Claude-only, through the same predicate that gates every
	// other auth decision — a codex or opencode directory holds no Claude
	// onboarding answer and must not be stamped with a Claude sign-in mode.
	// See sandbox_authstamp.go.
	if plugin.UsesClaudeAuthName(pluginName) {
		if err := reconcileClaudeAuthMode(m, d.paneAuthMode(pane)); err != nil {
			return sandbox.Mapping{}, fmt.Errorf("sandbox: %w", err)
		}
	}

	// So the pane opens on a working prompt instead of four first-run screens.
	// Gated on the container actually GETTING a credential: the same seed on
	// an unauthenticated pane hides the sign-in that lives inside onboarding.
	// See sandbox_claudeconfig.go.
	if err := seedClaudeConfig(m, d.sandboxTokenAvailable(pane, pluginName)); err != nil {
		return sandbox.Mapping{}, fmt.Errorf("sandbox: %w", err)
	}

	// The codex equivalent, and a different mechanism because codex has no
	// setup-token: its credential is a file, and copying it is what its own
	// users do for containers. Scoped to a codex pane — no other agent reads
	// it, and a credential should not travel further than the pane that needs
	// it. See sandbox_codexauth.go.
	if pluginName == plugin.CodexPluginName {
		if err := seedCodexAuth(m); err != nil {
			return sandbox.Mapping{}, fmt.Errorf("sandbox: %w", err)
		}
	}

	if err := writeOverlays(m); err != nil {
		return sandbox.Mapping{}, err
	}
	// Record which repository this pane's line went into BEFORE writing it.
	// A crash between the two leaves a registry entry for a line that does
	// not exist, which the repair pass simply finds nothing to do about; the
	// reverse — a line with no record — is the state nothing can repair.
	d.sandboxReg.put(pane.ID, m.HostGitCommon)
	if err := sandbox.AddAlternate(m); err != nil {
		return sandbox.Mapping{}, fmt.Errorf("sandbox: register object store: %w", err)
	}

	// A container left by a crashed daemon holds the --name this one needs,
	// and docker refuses the run rather than replacing it. Ignoring the
	// result is correct: a pane with no container is the normal case.
	if err := sandboxRemoveFn(ctx, pane.ID); err != nil {
		log.Printf("sandbox: pane %s: pre-run remove: %v", pane.ID, err)
	}
	return m, nil
}

// writeOverlays puts the two gitdir files in place.
//
// They live in a directory that is never mounted as a directory, only as two
// individual read-only files. A read-only flag is per mount point, so an
// overlay reachable read-write anywhere else is rewritable by the agent — and
// rewriting the admin one lets `git worktree prune` inside the container
// delete the host's worktree registration, taking uncommitted work with it.
func writeOverlays(m sandbox.Mapping) error {
	if m.Kind != sandbox.KindWorktree {
		return nil
	}
	for _, f := range []struct{ path, body string }{
		{m.HostDotGitOverlay(), sandbox.DotGitOverlay(m)},
		{m.HostAdminGitdirOverlay(), sandbox.AdminGitdirOverlay(m)},
	} {
		if err := os.WriteFile(f.path, []byte(f.body), 0o600); err != nil {
			return fmt.Errorf("sandbox: write overlay %s: %w", f.path, err)
		}
	}
	return nil
}

// ensureEmptyShadowFile makes sure the empty FILE that shadows
// config.worktree exists.
//
// A file rather than part of the directory loop, because docker invents a
// root-owned DIRECTORY for a missing bind source and git then refuses to read
// it as a config file.
//
// It STATS FIRST, and that is the fix rather than the tidy-up. The previous
// version opened with O_CREATE|O_WRONLY and forgave os.IsExist — but O_CREATE
// without O_EXCL never REPORTS EEXIST, it just opens the existing file for
// writing, and this file is created 0400. On Windows that sets the ReadOnly
// attribute, so opening it for writing answers "Access is denied" — an error
// os.IsExist does not match. The result: the first sandbox pane on a fresh
// QUIL_HOME worked, and every one after it failed to spawn. The forgiving
// branch was checking for an error the call could not produce.
//
// The host mode is not the security boundary — the mount carries docker's own
// `readonly` — so the file only has to EXIST and be EMPTY.
func ensureEmptyShadowFile(path string) error {
	switch st, err := os.Stat(path); {
	case err == nil:
		if st.IsDir() {
			return fmt.Errorf("shadow file %s is a directory (docker invents one for a "+
				"missing bind source); remove it", path)
		}
		if st.Size() != 0 {
			// It is mounted OVER config.worktree, which host git executes
			// values from, so content here is not cosmetic. Refused rather
			// than truncated: the 0400 mode means truncating needs the
			// attribute cleared first, and a file that grew content is a
			// state worth a human looking at.
			return fmt.Errorf("shadow file %s is not empty (%d bytes); it shadows "+
				"config.worktree and git would read it; remove it", path, st.Size())
		}
		return nil
	case !os.IsNotExist(err):
		return fmt.Errorf("stat shadow file %s: %w", path, err)
	}

	// O_EXCL so a lost race is distinguishable from a real failure — two panes
	// can prepare at once, and the loser must not report an error.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o400)
	if err != nil {
		if os.IsExist(err) {
			return nil
		}
		return fmt.Errorf("create shadow file %s: %w", path, err)
	}
	return f.Close()
}

// sandboxIdentity gathers what the container needs that only the host knows.
func (d *Daemon) sandboxIdentity(pane *Pane, pluginName, hookMode string, recordHistory bool) sandbox.Identity {
	id := sandbox.Identity{
		HookMode:      hookMode,
		RecordHistory: recordHistory,
		QuilHomeLabel: quilHomeLabel(config.QuilDir()),
		GitUserName:   gitConfigValue("user.name"),
		GitUserEmail:  gitConfigValue("user.email"),
	}
	// The token travels as a NAME only: docker forwards the value from its
	// own environment, so it never reaches argv and therefore never reaches
	// quild.log, which the F1 viewer renders.
	_, unrecognised := d.cfg.Sandbox.ResolveAuth()
	mode := d.paneAuthMode(pane)
	if unrecognised != "" {
		log.Printf("sandbox: pane %s: unknown [sandbox] auth = %q; using the "+
			"browser fallback. Valid values are \"token\" and \"browser\"", pane.ID, unrecognised)
	}
	if mode == config.SandboxAuthToken && plugin.UsesClaudeAuthName(pluginName) {
		if os.Getenv(oauthTokenEnv) != "" {
			id.ForwardOAuthToken = true
		} else {
			// Said out loud rather than silently degraded. This is the exact
			// state that reads as a broken feature: the pane opens, claude
			// asks the user to sign in, and nothing anywhere connects that
			// prompt to a token the daemon could not find. The remedy is in
			// the message because the daemon's environment is the one place
			// a user would not think to look.
			log.Printf("sandbox: pane %s: [sandbox] auth is the token flow but %s is not set "+
				"in the DAEMON's environment — the pane will ask you to sign in inside the "+
				"container instead. Run `claude setup-token`, export the result where quild "+
				"runs, and restart the daemon; or set [sandbox] auth = \"browser\" to choose "+
				"the per-pane sign-in deliberately", pane.ID, oauthTokenEnv)
		}
	}
	if runtime.GOOS != "windows" {
		// Docker Desktop on Windows ignores uids. On a Linux host — the
		// remote-daemon case — the per-pane tree is 0700 as the daemon user,
		// so an image running as some other uid cannot write its hook spool
		// or persist a sign-in, and the hook swallows the errors.
		id.User = strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())
	}
	return id
}

// oauthTokenEnv is the credential Claude Code reads for headless
// authentication. Quil never stores it: it is read from the daemon's own
// environment and forwarded by name.
const oauthTokenEnv = "CLAUDE_CODE_OAUTH_TOKEN"

// quilHomeLabel scopes a container to ONE daemon's data directory.
//
// Docker labels are engine-wide and the dev and production daemons share one
// engine, so a sweep filtering on quil.pane alone would have a dev daemon
// classify every production sandbox container as an orphan and force-remove
// it — killing the owner's live agents, and breaking the project's one
// always-on isolation rule.
//
// The value is canonicalised before hashing: QuilDir returns $QUIL_HOME
// verbatim, so a trailing slash or a case difference between a TUI-spawned and
// a manually started daemon would otherwise make a daemon miss its own
// orphans. It fails safe either way — a mismatch never reaps another install's
// containers.
func quilHomeLabel(quilDir string) string {
	s := filepath.ToSlash(filepath.Clean(quilDir))
	s = strings.TrimSuffix(s, "/")
	if runtime.GOOS == "windows" {
		s = strings.ToLower(s)
	}
	return fmt.Sprintf("%08x", fnv32(s))
}

// fnv32 is FNV-1a. A label only has to distinguish two data directories on one
// machine, so it needs no cryptographic property and no dependency.
func fnv32(s string) uint32 {
	h := uint32(2166136261)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

// hookPaths tells a hook prep two things it used to conflate: where on the
// HOST to write its files, and what path to NAME them by in the argument or
// environment the child will read.
//
// For an ordinary pane those are the same directory. For a sandbox pane they
// differ, and that difference is the whole of the hook work: the settings file
// is written into the pane's own tree on the host, and claude loads it from
// inside the container, where the host path does not exist. Conflating them is
// how a container ends up handed a `--settings C:\Users\...` it cannot open.
type hookPaths struct {
	// HostDir is where files are actually created.
	HostDir string
	// RefDir is HostDir as the CHILD will see it.
	RefDir string
	// ExePath is the quild the hook command invokes, in the child's terms.
	// Empty means "resolve the running executable", which is right for a host
	// pane and wrong for a container.
	ExePath string
}

// copyOpencodeScript stages the plugin script into a pane's own tree.
//
// opencode's config content names the script by ABSOLUTE path and the loader
// refuses a relative one, so the file has to exist at a path the container can
// open. Copying beats mounting the shared directory: one more mount of a
// shared $QUIL_HOME subtree is exactly the shape that made the first version
// of this feature a cross-pane escape.
func copyOpencodeScript(dstQuilDir, _ string) error {
	src := opencodehook.ScriptPath(config.QuilDir())
	dst := opencodehook.ScriptPath(dstQuilDir)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	body, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, body, 0o600)
}

// hostHookPaths is the ordinary case: write and name the same directory.
func hostHookPaths(quilDir string) hookPaths {
	return hookPaths{HostDir: quilDir, RefDir: quilDir}
}

// containerHookPaths writes into the pane's own tree and names it as the
// container sees it.
//
// ExePath stays empty when no hook binary was mounted, and the preps then
// disable hooks for the pane — which is the honest outcome. Naming a path
// nothing mounted would have claude invoke a file that is not there on every
// hook event.
func containerHookPaths(m sandbox.Mapping) hookPaths {
	hp := hookPaths{
		HostDir: m.HostPaneRoot,
		RefDir:  sandbox.ContainerQuil,
	}
	if m.HostQuild != "" {
		hp.ExePath = sandbox.ContainerQuild
	}
	return hp
}

// exe resolves the quild path the hook command should invoke.
func (hp hookPaths) exe() (string, error) {
	if hp.ExePath != "" {
		return hp.ExePath, nil
	}
	if hp.RefDir != hp.HostDir {
		// A container with no mounted quild. Answering the HOST executable
		// here would register a hook command naming a path that does not
		// exist inside the container.
		return "", fmt.Errorf("no linux hook binary is mounted for this pane")
	}
	return quildExeFn()
}

// ref restates a path that was built under HostDir in the child's terms.
//
// A path that is not under HostDir is returned unchanged: the callers build
// every path they pass from HostDir, so anything else is a bug worth leaving
// visible rather than silently rewriting.
func (hp hookPaths) ref(hostPath string) string {
	if hp.RefDir == hp.HostDir || hostPath == "" {
		return hostPath
	}
	rel, err := filepath.Rel(hp.HostDir, hostPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return hostPath
	}
	return hp.RefDir + "/" + filepath.ToSlash(rel)
}

// wrapInContainer replaces the pane's command with the `docker run` that
// starts it inside the container.
//
// It is the LAST transformation in spawnPane, after the hook switch and after
// shellinit — both of which can replace cmd and args, and both of which must
// see the command the container will actually run rather than the docker CLI.
//
// pane.CWD is deliberately left alone by the caller. It is the HOST path, and
// the git subsystem, the close dialog and ownedWorktreePaths all read it; the
// agent's working directory comes from `docker run -w` instead.
func (d *Daemon) wrapInContainer(m sandbox.Mapping, pane *Pane, p *plugin.PanePlugin,
	image, cmd string, args, envVars []string) (string, []string) {

	// A plugin's own [command] env still has to reach the child, and inside a
	// container it can only do so through docker's own -e. Anything the
	// container defines for itself is dropped with a log line rather than
	// forwarded: docker takes the LAST -e for a repeated name, so a plugin
	// setting QUIL_HOOK_HOME or GIT_OBJECT_DIRECTORY would silently repoint
	// the hook spool or the object store at a path outside the mount set.
	// Dropping it silently would be its own bug, so the author sees why.
	var extra []string
	for _, e := range envVars {
		if isReservedSandboxEnv(e) {
			log.Printf("sandbox: pane %s: %q is defined by the container — ignoring the pane-env copy",
				pane.ID, e)
			continue
		}
		extra = append(extra, e)
	}

	id := d.sandboxIdentity(pane, p.Name, hookModeFor(d.cfg, p), p.Command.RecordHistory)
	runArgs := sandbox.RunArgs(sandbox.Spec{Image: image}, m, id, runtime.GOOS, extra, cmd, args)

	dockerPath := dockerBinaryPath()
	pane.PluginMu.Lock()
	pane.ContainerCWD = m.ContainerCWD()
	pane.PluginMu.Unlock()

	log.Printf("sandbox: pane %s: image=%s workdir=%s kind=%d", pane.ID, image, m.ContainerCWD(), m.Kind)
	return dockerPath, runArgs
}

// isReservedSandboxEnv reports names the container defines for itself, which a
// plugin's own [command] env must not redefine.
func isReservedSandboxEnv(entry string) bool {
	name, _, _ := strings.Cut(entry, "=")
	switch name {
	case "QUIL_HOOK_HOME", "QUIL_HOOK_EXE", "QUIL_PANE_ID",
		"CLAUDE_CONFIG_DIR", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES":
		return true
	}
	return strings.HasPrefix(name, "GIT_CONFIG_")
}

// dockerCLIEnv is the environment the docker CLI itself runs with.
//
// Almost nothing: the container's environment travels through `docker run -e`,
// and handing the CLI the pane's env as well would put QUIL_HOOK_HOME and
// friends — host paths — into a process that has no use for them.
//
// The token is the exception and the reason this function exists. It is passed
// to `docker run` by NAME, so docker reads the VALUE from its own environment.
// That is what keeps it out of argv, and therefore out of quild.log, which the
// F1 log viewer renders on screen.
// Takes the RESOLVED mode, not the raw config string: this and
// sandboxIdentity must agree about which flow is in effect, and the two used
// to reach that answer by separately comparing against the literal "token" —
// so a change to the accepted spellings had to be made in both or the CLI
// would be handed a token the identity had decided not to forward.
func dockerCLIEnv(mode config.SandboxAuthMode) []string {
	if mode != config.SandboxAuthToken {
		return nil
	}
	if v := os.Getenv(oauthTokenEnv); v != "" {
		return []string{oauthTokenEnv + "=" + v}
	}
	return nil
}

// dockerBinaryPath resolves the docker CLI once, falling back to the bare name
// so the spawn produces docker's own "not found" rather than a Quil error for
// a condition the capability probe has already reported.
func dockerBinaryPath() string {
	if p, err := exec.LookPath("docker"); err == nil {
		return p
	}
	return "docker"
}

// gitConfigFn is the seam the identity tests drive.
var gitConfigFn = func(key string) (string, error) {
	cmd := exec.Command("git", "config", "--get", key)
	hideGitWindow(cmd)
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// gitConfigValue reads one global git setting, or "" when there is none.
//
// Without an identity a commit inside the container fails with "Author
// identity unknown": the host's global config is not mounted, deliberately,
// since it can carry credential helpers and fsmonitor commands that name host
// binaries. Passing just these two values is the narrow alternative.
func gitConfigValue(key string) string {
	v, err := gitConfigFn(key)
	if err != nil {
		return ""
	}
	return v
}
