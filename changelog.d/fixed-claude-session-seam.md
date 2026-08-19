---
headline: Claude session resume and history work in more setups
---
- **Claude panes register their session hook reliably on Windows.** The hook
  settings were handed to `claude` as inline JSON. On Windows `claude` is an npm
  `.cmd` shim that Windows re-parses, splitting that JSON at the wrong quote
  boundaries — so the hook silently never registered, and a pane that lost it
  stopped tracking `/clear`, `/resume` and compaction, then came back to a stale
  conversation after a restart. The settings now go to a file and only its path
  is passed.

- **The session picker finds your sessions when `CLAUDE_CONFIG_DIR` is set.**
  Quil always looked in `~/.claude`, so anyone who relocates Claude's config
  directory saw an empty list — indistinguishable from having no sessions at
  all. Resuming a pane was never affected, only finding a session to resume.

- **Session titles and prompt counts no longer come back blank on some Claude
  builds.** The transcript reader required one specific field that not every
  build writes; those sessions still listed, but by ID with no title. It now
  falls back to the transcript's own title entry, and then to the shape of the
  entry itself.
