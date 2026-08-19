# Claude Session Seam Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix how Quil delivers Claude's hook settings, resolve Claude's config directory, and read transcripts robustly — and replace six hardcoded `"claude-code"` checks with one derived capability.

**Architecture:** Six independent changes across four packages. `internal/claudehook` gains a settings-file writer so the hook survives Windows `.cmd` shims. `internal/claudesessions` learns `CLAUDE_CONFIG_DIR` and stops requiring one transcript schema. `internal/plugin` grows one predicate method that `internal/daemon` and `internal/tui` consult instead of comparing plugin names.

**Tech Stack:** Go 1.25, stdlib only. No new dependencies. Build and test via `./scripts/dev.sh` (Docker — the host has no Go).

**Spec:** [`docs/superpowers/specs/2026-08-19-claude-session-seam-design.md`](../specs/2026-08-19-claude-session-seam-design.md)

**Branch:** `fix/claude-session-seam` (already created off `master`)

## Global Constraints

- **Go 1.25, stdlib only.** No new module dependencies for any task.
- **No local toolchain.** `go` and `make` are not installed on the host. Every build/test/vet runs through `./scripts/dev.sh` (Docker).
- **`./scripts/dev.sh test` takes ONE package argument.** Extra package arguments are silently dropped, so a three-package invocation tests one and greps clean. Run one package per invocation.
- **`dev.sh test` is not the CI command.** CI runs `go test -race ./...`. A green local run does not prove the race detector is clean.
- **Dev mode only for manual verification.** Never touch the production daemon, socket, PID, or anything under `~/.quil/`. Use `./quil-dev.exe` (state in `.quil/`) and confirm `[dev]` in the status bar first.
- **Never `git add -A`.** The repo owner keeps unrelated work staged and untracked. Stage by explicit path in every commit step.
- **Do not commit the spec or this plan.** `docs/superpowers/specs/` and `docs/superpowers/plans/` are untracked by standing convention in this repo (six prior spec files are untracked on `master`).
- **No AI attribution.** No `Co-Authored-By`, no model or vendor names, no "generated with" notes in any commit message or PR body.
- **Commit subjects:** imperative mood, ≤72 characters, Conventional Commits type.
- **Tests that touch a home directory must call `t.Setenv("QUIL_HOME", t.TempDir())` or use `t.TempDir()` directly.** Docker tests run with a throwaway `/root`, so a test writing to the real home is green in CI and pollutes the developer's `~/.quil` everywhere else.
- **Intermediate commits are squashed at merge.** The repo squash-merges PRs, so per-task commits below are working checkpoints, not published history. The PR *title* is the released commit subject.

---

## Task 1: Hook settings delivered as a file path (§1 + §2)

**Files:**
- Modify: `internal/claudehook/claudehook.go` (add `WriteSettingsFile` after `BuildSettingsJSON`, ~line 198)
- Modify: `internal/daemon/daemon.go:3240-3275` (`claudeHookSpawnPrep`)
- Modify: `internal/daemon/daemon.go:2143` (`cleanupPaneArtifacts` name list)
- Test: `internal/claudehook/claudehook_test.go`
- Test: `internal/daemon/spawn_args_test.go:471-520`
- Test: `internal/daemon/spawn_env_test.go:14-20`

**Interfaces:**
- Consumes: existing unexported `atomicWrite(path string, data []byte, perm os.FileMode) error` and `validatePaneID(paneID string) error` in `internal/claudehook`.
- Produces: `claudehook.WriteSettingsFile(quilDir, paneID, settingsJSON string) (string, error)` — returns the absolute settings-file path, or `("", nil)` when `settingsJSON` is empty.

### Why

On Windows `exec.LookPath("claude")` resolves to `claude.cmd` (the npm shim) because `PATHEXT` includes `.CMD`. Windows runs a `.cmd` through `cmd.exe`, which re-parses the command line with different quoting rules than the CRT parser conpty already applied. Our settings JSON is full of `"`, so the argument is re-split at the wrong boundaries and the hook never registers. A path carries no shell metacharacters and survives both parsers.

- [ ] **Step 1: Write the failing test for `WriteSettingsFile`**

Add to `internal/claudehook/claudehook_test.go`:

```go
func TestWriteSettingsFile(t *testing.T) {
	quilDir := t.TempDir()

	path, err := WriteSettingsFile(quilDir, "pane-abc123", `{"hooks":{}}`)
	if err != nil {
		t.Fatalf("WriteSettingsFile: %v", err)
	}
	want := filepath.Join(quilDir, "sessions", "pane-abc123.settings.json")
	if path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(body) != `{"hooks":{}}` {
		t.Errorf("body = %q, want %q", body, `{"hooks":{}}`)
	}
}

func TestWriteSettingsFile_EmptyJSONWritesNothing(t *testing.T) {
	quilDir := t.TempDir()

	path, err := WriteSettingsFile(quilDir, "pane-abc123", "")
	if err != nil {
		t.Fatalf("WriteSettingsFile: %v", err)
	}
	if path != "" {
		t.Errorf("path = %q, want \"\" so the caller can skip --settings", path)
	}
	if _, err := os.Stat(filepath.Join(quilDir, "sessions")); !os.IsNotExist(err) {
		t.Errorf("sessions dir created for an empty write")
	}
}

func TestWriteSettingsFile_RejectsTraversalPaneID(t *testing.T) {
	quilDir := t.TempDir()

	for _, bad := range []string{"", "../escape", `a\b`, "a/b", "pane\x00id"} {
		if _, err := WriteSettingsFile(quilDir, bad, `{"hooks":{}}`); err == nil {
			t.Errorf("paneID %q accepted, want rejected", bad)
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `./scripts/dev.sh test internal/claudehook`
Expected: FAIL — `undefined: WriteSettingsFile`

- [ ] **Step 3: Implement `WriteSettingsFile`**

Insert into `internal/claudehook/claudehook.go` immediately after `BuildSettingsJSON`:

```go
// settingsFile returns the absolute path of the per-pane hook-settings JSON
// under <quilDir>/sessions/. Per-pane rather than shared so concurrent spawns
// never race on one file.
func settingsFile(quilDir, paneID string) string {
	return filepath.Join(quilDir, "sessions", paneID+".settings.json")
}

