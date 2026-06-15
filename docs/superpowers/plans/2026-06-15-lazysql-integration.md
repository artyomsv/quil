# lazysql Integration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** lazysql integration per spec `docs/superpowers/specs/2026-06-15-lazysql-integration-design.md` — a single built-in `lazysql` plugin (Tier A, TOML-only). No Go logic, no new dependency. A connection-picker is explicitly out of scope (credential leak via argv — see the spec's Rejected section).

**Architecture:** One phase. Add a `lazysql` default plugin TOML; binary gating, the greyed-when-missing UX, the `homepage` link, and the normal-pane lifecycle all come free from the existing plugin system (shipped with the k9s work). Update the default-file completeness test and the docs/site.

**Tech Stack:** Go 1.25. Build/test via Docker: `./scripts/dev.sh test` (full suite — no per-package form), `./scripts/dev.sh vet`. Host has no Go toolchain.

**Branch:** `feat/lazysql-integration` (already created off `origin/master` @ v1.27.0, which includes the merged k9s integration).

**Conventions:** 2-space TOML, tabs in Go, table-driven tests, `t.TempDir()`. Never run production `quil`/`quild` or touch `~/.quil/` — manual verification uses `./quil-dev.exe` from the project directory. No AI-tooling-vendor mentions in commits/docs/site.

---

## Task 1: lazysql built-in plugin TOML

**Files:**
- Create: `internal/plugin/defaults/lazysql.toml` (auto-embedded by `//go:embed defaults/*.toml` — no Go change)
- Test: `internal/plugin/defaults_test.go`, `internal/plugin/plugin_test.go`

- [ ] **Step 1: Write the failing test** (append to `defaults_test.go`, mirroring `TestEnsureDefaultPlugins_WritesK9s`)

```go
func TestEnsureDefaultPlugins_WritesLazysql(t *testing.T) {
	dir := t.TempDir()
	if _, err := EnsureDefaultPlugins(dir); err != nil {
		t.Fatalf("EnsureDefaultPlugins: %v", err)
	}
	p, err := loadPluginTOML(filepath.Join(dir, "lazysql.toml"))
	if err != nil {
		t.Fatalf("load lazysql.toml: %v", err)
	}
	if p.Name != "lazysql" || p.Command.Cmd != "lazysql" {
		t.Errorf("name/cmd = %q/%q", p.Name, p.Command.Cmd)
	}
	if p.Homepage != "https://github.com/jorgerojas26/lazysql" {
		t.Errorf("Homepage = %q, want the lazysql URL", p.Homepage)
	}
	// Connection-scoped, not directory-scoped: no CWD prompt, no discovery.
	if p.Command.PromptsCWD {
		t.Errorf("PromptsCWD = true, want false")
	}
	if p.Command.Discover != "" {
		t.Errorf("Discover = %q, want \"\" (no Quil-side connection picker)", p.Command.Discover)
	}
	if p.Persistence.Strategy != "rerun" || p.Persistence.GhostBuffer {
		t.Errorf("strategy=%q ghost=%v, want rerun/false", p.Persistence.Strategy, p.Persistence.GhostBuffer)
	}
	if len(p.Command.Toggles) != 1 || p.Command.Toggles[0].Name != "read_only" {
		t.Errorf("toggles = %+v, want one read_only", p.Command.Toggles)
	}
}
```

Also update `TestDefaultPluginTOMLFiles` (it currently checks 5 defaults after the k9s merge):

```go
// Verify all 6 default TOML files were created
for _, name := range []string{"claude-code.toml", "ssh.toml", "stripe.toml", "lazygit.toml", "k9s.toml", "lazysql.toml"} {
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `./scripts/dev.sh test`
Expected: FAIL — `load lazysql.toml: ... no such file`.

- [ ] **Step 3: Create `internal/plugin/defaults/lazysql.toml`**

```toml
# lazysql — database TUI (MySQL, PostgreSQL, SQLite, MSSQL)
# Edit this file to customize the plugin. Delete it to restore defaults.

[plugin]
name = "lazysql"
display_name = "lazysql"
category = "tools"
description = "Database TUI (MySQL, PostgreSQL, SQLite, MSSQL)"
homepage = "https://github.com/jorgerojas26/lazysql"
schema_version = 1

[command]
cmd = "lazysql"
# path = "/path/to/lazysql"  # uncomment to override PATH lookup
detect = "lazysql --version"
# lazysql is connection-scoped, not directory-scoped: it opens its own
# connection-manager UI and reads connections from its own config. No CWD
# prompt and no Quil-side connection picker — the only launch arg lazysql
# accepts is a full DSN (with credentials), which must never reach argv.
prompts_cwd = false

