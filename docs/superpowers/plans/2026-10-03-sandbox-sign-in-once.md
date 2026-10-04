# Sandbox: sign in once, remember the image, F1 page — Implementation Plan

> **Status: IMPLEMENTED** (issue #251). Kept as the record of the design steps; do not re-execute it. Deviations are listed in the PR description.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A per-pane "Shared" sign-in choice so Claude sandbox panes sign in once, a per-host remembered image, and an F1 → Settings → Sandbox page holding the Ctrl+N defaults.

**Architecture:** One new wire/persisted field (`SandboxSpec.ClaudeConfig` → `Pane.SandboxClaudeConfig`) decided through ONE pure function used by both spawn and resume. The TUI row becomes a three-way choice mapped by one table to the two wire fields, always sent explicitly. The image memory is a client-side per-destination JSON file behind a store the `Model` holds. The F1 page is a submenu screen editing `[sandbox]` config keys.

**Tech Stack:** Go 1.25, Bubble Tea v2, BurntSushi/toml. No new dependency.

**Spec:** `docs/superpowers/specs/2026-10-03-sandbox-sign-in-once-design.md`

## Global Constraints

- Defaults do not change: browser sign-in, own directory, empty `default_image`.
- `"token"` is selected only by the exact string (`ResolveAuth` rule). Never by a fallback.
- Values from the wire are validated in `applySandboxSpec`; unknown values are logged BY LENGTH, never content, and dropped to `""`.
- `plugin.UsesClaudeAuthName` gates every Claude-auth decision; the shared mount joins it.
- No test may write under the real `~/.quil`: any test touching `config.QuilDir()` sets `t.Setenv("QUIL_HOME", t.TempDir())`.
- Go: tabs, gofmt only the files you touched (`gofmt -l <files>`), never directory-wide.
- Test command (one package, optional `-run`), from the repo root in Git Bash:
  `docker run --rm -v "$(pwd -W)":/src -v quil-gomod:/go/pkg/mod -v quil-gocache:/root/.cache/go-build -w //src golang:1.25 go test ./internal/<pkg>/ -run '<Regex>'`
  Below this is written `GT <pkg> '<Regex>'`.
- Commits: no intermediate commits. All work lands as ONE commit at the end (owner rule). Stage by path, never `git add -A`. No AI attribution. `Ref #251`, never a closing keyword.

## Review Focus

1. A `"shared"` pane on a daemon whose config says off must RESUME its session after a restart (resume and mount agree) — pinned in Task 2.
2. A codex/opencode container must never receive the shared Claude directory, config on or off — pinned in Task 2.
3. An untouched dialog row must send the resolved default explicitly, and `"shared"` must never appear in `SandboxSpec.Auth` — pinned in Task 4.
4. The remembered image must be scoped to the dialog's PINNED destination, not the active project — pinned in Task 5.
5. A legacy `auth = "token"` + `shared_claude_config = true` config must display Token on the page and in the dialog, and saving any option must write both keys — pinned in Task 6.

---

### Task 1: Wire + persisted field `claude_config`

**Files:**
- Modify: `internal/ipc/protocol.go:426-451` (SandboxSpec)
- Modify: `internal/ipc/state.go:92-93` (PaneState)
- Modify: `internal/daemon/session.go:183-191` (Pane)
- Modify: `internal/daemon/sandbox_spawn.go:66-87` (applySandboxSpec)
- Modify: `internal/daemon/daemon.go:1090,1139` (restore), `:5054-5057` (typed snapshot builder)
- Modify: `internal/daemon/state_oracle_test.go:208` (add key ONLY, as `size_seq` was added)
- Test: `internal/daemon/sandbox_signin_test.go`, `internal/ipc/state_test.go`

**Interfaces:**
- Produces: `ipc.SandboxSpec.ClaudeConfig string` (`json:"claude_config,omitempty"`); constants in `internal/config/config.go`: `SandboxClaudeConfigOwn = "own"`, `SandboxClaudeConfigShared = "shared"`; `Pane.SandboxClaudeConfig string` (PluginMu); `ipc.PaneState.SandboxClaudeConfig *string` (`json:"sandbox_claude_config,omitempty"`).

- [ ] **Step 1: Failing test** — add to `internal/daemon/sandbox_signin_test.go`:

```go
// Any IPC client can set claude_config, and the two values mount different
// directories — one of them a trust domain shared with other panes. An unknown
// value follows the config rather than being stored and acted on.
func TestApplySandboxSpec_RecordsTheClaudeConfigChoice(t *testing.T) {
	tests := []struct{ name, wire, want string }{
		{"own is recorded", "own", "own"},
		{"shared is recorded", "shared", "shared"},
		{"absent follows the config", "", ""},
		{"unknown is refused", "everyone", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pane := &Pane{ID: "p1"}
			if err := applySandboxSpec(pane, &ipc.SandboxSpec{Image: "img:1", ClaudeConfig: tt.wire}); err != nil {
				t.Fatalf("applySandboxSpec: %v", err)
			}
			pane.PluginMu.Lock()
			got := pane.SandboxClaudeConfig
			pane.PluginMu.Unlock()
			if got != tt.want {
				t.Errorf("SandboxClaudeConfig = %q, want %q", got, tt.want)
			}
		})
	}
}
```

And in `internal/ipc/state_test.go` line 38-39, add `SandboxClaudeConfig: &empty` to the struct literal and `"sandbox_claude_config"` to the key list (a present-but-empty pointer must still marshal).

- [ ] **Step 2: Run** `GT daemon 'TestApplySandboxSpec_RecordsTheClaudeConfigChoice'` — Expected: compile FAIL (`unknown field ClaudeConfig`).

- [ ] **Step 3: Implement.**

`internal/config/config.go`, after the `SandboxAuthBrowser` const block:

```go
// The two values a pane's Claude config directory choice can name. "" is not
// among them: it means "follow [sandbox] shared_claude_config".
const (
	SandboxClaudeConfigOwn    = "own"
	SandboxClaudeConfigShared = "shared"
)
```

`internal/ipc/protocol.go`, in `SandboxSpec` after `Auth`:

```go
	// ClaudeConfig picks THIS pane's Claude config directory: "own" (its own,
	// the default) or "shared" (one directory for every pane that chose it,
	// so the user signs in once). Empty follows [sandbox] shared_claude_config,
	// which is what every older client, every restore of an older snapshot and
	// MCP without the field send.
	//
	// Per-pane so a config change never moves an existing pane's transcripts,
	// and so the shared trust domain holds only the panes that opted into it.
	// Validated like Auth; an unknown value follows the config.
	ClaudeConfig string `json:"claude_config,omitempty"`
```

`internal/ipc/state.go` after `SandboxAuth`:

```go
	// SandboxClaudeConfig is present (possibly "") whenever SandboxImage is set.
	SandboxClaudeConfig *string `json:"sandbox_claude_config,omitempty"`
```

`internal/daemon/session.go` after `SandboxAuth string`:

```go
	// SandboxClaudeConfig is the Claude config directory choice this pane was
	// created with: "own", "shared", or empty to follow
	// [sandbox] shared_claude_config. PERSISTED, PluginMu-protected, written
	// once at creation. Persisted for the reason SandboxAuth is, and one more:
	// the resume path maps the transcript through it, so a pane restored under
	// the other directory would look for its session where it is not.
	SandboxClaudeConfig string
```

`internal/daemon/sandbox_spawn.go`, in `applySandboxSpec` after the auth switch:

```go
	// Validated like the auth mode: "shared" mounts a directory other panes'
	// agents can write, so only a value this build understands is stored.
	claudeConfig := ""
	switch spec.ClaudeConfig {
	case config.SandboxClaudeConfigOwn, config.SandboxClaudeConfigShared:
		claudeConfig = spec.ClaudeConfig
	case "":
	default:
		log.Printf("pane %s: ignoring unknown sandbox claude_config of length %d; using the configured default",
			pane.ID, len(spec.ClaudeConfig))
	}
```

and set `pane.SandboxClaudeConfig = claudeConfig` beside `pane.SandboxAuth = auth`.

`internal/daemon/daemon.go` restore (~1090): `sandboxClaudeConfig, _ := paneData["sandbox_claude_config"].(string)` and in the `Pane{...}` literal `SandboxClaudeConfig: sandboxClaudeConfig,` with the comment "Absent on an older snapshot → empty, which follows [sandbox] shared_claude_config — the behaviour those panes had."
Typed builder (~5055): beside `paneData.SandboxAuth = &auth` add
`cc := pane.SandboxClaudeConfig; paneData.SandboxClaudeConfig = &cc`.
Oracle `state_oracle_test.go:208`: add `paneData["sandbox_claude_config"] = pane.SandboxClaudeConfig` on the next line (a NEW key, the `size_seq` precedent; no existing line changes).

- [ ] **Step 4: Run** `GT daemon 'TestApplySandboxSpec|Typed_MatchesOld|Snapshot'` and `GT ipc ''` — Expected: PASS.

- [ ] **Step 5: Snapshot round trip test** — add to `internal/daemon/sandbox_signin_test.go` next to the existing SandboxAuth persistence test (grep `sandbox_auth` in `internal/daemon/*_test.go` for the helper that snapshots and restores; mirror it with `SandboxClaudeConfig: "shared"` and with the key absent → `""`). Run it; PASS.

### Task 2: One sharing decision for spawn and resume; Claude-only shared mount

**Files:**
- Create: `internal/daemon/sandbox_shared.go`
- Modify: `internal/daemon/sandbox_spawn.go:159-161`
- Modify: `internal/daemon/sandbox_resume.go:33-83`
- Modify: `internal/daemon/daemon.go:367-370`
- Test: `internal/daemon/sandbox_shared_test.go`, `internal/daemon/sandbox_callsite_test.go`, `internal/daemon/sandbox_resume_test.go:225-246`

**Interfaces:**
- Consumes: `Pane.SandboxClaudeConfig` (Task 1).
- Produces: `func sharesClaudeConfig(choice string, cfgShared bool, pluginName string) bool`; `func sharedClaudeConfigDir(quilDir string) string`; `func (d *Daemon) paneSharesClaudeConfig(pane *Pane, pluginName string) bool`; package var `sharedClaudeConfigDefault bool` + `setSharedClaudeConfigDefault(bool)` (replaces `sharedClaudeRoot`/`setSharedClaudeRoot`).

- [ ] **Step 1: Failing tests** — `internal/daemon/sandbox_shared_test.go`:

```go
package daemon

import "testing"

// The one rule both the mount and the resume path read. A pane's own choice
// wins over the config; a non-Claude agent never joins the Claude trust domain.
func TestSharesClaudeConfig(t *testing.T) {
	tests := []struct {
		name, choice, plugin string
		cfg, want            bool
	}{
		{"own beats config on", "own", "claude-code", true, false},
		{"shared beats config off", "shared", "claude-code", false, true},
		{"empty follows config on", "", "claude-code", true, true},
		{"empty follows config off", "", "claude-code", false, false},
		{"codex never shares, config on", "", "codex", true, false},
		{"opencode never shares even if asked", "shared", "opencode", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sharesClaudeConfig(tt.choice, tt.cfg, tt.plugin); got != tt.want {
				t.Errorf("sharesClaudeConfig(%q, %v, %q) = %v, want %v", tt.choice, tt.cfg, tt.plugin, got, tt.want)
			}
		})
	}
}
```

Call-site tests in `internal/daemon/sandbox_callsite_test.go` (they use `sandboxCallsiteFixture`):

```go
// The mount follows the PANE's choice, not the config: a Shared pane on a
// config-off daemon must mount the shared directory, an Own pane on a
// config-on daemon must not, and a codex container never does.
func TestPrepareSandbox_SharedRootFollowsThePaneChoice(t *testing.T) {
	for _, tc := range []struct {
		name, choice, plugin string
		cfgShared, want      bool
	}{
		{"shared pane, config off", "shared", "claude-code", false, true},
		{"own pane, config on", "own", "claude-code", true, false},
		{"codex pane, config on", "", "codex", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, pane, _ := sandboxCallsiteFixture(t)
			d.cfg = config.Default()
			d.cfg.Sandbox.SharedClaudeConfig = tc.cfgShared
			pane.SandboxClaudeConfig = tc.choice
			stubNoSavedToken(t)
			m, err := d.prepareSandbox(context.Background(), pane, tc.plugin, "img:1")
			if err != nil {
				t.Fatalf("prepareSandbox: %v", err)
			}
			if got := m.SharedClaudeRoot != ""; got != tc.want {
				t.Errorf("SharedClaudeRoot = %q, want shared=%v", m.SharedClaudeRoot, tc.want)
			}
		})
	}
}
```

Rewrite `TestHostTranscriptPath_FollowsSharedMode` (`sandbox_resume_test.go:225`) as a table: `{choice "shared", cfg false → under shared dir}`, `{choice "own", cfg true → under per-pane dir}`, `{choice "", cfg true → shared}`; each case sets `QUIL_HOME`, creates both dirs (`sharedClaudeConfigDir(home)`, `sandboxClaudeConfigDir(home, "pane1")`), sets `prev := sharedClaudeConfigDefault; setSharedClaudeConfigDefault(tc.cfg); t.Cleanup(func(){ sharedClaudeConfigDefault = prev })`, and builds `&Pane{ID: "pane1", Type: "claude-code", SandboxImage: "img", SandboxClaudeConfig: tc.choice}`.

- [ ] **Step 2: Run** `GT daemon 'TestSharesClaudeConfig|SharedRootFollows|FollowsSharedMode'` — Expected: compile FAIL.

- [ ] **Step 3: Implement** `internal/daemon/sandbox_shared.go`:

```go
package daemon

import (
	"path/filepath"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/plugin"
)

// sharesClaudeConfig decides whether a sandbox pane mounts the ONE shared
// Claude config directory instead of its own.
//
// The single rule, read by both the mount (prepareSandbox) and the resume
// path (hostTranscriptPath). Splitting them mounts one directory and looks
// for the transcript in the other — a restored pane then passes
// --session-id for a session that has a transcript, and claude exits 129.
//
// A non-Claude agent never shares: the directory holds Claude's user-scope
// hooks and MCP servers, so a codex or opencode container with write access
// could plant code every Shared Claude pane then runs.
func sharesClaudeConfig(choice string, cfgShared bool, pluginName string) bool {
	if !plugin.UsesClaudeAuthName(pluginName) {
		return false
	}
	switch choice {
	case config.SandboxClaudeConfigShared:
		return true
	case config.SandboxClaudeConfigOwn:
		return false
	}
	return cfgShared
}

// sharedClaudeConfigDir is the only spelling of the shared directory's path.
func sharedClaudeConfigDir(quilDir string) string {
	return filepath.Join(sandboxRoot(quilDir), "claude")
}

// paneSharesClaudeConfig is the Daemon-side reader, mirroring paneAuthMode.
func (d *Daemon) paneSharesClaudeConfig(pane *Pane, pluginName string) bool {
	pane.PluginMu.Lock()
	choice := pane.SandboxClaudeConfig
	pane.PluginMu.Unlock()
	return sharesClaudeConfig(choice, d.cfg.Sandbox.SharedClaudeConfig, pluginName)
}
```

`sandbox_spawn.go:159-161` becomes:

```go
	if d.paneSharesClaudeConfig(pane, pluginName) {
		m.SharedClaudeRoot = sharedClaudeConfigDir(quilDir)
	}
```

`sandbox_resume.go`: replace the `sharedClaudeRoot` var/setter (lines 76-83) with:

```go
// sharedClaudeConfigDefault is [sandbox] shared_claude_config, recorded at
// daemon start for the resume path, which has no Daemon. A pane's own
// recorded choice overrides it — see sharesClaudeConfig.
var sharedClaudeConfigDefault bool

// setSharedClaudeConfigDefault records the config default. Called once at
// daemon start.
func setSharedClaudeConfigDefault(on bool) { sharedClaudeConfigDefault = on }
```

and in `hostTranscriptPath`, read `pane.SandboxClaudeConfig` and `pane.Type` inside the existing PluginMu span (beside `sandboxed`), then:

```go
	hostRoot := sandboxClaudeConfigDir(config.QuilDir(), pane.ID)
	if sharesClaudeConfig(choice, sharedClaudeConfigDefault, typ) {
		hostRoot = sharedClaudeConfigDir(config.QuilDir())
	}
```

Check first that `pane.Type` is read under PluginMu elsewhere (grep `pane.Type` in `daemon.go`); if it is read without the lock elsewhere, read it the same way here.

`daemon.go:367-370` becomes `setSharedClaudeConfigDefault(cfg.Sandbox.SharedClaudeConfig)` (unconditional, comment: "Recorded for the resume path, which has no Daemon to ask.").

- [ ] **Step 4: Run** `GT daemon 'Sandbox|HostTranscript|Shares'` — Expected: PASS. Then full `./scripts/dev.sh test internal/daemon` — PASS.

- [ ] **Step 5: Mutation check** — temporarily change `sandbox_spawn.go` back to `if d.cfg.Sandbox.SharedClaudeConfig {` and run `GT daemon 'SharedRootFollows'`; Expected: FAIL. Revert. Same for removing the plugin gate in `sharesClaudeConfig` → `TestSharesClaudeConfig` FAILs. Revert and confirm `git diff` shows only intended code.

### Task 3: User-facing daemon text

**Files:**
- Modify: `internal/daemon/sandbox_signin.go:~245`
- Modify: `internal/daemon/sandbox_spawn.go:352-356`

- [ ] **Step 1:** Read `sandbox_signin.go` around the string "This happens once". Change it so it describes the token mode only, e.g. "This happens once: every TOKEN sandbox pane after this is signed in. Browser and Shared panes sign in inside their container." Keep the existing wording style and line breaks.
- [ ] **Step 2:** In `sandboxIdentity`'s log line replace `or set [sandbox] auth = \"browser\" to choose the per-pane sign-in deliberately` with `or pick Browser or Shared (sign in once) in the Ctrl+N dialog or F1 → Settings → Sandbox`.
- [ ] **Step 3:** `grep -rn "This happens once" internal/daemon/*_test.go` — update any assertion on the old text. Run `GT daemon 'SignIn|Identity'` — PASS.

### Task 4: Ctrl+N three-way sign-in row

**Files:**
- Modify: `internal/tui/sandbox_field.go:203-330`
- Modify: `internal/tui/model.go:705-708`
- Test: `internal/tui/sandbox_field_test.go:574-660,690-720`

**Interfaces:**
- Consumes: `ipc.SandboxSpec.ClaudeConfig`, `config.SandboxClaudeConfigOwn/Shared` (Task 1).
- Produces: `func defaultSandboxSignIn(c config.SandboxConfig) string` (returns `"browser"|"shared"|"token"`) — Task 6 uses it; `func sandboxSignInFields(choice string) (auth, claudeConfig string)` — Task 6 uses the choice keys; `Model.sandboxSignIn string`; `func (m Model) effectiveSandboxSignIn() string`.

- [ ] **Step 1: Failing tests** — replace `TestEffectiveSandboxAuth_FollowsTheConfigUntilPicked`, `TestHandleSandboxAuthFieldKey`, `TestSandboxSpec_CarriesTheChosenAuth`, `TestResetSandboxField_ClearsTheAuthChoice` with:

```go
func TestDefaultSandboxSignIn(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		auth   string
		shared bool
		want   string
	}{
		{"default is browser", "", false, "browser"},
		{"browser + shared is shared", "browser", true, "shared"},
		{"token wins over shared (legacy pair)", "token", true, "token"},
		{"typo resolves to browser", "Token", false, "browser"},
	}
	for _, tt := range tests {
		c := config.SandboxConfig{Auth: tt.auth, SharedClaudeConfig: tt.shared}
		if got := defaultSandboxSignIn(c); got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestHandleSandboxAuthFieldKey_CyclesThreeChoices(t *testing.T) {
	t.Parallel()
	var m Model
	m.cfg = config.Default()
	want := []string{"browser", "shared", "token", "browser"}
	if got := m.effectiveSandboxSignIn(); got != want[0] {
		t.Fatalf("untouched row shows %q, want browser", got)
	}
	for _, w := range want[1:] {
		if !m.handleSandboxAuthFieldKey(tea.KeyPressMsg{Code: tea.KeyRight}) {
			t.Fatal("right was not consumed")
		}
		if got := m.effectiveSandboxSignIn(); got != w {
			t.Errorf("after right: %q, want %q", got, w)
		}
	}
	m.handleSandboxAuthFieldKey(tea.KeyPressMsg{Code: tea.KeyLeft})
	if got := m.effectiveSandboxSignIn(); got != "token" {
		t.Errorf("left wrapped to %q, want token", got)
	}
	for _, k := range []tea.KeyPressMsg{{Code: tea.KeyTab}, {Code: tea.KeyEnter}, {Code: tea.KeyEsc}} {
		if m.handleSandboxAuthFieldKey(k) {
			t.Errorf("%v was consumed by the sign-in row", k)
		}
	}
}

// The row sends what it SHOWS, so the TUI's default reaches a remote daemon,
// and "shared" can never land in Auth (applySandboxSpec would drop it).
func TestSandboxSpec_SendsTheDisplayedChoice(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, pick      string
		cfg             config.SandboxConfig
		auth, claudeCfg string
	}{
		{"untouched default", "", config.SandboxConfig{}, "browser", "own"},
		{"untouched, config shared", "", config.SandboxConfig{SharedClaudeConfig: true}, "browser", "shared"},
		{"picked shared", "shared", config.SandboxConfig{}, "browser", "shared"},
		{"picked token", "token", config.SandboxConfig{SharedClaudeConfig: true}, "token", "own"},
	}
	for _, tt := range tests {
		var m Model
		m.cfg.Sandbox = tt.cfg
		m.sandboxOn, m.sandboxImage, m.sandboxSignIn = true, "img", tt.pick
		spec := m.sandboxSpec()
		if spec == nil || spec.Auth != tt.auth || spec.ClaudeConfig != tt.claudeCfg {
			t.Errorf("%s: spec = %+v, want auth=%q claude_config=%q", tt.name, spec, tt.auth, tt.claudeCfg)
		}
	}
}

func TestResetSandboxField_ClearsTheSignInChoice(t *testing.T) {
	t.Parallel()
	var m Model
	m.sandboxSignIn = "shared"
	m.resetSandboxField("")
	if m.sandboxSignIn != "" {
		t.Errorf("the sign-in choice survived a reset: %q", m.sandboxSignIn)
	}
}
```

Update `TestRenderSetupDialog_DrawsTheSignInRow` (:690) to assert `"Shared"` appears in the frame.

- [ ] **Step 2: Run** `GT tui 'Sandbox'` — Expected: compile FAIL.

- [ ] **Step 3: Implement** in `sandbox_field.go`:

```go
// sandboxAuthChoices are the choices the row offers, in display order. A
// choice is NOT an auth mode: sandboxSignInFields maps it to the two wire
// fields, so "shared" can never reach SandboxSpec.Auth.
//
// Browser leads: it is the default and changes nothing outside the pane.
// Shared's detail names the trust domain; Token's names the machine-wide reach.
var sandboxAuthChoices = []struct{ choice, label, detail string }{
	{"browser", "Browser", "sign in in this container · full subscription"},
	{"shared", "Shared", "sign in once for all Shared panes · they share hooks, MCP servers, history"},
	{"token", "Token", "no sign-in · saves a token every later Claude uses"},
}

// sandboxSignInFields maps a row choice to (Auth, ClaudeConfig) on the wire.
// Token sends "own": a token pane from the dialog never enters the shared
// directory, so the shared directory's mode stamp cannot alternate.
func sandboxSignInFields(choice string) (auth, claudeConfig string) {
	switch choice {
	case "shared":
		return string(config.SandboxAuthBrowser), config.SandboxClaudeConfigShared
	case "token":
		return string(config.SandboxAuthToken), config.SandboxClaudeConfigOwn
	}
	return string(config.SandboxAuthBrowser), config.SandboxClaudeConfigOwn
}

// defaultSandboxSignIn is the choice a config selects. Token wins whatever
// shared_claude_config says (ResolveAuth decides the mode); browser + shared
// is Shared. Shared with the F1 page, so the two cannot disagree.
func defaultSandboxSignIn(c config.SandboxConfig) string {
	if mode, _ := c.ResolveAuth(); mode == config.SandboxAuthToken {
		return "token"
	}
	if c.SharedClaudeConfig {
		return "shared"
	}
	return "browser"
}
```

Rename `effectiveSandboxAuth` → `effectiveSandboxSignIn` returning `m.sandboxSignIn` or `defaultSandboxSignIn(m.cfg.Sandbox)`; rename `m.sandboxAuth` → `m.sandboxSignIn` everywhere (model.go field comment: "the sign-in CHOICE for this pane — browser, shared or token — empty until the user picks"); `renderSetupSandboxAuthField` and `stepSandboxAuth` iterate `c.choice` instead of `c.mode`; `sandboxSpec()` ends with:

```go
	auth, claudeConfig := sandboxSignInFields(m.effectiveSandboxSignIn())
	return &ipc.SandboxSpec{Image: image, Auth: auth, ClaudeConfig: claudeConfig}
```

Update the `sandboxSpec` doc comment: it now always sends the displayed choice, because an empty `Auth` resolved through the DAEMON's config, which for a remote project is another machine's.

Note: `sandboxSpec()` now returns a non-empty `Auth` for codex/opencode too. That is harmless (the daemon gates every Claude-auth use on `UsesClaudeAuthName`), but keep it exact: when `!p.UsesClaudeAuth()` is not knowable here, leave it — the daemon gate is the authority. Verify `grep -n "Auth" internal/daemon/sandbox_signin.go` shows every consumer behind `UsesClaudeAuthName` or `paneAuthMode` with a plugin gate.

- [ ] **Step 4: Run** `GT tui 'Sandbox|Setup'` — Expected: PASS. Then `./scripts/dev.sh test internal/tui` — PASS.

### Task 5: Remember the image per host

**Files:**
- Modify: `internal/config/config.go` (after `RecentCWDsPath`)
- Create: `internal/tui/sandbox_image_store.go`
- Modify: `internal/tui/model.go` (field + setter), `internal/tui/sandbox_field.go:103-108,170-176`, `internal/tui/dialog.go:2606`
- Modify: `cmd/quil/main.go:~713`
- Test: `internal/config/config_test.go`, `internal/tui/sandbox_image_store_test.go`

**Interfaces:**
- Produces: `func config.SandboxImagePath(dest string) string`; `func LoadSandboxImage(path string) string`; `func SaveSandboxImage(path, image string) error`; `type sandboxImageStore struct{ load func(dest string) string; save func(dest, image string) }`; `func (m *Model) SetSandboxImageStore(load func(string) string, save func(string, string))`; `func (m Model) sandboxImageDefault(dest string) string`.

- [ ] **Step 1: Failing tests.** `internal/config/config_test.go`:

```go
func TestSandboxImagePath_KeyedLikeRecentCWDs(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	if got := filepath.Base(SandboxImagePath("")); got != "sandbox-image.json" {
		t.Errorf("local = %q, want sandbox-image.json", got)
	}
	a, b := SandboxImagePath("a/b"), SandboxImagePath("a-b")
	if a == b {
		t.Error("two destinations share one file")
	}
	if strings.Contains(filepath.Base(SandboxImagePath("../../x")), "..") {
		t.Error("a traversal marker survived into the filename")
	}
}
```

`internal/tui/sandbox_image_store_test.go`:

```go
package tui

import (
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
)

func TestSandboxImageFile_RoundTrip(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	p := filepath.Join(t.TempDir(), "sandbox-image.json")
	if got := LoadSandboxImage(p); got != "" {
		t.Errorf("missing file loaded %q", got)
	}
	if err := SaveSandboxImage(p, "quil-sandbox:latest"); err != nil {
		t.Fatalf("save: %v", err)
	}
	if got := LoadSandboxImage(p); got != "quil-sandbox:latest" {
		t.Errorf("loaded %q", got)
	}
}

// Pre-fill: the host's last image, else default_image, else empty.
func TestSandboxImageDefault_LastUsedThenConfig(t *testing.T) {
	t.Parallel()
	var m Model
	m.cfg = config.Default()
	m.cfg.Sandbox.DefaultImage = "cfg:1"
	if got := m.sandboxImageDefault("hostA"); got != "cfg:1" {
		t.Errorf("no store: %q, want the config default", got)
	}
	mem := map[string]string{"hostA": "last:2"}
	m.SetSandboxImageStore(func(d string) string { return mem[d] }, func(d, i string) { mem[d] = i })
	if got := m.sandboxImageDefault("hostA"); got != "last:2" {
		t.Errorf("hostA: %q, want last:2", got)
	}
	if got := m.sandboxImageDefault("hostB"); got != "cfg:1" {
		t.Errorf("hostB saw hostA's image: %q", got)
	}
}

// Submitting a sandbox pane files the image under the dialog's PINNED
// destination — driven through the real key path, not the helper.
func TestCreatePaneSubmit_RemembersTheImageForThePinnedDest(t *testing.T) {
	m := sandboxKeyModel(t)
	mem := map[string]string{}
	m.SetSandboxImageStore(func(d string) string { return mem[d] }, func(d, i string) { mem[d] = i })
	m.sandboxImage = "quil-sandbox:latest"
	// Walk the dialog to the split step exactly as a user does; reuse the
	// helper the existing submit tests in sandbox_field_test.go use (grep
	// handleCreatePaneSplit in internal/tui/*_test.go) and assert:
	upd, _ := m.handleCreatePaneSplit()
	_ = upd.(Model)
	if mem[""] != "quil-sandbox:latest" {
		t.Errorf("remembered = %v, want the image under the local dest", mem)
	}
	_ = tea.KeyPressMsg{}
}
```

If `handleCreatePaneSplit` needs more state than `sandboxKeyModel` provides (target, tab), copy the setup from the nearest existing test that calls it; the assertion stays as written. Add a second case where `createPaneDialogDest()` returns a remote dest (set the field the dialog pins — grep `createPaneDest` in `dialog.go`) and assert the image is filed under that dest, not `""`.

- [ ] **Step 2: Run** `GT config 'SandboxImagePath'`, `GT tui 'SandboxImage|RemembersTheImage'` — Expected: compile FAIL.

- [ ] **Step 3: Implement.**

`internal/config/config.go`:

```go
// SandboxImagePath is where the client remembers the last sandbox image used
// on one destination, so the Ctrl+N dialog pre-fills it. Keyed like
// RecentCWDsPath and for the same reasons: an image that exists on one host's
// docker may not exist on another's, and the destination reaches a filename.
func SandboxImagePath(dest string) string {
	if dest == "" {
		return filepath.Join(QuilDir(), "sandbox-image.json")
	}
	return filepath.Join(QuilDir(), "sandbox-image-"+destFileKey(dest)+".json")
}
```

`internal/tui/sandbox_image_store.go`:

```go
package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// sandboxImageFile is the on-disk shape. An object rather than a bare string
// so a later field does not need a migration.
type sandboxImageFile struct {
	Image string `json:"image"`
}

// LoadSandboxImage reads the remembered image, or "" for a missing, linked or
// malformed file — a bad file costs a pre-fill, never a dialog.
func LoadSandboxImage(path string) string {
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var f sandboxImageFile
	if json.Unmarshal(data, &f) != nil || len(f.Image) > sandboxImageMax {
		return ""
	}
	return f.Image
}

// SaveSandboxImage writes it atomically (.tmp + rename), as SaveRecentCWDs does.
func SaveSandboxImage(path, image string) error {
	data, err := json.Marshal(sandboxImageFile{Image: image})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// sandboxImageStore is how the Model reaches the disk. Nil functions mean "no
// memory", which is what every Model built directly in a test gets — so no
// test writes under the real ~/.quil (the SetRecentCWDs precedent).
type sandboxImageStore struct {
	load func(dest string) string
	save func(dest, image string)
}

// SetSandboxImageStore installs the store. cmd/quil/main.go wires the
// file-backed one.
func (m *Model) SetSandboxImageStore(load func(string) string, save func(string, string)) {
	m.sandboxImages = sandboxImageStore{load: load, save: save}
}

// sandboxImageDefault is what the image field pre-fills with for dest.
func (m Model) sandboxImageDefault(dest string) string {
	if m.sandboxImages.load != nil {
		if img := m.sandboxImages.load(dest); img != "" {
			return img
		}
	}
	return m.cfg.Sandbox.DefaultImage
}

// rememberSandboxImage files a submitted image under dest, when it changed.
func (m Model) rememberSandboxImage(dest, image string) {
	if m.sandboxImages.save == nil || image == "" {
		return
	}
	if m.sandboxImages.load != nil && m.sandboxImages.load(dest) == image {
		return
	}
	m.sandboxImages.save(dest, image)
}
```

`model.go`: add `sandboxImages sandboxImageStore` beside `sandboxImage`.
`sandbox_field.go`: `resetSandboxField(dest)` uses `m.sandboxImageDefault(dest)`; the space-toggle branch uses `m.sandboxImageDefault(m.createPaneDialogDest())`.
`dialog.go:2606`, right after `sbox := m.sandboxSpec()`:

```go
	// Filed under the dialog's PINNED destination, like the recent CWDs: the
	// image is pulled by THAT host's docker.
	if sbox != nil {
		m.rememberSandboxImage(m.createPaneDialogDest(), sbox.Image)
	}
```

`cmd/quil/main.go`, after the project-groups block:

```go
	// The remembered sandbox image, per destination. Installed here rather
	// than in NewModel for the reason SetRecentCWDs is: tests build a Model
	// directly and must never touch the real ~/.quil.
	model.SetSandboxImageStore(
		func(dest string) string { return tui.LoadSandboxImage(config.SandboxImagePath(dest)) },
		func(dest, image string) {
			if err := tui.SaveSandboxImage(config.SandboxImagePath(dest), image); err != nil {
				log.Printf("sandbox: remember image: %v", err)
			}
		},
	)
```

- [ ] **Step 4: Run** `GT config ''`, `./scripts/dev.sh test internal/tui` — PASS.

### Task 6: F1 → Settings → Sandbox page

**Files:**
- Create: `internal/tui/dialog_sandboxsettings.go`
- Modify: `internal/tui/dialog.go:195-212` (settingsField flag), settings table (add row after "Notifications"), `:1031-1045` (Enter), `:722` (key dispatch), `:1483` (render dispatch)
- Modify: `internal/tui/model.go:435` (dialog kind)
- Test: `internal/tui/dialog_sandboxsettings_test.go`

**Interfaces:**
- Consumes: `defaultSandboxSignIn`, `sandboxAuthChoices` (Task 4).
- Produces: `dialogSandboxSettings`; `settingsField.sandboxSettings bool`.

- [ ] **Step 1: Failing tests** `internal/tui/dialog_sandboxsettings_test.go`:

```go
package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
)

func openSandboxSettings(t *testing.T, cfg config.Config) Model {
	t.Helper()
	m := Model{cfg: cfg, dialog: dialogSettings}
	m.width, m.height = 120, 40
	for i, f := range settingsFields() {
		if f.sandboxSettings {
			m.dialogCursor = i
		}
	}
	upd, _ := m.handleSettingsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = upd.(Model)
	if m.dialog != dialogSandboxSettings {
		t.Fatalf("Enter on the Sandbox row opened dialog %v", m.dialog)
	}
	return m
}

// Each choice writes BOTH keys, so the page can never write the mixed
// token + shared pair.
func TestSandboxSettings_SignInWritesBothKeys(t *testing.T) {
	m := openSandboxSettings(t, config.Default())
	m.dialogCursor = sandboxSettingsSignInRow
	want := []struct {
		auth   string
		shared bool
	}{{"browser", true}, {"token", false}, {"browser", false}}
	for _, w := range want {
		upd, _ := m.handleSandboxSettingsKey(tea.KeyPressMsg{Code: tea.KeyRight})
		m = upd.(Model)
		if m.cfg.Sandbox.Auth != w.auth || m.cfg.Sandbox.SharedClaudeConfig != w.shared {
			t.Errorf("got auth=%q shared=%v, want %q %v", m.cfg.Sandbox.Auth, m.cfg.Sandbox.SharedClaudeConfig, w.auth, w.shared)
		}
		if !m.configChanged {
			t.Error("configChanged not set — the edit is lost on exit")
		}
	}
}

func TestSandboxSettings_LegacyTokenSharedShowsToken(t *testing.T) {
	cfg := config.Default()
	cfg.Sandbox.Auth, cfg.Sandbox.SharedClaudeConfig = "token", true
	m := openSandboxSettings(t, cfg)
	if frame := m.renderSandboxSettingsDialog(); !strings.Contains(frame, "(•) Token") {
		t.Errorf("legacy pair not shown as Token:\n%s", frame)
	}
}

func TestSandboxSettings_DefaultImageEdit(t *testing.T) {
	m := openSandboxSettings(t, config.Default())
	m.dialogCursor = sandboxSettingsImageRow
	upd, _ := m.handleSandboxSettingsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = upd.(Model)
	for _, r := range "img:1" {
		upd, _ = m.handleSandboxSettingsKey(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = upd.(Model)
	}
	upd, _ = m.handleSandboxSettingsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = upd.(Model)
	if m.cfg.Sandbox.DefaultImage != "img:1" || !m.configChanged {
		t.Errorf("default_image = %q changed=%v", m.cfg.Sandbox.DefaultImage, m.configChanged)
	}
}

func TestSandboxSettings_EscReturnsToTheSandboxRow(t *testing.T) {
	m := openSandboxSettings(t, config.Default())
	upd, _ := m.handleSandboxSettingsKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = upd.(Model)
	if m.dialog != dialogSettings || !settingsFields()[m.dialogCursor].sandboxSettings {
		t.Errorf("Esc landed on dialog %v row %d", m.dialog, m.dialogCursor)
	}
}
```

- [ ] **Step 2: Run** `GT tui 'SandboxSettings'` — compile FAIL.

- [ ] **Step 3: Implement.**

`dialog.go` settingsField: add `sandboxSettings bool` with comment "opens F1 → Settings → Sandbox; a separate flag from submenu, matching templateSettings, so settingsSubmenuIndex keeps finding Notifications". New row right after the Notifications row:

```go
		{label: "Sandbox", get: func(m *Model) string { return "…" }, set: func(m *Model, _ string) {}, sandboxSettings: true},
```

Enter handler (`case f.templateSettings:` neighbour):

```go
		case f.sandboxSettings:
			m.dialog = dialogSandboxSettings
			m.dialogCursor = 0
			m.dialogEdit = false
			return m, tea.ClearScreen
```

`model.go`: add `dialogSandboxSettings // F1 → Settings → Sandbox: Ctrl+N defaults` after `dialogNotifySettings`. Key dispatch `case dialogSandboxSettings: return m.handleSandboxSettingsKey(msg)`; render dispatch `case dialogSandboxSettings: content = m.renderSandboxSettingsDialog()`.

`internal/tui/dialog_sandboxsettings.go`:

```go
package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/artyomsv/quil/internal/config"
)

// F1 → Settings → Sandbox: the Ctrl+N dialog's defaults.
//
// Two rows only, so no windowing (the Notifications screen needs it for
// fifteen). Both are CLIENT defaults: the dialog sends its choice on the wire,
// so they apply at once and for remote projects too. The daemon reads the same
// keys at its own start as the fallback for creates that name nothing (MCP,
// older clients).
const (
	sandboxSettingsImageRow  = 0
	sandboxSettingsSignInRow = 1
	sandboxSettingsRows      = 2
)

// setSandboxSignInDefault writes BOTH keys for a choice, so the page can never
// produce auth = "token" + shared_claude_config = true — the pair that lets a
// token pane and a browser pane share one directory and re-onboard each other.
func (m *Model) setSandboxSignInDefault(choice string) {
	auth, claudeConfig := sandboxSignInFields(choice)
	shared := claudeConfig == config.SandboxClaudeConfigShared
	if m.cfg.Sandbox.Auth == auth && m.cfg.Sandbox.SharedClaudeConfig == shared {
		return
	}
	m.cfg.Sandbox.Auth = auth
	m.cfg.Sandbox.SharedClaudeConfig = shared
	m.configChanged = true
}

func (m Model) handleSandboxSettingsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.dialogEdit {
		switch msg.String() {
		case "esc":
			m.dialogEdit = false
		case "enter":
			v := strings.TrimSpace(m.dialogInput)
			if v != m.cfg.Sandbox.DefaultImage && len([]rune(v)) <= sandboxImageMax {
				m.cfg.Sandbox.DefaultImage = v
				m.configChanged = true
			}
			m.dialogEdit = false
		case "backspace":
			if r := []rune(m.dialogInput); len(r) > 0 {
				m.dialogInput = string(r[:len(r)-1])
			}
		default:
			if t := msg.Text; t != "" && isPrintableText(t) {
				m.dialogInput += t
			}
		}
		return m, nil
	}
	switch msg.String() {
	case "esc":
		m.dialog = dialogSettings
		for i, f := range settingsFields() {
			if f.sandboxSettings {
				m.dialogCursor = i
			}
		}
		return m, tea.ClearScreen
	case "up", "k":
		if m.dialogCursor > 0 {
			m.dialogCursor--
		}
	case "down", "j":
		if m.dialogCursor < sandboxSettingsRows-1 {
			m.dialogCursor++
		}
	case "enter", " ", "space", "left", "h", "right", "l":
		switch m.dialogCursor {
		case sandboxSettingsImageRow:
			if k := msg.String(); k == "enter" || k == " " || k == "space" {
				m.dialogEdit = true
				m.dialogInput = m.cfg.Sandbox.DefaultImage
			}
		case sandboxSettingsSignInRow:
			delta := 1
			if k := msg.String(); k == "left" || k == "h" {
				delta = -1
			}
			cur := defaultSandboxSignIn(m.cfg.Sandbox)
			idx := 0
			for i, c := range sandboxAuthChoices {
				if c.choice == cur {
					idx = i
				}
			}
			n := len(sandboxAuthChoices)
			m.setSandboxSignInDefault(sandboxAuthChoices[((idx+delta)%n+n)%n].choice)
		}
	}
	return m, nil
}

func (m Model) renderSandboxSettingsDialog() string {
	inner := dialogInnerWidth(m.width, dialogWidth)
	row := func(i int, label, value string) string {
		cursor, style := "    ", dialogLabelStyle
		if i == m.dialogCursor {
			cursor, style = "  > ", style.Foreground(lipgloss.Color("230")).Bold(true)
		}
		return cursor + style.Render(label) + dialogValStyle.Render(value) + "\n"
	}

	var b strings.Builder
	b.WriteString(dialogTitle.Render("Sandbox") + "\n")
	b.WriteString(dialogSubtle.Render("  defaults for Ctrl+N · existing panes keep their mode") + "\n\n")

	img := sanitizeRemoteText(m.cfg.Sandbox.DefaultImage)
	if m.dialogEdit && m.dialogCursor == sandboxSettingsImageRow {
		img = sanitizeRemoteText(m.dialogInput) + "█"
	} else if img == "" {
		img = "(none)"
	}
	b.WriteString(row(sandboxSettingsImageRow, "Default image", truncateToWidth(img, inner-24)))
	b.WriteString("      " + dialogSubtle.Render(truncateToWidth(
		"first image for a host; after that Ctrl+N remembers the last one used there", inner-6)) + "\n")

	cur := defaultSandboxSignIn(m.cfg.Sandbox)
	var opts []string
	detail := ""
	for _, c := range sandboxAuthChoices {
		mark := "( ) "
		if c.choice == cur {
			mark, detail = "(•) ", c.detail
		}
		opts = append(opts, mark+c.label)
	}
	b.WriteString(row(sandboxSettingsSignInRow, "Sign-in", strings.Join(opts, "  ")))
	b.WriteString("      " + dialogSubtle.Render(truncateToWidth(detail, inner-6)) + "\n\n")

	b.WriteString(dialogSubtle.Render("  ↑↓ navigate  ←/→ change  Enter edit  Esc back"))
	return b.String()
}
```

Before writing, check `dialogLabelStyle`'s fixed width in `dialog.go` so the label column matches the Settings rows; if the image budget `inner-24` misaligns, compute it from `lipgloss.Width` of the rendered prefix instead.

- [ ] **Step 4: Run** `GT tui 'SandboxSettings|Settings'` — PASS. Then `./scripts/dev.sh test internal/tui` — PASS.

### Task 7: MCP `sandbox_claude_config`

**Files:**
- Modify: `cmd/quil/mcp_tools.go:296-323`
- Test: `cmd/quil/mcp_tools_test.go` (or the file holding `toReq` tests — grep `toReq(` in `cmd/quil/*_test.go`)

- [ ] **Step 1: Failing test:**

```go
func TestCreatePaneInput_CarriesTheClaudeConfigChoice(t *testing.T) {
	in := createPaneInput{SandboxImage: "img:1", SandboxClaudeConfig: "shared"}
	req := in.toReq("t1")
	if req.Sandbox == nil || req.Sandbox.ClaudeConfig != "shared" {
		t.Errorf("Sandbox = %+v, want claude_config shared", req.Sandbox)
	}
	if !(createPaneInput{SandboxClaudeConfig: "own"}).usesDialogOptions() {
		t.Error("the field is not version-checked with the other dialog options")
	}
}
```

- [ ] **Step 2: Run** `GT 'cmd/quil' 'ClaudeConfigChoice'` — compile FAIL.
- [ ] **Step 3: Implement:** field
`SandboxClaudeConfig string \`json:"sandbox_claude_config,omitempty" jsonschema:"claude-code sandbox config directory: own (default, sign in per pane) or shared (one directory for every shared pane: sign in once, but those panes share hooks, MCP servers and history); empty = daemon config"\``; add `|| in.SandboxClaudeConfig != ""` to `usesDialogOptions`; `req.Sandbox = &ipc.SandboxSpec{Image: in.SandboxImage, Auth: in.SandboxAuth, ClaudeConfig: in.SandboxClaudeConfig}`.
- [ ] **Step 4: Run** — PASS.

### Task 8: Docs and changelog

**Files:**
- Modify: `.claude/rules/sandbox.md` (new section "The Claude config directory is PER PANE" after "The sign-in mode is PER PANE"; amend "What token auth COSTS" last paragraph)
- Modify: `docs/sandbox-panes.md`, `docs/configuration.md` (`[sandbox]` keys: say they are Ctrl+N defaults and the daemon's fallback for MCP), `docs/mcp.md` (`create_pane` field), `internal/config/config.go` `SandboxConfig` comments (Auth, SharedClaudeConfig, DefaultImage now pre-fill only until a host has a remembered image)
- Create: `changelog.d/feat-sandbox-sign-in-once.md`

- [ ] **Step 1:** Write the rule section. Must say: one pure decision (`sharesClaudeConfig`) read by mount AND resume; the plugin gate and why (codex could plant hooks); Token from the dialog sends `own`; the remaining hand-edited token + shared + MCP case; downgrade fails toward `own`; the field cannot be version-gated.
- [ ] **Step 2:** Check fragment format: `cat changelog.d/README.md`, then `sh scripts/promote-changelog.sh --filter-names` usage per README. Write the fragment with a `headline:` front matter (no `"` or `\`), e.g. headline `Sandbox panes can sign in once, remember the image, and have a Settings page`.
- [ ] **Step 3:** `./scripts/dev.sh docs-size` — PASS.

### Task 9: Verification and PR

- [ ] **Step 1:** `./scripts/dev.sh vet` and the CI-exact command:
  `docker run --rm -v "$(pwd -W)":/src -v quil-gomod:/go/pkg/mod -v quil-gocache:/root/.cache/go-build -w //src golang:1.25 sh -c 'go test -race ./... && go test -tags=integration ./...'` — Expected: all `ok`.
- [ ] **Step 2:** `gofmt -l` on every touched `.go` file — Expected: no output.
- [ ] **Step 3:** `./scripts/dev.sh build`, then check binary mtimes are new (`ls -l --time-style=full-iso quil-dev.exe quild-dev.exe`).
- [ ] **Step 4: Live test (dev mode only, never `~/.quil`).** Launch `./quil-dev.exe` in a new Windows Terminal window (`wt.exe -w -1`), confirm `[dev]`. In repo `E:\Projects\Stukans\quil`:
  1. Ctrl+N → Claude Code → sandbox on: image pre-fills empty (first time) → type `quil-sandbox:latest`, choose Shared → pane signs in through the browser (owner does the browser step).
  2. Ctrl+N again: image pre-filled `quil-sandbox:latest`. Choose Shared → NO sign-in.
  3. Ctrl+N, choose Browser → this pane asks for its own sign-in.
  4. Restart the first Shared pane (Alt+R) → its session resumes.
  5. F1 → Settings → Sandbox: change Sign-in to Shared; reopen Ctrl+N → row shows Shared.
  Record what was observed. Nothing in step 1-5 needs the owner except the browser sign-in.
- [ ] **Step 5:** Code review: `superpowers:requesting-code-review` on the whole branch (fresh reviewer). Fix findings; re-run Step 1.
- [ ] **Step 6:** One commit, files staged by path (`git add <each file>`), message:

```
feat(sandbox): sign in once, remember the image, F1 settings page

<body: the three changes, the per-pane claude_config field, the
codex/opencode shared-mount fix, version gap>

Ref #251
```

  Spec and plan files are included in this commit (owner rule: no separate doc commits).
- [ ] **Step 7:** Push, open the PR with a conventional-commit title `feat(sandbox): sign in once, remember the image, F1 settings page`, body with summary + test evidence + `Ref #251`, no closing keyword, no AI attribution. Then read Greptile inline comments (`gh api repos/artyomsv/quil/pulls/<n>/comments`), fix or answer, resolve threads.
