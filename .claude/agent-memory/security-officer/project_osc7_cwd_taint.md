---
name: osc7-cwd-taint-source
description: pane.CWD in quil is attacker-influenced (OSC7 from PTY output) and flows TUI→daemon→workspace.json; treat as untrusted input in security reviews
metadata:
  type: project
---

In quil, a pane's `CWD` is **attacker-influenceable untrusted input**, not a trusted value.

Chain (verified 2026-06-12 during lazygit-integration review):
- Any process in a pane can emit an OSC 7 escape sequence; the VT callback sets `internal/tui/pane.go` `p.CWD = parseOSC7Path(dir)`. `parseOSC7Path` returns the raw string unchanged for non-`file://` input — no validation.
- The TUI forwards it to the daemon via `MsgUpdatePane{CWD}` (`internal/tui/model.go` `updatePaneCWD`), and `daemon.go handleUpdatePane` stores `pane.CWD = payload.CWD` verbatim, then persists to `workspace.json`.
- Spawn paths (`handleCreatePane`, `defaultCWD`) DO re-validate with `os.Stat` + `filepath.EvalSymlinks`, so this is not RCE today. The lazygit feature's `gitdiscover.Candidates(pane.CWD)` does NOT have that guard and probes the path directly (`os.Stat`/`os.ReadDir`) → Windows UNC/device-path SMB-leak vector (finding security/H-1).

**Why:** the OSC7 producer is frequently a remote ssh session or an AI agent's tool output — outside the user's trust boundary.

**How to apply:** in any future review, whenever code consumes `pane.CWD` (or the daemon's persisted CWD) and hands it to a filesystem/network syscall WITHOUT the Stat+EvalSymlinks+UNC-rejection guard, flag it. The canonical safe gate is the one in `handleCreatePane`. UNC roots (`\\host\...`) and Windows device namespaces (`\\.\`, `\\?\`) are the dangerous shapes on Windows.
