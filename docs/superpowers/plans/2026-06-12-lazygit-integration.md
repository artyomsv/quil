# Lazygit Integration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Lazygit integration per spec `docs/superpowers/specs/2026-06-12-lazygit-integration-design.md` — built-in plugin, git-aware setup dialog, per-tab toggle overlay (Alt+G).

**Architecture:** Three independent phases. Phase 1 adds a `lazygit` default plugin TOML and a pure `internal/gitdiscover` package. Phase 2 adds a `discover = "git"` plugin opt-in that swaps the setup dialog's directory browser for a repo-candidate pick list. Phase 3 adds an ephemeral overlay pane: a daemon-side `Pane.Overlay` flag excluded from disk snapshots, a TUI-side per-tab overlay slot outside the layout tree, and an Alt+G state machine.

**Tech Stack:** Go 1.25, Bubble Tea v2, BurntSushi/toml. Build/test via Docker: `./scripts/dev.sh test` (full suite — there is no per-package form), `./scripts/dev.sh vet`. Host has no Go toolchain.

**Branch:** work on `feat/lazygit-integration` (already exists, contains the spec).

**Conventions that apply to every task:** tabs in Go files, errors wrapped with `fmt.Errorf("doing X: %w", err)`, table-driven tests named `TestFunc_Scenario`, `t.TempDir()`/`t.Setenv()` for fixtures, no mocking frameworks (manual fakes). Never run the production `quil`/`quild` or touch `~/.quil/` — manual verification uses `./quil-dev.exe` only (see `.claude/rules/dev-environment.md`).

---

## Phase 1 — gitdiscover package + lazygit plugin

### Task 1: `internal/gitdiscover` package

**Files:**
- Create: `internal/gitdiscover/gitdiscover.go`
- Create: `internal/gitdiscover/gitdiscover_test.go`

- [ ] **Step 1: Write the failing tests**

```go
package gitdiscover

import (
	"os"
	"path/filepath"
	"testing"
)

// mkRepo creates dir with a .git subdirectory (normal repo shape).
func mkRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o700); err != nil {
		t.Fatalf("mkRepo %s: %v", dir, err)
	}
}

// mkWorktree creates dir with a .git FILE (worktree/submodule shape).
func mkWorktree(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkWorktree mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /elsewhere\n"), 0o600); err != nil {
		t.Fatalf("mkWorktree write %s: %v", dir, err)
	}
}

func TestEnclosingRepo_RepoRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	mkRepo(t, root)
	got, ok := EnclosingRepo(root)
	if !ok {
		t.Fatal("expected ok=true at repo root")
	}
	if got != root {
		t.Errorf("got %q, want %q", got, root)
	}
}

func TestEnclosingRepo_NestedDir(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	mkRepo(t, root)
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	got, ok := EnclosingRepo(nested)
	if !ok || got != root {
		t.Errorf("got (%q, %v), want (%q, true)", got, ok, root)
	}
}

func TestEnclosingRepo_GitFile_Worktree(t *testing.T) {
	t.Parallel()
	wt := filepath.Join(t.TempDir(), "wt")
	mkWorktree(t, wt)
	got, ok := EnclosingRepo(wt)
	if !ok || got != wt {
		t.Errorf("got (%q, %v), want (%q, true)", got, ok, wt)
	}
}

func TestEnclosingRepo_NoRepo(t *testing.T) {
	t.Parallel()
	if got, ok := EnclosingRepo(t.TempDir()); ok {
		t.Errorf("expected not found, got %q", got)
	}
}

func TestEnclosingRepo_EmptyAndMissing(t *testing.T) {
	t.Parallel()
	if _, ok := EnclosingRepo(""); ok {
		t.Error("empty dir must not resolve")
	}
	if _, ok := EnclosingRepo(filepath.Join(t.TempDir(), "does-not-exist")); ok {
		// A missing dir whose PARENT chain contains no repo must not resolve.
		t.Error("missing dir with no enclosing repo must not resolve")
	}
}

func TestSubRepos_OneLevel(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	mkRepo(t, filepath.Join(base, "proj-a"))
	mkRepo(t, filepath.Join(base, "proj-b"))
	mkWorktree(t, filepath.Join(base, "proj-wt"))
	// Non-repo subdir and a deeper repo that must NOT be found (2 levels).
	if err := os.MkdirAll(filepath.Join(base, "plain", "deep"), 0o700); err != nil {
		t.Fatal(err)
	}
	mkRepo(t, filepath.Join(base, "plain", "deep"))

	got := SubRepos(base)
	want := map[string]bool{
		filepath.Join(base, "proj-a"):  true,
		filepath.Join(base, "proj-b"):  true,
		filepath.Join(base, "proj-wt"): true,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d repos %v, want %d", len(got), got, len(want))
	}
	for _, r := range got {
		if !want[r] {
			t.Errorf("unexpected sub-repo %q", r)
		}
	}
}

func TestSubRepos_UnreadableDir_ReturnsNil(t *testing.T) {
	t.Parallel()
	if got := SubRepos(filepath.Join(t.TempDir(), "nope")); got != nil {
		t.Errorf("expected nil for unreadable dir, got %v", got)
	}
}

func TestCandidates_EnclosingFirstThenSubsDeduped(t *testing.T) {
	t.Parallel()
	// base is itself a repo AND contains a sub-repo: enclosing (=base) must
	// come first, sub second, base not duplicated.
	base := t.TempDir()
	mkRepo(t, base)
	sub := filepath.Join(base, "vendor-fork")
	mkRepo(t, sub)

	got := Candidates(base)
	if len(got) != 2 {
		t.Fatalf("got %v, want [base sub]", got)
	}
	if got[0] != base || got[1] != sub {
		t.Errorf("got %v, want [%q %q]", got, base, sub)
	}
}

func TestCandidates_EmptyDir(t *testing.T) {
	t.Parallel()
	if got := Candidates(""); got != nil {
		t.Errorf("expected nil for empty dir, got %v", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `./scripts/dev.sh test`
Expected: FAIL — `internal/gitdiscover` does not compile (undefined: EnclosingRepo, SubRepos, Candidates).

- [ ] **Step 3: Write the implementation**

```go
// Package gitdiscover finds git repositories near a directory: the enclosing
// repo (upward walk, same discovery rule as git itself) and immediate
// sub-repositories (one level down). Pure filesystem probes — no git binary,
// no I/O errors surfaced: every failure degrades to "no candidates" so
// discovery can never block pane creation.
package gitdiscover

import (
	"os"
	"path/filepath"
)

// maxWalkUp bounds EnclosingRepo's upward walk. The walk already terminates
// at the volume root (filepath.Dir fixpoint); the cap is a belt-and-suspenders
// guard against symlink-loop pathologies.
const maxWalkUp = 32

// hasGitEntry reports whether dir contains a .git entry. Both shapes count:
// a directory (normal repo) and a regular file (worktree / submodule).
func hasGitEntry(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, ".git"))
	if err != nil {
		return false
	}
	return info.IsDir() || info.Mode().IsRegular()
}

// EnclosingRepo walks up from dir and returns the first ancestor (or dir
// itself) containing a .git entry. Symlinks are resolved first to match the
// daemon's defaultCWD discipline.
func EnclosingRepo(dir string) (string, bool) {
	if dir == "" {
		return "", false
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	cur := abs
	for i := 0; i < maxWalkUp; i++ {
		if hasGitEntry(cur) {
			return cur, true
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", false
		}
		cur = parent
	}
	return "", false
}

// SubRepos returns the immediate (one level down) subdirectories of dir that
// contain a .git entry. Returns nil on any read error.
func SubRepos(dir string) []string {
	if dir == "" {
		return nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil
	}
	var repos []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sub := filepath.Join(abs, e.Name())
		if hasGitEntry(sub) {
			repos = append(repos, sub)
		}
	}
	return repos
}

// Candidates returns the repos to offer for dir: the enclosing repo first
// (if any), then one-level sub-repos, deduplicated, absolute paths.
func Candidates(dir string) []string {
	if dir == "" {
		return nil
	}
	var out []string
	seen := make(map[string]bool)
	if root, ok := EnclosingRepo(dir); ok {
		out = append(out, root)
		seen[root] = true
	}
	for _, r := range SubRepos(dir) {
		if !seen[r] {
			out = append(out, r)
			seen[r] = true
		}
	}
	return out
}
```

Note: `t.TempDir()` on macOS returns paths under `/var` (symlink to `/private/var`); `EnclosingRepo` resolves symlinks, so the `TestEnclosingRepo_RepoRoot` comparison could mismatch. Guard the tests by resolving the fixture root once: at the top of each test that compares paths, replace `root := t.TempDir()` with:

```go
root, err := filepath.EvalSymlinks(t.TempDir())
if err != nil {
	t.Fatal(err)
}
```

(CI runs Linux in Docker where this is a no-op, but keep the tests portable.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `./scripts/dev.sh test`
Expected: PASS (all packages).

- [ ] **Step 5: Commit**

```bash
git add internal/gitdiscover/
git commit -m "feat(gitdiscover): add git repo discovery package

