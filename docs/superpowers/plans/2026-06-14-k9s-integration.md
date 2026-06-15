# k9s Integration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** k9s integration per spec `docs/superpowers/specs/2026-06-14-k9s-integration-design.md` — built-in k9s plugin (Tier A, TOML-only) and a kube-context-aware pane setup dialog (Tier B).

**Architecture:** Two independent phases. Phase 1 (Tier A) adds a `k9s` default plugin TOML — zero Go logic; binary gating and the normal pane lifecycle come free from the existing plugin system. Phase 2 (Tier B) adds a pure `internal/kubediscover` package (parses kubeconfig contexts), extends the `discover` plugin opt-in to accept `"kube"`, adds a kube-context pick field to the setup dialog that injects `--context <name>`, then flips the k9s plugin to `discover = "kube"`.

**Tech Stack:** Go 1.25, Bubble Tea v2, BurntSushi/toml. Phase 2 adds `gopkg.in/yaml.v3` (kubeconfig is YAML; the repo is TOML/JSON-only today — `yaml` is not currently in `go.sum`). Build/test via Docker: `./scripts/dev.sh test` (full suite — there is no per-package form), `./scripts/dev.sh vet`. Host has no Go toolchain.

**Branch:** create `feat/k9s-integration` off `master`. Commit the spec and this plan first if not already present.

**Conventions that apply to every task:** tabs in Go files, 2-space TOML, errors wrapped with `fmt.Errorf("doing X: %w", err)`, table-driven tests named `TestFunc_Scenario`, `t.TempDir()`/`t.Setenv()` for fixtures, no mocking frameworks (manual fakes). Never run the production `quil`/`quild` or touch `~/.quil/` — manual verification uses `./quil-dev.exe` only (see `.claude/rules/dev-environment.md`). No mention of AI tooling vendors anywhere in commits, docs, site, or comments.

---

## Phase 1 — Tier A: k9s built-in plugin

### Task 1: k9s built-in plugin TOML

**Files:**
- Create: `internal/plugin/defaults/k9s.toml` (auto-embedded by `//go:embed defaults/*.toml` in `internal/plugin/defaults.go` — no Go change needed)
- Test: `internal/plugin/defaults_test.go`

- [ ] **Step 1: Write the failing test** (append to `defaults_test.go`, mirroring `TestEnsureDefaultPlugins_WritesLazygit`)

```go
func TestEnsureDefaultPlugins_WritesK9s(t *testing.T) {
	dir := t.TempDir()
	if _, err := EnsureDefaultPlugins(dir); err != nil {
		t.Fatalf("EnsureDefaultPlugins: %v", err)
	}
	p, err := loadPluginTOML(filepath.Join(dir, "k9s.toml"))
	if err != nil {
		t.Fatalf("load k9s.toml: %v", err)
	}
	if p.Name != "k9s" || p.Command.Cmd != "k9s" {
		t.Errorf("name/cmd = %q/%q", p.Name, p.Command.Cmd)
	}
	// k9s is cluster-scoped, not directory-scoped: no CWD prompt in Tier A,
	// and discover stays empty until Tier B turns it on.
	if p.Command.PromptsCWD {
		t.Errorf("PromptsCWD = true, want false")
	}
	if p.Command.Discover != "" {
		t.Errorf("Discover = %q, want \"\" (Tier A)", p.Command.Discover)
	}
	if p.Persistence.Strategy != "rerun" || p.Persistence.GhostBuffer {
		t.Errorf("strategy=%q ghost=%v, want rerun/false", p.Persistence.Strategy, p.Persistence.GhostBuffer)
	}
	if len(p.Command.Toggles) != 2 {
		t.Fatalf("toggles = %+v, want 2 (readonly, start_pods)", p.Command.Toggles)
	}
	if p.Command.Toggles[0].Name != "readonly" || p.Command.Toggles[1].Name != "start_pods" {
		t.Errorf("toggle names = %q,%q", p.Command.Toggles[0].Name, p.Command.Toggles[1].Name)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test`
Expected: FAIL — `load k9s.toml: ... no such file`.

- [ ] **Step 3: Create `internal/plugin/defaults/k9s.toml`**

