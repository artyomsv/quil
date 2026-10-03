package daemon

import (
	"path/filepath"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/plugin"
)

// sharesClaudeConfig decides whether a sandbox pane mounts the ONE shared
// Claude config directory instead of its own.
//
// The single rule, read by both the mount (prepareSandbox) and the resume path
// (hostTranscriptPath). Splitting them mounts one directory and looks for the
// transcript in the other — a restored pane then passes --session-id for a
// session that has a transcript, and claude exits 129.
//
// The pane's own recorded choice wins over the config, so flipping
// [sandbox] shared_claude_config never moves an existing pane's transcripts;
// "" (older clients, older snapshots, MCP without the field) follows it.
//
// A non-Claude agent never shares: the directory holds Claude's user-scope
// hooks and MCP servers, so a codex or opencode container with write access
// could plant code every Shared Claude pane then runs. Same predicate that
// gates the sign-in, the token and the auth stamp.
func sharesClaudeConfig(choice string, cfgShared bool, pluginName string) bool {
	if !plugin.UsesClaudeAuthName(pluginName) {
		return false
	}
	switch choice {
	case config.SandboxClaudeConfigShared:
		return true
	case config.SandboxClaudeConfigOwn:
		return false
	}
	return cfgShared
}

// sharedClaudeConfigDir is the only spelling of the shared directory's path.
func sharedClaudeConfigDir(quilDir string) string {
	return filepath.Join(sandboxRoot(quilDir), "claude")
}

// paneSharesClaudeConfig is the Daemon-side reader, mirroring paneAuthMode.
func (d *Daemon) paneSharesClaudeConfig(pane *Pane, pluginName string) bool {
	pane.PluginMu.Lock()
	choice := pane.SandboxClaudeConfig
	pane.PluginMu.Unlock()
	return sharesClaudeConfig(choice, d.cfg.Sandbox.SharedClaudeConfig, pluginName)
}
