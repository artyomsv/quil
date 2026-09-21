---
headline: Typing claude in a terminal pane opens a Claude Code pane
---
- **Starting `claude`, `codex` or `opencode` in a terminal pane now opens the
  pane as that agent, with the arguments you typed.** Before, a hand-started
  agent got no hook and no session tracking, so it came back as a fresh
  conversation after every restart and Quil never said so — the gap reported in
  #221, where four conversations were lost across a reboot and the logs looked
  identical to a healthy install.

  Nothing is killed to do it. Quil's shell integration shadows the binary with a
  function, so the daemon learns the command line before the binary starts and
  reopens the pane rather than interrupting it. The converted pane gets
  everything a `Ctrl+N` pane gets: hooks, session-id tracking through `/clear`
  and compaction, work indicators, notifications and resume. When the agent
  exits cleanly the pane goes back to being a terminal.

  It covers an interactive bash 4.1+, zsh or PowerShell prompt. Everywhere else
  the command runs exactly as typed, unchanged: fish, the macOS system bash,
  your own `claude` wrapper, `command claude`, pipelines, background jobs,
  non-session subcommands, and a launch carrying agent environment the daemon
  does not have. Where Quil declines to convert a Claude launch it still records
  the session, so the pane resumes that conversation after a restart.

  Set `[agents] hand_started` to `adopt`, `notify` or `off` to change or disable
  it; `command claude` bypasses it for one invocation.
