---
headline: Create a tab of named panes from a workspace template
---
- Choose **New from template** in the command palette to create ordered panes with frozen model/toggle arguments, starting prompts, and rows, columns, main-left, main-top or grid layouts. An optional branch creates a worktree; the layout waits for its completed panes.
- Edit `templates.toml` through F1 → Settings → Templates in the TOML editor. Invalid files stay open with a named error; valid saves preserve comments and formatting and replace the file atomically. Three templates ship: agent-team, pair and review.
- Agents can use `create_from_template` through Quil MCP. It requires daemon 1.73.0 while existing project/tab/task tools remain available against 1.72.0. Template panes can opt into the ordinary Quil server without changing global agent settings.
