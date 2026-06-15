# lazysql Integration — Design Spec

**Date:** 2026-06-15
**Status:** Approved for planning
**Scope:** One deliverable — a `lazysql` built-in plugin (Tier A, TOML-only). A connection-picker tier is explicitly **rejected** on security grounds (see §Rejected).

## Problem

Database work is a daily activity for the developers Quil targets. [lazysql](https://github.com/jorgerojas26/lazysql)
is a mature cross-platform TUI for MySQL, PostgreSQL, SQLite, and MSSQL (MongoDB
WIP). It is a single static Go binary, so it fits Quil's typed-pane model exactly
as lazygit and k9s do.

The feature must be inert when the `lazysql` binary is not installed.

## Verified facts (from the lazysql source/README)

- **Distribution:** single static Go binary; Homebrew, `go install`, AUR, GitHub
  release binaries. Windows/macOS/Linux. → uniform PATH detection.
- **CLI:** `lazysql` (no args) opens a **connection-manager UI**; `lazysql <arg>`
  connects directly; flags `--read-only`, `-config <path>`, `-version`,
  `-loglevel`, `-logfile` (Go `flag` package — accepts `-x` and `--x`).
- **The positional arg is a full DSN, not a connection name.** Verified in
  `components/arg_connection.go`: `InitFromArg` calls
  `helpers.ParseConnectionString(arg)` and stores `URL: connectionString`. There
  is no saved-connection-name lookup path.
- **Config:** TOML at `~/.config/lazysql/config.toml` (Linux),
  `~/Library/Application Support/lazysql/config.toml` (macOS),
  `%APPDATA%\lazysql\config.toml` (Windows). Stores connection **URLs with
  embedded credentials**, with optional `${env:VAR}` substitution to keep
  passwords out of the file.
- Full-screen TUI, vim keybindings, `Ctrl+E` SQL editor, multi-tab.

## Decisions (made during analysis)

| Question | Decision |
|---|---|
| Presentation | **Normal pane**, not an overlay. A DB session is long-lived, split alongside other panes. |
| Scope | **Tier A only** — a TOML plugin that launches lazysql's own connection-manager UI. |
| Connection picker (Quil-side) | **Rejected** — see §Rejected. lazysql handles connection selection + credentials itself. |
| CWD prompting | `prompts_cwd = false` — lazysql is connection-scoped; connections (incl. SQLite file paths) live in lazysql's own config, not the CWD. |
| Read-only safety | `--read-only` offered as an opt-in toggle (default off). |
| Persistence | `rerun` (relaunch → connection manager), `ghost_buffer = false` (full-screen TUI). |
| Discoverability | Reuse the existing greyed-when-missing UX + `homepage` field (shipped with k9s). |

## The plugin

New `internal/plugin/defaults/lazysql.toml`, auto-embedded and written by
`EnsureDefaultPlugins`:

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
# prompt and no Quil-side connection picker (see the design spec for why).
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

- **Binary gating is free** via the existing 3-tier `DetectAvailability`. When
  absent, the entry shows greyed in Ctrl+N with the homepage link (the behavior
  added alongside k9s) — not hidden.
- **Zero new Go code, zero new dependency.** Tier A is the embedded TOML plus
  CHANGELOG / docs / site entries.
- Normal-pane semantics throughout (PTY, ring buffer, splits, focus, resize).
  lazysql drives its own vim keys + `Ctrl+E`; all keys route to the PTY.

## Rejected: a Quil-side connection picker (the "Tier B" that k9s had)

k9s got a `discover = "kube"` context picker because a kube context name is a
**non-secret** identifier that can be passed as `--context <name>` (argv, no
shell, no credentials). lazysql is fundamentally different:

- The **only** launch argument lazysql accepts is a **full DSN**
  (`ParseConnectionString`), which embeds the password for MySQL/Postgres/MSSQL.
- Passing a DSN as a process argument leaks the credential: it is visible in
  `ps`/Task Manager, in `/proc/<pid>/cmdline`, and in Quil's MCP interaction
  logs. Quil's redaction covers `send_to_pane`/`send_keys` payloads, **not**
  spawn argv.
- lazysql exposes **no** connection-*name* launch path, so there is no safe
  identifier to inject (the way `--context <name>` was for k9s).

Therefore Quil must **not** read lazysql's config and inject connections. The
secure path is lazysql's own connection manager, which owns the credentials and
already supports `${env:VAR}` substitution to keep secrets out of its config.
This rejection is recorded so a future contributor doesn't add a
credential-injecting picker "for parity with k9s."

## Testing

- Plugin TOML: extend `defaults_test.go` with a `TestEnsureDefaultPlugins_WritesLazysql`
  (name/cmd/detect, `prompts_cwd == false`, `strategy == "rerun"`,
  `ghost_buffer == false`, one `read_only` toggle, `homepage` set), mirroring the
  k9s/lazygit tests.
- Update `TestDefaultPluginTOMLFiles` to expect 6 default files (add `lazysql.toml`).

## Out of scope (v1)

- **Quil-side connection picker / DSN injection** — rejected above.
- **Reading or parsing lazysql's config** (credentials) — never.
- **`-config <path>` selection** in a setup dialog — the default config is fine;
  power users edit the TOML's `args`.
- **Overlay** (an Alt-key toggle like lazygit) — lazysql is a persistent pane.
