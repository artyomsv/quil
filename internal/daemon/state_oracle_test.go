package daemon

// This file is a FROZEN copy of the pre-3a workspace-state builder — the one
// that built map[string]any instead of ipc.WorkspaceState. It exists ONLY as
// the oracle TestWorkspaceStateFromSnapshot_Typed_MatchesOldMap and
// TestBuildWorkspaceState_Typed_MatchesOldMapExceptRev compare the new typed
// builder against. Never edit this file to make a test pass — if the typed
// builder and this file disagree, the typed builder is what is wrong (or, in
// the rare case the old behavior itself was a bug, the plan/task brief says so
// explicitly and this file is retired, not patched around).

func (d *Daemon) oldBuildWorkspaceStateMap() map[string]any {
	activeTab, tabs, panesByTab, projects, activeProject := d.session.SnapshotState()
	state := d.oldWorkspaceStateMap(activeTab, tabs, panesByTab, projects, activeProject, true)
	// Broadcast-only (never persisted): announced newer release, if any.
	if info := d.currentUpdateInfo(); info != nil {
		state["update"] = info
	}
	// Broadcast-only as well: the size master's client id ("" for none) and
	// the attached-client count. Each TUI reads them to tell whether it is the
	// master or a follower. snapshot() writes size_master to disk by itself,
	// for the restart reserve; the count means nothing after a restart.
	state["size_master"] = d.masterID()
	state["clients"] = d.clientCount()
	// Broadcast-only, omitted unless true: a daemon in session 0 (started over
	// ssh, or by a service) has no saved credentials and no visible desktop.
	if d.limited {
		state["daemon_limited"] = true
	}
	return state
}

