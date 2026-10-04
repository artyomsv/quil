package daemon

import (
	"log"
	"path/filepath"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
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

// applySandboxSpecFor is applySandboxSpec for a NEW pane on this daemon: it
// validates and records the spec, then settles the one combination the two
// fields must never produce — a token pane in the shared directory.
//
// The rule runs on the EFFECTIVE values, because either field may be empty and
// filled in from this daemon's config: an MCP create naming only
// sandbox_claude_config = "shared" on a daemon configured auth = "token" is a
// token pane, and so is a create naming neither on a daemon configured token +
// shared. In the shared directory a token pane and the browser panes keep
// resetting each other's onboarding through the .quil-auth stamp
// (reconcileClaudeAuthMode), and the token pane would run hooks any Shared
// pane can plant. Recorded as "own" at creation, so a later config change
// cannot move it.
//
// Only creates call this. A restored pane keeps whatever its snapshot
// recorded, including the empty choice, so an upgrade moves nobody's
// transcripts.
func (d *Daemon) applySandboxSpecFor(pane *Pane, spec *ipc.SandboxSpec) error {
	if err := applySandboxSpec(pane, spec); err != nil {
		return err
	}
	if spec == nil {
		return nil
	}
	pane.PluginMu.Lock()
	choice := pane.SandboxClaudeConfig
	pane.PluginMu.Unlock()
	if d.paneAuthMode(pane) != config.SandboxAuthToken {
		return nil
	}
	shares := choice == config.SandboxClaudeConfigShared ||
		(choice == "" && d.cfg.Sandbox.SharedClaudeConfig)
	if !shares {
		return nil
	}
	log.Printf("pane %s: a token pane does not share the Claude config directory; using its own", pane.ID)
	pane.PluginMu.Lock()
	pane.SandboxClaudeConfig = config.SandboxClaudeConfigOwn
	pane.PluginMu.Unlock()
	return nil
}

// paneSharesClaudeConfig is the Daemon-side reader, mirroring paneAuthMode.
func (d *Daemon) paneSharesClaudeConfig(pane *Pane, pluginName string) bool {
	pane.PluginMu.Lock()
	choice := pane.SandboxClaudeConfig
	pane.PluginMu.Unlock()
	return sharesClaudeConfig(choice, d.cfg.Sandbox.SharedClaudeConfig, pluginName)
}
