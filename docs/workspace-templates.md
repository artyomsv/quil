# Workspace templates

A template creates a tab with named panes, an initial layout, and optional starting prompts. Quil sets up the workspace; the prompts and the people or agents using it decide how the work proceeds.

## Create a workspace

Open the command palette and choose **New from template**. The dialog has four rows:

1. **Template** — use Left/Right to choose a template and see its description.
2. **Task** — optional multiline text to include in prompts.
3. **Directory** — a directory browser, the same one the Ctrl+N pane dialog uses. It opens at the active project's root. Up/Down move, Enter descends, Left or Backspace goes up, and Ctrl+V pastes a path to jump to. On Windows, going up from a drive shows the drive list. The directory it is showing is the one the tab is created in.
4. **New branch** — leave empty to work in the chosen directory, or enter a branch name to create a worktree.

Tab/Shift+Tab move between rows. Ctrl+S creates the tab from any row, and Enter creates it from every row except **Task**, where Enter adds a line, and **Directory**, where Enter descends into the highlighted folder. Escape cancels. Creating is refused for as long as a directory listing is still loading, so the tab cannot be created against a directory other than the one on screen. Pasted text belongs to the focused field; the template selector and the directory browser consume paste without sending it to a pane. Browsing and creation stay on the host and project selected when the dialog opened. A successful request focuses the returned tab in the requesting client only.

A branch request first shows a preparing pane while the checkout runs. The final layout is built after all panes exist and the main pane is known. The layout then behaves like an ordinary tab: resize its borders as needed, and the saved tree survives later template edits.

## Edit the file

Templates live in `$QUIL_HOME/templates.toml`. F1 → Settings → **Templates** opens that local file in the existing TOML editor. A missing file starts from the embedded defaults without writing anything until you save. Ctrl+S validates the entire document and writes it atomically. Invalid syntax or template settings leave the editor open with the error visible and the previous file intact. Comments and prompt formatting are preserved. A successful save requests a reload from the local daemon; templates are also loaded afresh on every creation request.

The dialog's template catalog is local. A remote daemon uses its own templates file when creating panes, so install matching templates on that host if using custom templates remotely. Discovery and creation do not copy configuration files between machines.

```toml
[[templates]]
name = "shell-pair"
description = "Two shells for editing and tests"
layout = "columns"

  [[templates.panes]]
  type = "terminal"
  name = "edit"
  main = true

  [[templates.panes]]
  type = "terminal"
  name = "tests"
  cwd = "tests"
```

Each `[[templates]]` entry requires a unique `name`: lowercase letters, digits and hyphens, starting with a letter, at most 32 characters. `description` is an optional single line. `layout` defaults to `rows`. Each template contains one to eight `[[templates.panes]]` entries, created in listed order.

## Pane fields

| Field | Meaning |
|---|---|
| `type` | Required plugin name: terminal, an AI plugin, or another available plugin. |
| `name` | Optional pane name; otherwise the plugin supplies it. |
| `model` | Optional model ID, passed as the agent's model argument; ignored for non-AI plugins. The ID's spelling is validated, not its availability at the provider. |
| `toggles` | Plugin toggle names, as returned by MCP `list_plugins`. Conflicting toggles are refused. AI plugins exposing a permission-mode group require an explicit selection from it. |
| `cwd` | Optional relative subdirectory. Absolute paths, traversal, missing directories and symlink escapes are refused. With a branch, the first pane is validated against the new checkout. |
| `prompt` | Optional starting prompt, queued after every pane exists, in listed order. |
| `muted` | Use the existing pane mute to suppress its notifications. |
| `main` | Layout anchor for main-left/main-top; at most one pane may set it. Defaults to the first pane. |
| `quil_mcp` | Register Quil's ordinary MCP server for this pane. Supported for Claude Code, Codex and OpenCode. |

Toggle and model arguments are frozen into the pane at creation, so a later file edit does not change an existing pane's restart arguments. Terminal-control characters in names, prompts and directories are refused. Plugin existence, availability and toggle validity are checked on the destination daemon.

## Layouts

| Keyword | Initial shape |
|---|---|
| `rows` | Every pane stacked top to bottom; the default. |
| `columns` | Every pane side by side. |
| `main-left` | Main pane full height on the left; remaining panes stacked on the right. |
| `main-top` | Main pane full width on top; remaining panes side by side below. |
| `grid` | Two columns filled top to bottom; an odd last pane spans both columns underneath. |

`main` chooses placement independently of creation/prompt order. Put an orchestrator last when it should receive its brief after its teammates, and mark it as main when it should have the large anchor region.

## Prompt placeholders

| Placeholder | Substitution |
|---|---|
| `{{task}}` | The dialog's free text; empty when omitted. |
| `{{dir}}` | Absolute workspace directory. |
| `{{branch}}` | Requested branch; empty when none. |
| `{{panes}}` | One line per pane in this tab: name, pane ID and plugin type. |

Substitution is a single pass, so placeholder-like text inside the task remains literal. Prompt delivery means queued, not completed; Quil does not supervise the agents' subsequent work or interpret their answers.

## MCP

`create_from_template` accepts `template` (required), `task`, `cwd`, `branch`, `project_id`, and the normal optional `host` selector. It returns `tab_id` and the pane IDs that currently exist. Branch creation returns `preparing_worktree` and a placeholder ID immediately; later workspace state contains the completed panes. The MCP call does not switch TUI focus. The destination daemon must be version **1.74.0 or newer**; older released project/tab/task tools retain their 1.72.0 floor. See the [MCP guide](mcp.md).

Unknown templates, invalid settings and unusable directories are refused. Validation of a first-pane subdirectory after checkout can fail asynchronously: the new checkout and provisional tab are removed, and a named error event is emitted. A failed process or prompt delivery is reported on its pane; it never silently launches at a fallback directory.

## Shipped templates and limits

- **agent-team** — analyst (Claude Code), developer (Codex), and an orchestrator (Claude Code) created last and marked main, using main-left. The orchestrator gets Quil MCP and the pane roster; teammates are muted. Its prompts assign git and user communication to the orchestrator and ask workers for file reports.
- **pair** — Claude Code and Codex side by side, without starting prompts.
- **review** — lazygit and Claude Code in rows, with a working-tree review prompt.

Two limits matter when writing team prompts. **A Codex pane's output cannot be read as its answer**: scrollback and captured screen output can contain spinner animation instead of the result. A prompt that needs a reliable answer should ask for a file.

**A restricted tool set is a guard rail, not a security boundary against an agent with shell access.** Template panes receive the ordinary Quil server. Claude uses `--strict-mcp-config`; Codex probes effective server names, disables inherited servers and registers a separate bridge; OpenCode can retain other configured servers. None of these settings confines what an agent can execute in its shell.
