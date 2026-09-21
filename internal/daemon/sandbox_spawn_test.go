package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/sandbox"
)

// The persisted type is the DOWNGRADE guard. Auto-update has a rollback path,
// and a daemon too old to read sandbox_image would restore a sandbox pane as
// an ordinary one pointed at the worktree — an agent on the host, silently.
// An unknown type takes the existing fallback to a plain shell instead.
func TestSandboxPaneType_RoundTrips(t *testing.T) {
	typ := sandboxPaneType("claude-code")
	if !strings.HasPrefix(typ, "sandbox/") {
		t.Fatalf("sandboxPaneType = %q — an older daemon would read this as a HOST "+
			"claude-code pane and start an un-sandboxed agent", typ)
	}
	plugin, sandboxed := splitSandboxType(typ)
	if plugin != "claude-code" || !sandboxed {
		t.Errorf("splitSandboxType(%q) = %q, %v; want claude-code, true", typ, plugin, sandboxed)
	}
	// An ordinary type is unchanged and not sandboxed, so every existing
	// snapshot restores exactly as before.
	plugin, sandboxed = splitSandboxType("claude-code")
	if plugin != "claude-code" || sandboxed {
		t.Errorf("splitSandboxType(plain) = %q, %v; want claude-code, false", plugin, sandboxed)
	}
}

// The hook paths a container gets. Getting ExePath wrong hands claude a hook
// command naming a binary that is not in the container; getting RefDir wrong
// points QUIL_HOOK_HOME at a host path the container cannot write.
func TestContainerHookPaths(t *testing.T) {
	m := sandbox.Mapping{
		PaneID:       "p1",
		HostPaneRoot: filepath.Join("/home/u/.quil", "sandbox", "panes", "p1"),
		HostQuild:    "/home/u/.quil/sandbox/bin/1.0.0/quild-linux-amd64",
	}
	hp := containerHookPaths(m)
	if hp.RefDir != sandbox.ContainerQuil {
		t.Errorf("RefDir = %q, want %q", hp.RefDir, sandbox.ContainerQuil)
	}
	if hp.ExePath != sandbox.ContainerQuild {
		t.Errorf("ExePath = %q, want %q", hp.ExePath, sandbox.ContainerQuild)
	}
	if hp.HostDir != m.HostPaneRoot {
		t.Errorf("HostDir = %q, want the pane's own tree", hp.HostDir)
	}

	// With no binary mounted the hook must be DISABLED, not pointed at a path
	// that does not exist inside the container.
	m.HostQuild = ""
	if got := containerHookPaths(m).ExePath; got != "" {
		t.Errorf("ExePath = %q with no mounted binary; want empty so the prep disables hooks", got)
	}
	if _, err := containerHookPaths(m).exe(); err == nil {
		t.Error("exe() answered a path for a container with no mounted quild")
	}
}

// ref() restates a host path in the child's terms. A host pane must be
// untouched, or every existing pane's settings path changes.
func TestHookPaths_RefIsIdentityForAHostPane(t *testing.T) {
	hp := hostHookPaths("/home/u/.quil")
	in := filepath.Join("/home/u/.quil", "sessions", "p1.settings.json")
	if got := hp.ref(in); got != in {
		t.Errorf("ref rewrote a host pane's path: %q", got)
	}
}

func TestHookPaths_RefMapsIntoTheContainer(t *testing.T) {
	root := filepath.Join("/home/u/.quil", "sandbox", "panes", "p1")
	hp := hookPaths{HostDir: root, RefDir: sandbox.ContainerQuil, ExePath: sandbox.ContainerQuild}
	got := hp.ref(filepath.Join(root, "sessions", "p1.settings.json"))
	if got != "/quil/sessions/p1.settings.json" {
		t.Errorf("ref = %q, want the container path", got)
	}
	// A path built outside HostDir is a bug worth leaving visible rather than
	// silently rewriting into the container's namespace.
	outside := filepath.Join("/somewhere/else", "x")
	if got := hp.ref(outside); got != outside {
		t.Errorf("ref rewrote a path from outside the pane tree: %q", got)
	}
}

