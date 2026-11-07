package hookevents

import "regexp"

// EventWorktreeProbe is the spool line an agent hook writes when the agent
// ran a command that may have created, removed or switched a git worktree or
// branch. It is a TRIGGER, not a record: the daemon answers it by comparing
// the repository's `git worktree list` and branches with what it saw before,
// so a probe that fires for nothing costs one listing and a missed one is
// caught by the compare at the end of the agent's turn.
//
// It never reaches the event queue or the TUI: the daemon's spool watcher
// takes it out before the coalescer (which is last-wins per pane and event
// name, and would drop the first of two probes in one tick).
const EventWorktreeProbe = "WorktreeProbe"

// DataProbeTrigger is the one data key a probe carries: what kind of command
// fired it. The command text itself is never sent — a shell command can
// carry a credential, and the compare needs only the directory.
const DataProbeTrigger = "trigger"

// Probe triggers.
const (
	ProbeShell    = "shell"    // a shell command that matched WorktreeCommandPattern
	ProbeEnter    = "enter"    // Claude's EnterWorktree tool
	ProbeExit     = "exit"     // Claude's ExitWorktree tool
	ProbeSubagent = "subagent" // a subagent started in its own directory
)

// MaxProbeDirBytes bounds the directory a probe names (Payload.CWD). A longer
// one is DROPPED by the producer, never cut: a truncated path names another
// directory.
const MaxProbeDirBytes = 1024

// WorktreeCommandPattern matches a shell command that can create, remove or
// switch a worktree or a branch. Matched case-insensitively anywhere in the
// text, so `cd x && git worktree add …` and a script naming it both match.
// Over-matching is cheap (one listing); under-matching is covered by the
// turn-end compare.
//
// Written WITHOUT an inline (?i) flag so the OpenCode plugin can use the same
// text as a JavaScript regex literal (/…/i): JavaScript has no inline flags.
// TestOpencodeScript_ProbeRegexMatchesGo keeps the two copies equal.
const WorktreeCommandPattern = `worktree|git\s+branch\s|checkout\s+-[bB]|switch\s+-[cC]|push\b.*(--delete|\s:)`

var worktreeCommandRe = regexp.MustCompile(`(?i)` + WorktreeCommandPattern)

// IsWorktreeCommand reports whether a shell command should fire a probe.
func IsWorktreeCommand(cmd string) bool {
	return worktreeCommandRe.MatchString(cmd)
}
