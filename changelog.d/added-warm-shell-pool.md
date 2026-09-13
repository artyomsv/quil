---
headline: Ctrl+N/Ctrl+T can open a pane on a pre-warmed shell
---
- **New terminal panes can skip full shell startup.** The daemon now keeps a
  small pool of pre-spawned, shell-integration-armed shells; `Ctrl+N`/`Ctrl+T`
  claims one instead of spawning fresh when a match is ready, silently moving
  it to the pane's working directory before handoff.

  Configurable via `warm_shell_pool_size` in `config.toml` (default `1`, `0`
  disables pooling). Falls back to the existing spawn path with no added
  latency whenever the pool is empty, disabled, or doesn't apply — sandboxed
  panes, non-terminal panes (`claude-code`, `codex`, etc.), and workspace
  restore always use the normal path.