```toml
# k9s — Kubernetes cluster TUI
# Edit this file to customize the plugin. Delete it to restore defaults.

[plugin]
name = "k9s"
display_name = "k9s"
category = "tools"
description = "Kubernetes cluster TUI"
schema_version = 1

[command]
cmd = "k9s"
# path = "/path/to/k9s"  # uncomment to override PATH lookup
detect = "k9s version"
# k9s connects to the cluster via KUBECONFIG / ~/.kube/config, not the CWD,
# so no directory prompt. Tier B turns on `discover = "kube"` below.
prompts_cwd = false
# discover = "kube"      # Tier B opt-in — kube-context pick-list

[[command.toggles]]
name = "readonly"
label = "Read-only (disable all cluster-modifying commands)"
args_when_on = ["--readonly"]
default = false

[[command.toggles]]
name = "start_pods"
label = "Start on the Pods view"
args_when_on = ["--command", "pods"]
default = false

[persistence]
# Re-run k9s on daemon restart; it reconnects to the cluster fresh.
strategy = "rerun"
# Full-screen TUI — replaying stale frames on reconnect is useless.
ghost_buffer = false
```

- [ ] **Step 4: Run test to verify it passes**

Run: `./scripts/dev.sh test`
Expected: PASS.

- [ ] **Step 5: Manual verification (dev mode).** `./scripts/dev.sh build`, run `./scripts/quil-dev.ps1`, confirm `[dev]` in the status bar. With k9s installed and a reachable cluster: Ctrl+N → Tools → k9s → toggle dialog shows Read-only + Start on Pods → pane opens k9s connected to the current-context cluster. Toggle Read-only on and confirm k9s refuses mutating commands. Without k9s installed: the k9s entry is greyed out in Ctrl+N (existing `DetectAvailability` behavior — no code needed).

- [ ] **Step 6: Commit**

```bash
git add internal/plugin/defaults/k9s.toml internal/plugin/defaults_test.go
git commit -m "feat(plugin): add k9s built-in plugin

Cluster TUI as a normal pane: detect via 'k9s version', no CWD prompt
(k9s is cluster-scoped, not directory-scoped), rerun persistence,
ghost buffer off. Optional --readonly and start-on-Pods toggles.
Binary gating comes free from DetectAvailability."
```

### Task 2: Tier A docs + site + changelog

**Files:**
- Modify: `docs/plugin-reference.md` (add k9s to the built-in plugins list)
- Modify: `docs/features.md` (mention k9s under the plugins/tools area)
- Modify: `site/src/data/plugins.ts` (add a k9s `PluginEntry`)
- Modify: `CHANGELOG.md` (`[Unreleased]` → Added)

- [ ] **Step 1: Add the k9s plugin entry to the site.** In `site/src/data/plugins.ts`, append a `PluginEntry` following the lazygit entry's shape (slug, name, description, a `spawnExample` showing the TOML, and a short feature list). Use only honest facts (cluster TUI, `--readonly`, `--context` in Tier B, binary-gated). Do **not** invent metrics or screenshots.

- [ ] **Step 2: Build the site to verify types.**

Run (in `site/`): `npm run build`
Expected: `0 errors, 0 warnings, 0 hints` and a clean build. If a new icon name is referenced, add it to the `IconName` union in `site/src/data/features.ts` AND the `Icon.astro` `name` union + a switch branch (the heart-pulse precedent).

- [ ] **Step 3: Update `docs/plugin-reference.md` and `docs/features.md`.** Add k9s alongside lazygit in the built-in plugin catalog. Note: normal pane (not an overlay), cluster-scoped, binary-gated, `--readonly` toggle.

- [ ] **Step 4: CHANGELOG.** Under `## [Unreleased]` → `### Added`:

```markdown
- **k9s plugin** — Kubernetes cluster TUI as a built-in pane type. Binary-gated
  (greyed out when `k9s` is not on PATH), opens as a normal pane with optional
  read-only and start-on-Pods toggles. Cross-platform (Windows, macOS, Linux).
```

- [ ] **Step 5: Commit**

```bash
git add docs/plugin-reference.md docs/features.md site/src/data/plugins.ts CHANGELOG.md
git commit -m "docs(plugin): document k9s built-in plugin"
```

