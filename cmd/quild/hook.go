package main

import (
	"os"
	"time"

	"github.com/artyomsv/quil/internal/claudehook"
	"github.com/artyomsv/quil/internal/config"
)

// runClaudeHook handles the `quild claude-hook` subcommand: it processes one
// Claude Code hook invocation (JSON on stdin) and writes the session-id file
// or a spool line under $QUIL_HOME. It deliberately does NOT initialize the
// daemon, logger, or config-from-disk — this is the hot path Claude spawns
// once per hook event, so it must start fast. Errors are swallowed (RunHook
// logs them to the hook log) and the process always exits 0 via the caller.
//
// QUIL_PANE_ID and QUIL_HOOK_MODE are set by the daemon in the claude-code
// pane's environment; QUIL_HOME resolves through config.QuilDir (set either by
// the daemon's hook env or, for the dev build, by main()'s build-mode default).
func runClaudeHook() {
	_ = claudehook.RunHook(os.Stdin, claudehook.HookEnv{
		PaneID:  os.Getenv("QUIL_PANE_ID"),
		QuilDir: config.QuilDir(),
		Mode:    os.Getenv("QUIL_HOOK_MODE"),
	}, time.Now().UnixMilli())
}
