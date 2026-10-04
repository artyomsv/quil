---
headline: Sandbox panes can sign in once and remember the image
---
- **Sign in once for all your sandbox panes.** The Claude Code sign-in row in the
  create dialog now offers **Shared** beside Browser and Token: every Shared pane
  uses one config directory, so you sign in through the browser once instead of
  for every new pane. The row says what it costs — Shared panes share hooks, MCP
  servers and history. The choice is recorded on each pane, so changing the
  default later never moves an existing pane's conversation. Browser stays the
  default.
- **The image is remembered.** The create dialog fills in the last sandbox image
  you used on each host, so `quil-sandbox:latest` no longer has to be typed for
  every pane.
- **F1 → Settings → Sandbox** holds the defaults: the image for a host's first
  sandbox pane and the default sign-in, each with its warning. The dialog sends
  the choice it shows, so the defaults apply at once and to remote projects too.
- **Codex and OpenCode containers no longer get the shared Claude directory.**
  With `shared_claude_config = true` they were given write access to it, so they
  could plant a hook that shared Claude panes then ran.
- MCP `create_pane` accepts `sandbox_claude_config` (`own` or `shared`).