// WriteSettingsFile writes the hook-settings JSON for paneID and returns its
// absolute path, for `claude --settings <path>`.
//
// A FILE rather than an inline JSON argument, because on Windows the claude
// binary is an npm .cmd shim that cmd.exe re-parses: the quotes inside an
// inline JSON are re-split at the wrong boundaries by a second parser with
// different rules than the one conpty quoted for, so the hook never registers
// and a JSON fragment is mistaken for a command name. A path has no shell
// metacharacters and survives every layer intact.
//
// Returns ("", nil) when there is nothing to write, letting the caller skip
// the --settings argument entirely rather than passing an empty path.
func WriteSettingsFile(quilDir, paneID, settingsJSON string) (string, error) {
	if err := validatePaneID(paneID); err != nil {
		return "", err
	}
	if settingsJSON == "" {
		return "", nil
	}
	dir := filepath.Join(quilDir, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("claudehook: create sessions dir: %w", err)
	}
	path := settingsFile(quilDir, paneID)
	if err := atomicWrite(path, []byte(settingsJSON), 0o600); err != nil {
		return "", fmt.Errorf("claudehook: write hook settings: %w", err)
	}
	return path, nil
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `./scripts/dev.sh test internal/claudehook`
Expected: PASS

- [ ] **Step 5: Update `claudeHookSpawnPrep` to pass the path**

In `internal/daemon/daemon.go`, replace the doc comment's final sentence and the return.

Replace this comment fragment:

```go
// pre-feature behaviour rather than failing the whole spawn. Logs a warning if
// userArgs already contain --settings; Claude treats later wins, so our
// prepend silently overrides the user's value.
```

with:

```go
// pre-feature behaviour rather than failing the whole spawn.
//
// The settings go to a per-pane FILE passed as `--settings <path>`, never an
// inline JSON string: on Windows the claude binary is an npm .cmd shim that
// cmd.exe re-parses, and the quotes inside an inline JSON are re-split at the
// wrong boundaries. A path has no shell metacharacters.
//
// Logs a warning when userArgs already contain --settings. Which value wins is
// UNVERIFIED — Quil prepends its own, so depending on Claude's precedence
// either the user loses their settings or Quil loses rotation tracking. See
// §7 of docs/superpowers/specs/2026-08-19-claude-session-seam-design.md.
```

Then insert the write immediately after the `BuildSettingsJSON` error check:

```go
	settingsPath, err := claudehook.WriteSettingsFile(quilDir, paneID, js)
	if err != nil {
		log.Printf("warning: pane %s: write hook settings file: %v — session-id rotation tracking disabled", paneID, err)
		return nil, nil
	}
```

Replace the `--settings` warning line:

```go
			log.Printf("warning: pane %s: claude-code args already contain --settings; precedence with Quil's hook entry is unverified", paneID)
```

Replace the return value `[]string{"--settings", js}` with `[]string{"--settings", settingsPath}`.

- [ ] **Step 6: Add the settings file to pane cleanup**

In `internal/daemon/daemon.go`, in `cleanupPaneArtifacts`, change:

```go
	for _, name := range []string{paneID + ".id", paneID + ".transcript", "opencode-" + paneID + ".id"} {
```

to:

```go
	for _, name := range []string{paneID + ".id", paneID + ".transcript", paneID + ".settings.json", "opencode-" + paneID + ".id"} {
```

- [ ] **Step 7: Update the two spawn tests to use a writable dir**

`claudeHookSpawnPrep` now writes to disk, so its tests can no longer pass a fake path.

In `internal/daemon/spawn_args_test.go`, replace:

```go
			prefix, env := claudeHookSpawnPrep("/tmp/quil", tt.paneID, "default", tt.userArgs)
```

with:

```go
			quilDir := t.TempDir()
			prefix, env := claudeHookSpawnPrep(quilDir, tt.paneID, "default", tt.userArgs)
```

Replace the two assertions on `prefix[1]`:

```go
				if !strings.Contains(prefix[1], `"SessionStart"`) {
					t.Errorf("prefix[1] missing SessionStart key: %s", prefix[1])
				}
				if !strings.Contains(prefix[1], "claude-hook") {
					t.Errorf("prefix[1] missing native claude-hook command: %s", prefix[1])
				}
```

with a read of the file the path names, plus an assertion that the argument is shell-safe — that is the whole point of the change:

```go
				if strings.ContainsAny(prefix[1], `"{}`) {
					t.Errorf("prefix[1] = %q contains shell metacharacters; must be a bare path", prefix[1])
				}
				body, err := os.ReadFile(prefix[1])
				if err != nil {
					t.Fatalf("read settings file %s: %v", prefix[1], err)
				}
				if !strings.Contains(string(body), `"SessionStart"`) {
					t.Errorf("settings file missing SessionStart key: %s", body)
				}
				if !strings.Contains(string(body), "claude-hook") {
					t.Errorf("settings file missing native claude-hook command: %s", body)
				}
```

Replace the `QUIL_HOOK_HOME` assertion:

```go
				if env[2] != "QUIL_HOOK_HOME="+quilDir {
					t.Errorf("env[2] = %q, want QUIL_HOOK_HOME=%s", env[2], quilDir)
				}
```

Ensure `os` is imported in that file.

In `internal/daemon/spawn_env_test.go`, replace:

```go
	_, env := claudeHookSpawnPrep("/data/quil", "pane-abc123", "default", nil)
	assertHookHomeOnly(t, env, "/data/quil")
```

with:

```go
	// quilDir must be writable: claudeHookSpawnPrep now writes the hook
	// settings to a per-pane file under <quilDir>/sessions/.
	quilDir := t.TempDir()
	_, env := claudeHookSpawnPrep(quilDir, "pane-abc123", "default", nil)
	assertHookHomeOnly(t, env, quilDir)
```

- [ ] **Step 8: Retire every other "inline JSON" and "later wins" claim**

Three more places assert the mechanism this task removes or the precedence it declares unverified. Leaving them makes §1's own rationale documented as false — and this repo treats comments as load-bearing.

1. **`internal/claudehook/claudehook.go:1-9`**, the package doc, says the hook is registered *"via --settings (inline JSON)"*. Change that phrase to `via --settings (a per-pane settings file)`.

2. **`internal/daemon/daemon.go:3740-3746`**, the comment block above the spawn switch, describes prepending *"--settings with an inline JSON that registers a SessionStart hook"*. Task 3 Step 8 rewrites the code below this block but not the block. Change "an inline JSON" to "a settings file".

3. **`internal/daemon/spawn_args_test.go:427` and `:454`** still assert the direction §2 declares unverified — a comment reading *"(Claude later-wins)"* and a subtest named `"user already passed --settings — still injects (later-wins warning logged)"`. Rewrite both to describe the behaviour under test rather than a precedence direction:

```go
// (not error) when --settings is already in the user's args (precedence unverified).
```

```go
			name:       "user already passed --settings — still injects (warning logged)",
```

A grep for `later.wins` across `internal/` must return nothing when this step is done.

- [ ] **Step 9: Run both package test suites**

Run: `./scripts/dev.sh test internal/claudehook`
Expected: PASS

Run: `./scripts/dev.sh test internal/daemon`
Expected: PASS

- [ ] **Step 10: Commit**

```bash
git add internal/claudehook/claudehook.go internal/claudehook/claudehook_test.go internal/daemon/daemon.go internal/daemon/spawn_args_test.go internal/daemon/spawn_env_test.go
git commit -m "fix(claude): pass hook settings as a file path"
```

---

## Task 2: Generalize the session-picker predicate (§3)

**Files:**
- Modify: `internal/tui/dialog.go:3370, 3841, 3881, 5453`

**Interfaces:**
- Consumes: `plugin.CommandConfig.Sessions` (existing field).
- Produces: nothing new.

### Why

Four sites hardcode the *value* of a field `registry.go:416` already validates against a closed set. Three of them (`setupFieldCount`, `setupFieldKind`, `renderCreatePaneSetupDialog`) are independently maintained copies of one predicate; desyncing them puts the dialog cursor on the wrong row — a runtime bug, not a compile error.

This is a behavioural no-op today: `""` and `"claude"` are the only values `loadPluginTOML` accepts.

- [ ] **Step 1: Write the failing test**

Add to `internal/tui/dialog_test.go` (create the file only if it does not exist; otherwise append):

```go
func TestSetupFieldCount_CountsAnyNonEmptySessionsSource(t *testing.T) {
	p := &plugin.PanePlugin{
		Name: "future-agent",
		Command: plugin.CommandConfig{
			PromptsCWD: true,
			Sessions:   "future-store",
		},
	}
	m := Model{}
	// Continue + CWD + worktree + session. setupFieldCount starts at
	// len(Toggles)+1, where the +1 is the Continue button — so this plugin
	// counts 3 BEFORE the change and 4 after.
	if got := m.setupFieldCount(p); got != 4 {
		t.Errorf("setupFieldCount = %d, want 4 (a non-empty sessions source must add the picker row)", got)
	}
}