// oldWorkspaceStateMap is the pure half of oldBuildWorkspaceStateMap — it
// turns an already-taken SnapshotState into the wire/persistence map. Callers
// that already hold a consistent snapshot (e.g. snapshot()) reuse it instead
// of calling SnapshotState a second time.
//
// includeOverlays controls whether ephemeral overlay panes are present in the
// output. Pass true for live broadcasts (TUI needs them for routing) and false
// for disk snapshots (overlays are intentionally ephemeral — gone on restart).
//
// projects/activeProject come from the SAME SnapshotState call as tabs/
// panesByTab — see SnapshotState's doc comment. They ride both the disk
// snapshot and the live broadcast because this function is shared by both
// (oldBuildWorkspaceStateMap and snapshot()); writing them only at the
// persist.Save call site would leave every broadcast project-less.
func (d *Daemon) oldWorkspaceStateMap(activeTab string, tabs []*Tab, panesByTab map[string][]*Pane, projects []Project, activeProject string, includeOverlays bool) map[string]any {
	tabList := make([]map[string]any, 0, len(tabs))
	paneList := make([]map[string]any, 0)

	for _, tab := range tabs {
		// Overlay is PluginMu-guarded like Muted: handleCreatePane sets it
		// AFTER the pane is published to the session maps, concurrently with
		// this snapshot/broadcast. Capture one consistent view per tab here
		// and reuse it for both the pane-ID filter and the pane-loop skip.
		overlayIDs := make(map[string]bool)
		for _, pane := range panesByTab[tab.ID] {
			pane.PluginMu.Lock()
			isOverlay := pane.Overlay
			pane.PluginMu.Unlock()
			if isOverlay {
				overlayIDs[pane.ID] = true
			}
		}
		paneIDs := make([]string, 0, len(tab.Panes))
		for _, pid := range tab.Panes {
			if !includeOverlays && overlayIDs[pid] {
				continue
			}
			paneIDs = append(paneIDs, pid)
		}
		tabData := map[string]any{
			"id":         tab.ID,
			"name":       tab.Name,
			"color":      tab.Color,
			"panes":      paneIDs,
			"project_id": tab.ProjectID,
			// Unconditional, unlike "layout" below: every client compares
			// this against its own copy on every broadcast to decide whether
			// to adopt (spec §7.1), including a tab whose layout has never
			// been written, so it must be on the wire even at its zero value.
			"layout_rev": tab.LayoutRev,
		}
		if len(tab.Layout) > 0 {
			tabData["layout"] = tab.Layout
		}
		if tab.TemplateLayout != "" {
			tabData["template_layout"] = tab.TemplateLayout
		}
		if tab.TemplateMain != "" {
			tabData["template_main"] = tab.TemplateMain
		}
		tabList = append(tabList, tabData)

		for _, pane := range panesByTab[tab.ID] {
			// overlayIDs was captured under PluginMu above — reuse it so the
			// skip decision agrees with the pane-ID filter for this snapshot.
			if !includeOverlays && overlayIDs[pane.ID] {
				continue
			}
			// tab_id is the tab that LISTS this pane in this snapshot, never
			// pane.TabID read afterwards: a MovePane between SnapshotState and
			// here would make the two disagree within one frame, and the TUI's
			// applyTemplateLayout bails on exactly that mismatch.
			paneData := map[string]any{
				"id":     pane.ID,
				"tab_id": tab.ID,
			}
			if pane.Name != "" {
				paneData["name"] = pane.Name
			}
			// Type and CWD are PluginMu-protected: spawnRestoredPane mutates
			// them on the lazy-spawn error paths (CWD="" when the saved dir is
			// gone, Type="terminal" on spawn fallback) concurrently with this
			// snapshot. Capture both under the same lock as the other
			// PluginMu-guarded fields (Overlay included — see session.go).
			pane.PluginMu.Lock()
			typ := pane.Type
			cwd := pane.CWD
			isOverlay := pane.Overlay
			mouseTracking := pane.MouseModes.tracking()
			mouseSGR := pane.MouseModes.sgr
			bracketedPaste := pane.MouseModes.bracketedPaste
			sessionID := pane.PluginState["session_id"]
			historyLines := pane.HistoryLines
			lastModel := pane.LastModel
			lastContextTokens := pane.LastContextTokens
			if len(pane.PluginState) > 0 {
				// Copy to avoid holding lock during JSON marshal
				ps := make(map[string]string, len(pane.PluginState))
				for k, v := range pane.PluginState {
					ps[k] = v
				}
				paneData["plugin_state"] = ps
			}
			if pane.Muted {
				paneData["muted"] = true
			}
			if pane.Eager {
				paneData["eager"] = true
			}
			// PERSISTED because the loop it serves spans restarts: a user whose
			// habit is shell -> agent -> /exit -> shell would otherwise find
			// every cycle after a daemon restart ending on a dead agent pane.
			if pane.ConvertedFromTerminal != "" {
				paneData["converted_from"] = pane.ConvertedFromTerminal
			}
			// PERSISTED so the restore resumes the conversation the user
			// started by hand. Without it the session id in plugin_state is
			// indistinguishable from one Quil itself preassigned, and the
			// UI would claim tracking that a restart silently dropped.
			if pane.Adopted {
				paneData["adopted"] = true
			}
			// PERSISTED for the reason the field exists: the mark is the user's
			// own, nothing re-derives it, and a hook edge that would set it
			// again is never coming. Written here rather than in the
			// includeOverlays block so ONE line serves both the disk snapshot
			// and the broadcast — the same arrangement muted has.
			if pane.PinnedAttention {
				paneData["pinned_attention"] = true
			}
			// PERSISTED for the same reason, and the case for it is stronger:
			// the mark is set precisely so the user can walk away from a pane
			// with a job still running and decide about it later, and "later"
			// routinely spans a restart. A mark lost there sends them back to
			// reading the scrollback, which is what the mark replaces.
			if pane.MarkedForDeletion {
				paneData["marked_for_deletion"] = true
			}
			// PERSISTED so the green "finished while you were away" tab
			// survives a TUI restart, which is when the user most needs it:
			// they left the pane running, came back, and want to know what
			// finished. The client derives the mark and reports it; this copy
			// only has to outlive the client. Same one-line-serves-both
			// arrangement as the pin.
			if pane.Unseen {
				paneData["unseen"] = true
			}
			// PERSISTED, unlike SpawnError beside it: this is how restore tells
			// a missing worktree from a stale browsed directory, and without it
			// the snapshot carries only CWD, which cannot distinguish them.
			if pane.WorktreeOwned {
				paneData["worktree_owned"] = true
			}
			if pane.QuilMCP {
				paneData["quil_mcp"] = true
			}
			// Persisted for the same reason, and it is the half that says WHICH
			// directory. Without it a restored pane can only name its CWD, which
			// the shell has been rewriting all along — see Pane.WorktreePath.
			if pane.WorktreePath != "" {
				paneData["worktree_path"] = pane.WorktreePath
			}
			// The sandbox pair. The image is what makes a restored pane
			// sandboxed at all; the container CWD is what the resume path
			// needs and cannot re-derive, since Claude names a transcript
			// directory after the working directory its own process saw.
			//
			// The pane TYPE is written with a sandbox prefix alongside them
			// (see sandboxPaneType). That is what protects a DOWNGRADE: a
			// daemon too old to read these keys would otherwise restore a
			// sandbox pane as an ordinary one pointed at the worktree — an
			// agent on the host, silently un-sandboxed — where an unknown
			// type falls back to a plain shell.
			if pane.SandboxImage != "" {
				paneData["sandbox_image"] = pane.SandboxImage
				paneData["sandbox_auth"] = pane.SandboxAuth
				paneData["type"] = sandboxPaneType(pane.Type)
			}
			if pane.ContainerCWD != "" {
				paneData["container_cwd"] = pane.ContainerCWD
			}
			// PERSISTED, unlike the branch it stands for. A snapshot landing
			// inside the checkout window would otherwise restore an ordinary
			// terminal in the repository root — the bug this feature removes,
			// brought back from disk. See Pane.WorktreeInterrupted.
			if pane.PreparingWorktree != "" || pane.WorktreeInterrupted {
				paneData["worktree_interrupted"] = true
			}
			// SpawnError is captured for the BROADCAST only — see
			// includeOverlays below. It is never written to paneData.
			spawnErr := pane.SpawnError
			// Same: captured under the lock that protects it, written to
			// paneData only on the broadcast side.
			preparingWorktree := pane.PreparingWorktree
			// Captured here rather than read below: this runs on the snapshot
			// goroutine while handleResizePane writes them from a conn dispatch
			// goroutine.
			snapCols, snapRows := pane.Cols, pane.Rows
			// In the same span as Cols/Rows: the number must describe exactly
			// the size read beside it (see Pane.sizeSeq).
			snapSizeSeq := pane.colsSeq
			pane.PluginMu.Unlock()
			// Broadcast-only, runtime: the counter restarts with the daemon,
			// so a persisted one would mean nothing.
			if includeOverlays && snapSizeSeq > 0 {
				paneData["size_seq"] = snapSizeSeq
			}
			// Pending (deferred, not yet lazy-spawned) is spawnMu-guarded —
			// read it the same way list_panes does. The TUI uses it to show the
			// restore indicator on deferred panes and to re-arm the indicator
			// when the pane actually spawns (Pending→running on tab switch).
			// Broadcast-only: Pending is runtime state, never persisted to disk
			// (gated on includeOverlays, like overlay panes).
			if includeOverlays {
				pane.spawnMu.Lock()
				pending := pane.Pending
				pane.spawnMu.Unlock()
				if pending {
					paneData["pending"] = true
				}
			}
			// Broadcast-only restore-checklist hints (runtime, never persisted):
			// the tracked session id and the ghost-buffer line count the TUI
			// shows in the per-pane restore checklist.
			if includeOverlays {
				if sessionID != "" {
					paneData["session_id"] = sessionID
				}
				if historyLines > 0 {
					paneData["history_lines"] = historyLines
				}
				// Mouse-mode state is runtime-only (broadcast, never persisted):
				// it is re-derived from the live PTY stream on every spawn.
				if mouseTracking {
					paneData["mouse_tracking"] = true
				}
				if mouseSGR {
					paneData["mouse_sgr"] = true
				}
				if bracketedPaste {
					paneData["bracketed_paste"] = true
				}
				// Why the pane has no process, runtime-only: a fresh daemon
				// re-stats and re-derives it, and persisting it would
				// resurrect a complaint about a worktree the user has since
				// restored. Broadcast rather than logged because a relocation
				// nobody sees is the failure mode this replaces.
				if spawnErr != "" {
					paneData["spawn_error"] = spawnErr
				}
				// The branch a checkout is running for, runtime-only for the
				// same reason and one more: a snapshot landing inside the
				// checkout window would restore a pane waiting on an add no
				// daemon is running, and nothing would ever settle it.
				// Broadcast because without it the placeholder renders as an
				// ordinary blank terminal — which is what it looked like for the
				// whole of a monorepo-sized checkout.
				if preparingWorktree != "" {
					paneData["preparing_worktree"] = preparingWorktree
				}
				// Model/context usage of the last completed AI turn is
				// runtime-only (broadcast, never persisted): a stale token
				// count from a previous daemon run would be wrong until the
				// next turn refreshes it.
				if lastModel != "" {
					paneData["model"] = lastModel
					paneData["context_tokens"] = lastContextTokens
				}
				// Git state is runtime-only for the same reason: a branch name
				// from a previous daemon run describes a checkout nobody has
				// re-probed. lookup() never probes, so this cannot slow a
				// broadcast down whatever the filesystem is doing.
				if info, ok, stale := d.gitCache.lookup(cwd); ok {
					if info.Branch != "" {
						paneData["git_branch"] = info.Branch
					}
					if info.Detached {
						paneData["git_detached"] = true
					}
					if info.LinkedWorktree {
						paneData["git_worktree"] = true
						// Conditional like git_branch: an absent key decodes
						// to the zero value on the client, where the copy is
						// unconditional and therefore clears it.
						if info.WorktreeName != "" {
							paneData["git_worktree_name"] = info.WorktreeName
						}
					}
					if info.HasUpstream {
						// Sent even at zero: "0 ahead, 0 behind" means in sync,
						// which is a different statement from having no
						// upstream to compare against.
						paneData["git_upstream"] = true
						paneData["git_ahead"] = info.Ahead
						paneData["git_behind"] = info.Behind
					}
					if stale {
						paneData["git_stale"] = true
					}
				}
			}
			paneData["cwd"] = cwd
			if typ != "" && typ != "terminal" {
				paneData["type"] = typ
			}
			if pane.InstanceName != "" {
				paneData["instance_name"] = pane.InstanceName
			}
			if len(pane.InstanceArgs) > 0 {
				paneData["instance_args"] = pane.InstanceArgs
			}
			// Persist last known size so respawnPanes can recreate the
			// ConPTY at the right dimensions instead of the 80x24 default
			// (children that boot before the first resize event would
			// otherwise render an 80-column UI — see resizeKick).
			if snapCols > 0 && snapRows > 0 {
				paneData["cols"] = snapCols
				paneData["rows"] = snapRows
			}
			if isOverlay {
				paneData["overlay"] = true
			}
			paneList = append(paneList, paneData)
		}
	}

	projectList := make([]any, 0, len(projects))
	for _, p := range projects {
		projectList = append(projectList, map[string]any{
			"id":         p.ID,
			"name":       p.Name,
			"root_dir":   p.RootDir,
			"tab_ids":    p.TabIDs,
			"active_tab": p.ActiveTab,
			// Reaches the client AND the disk snapshot from here, because this
			// map is both. The client needs it to decide whether naming a
			// project adopts this one; the snapshot needs it so a restart does
			// not turn an un-adopted default into a real project.
			"bootstrap": p.Bootstrap,
		})
	}

	return map[string]any{
		"active_tab":     activeTab,
		"tabs":           tabList,
		"panes":          paneList,
		"projects":       projectList,
		"active_project": activeProject,
	}
}