> **Tier A is shippable here.** Phase 2 is an independent enhancement and has no effect until k9s is installed and a kubeconfig exists.

---

## Phase 2 — Tier B: kube-context-aware setup dialog

### Task 3: `internal/kubediscover` package + yaml.v3 dependency

**Files:**
- Modify: `go.mod` (+ `gopkg.in/yaml.v3 v3.0.1` require), `go.sum` (via `go mod tidy`)
- Create: `internal/kubediscover/kubediscover.go`
- Create: `internal/kubediscover/kubediscover_test.go`

> **Dependency note:** this is the one new third-party dependency in the whole feature. `gopkg.in/yaml.v3` is a single module with no transitive dependencies — chosen over `sigs.k8s.io/yaml` (pulls k8s apimachinery) and over hand-parsing (kubeconfig YAML can use flow style / quoting that a line scanner would mishandle). We unmarshal **only** context names + default namespaces + `current-context`; clusters, users, and credentials are never read. If the owner prefers zero new deps, the fallback is a hand-rolled scanner restricted to block-style `contexts:` — call that out in review before implementing it.

- [ ] **Step 1: Write the failing tests**

```go
package kubediscover

import (
	"os"
	"path/filepath"
	"testing"
)

const sampleKubeconfig = `apiVersion: v1
kind: Config
current-context: prod
contexts:
- name: prod
  context:
    cluster: prod-cluster
    namespace: payments
    user: prod-user
- name: staging
  context:
    cluster: staging-cluster
    user: staging-user
clusters: []
users: []
`

func writeConfig(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

func TestContexts_ParsesNamesNamespacesAndCurrent(t *testing.T) {
	dir := t.TempDir()
	cfg := writeConfig(t, dir, "config", sampleKubeconfig)
	t.Setenv("KUBECONFIG", cfg)

	got := Contexts()
	if len(got) != 2 {
		t.Fatalf("got %d contexts, want 2: %+v", len(got), got)
	}
	if got[0].Name != "prod" || got[0].Namespace != "payments" || !got[0].Current {
		t.Errorf("ctx[0] = %+v, want prod/payments/current", got[0])
	}
	if got[1].Name != "staging" || got[1].Namespace != "" || got[1].Current {
		t.Errorf("ctx[1] = %+v, want staging/empty/not-current", got[1])
	}
}

func TestContexts_KubeconfigList_FirstFileWins(t *testing.T) {
	dir := t.TempDir()
	a := writeConfig(t, dir, "a", "contexts:\n- name: dup\n  context:\n    namespace: from-a\ncurrent-context: dup\n")
	b := writeConfig(t, dir, "b", "contexts:\n- name: dup\n  context:\n    namespace: from-b\n- name: only-b\n  context: {}\n")
	t.Setenv("KUBECONFIG", a+string(os.PathListSeparator)+b)

	got := Contexts()
	if len(got) != 2 {
		t.Fatalf("got %d, want 2 (dup deduped + only-b): %+v", len(got), got)
	}
	if got[0].Name != "dup" || got[0].Namespace != "from-a" {
		t.Errorf("dup should resolve from the first file: %+v", got[0])
	}
}

func TestContexts_MissingFile_DegradesToEmpty(t *testing.T) {
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "nope"))
	if got := Contexts(); len(got) != 0 {
		t.Errorf("got %+v, want empty", got)
	}
}

func TestContexts_MalformedYAML_DegradesToEmpty(t *testing.T) {
	dir := t.TempDir()
	cfg := writeConfig(t, dir, "config", "contexts: [this is : not : valid : yaml\n")
	t.Setenv("KUBECONFIG", cfg)
	if got := Contexts(); len(got) != 0 {
		t.Errorf("got %+v, want empty for malformed yaml", got)
	}
}

func TestContexts_SymlinkedConfig_Rejected(t *testing.T) {
	dir := t.TempDir()
	real := writeConfig(t, dir, "real", sampleKubeconfig)
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unsupported: %v", err) // Windows without privilege
	}
	t.Setenv("KUBECONFIG", link)
	if got := Contexts(); len(got) != 0 {
		t.Errorf("symlinked kubeconfig must be rejected, got %+v", got)
	}
}

func TestKubeconfigPaths_SplitsListSeparator(t *testing.T) {
	t.Setenv("KUBECONFIG", "x"+string(os.PathListSeparator)+"y")
	got := KubeconfigPaths()
	if len(got) != 2 || got[0] != "x" || got[1] != "y" {
		t.Errorf("got %v, want [x y]", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `./scripts/dev.sh test`
Expected: FAIL — `internal/kubediscover` does not compile (undefined: Contexts, KubeconfigPaths) and `gopkg.in/yaml.v3` not found.

- [ ] **Step 3: Add the dependency and implement.**

Add to `go.mod` require block: `gopkg.in/yaml.v3 v3.0.1`, then run `go mod tidy` inside the build container (the host has no Go toolchain — mirror how `./scripts/dev.sh` invokes `golang:1.25-alpine` with the `quil-gomod` volume; a one-off `docker run --rm -v "$PWD":/src -v quil-gomod:/go/pkg/mod -w /src golang:1.25-alpine go mod tidy` works).

`internal/kubediscover/kubediscover.go`:

```go
// Package kubediscover enumerates kube contexts from the kubeconfig file(s)
// referenced by KUBECONFIG (OS-list-separated) or ~/.kube/config. Pure reads
// with no cluster I/O: every failure (missing file, unreadable, malformed
// YAML, symlinked path) degrades to "no contexts" so discovery can never
// block pane creation. Only context names, default namespaces, and
// current-context are parsed — clusters, users, and credentials are ignored.
package kubediscover

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Context is one entry enumerated from the kubeconfig file(s).
type Context struct {
	Name      string // value passed to k9s --context
	Namespace string // default namespace for the context; may be empty
	Current   bool   // true if this is the kubeconfig's current-context
}