Pure filesystem probes: EnclosingRepo (upward walk, .git dir or
file), SubRepos (one level down), Candidates (enclosing first,
deduped). I/O errors degrade to no-candidates by design."
```

### Task 2: `discover` field in the plugin schema

**Files:**
- Modify: `internal/plugin/plugin.go` (CommandConfig, ~line 47 after RawKeys)
- Modify: `internal/plugin/registry.go` (tomlPlugin ~line 303, loadPluginTOML ~line 388)
- Test: `internal/plugin/plugin_test.go`

- [ ] **Step 1: Write the failing test** (append to `plugin_test.go`; follow the existing pattern there of writing TOML bytes to a temp file and calling `loadPluginTOML` — see `TestLoadPluginTOML*` siblings)

```go
func TestLoadPluginTOML_DiscoverField(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "lg.toml")
	data := []byte(`
[plugin]
name = "lg"
[command]
cmd = "lazygit"
discover = "git"
prompts_cwd = true
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := loadPluginTOML(path)
	if err != nil {
		t.Fatalf("loadPluginTOML: %v", err)
	}
	if p.Command.Discover != "git" {
		t.Errorf("Discover = %q, want %q", p.Command.Discover, "git")
	}
}

func TestLoadPluginTOML_DiscoverInvalid(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.toml")
	data := []byte(`
[plugin]
name = "bad"
[command]
cmd = "x"
discover = "svn"
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPluginTOML(path); err == nil {
		t.Error("expected error for discover=\"svn\"")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `./scripts/dev.sh test`
Expected: FAIL — `p.Command.Discover undefined`.

- [ ] **Step 3: Implement.** Three edits:

In `internal/plugin/plugin.go`, add to `CommandConfig` after `RawKeys`:

```go
	// Discover selects a repo-discovery mode for the pane setup dialog.
	// "" = none (plain directory browser). "git" = the CWD step lists git
	// repo candidates (enclosing repo + 1-level sub-repos of the active
	// pane's CWD) with a "Browse…" escape hatch. Only meaningful when
	// PromptsCWD is true.
	Discover string
```

In `internal/plugin/registry.go`, add to `tomlPlugin.Command` after `RawKeys []string`:

```go
		Discover string `toml:"discover"`
```

In `loadPluginTOML`, add validation after the persistence-strategy switch (~line 360):

```go
	switch tp.Command.Discover {
	case "", "git":
		// valid
	default:
		return nil, fmt.Errorf("plugin %q: unknown discover mode %q", tp.Plugin.Name, tp.Command.Discover)
	}
```

and copy the field in the `CommandConfig` literal (~line 388, next to `PromptsCWD`):

```go
			Discover:         tp.Command.Discover,
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `./scripts/dev.sh test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/plugin/plugin.go internal/plugin/registry.go internal/plugin/plugin_test.go
git commit -m "feat(plugin): add discover field to command schema

discover = \"git\" opts a plugin into git-repo candidate discovery
in the pane setup dialog. Validated at TOML load (only \"\"/\"git\")."
```

### Task 3: lazygit built-in plugin

**Files:**
- Create: `internal/plugin/defaults/lazygit.toml` (auto-embedded by `//go:embed defaults/*.toml` in `internal/plugin/defaults.go:12` — no Go change needed)
- Test: `internal/plugin/defaults_test.go`

- [ ] **Step 1: Write the failing test** (append to `defaults_test.go`)

```go
func TestEnsureDefaultPlugins_WritesLazygit(t *testing.T) {
	dir := t.TempDir()
	if _, err := EnsureDefaultPlugins(dir); err != nil {
		t.Fatalf("EnsureDefaultPlugins: %v", err)
	}
	p, err := loadPluginTOML(filepath.Join(dir, "lazygit.toml"))
	if err != nil {
		t.Fatalf("load lazygit.toml: %v", err)
	}
	if p.Name != "lazygit" || p.Command.Cmd != "lazygit" {
		t.Errorf("name/cmd = %q/%q", p.Name, p.Command.Cmd)
	}
	if !p.Command.PromptsCWD || p.Command.Discover != "git" {
		t.Errorf("PromptsCWD=%v Discover=%q, want true/git", p.Command.PromptsCWD, p.Command.Discover)
	}
	if p.Persistence.Strategy != "rerun" || p.Persistence.GhostBuffer {
		t.Errorf("strategy=%q ghost=%v, want rerun/false", p.Persistence.Strategy, p.Persistence.GhostBuffer)
	}
	if len(p.Command.Toggles) != 1 || p.Command.Toggles[0].Name != "screen_mode_full" {
		t.Errorf("toggles = %+v", p.Command.Toggles)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test`
Expected: FAIL — `load lazygit.toml: ... no such file`.

- [ ] **Step 3: Create `internal/plugin/defaults/lazygit.toml`**

```toml
# Lazygit — git TUI for the current workspace
# Edit this file to customize the plugin. Delete it to restore defaults.

[plugin]
name = "lazygit"
display_name = "Lazygit"
category = "tools"
description = "Git TUI for the current workspace"
schema_version = 1

[command]
cmd = "lazygit"
# path = "/path/to/lazygit"  # uncomment to override PATH lookup
detect = "lazygit --version"
prompts_cwd = true
# The CWD step lists git repos found near the active pane's directory
# (enclosing repo + one-level sub-repos) instead of the plain browser.
discover = "git"

[[command.toggles]]
name = "screen_mode_full"
label = "Open focused panel full-screen"
args_when_on = ["--screen-mode", "full"]
default = false

[persistence]
# Re-run lazygit in the saved CWD on daemon restart. Lazygit discovers
# the repo by walking up from its CWD, exactly like git itself.
strategy = "rerun"
# Full-screen TUI — replaying stale frames on reconnect is useless.
ghost_buffer = false
```

- [ ] **Step 4: Run test to verify it passes**

Run: `./scripts/dev.sh test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/plugin/defaults/lazygit.toml internal/plugin/defaults_test.go
git commit -m "feat(plugin): add lazygit built-in plugin

prompts_cwd + discover=git, rerun persistence, ghost buffer off,
optional --screen-mode full toggle. Binary gating comes free from
DetectAvailability (path override -> LookPath -> searchBinary)."
```

---

## Phase 2 — git-aware setup dialog

### Task 4: repo-candidate state + wiring in `enterSetupOrSplit`

**Files:**
- Modify: `internal/tui/model.go` (~line 235, next to `lastSelectedCWD`)
- Modify: `internal/tui/dialog.go` (`enterSetupOrSplit` at line 1877)
- Test: `internal/tui/setup_dialog_test.go`

**Design recap:** when the plugin has `Discover == "git"` and candidates are found, the CWD field of the setup dialog renders the candidate list (plus a final "Browse…" row) instead of the directory browser. `m.cwdBrowseDir` doubles as "currently selected candidate" so `submitSetupDialog` (dialog.go:2334, which copies `m.cwdBrowseDir` into `m.selectedCWD`) needs **no change**. `m.cwdBrowseCursor` doubles as the list cursor. Choosing "Browse…" clears `repoCandidates` and falls back to the existing browser init.

- [ ] **Step 1: Write the failing tests** (append to `setup_dialog_test.go`; it already builds `*Model` literals directly — follow that pattern)

```go
func TestEnterSetupOrSplit_GitDiscover_PopulatesCandidates(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}

	pane := NewPaneModel("pane-1", 1024)
	pane.CWD = root
	tab := NewTabModel("tab-1", "t")
	tab.Root = NewLeaf(pane)
	tab.ActivePane = pane.ID

	m := &Model{tabs: []*TabModel{tab}, activeTab: 0}
	p := &plugin.PanePlugin{
		Name: "lazygit",
		Command: plugin.CommandConfig{
			Cmd:        "lazygit",
			PromptsCWD: true,
			Discover:   "git",
		},
	}
	m.enterSetupOrSplit(p)

	if len(m.repoCandidates) != 1 || m.repoCandidates[0] != root {
		t.Fatalf("repoCandidates = %v, want [%q]", m.repoCandidates, root)
	}
	if m.cwdBrowseDir != root {
		t.Errorf("cwdBrowseDir = %q, want first candidate pre-selected %q", m.cwdBrowseDir, root)
	}
	if m.dialog != dialogCreatePaneSetup {
		t.Errorf("dialog = %v, want dialogCreatePaneSetup", m.dialog)
	}
}

func TestEnterSetupOrSplit_GitDiscover_NoRepo_FallsBackToBrowser(t *testing.T) {
	plain, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pane := NewPaneModel("pane-1", 1024)
	pane.CWD = plain
	tab := NewTabModel("tab-1", "t")
	tab.Root = NewLeaf(pane)
	tab.ActivePane = pane.ID

	m := &Model{tabs: []*TabModel{tab}, activeTab: 0}
	p := &plugin.PanePlugin{
		Name: "lazygit",
		Command: plugin.CommandConfig{
			Cmd:        "lazygit",
			PromptsCWD: true,
			Discover:   "git",
		},
	}
	m.enterSetupOrSplit(p)

	if len(m.repoCandidates) != 0 {
		t.Fatalf("repoCandidates = %v, want empty", m.repoCandidates)
	}
	if m.cwdBrowseDir == "" {
		t.Error("browser should have initialized via the normal pre-fill chain")
	}
}

func TestEnterSetupOrSplit_GitDiscover_ClearsStaleCandidates(t *testing.T) {
	m := &Model{repoCandidates: []string{"/stale"}}
	// Plugin with no setup at all — state must still be cleared.
	m.enterSetupOrSplit(&plugin.PanePlugin{Name: "terminal", Command: plugin.CommandConfig{Cmd: "sh"}})
	if m.repoCandidates != nil {
		t.Errorf("repoCandidates = %v, want nil", m.repoCandidates)
	}
}
```

Check the existing tests in `setup_dialog_test.go:217-231` for how `Model` literals are seeded — if `activeTabModel()` requires more fields than `tabs`/`activeTab`, mirror what `TestEnterSetupOrSplit`-adjacent tests already do.

- [ ] **Step 2: Run tests to verify they fail**

Run: `./scripts/dev.sh test`
Expected: FAIL — `m.repoCandidates undefined`.

- [ ] **Step 3: Implement.** In `internal/tui/model.go` next to `lastSelectedCWD` (~line 235):

```go
	repoCandidates []string // git repos offered by the setup dialog (discover="git"); nil = plain browser
```

In `internal/tui/dialog.go` `enterSetupOrSplit`: add to the reset block at the top (after `m.cwdBrowseScroll = 0`):

```go
	m.repoCandidates = nil
```

Then extract the existing browser-init loop (lines 1898–1921, the `candidates := []string{m.lastSelectedCWD}` block) into a method on `*Model` so the "Browse…" row can reuse it:

```go
// initSetupBrowser seeds the directory browser using the standard pre-fill
// chain: last selected CWD -> active pane OSC7 CWD -> home. Stale entries
// are skipped (and lastSelectedCWD cleared) exactly as before.
func (m *Model) initSetupBrowser() {
	candidates := []string{m.lastSelectedCWD}
	if tab := m.activeTabModel(); tab != nil {
		if pane := tab.ActivePaneModel(); pane != nil {
			candidates = append(candidates, pane.CWD)
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, home)
	}
	for _, dir := range candidates {
		if dir == "" {
			continue
		}
		if err := m.loadBrowseDir(dir); err != nil {
			log.Printf("setup dialog: load browse dir %q failed, trying next: %v", dir, err)
			if dir == m.lastSelectedCWD {
				m.lastSelectedCWD = "" // clear stale memory
			}
			continue
		}
		break
	}
}
```

and replace the `if p.Command.PromptsCWD { ... }` body with:

```go
	if p.Command.PromptsCWD {
		if p.Command.Discover == "git" {
			// Discovery base is the active pane's OSC7 CWD DIRECTLY — not
			// lastSelectedCWD (that memory belongs to the generic browser;
			// a stale last-choice from another project would seed wrong
			// candidates).
			var base string
			if tab := m.activeTabModel(); tab != nil {
				if pane := tab.ActivePaneModel(); pane != nil {
					base = pane.CWD
				}
			}
			m.repoCandidates = gitdiscover.Candidates(base)
		}
		if len(m.repoCandidates) > 0 {
			// Pre-select the first candidate so Enter-through submits it.
			m.cwdBrowseDir = m.repoCandidates[0]
			m.cwdBrowseCursor = 0
		} else {
			m.initSetupBrowser()
		}
	}
```

Add the import `"github.com/artyomsv/quil/internal/gitdiscover"` to dialog.go.

- [ ] **Step 4: Run tests to verify they pass**

Run: `./scripts/dev.sh test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/model.go internal/tui/dialog.go internal/tui/setup_dialog_test.go
git commit -m "feat(tui): populate git repo candidates in pane setup dialog

discover=git plugins seed repoCandidates from the active pane's
OSC7 CWD (deliberately not lastSelectedCWD). Browser init extracted
to initSetupBrowser for reuse by the Browse… escape hatch."
```

### Task 5: candidate-list key handling + rendering

**Files:**
- Modify: `internal/tui/dialog.go` (`handleSetupCWDKey` at line 2204; the CWD-field section of the setup render function — find it via `grep -n "renderCreatePaneSetup" internal/tui/dialog.go`)
- Test: `internal/tui/setup_dialog_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// repoPickModel builds a Model sitting in the setup dialog with a candidate
// list, mirroring what enterSetupOrSplit produces for discover="git".
func repoPickModel(t *testing.T, candidates []string) Model {
	t.Helper()
	return Model{
		dialog:         dialogCreatePaneSetup,
		repoCandidates: candidates,
		cwdBrowseDir:   candidates[0],
		pluginRegistry: registryWithLazygit(t),
		selectedPlugin: "lazygit",
	}
}

// registryWithLazygit returns a plugin registry containing a minimal
// lazygit plugin with PromptsCWD+Discover set and Available forced true.
// Registry has NO Register method — the established pattern (see
// setup_dialog_test.go:440-456) is temp TOML + LoadFromDir, then flip the
// exported Available field via Get.
func registryWithLazygit(t *testing.T) *plugin.Registry {
	t.Helper()
	dir := t.TempDir()
	content := `[plugin]
name = "lazygit"

[command]
cmd = "lazygit"
prompts_cwd = true
discover = "git"
`
	if err := os.WriteFile(filepath.Join(dir, "lazygit.toml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write test toml: %v", err)
	}
	r := plugin.NewRegistry()
	if err := r.LoadFromDir(dir); err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}
	r.Get("lazygit").Available = true
	return r
}

func TestSetupRepoKey_DownMovesAndSelects(t *testing.T) {
	t.Parallel()
	m := repoPickModel(t, []string{"/repo-a", "/repo-b"})
	out, _ := m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: tea.KeyDown})
	got := out.(Model)
	if got.cwdBrowseCursor != 1 {
		t.Errorf("cursor = %d, want 1", got.cwdBrowseCursor)
	}
	if got.cwdBrowseDir != "/repo-b" {
		t.Errorf("cwdBrowseDir = %q, want /repo-b", got.cwdBrowseDir)
	}
}

func TestSetupRepoKey_BrowseRowFallsBackToBrowser(t *testing.T) {
	m := repoPickModel(t, []string{"/repo-a"})
	// Cursor to the "Browse…" row (index == len(candidates)).
	m.cwdBrowseCursor = 1
	out, _ := m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := out.(Model)
	if got.repoCandidates != nil {
		t.Errorf("repoCandidates = %v, want nil after Browse…", got.repoCandidates)
	}
}
```

Note on key construction: check how existing tests in `setup_dialog_test.go` build `tea.KeyPressMsg` values (arrow keys vs text keys) and copy that exact construction — Bubble Tea v2 key codes, not strings.

- [ ] **Step 2: Run tests to verify they fail**

Run: `./scripts/dev.sh test`
Expected: FAIL (cursor/selection unchanged — repo branch doesn't exist yet).

- [ ] **Step 3: Implement.** At the top of `handleSetupCWDKey` (dialog.go:2204), before the `len(m.cwdBrowseEntries) == 0` check:

```go
	if len(m.repoCandidates) > 0 {
		return m.handleSetupRepoKey(p, key)
	}
```

New method (place after `handleSetupCWDKey`):

```go
// handleSetupRepoKey processes keystrokes when the CWD field is in repo-pick
// mode (discover="git" found candidates). Rows are the candidates plus one
// trailing "Browse…" escape hatch. cwdBrowseCursor is the row cursor and
// cwdBrowseDir mirrors the highlighted candidate so submitSetupDialog's
// selectedCWD = cwdBrowseDir capture works unchanged.
func (m Model) handleSetupRepoKey(p *plugin.PanePlugin, key string) (tea.Model, tea.Cmd) {
	rows := len(m.repoCandidates) + 1 // + Browse…
	syncSelection := func() {
		if m.cwdBrowseCursor < len(m.repoCandidates) {
			m.cwdBrowseDir = m.repoCandidates[m.cwdBrowseCursor]
		}
	}
	switch key {
	case "up", "k":
		if m.cwdBrowseCursor > 0 {
			m.cwdBrowseCursor--
			syncSelection()
		}
		return m, nil
	case "down", "j":
		if m.cwdBrowseCursor < rows-1 {
			m.cwdBrowseCursor++
			syncSelection()
		}
		return m, nil
	case "enter":
		if m.cwdBrowseCursor == len(m.repoCandidates) {
			// Browse… — drop candidate mode, fall back to the directory
			// browser with its normal pre-fill chain.
			m.repoCandidates = nil
			m.cwdBrowseDir = ""
			m.cwdBrowseCursor = 0
			m.initSetupBrowser()
			return m, nil
		}
		// Selecting a candidate submits the dialog (the repo IS the answer
		// to the CWD question; toggles keep their defaults unless the user
		// tabbed to them first).
		m.cwdBrowseDir = m.repoCandidates[m.cwdBrowseCursor]
		return m.submitSetupDialog(p)
	}
	return m, nil
}
```

Rendering: locate the CWD-field section in the setup render function (`grep -n "cwdBrowse" internal/tui/dialog.go` — the render function iterates `cwdBrowseEntries`). Add a branch BEFORE the browser listing: when `len(m.repoCandidates) > 0`, render one line per candidate plus the Browse row, marking the cursor row the same way the browser marks its cursor row (reuse the exact same prefix/style the browser rows use, e.g. `"> "` vs `"  "`):

```go
	if len(m.repoCandidates) > 0 {
		for i, repo := range m.repoCandidates {
			marker := "  "
			if focused && i == m.cwdBrowseCursor {
				marker = "> "
			}
			lines = append(lines, marker+displayPath(repo))
		}
		marker := "  "
		if focused && m.cwdBrowseCursor == len(m.repoCandidates) {
			marker = "> "
		}
		lines = append(lines, marker+"Browse…")
	} else {
		// existing browser listing
	}
```

Adapt variable names (`lines`, `focused`, path-shortening helper) to what the render function actually uses — the structure above is the contract; the surrounding render code dictates the exact builder. If no `displayPath` helper exists, render the absolute path as-is (the dialog is 60 cols; long paths truncate from the left with `…` if the browser rows already do that, otherwise don't add truncation).

- [ ] **Step 4: Run tests to verify they pass**

Run: `./scripts/dev.sh test`
Expected: PASS.

- [ ] **Step 5: Manual verification (dev mode).** Build `./scripts/dev.sh build`, run `./scripts/quil-dev.ps1`, confirm `[dev]` in the status bar. With lazygit installed: Ctrl+N → Tools → Lazygit → candidate list shows this repo; Browse… falls back to the directory browser; Enter on a candidate proceeds to split selection and the pane opens lazygit in the repo.

- [ ] **Step 6: Commit**

```bash
git add internal/tui/dialog.go internal/tui/setup_dialog_test.go
git commit -m "feat(tui): repo candidate pick-list in pane setup dialog

discover=git plugins render git repo candidates with a Browse…
escape hatch in place of the directory browser. Enter on a
candidate submits; Browse… falls back to the normal browser."
```

---

## Phase 3 — toggle overlay

### Task 6: `Pane.Overlay` — IPC field, daemon flag, state broadcast, snapshot exclusion

**Files:**
- Modify: `internal/ipc/protocol.go` (CreatePanePayload, line 104)
- Modify: `internal/daemon/session.go` (Pane struct, ~line 63 after Eager)
- Modify: `internal/daemon/daemon.go` (`handleCreatePane` ~line 1045; `workspaceStateFromSnapshot` line 1481 + its two call sites at lines 351 and 1474)
- Test: `internal/daemon/daemon_test.go`

- [ ] **Step 1: Write the failing tests** (append to `daemon_test.go`; mirror the existing `workspaceStateFromSnapshot` test at line 87 for daemon/tab/pane construction)

```go
func TestWorkspaceState_OverlayPane_BroadcastVsDisk(t *testing.T) {
	d := newTestDaemon(t) // reuse whatever helper the existing test at line ~87 uses
	tab := d.session.CreateTab("t")
	normal, err := d.session.CreatePane(tab.ID, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	overlay, err := d.session.CreatePane(tab.ID, "/tmp/repo")
	if err != nil {
		t.Fatal(err)
	}
	overlay.Overlay = true

	tabs, panesByTab := []*Tab{tab}, map[string][]*Pane{tab.ID: {normal, overlay}}

	// Broadcast: overlay included, flagged.
	live := d.workspaceStateFromSnapshot(tab.ID, tabs, panesByTab, true)
	livePanes := live["panes"].([]map[string]any)
	if len(livePanes) != 2 {
		t.Fatalf("broadcast panes = %d, want 2", len(livePanes))
	}
	var flagged bool
	for _, p := range livePanes {
		if p["id"] == overlay.ID && p["overlay"] == true {
			flagged = true
		}
	}
	if !flagged {
		t.Error("broadcast must carry overlay=true for the overlay pane")
	}

	// Disk: overlay pane absent from panes AND from the tab's pane-ID list.
	disk := d.workspaceStateFromSnapshot(tab.ID, tabs, panesByTab, false)
	diskPanes := disk["panes"].([]map[string]any)
	if len(diskPanes) != 1 || diskPanes[0]["id"] != normal.ID {
		t.Fatalf("disk panes = %v, want only %s", diskPanes, normal.ID)
	}
	diskTabs := disk["tabs"].([]map[string]any)
	ids := diskTabs[0]["panes"].([]string)
	for _, id := range ids {
		if id == overlay.ID {
			t.Error("disk tab pane-ID list must not reference the overlay pane")
		}
	}
}
```

The tab's pane-ID membership: `SessionManager.CreatePane` appends the pane ID to `tab.Panes` — verify by reading `CreatePane` in session.go; if it does not, append manually in the test (`tab.Panes = append(tab.Panes, normal.ID, overlay.ID)`).

- [ ] **Step 2: Run tests to verify they fail**

Run: `./scripts/dev.sh test`
Expected: FAIL — `overlay.Overlay undefined` / wrong arity on `workspaceStateFromSnapshot`.

- [ ] **Step 3: Implement.**

`internal/ipc/protocol.go` — `CreatePanePayload`:

```go
	// Overlay marks the pane as a TUI overlay (lazygit toggle view): it
	// never enters the layout tree, is muted at creation, and is excluded
	// from disk snapshots (ephemeral — gone on daemon restart).
	Overlay bool `json:"overlay,omitempty"`
```

`internal/daemon/session.go` — `Pane`, after `Eager`:

```go
	// Overlay marks an ephemeral TUI overlay pane (lazygit toggle view).
	// Set once in handleCreatePane before the pane is first broadcast and
	// never mutated afterwards (immutable post-creation, like ID/TabID),
	// so readers take no lock. Excluded from disk snapshots.
	Overlay bool
```

`internal/daemon/daemon.go` — `handleCreatePane`: read the rest of the handler (lines 1045–1120) to find where `pane.Type`, `pane.InstanceName`, `pane.InstanceArgs` are assigned after `d.session.CreatePane`; immediately after those assignments (still before `spawnPane`/first broadcast — dispatch is serialized, so no broadcast can interleave), add:

```go
	if payload.Overlay {
		pane.Overlay = true
		// Overlay panes are muted at the source: a hidden lazygit
		// refreshing must not ping the notification sidebar.
		pane.PluginMu.Lock()
		pane.Muted = true
		pane.PluginMu.Unlock()
	}
```

`workspaceStateFromSnapshot` — new signature and body changes:

```go
func (d *Daemon) workspaceStateFromSnapshot(activeTab string, tabs []*Tab, panesByTab map[string][]*Pane, includeOverlays bool) map[string]any {
```

Inside the per-tab loop, replace the pane-ID copy (`paneIDs := make([]string, len(tab.Panes)); copy(paneIDs, tab.Panes)`) with a filtered build:

```go
		overlayIDs := make(map[string]bool)
		for _, pane := range panesByTab[tab.ID] {
			if pane.Overlay {
				overlayIDs[pane.ID] = true
			}
		}
		paneIDs := make([]string, 0, len(tab.Panes))
		for _, pid := range tab.Panes {
			if !includeOverlays && overlayIDs[pid] {
				continue
			}
			paneIDs = append(paneIDs, pid)
		}
```

In the pane loop, first line:

```go
		for _, pane := range panesByTab[tab.ID] {
			if !includeOverlays && pane.Overlay {
				continue
			}
```

and after the `eager` write (next to `paneData["eager"]`):

```go
			if pane.Overlay {
				paneData["overlay"] = true
			}
```

Call sites: line 351 (`snapshot()` → `persist.Save`) passes `false`; line 1474 (`buildWorkspaceState`, the broadcast/attach path) passes `true`; `daemon_test.go:87` passes `false` (it asserts disk semantics — verify the test still passes, adjust the literal if it tests broadcast shape).

Also check the ghost-buffer loop in `snapshot()` (daemon.go:362–376): it iterates all panes to save buffers. Add the same guard so an overlay pane never writes a ghost file:

```go
			if pane.Overlay {
				continue
			}
```

(lazygit sets `ghost_buffer = false` anyway, but the flag must not depend on plugin lookup succeeding.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `./scripts/dev.sh test`
Expected: PASS, including the pre-existing `workspaceStateFromSnapshot` test.

- [ ] **Step 5: Commit**

```bash
git add internal/ipc/protocol.go internal/daemon/session.go internal/daemon/daemon.go internal/daemon/daemon_test.go
git commit -m "feat(daemon): overlay pane flag with snapshot exclusion

CreatePanePayload.Overlay marks ephemeral overlay panes: muted at
creation, broadcast with overlay=true so the TUI keeps them out of
the layout tree, excluded from disk snapshots and ghost-buffer
saves (gone on daemon restart by design)."
```

### Task 7: last-pane accounting

**Files:**
- Modify: `internal/daemon/daemon.go` (`handleDestroyPane` line 1148–1159; check `handleDestroyPaneReq` at line 2938 for the same block)
- Test: `internal/daemon/daemon_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestDestroyPane_LastNormalPane_DestroysOverlayAndRecovers(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	normal, _ := d.session.CreatePane(tab.ID, "/tmp")
	overlay, _ := d.session.CreatePane(tab.ID, "/tmp/repo")
	overlay.Overlay = true

	// Simulate the destroy of the last normal pane, then drive the same
	// path handleDestroyPane runs afterwards.
	d.session.DestroyPane(normal.ID)
	d.ensureTabNotEmpty(tab.ID)

	panes := d.session.Panes(tab.ID)
	for _, p := range panes {
		if p.ID == overlay.ID {
			t.Error("overlay pane must be destroyed when the last normal pane goes")
		}
		if p.Overlay {
			t.Error("no overlay panes may remain")
		}
	}
	if len(panes) == 0 {
		t.Error("auto-recovery must have created a replacement pane")
	}
}
```

Write the test against the helper you extract in Step 3 (`ensureTabNotEmpty`) rather than the full `handleDestroyPane` (which needs PTY spawning); if `newTestDaemon` already supports a fake spawn (see `newTestDaemonInDir` / `fakeSession` in `lazy_restore_test.go`), drive `handleDestroyPane` end-to-end instead — prefer whichever the existing destroy tests do (`grep -n "handleDestroyPane" internal/daemon/*_test.go`).

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test`
Expected: FAIL — helper undefined / overlay survives.

- [ ] **Step 3: Implement.** Replace the auto-recovery block in `handleDestroyPane` (lines 1148–1159):

```go
	// Auto-create replacement if the last NORMAL pane in the tab was
	// destroyed. Overlay panes don't count — a tab holding only a hidden
	// lazygit overlay would otherwise render an empty layout. Remaining
	// overlays are destroyed with the tab's last normal pane.
	if tabID != "" {
		d.ensureTabNotEmpty(tabID)
	}
```

New method (place after `handleDestroyPane`):

```go
// ensureTabNotEmpty destroys orphaned overlay panes and spawns a fresh
// terminal pane when a tab has no normal panes left. Shared by the TUI
// destroy path (handleDestroyPane) and the MCP path (handleDestroyPaneReq).
func (d *Daemon) ensureTabNotEmpty(tabID string) {
	var overlays []*Pane
	normal := 0
	for _, p := range d.session.Panes(tabID) {
		if p.Overlay {
			overlays = append(overlays, p)
		} else {
			normal++
		}
	}
	if normal > 0 {
		return
	}
	for _, op := range overlays {
		log.Printf("pane destroy: orphaned overlay %s (tab=%s)", op.ID, tabID)
		d.cleanupPaneArtifacts(op.ID)
		d.session.DestroyPane(op.ID)
	}
	if newPane, err := d.session.CreatePane(tabID, d.defaultCWD()); err == nil {
		newPane.Type = "terminal"
		ptySession := apty.New()
		if err := d.spawnPane(newPane, ptySession, false); err != nil {
			log.Printf("failed to start replacement shell: %v", err)
		}
	}
}
```

Then check `handleDestroyPaneReq` (line 2938): if it contains its own copy of the old auto-recovery block, replace it with `d.ensureTabNotEmpty(tabID)` too. Also check `DestroyTab` paths — tab destruction already destroys all panes including overlays (they're normal panes in `tab.Panes`), no change needed there.

- [ ] **Step 4: Run tests to verify they pass**

Run: `./scripts/dev.sh test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/daemon.go internal/daemon/daemon_test.go
git commit -m "feat(daemon): overlay panes excluded from last-pane accounting

Closing the last normal pane destroys orphaned overlays, then the
existing auto-recovery creates the replacement shell. Extracted to
ensureTabNotEmpty, shared by the TUI and MCP destroy paths."
```

### Task 8: `toggle_lazygit` keybinding config

**Files:**
- Modify: `internal/config/config.go` (KeybindingsConfig ~line 136; Default() ~line 212)
- Test: `internal/config/config_test.go`

- [ ] **Step 1: Write the failing test** (mirror however existing keybinding defaults are asserted in `config_test.go`; if there is no per-binding test, add)

```go
func TestDefault_ToggleLazygitBinding(t *testing.T) {
	cfg := Default()
	if cfg.Keybindings.ToggleLazygit != "alt+g" {
		t.Errorf("ToggleLazygit = %q, want alt+g", cfg.Keybindings.ToggleLazygit)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test`
Expected: FAIL — field undefined.

- [ ] **Step 3: Implement.** In `KeybindingsConfig` after `ToggleEager`:

```go
	// ToggleLazygit opens/hides the per-tab lazygit overlay for the git
	// repo resolved from the active pane's CWD.
	ToggleLazygit string `toml:"toggle_lazygit"`
```

In `Default()` after `ToggleEager: "alt+shift+e",`:

```go
			ToggleLazygit: "alt+g",
```

- [ ] **Step 4: Run test to verify it passes**

Run: `./scripts/dev.sh test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): add toggle_lazygit keybinding (default alt+g)"
```

### Task 9: TUI overlay plumbing — PaneInfo flag, TabModel slot, render/resize/input choke points

**Files:**
- Modify: `internal/tui/model.go` (PaneInfo struct line 56; workspace-state decode ~line 2824; `handlePaneOutput` line 1906; new `flashText` fields + helper)
- Modify: `internal/tui/tab.go` (TabModel struct ~line 12; `ActivePaneModel`; `View` ~line 300; `Resize` ~line 274)
- Test: `internal/tui/tab_test.go` (or the file where TabModel tests live — `grep -rn "func TestTabModel" internal/tui`)

- [ ] **Step 1: Write the failing tests**

```go
func TestTabModel_OverlayVisible_ActivePaneModelReturnsOverlay(t *testing.T) {
	t.Parallel()
	normal := NewPaneModel("pane-n", 1024)
	overlay := NewPaneModel("pane-o", 1024)
	tab := NewTabModel("tab-1", "t")
	tab.Root = NewLeaf(normal)
	tab.ActivePane = normal.ID
	tab.overlayPane = overlay

	if got := tab.ActivePaneModel(); got != normal {
		t.Fatalf("hidden overlay: ActivePaneModel = %v, want normal pane", got)
	}
	tab.overlayVisible = true
	if got := tab.ActivePaneModel(); got != overlay {
		t.Fatalf("visible overlay: ActivePaneModel = %v, want overlay pane", got)
	}
}

func TestTabModel_OverlayVisible_ViewRendersOverlay(t *testing.T) {
	t.Parallel()
	normal := NewPaneModel("pane-n", 1024)
	overlay := NewPaneModel("pane-o", 1024)
	tab := NewTabModel("tab-1", "t")
	tab.Root = NewLeaf(normal)
	tab.ActivePane = normal.ID
	tab.overlayPane = overlay
	tab.overlayVisible = true
	tab.Resize(80, 24)

	// The overlay pane must receive the full tab dimensions, exactly like
	// the focus-mode branch sizes the active pane.
	if overlay.Width != 80 || overlay.Height != 24 {
		t.Errorf("overlay sized %dx%d, want 80x24", overlay.Width, overlay.Height)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `./scripts/dev.sh test`
Expected: FAIL — `tab.overlayPane undefined`.

- [ ] **Step 3: Implement.**

`internal/tui/tab.go` — TabModel fields after `focusMode`:

```go
	// overlayPane is the tab's lazygit overlay (never part of the layout
	// tree). overlayVisible controls rendering; the pane's PTY keeps
	// running while hidden so re-show is instant with UI state intact.
	overlayPane    *PaneModel
	overlayVisible bool
```

`ActivePaneModel` — add at the top (find it: `grep -n "func (t \*TabModel) ActivePaneModel" internal/tui/tab.go`):

```go
	if t.overlayVisible && t.overlayPane != nil {
		return t.overlayPane
	}
```

This single override routes keyboard input (the default `keyToBytes` → `forwardInputBytes` path at model.go:1802-1815), scroll reset, and most active-pane operations to the overlay while visible.

`View()` (line ~300) — add BEFORE the focusMode branch:

```go
	if t.overlayVisible && t.overlayPane != nil {
		return t.overlayPane.View()
	}
```

`Resize()` (line ~274) — add a branch mirroring the focusMode branch, but for the overlay; read the full focus branch (tab.go:276-298) and replicate it exactly with `t.overlayPane` in place of `pane`, guarded by:

```go
	if t.overlayVisible && t.overlayPane != nil {
		// ... focus-branch body with t.overlayPane ...
		// then ALSO run the normal layout resize below (the hidden layout
		// must stay current for when the overlay hides).
	}
```

Important: unlike focus mode, do NOT return early without resizing the layout tree — fall through so hidden panes keep correct sizes (read how the focus branch terminates; if it returns early, restructure the overlay branch to size the overlay AND continue to the tree-resize code).

`internal/tui/model.go`:

PaneInfo (line 56) — add `Overlay bool`. Decode (~line 2827, after `eager`):

```go
				if overlay, ok := pm["overlay"].(bool); ok {
					pi.Overlay = overlay
				}
```

`handlePaneOutput` (line 1906) — overlay panes are not leaves, so output for them is currently dropped. Add before the tab loop:

```go
	for _, tab := range m.tabs {
		if tab.overlayPane != nil && tab.overlayPane.ID == msg.PaneID {
			tab.overlayPane.preparing = false
			tab.overlayPane.AppendOutput(msg.Data)
			return nil
		}
	}
```

Flash message facility (used by Task 10's state machine; there is no existing status-message mechanism — verified). Model fields near the other UI state:

```go
	flashText  string    // transient status-bar message
	flashUntil time.Time // flash expiry; rendered only while now < flashUntil
```

Helper:

```go
// setFlash shows a transient message in the status bar for ~3 seconds.
// Expiry needs no timer: the 1 s sizePollTick already repaints, and the
// status bar renderer checks flashUntil on every frame.
func (m *Model) setFlash(text string) {
	m.flashText = text
	m.flashUntil = time.Now().Add(3 * time.Second)
}
```

In the status-bar render function (find it: `grep -n "\[dev\]" internal/tui/*.go` — the function that renders the `[dev]` / `mem` segments), append a segment:

```go
	if m.flashText != "" && time.Now().Before(m.flashUntil) {
		segments = append(segments, m.flashText)
	}
```

(adapt to the actual segment-building code shape).

- [ ] **Step 4: Run tests to verify they pass**

Run: `./scripts/dev.sh test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/model.go internal/tui/tab.go internal/tui/tab_test.go
git commit -m "feat(tui): overlay pane slot on TabModel

overlayPane lives outside the layout tree; ActivePaneModel returns
it while visible (single choke point routes input/scroll), View and
Resize mirror the focus-mode full-area branch, handlePaneOutput
feeds overlay output, status bar gains a transient flash segment."
```

### Task 10: workspace-state reconciliation for overlays

**Files:**
- Modify: `internal/tui/model.go` (`applyWorkspaceState` line 1972)
- Test: `internal/tui/model_test.go` (or wherever `applyWorkspaceState` tests live — `grep -rn "applyWorkspaceState" internal/tui/*_test.go`)

- [ ] **Step 1: Write the failing tests**

```go
func TestApplyWorkspaceState_OverlayPane_NotInLayoutTree(t *testing.T) {
	m := &Model{}
	state := WorkspaceStateMsg{
		ActiveTab: "tab-1",
		Tabs: []TabInfo{{
			ID: "tab-1", Name: "t",
			Panes: []string{"pane-n", "pane-o"},
		}},
		Panes: []PaneInfo{
			{ID: "pane-n", TabID: "tab-1", Type: "terminal"},
			{ID: "pane-o", TabID: "tab-1", Type: "lazygit", CWD: "/repo", Overlay: true},
		},
	}
	m.applyWorkspaceState(state)

	if len(m.tabs) != 1 {
		t.Fatalf("tabs = %d, want 1", len(m.tabs))
	}
	tab := m.tabs[0]
	if tab.Root == nil || len(tab.Leaves()) != 1 || tab.Leaves()[0].ID != "pane-n" {
		t.Errorf("layout tree must hold only the normal pane, got %v", tab.Leaves())
	}
	if tab.overlayPane == nil || tab.overlayPane.ID != "pane-o" {
		t.Fatalf("overlayPane = %v, want pane-o", tab.overlayPane)
	}
	if tab.overlayVisible {
		t.Error("overlay must default to hidden on reattach")
	}
}

func TestApplyWorkspaceState_OverlayGone_ClearsSlot(t *testing.T) {
	m := &Model{}
	withOverlay := WorkspaceStateMsg{
		ActiveTab: "tab-1",
		Tabs:      []TabInfo{{ID: "tab-1", Name: "t", Panes: []string{"pane-n", "pane-o"}}},
		Panes: []PaneInfo{
			{ID: "pane-n", TabID: "tab-1", Type: "terminal"},
			{ID: "pane-o", TabID: "tab-1", Type: "lazygit", Overlay: true},
		},
	}
	m.applyWorkspaceState(withOverlay)
	m.tabs[0].overlayVisible = true

	// Overlay exits (user pressed q in lazygit) — daemon broadcasts without it.
	without := WorkspaceStateMsg{
		ActiveTab: "tab-1",
		Tabs:      []TabInfo{{ID: "tab-1", Name: "t", Panes: []string{"pane-n"}}},
		Panes:     []PaneInfo{{ID: "pane-n", TabID: "tab-1", Type: "terminal"}},
	}
	m.applyWorkspaceState(without)

	tab := m.tabs[0]
	if tab.overlayPane != nil || tab.overlayVisible {
		t.Errorf("overlay slot must be cleared, got pane=%v visible=%v", tab.overlayPane, tab.overlayVisible)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `./scripts/dev.sh test`
Expected: FAIL — overlay pane lands in the layout tree (2 leaves) / slot not cleared.

- [ ] **Step 3: Implement.** Three changes inside `applyWorkspaceState`:

(a) Index overlay panes for reuse — in the "Index existing tabs and panes" loop (line 1978-1985), after the leaves loop:

```go
		if tab.overlayPane != nil {
			existingPanes[tab.overlayPane.ID] = tab.overlayPane
		}
```

(b) Keep overlays out of the tree — in the per-tab reconciliation, `daemonPaneSet` build (line 2014-2017) and the add-loop (line 2038) both must skip overlay panes:

```go
		daemonPaneSet := make(map[string]bool, len(tabInfo.Panes))
		for _, pid := range tabInfo.Panes {
			if info, ok := paneMap[pid]; ok && info.Overlay {
				continue
			}
			daemonPaneSet[pid] = true
		}
```

and at the top of the add-loop body (line 2038, before the `treePaneIDs[paneID]` check):

```go
			if info, ok := paneMap[paneID]; ok && info.Overlay {
				continue
			}
```

Note: the `restoreTabLayout` early path (line 2000-2008) deserializes the daemon's layout tree — overlay panes were never inserted into any layout by the TUI, so the stored tree cannot reference them; the post-restore prune handles any pathological case. No change there.

(c) Reconcile the overlay slot — after the add-loop (after the `PrunePlaceholders` block, line 2094-2097), add:

```go
		// Reconcile the overlay slot (never part of the layout tree).
		var overlayInfo *PaneInfo
		for _, pid := range tabInfo.Panes {
			if info, ok := paneMap[pid]; ok && info.Overlay {
				overlayInfo = info
				break
			}
		}
		switch {
		case overlayInfo == nil:
			// Daemon has no overlay for this tab (exited or destroyed).
			if tab.overlayPane != nil {
				tab.overlayPane.Dispose()
				tab.overlayPane = nil
				tab.overlayVisible = false
			}
		case tab.overlayPane == nil || tab.overlayPane.ID != overlayInfo.ID:
			if tab.overlayPane != nil {
				tab.overlayPane.Dispose()
			}
			pane, ok := existingPanes[overlayInfo.ID]
			if !ok {
				pane = NewPaneModel(overlayInfo.ID, m.replayBufSize())
				newPaneIDs = append(newPaneIDs, overlayInfo.ID)
			}
			syncPaneMeta(pane, overlayInfo)
			tab.overlayPane = pane
			// A create initiated by this TUI's Alt+G shows on arrival;
			// reattach to a pre-existing overlay defaults to hidden.
			if m.pendingOverlayShow[tab.ID] {
				delete(m.pendingOverlayShow, tab.ID)
				tab.overlayVisible = true
			}
		default:
			syncPaneMeta(tab.overlayPane, overlayInfo)
		}
```

Model field (near `pendingSplit`):

```go
	pendingOverlayShow map[string]bool // tabID → show overlay when its pane arrives
```

(`delete` and reads on a nil map are safe; only the writer in Task 10's `createOverlay` initializes it.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `./scripts/dev.sh test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/model.go internal/tui/model_test.go
git commit -m "feat(tui): reconcile overlay panes outside the layout tree

applyWorkspaceState skips Overlay panes in daemonPaneSet and the
add-loop, adopts them into TabModel.overlayPane (hidden by default
on reattach, shown when this TUI initiated the create), and clears
the slot when the daemon no longer reports the pane."
```

### Task 11: Alt+G state machine + overlay key routing

**Files:**
- Modify: `internal/tui/model.go` (key dispatch — the function containing the `kbMatches(key, kb.MutePane)` case at line 1552; new methods)
- Create: `internal/tui/overlay.go` (state machine + helpers — keeps model.go from growing)
- Test: `internal/tui/overlay_test.go`

- [ ] **Step 1: Write the failing tests** (uses `fakeSender` from `dialog_test.go` — it records sent messages in `fake.sent`)

```go
package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/plugin"
)

func overlayTestModel(t *testing.T, paneCWD string) (*Model, *fakeSender, *TabModel) {
	t.Helper()
	pane := NewPaneModel("pane-n", 1024)
	pane.CWD = paneCWD
	tab := NewTabModel("tab-1", "t")
	tab.Root = NewLeaf(pane)
	tab.ActivePane = pane.ID

	reg := registryWithLazygit(t) // Task 5's helper — move it into a shared
	// test file (e.g. overlay_test.go) if setup_dialog_test.go ordering
	// makes it awkward; both files are package tui so either placement works.

	fake := &fakeSender{}
	m := &Model{tabs: []*TabModel{tab}, activeTab: 0, client: fake, pluginRegistry: reg}
	return m, fake, tab
}

func gitRepoDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestToggleLazygit_VisibleOverlay_Hides(t *testing.T) {
	m, _, tab := overlayTestModel(t, "")
	tab.overlayPane = NewPaneModel("pane-o", 1024)
	tab.overlayVisible = true
	m.handleToggleLazygit()
	if tab.overlayVisible {
		t.Error("visible overlay must hide on toggle")
	}
}

func TestToggleLazygit_NoRepoNoOverlay_Flashes(t *testing.T) {
	m, fake, _ := overlayTestModel(t, t.TempDir())
	m.handleToggleLazygit()
	if m.flashText == "" {
		t.Error("expected flash message for no-repo case")
	}
	if len(fake.sent) != 0 {
		t.Errorf("no IPC expected, got %d msgs", len(fake.sent))
	}
}

func TestToggleLazygit_NoRepo_ExistingOverlay_ShowsAnyway(t *testing.T) {
	m, _, tab := overlayTestModel(t, t.TempDir())
	tab.overlayPane = NewPaneModel("pane-o", 1024)
	m.handleToggleLazygit()
	if !tab.overlayVisible {
		t.Error("Alt+G must never be a dead key when an overlay exists")
	}
}

func TestToggleLazygit_MatchingRepo_ShowsExisting(t *testing.T) {
	repo := gitRepoDir(t)
	m, fake, tab := overlayTestModel(t, repo)
	op := NewPaneModel("pane-o", 1024)
	op.CWD = repo
	tab.overlayPane = op
	m.handleToggleLazygit()
	if !tab.overlayVisible {
		t.Error("matching repo must show the existing overlay")
	}
	// Show sends a resize (PTY may be stale-sized) but no create/destroy.
	for _, msg := range fake.sent {
		if msg.Type == ipc.MsgCreatePane || msg.Type == ipc.MsgDestroyPane {
			t.Errorf("unexpected %s for matching repo", msg.Type)
		}
	}
}

func TestToggleLazygit_SingleRepo_NoOverlay_SendsOverlayCreate(t *testing.T) {
	repo := gitRepoDir(t)
	m, fake, tab := overlayTestModel(t, repo)
	m.handleToggleLazygit()

	// fakeSender.sent is []*ipc.Message (dialog_test.go:371).
	var create *ipc.Message
	for _, sent := range fake.sent {
		if sent.Type == ipc.MsgCreatePane {
			create = sent
		}
	}
	if create == nil {
		t.Fatal("expected MsgCreatePane")
	}
	var payload ipc.CreatePanePayload
	if err := create.DecodePayload(&payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Overlay || payload.Type != "lazygit" || payload.CWD != repo {
		t.Errorf("payload = %+v", payload)
	}
	if len(payload.InstanceArgs) != 2 || payload.InstanceArgs[0] != "--path" || payload.InstanceArgs[1] != repo {
		t.Errorf("InstanceArgs = %v, want [--path %s]", payload.InstanceArgs, repo)
	}
	if !m.pendingOverlayShow[tab.ID] {
		t.Error("pendingOverlayShow must be set so the overlay shows on arrival")
	}
}

func TestToggleLazygit_DifferentRepo_ReplacesOverlay(t *testing.T) {
	repo := gitRepoDir(t)
	m, fake, tab := overlayTestModel(t, repo)
	op := NewPaneModel("pane-o", 1024)
	op.CWD = "/some/other/repo"
	tab.overlayPane = op
	m.handleToggleLazygit()

	var sawDestroy, sawCreate bool
	for i := range fake.sent {
		switch fake.sent[i].Type {
		case ipc.MsgDestroyPane:
			sawDestroy = true
		case ipc.MsgCreatePane:
			sawCreate = true
		}
	}
	if !sawDestroy || !sawCreate {
		t.Errorf("destroy=%v create=%v, want both (silent replace)", sawDestroy, sawCreate)
	}
}

func TestToggleLazygit_LazygitUnavailable_Flashes(t *testing.T) {
	repo := gitRepoDir(t)
	m, fake, _ := overlayTestModel(t, repo)
	m.pluginRegistry.Get("lazygit").Available = false
	m.handleToggleLazygit()
	if m.flashText == "" {
		t.Error("expected flash for missing binary")
	}
	if len(fake.sent) != 0 {
		t.Errorf("no IPC expected, got %d", len(fake.sent))
	}
}
```

Note: `handleToggleLazygit` below is a **pointer-receiver** method that mutates `m` and returns only a `tea.Cmd` — this keeps the tests free of `out.(Model)` re-assignment dances. The value-receiver dispatch site adapts (see Step 3). For the IPC-sending tests, the returned `tea.Cmd` closures must be EXECUTED for messages to reach `fakeSender` — call the returned cmd(s) in the test: if the cmd is a `tea.Batch`, iterate; simplest is a test helper:

```go
func runCmd(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	if msg := cmd(); msg != nil {
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				runCmd(c)
			}
		}
	}
}
```

Wrap every `m.handleToggleLazygit()` call in the tests as `runCmd(m.handleToggleLazygit())`. (Check how `dialog_test.go:193-199` executes cmds against fakeSender and copy that pattern if it differs — the shutdown test sends synchronously, but overlay sends are async cmds.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `./scripts/dev.sh test`
Expected: FAIL — `handleToggleLazygit` undefined.

- [ ] **Step 3: Implement `internal/tui/overlay.go`:**

```go
package tui

import (
	"log"

	tea "charm.land/bubbletea/v2"
	"github.com/artyomsv/quil/internal/gitdiscover"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/logger"
)

// handleToggleLazygit implements the Alt+G state machine (spec §4):
//  1. visible → hide
//  2. resolve candidates from the active normal pane's CWD
//  3. none: show existing overlay if any, else flash
//  4. candidate matches existing overlay → show (no binary check needed —
//     the process is already running)
//  5. multiple, none matching → picker dialog
//  6. create / replace (binary availability gates THIS step only)
func (m *Model) handleToggleLazygit() tea.Cmd {
	tab := m.activeTabModel()
	if tab == nil {
		return nil
	}

	// 1. Visible → hide. Never replaces from the overlay itself.
	if tab.overlayVisible {
		tab.overlayVisible = false
		return tea.ClearScreen
	}

	// 2. Resolve candidates from the active NORMAL pane (overlay is hidden,
	// so ActivePaneModel returns the layout pane).
	var base string
	if pane := tab.ActivePaneModel(); pane != nil {
		base = pane.CWD
	}
	candidates := gitdiscover.Candidates(base)

	// 3. No candidates: a dead key is worse than a stale overlay.
	if len(candidates) == 0 {
		if tab.overlayPane != nil {
			return m.showOverlay(tab)
		}
		m.setFlash("no git repo here")
		return nil
	}

	// 4. A candidate matches the existing overlay → instant re-show.
	if tab.overlayPane != nil {
		for _, c := range candidates {
			if c == tab.overlayPane.CWD {
				return m.showOverlay(tab)
			}
		}
	}

	// 5. Multiple candidates, none matching → let the user pick.
	if len(candidates) > 1 {
		m.repoPickCandidates = candidates
		m.dialog = dialogGitRepoPick
		m.dialogCursor = 0
		return tea.ClearScreen
	}

	// 6. Create (or replace) for the single candidate.
	return m.createOverlay(tab, candidates[0])
}

// showOverlay makes the existing overlay visible and re-syncs its PTY size
// (nothing resizes a hidden overlay, so the terminal may have changed).
func (m *Model) showOverlay(tab *TabModel) tea.Cmd {
	tab.overlayVisible = true
	tab.Resize(tab.Width, tab.Height) // size the overlay pane model
	return tea.Batch(tea.ClearScreen, m.overlayResizeCmd(tab))
}

// createOverlay destroys a mismatched overlay (silent replace — lazygit
// holds no unsaved state worth a confirm) and requests a new overlay pane
// for repo. The pane shows when its workspace_state arrives
// (pendingOverlayShow).
func (m *Model) createOverlay(tab *TabModel, repo string) tea.Cmd {
	p := m.pluginRegistry.Get("lazygit")
	if p == nil || !p.Available {
		m.setFlash("lazygit not installed")
		return nil
	}

	var cmds []tea.Cmd
	client := m.client

	if tab.overlayPane != nil {
		oldID := tab.overlayPane.ID
		tab.overlayPane.Dispose()
		tab.overlayPane = nil
		tab.overlayVisible = false
		cmds = append(cmds, func() tea.Msg {
			msg, err := ipc.NewMessage(ipc.MsgDestroyPane, ipc.DestroyPanePayload{PaneID: oldID})
			if err == nil {
				if err := client.Send(msg); err != nil {
					log.Printf("overlay: destroy old pane: %v", err)
				}
			}
			return nil
		})
	}

	if m.pendingOverlayShow == nil {
		m.pendingOverlayShow = make(map[string]bool)
	}
	m.pendingOverlayShow[tab.ID] = true

	tabID := tab.ID
	logger.Debug("overlay: creating lazygit pane for repo %q (tab=%s)", repo, tabID)
	cmds = append(cmds, func() tea.Msg {
		msg, err := ipc.NewMessage(ipc.MsgCreatePane, ipc.CreatePanePayload{
			TabID:        tabID,
			CWD:          repo,
			Type:         "lazygit",
			InstanceArgs: []string{"--path", repo},
			Overlay:      true,
		})
		if err == nil {
			if err := client.Send(msg); err != nil {
				log.Printf("overlay: create pane: %v", err)
			}
		}
		return nil
	})
	return tea.Batch(cmds...)
}

// overlayResizeCmd sends the daemon a resize for the overlay PTY at full
// tab dimensions (the same -2 border math the focus-mode branch uses).
func (m *Model) overlayResizeCmd(tab *TabModel) tea.Cmd {
	op := tab.overlayPane
	if op == nil {
		return nil
	}
	cols := tab.Width - 2
	rows := tab.Height - 2
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	client := m.client
	id := op.ID
	return func() tea.Msg {
		msg, err := ipc.NewMessage(ipc.MsgResizePane, ipc.ResizePanePayload{
			PaneID: id,
			Cols:   uint16(cols),
			Rows:   uint16(rows),
		})
		if err == nil {
			if err := client.Send(msg); err != nil {
				log.Printf("overlay: resize: %v", err)
			}
		}
		return nil
	}
}

// handleOverlayKey routes keys while the overlay is visible. A small
// allow-list of global shortcuts works; everything else goes to the
// overlay PTY (Esc must reach lazygit — Alt+G is the way out, and q
// quits lazygit which destroys the pane via process exit).
func (m *Model) handleOverlayKey(msg tea.KeyPressMsg, tab *TabModel) tea.Cmd {
	key := msg.String()
	kb := m.cfg.Keybindings
	switch {
	case kbMatches(key, kb.ToggleLazygit):
		tab.overlayVisible = false
		return tea.ClearScreen
	case kbMatches(key, kb.Quit):
		return tea.Quit
	case kbMatches(key, kb.Redraw):
		return tea.Sequence(tea.ClearScreen, tea.RequestWindowSize())
	case key == "alt+1" || key == "alt+2" || key == "alt+3" ||
		key == "alt+4" || key == "alt+5" || key == "alt+6" ||
		key == "alt+7" || key == "alt+8" || key == "alt+9":
		idx := int(key[len(key)-1] - '1')
		return m.switchTab(idx)
	}
	data := keyToBytes(msg)
	if data == nil {
		return nil
	}
	return m.forwardInputBytes(data)
}
```

Adapt the Redraw case to call whatever the existing `kb.Redraw` branch in the main key switch does (find it: `grep -n "kb.Redraw" internal/tui/model.go`) — reuse the same expression. Same for `m.switchTab` (verified to exist at model.go:1799) and `m.forwardInputBytes` (model.go:1815).

**Dispatch wiring** in `internal/tui/model.go`: two additions to the main key-handling function (the one containing the `kbMatches(key, kb.MutePane)` case at line 1552):

(a) Overlay-visible delegation — after the notes-mode and rename and dialog handling, BEFORE the sidebar-focused branch (~line 1559), add:

```go
	if tab := m.activeTabModel(); tab != nil && tab.overlayVisible && m.dialog == dialogNone && !m.renaming {
		return m, m.handleOverlayKey(msg, tab)
	}
```

CAUTION — receiver shape: if the enclosing function has a VALUE receiver `(m Model)`, pointer-method calls on `m` work (addressable local) and mutations to `*TabModel` stick (pointer), but mutations to Model scalar fields (`flashText`) survive only if the function returns `m`. The dispatch above returns `m`, so this is safe. Mirror what the `kbMatches(key, kb.NotesToggle)` delegation does.

(b) The Alt+G binding — next to the MutePane/ToggleEager cases (line 1552):

```go
	case kbMatches(key, kb.ToggleLazygit):
		return m, m.handleToggleLazygit()
```

(c) Mouse guard — overlay coordinates don't map to layout-tree rects. In the `tea.MouseClickMsg` / `tea.MouseMotionMsg` / `tea.MouseReleaseMsg` / `tea.MouseWheelMsg` handlers, add an early return when the active tab's overlay is visible (v1: overlay is keyboard-only; mouse support needs coordinate translation and is out of scope):

```go
	if tab := m.activeTabModel(); tab != nil && tab.overlayVisible {
		return m, nil
	}
```

(d) WindowSizeMsg — after the existing resize handling, when the active tab's overlay is visible, also send the overlay PTY resize:

```go
	if tab := m.activeTabModel(); tab != nil && tab.overlayVisible {
		cmds = append(cmds, m.overlayResizeCmd(tab))
	}
```

(adapt to the handler's actual cmd-accumulation shape).

Model field for the picker (next to `repoCandidates`):

```go
	repoPickCandidates []string // candidates shown by dialogGitRepoPick (Alt+G with multiple repos)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `./scripts/dev.sh test`
Expected: PASS (the `dialogGitRepoPick` constant is added in Task 12 — if the compiler complains here, add the iota constant now as part of this task and note it in the commit).

- [ ] **Step 5: Commit**

```bash
git add internal/tui/overlay.go internal/tui/overlay_test.go internal/tui/model.go
git commit -m "feat(tui): Alt+G lazygit overlay state machine

Visible->hide; candidates from active pane CWD; match->show
(no binary check, process already runs); multiple->picker;
create/replace gated on plugin availability. Overlay-visible key
routing forwards everything to the PTY except toggle/quit/redraw/
tab-switch; mouse swallowed (keyboard-only v1)."
```

### Task 12: `dialogGitRepoPick` picker

**Files:**
- Modify: `internal/tui/model.go` (dialogScreen iota, line 166-181)
- Modify: `internal/tui/dialog.go` (key dispatch switch + render switch; find them: `grep -n "case dialogConfirm" internal/tui/*.go` shows both)
- Test: `internal/tui/overlay_test.go`

- [ ] **Step 1: Write the failing tests**

```go
func TestGitRepoPick_EnterCreatesOverlayForChosenRepo(t *testing.T) {
	repoA := gitRepoDir(t)
	repoB := gitRepoDir(t)
	m, fake, _ := overlayTestModel(t, "")
	m.dialog = dialogGitRepoPick
	m.repoPickCandidates = []string{repoA, repoB}
	m.dialogCursor = 1

	out, cmd := m.handleGitRepoPickKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := out.(Model)
	runCmd(cmd)

	if got.dialog != dialogNone {
		t.Errorf("dialog = %v, want dialogNone", got.dialog)
	}
	var payload ipc.CreatePanePayload
	for i := range fake.sent {
		if fake.sent[i].Type == ipc.MsgCreatePane {
			if err := fake.sent[i].DecodePayload(&payload); err != nil {
				t.Fatal(err)
			}
		}
	}
	if payload.CWD != repoB {
		t.Errorf("created for %q, want cursor choice %q", payload.CWD, repoB)
	}
}

func TestGitRepoPick_EscCloses(t *testing.T) {
	m, fake, _ := overlayTestModel(t, "")
	m.dialog = dialogGitRepoPick
	m.repoPickCandidates = []string{"/a", "/b"}

	out, _ := m.handleGitRepoPickKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	got := out.(Model)
	if got.dialog != dialogNone || got.repoPickCandidates != nil {
		t.Errorf("dialog=%v candidates=%v, want closed+cleared", got.dialog, got.repoPickCandidates)
	}
	if len(fake.sent) != 0 {
		t.Error("Esc must not create anything")
	}
}
```

(Use the same `tea.KeyPressMsg` construction style as existing dialog tests — check `dialog_test.go:193`.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `./scripts/dev.sh test`
Expected: FAIL — `dialogGitRepoPick` / `handleGitRepoPickKey` undefined.

- [ ] **Step 3: Implement.**

`model.go` iota — append after `dialogMemory`:

```go
	dialogGitRepoPick
```

`dialog.go` — handler (place near `handleConfirmKey`):

```go
// handleGitRepoPickKey drives the Alt+G multi-repo picker: a plain list of
// git repos found near the active pane's CWD.
func (m Model) handleGitRepoPickKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "esc":
		m.dialog = dialogNone
		m.repoPickCandidates = nil
		return m, tea.ClearScreen
	case "up", "k":
		if m.dialogCursor > 0 {
			m.dialogCursor--
		}
		return m, nil
	case "down", "j":
		if m.dialogCursor < len(m.repoPickCandidates)-1 {
			m.dialogCursor++
		}
		return m, nil
	case "enter":
		if m.dialogCursor >= len(m.repoPickCandidates) {
			return m, nil
		}
		repo := m.repoPickCandidates[m.dialogCursor]
		m.dialog = dialogNone
		m.repoPickCandidates = nil
		tab := m.activeTabModel()
		if tab == nil {
			return m, tea.ClearScreen
		}
		return m, tea.Batch(tea.ClearScreen, m.createOverlay(tab, repo))
	}
	return m, nil
}
```

Render (place near the confirm-dialog renderer, mirroring its box style — title, list rows with `> ` cursor marker, `Esc cancel · Enter open` footer):

```go
// renderGitRepoPickDialog renders the Alt+G repo picker.
func (m Model) renderGitRepoPickDialog() string {
	var b strings.Builder
	b.WriteString("Open lazygit for which repo?\n\n")
	for i, repo := range m.repoPickCandidates {
		marker := "  "
		if i == m.dialogCursor {
			marker = "> "
		}
		b.WriteString(marker + repo + "\n")
	}
	b.WriteString("\nEnter open · Esc cancel")
	return b.String()
}
```

Wire both into the dialog dispatch: add `case dialogGitRepoPick: return m.handleGitRepoPickKey(msg)` to the key switch and the equivalent render call to the render switch — copy exactly how `dialogConfirm` plugs into both (same files, same switches), including any box/width styling wrapper the other dialogs pass through (use `dialogWidth` 60 like the rest; long paths may truncate — acceptable v1).

- [ ] **Step 4: Run tests to verify they pass**

Run: `./scripts/dev.sh test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/model.go internal/tui/dialog.go internal/tui/overlay_test.go
git commit -m "feat(tui): repo picker dialog for Alt+G with multiple candidates"
```

### Task 13: shortcuts help, notes-mode exemption, docs

**Files:**
- Modify: `internal/tui/dialog.go` (`shortcutsList`, line 248)
- Modify: `internal/tui/notes.go` (`notesKeyExempt` — find it: `grep -n "notesKeyExempt" internal/tui/*.go`)
- Modify: `docs/keybindings.md`, `docs/configuration.md`, `docs/features.md`, `docs/plugin-reference.md`
- Modify: `.claude/CLAUDE.md`

- [ ] **Step 1: Shortcuts dialog.** In `shortcutsList` (dialog.go:248), add a row following the existing pattern (`kbDisplay` for the key column):

```go
		{kbDisplay(kb.ToggleLazygit), "Toggle lazygit overlay for current repo"},
```

(place it near the MutePane/ToggleEager rows; match the exact struct-literal shape used there).

- [ ] **Step 2: Notes-mode exemption.** Do NOT add `ToggleLazygit` to `notesKeyExempt` — notes mode binds the editor to a pane; popping a full-screen overlay over it mid-edit conflicts with the notes layout. Alt+G in notes mode falls through to the editor (types nothing — it's a modifier chord) which is the safe default. Add a one-line comment in `notesKeyExempt`'s comment block noting the exclusion is deliberate.

- [ ] **Step 3: Docs.**

`docs/keybindings.md` — add `Alt+G` to the keymap table: "Toggle lazygit overlay (git repo from active pane's directory)" + the `toggle_lazygit` config key in the customization section.

`docs/configuration.md` — add `toggle_lazygit = "alt+g"` to the `[keybindings]` reference.

`docs/features.md` — new subsection under the panes/plugins area:

```markdown
### Lazygit integration

- **Lazygit plugin** (Ctrl+N → Tools → Lazygit): opens lazygit as a regular
  pane. The directory step lists git repos found near the active pane's
  directory (the enclosing repo plus one-level subfolders) with a Browse…
  escape hatch. Only offered when the `lazygit` binary is installed.
- **Overlay (Alt+G)**: toggles a full-tab lazygit view for the repo resolved
  from the active pane's current directory. Hidden overlays keep running —
  re-show is instant with lazygit's UI state intact. One overlay per tab.
  Overlays are ephemeral: they don't survive a daemon restart (one keypress
  recreates them). Quit lazygit (q) to destroy the overlay pane.
```

`docs/plugin-reference.md` — document the new `[command]` field:

```markdown
#### `discover`

Optional. `"git"` switches the setup dialog's directory step from the plain
browser to a git-repo candidate list (enclosing repo of the active pane's
CWD + one-level sub-repos), with a final "Browse…" row that falls back to
the browser. Only meaningful together with `prompts_cwd = true`.
Unknown values fail plugin load.
```

`.claude/CLAUDE.md` — add to Key Conventions / Architecture notes (one bullet, terse, matching existing style): lazygit integration — `internal/gitdiscover` (pure repo discovery), `discover = "git"` plugin opt-in (setup-dialog candidate list), per-tab overlay (`Pane.Overlay` excluded from disk snapshots + TUI `TabModel.overlayPane` outside the layout tree, Alt+G state machine in `internal/tui/overlay.go`, `toggle_lazygit` keybinding). Update the dialogScreen iota list to include `dialogGitRepoPick` and the M5 milestone line.

- [ ] **Step 4: Run full suite + vet**

Run: `./scripts/dev.sh test && ./scripts/dev.sh vet`
Expected: PASS / no vet findings.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/dialog.go internal/tui/notes.go docs/ .claude/CLAUDE.md
git commit -m "docs: lazygit integration — keybinding, plugin field, features

Alt+G in shortcuts dialog; discover field in plugin reference;
overlay semantics in features; CLAUDE.md architecture notes."
```

### Task 14: race check + manual end-to-end verification

- [ ] **Step 1: Race detector**

Run: `./scripts/dev.sh test-race`
Expected: PASS, no races (the overlay flag is immutable post-creation; if the detector flags `Pane.Overlay`, move its reads under `PluginMu` like `Muted`).

- [ ] **Step 2: Build all variants**

Run: `./scripts/dev.sh build`
Expected: 6 binaries, no errors.

- [ ] **Step 3: Manual verification in dev mode** (`./scripts/quil-dev.ps1`, confirm `[dev]` in status bar; production `~/.quil/` untouched per `.claude/rules/dev-environment.md`):

1. Terminal pane `cd` into this repo → Alt+G → lazygit overlay appears full-tab.
2. Alt+G again → hides instantly. Alt+G → re-shows with lazygit state intact (e.g. scroll position).
3. Resize the window while hidden → Alt+G → lazygit renders at the new size.
4. `q` inside lazygit → overlay disappears, layout intact.
5. Terminal pane in a folder with 2+ git subfolders → Alt+G → picker → choose → overlay for chosen repo.
6. Terminal in a non-git dir (e.g. `~`) — if home has no repos: flash "no git repo here".
7. Ctrl+N → Tools → Lazygit → candidate list → Browse… fallback → create as normal pane; works in splits.
8. Close the only normal pane in a tab with a hidden overlay → fresh shell appears, no ghost-empty tab.
9. Quit dev TUI, relaunch → overlay gone (ephemeral), normal lazygit panes respawn via rerun.
10. Alt+1..9 while overlay visible → switches tab; switching back shows the overlay still hidden/visible per its per-tab state... (verify the visible state is preserved per tab).

- [ ] **Step 4: Final commit if fixes were needed; otherwise done.** Hand off via superpowers:finishing-a-development-branch.

---

## Spec coverage map

| Spec section | Tasks |
|---|---|
| §1 plugin TOML | 3 |
| §2 gitdiscover | 1 |
| §3 discover="git" dialog | 2, 4, 5 |
| §4 daemon (flag, broadcast, snapshot exclusion, muted) | 6 |
| §4 last-pane accounting | 7 |
| §4 TUI reattach/reconciliation, hidden default | 9, 10 |
| §4 Alt+G state machine precedence 1–7 | 11, 12 |
| §4 rendering/keys/resize | 9, 11 |
| §5 error handling | 1 (I/O degrade), 11 (flash), 6/10 (process exit) |
| §6 testing incl. layout-assumption grep | every task; assumption grep = 9/10 mouse+output+navigation guards |
| §7 phasing | task order |
| Out of scope: MCP overlay | CreatePaneReqPayload untouched (verified — separate struct) |
