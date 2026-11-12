---
headline: MCP agents no longer lose their Quil connection
---
- **`screenshot_pane` no longer breaks an agent's connection to Quil.** A
  screenshot of a pane whose output held a terminal query (Claude Code sends
  several on every start) never answered, and every later MCP call from the
  same agent timed out until its pane was restarted. Screenshots now answer,
  and the connection keeps working.
- **Worktree tabs and panes asked for at the same time are all created.** Quil
  checks out one worktree at a time, and a second request used to fail at
  once with "another worktree is being created". It now waits for the one in
  progress (up to two minutes) and then runs.
- **A delegated task that never starts no longer blocks its pane.** A task
  whose prompt the target agent never picked up stayed open forever, and every
  later `delegate_task` to that pane was refused. It now fails after three
  minutes with a reason. Delegating to a pane that is not running yet starts
  it and asks you to try again once it is ready, instead of typing the prompt
  into an agent that is still loading.