func TestSetupFieldKind_ReachesSessionRowForAnySource(t *testing.T) {
	p := &plugin.PanePlugin{
		Name: "future-agent",
		Command: plugin.CommandConfig{
			PromptsCWD: true,
			Sessions:   "future-store",
		},
	}
	m := Model{}
	kind, _ := m.setupFieldKind(p, 2)
	if kind != "session" {
		t.Errorf("setupFieldKind(2) = %q, want \"session\"", kind)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `./scripts/dev.sh test internal/tui`
Expected: FAIL — `setupFieldCount = 3, want 4`, and `setupFieldKind(2) = "continue", want "session"`

Both must fail here. If `setupFieldCount` passes at this step the arithmetic is wrong and the test proves nothing.

- [ ] **Step 3: Replace all four literals**

In `internal/tui/dialog.go`:

Line 3370, inside `enterSetupOrSplit`:

```go
	needsSetup := p != nil && (p.Command.PromptsCWD || len(p.Command.Toggles) > 0 ||
		p.Command.Discover == "kube" || p.Command.Sessions != "")
```

Lines 3841, 3881 and 5453 each change `if p.Command.Sessions == "claude" {` to:

```go
	if p.Command.Sessions != "" {
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `./scripts/dev.sh test internal/tui`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/tui/dialog.go internal/tui/dialog_test.go
git commit -m "refactor(tui): key the session picker off any sessions source"
```

---

## Task 3: Derive the Claude capability, daemon side (§4a)

**Files:**
- Modify: `internal/plugin/plugin.go` (add constant + method at end of file)
- Modify: `internal/daemon/claudesessions.go:461`
- Modify: `internal/daemon/daemon.go:422, 3275, 3688, 3737`
- Test: `internal/plugin/plugin_test.go`

**Interfaces:**
- Consumes: `plugin.CommandConfig.Sessions`; `(*Registry).Get(name string) *PanePlugin` (existing, `RWMutex`-guarded).
- Produces:
  - `plugin.ClaudeSessionSource` — untyped string constant `"claude"`.
  - `func (p *PanePlugin) UsesClaudeSessions() bool` — nil-receiver safe.
  - `func (d *Daemon) usesClaudeSessions(paneType string) bool` — unexported daemon helper for the two sites that hold only a type string.

### Why

Six sites branch on the literal `"claude-code"`. A user who renames the plugin, or any future Claude-compatible tool, needs all six edited with no compiler help — and a missed site fails *silently*: resume simply stops working.

`claude-code` is the only default plugin with `sessions != ""` (all eight surveyed), so the capability is already derivable from data every Claude-family plugin file must carry. No new TOML field, so no `schema_version` bump and no migration dialog.

- [ ] **Step 1: Write the failing test for the plugin method**

Add to `internal/plugin/plugin_test.go`:

```go
func TestUsesClaudeSessions(t *testing.T) {
	tests := []struct {
		name     string
		sessions string
		want     bool
	}{
		{"claude source", "claude", true},
		{"no sessions", "", false},
		{"other source", "future-store", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &PanePlugin{Command: CommandConfig{Sessions: tt.sessions}}
			if got := p.UsesClaudeSessions(); got != tt.want {
				t.Errorf("UsesClaudeSessions() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestUsesClaudeSessions_NilReceiverIsFalse(t *testing.T) {
	var p *PanePlugin
	if p.UsesClaudeSessions() {
		t.Error("nil plugin reported as using claude sessions")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `./scripts/dev.sh test internal/plugin`
Expected: FAIL — `p.UsesClaudeSessions undefined`

- [ ] **Step 3: Add the constant and method**

Append to `internal/plugin/plugin.go`:

```go
// ClaudeSessionSource is the Command.Sessions value naming Claude Code's
// transcript store. It implies the whole Claude protocol: the preassigned
// session id, the SessionStart hook that tracks rotation, and transcripts
// under the Claude config dir.
const ClaudeSessionSource = "claude"

// UsesClaudeSessions reports whether this plugin's sessions are Claude Code
// sessions. It is the capability every site that once compared Name against
// "claude-code" should ask for instead — a renamed plugin, or any future
// Claude-compatible tool, then works without a code change.
//
// Nil-receiver safe: callers resolve through Registry.Get, which returns nil
// for an unknown or failed-to-load plugin. A nil answer of false is correct
// there, because a plugin that failed to load has already been degraded to
// "terminal" at the spawn site.
func (p *PanePlugin) UsesClaudeSessions() bool {
	return p != nil && p.Command.Sessions == ClaudeSessionSource
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `./scripts/dev.sh test internal/plugin`
Expected: PASS

- [ ] **Step 5: Write the failing test for the daemon helper**

Add to `internal/daemon/claudesessions_test.go`:

```go
func TestUsesClaudeSessions_ResolvesThroughRegistry(t *testing.T) {
	reg := plugin.NewRegistry()
	d := &Daemon{registry: reg}

	if d.usesClaudeSessions("claude-code") {
		t.Error("unknown plugin reported as claude sessions")
	}

	nilReg := &Daemon{}
	if nilReg.usesClaudeSessions("claude-code") {
		t.Error("nil registry must answer false, not panic")
	}
}
```

- [ ] **Step 6: Run the test to verify it fails**

Run: `./scripts/dev.sh test internal/daemon`
Expected: FAIL — `d.usesClaudeSessions undefined`

- [ ] **Step 7: Add the daemon helper**

Add to `internal/daemon/claudesessions.go`, near the top after the imports:

```go
// usesClaudeSessions reports whether panes of this plugin type speak the Claude
// protocol. The two call sites that hold only a pane TYPE (rather than a
// resolved plugin) route through here.
//
// A miss answers false. That is safe rather than a new failure mode: a plugin
// absent from the registry failed to load, and spawnPane already degrades such
// a pane to "terminal", so the capability goes false exactly when the pane is
// already unusable.
func (d *Daemon) usesClaudeSessions(paneType string) bool {
	if d.registry == nil {
		return false
	}
	return d.registry.Get(paneType).UsesClaudeSessions()
}
```

- [ ] **Step 8: Replace the four daemon sites**

`internal/daemon/claudesessions.go`, in `claudeSessionIDs` (~line 461):

```go
			if !d.usesClaudeSessions(typ) {
				continue
			}
```

`internal/daemon/daemon.go`, in `refreshPluginStateFromHooks` (~line 422) — the `switch pane.Type` becomes an if/else because one arm is no longer a constant:

```go
			var hookID, transcript string
			switch {
			case d.usesClaudeSessions(pane.Type):
				if rec, err := readHookSessionFn(pane.ID); err == nil {
					hookID, transcript = rec.ID, rec.TranscriptPath
				}
			case pane.Type == "opencode":
				if id, err := readOpencodeSessionIDFn(pane.ID); err == nil {
					hookID = id
				}
			default:
				continue
			}
```

`internal/daemon/daemon.go`, in `resumeTemplateFor` (~line 3275):

```go
	case p.UsesClaudeSessions() && p.Persistence.Strategy == "preassign_id":
```

`internal/daemon/daemon.go`, in `spawnPane` (~line 3688):

```go
		if p.UsesClaudeSessions() {
			if rec, err := readHookSessionFn(pane.ID); err == nil {
				hookID = rec.ID
			}
		}
```

`internal/daemon/daemon.go`, in `spawnPane` (~line 3737) — the spawn switch becomes if/else for the same reason:

```go
	envVars := append([]string{}, p.Command.Env...)
	switch {
	case p.UsesClaudeSessions():
		settingsArgs, hookEnv := claudeHookSpawnPrep(config.QuilDir(), pane.ID, d.cfg.Notification.Hooks.Claude, args)
		if len(settingsArgs) > 0 {
			args = append(settingsArgs, args...)
		}
		envVars = append(envVars, hookEnv...)
	case p.Name == "opencode":
		envVars = append(envVars, opencodeSpawnPrep(config.QuilDir(), pane.ID, d.cfg.Notification.Hooks.OpenCode)...)
	}
```

- [ ] **Step 9: Run the daemon tests**

Run: `./scripts/dev.sh test internal/daemon`
Expected: PASS

- [ ] **Step 10: Commit**

```bash
git add internal/plugin/plugin.go internal/plugin/plugin_test.go internal/daemon/claudesessions.go internal/daemon/claudesessions_test.go internal/daemon/daemon.go
git commit -m "refactor(daemon): derive the claude capability from plugin data"
```

---

## Task 4: Derive the Claude capability, TUI side (§4b)

**Files:**
- Modify: `internal/plugin/plugin.go` (add `RestoresOwnHistory` method)
- Modify: `internal/daemon/daemon.go:1565` (`restoresOwnHistory` delegates to the method)
- Modify: `internal/tui/model.go` (add helper next to `pluginWideCanvas` at line 2661; update five `syncPaneMeta` call sites at 5259, 5317, 5448, 5562, 5574)
- Modify: `internal/tui/workstate.go:767` (`syncPaneMeta` signature)
- Modify: `internal/tui/pane.go` (add `PaneModel` field; `restoresViaSession` at 780; `resumeLabel` gate at 804; call sites at 832 and 839)
- Test: `internal/tui/pane_test.go`

**Interfaces:**
- Consumes: `plugin.PanePlugin` (`Persistence.Strategy`).
- Produces:
  - `func (p *PanePlugin) RestoresOwnHistory() bool` — nil-receiver safe; promoted verbatim from the daemon's existing `restoresOwnHistory`, now shared by daemon and TUI.
  - `PaneModel.RestoresViaSession bool` — resolved by `Model`, read by `PaneModel`.
  - `func (m Model) pluginRestoresViaSession(paneType string) bool`.
  - `syncPaneMeta(pane *PaneModel, info *PaneInfo, wideCanvas bool, minNativeCols int, restoresViaSession bool)`.

### Why

`restoresViaSession` is called from `PaneModel.View()`, which has no registry access — threading one through the view would be invasive. `syncPaneMeta` already takes two plugin-derived values (`wideCanvas`, `minNativeCols`) that `Model` resolves for it, so a third follows the established pattern.

**Accepted limitation, per §4 of the spec.** In remote mode this consults the *client's* plugin definitions about a pane on the *daemon's* machine (RD-035 keeps definitions client-local), so the answer can disagree. That is not a regression — today's hardcoded list is wrong in the same remote case *and* additionally wrong for a renamed plugin — and it stays acceptable only because the value drives a cosmetic checklist label. If it ever gates something load-bearing it must move onto the wire in `PaneInfo` instead.

- [ ] **Step 1: Write the failing test**

Add to `internal/tui/pane_test.go`:

```go
func TestRestoreSteps_UsesResolvedCapabilityNotPaneName(t *testing.T) {
	// A renamed claude plugin: the old hardcoded list would miss it.
	p := &PaneModel{
		Type:               "claude-code-custom",
		SessionID:          "abcdef0123456789",
		RestoresViaSession: true,
	}
	steps := p.restoreSteps()

	var texts []string
	for _, s := range steps {
		texts = append(texts, s.text)
	}
	joined := strings.Join(texts, "|")
	if !strings.Contains(joined, "history via resume") {
		t.Errorf("steps = %q, want a \"history via resume\" row", joined)
	}
	if !strings.Contains(joined, "abcdef01") {
		t.Errorf("steps = %q, want the session-id prefix appended", joined)
	}
}

func TestRestoreSteps_NoSessionRestoreWhenCapabilityFalse(t *testing.T) {
	p := &PaneModel{
		Type:               "terminal",
		SessionID:          "abcdef0123456789",
		RestoresViaSession: false,
	}
	joined := ""
	for _, s := range p.restoreSteps() {
		joined += s.text + "|"
	}
	if strings.Contains(joined, "history via resume") {
		t.Errorf("steps = %q, want no resume row for a non-session plugin", joined)
	}
	if strings.Contains(joined, "abcdef01") {
		t.Errorf("steps = %q, want no session-id suffix for a non-session plugin", joined)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `./scripts/dev.sh test internal/tui`
Expected: FAIL — `unknown field RestoresViaSession in struct literal`

- [ ] **Step 3: Add the `PaneModel` field**

In `internal/tui/pane.go`, in the `PaneModel` struct, immediately after the `Type` field:

```go
	// RestoresViaSession is the plugin capability resolved by Model (which
	// owns the registry) and copied in by syncPaneMeta. PaneModel.View has no
	// registry access, so the answer is resolved once per broadcast rather
	// than looked up at render time.
	RestoresViaSession bool
```

- [ ] **Step 4: Replace `restoresViaSession` and the `resumeLabel` gate**

In `internal/tui/pane.go`, delete the free function `restoresViaSession` (lines 776-782) entirely.

Change `resumeLabel`'s signature and its session-id gate. The `switch paneType` copy table stays — it is user-facing phrasing, not dispatch:

```go
// resumeLabel is row 3 of the checklist: a human description of the resume
// strategy for this pane type, with the tracked session-id prefix appended when
// the plugin restores through a session id.
//
// The switch is a copy table, not dispatch: "resuming claude" vs "reconnecting
// ssh" is phrasing per plugin, and no plugin field carries a verb phrase. The
// capability question — does a session id mean anything here — is the
// restoresViaSession parameter.
func resumeLabel(paneType, sessionID string, restoresViaSession bool) string {
```

and inside it:

```go
	if sessionID != "" && restoresViaSession {
```

- [ ] **Step 5: Update the two `restoreSteps` call sites**

In `internal/tui/pane.go`, line ~832:

```go
	case p.RestoresViaSession && p.SessionID != "":
```

and line ~839:

```go
	resume := restoreStep{text: resumeLabel(p.Type, p.SessionID, p.RestoresViaSession), state: stepActive}
```

- [ ] **Step 6: Promote the existing strategy predicate into `internal/plugin`**

The daemon already answers this exact question correctly. `restoresOwnHistory` (`internal/daemon/daemon.go:1565`) asks the **resume strategy**, and its own comment rejects the approach the TUI still uses:

> This is the resume-strategy question, not a plugin-name list: the two strategies below are exactly the ones resolveSpawnArgs expands into `--resume <id>` / `--session <id>`.

`restoresViaSession` in the TUI is the same predicate with a stale name list. Promote the daemon's version to the plugin type so both consume one definition, and the `opencode` case needs no special-casing at all — `session_scrape` already covers it.

Append to `internal/plugin/plugin.go`, below `UsesClaudeSessions`:

```go
// RestoresOwnHistory reports whether this plugin's resume strategy hands the
// respawned child a session id, so the child paints its own transcript back
// instead of depending on Quil's ghost replay.
//
// The resume-strategy question, not a plugin-name list: these two strategies
// are exactly the ones resolveSpawnArgs expands into `--resume <id>` /
// `--session <id>`. `rerun` re-runs a command that starts from nothing and
// `cwd_only` respawns a shell that will not reprint a word of its scrollback —
// both still need the replay.
func (p *PanePlugin) RestoresOwnHistory() bool {
	if p == nil {
		return false
	}
	switch p.Persistence.Strategy {
	case "preassign_id", "session_scrape":
		return true
	}
	return false
}
```

Replace the daemon's free function body (`internal/daemon/daemon.go:1565`) with a delegation, keeping the call sites untouched:

```go
// restoresOwnHistory reports whether a plugin's resume strategy hands the
// respawned child a session id. Thin wrapper over the plugin method so the
// daemon's call sites read unchanged; the predicate itself lives with the type
// it describes, where the TUI can reach it too.
func restoresOwnHistory(p *plugin.PanePlugin) bool {
	return p.RestoresOwnHistory()
}
```

Then add the Model resolver in `internal/tui/model.go`, immediately after `pluginWideCanvas` (line 2661):

```go
// pluginRestoresViaSession reports whether panes of this type restore their own
// history through a session id rather than a Quil ghost buffer. Mirrors
// pluginWideCanvas: Model owns the registry, PaneModel does not.
//
// Shares one definition with the daemon's restoresOwnHistory. The TUI used to
// keep its own hardcoded {claude-code, opencode} list, which was wrong for a
// renamed plugin and would have needed editing for any new session-resuming
// one.
func (m Model) pluginRestoresViaSession(paneType string) bool {
	if m.pluginRegistry == nil {
		return false
	}
	return m.pluginRegistry.Get(paneType).RestoresOwnHistory()
}
```

Note this makes `UsesClaudeSessions` a daemon-side capability only — the TUI's question is about resume strategy, which is the more precise one for a checklist about history.

- [ ] **Step 7: Widen `syncPaneMeta` and its five call sites**

In `internal/tui/workstate.go`, line 767:

```go
func syncPaneMeta(pane *PaneModel, info *PaneInfo, wideCanvas bool, minNativeCols int, restoresViaSession bool) {
```

and inside it, after `pane.Type = info.Type`:

```go
	pane.RestoresViaSession = restoresViaSession
```

In `internal/tui/model.go`, each of lines 5259, 5317, 5448 becomes:

```go
syncPaneMeta(pane, info, m.pluginWideCanvas(info.Type), m.pluginMinNativeCols(info.Type), m.pluginRestoresViaSession(info.Type))
```

(at line 5259 the first argument is `leaf.Pane`, not `pane` — keep it)

and lines 5562 and 5574:

```go
syncPaneMeta(pane, overlayInfo, m.pluginWideCanvas(overlayInfo.Type), m.pluginMinNativeCols(overlayInfo.Type), m.pluginRestoresViaSession(overlayInfo.Type))
```

(at line 5574 the first argument is `tab.overlayPane`, not `pane` — keep it)

- [ ] **Step 8: Fix the 7 existing test files the signature changes break**

Both signature changes have a blast radius in tests that Steps 1-7 do not cover. Do not discover this by running the suite and improvising — the semantic breakages below look like ordinary assertion failures.

**16 four-argument `syncPaneMeta` calls across 7 files** — all compile-break:

| File | Lines |
|---|---|
| `internal/tui/gitworktreename_test.go` | 16, 25 |
| `internal/tui/modelinfo_test.go` | 39, 47 |
| `internal/tui/pinned_attention_test.go` | 361, 365 |
| `internal/tui/restore_indicator_test.go` | 102, 106, 293 |
| `internal/tui/spawnerror_test.go` | 55, 64 |
| `internal/tui/wheel_forward_test.go` | 120 |
| `internal/tui/workstate_test.go` | 1434, 1438, 1453, 1460 |

For each, append the fifth argument. These tests construct a `Model` without a populated registry, so the resolver correctly answers `false` and that preserves their current expectations:

```go
syncPaneMeta(pane, info, false, 0, false)
```

— matching whatever the existing call already passes for `wideCanvas` and `minNativeCols`; only the new trailing argument is added.

**`internal/tui/restore_indicator_test.go` breaks semantically, not just at compile.** Two spots:

1. Line 318 calls the two-argument `resumeLabel`:

```go
			if got := resumeLabel(tc.typ, tc.sid); got != tc.want {
```

Its table covers `claude-code`/`opencode` rows expecting a session-id suffix and other rows expecting none. Add a `restores bool` field to the table struct, set it `true` on the `claude-code` and `opencode` rows and `false` elsewhere, and pass it:

```go
			if got := resumeLabel(tc.typ, tc.sid, tc.restores); got != tc.want {
```

Do **not** pass a blanket `true` — the rows expecting no suffix are what pin the gate.

2. `TestRestoreSteps` (~line 332) builds `&PaneModel{Type: "claude-code", SessionID: ...}` literals. `RestoresViaSession` now defaults to `false` in those literals, so the `"history via resume"` and `"resuming claude · 8f2e1c00"` expectations fail. Add the field to each literal whose expectation assumes session restore:

```go
	p := &PaneModel{Type: "claude-code", SessionID: "8f2e1c00-...", RestoresViaSession: true}
```

This is the exact spot where a wrong fix hides: hardcoding `true` on a literal whose expectation says *no* resume row would make the suite green while inverting the behaviour. Match each literal to what its assertion already claims.

- [ ] **Step 9: Run the TUI tests**

Run: `./scripts/dev.sh test internal/tui`
Expected: PASS

- [ ] **Step 10: Run the daemon and plugin tests too**

`restoresOwnHistory` changed in `internal/daemon` and the method landed in `internal/plugin`.

Run: `./scripts/dev.sh test internal/daemon`
Expected: PASS

Run: `./scripts/dev.sh test internal/plugin`
Expected: PASS

- [ ] **Step 11: Commit**

```bash
git add internal/plugin/plugin.go internal/daemon/daemon.go internal/tui/pane.go internal/tui/pane_test.go internal/tui/model.go internal/tui/workstate.go internal/tui/gitworktreename_test.go internal/tui/modelinfo_test.go internal/tui/pinned_attention_test.go internal/tui/restore_indicator_test.go internal/tui/spawnerror_test.go internal/tui/wheel_forward_test.go internal/tui/workstate_test.go
git commit -m "refactor(tui): resolve the session-restore capability once per sync"
```

---

## Task 5: Honour `CLAUDE_CONFIG_DIR` (§5)

**Files:**
- Modify: `internal/claudesessions/claudesessions.go` (`ProjectDir` ~line 120; add `ConfigDir`, `ProjectDirIn`, `ListIn`, `ReadDetailIn`)
- Test: `internal/claudesessions/claudesessions_test.go`

**Interfaces:**
- Produces:
  - `claudesessions.ConfigDir() string`
  - `claudesessions.ProjectDirIn(configDir, cwd string) string`
  - `claudesessions.ListIn(ctx context.Context, configDir, cwd string) ([]Session, bool, error)`
  - `claudesessions.ReadDetailIn(ctx context.Context, configDir, cwd, sessionID string) (Detail, error)`
  - Existing `ProjectDir`, `TranscriptPath`, `List`, `ReadDetail` keep their signatures and delegate through `ConfigDir()`.

### Why

`CLAUDE_CONFIG_DIR` appears nowhere in the repo; `ProjectDir` hardcodes `$HOME/.claude/projects`. It is an upstream Claude Code variable that relocates the whole config directory, `projects/` included, and users set it to separate work from personal config.

Only the session **picker** is affected. Resume already follows a relocated dir because it uses the transcript path Claude itself reports through the hook. The picker's failure is silent: an empty list looks identical to "no sessions recorded yet".

Resolution is daemon-side only, and the picker does **not** honour a per-plugin `Command.Env` override — see the spec's "Deliberately not fixed" note. Doing so would need a request field naming the plugin, which is the `Source`-shaped field this design rejects.

- [ ] **Step 1: Write the failing test**

Add to `internal/claudesessions/claudesessions_test.go`:

```go
func TestConfigDir_PrefersEnv(t *testing.T) {
	// t.TempDir() rather than a literal "/tmp/..." path: filepath.Abs prepends
	// a drive letter on Windows, so a POSIX literal fails the equality when the
	// test binary is run natively on the host — which this repo does.
	want := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", want)
	if got := ConfigDir(); got != want {
		t.Errorf("ConfigDir() = %q, want the env value %q", got, want)
	}
}

func TestConfigDir_FallsBackToHomeDotClaude(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory in this environment")
	}
	if got := ConfigDir(); got != filepath.Join(home, ".claude") {
		t.Errorf("ConfigDir() = %q, want %q", got, filepath.Join(home, ".claude"))
	}
}

func TestConfigDir_ExpandsTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory in this environment")
	}
	t.Setenv("CLAUDE_CONFIG_DIR", "~/scoped")
	if got := ConfigDir(); got != filepath.Join(home, "scoped") {
		t.Errorf("ConfigDir() = %q, want %q", got, filepath.Join(home, "scoped"))
	}
}

func TestProjectDirIn_DoesNotConsultEnv(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/should/not/be/read")
	got := ProjectDirIn("/explicit", "/work/repo")
	want := filepath.Join("/explicit", "projects", EscapeCWD("/work/repo"))
	if got != want {
		t.Errorf("ProjectDirIn = %q, want %q", got, want)
	}
}

func TestListIn_ReadsTheGivenConfigDir(t *testing.T) {
	cfg := t.TempDir()
	cwd := "/work/repo"
	dir := filepath.Join(cfg, "projects", EscapeCWD(cwd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"user","promptSource":"typed","message":{"content":"hello there"}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "11111111-2222-3333-4444-555555555555.jsonl"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	sessions, _, err := ListIn(context.Background(), cfg, cwd)
	if err != nil {
		t.Fatalf("ListIn: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("len(sessions) = %d, want 1", len(sessions))
	}
	if sessions[0].Title != "hello there" {
		t.Errorf("Title = %q, want %q", sessions[0].Title, "hello there")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `./scripts/dev.sh test internal/claudesessions`
Expected: FAIL — `undefined: ConfigDir`

- [ ] **Step 3: Implement the config-dir seam**

In `internal/claudesessions/claudesessions.go`, replace `ProjectDir` and add the new functions:

```go
// claudeConfigDirEnv is Claude Code's own override for its config directory.
// Setting it relocates everything Claude stores, projects/ included — which is
// exactly how wrapper distributions keep their sessions separate.
const claudeConfigDirEnv = "CLAUDE_CONFIG_DIR"

// ConfigDir returns Claude Code's config directory: $CLAUDE_CONFIG_DIR when
// set, else ~/.claude. Returns "" when neither can be resolved.
//
// A "~/"-prefixed or relative value is expanded here, so every caller gets an
// absolute path. Resolution belongs on whichever machine owns the disk — the
// daemon — because the directory describes that machine's filesystem.
func ConfigDir() string {
	if v := strings.TrimSpace(os.Getenv(claudeConfigDirEnv)); v != "" {
		if v == "~" || strings.HasPrefix(v, "~/") || strings.HasPrefix(v, `~\`) {
			home, err := os.UserHomeDir()
			if err != nil {
				return ""
			}
			if v == "~" {
				return home
			}
			return filepath.Join(home, v[2:])
		}
		if abs, err := filepath.Abs(v); err == nil {
			return abs
		}
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude")
}

// ProjectDirIn maps a CWD to its transcript directory under an explicit config
// dir. Takes the directory rather than reading the environment so callers that
// already resolved one are not at the mercy of a concurrent Setenv, and so
// tests need no environment at all.
func ProjectDirIn(configDir, cwd string) string {
	if configDir == "" || cwd == "" {
		return ""
	}
	return filepath.Join(configDir, "projects", EscapeCWD(cwd))
}

// ProjectDir returns the absolute directory Claude stores this CWD's session
// transcripts in, or "" when the config directory cannot be resolved.
func ProjectDir(cwd string) string {
	return ProjectDirIn(ConfigDir(), cwd)
}
```

Then add the `…In` variants beside `List` and `ReadDetail`, and make the existing names delegate:

```go
// ListIn is List against an explicit config dir.
func ListIn(ctx context.Context, configDir, cwd string) (sessions []Session, truncated bool, err error) {
	dir := ProjectDirIn(configDir, cwd)
	if dir == "" || ctx.Err() != nil {
		return nil, false, nil
	}
	return listDir(ctx, dir)
}

// ReadDetailIn is ReadDetail against an explicit config dir.
func ReadDetailIn(ctx context.Context, configDir, cwd, sessionID string) (Detail, error) {
	if err := ctx.Err(); err != nil {
		return Detail{}, fmt.Errorf("read session detail: %w", err)
	}
	if sessionID == "" || sessionID != filepath.Base(sessionID) ||
		sessionID == "." || sessionID == ".." || strings.ContainsRune(sessionID, filepath.Separator) {
		return Detail{}, fmt.Errorf("invalid session id")
	}
	dir := ProjectDirIn(configDir, cwd)
	if dir == "" {
		return Detail{}, fmt.Errorf("no transcript path for this session")
	}
	return readDetail(ctx, filepath.Join(dir, sessionID+".jsonl"), sessionID)
}
```

Replace the bodies of `List` and `ReadDetail` with delegations:

```go
func List(ctx context.Context, cwd string) (sessions []Session, truncated bool, err error) {
	return ListIn(ctx, ConfigDir(), cwd)
}

func ReadDetail(ctx context.Context, cwd, sessionID string) (Detail, error) {
	return ReadDetailIn(ctx, ConfigDir(), cwd, sessionID)
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `./scripts/dev.sh test internal/claudesessions`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/claudesessions/claudesessions.go internal/claudesessions/claudesessions_test.go
git commit -m "fix(claude): read sessions from CLAUDE_CONFIG_DIR when set"
```

---

## Task 6: Title fallbacks for unfamiliar transcript schemas (§6a)

**Files:**
- Modify: `internal/claudesessions/claudesessions.go` (`transcriptLine` struct ~line 249; `readTitle` ~line 405)
- Test: `internal/claudesessions/claudesessions_test.go`

**Interfaces:**
- Consumes: existing `contentText`, `sanitizeTitle`.
- Produces: no exported change. `transcriptLine` gains an `AiTitle string \`json:"aiTitle"\`` field.

### Why

`readTitle` pre-filters on the literal `"promptSource":"typed"`. A transcript without that field yields an empty title, silently. `promptSource` is a Claude Code schema detail, and the `EscapeCWD` comment directly above records the 2026-07-05 incident where a similar version-pinned detail "fail[ed] quietly rather than loudly".

Strictly additive: the typed-prompt pass runs **first** and its result wins, so no currently-working install changes behaviour.

- [ ] **Step 1: Write the failing test**

Add to `internal/claudesessions/claudesessions_test.go`:

```go
// writeTranscript is a helper for the fallback tests: one transcript in a
// throwaway project dir, returning its path.
func writeTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "11111111-2222-3333-4444-555555555555.jsonl")
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadTitle_TypedPromptStillWins(t *testing.T) {
	path := writeTranscript(t,
		`{"type":"ai-title","aiTitle":"Generated Title"}`,
		`{"type":"user","promptSource":"typed","message":{"content":"the typed one"}}`,
	)
	if got := readTitle(path); got != "the typed one" {
		t.Errorf("readTitle = %q, want the typed prompt to win", got)
	}
}

func TestReadTitle_FallsBackToAiTitle(t *testing.T) {
	// Entry shape transcribed from the hanxu1210/quil fork's fixtures — the
	// only sample of this schema available. Extra keys are present on purpose:
	// the pre-filter must match a real line, not a minimal one.
	path := writeTranscript(t,
		`{"type":"ai-title","aiTitle":"Refactor the parser","leafUuid":"u1","timestamp":"2026-07-01T10:00:30.000Z"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"sure"}]}}`,
	)
	if got := readTitle(path); got != "Refactor the parser" {
		t.Errorf("readTitle = %q, want the ai-title", got)
	}
}

func TestReadTitle_CurrentSchemaNonTypedEntryIsNotPromoted(t *testing.T) {
	// The overbroad-fallback guard. This transcript is CURRENT schema — it
	// records promptSource — but has no "typed" entry. Without the
	// promptSource-absent condition the fallback would promote this
	// compaction summary to the session's title.
	path := writeTranscript(t,
		`{"type":"user","promptSource":"compact","message":{"content":"This session is being continued from a previous conversation that ran out of context..."}}`,
	)
	if got := readTitle(path); got != "" {
		t.Errorf("readTitle = %q, want \"\" — a non-typed current-schema entry is not a prompt", got)
	}
}

func TestReadTitle_FallsBackToStringContentUserEntry(t *testing.T) {
	path := writeTranscript(t,
		`{"type":"user","message":{"content":"no promptSource here"}}`,
	)
	if got := readTitle(path); got != "no promptSource here" {
		t.Errorf("readTitle = %q, want the shape-detected prompt", got)
	}
}

func TestReadTitle_ArrayContentUserEntryIsAToolResultNotAPrompt(t *testing.T) {
	path := writeTranscript(t,
		`{"type":"user","message":{"content":[{"type":"tool_result","text":"exit 0"}]}}`,
	)
	if got := readTitle(path); got != "" {
		t.Errorf("readTitle = %q, want \"\" — array content is a tool result", got)
	}
}

func TestReadTitle_SidechainExcludedOnEveryPath(t *testing.T) {
	path := writeTranscript(t,
		`{"type":"user","isSidechain":true,"message":{"content":"subagent chatter"}}`,
	)
	if got := readTitle(path); got != "" {
		t.Errorf("readTitle = %q, want \"\" — sidechain entries are not the user's conversation", got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `./scripts/dev.sh test internal/claudesessions`
Expected: FAIL — `TestReadTitle_FallsBackToAiTitle` gets `""`

- [ ] **Step 3: Add the `aiTitle` field to `transcriptLine`**

In `internal/claudesessions/claudesessions.go`, add to the `transcriptLine` struct:

```go
	// AiTitle carries the session title on builds that emit a
	// {"type":"ai-title"} entry. Absent on builds that do not; the title scan
	// only consults it after the typed-prompt pass finds nothing.
	AiTitle string `json:"aiTitle"`
```

- [ ] **Step 4: Add a shape helper**

Add near `contentText`:

```go
// contentIsString reports whether a message content field decoded as a plain
// JSON string. It is the schema-free way to tell a typed prompt from a tool
// result: Claude records both as type "user", but a tool result always carries
// an ARRAY of content blocks while a typed prompt carries a bare string. Used
// only on transcripts that lack promptSource.
func contentIsString(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var s string
	return json.Unmarshal(raw, &s) == nil
}
```

- [ ] **Step 5: Add the two fallback passes to `readTitle`**

In `internal/claudesessions/claudesessions.go`, replace `readTitle`'s final `return ""` with the two extra passes over the same in-memory window. The existing typed-prompt loop is unchanged and stays first:

```go
	// Fallback 1 — an {"type":"ai-title"} entry, emitted by builds that do not
	// record promptSource. Reached only when no typed prompt was found, so an
	// install whose transcripts carry both is unaffected.
	for _, line := range lines {
		if !bytes.Contains(line, []byte(`"type":"ai-title"`)) {
			continue
		}
		var tl transcriptLine
		if err := json.Unmarshal(bytes.TrimSpace(line), &tl); err != nil {
			continue
		}
		if text := sanitizeTitle(tl.AiTitle); text != "" {
			return text
		}
	}

	// Fallback 2 — the first user entry whose content is a bare STRING. Tool
	// results are recorded as type "user" too, but always with array content,
	// so the shape separates them without a schema marker.
	for _, line := range lines {
		if !bytes.Contains(line, []byte(`"type":"user"`)) {
			continue
		}
		var tl transcriptLine
		if err := json.Unmarshal(bytes.TrimSpace(line), &tl); err != nil {
			continue
		}
		if tl.Type != "user" || tl.IsSidechain || tl.PromptSource != "" ||
			!contentIsString(tl.Message.Content) {
			continue
		}
		if text := sanitizeTitle(contentText(tl.Message.Content, MaxTitleRunes)); text != "" {
			return text
		}
	}
	return ""
```

**The `tl.PromptSource != ""` condition is the correctness guard, not a nicety.** Without it, "additive only" is false in a case that really occurs: a current-schema transcript with zero *typed* prompts but some non-typed string-content user entries — slash-command expansions and compaction-continuation summaries, which are precisely what the `promptSource == "typed"` filter exists to exclude (see `Detail.UserPrompts`'s doc comment). Those would be promoted to titles, and a compaction summary makes a grotesque one. Gating on the field's *absence* confines the fallback to schemas that never record it.

- [ ] **Step 6: Run the test to verify it passes**

Run: `./scripts/dev.sh test internal/claudesessions`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/claudesessions/claudesessions.go internal/claudesessions/claudesessions_test.go
git commit -m "fix(claude): read session titles from older transcript schemas"
```

---

## Task 7: Detail-scan fallback for unfamiliar schemas (§6b)

**Files:**
- Modify: `internal/claudesessions/claudesessions.go` (`readDetail` ~line 330)
- Test: `internal/claudesessions/claudesessions_test.go`

**Interfaces:**
- Consumes: `contentIsString` from Task 6.
- Produces: no signature change. `readDetail` gains a conditional second pass.

### Why

`Detail`'s scan has the same `promptSource` dependency, so `UserPrompts`, `FirstPrompt` and `LastPrompt` all come back empty on an unfamiliar schema.

**Two passes, not one combined pass.** The existing loop's byte pre-filter is load-bearing: the doc comment records that skipping the JSON parse for non-typed lines "is the difference between this being affordable on a keypress and not — the largest transcript on hand is 88 MB". Parsing every `"type":"user"` line unconditionally would regress that for every existing user. A second pass runs **only** when the first found zero prompts, so the normal case pays nothing and only the schema that needs the fallback re-reads.

- [ ] **Step 1: Write the failing test**

Add to `internal/claudesessions/claudesessions_test.go`:

```go
func TestReadDetail_FallsBackToShapeWhenNoPromptSource(t *testing.T) {
	path := writeTranscript(t,
		`{"type":"user","timestamp":"2026-08-19T10:00:00Z","message":{"content":"first question"}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","text":"exit 0"}]}}`,
		`{"type":"user","message":{"content":"second question"}}`,
	)
	d, err := readDetail(context.Background(), path, "11111111-2222-3333-4444-555555555555")
	if err != nil {
		t.Fatalf("readDetail: %v", err)
	}
	if d.UserPrompts != 2 {
		t.Errorf("UserPrompts = %d, want 2 (the tool result is not a prompt)", d.UserPrompts)
	}
	if d.FirstPrompt != "first question" {
		t.Errorf("FirstPrompt = %q, want %q", d.FirstPrompt, "first question")
	}
	if d.LastPrompt != "second question" {
		t.Errorf("LastPrompt = %q, want %q", d.LastPrompt, "second question")
	}
}

func TestReadDetail_TypedTranscriptDoesNotUseFallback(t *testing.T) {
	// A transcript with BOTH shapes: only the typed entry may be counted, or
	// the fallback has changed behaviour for an existing install.
	path := writeTranscript(t,
		`{"type":"user","promptSource":"typed","timestamp":"2026-08-19T10:00:00Z","message":{"content":"typed one"}}`,
		`{"type":"user","message":{"content":"untyped noise"}}`,
	)
	d, err := readDetail(context.Background(), path, "11111111-2222-3333-4444-555555555555")
	if err != nil {
		t.Fatalf("readDetail: %v", err)
	}
	if d.UserPrompts != 1 {
		t.Errorf("UserPrompts = %d, want 1 — the typed pass must win outright", d.UserPrompts)
	}
	if d.LastPrompt != "typed one" {
		t.Errorf("LastPrompt = %q, want %q", d.LastPrompt, "typed one")
	}
}

func TestReadDetail_CurrentSchemaWithNoTypedEntriesStaysEmpty(t *testing.T) {
	// The overbroad-fallback guard, detail-side. Current schema (promptSource
	// present) but nothing typed: the fallback must not reclassify these.
	path := writeTranscript(t,
		`{"type":"user","promptSource":"slash_command","timestamp":"2026-08-19T10:00:00Z","message":{"content":"/compact"}}`,
		`{"type":"user","promptSource":"compact","message":{"content":"This session is being continued from a previous conversation..."}}`,
	)
	d, err := readDetail(context.Background(), path, "11111111-2222-3333-4444-555555555555")
	if err != nil {
		t.Fatalf("readDetail: %v", err)
	}
	if d.UserPrompts != 0 {
		t.Errorf("UserPrompts = %d, want 0 — non-typed current-schema entries are not prompts", d.UserPrompts)
	}
	if d.FirstPrompt != "" || d.LastPrompt != "" {
		t.Errorf("prompts = (%q, %q), want both empty", d.FirstPrompt, d.LastPrompt)
	}
	if d.Started.IsZero() {
		t.Error("Started is zero — the timestamp scan is independent of prompt classification")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `./scripts/dev.sh test internal/claudesessions`
Expected: FAIL — `UserPrompts = 0, want 2`

- [ ] **Step 3: Add the conditional second pass**

In `internal/claudesessions/claudesessions.go`, in `readDetail`, after the existing scan loop and before `return d, nil`:

```go
	// Fallback: a transcript from a build that does not record promptSource
	// yields zero prompts above. Re-scan classifying by CONTENT SHAPE instead
	// — string content is a prompt, array content is a tool result.
	//
	// A second pass rather than one combined pass, deliberately: the loop above
	// owes its speed to rejecting non-typed lines with a byte compare before
	// any JSON parse, and parsing every "type":"user" line unconditionally
	// would regress an 88 MB transcript for every existing user. This runs only
	// when the fast path found nothing, so the normal case pays nothing.
	if d.UserPrompts == 0 {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return d, nil // keep the timestamps already gathered
		}
		r = bufio.NewReaderSize(f, 64<<10)
		userMark := []byte(`"type":"user"`)
		for lineNo := 0; ; lineNo++ {
			if lineNo%4096 == 0 {
				if err := ctx.Err(); err != nil {
					return Detail{}, fmt.Errorf("rescan transcript %s at line %d: %w", id, lineNo, err)
				}
			}
			line, readErr := r.ReadBytes('\n')
			if len(line) > 0 && bytes.Contains(line, userMark) {
				var tl transcriptLine
				if json.Unmarshal(bytes.TrimSpace(line), &tl) == nil &&
					tl.Type == "user" && !tl.IsSidechain && tl.PromptSource == "" &&
					contentIsString(tl.Message.Content) {
					d.UserPrompts++
					if text := sanitizePrompt(contentText(tl.Message.Content, MaxPromptRunes)); text != "" {
						if d.FirstPrompt == "" {
							d.FirstPrompt = text
						}
						d.LastPrompt = text
					}
				}
			}
			if readErr != nil {
				break
			}
		}
	}
```

Change the reader declaration earlier in the function from `r := bufio.NewReaderSize(...)` to keep `r` assignable — it already is, since the fallback reassigns it with `=`.

- [ ] **Step 4: Run the test to verify it passes**

Run: `./scripts/dev.sh test internal/claudesessions`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/claudesessions/claudesessions.go internal/claudesessions/claudesessions_test.go
git commit -m "fix(claude): count prompts in transcripts without promptSource"
```

---

## Task 8: Changelog fragment, full verification, and PR

**Files:**
- Create: `changelog.d/fixed-claude-session-seam.md`

**Interfaces:**
- Consumes: nothing. Produces: nothing.

### Why

CI rejects a PR that touches `cmd/` or `internal/` without a fragment. The fragment must carry a three-line `headline:` front-matter block: ≤64 bytes, no `"` and no `\`, no blank line inside. The promoter strips it into `internal/changelog/highlights.txt` for the What's New dialog.

Only §1, §5 and §6 are user-facing. §2, §3 and §4 are absent from the prose rather than given a second fragment.

- [ ] **Step 1: Write the fragment**

Create `changelog.d/fixed-claude-session-seam.md`. Write it with an editor, not a shell heredoc — a heredoc silently halves backslashes, and this file's prose is checked byte-for-byte into the release notes.

```markdown
---
headline: Claude session resume and history work in more setups
---
- **Claude panes register their session hook reliably on Windows.** The hook
  settings were passed to `claude` as inline JSON. On Windows `claude` is an
  npm `.cmd` shim that Windows re-parses, splitting that JSON at the wrong
  quote boundaries, so the hook silently never registered — and a pane that
  lost it stopped tracking `/clear`, `/resume` and compaction, then resumed a
  stale conversation after a restart. The settings now go to a file and only
  its path is passed.

- **The session picker finds your sessions when `CLAUDE_CONFIG_DIR` is set.**
  Quil always looked in `~/.claude`, so anyone who relocates Claude's config
  directory saw an empty list that was indistinguishable from having no
  sessions at all.

- **Session titles and prompt counts no longer come back blank on some Claude
  builds.** The transcript reader required one specific field that not every
  build writes; sessions still listed, but by UUID with no title. It now falls
  back to the transcript's own title entry, and then to the shape of the entry
  itself.
```

- [ ] **Step 2: Validate the fragment**

Run: `sh scripts/promote-changelog.sh --check`
Expected: exits 0 with no complaint about the fragment name or headline.

- [ ] **Step 3: Run the full test suite and vet**

Run: `./scripts/dev.sh test`
Expected: PASS, all packages

Run: `./scripts/dev.sh vet`
Expected: clean

- [ ] **Step 4: Confirm the hardcodes are gone**

Run:

```bash
grep -rn '"claude-code"' internal/ cmd/ --include=*.go | grep -v _test
```

Expected: exactly three lines — the two doc comments (`internal/plugin/plugin.go`, `internal/tui/pane.go`) and `resumeLabel`'s copy table. Any other hit is a missed §4 site.

- [ ] **Step 5: Build and confirm the binaries are fresh**

Run: `./scripts/dev.sh build`

`build` REFUSES while a running process holds a binary it would write, and the six builds are chained — a refusal partway leaves some fresh and some stale. Confirm the mtimes moved before trusting any manual test:

```bash
ls -l --time-style=+%H:%M:%S quil-dev.exe quild-dev.exe
```

Expected: both stamped within the last few minutes.

- [ ] **Step 6: Manual verification in dev mode**

Launch `./quil-dev.exe` and confirm `[dev]` in the status bar before doing anything else. Never attach to the production daemon.

1. Create a `claude-code` pane. Confirm `.quil/sessions/<paneID>.settings.json` exists and `.quil/sessions/<paneID>.id` appears once the pane starts — that is §1 working end to end.
2. Close the pane. Confirm the `.settings.json` file is gone — that is the cleanup step.
3. Open the create-pane dialog for `claude-code` and confirm the session picker still renders as the last field and every field is reachable with the cursor — that is §3.
4. Restart the dev daemon and confirm a `claude-code` pane resumes its conversation — that is §4 not having broken the resume path.

- [ ] **Step 7: Commit and push**

```bash
git add changelog.d/fixed-claude-session-seam.md
git commit -m "docs(changelog): add fragment for the claude session fixes"
git push -u origin fix/claude-session-seam
```

- [ ] **Step 8: Open the PR**

Title — this is the squash-commit subject the release pipeline classifies, so the conventional type is release-critical. A non-conventional title silently skips the release:

```
fix(claude): correct hook delivery, session store, and dispatch
```

Body:

```markdown
## Summary

- Hook settings go to a per-pane file instead of an inline JSON argument, so
  they survive the Windows npm `.cmd` shim that re-parses the command line.
- The session picker resolves `CLAUDE_CONFIG_DIR` instead of assuming
  `~/.claude`, and the transcript reader no longer requires one schema version
  to produce titles and prompt counts.
- Six hardcoded `"claude-code"` checks now derive the capability from the
  plugin's own `sessions` value, so a renamed or compatible plugin works
  without a code change.

Design: `docs/superpowers/specs/2026-08-19-claude-session-seam-design.md`

Larger than the 400-line guideline in CONTRIBUTING.md, by explicit decision —
the changes share one subsystem and were reviewed together. Suggested review
order: the capability change first (behavioural no-op, widest blast radius, a
missed site fails silently), then the settings-file change (the only one adding
a new failure mode), then the two `claudesessions` fixes.

## Test plan

- `./scripts/dev.sh test` and `./scripts/dev.sh vet` clean.
- New unit tests: settings-file write and traversal rejection; the capability
  predicate including its nil receiver; `CLAUDE_CONFIG_DIR` resolution
  including tilde expansion; title and prompt fallbacks, with a regression test
  asserting a transcript carrying BOTH schemas is read exactly as before.
- Manual, dev mode: settings file written on spawn and removed on close;
  session picker renders and navigates; a `claude-code` pane resumes across a
  daemon restart.
```

---

## Deferred, not forgotten

**§7 — the `--settings` precedence experiment.** Not implemented here. Launch `claude` once with two `--settings` flags, each registering a `SessionStart` hook that writes a distinct marker into a scratch directory (not `$QUIL_HOME`, not any project tree), and see which marker appears. Run on Windows and Linux. The result decides whether §8 is ever built and lets Task 1's "precedence is unverified" comment be replaced with a definite statement.

**§8 — project-settings hook registration.** Blocked on §7. The spec records its five mandatory hardening requirements so they are not rediscovered.

## Self-review notes

Checked against the spec:

- §1 → Task 1. §2 → Task 1 Step 5. §3 → Task 2. §4 → Tasks 3 and 4. §5 → Task 5. §6 → Tasks 6 and 7. §7 and §8 → deferred above, with §7's procedure carried over verbatim.
- Two deviations from the spec, both deliberate and recorded above rather than silently absorbed:
  1. `resumeLabel` keeps its `switch paneType` copy table (Task 4 Step 4). It is user-facing phrasing, not dispatch, and no plugin field carries a verb phrase. The spec's §4 success criterion was amended to match.
  2. The TUI's predicate is the **resume strategy**, not the Claude capability (Task 4 Step 6). The spec framed §4 as one capability; implementation found the daemon already owns the right predicate in `restoresOwnHistory` (`daemon.go:1565`), whose comment explicitly rejects a plugin-name list for this question. The TUI's `restoresViaSession` is that predicate with a stale name list, so Task 4 promotes the daemon's version to `plugin.PanePlugin` and both consume it. This is strictly better than the spec's framing: `session_scrape` covers opencode with no special case, and a future session-resuming plugin needs no edit.
- Type consistency: `UsesClaudeSessions` is the method name in Tasks 3 and 4's *daemon* sites only; `usesClaudeSessions` is the daemon helper in Task 3; `RestoresOwnHistory` is the shared predicate introduced in Task 4 and consumed by both `internal/daemon` and `internal/tui`; `contentIsString` is introduced in Task 6 and consumed in Task 7.
  3. **Task 7 runs two passes where spec §6 said one.** The spec called for tracking typed and shape-derived counters side by side in the single existing scan. That would JSON-parse every `"type":"user"` tool-result line, which is exactly the cost `readDetail`'s existing byte pre-filter comment forbids ("the difference between this being affordable on a keypress and not — the largest transcript on hand is 88 MB"). The conditional second pass produces the same results at zero cost for the normal case. Spec §6 was amended to match rather than left contradicting the plan.
  4. **A `promptSource`-absent condition was added to both fallbacks**, which the spec's first draft omitted. Without it "additive only" is false for a current-schema transcript that has no typed prompt but does have non-typed string-content user entries (slash-command expansions, compaction summaries). Both spec and plan now carry the guard and a regression test for it.
- Two capabilities, deliberately distinct: `UsesClaudeSessions` answers "does this speak Claude's hook and transcript protocol" (daemon: hook injection, hook-record reads, occupancy, resume promotion). `RestoresOwnHistory` answers "does the child repaint its own history from a session id" (daemon: ghost-replay skip; TUI: restore checklist). claude-code is both; opencode is only the second. Collapsing them would break one of the two.