// KubeconfigPaths returns the resolved kubeconfig precedence list: KUBECONFIG
// split on the OS list separator if set, else ~/.kube/config. Exported for
// testing.
func KubeconfigPaths() []string {
	if env := os.Getenv("KUBECONFIG"); env != "" {
		var paths []string
		for _, p := range filepath.SplitList(env) {
			if p != "" {
				paths = append(paths, p)
			}
		}
		return paths
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{filepath.Join(home, ".kube", "config")}
}

// kubeconfigFile is the minimal shape we unmarshal — nothing more.
type kubeconfigFile struct {
	CurrentContext string `yaml:"current-context"`
	Contexts       []struct {
		Name    string `yaml:"name"`
		Context struct {
			Namespace string `yaml:"namespace"`
		} `yaml:"context"`
	} `yaml:"contexts"`
}

// Contexts merges the kubeconfig path list (first-file-wins for duplicate
// context names, matching kubectl) and returns the contexts with the
// current-context marked.
func Contexts() []Context {
	var out []Context
	seen := make(map[string]bool)
	current := ""
	for _, path := range KubeconfigPaths() {
		// Reject symlinks: an attacker-controlled KUBECONFIG could otherwise
		// point discovery at an arbitrary file. k9s itself handles the real
		// connection; we only read named contexts.
		fi, err := os.Lstat(path)
		if err != nil || fi.Mode()&os.ModeSymlink != 0 {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var kc kubeconfigFile
		if err := yaml.Unmarshal(data, &kc); err != nil {
			continue
		}
		if current == "" && kc.CurrentContext != "" {
			current = kc.CurrentContext
		}
		for _, c := range kc.Contexts {
			if c.Name == "" || seen[c.Name] {
				continue
			}
			seen[c.Name] = true
			out = append(out, Context{Name: c.Name, Namespace: c.Context.Namespace})
		}
	}
	for i := range out {
		if out[i].Name == current {
			out[i].Current = true
		}
	}
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `./scripts/dev.sh test` then `./scripts/dev.sh vet`
Expected: PASS, vet clean.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum internal/kubediscover/
git commit -m "feat(kubediscover): add kube-context discovery package

Parses kubeconfig (KUBECONFIG list -> ~/.kube/config) for context
names, default namespaces, and current-context. Reads nothing else;
symlinked paths and parse errors degrade to no-contexts. Adds
gopkg.in/yaml.v3 (kubeconfig is YAML)."
```

### Task 4: `discover = "kube"` registry validation

**Files:**
- Modify: `internal/plugin/registry.go` (the `discover` switch ~line 363)
- Test: `internal/plugin/plugin_test.go`

- [ ] **Step 1: Write the failing test** (append to `plugin_test.go`, mirroring `TestLoadPluginTOML_DiscoverField`)

```go
func TestLoadPluginTOML_DiscoverKube(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "k.toml")
	data := []byte(`
[plugin]
name = "k9s"
[command]
cmd = "k9s"
discover = "kube"
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := loadPluginTOML(path)
	if err != nil {
		t.Fatalf("loadPluginTOML: %v", err)
	}
	if p.Command.Discover != "kube" {
		t.Errorf("Discover = %q, want kube", p.Command.Discover)
	}
}
```

The existing `TestLoadPluginTOML_DiscoverInvalid` (discover="svn" must error) already guards the negative case — leave it.

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test`
Expected: FAIL — `unknown discover mode "kube"`.

- [ ] **Step 3: Implement.** In `registry.go`, extend the discover switch:

```go
	switch tp.Command.Discover {
	case "", "git", "kube":
		// valid
	default:
		return nil, fmt.Errorf("plugin %q: unknown discover mode %q", tp.Plugin.Name, tp.Command.Discover)
	}
```

Also update the doc comment on `CommandConfig.Discover` in `internal/plugin/plugin.go` to mention `"kube"`:

```go
	// Discover selects a discovery mode for the pane setup dialog.
	// "" = none. "git" = the CWD step lists git repo candidates (requires
	// PromptsCWD). "kube" = a context pick-list lists kube contexts from the
	// kubeconfig and injects --context <name> (CWD-independent).
	Discover string
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `./scripts/dev.sh test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/plugin/plugin.go internal/plugin/registry.go internal/plugin/plugin_test.go
git commit -m "feat(plugin): accept discover=\"kube\" in command schema"
```

### Task 5: kube-context pick field in the setup dialog

**Files:**
- Modify: `internal/tui/model.go` (Model state near `repoCandidates` ~line 235; a `maxKubeContexts` const)
- Modify: `internal/tui/dialog.go` (`enterSetupOrSplit` reset + population; `setupFieldCount`; `setupFieldKind`; `handleCreatePaneSetupKey` dispatch; new `handleSetupKubeKey`; `submitSetupDialog` injection; `renderCreatePaneSetupDialog` branch)
- Test: `internal/tui/setup_dialog_test.go`

**Design recap:** when the plugin has `Discover == "kube"`, the setup dialog gains a context-pick field (a focusable field like CWD, but CWD-independent since `prompts_cwd = false`). Row 0 is **"Default context"** (injects no `--context`, k9s uses its own current-context); rows 1..N are the discovered contexts (current-context marked). On submit, a selected named context appends `["--context", "<name>"]` to `selectedInstanceArgs`. The field model (`setupFieldCount`/`setupFieldKind`) already cleanly supports an extra leading field.

- [ ] **Step 1: Write the failing tests** (append to `setup_dialog_test.go`; reuse the `Model`-literal pattern from the repo-pick tests)

```go
// kubePickModel builds a Model sitting in the setup dialog with a kube field.
func kubePickModel(t *testing.T, ctxs []kubediscover.Context) Model {
	t.Helper()
	return Model{
		dialog:         dialogCreatePaneSetup,
		kubeContexts:   ctxs,
		kubeCursor:     0,
		toggleStates:   nil,
		pluginRegistry: registryWithK9s(t),
		selectedPlugin: "k9s",
	}
}

// registryWithK9s mirrors registryWithLazygit: temp TOML + LoadFromDir, then
// flip Available. k9s has prompts_cwd=false and discover=kube.
func registryWithK9s(t *testing.T) *plugin.Registry {
	t.Helper()
	dir := t.TempDir()
	content := `[plugin]
name = "k9s"

[command]
cmd = "k9s"
prompts_cwd = false
discover = "kube"
`
	if err := os.WriteFile(filepath.Join(dir, "k9s.toml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write test toml: %v", err)
	}
	r := plugin.NewRegistry()
	if err := r.LoadFromDir(dir); err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}
	r.Get("k9s").Available = true
	return r
}

func TestSetupFieldKind_KubeFieldFirst(t *testing.T) {
	t.Parallel()
	m := Model{}
	p := &plugin.PanePlugin{Command: plugin.CommandConfig{Discover: "kube"}}
	if kind, _ := m.setupFieldKind(p, 0); kind != "kube" {
		t.Errorf("field 0 kind = %q, want kube", kind)
	}
	// Only field besides Continue → count is 2.
	if got := m.setupFieldCount(p); got != 2 {
		t.Errorf("setupFieldCount = %d, want 2 (kube + continue)", got)
	}
}

func TestSetupKubeKey_DownMoves(t *testing.T) {
	t.Parallel()
	m := kubePickModel(t, []kubediscover.Context{{Name: "prod", Current: true}, {Name: "staging"}})
	out, _ := m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: tea.KeyDown})
	got := out.(Model)
	if got.kubeCursor != 1 {
		t.Errorf("kubeCursor = %d, want 1", got.kubeCursor)
	}
}

func TestSubmitSetup_KubeContext_InjectsContextArg(t *testing.T) {
	t.Parallel()
	m := kubePickModel(t, []kubediscover.Context{{Name: "prod"}, {Name: "staging"}})
	p := m.pluginRegistry.Get("k9s")
	m.kubeCursor = 2 // row 0 = Default, row 1 = prod, row 2 = staging
	out, _ := m.submitSetupDialog(p)
	got := out.(Model)
	want := []string{"--context", "staging"}
	if len(got.selectedInstanceArgs) != 2 || got.selectedInstanceArgs[0] != want[0] || got.selectedInstanceArgs[1] != want[1] {
		t.Errorf("selectedInstanceArgs = %v, want %v", got.selectedInstanceArgs, want)
	}
}

func TestSubmitSetup_KubeDefaultRow_NoContextArg(t *testing.T) {
	t.Parallel()
	m := kubePickModel(t, []kubediscover.Context{{Name: "prod"}})
	p := m.pluginRegistry.Get("k9s")
	m.kubeCursor = 0 // Default context
	out, _ := m.submitSetupDialog(p)
	got := out.(Model)
	for _, a := range got.selectedInstanceArgs {
		if a == "--context" {
			t.Errorf("Default row must not inject --context, got %v", got.selectedInstanceArgs)
		}
	}
}
```

Check the existing repo-pick tests for the exact `tea.KeyPressMsg` construction and copy it.

- [ ] **Step 2: Run tests to verify they fail**

Run: `./scripts/dev.sh test`
Expected: FAIL — `m.kubeContexts undefined` / `kube` field kind unknown.

- [ ] **Step 3: Implement.**

`internal/tui/model.go` — next to `repoCandidates` (~line 235):

```go
	kubeContexts []kubediscover.Context // contexts offered by the setup dialog (discover="kube"); nil = none
	kubeCursor   int                    // row cursor in the kube field: 0 = Default context, 1.. = kubeContexts
```

Add a const near `maxRepoCandidates`:

```go
const maxKubeContexts = 50
```

Add the import `"github.com/artyomsv/quil/internal/kubediscover"` to whichever files reference it (model.go, dialog.go).

`internal/tui/dialog.go`:

`enterSetupOrSplit` reset block — after `m.repoCandidates = nil`:

```go
	m.kubeContexts = nil
	m.kubeCursor = 0
```

`enterSetupOrSplit` show condition — extend so a kube plugin shows the dialog even with no toggles:

```go
	needsSetup := p != nil && (p.Command.PromptsCWD || len(p.Command.Toggles) > 0 || p.Command.Discover == "kube")
```

`enterSetupOrSplit` — after the `if p.Command.PromptsCWD { ... }` block and before the toggle-init block, populate contexts:

```go
	if p.Command.Discover == "kube" {
		m.kubeContexts = kubediscover.Contexts()
		if len(m.kubeContexts) > maxKubeContexts {
			m.kubeContexts = m.kubeContexts[:maxKubeContexts]
		}
		m.kubeCursor = 0 // Default context row
	}
```

`setupFieldCount` — add the kube field:

```go
func (m Model) setupFieldCount(p *plugin.PanePlugin) int {
	n := len(p.Command.Toggles) + 1 // +1 for Continue
	if p.Command.PromptsCWD {
		n++
	}
	if p.Command.Discover == "kube" {
		n++
	}
	return n
}
```

`setupFieldKind` — insert the kube field after the CWD slot (they never co-occur in practice, but keep the order deterministic):

```go
func (m Model) setupFieldKind(p *plugin.PanePlugin, cursor int) (kind string, toggleIdx int) {
	i := cursor
	if p.Command.PromptsCWD {
		if i == 0 {
			return "cwd", -1
		}
		i--
	}
	if p.Command.Discover == "kube" {
		if i == 0 {
			return "kube", -1
		}
		i--
	}
	if i < len(p.Command.Toggles) {
		return "toggle", i
	}
	return "continue", -1
}
```

`handleCreatePaneSetupKey` field dispatch — add a case alongside `"cwd"`:

```go
	case "kube":
		return m.handleSetupKubeKey(p, key)
```

New method (place after `handleSetupRepoKey`):

```go
// handleSetupKubeKey processes keystrokes when the kube-context field is
// focused (discover="kube"). Row 0 is the "Default context" (no --context
// flag); rows 1..N are the discovered contexts. kubeCursor is the row index.
func (m Model) handleSetupKubeKey(p *plugin.PanePlugin, key string) (tea.Model, tea.Cmd) {
	rows := len(m.kubeContexts) + 1 // + Default context row
	switch key {
	case "up", "k":
		if m.kubeCursor > 0 {
			m.kubeCursor--
		}
		return m, nil
	case "down", "j":
		if m.kubeCursor < rows-1 {
			m.kubeCursor++
		}
		return m, nil
	case "enter":
		return m.submitSetupDialog(p)
	}
	return m, nil
}
```

`submitSetupDialog` — inject the chosen context before the toggle-merge block (after `m.cwdInputError = ""`):

```go
	// Inject the chosen kube context (row 0 = Default = no --context flag).
	if p.Command.Discover == "kube" && m.kubeCursor > 0 && m.kubeCursor-1 < len(m.kubeContexts) {
		ctx := m.kubeContexts[m.kubeCursor-1].Name
		merged := make([]string, 0, len(m.selectedInstanceArgs)+2)
		merged = append(merged, m.selectedInstanceArgs...)
		merged = append(merged, "--context", ctx)
		m.selectedInstanceArgs = merged
	}
```

`renderCreatePaneSetupDialog` — add a kube field render branch where the CWD/field rows are built (find it via `grep -n "setupFieldKind\|cwdBrowse" internal/tui/dialog.go` inside the render function). When `p.Command.Discover == "kube"`, render the field as a list: a "Default context" row, then one row per context with the current-context marked (e.g. `●`), highlighting `m.kubeCursor` with the same cursor marker the other fields use. Mark the field focused when `setupFieldKind(p, m.setupFieldCursor) == "kube"`. Adapt to the render function's actual line-builder shape; the structure is the contract.

- [ ] **Step 4: Run tests to verify they pass**

Run: `./scripts/dev.sh test` then `./scripts/dev.sh vet`
Expected: PASS, vet clean.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/model.go internal/tui/dialog.go internal/tui/setup_dialog_test.go
git commit -m "feat(tui): kube-context pick field in pane setup dialog

discover=kube plugins gain a context pick field: Default context
(current-context, no flag) plus the kubeconfig's contexts. Selecting
a named context injects --context <name> into the spawn args."
```

### Task 6: flip k9s to `discover = "kube"` + docs/site/changelog

**Files:**
- Modify: `internal/plugin/defaults/k9s.toml` (uncomment `discover = "kube"`; bump `schema_version` to 2)
- Modify: `internal/plugin/defaults_test.go` (update the Tier A assertion)
- Modify: `docs/plugin-reference.md`, `site/src/data/plugins.ts`, `CHANGELOG.md`

- [ ] **Step 1: Update the test.** Change `TestEnsureDefaultPlugins_WritesK9s` to expect `p.Command.Discover == "kube"` and `p.SchemaVersion == 2` (verify the actual field name for schema version on the parsed plugin via `grep -n "SchemaVersion\|schema_version" internal/plugin/*.go`).

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test`
Expected: FAIL — Discover still "".

- [ ] **Step 3: Edit `k9s.toml`.** Uncomment `discover = "kube"` and bump `schema_version = 2`. Add a brief comment:

```toml
schema_version = 2

[command]
cmd = "k9s"
detect = "k9s version"
prompts_cwd = false
# Pane setup offers a pick-list of kube contexts (from KUBECONFIG /
# ~/.kube/config) and injects --context <name>. "Default context" = no flag.
discover = "kube"
```

> **schema_version bump rationale:** existing users who already have a `k9s.toml` from a Tier-A-only release get the migration dialog (per `EnsureDefaultPlugins` stale-detection) so the new `discover` line is offered rather than silently overwritten. If Tier A and Tier B ship in the **same** release (no intermediate Tier-A-only version reached users), keep `schema_version = 1` and skip this paragraph — verify against the released `VERSION`/CHANGELOG before deciding.

- [ ] **Step 4: Run tests to verify they pass**

Run: `./scripts/dev.sh test`
Expected: PASS.

- [ ] **Step 5: Manual verification (dev mode).** `./scripts/dev.sh build`, run `./scripts/quil-dev.ps1`. With k9s installed and a kubeconfig holding ≥2 contexts: Ctrl+N → Tools → k9s → the setup dialog shows a context pick-list with the current-context marked. Select a non-default context → the pane opens k9s connected to that context (verify via k9s's context indicator). Choose "Default context" → k9s uses the kubeconfig current-context. With no kubeconfig: the pick-list shows only "Default context" and the pane still opens.

- [ ] **Step 6: Update docs/site/changelog.** `docs/plugin-reference.md`: document `discover = "kube"` alongside `discover = "git"`. `site/src/data/plugins.ts`: mention the context picker in the k9s entry. `CHANGELOG.md` `[Unreleased]` → Added:

```markdown
- **k9s context picker** — the k9s pane setup dialog now lists kube contexts
  from KUBECONFIG / ~/.kube/config and pins the pane to the chosen context
  via `--context`. "Default context" uses the kubeconfig current-context.
```

Build the site: `npm run build` in `site/` → 0 errors.

- [ ] **Step 7: Commit**

```bash
git add internal/plugin/defaults/k9s.toml internal/plugin/defaults_test.go docs/plugin-reference.md site/src/data/plugins.ts CHANGELOG.md
git commit -m "feat(plugin): enable kube-context discovery for k9s

Flip k9s to discover=kube so the setup dialog offers a context
pick-list. Bumps schema_version for the migration prompt."
```

---

## Final verification

- [ ] `./scripts/dev.sh test` — full suite green.
- [ ] `./scripts/dev.sh test-race` — race detector clean (kubediscover is pure; the setup-dialog state is single-goroutine, but run it as the standard gate).
- [ ] `./scripts/dev.sh vet` — clean.
- [ ] `npm run build` in `site/` — 0 errors.
- [ ] Manual: k9s pane opens cross-platform when the binary is present; greyed out when absent; context picker pins `--context`; restart respawns via `rerun`.
- [ ] `grep` the diff for any AI-vendor mentions — must be zero (commits, comments, docs, site).

## Notes for the implementer

- **Tier A and Tier B are independent.** Phase 1 is a complete, shippable feature on its own; do not let Phase 2 work block a Phase 1 release.
- **No cluster probing.** Detection (`k9s version`) checks binary presence only — never cluster reachability. An unreachable cluster is k9s's own error screen, shown in the pane.
- **No synthetic data.** Never fabricate context names, namespaces, or clusters in tests that could be read as real config — the table tests above use obviously-synthetic names (`prod`/`staging`/`dup`/`only-b`) inside `t.TempDir()` fixtures, which is fine.
- **Out of scope (do not build):** k9s overlay (Alt-key toggle), live namespace/resource enumeration, custom `--kubeconfig` path selection in the dialog, MCP-created k9s context args, reading clusters/users/credentials from kubeconfig.
