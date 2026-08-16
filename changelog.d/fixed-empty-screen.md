---
headline: Panes and overlays say what they are waiting for
---
- **A pane being created no longer waits behind a blank rectangle.** Opening
  lazygit or hunk, and creating a pane on a new worktree, each left a black area
  on screen for as long as the tool or the checkout took — with nothing to say
  whether anything was happening at all.

  Overlay panes now show the same boot indicator every other new pane has always
  shown, and the "Creating worktree *branch*" box appears the moment the request
  leaves rather than whenever the daemon next happened to broadcast — which,
  during a `git worktree add`, was usually not until the checkout had already
  finished. A create with no worktree names the pane it is starting instead of
  rendering nothing, which matters most over `--remote`, where an ordinary
  create is not instant either.