[[command.toggles]]
name = "read_only"
label = "Read-only (no data modification)"
args_when_on = ["--read-only"]
default = false

[persistence]
# Re-run lazysql on daemon restart; it reopens its connection manager.
strategy = "rerun"
# Full-screen TUI — replaying stale frames on reconnect is useless.
ghost_buffer = false
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `./scripts/dev.sh test` then `./scripts/dev.sh vet`
Expected: PASS, vet clean.

- [ ] **Step 5: Manual verification (dev mode).** `./scripts/dev.sh build`, run `./scripts/quil-dev.ps1` from the project dir, confirm `[dev]`. With lazysql installed: `Ctrl+N → Tools → lazysql` opens the connection manager; the read-only toggle is offered. Without lazysql: the entry is greyed with the homepage link.

- [ ] **Step 6: Commit**

```bash
git add internal/plugin/defaults/lazysql.toml internal/plugin/defaults_test.go internal/plugin/plugin_test.go
git commit -m "feat(plugin): add lazysql built-in plugin

Database TUI as a normal pane: detect via 'lazysql --version', no CWD
prompt (connection-scoped), rerun persistence, ghost buffer off,
optional --read-only toggle. No Quil-side connection picker — lazysql's
only launch arg is a credentialed DSN, so connection selection stays in
lazysql's own manager. Binary gating + greyed-when-missing come free."
```

## Task 2: docs + site + changelog

**Files:**
- Modify: `docs/features.md` (k9s-style entry + TOC), `site/src/data/plugins.ts`, `site/src/data/features.ts`, `README.md` (integrations table), `CHANGELOG.md`

- [ ] **Step 1: CHANGELOG** — under `## [Unreleased]` → `### Added`:

```markdown
- **lazysql plugin** — database TUI (MySQL, PostgreSQL, SQLite, MSSQL) as a
  built-in pane type. Binary-gated (greyed in Ctrl+N with a homepage link when
  `lazysql` is not on `PATH`), opens lazysql's own connection manager, with an
  optional read-only toggle. Cross-platform; re-runs on daemon restart.
```

- [ ] **Step 2: `site/src/data/plugins.ts`** — add a `lazysql` `PluginEntry` mirroring the k9s entry (slug, name, `spawnExample` with the TOML above, honest feature bullets: normal pane, connection manager, read-only toggle, binary-gated, **note that connection selection + credentials stay in lazysql**). No fabricated screenshot URL.

- [ ] **Step 3: `site/src/data/features.ts`** — add a `lazysql` showcase `Feature` (category `extensibility`, an existing `IconName` such as `"layers"` or `"terminal"`, no `image`).

- [ ] **Step 4: `docs/features.md`** — add a short "lazysql integration" subsection after the k9s one, plus its TOC entry.

- [ ] **Step 5: `README.md`** — add a row to the Built-in integrations table:

```markdown
| **lazysql** | Database TUI ([lazysql](https://github.com/jorgerojas26/lazysql)) for MySQL, PostgreSQL, SQLite, and MSSQL. |
```

- [ ] **Step 6: Build the site** — `npm run build` in `site/` → 0 errors. Add any new icon to both `IconName` and `Icon.astro` only if you didn't reuse an existing one.

- [ ] **Step 7: Commit**

```bash
git add docs/features.md site/src/data/plugins.ts site/src/data/features.ts README.md CHANGELOG.md
git commit -m "docs: document lazysql built-in plugin"
```

## Final verification

- [ ] `./scripts/dev.sh test` — full suite green.
- [ ] `./scripts/dev.sh vet` — clean.
- [ ] `npm run build` in `site/` — 0 errors.
- [ ] `grep` the diff for AI-vendor mentions — must be zero.
- [ ] Manual: lazysql pane opens its connection manager when installed; greyed with link when absent.

## Notes for the implementer

- **No discovery, no dependency, no dialog changes.** If you find yourself reading
  `~/.config/lazysql/config.toml` or injecting a connection into argv, STOP — that
  is the rejected credential-leaking path from the spec.
- **Plugin-reference.md needs no change** — lazysql adds no new schema field
  (`homepage`/`prompts_cwd`/toggles/`rerun` are all already documented).
- This is a clean, releasable change on its own; it has no effect until `lazysql`
  is installed.
