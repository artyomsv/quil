# Plugin Configuration Reference

Quil supports custom pane types via TOML plugin files. This document covers every configuration option, constraint, and behavior so you can create your own plugins.

## Overview

Plugins define how panes are spawned, persisted, and restored. Each plugin is a single `.toml` file placed in the plugins directory.

| Item | Details |
|------|---------|
| **File location** | `~/.quil/plugins/*.toml` (or `$QUIL_HOME/plugins/`) |
| **Loading** | On daemon startup; hot reload via F1 → Plugins → Reload |
| **Defaults** | 3 built-in TOML plugins (claude-code, ssh, stripe) are written on first run and never overwritten — your edits are preserved across upgrades |
| **Built-in** | The `terminal` plugin is defined in Go (not TOML) because it requires runtime shell detection |

## Quick Start

Save this as `~/.quil/plugins/htop.toml`:

```toml
[plugin]
name = "htop"
display_name = "System Monitor"
category = "tools"
description = "Interactive process viewer"

[command]
cmd = "htop"
```

Then press **F1 → Plugins → Reload** (or restart the daemon). Open with **Ctrl+N** → Tools → System Monitor.

---

## Plugin Metadata — `[plugin]`

```toml
[plugin]
name = "my-tool"
display_name = "My Tool"
category = "tools"
description = "A short description of what this plugin does"
```

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `name` | string | **Yes** | — | Unique plugin identifier. Should match the filename (e.g., `my-tool.toml` → `name = "my-tool"`). Used internally for pane type tracking. |
| `display_name` | string | No | Value of `name` | Human-readable label shown in the Ctrl+N creation dialog and pane borders. |
| `category` | string | No | `"tools"` | Groups the plugin in the creation dialog. |
| `description` | string | No | `""` | One-line description shown in plugin lists. |

### Categories

| Value | Label in UI | Use for |
|-------|-------------|---------|
| `"terminal"` | Terminal | Shell-based plugins |
| `"ai"` | AI Assistant | AI coding tools, chat interfaces |
| `"tools"` | Tools | Utilities, monitors, webhook listeners |
| `"remote"` | Remote | SSH, remote connections |

Categories control display order in the Ctrl+N dialog: Terminal → AI Assistant → Tools → Remote.

---

## Command Configuration — `[command]`

