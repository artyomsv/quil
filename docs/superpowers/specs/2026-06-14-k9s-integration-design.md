# k9s Integration — Design Spec

**Date:** 2026-06-14
**Status:** Approved for planning
**Scope:** Two deliverables — k9s built-in plugin (Tier A) + kube-context-aware pane setup dialog (Tier B)

## Problem

Quil panes already host full-screen developer TUIs (claude-code, opencode, lazygit).
Kubernetes operators want the same one-keystroke access to **k9s** — a mature
cluster TUI — without leaving their Quil workspace. k9s is a single Go binary
released for Windows, macOS, and Linux, so it fits the existing plugin model.

Two properties shape the design and separate it from lazygit:

1. **k9s is cluster-scoped, not directory-scoped.** It ignores the working
   directory and connects to whatever the kubeconfig points at (`KUBECONFIG`
   env list → `~/.kube/config`). There is no filesystem walk to do — the
   `discover = "git"` machinery does not apply.
2. **Cluster selection is by context, not path.** The only meaningful "where
   does this pane point" choice is the **kube context** (`--context <name>`),
   enumerable statically from the kubeconfig file(s).

The feature must be inert when the k9s binary is not installed.

## Decisions (made during brainstorming)

| Question | Decision |
|---|---|
| Presentation | **Normal pane**, not an overlay. k9s is a long-lived monitoring view you split alongside other panes — the lazygit "quick peek + quit" overlay model does not fit. |
| Tier A scope | TOML-only plugin, **zero Go changes**. Ships independently. |
| Tier B scope | New `discover = "kube"` mode: setup dialog offers a kube-context pick-list, injecting `--context`. |
| CWD prompting | `prompts_cwd = false` — k9s ignores CWD; the pane inherits the active pane's CWD for cosmetics/`rerun` only. |
| Read-only safety | `--readonly` offered as an opt-in toggle (default off), not forced and not silently allowed. |
| kubeconfig parsing | Parse it ourselves via a YAML dep (`gopkg.in/yaml.v3`). Do **not** shell out to `kubectl` (k9s uses client-go directly; kubectl may be absent). |
| Namespace selection | Out of scope — live namespace lists require cluster connectivity. Context's default namespace (from kubeconfig) applies. |
| MCP exposure | `create_pane` schema unchanged; no context arg in v1. |

## Tier A — Built-in plugin

New `internal/plugin/defaults/k9s.toml`, auto-embedded and written on first run
by `EnsureDefaultPlugins` (same path as `lazygit.toml`):

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
# k9s connects to the cluster via KUBECONFIG / ~/.kube/config, not the CWD.
prompts_cwd = false
# discover = "kube"      # Tier B opt-in — see §Tier B

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

- **Binary gating is free.** The existing 3-tier `DetectAvailability()` (path
  override → `exec.LookPath` → `searchBinary`) greys the plugin out of Ctrl+N
  when k9s is missing — identical to lazygit. Detection checks *binary
  presence only*, never cluster reachability (correct: lazygit likewise does
  not verify a repo exists).
- **No new code.** Tier A is purely the embedded TOML default plus the
  CHANGELOG / docs / site entries.
- **Normal pane semantics throughout:** PTY, ring buffer, memory reporting,
  `list_panes`, splits, focus mode, ConPTY resize-healing — all unchanged.
  k9s drives `:` command mode and `Ctrl+C` / `:q` to quit; all keys route to
  the PTY naturally (no overlay key allow-list needed).
- **Persistence:** `rerun` respawns `k9s` (plus any enabled toggle args) on
  daemon restart; k9s re-establishes the cluster connection itself.

### Cross-platform notes (Tier A)

- k9s is shipped for all three OSes (Homebrew/MacPorts on macOS; apt/dnf/snap/
  pacman/zypper on Linux; Winget/Scoop/Chocolatey on Windows). `k9s version`
  exits 0 on every platform → uniform detection.
- No platform-specific code. The TOML default is identical everywhere.

## Tier B — kube-context-aware setup dialog

Adds a static context picker to the Ctrl+N flow so a pane can be pinned to a
specific cluster context at creation. Builds on Tier A; ships independently
after it.

### New package: `internal/kubediscover`

Pure, table-testable, mirroring `internal/gitdiscover`'s discipline (errors
degrade to "no candidates"; discovery never blocks pane creation):

```go
package kubediscover

// Context is one entry enumerated from the kubeconfig file(s).
type Context struct {
    Name      string // context name (the value passed to --context)
    Namespace string // default namespace for the context; may be empty
    Current   bool   // true if this is the kubeconfig's current-context
}

// Contexts resolves the kubeconfig path list, parses each file, and returns
// the merged context set with the current-context marked. Any failure
// (no file, unreadable, malformed YAML) degrades to an empty slice.
func Contexts() []Context

// KubeconfigPaths returns the resolved kubeconfig precedence list:
// the KUBECONFIG env var split on the OS list separator if set, else
// ~/.kube/config. Exported for testing.
func KubeconfigPaths() []string
```

- **Path resolution:** `KUBECONFIG` env may hold a list joined by the OS
  separator — split on `filepath.ListSeparator` (`:` Unix, `;` Windows). If
  unset, fall back to `os.UserHomeDir()/.kube/config`. First-file-wins for
  duplicate context names (kubectl merge semantics), but enumeration is the
  union of names.