// The overlays are what make git work inside the container, and the admin one
// is what stops `git worktree prune` deleting the host's registration —
// measured data loss, with uncommitted work in it.
func TestWriteOverlays_WritesBothWithContainerPaths(t *testing.T) {
	dir := t.TempDir()
	m := sandbox.Mapping{
		PaneID:         "p1",
		Kind:           sandbox.KindWorktree,
		AdminName:      "wt",
		Slug:           "wt-abcd1234",
		HostOverlayDir: dir,
	}
	if err := writeOverlays(m); err != nil {
		t.Fatalf("writeOverlays: %v", err)
	}
	for _, tc := range []struct{ path, want string }{
		{m.HostDotGitOverlay(), "gitdir: /repo/.git/worktrees/wt\n"},
		{m.HostAdminGitdirOverlay(), "/work/wt-abcd1234/.git\n"},
	} {
		body, err := os.ReadFile(tc.path)
		if err != nil {
			t.Fatalf("read %s: %v", tc.path, err)
		}
		if string(body) != tc.want {
			t.Errorf("%s = %q, want %q", filepath.Base(tc.path), body, tc.want)
		}
		if strings.Contains(string(body), `\`) {
			t.Errorf("%s has a backslash; the container cannot resolve it", filepath.Base(tc.path))
		}
	}
}

// An ordinary checkout has a real .git directory, so overlaying anything on it
// would break the repository it is trying to describe.
func TestWriteOverlays_SkipsAnOrdinaryCheckout(t *testing.T) {
	dir := t.TempDir()
	m := sandbox.Mapping{PaneID: "p1", Kind: sandbox.KindCheckout, HostOverlayDir: dir}
	if err := writeOverlays(m); err != nil {
		t.Fatalf("writeOverlays: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("an ordinary checkout got %d overlay files", len(entries))
	}
}

// A plugin's own [command] env reaches the container through docker -e, and
// docker takes the LAST -e for a repeated name — so a plugin redefining these
// would repoint the hook spool or the object store outside the mount set.
func TestIsReservedSandboxEnv(t *testing.T) {
	reserved := []string{
		"QUIL_HOOK_HOME=/elsewhere",
		"QUIL_HOOK_EXE=/bin/sh",
		"QUIL_PANE_ID=other",
		"CLAUDE_CONFIG_DIR=/tmp",
		"GIT_OBJECT_DIRECTORY=/tmp",
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=/tmp",
		"GIT_CONFIG_COUNT=9",
		"GIT_CONFIG_KEY_0=core.hooksPath",
	}
	for _, e := range reserved {
		if !isReservedSandboxEnv(e) {
			t.Errorf("%q is not treated as reserved", e)
		}
	}
	for _, e := range []string{"FOO=bar", "PATH=/usr/bin", "QUIL_RECORD_HISTORY=1"} {
		if isReservedSandboxEnv(e) {
			t.Errorf("%q was wrongly refused; a plugin may set it", e)
		}
	}
}

// The token reaches the docker CLI through its own environment, never argv.
//
// Driven through ResolveAuth rather than by handing dockerCLIEnv a literal:
// this used to pass the raw config string, so `dockerCLIEnv("")` asserted that
// the DEFAULT forwards nothing — which was true then and is a state that can
// no longer occur, since "" now resolves to the token flow. A test that keeps
// passing while describing an unreachable case is the trap; going through the
// resolver keeps it describing the real configuration.
func TestDockerCLIEnv_OnlyCarriesTheTokenUnderTokenAuth(t *testing.T) {
	t.Setenv(oauthTokenEnv, "secret-value")

	browser, _ := config.SandboxConfig{Auth: "browser"}.ResolveAuth()
	if env := dockerCLIEnv(browser); len(env) != 0 {
		t.Errorf("the browser fallback passed env to the docker CLI: %v", env)
	}

	// Only the named token flow forwards it. The empty value every existing
	// config.toml carries now means the browser flow, so it is covered by the
	// row above rather than here — a credential the user did not ask for must
	// not reach a container.
	mode, _ := config.SandboxConfig{Auth: "token"}.ResolveAuth()
	env := dockerCLIEnv(mode)
	if len(env) != 1 || !strings.HasPrefix(env[0], oauthTokenEnv+"=") {
		t.Errorf("auth=\"token\" env = %v, want the token variable", env)
	}

	unset, _ := config.SandboxConfig{}.ResolveAuth()
	if env := dockerCLIEnv(unset); len(env) != 0 {
		t.Errorf("an unset auth passed env to the docker CLI: %v", env)
	}
}

func TestDockerCLIEnv_NothingToForward(t *testing.T) {
	t.Setenv(oauthTokenEnv, "")
	mode, _ := config.SandboxConfig{Auth: "token"}.ResolveAuth()
	if env := dockerCLIEnv(mode); len(env) != 0 {
		t.Errorf("env = %v with no token set", env)
	}
}

// sandboxIdentity and dockerCLIEnv must agree: the identity decides whether
// RunArgs emits `-e CLAUDE_CODE_OAUTH_TOKEN`, and dockerCLIEnv decides whether
// the docker CLI has a value under that name to forward. One without the other
// is either a name docker cannot resolve or a value nothing asks for.
func TestSandboxAuth_IdentityAndCLIEnvAgree(t *testing.T) {
	t.Setenv(oauthTokenEnv, "secret-value")

	for _, tc := range []struct {
		auth        string
		wantForward bool
	}{
		{"", false},     // every config already on disk — now the browser flow
		{"token", true}, // explicit, the only value that forwards
		{"browser", false},
		{"typo", false}, // unrecognised falls back
	} {
		d := &Daemon{cfg: config.Default()}
		d.cfg.Sandbox.Auth = tc.auth
		id := d.sandboxIdentity(&Pane{ID: "pane-1"}, "claude-code", "", false)
		mode, _ := d.cfg.Sandbox.ResolveAuth()
		gotEnv := len(dockerCLIEnv(mode)) == 1

		if id.ForwardOAuthToken != tc.wantForward {
			t.Errorf("auth=%q ForwardOAuthToken = %v, want %v",
				tc.auth, id.ForwardOAuthToken, tc.wantForward)
		}
		if gotEnv != id.ForwardOAuthToken {
			t.Errorf("auth=%q: identity forwards=%v but the CLI env has the value=%v — "+
				"docker is handed a name it cannot resolve, or a value nothing asks for",
				tc.auth, id.ForwardOAuthToken, gotEnv)
		}
	}
}

// The token flow with no token in the daemon's environment must NOT claim to
// forward one: RunArgs would emit a bare `-e CLAUDE_CODE_OAUTH_TOKEN` that
// docker resolves to nothing, and claude would see an empty credential rather
// than none.
func TestSandboxIdentity_TokenFlowWithNoTokenForwardsNothing(t *testing.T) {
	t.Setenv(oauthTokenEnv, "")
	d := &Daemon{cfg: config.Default()}

	if d.sandboxIdentity(&Pane{ID: "pane-1"}, "claude-code", "", false).ForwardOAuthToken {
		t.Error("forwarded a token that is not set")
	}
}

// quilHomeLabel must canonicalise, or a TUI-spawned and a manually started
// daemon disagree about their own containers.
func TestQuilHomeLabel_StableAcrossSpelling(t *testing.T) {
	if quilHomeLabel("/home/u/.quil") != quilHomeLabel("/home/u/./.quil/") {
		t.Error("an equivalent spelling produced a different label")
	}
}
