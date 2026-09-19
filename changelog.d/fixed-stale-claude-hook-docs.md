---
headline: Hook troubleshooting docs match the real hook again
---
- **The "Claude Code session doesn't resume" runbook described a hook that no longer
  exists.** It told you to look for `quil-session-hook.sh` / `.ps1` in
  `~/.quil/claudehook/` and to restart the daemon if they were missing. Those scripts were
  replaced by the `quild claude-hook` subcommand in v1.18.0, so the directory is not
  created at daemon start and the files never appear — on a healthy install.

  Following the old steps led to the conclusion that the hook had never installed, when in
  fact nothing was wrong with the install. The runbook now checks
  `~/.quil/sessions/<pane-id>.settings.json` (hook registered) and `<pane-id>.id` (hook
  fired), and says plainly that a missing `claudehook/` directory is not a fault.

  The same stale description is corrected in the feature docs, the roadmap and the
  architecture file tree.