- **Parsing:** add `gopkg.in/yaml.v3` (single dependency, no transitive k8s
  apimachinery). Parse only the `contexts:` array (`name`, `context.namespace`)
  and `current-context` scalar — ignore clusters/users/auth entirely. We never
  read credentials.
- **Security:** treat kubeconfig as untrusted input — bound the parse (k9s/
  kubectl handle the real connection), `Lstat`-reject symlinked config paths
  consistent with the daemon's `EvalSymlinks` discipline, and never log
  context contents beyond names.

### Registry validation

Extend the `discover` switch in `registry.go` (currently `"", "git"`) to accept
`"kube"`; unknown values still error on plugin load:

```go
switch tp.Command.Discover {
case "", "git", "kube":
    // valid
default:
    return nil, fmt.Errorf("plugin %q: unknown discover mode %q", ...)
}
```

The k9s default TOML then sets `discover = "kube"` under `[command]`.

### Setup dialog generalization (B)

Today `discover = "git"` replaces the **directory step** of
`dialogCreatePaneSetup` with a repo pick-list. `discover` must generalize from
"what the directory step enumerates" to "what the discovery step enumerates and
which arg it injects":

| `discover` | Step shows | Injects | CWD step |
|---|---|---|---|
| `"git"` | git repo candidates + "Browse…" | spawn CWD (lazygit walks for `.git`) | yes (`prompts_cwd`) |
| `"kube"` | kube-context pick-list | `--context <name>` into `InstanceArgs` | no (`prompts_cwd = false`) |

- The kube step lists `kubediscover.Contexts()` with the **current-context**
  marked (e.g. a `●` prefix) and a **"Default context"** row at top that
  injects *no* `--context` flag (k9s uses its own current-context). Selecting a
  named context appends `["--context", "<name>"]` to `InstanceArgs`.
- **Zero contexts** (no kubeconfig / empty / malformed) → skip straight to
  launch with no `--context`; k9s resolves its own current-context or shows its
  error screen in-pane. We never fabricate context names.
- No new dialog screen — it is a mode of the existing setup step, exactly as
  the git pick-list is. Reuses the candidate-list rendering/keys already built
  for `dialogGitRepoPick` where practical.

### Persistence (Tier B)

The chosen `--context <name>` lives in `Pane.InstanceArgs`, persisted in
`workspace.json` like any other plugin args. The `rerun` strategy respawns k9s
with the same context on restart — no special handling.

## Error handling

| Failure | Behavior |
|---|---|
| k9s binary missing | Plugin greyed out of Ctrl+N (Tier A, free via `DetectAvailability`) |
| No kubeconfig / no contexts | Context step shows only "Default context"; launch with no `--context` |
| Malformed kubeconfig YAML | Treated as no contexts (degrade); pane still creatable |
| Symlinked kubeconfig path | Rejected by `Lstat` guard; treated as no contexts |
| No cluster reachable | k9s shows its own error screen in the pane — not Quil's concern; detection never probes the cluster |
| Daemon restart | k9s pane respawns via `rerun` (with saved `--context` if any) and reconnects |

## Testing

- **Tier A — plugin TOML:** parse + toggle test alongside existing
  `plugin_test.go` patterns (verify `readonly` and `start_pods` toggles produce
  the right args; verify `prompts_cwd == false`, `strategy == "rerun"`,
  `ghost_buffer == false`). Detection itself is existing, already-tested
  behavior.
- **Tier B — `kubediscover`:** table-driven with `t.TempDir()` fake kubeconfig
  files and `t.Setenv("KUBECONFIG", ...)`. Cases: single file, KUBECONFIG list
  (OS-separator split), missing file, unreadable, malformed YAML, no
  current-context, duplicate context names across files, symlinked path
  (rejected). Cross-platform separator handled via `filepath.ListSeparator`.
- **Tier B — registry:** validation test that `discover = "kube"` loads and an
  unknown value still errors (extends the existing `discover=svn` test).
- **Tier B — setup dialog:** `fakeSender` injection (pattern from the
  shutdown-confirm / git-repo-pick tests) covering: contexts present →
  pick-list with current marked; "Default context" → no `--context`; named
  context → `--context <name>` in `InstanceArgs`; zero contexts → straight to
  launch.

## Phasing

1. **Phase 1 (Tier A):** embed `k9s.toml`, register in `EnsureDefaultPlugins`,
   CHANGELOG + docs (`docs/plugin-reference.md`, features doc) + site
   (`site/src/data/plugins.ts`). Shippable alone; no Go logic.
2. **Phase 2 (Tier B):** `internal/kubediscover` package + `yaml.v3` dep +
   `registry.go` `discover = "kube"` validation + setup-dialog kube-context
   mode + flip the default TOML to `discover = "kube"`.

Each phase ships independently; Phase 2 has no effect until k9s is installed
and a kubeconfig exists.

## Out of scope (v1)

- **k9s overlay** (an Alt-key toggle like lazygit's Alt+G) — k9s is a
  persistent pane, not a peek-and-dismiss overlay.
- **Live namespace / resource enumeration** in the dialog — requires a cluster
  API call; the context's kubeconfig default namespace applies.
- **Custom `--kubeconfig` path selection** in the dialog — `KUBECONFIG` env
  multi-file resolution covers the realistic cases.
- **MCP-created k9s panes with a context arg** — `create_pane` schema unchanged.
- **Reading clusters/users/credentials** from the kubeconfig — we parse context
  names and default namespaces only.
