---
headline: Sandbox panes can run in a Docker Sandboxes microVM
---
- **A second sandbox backend: Docker Sandboxes (`sbx`).** A sandbox pane can now
  run its agent inside an `sbx` microVM instead of a plain container, with its own
  kernel and a default-deny network allowlist. The cage is declared by your
  project's own sbx environment file; a `.quil-sandbox.toml` in the project names
  it, plus an optional host step before the sandbox is created and optional
  set-up steps inside it afterwards. Quil uses a profile only after you approve it
  with `quil sandbox trust`, and refuses again if any covered file changes; the
  approval refuses symbolic links, and the host step and `sbx env create` run
  from private copies of the approved files, so a change made after the check
  cannot reach your machine or the cage. Pick
  it with `←`/`→` on the `Ctrl+N` sandbox row, `[sandbox] backend = "sbx"`, or
  MCP `create_pane` with `sandbox_backend = "sbx"`. A repository and all its
  worktrees share one sandbox: Quil adds the repository's worktrees folder to the
  running sandbox with `sbx mount`, so a new worktree is visible at once without
  a restart. A pane can also attach to an existing sandbox that another tool
  created and owns: pick it with `←`/`→` (only sandboxes that mount the pane
  folder are offered) or send `sandbox_sbx_name` from MCP. Quil then needs no
  profile, and never creates, changes or removes that sandbox. sbx panes have no
  notifications or session resume yet.
