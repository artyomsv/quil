package main

import (
	"os"
	"time"

	"github.com/artyomsv/quil/internal/claudehook"
	"github.com/artyomsv/quil/internal/config"
)

// runClaudeHook handles the `quild claude-hook` subcommand: it processes one
// Claude Code hook invocation (JSON on stdin) and writes the session-id file
// or a spool line under $QUIL_HOOK_HOME. It deliberately does NOT initialize
// the daemon, logger, or config-from-disk — this is the hot path Claude spawns
// once per hook event, so it must start fast. Errors are swallowed (RunHook
// logs them to the hook log) and the process always exits 0 via the caller.
//
// QUIL_PANE_ID and QUIL_HOOK_MODE are set by the daemon in the claude-code
// pane's environment; the data dir resolves via QUIL_HOOK_HOME with a
// QUIL_HOME fallback (see hookHomeDir). main()'s dev-mode gates run AFTER
// the claude-hook fast-path dispatch and never affect this resolution.
func runClaudeHook() {
	_ = claudehook.RunHook(os.Stdin, claudehook.HookEnv{
		PaneID:  os.Getenv("QUIL_PANE_ID"),
		QuilDir: hookHomeDir(),
		Mode:    os.Getenv("QUIL_HOOK_MODE"),

		RecordHistory: os.Getenv("QUIL_RECORD_HISTORY") == "1",
	}, time.Now().UnixMilli())
}

// hookHomeDir resolves the data dir for hook writes. The daemon sets
// QUIL_HOOK_HOME in pane envs (renamed from QUIL_HOME, which children
// inherited and which retargeted quil dev builds at production —
// see the 2026-06-10 incident). QUIL_HOME remains as a fallback for panes
// spawned by a pre-rename daemon that survives the upgrade; remove the
// fallback after one release.
func hookHomeDir() string {
	if dir := os.Getenv("QUIL_HOOK_HOME"); dir != "" {
		return dir
	}
	return config.QuilDir()
}
