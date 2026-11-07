package tui

import (
	"log"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// worktreePolicyCmd pushes the "Allow remote branch delete" switch to every
// daemon this client may administer.
//
// The daemon is where the switch is ENFORCED — it refuses a remote delete
// while its copy is off — so the value has to reach it now, not when the TUI
// exits and writes config.toml (and a remote daemon never reads this
// machine's file at all). Sent after attach, beside overlayPolicyCmd, and when
// the F1 row changes.
//
// Admin class, and skipped for a destination this client cannot administer,
// exactly like overlayPolicyCmd: a refused push would be audited on every
// attach. A standard-token destination therefore keeps its own config's value,
// which defaults off.
func (m *Model) worktreePolicyCmd() tea.Cmd {
	if m.client == nil {
		return nil
	}
	p := ipc.WorktreePolicyPayload{RemoteBranchDelete: m.cfg.Worktree.RemoteBranchDelete}
	var dests []string
	for _, d := range m.knownDests() {
		if m.destCanAdmin(d) {
			dests = append(dests, d)
		}
	}
	return func() tea.Msg {
		for _, dest := range dests {
			// A fresh Message per destination: sendForDest stamps Origin.
			msg, err := ipc.NewMessage(ipc.MsgWorktreePolicy, p)
			if err != nil {
				log.Printf("worktree policy: encode for %q: %v", dest, err)
				continue
			}
			if err := m.sendForDest(dest, msg); err != nil {
				log.Printf("worktree policy: send to %q: %v", dest, err)
			}
		}
		return nil
	}
}