```toml
[command]
cmd = "ssh"
args = ["-o", "ServerAliveInterval=60"]
env = ["TERM=xterm-256color"]
detect = "ssh -V"
arg_template = ["-p", "{port}", "{user}@{host}"]
```

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `cmd` | string | **Yes** | — | Binary name or absolute path. Resolved via PATH at runtime (`exec.LookPath`). |
| `args` | string[] | No | `[]` | Default arguments passed every time the plugin is launched. Overridden when instance-specific args are provided. |
| `env` | string[] | No | `[]` | Environment variables as `KEY=VALUE` pairs. Merged into the PTY process environment. |
| `detect` | string | No | Value of `cmd` | Command used to check if the tool is installed. Only the **first word** is used for PATH lookup (e.g., `"ssh -V"` checks for `ssh`). |
| `shell_integration` | bool | No | `false` | **Reserved for the built-in terminal plugin.** Injects OSC 7 directory tracking hooks. Has no effect in user TOML plugins. |
| `arg_template` | string[] | No | `[]` | Template arguments with `{placeholder}` tokens. Expanded from form field values when creating an instance. See [Template Expansion](#template-expansion). |

### Form Fields — `[[command.form_fields]]`

Define user-fillable fields for the instance creation dialog (Ctrl+N → select plugin → fill form).

```toml
[[command.form_fields]]
name = "host"
label = "Hostname"
required = true

[[command.form_fields]]
name = "port"
label = "Port"
default = "22"
```

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `name` | string | **Yes** | — | Placeholder key used in `arg_template` expansion. Must be unique within the plugin. |
| `label` | string | No | `""` | Display label shown next to the input field. |
| `required` | bool | No | `false` | If `true`, the form cannot be submitted until this field has a value. Shown with `*` in the UI. |
| `default` | string | No | `""` | Pre-filled value shown in the input field. |

If a plugin defines no `form_fields`, the instance creation step is skipped — the pane is created directly with the plugin's default `args`.

### Template Expansion

The `arg_template` field supports `{fieldname}` placeholders that are replaced with form field values at pane creation time.

**Example:**

```toml
arg_template = ["-p", "{port}", "{user}@{host}"]
```

With form values `port = "2222"`, `user = "admin"`, `host = "example.com"`:

```
Result: ["-p", "2222", "admin@example.com"]
```

These expanded args become the pane's `InstanceArgs` — they override the plugin's default `args` and are saved for `rerun` resume strategy.

---

## Persistence Configuration — `[persistence]`

Controls how panes survive daemon restarts.

```toml
[persistence]
strategy = "rerun"
ghost_buffer = true
start_args = ["--session-id", "{session_id}"]
resume_args = ["--resume", "{session_id}"]
```

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `strategy` | string | No | `""` (none) | Resume mechanism. See [Strategy Reference](#strategy-reference). |
| `ghost_buffer` | bool | No | `true` | If `true`, PTY output is saved to disk and replayed on reconnect (shows previous terminal content with a dimmed "restored" label). Set to `false` for TUI apps that manage their own display. |
| `start_args` | string[] | No | `[]` | Template arguments appended on **fresh** pane creation. `{key}` placeholders expanded from plugin state (e.g., generated UUIDs). Used with `preassign_id` strategy. |
| `resume_args` | string[] | No | `[]` | Template arguments used when **restoring** a pane after daemon restart. `{key}` placeholders expanded from previously scraped state. If any placeholder cannot be resolved, the pane starts fresh instead. |

### Scrape Patterns — `[[persistence.scrape]]`

Extract state values from PTY output using regex patterns. Scraped values are persisted to disk and available for resume arg expansion.

```toml
[[persistence.scrape]]
name = "session_id"
pattern = 'Session ID: ([a-f0-9-]+)'
```

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `name` | string | **Yes** | — | Key name for the scraped value (e.g., `"session_id"`). Used as `{name}` in `start_args`/`resume_args` templates. |
| `pattern` | string | **Yes** | — | Go regex pattern with **exactly one capture group**. The first submatch is stored as the value. Matched continuously against all PTY output. |

**How scraping works at runtime:**
1. Every chunk of PTY output is tested against all scrape patterns
2. When a pattern matches, the captured value is stored in the pane's plugin state
3. Plugin state is persisted to `workspace.json` on each snapshot
4. On daemon restart, scraped values are loaded and available for `resume_args` expansion

**Regex tips:**
- Use raw TOML strings (`'...'`) to avoid escaping backslashes
- Only the **first** capture group is used — keep patterns simple
- Invalid patterns are logged at startup but don't prevent the plugin from loading
- Patterns are pre-compiled for performance (not re-compiled on each match)

### Strategy Reference

| Strategy | On fresh start | On restore after daemon restart | Best for |
|----------|---------------|---------------------------------|----------|
| `""` / `"none"` | Start `cmd` + `args` | Start `cmd` + `args` (no state) | Stateless tools, system monitors |
| `"cwd_only"` | Start in current directory | Restore saved CWD, start fresh shell | Terminal shells (built-in only) |
| `"rerun"` | Start `cmd` + `instanceArgs` | Rerun `cmd` + saved `instanceArgs` | SSH connections, Stripe listeners |
| `"preassign_id"` | Generate UUID, store in plugin state, expand `start_args` | Expand `resume_args` using saved state | AI tools (Claude Code) |
| `"session_scrape"` | Start normally | Expand `resume_args` from scraped output values | Tools that emit session tokens |

**Strategy details:**

**`"none"` / `""`** — No persistence. The pane is recreated from scratch on every daemon restart. Suitable for disposable tools like system monitors.

**`"cwd_only"`** — Reserved for the built-in terminal plugin. Saves the current working directory (tracked via OSC 7 shell integration) and restores it on respawn. The shell itself starts fresh.

**`"rerun"`** — The simplest persistence for interactive tools. On restore, the exact same command and arguments are re-executed. The user's saved instance args (from the form) are preserved. Example: an SSH connection to `admin@prod-server:2222` is re-established automatically.

**`"preassign_id"`** — For tools that support session IDs. On first launch:
1. Quil generates a UUID
2. Stores it as `session_id` in plugin state
3. Expands `start_args` (e.g., `["--session-id", "{session_id}"]`)
4. Appends expanded args to the command

On restore:
1. Loads saved `session_id` from disk
2. Expands `resume_args` (e.g., `["--resume", "{session_id}"]`)
3. Uses expanded args instead of `start_args`

**`"session_scrape"`** — For tools that emit session tokens in their output. Scrape patterns continuously match PTY output and store extracted values. On restore, `resume_args` are expanded with the scraped values. If scraping never captured the required values, the pane starts fresh.

---

## Display Configuration — `[display]`

```toml
[display]
border_color = "blue"
dialog_width = 60
```

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `border_color` | string | No | `""` | Lipgloss terminal color for the pane border (e.g., `"blue"`, `"cyan"`, `"#ff5733"`). |
| `dialog_width` | int | No | `50` | Width (in characters) of the instance creation form dialog. Increase for plugins with long field labels. |

---

## Pre-configured Instances — `[[instances]]`

Ship the plugin with ready-to-use configurations. Users see these as selectable options in the Ctrl+N dialog.

```toml
[[instances]]
name = "staging"
display_name = "Staging Server"
args = ["-p", "2222", "deploy@staging.example.com"]

[[instances]]
name = "production"
display_name = "Production"
args = ["admin@prod.example.com"]
env = ["SSH_AUTH_SOCK=/run/ssh-agent.sock"]
```

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `name` | string | **Yes** | — | Unique identifier for this instance within the plugin. |
| `display_name` | string | No | `""` | Human-readable label shown in the instance selection list. |
| `args` | string[] | No | `[]` | Instance-specific command arguments. Override the plugin's default `args`. |
| `env` | string[] | No | `[]` | Instance-specific environment variables (`KEY=VALUE`). |

**Note:** Users can also create and save instances at runtime via the Ctrl+N form. These are stored in `~/.quil/instances.json` and appear alongside TOML-defined instances.

---

## Error Handlers — `[[error_handlers]]`

Match PTY output against regex patterns to show help dialogs or log errors.

```toml
[[error_handlers]]
pattern = 'Permission denied \(publickey'
title = "SSH Authentication Failed"
message = """
SSH key not configured for {host}.

1. Generate: ssh-keygen -t ed25519
2. Copy key: ssh-copy-id {user}@{host}
3. Retry the connection"""
action = "dialog"

[[error_handlers]]
pattern = "Connection refused|No route to host"
title = "Connection Failed"
message = "Cannot reach {host}. Check that the server is running."
action = "dialog"
```

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `pattern` | string | **Yes** | — | Go regex matched against PTY output. Use `\|` alternation for multiple patterns. Use raw TOML strings (`'...'`) to avoid double-escaping. |
| `title` | string | No | `""` | Title shown in the error dialog. |
| `message` | string | No | `""` | Body text of the error dialog. Supports `{host}`, `{user}`, `{port}` placeholder expansion from the pane's instance args. Use TOML multi-line strings (`"""..."""`) for longer messages. |
| `action` | string | No | `"log"` | What to do when the pattern matches. |

### Actions

| Value | Behavior |
|-------|----------|
| `"dialog"` | Show a modal error dialog in the TUI with `title` and `message`. |
| `"log"` | Log the match to the daemon log file (`~/.quil/quild.log`). No visible UI. |

Invalid action values are logged as a warning and treated as `"log"`.

### Message Placeholders

Error messages support these placeholders, extracted from the pane's instance args:

| Placeholder | Source | Example |
|-------------|--------|---------|
| `{host}` | Hostname from `user@host` pattern in args | `example.com` |
| `{user}` | Username from `user@host` pattern in args | `admin` |
| `{port}` | Port from `-p <port>` flag or `host:port` in args | `2222` |

---

## Cross-Platform Notes

TOML plugin files are **fully portable** — the same `.toml` works on Linux, macOS, and Windows without modification.

| Aspect | Behavior |
|--------|----------|
| **Binary resolution** | `cmd` and `detect` are resolved via `exec.LookPath` at runtime. Use the binary name (e.g., `"ssh"`), not an absolute path. |
| **Windows `.exe`** | Not needed — Go automatically appends `.exe` during PATH lookup on Windows. |
| **Detection** | `detect` runs the first word through PATH lookup (e.g., `"ssh -V"` checks if `ssh` is on PATH). |
| **Environment vars** | `env` entries use `KEY=VALUE` format on all platforms. |
| **PTY** | Unix uses `creack/pty`, Windows uses ConPTY. Transparent to plugins. |
| **Shell integration** | Reserved for the built-in terminal plugin. Handles bash, zsh, PowerShell, and fish automatically. |

---

## Validation Rules

These rules are enforced when loading a TOML plugin file:

| Rule | Behavior on violation |
|------|-----------------------|
| `name` is required | Plugin file skipped with error log |
| `cmd` is required | Plugin file skipped with error log |
| `strategy` must be a valid value | Plugin file skipped with error log |
| Invalid regex in `pattern` | Pattern skipped with warning log; plugin still loads |
| Invalid `action` value | Defaults to `"log"` with warning log |
| Missing `display_name` | Defaults to `name` |
| Missing `category` | Defaults to `"tools"` |
| Missing `ghost_buffer` | Defaults to `true` |

---

## Plugin Lifecycle

```
Daemon startup
  ├── Write default TOML files (if missing)
  ├── Load built-in terminal plugin (Go)
  ├── Load all *.toml from ~/.quil/plugins/
  │     ├── Parse TOML → validate → compile regex patterns
  │     └── TOML plugins override built-ins with same name
  ├── Detect availability (exec.LookPath on each binary)
  └── Restore workspace (load panes, ghost buffers, plugin state)

User creates pane (Ctrl+N)
  ├── Dialog: Category → Plugin → Instance/Form → Split Direction
  ├── Expand arg_template from form field values
  └── Send to daemon: type, instanceName, instanceArgs

Daemon spawns pane
  ├── Look up plugin in registry
  ├── Apply instance args (override default args)
  ├── If preassign_id: generate UUID, expand start_args
  ├── Merge plugin env vars into PTY process
  ├── Resolve cmd to absolute path via exec.LookPath
  └── Start PTY process → begin output streaming

Pane running
  ├── PTY output → scrape patterns → store matches in plugin state
  ├── PTY output → error patterns → show dialog if matched
  └── Periodic snapshots save plugin state to disk

Daemon restart
  ├── Load workspace + plugin state from disk
  ├── For each pane, dispatch by strategy:
  │     ├── rerun → same cmd + saved args
  │     ├── preassign_id → expand resume_args from state
  │     ├── session_scrape → expand resume_args from scraped values
  │     └── none → start fresh
  └── Replay ghost buffers to reconnecting clients

Hot reload (F1 → Plugins → Reload)
  ├── Re-read all *.toml files
  ├── Re-compile regex patterns
  └── Re-detect availability
```

---

## Complete Examples

### Example 1: Minimal Plugin

A system monitor with no persistence or forms — just run a binary.

```toml
[plugin]
name = "htop"
display_name = "System Monitor"
category = "tools"
description = "Interactive process viewer"

[command]
cmd = "htop"
```

### Example 2: Remote Connection with Forms

A full-featured plugin with instance forms, error handlers, and persistence.

```toml
[plugin]
name = "ssh"
display_name = "SSH"
category = "remote"
description = "Remote SSH connection"

[command]
cmd = "ssh"
detect = "ssh -V"
arg_template = ["-p", "{port}", "{user}@{host}"]

# --- Form fields shown when creating a new instance ---

[[command.form_fields]]
name = "name"
label = "Connection Name"
required = true

[[command.form_fields]]
name = "host"
label = "Hostname"
required = true

[[command.form_fields]]
name = "user"
label = "Username"
required = true

[[command.form_fields]]
name = "port"
label = "Port"
default = "22"

[[command.form_fields]]
name = "description"
label = "Description"

# --- Visual settings ---

[display]
dialog_width = 60

# --- Resume after daemon restart ---

[persistence]
strategy = "rerun"
ghost_buffer = true

# --- Help dialogs for common errors ---

[[error_handlers]]
pattern = 'Permission denied \(publickey'
title = "SSH Authentication Failed"
message = """
SSH key not configured for {host}.

1. Generate:  ssh-keygen -t ed25519
2. Copy key:  ssh-copy-id {user}@{host}
3. Retry the connection"""
action = "dialog"

[[error_handlers]]
pattern = "Host key verification failed"
title = "Unknown Host"
message = "Run: ssh-keyscan {host} >> ~/.ssh/known_hosts"
action = "dialog"

[[error_handlers]]
pattern = "Connection refused|No route to host"
title = "Connection Failed"
message = "Cannot reach {host}. Check that the server is running and the address is correct."
action = "dialog"

# --- Pre-configured servers ---

[[instances]]
name = "staging"
display_name = "Staging Server"
args = ["-p", "2222", "deploy@staging.example.com"]

[[instances]]
name = "production"
display_name = "Production"
args = ["admin@prod.example.com"]
```

### Example 3: AI Tool with Session Resume

A plugin that generates a session ID, passes it to the tool, and resumes the session after restart.

```toml
[plugin]
name = "my-ai-tool"
display_name = "My AI Tool"
category = "ai"
description = "AI assistant with session persistence"

[command]
cmd = "my-ai-tool"
detect = "my-ai-tool --version"

[persistence]
strategy = "preassign_id"
ghost_buffer = false
start_args = ["--session-id", "{session_id}"]
resume_args = ["--resume", "{session_id}"]

[[error_handlers]]
pattern = '(?i)API.?key.*not (found|set)|authentication.*failed'
title = "Authentication Required"
message = "Set MY_AI_TOOL_API_KEY in your environment."
action = "dialog"
```

**How it works:**

1. **First launch:** Quil generates a UUID (e.g., `a1b2c3d4-...`), stores it as `session_id`, and starts: `my-ai-tool --session-id a1b2c3d4-...`
2. **Daemon restart:** Quil loads the saved `session_id` from disk and starts: `my-ai-tool --resume a1b2c3d4-...`
3. **`ghost_buffer = false`:** The tool's TUI output is not saved/replayed (the tool redraws its own interface on resume).

### Example 4: Webhook Listener

A simple tool with default args, a form for URL override, and auth error handling.

```toml
[plugin]
name = "stripe"
display_name = "Stripe"
category = "tools"
description = "Stripe webhook listener"

[command]
cmd = "stripe"
args = ["listen"]
detect = "stripe --version"
arg_template = ["listen", "--forward-to", "{url}"]

[[command.form_fields]]
name = "name"
label = "Name"
required = true

[[command.form_fields]]
name = "url"
label = "Forward URL"
required = true
default = "http://localhost:8080/webhook"

[[command.form_fields]]
name = "description"
label = "Description"

[persistence]
strategy = "rerun"
ghost_buffer = true

[[error_handlers]]
pattern = "not logged in|login required"
title = "Stripe Authentication Required"
message = "Run 'stripe login' in a terminal pane first."
action = "dialog"
```

---

## TOML Syntax Tips

- **Raw strings** (`'...'`): No escape processing. Use for regex patterns: `'Permission denied \(publickey'`
- **Multi-line strings** (`"""..."""`): For long error messages with line breaks.
- **Array of tables** (`[[section]]`): Each `[[command.form_fields]]` or `[[error_handlers]]` block defines one entry in the array.
- **Boolean values**: `true` / `false` (lowercase, no quotes).
- **Comments**: Lines starting with `#` are ignored.
