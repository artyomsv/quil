---
headline: Settings, file editors and logs come to the browser
---
- **Browser client (`quil web`), step 5d.** The browser now has:
  - F1 → Settings with General, Notifications, Sandbox and Keys; each change saves at once, and keys, notifications and sandbox defaults apply to the page without a reload;
  - editors for `templates.toml` and plugin files, which refuse to overwrite a file changed on disk unless you choose Overwrite;
  - the plugin update view for out-of-date plugin files, without blocking the page;
  - the client, daemon, MCP and web logs; What's New; and the beta notice.
  Writes and logs need a full-rights token. Under `--connect` these are the files of the machine where `quil web` runs.
- **A TUI no longer erases settings changed elsewhere when it exits.** It now writes only the settings it changed itself, so a change made in the browser or in another TUI meanwhile is kept.
- **Saving `config.toml` or `bindings.toml` from two programs at once no longer loses a save.** Each save uses its own temporary file.
