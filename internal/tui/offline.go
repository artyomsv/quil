package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// OfflineKind says what is wrong with a destination the client cannot reach,
// and therefore what can be done about it.
//
// The split that matters is repairable-by-waiting versus repairable-by-acting:
// offlineRetrying enters the reconnect ladder, the other two cannot be fixed
// by any number of redials and must never start one — a ladder against a
// missing binary or a version-drifted daemon re-authenticates and re-fails
// forever, and for the version case actively harmful, since every failed
// attempt is a fresh handshake against a daemon that answers the same
// mismatch each time.
type OfflineKind int

const (
	offlineRetrying OfflineKind = iota
	offlineNeedsInstall
	offlineNeedsUpgrade
)

// Exported aliases for cmd/quil, which classifies a dial failure but cannot name
// unexported constants. The unexported names stay the ones this package uses.
const (
	OfflineRetrying     = offlineRetrying
	OfflineNeedsInstall = offlineNeedsInstall
	OfflineNeedsUpgrade = offlineNeedsUpgrade
)

// laddered reports whether this kind belongs in the reconnect ladder.
func (k OfflineKind) laddered() bool {
	return k == offlineRetrying
}

// OfflineState is why a destination's rows are stand-ins, and what to offer.
type OfflineState struct {
	Kind OfflineKind
	// Detail is remote-influenced: ssh's own stderr, or a version pair reported
	// by the far side. Sanitized and capped at RENDER, never here — the raw
	// value is what a log line should carry.
	Detail string
	Since  time.Time
}

// offlineNow is a package var so a test can pin Since without a clock injection
// ceremony; production never reassigns it.
var offlineNow = time.Now

// confirmDetailCap bounds the version pair on the upgrade confirm. The string
// is built from what the far side reported, and sanitizing does not shorten —
// the same pairing every other remote-influenced render in this package makes.
const confirmDetailCap = 60

// enqueueUpgradePrompt records a destination whose only remedy is provisioning,
// so the user can be ASKED rather than left with a parked row.
//
// A queue rather than a flag: several configured hosts can be stale at once
// after a client update, and one confirm dialog is on screen at a time. Deduped
// because both the launch dial and the reconnect ladder can classify the same
// destination, and asking twice about one host reads as the first answer having
// been ignored.
//
// installedDests is consulted here as well as at the point of action: a host
// already provisioned this session that STILL reports a mismatch did not
// restart its daemon, and pushing the same archive again cannot change that —
// the loop that guard exists to break is install, retry, same error, install.
//
// Whether a provisioner EXISTS is deliberately not asked here, and that is the
// whole reason the launch path offered nothing for a year: cmd/quil/main.go
// seeds the offline rows at :597 and wires installDestFn at :765, so every
// launch-time ask was rejected before it was queued and the drain point found
// an empty queue. Queueing is a record of what is wrong with a host — it does
// not depend on what this Model can do about it yet. promptNextUpgrade asks
// that question instead, by which time the wiring has run.
func (m *Model) enqueueUpgradePrompt(dest, detail string, kind OfflineKind) {
	if dest == "" || m.installedDests[dest] {
		return
	}
	for _, q := range m.upgradeQueue {
		if q.dest == dest {
			return
		}
	}
	m.upgradeQueue = append(m.upgradeQueue, upgradePrompt{dest: dest, detail: detail, kind: kind})
}

// upgradePrompt is one queued ask: which host, why, and the version pair to show.
//
// kind rides along because the confirm's BODY is not the same sentence for both
// of the two states that queue one. An upgrade stops a running daemon and takes
// its panes with it; a first install has no daemon and no panes to lose, and
// telling a user their panes will be killed on a host that has never run quil
// is a reason to decline something that was free. installOffer already draws
// that line for its own two messages.
type upgradePrompt struct {
	dest, detail string
	kind         OfflineKind
}

// promptNextUpgrade opens the confirm for the next queued host, if the screen
// is free to show it.
//
// Gated on dialogNone so it cannot displace the disclaimer, the what's-new
// dialog or the plugin migration at startup — all of which the user is already
// answering, and the migration deliberately blocks until resolved.
// handleDialogKey calls this on every dialog's return to dialogNone, so a
// deferred prompt arrives as soon as the screen is free rather than being
// dropped. That sentence used to appear here while only ONE dismissal honoured
// it; see the comment on handleDialogKey for what that cost.
//
// The no-provisioner check lives HERE rather than at enqueue time because this
// is the moment the answer is final: a dialog whose only action cannot run is
// worse than the parked row, but a Model that has not been wired YET is the
// ordinary launch state, not a Model that never will be.
func (m *Model) promptNextUpgrade() {
	if m.installDestFn == nil || m.dialog != dialogNone || len(m.upgradeQueue) == 0 {
		return
	}
	next := m.upgradeQueue[0]
	m.upgradeQueue = m.upgradeQueue[1:]
	// The destination may have been disconnected, provisioned through the New
	// Project dialog, or COME BACK, between queueing and now.
	//
	// That third one is not cosmetic. A host that reconnected has its
	// Offline cleared by applyWorkspaceState while adoptDest never touches
	// installedDests, so it still resolves to a live project and would be
	// offered an upgrade it no longer needs — and accepting runs the same
	// runRemoteSetup as the CLI, which STOPS that daemon and takes its panes
	// down with it. The confirm's own body warns about exactly that cost; it
	// must not be charged against a host that is working. The window was one
	// WindowSizeMsg wide before the drain moved to every dialog dismissal,
	// which is what made it worth closing rather than noting.
	p := m.projectForDest(next.dest)
	if m.installedDests[next.dest] || p == nil || p.Offline == nil {
		m.promptNextUpgrade()
		return
	}
	m.dialog = dialogConfirm
	m.confirmKind = confirmKindUpgradeDest
	m.resetConfirmWorktrees()
	m.confirmID = next.dest
	m.confirmName = next.dest
	m.confirmDetail = next.detail
	m.confirmOfflineKind = next.kind
}

// SeedOfflineDest installs (or replaces) the stand-in rows for one destination.
//
// Called from the launch path for a destination whose dial failed, and again
// whenever a repair reclassifies one. Replacing rather than appending is what
// makes the second call safe.
//
// cached carries the last project list this client saw on that host. An empty
// one yields a single row named for the destination itself: a host nobody has
// ever reached still has to be visible, because that row is the only thing
// saying it is configured at all.
func (m *Model) SeedOfflineDest(dest, label string, kind OfflineKind, detail string, cached []CachedProject) {
	if dest == "" {
		return // the local daemon is not seedable; its panes died with it
	}

	kept := make([]*ProjectModel, 0, len(m.projects)+1)
	for _, p := range m.projects {
		if p.Dest != dest {
			kept = append(kept, p)
		}
	}

	state := &OfflineState{Kind: kind, Detail: detail, Since: offlineNow()}

	// Queue the ask for a host whose only remedy is provisioning. Queued rather
	// than opened here because SeedOfflineDest runs BEFORE tea.Program starts —
	// and because the disclaimer or the plugin migration may own the screen at
	// that point. promptNextUpgrade drains it once the screen is free.
	if kind == offlineNeedsInstall || kind == offlineNeedsUpgrade {
		m.enqueueUpgradePrompt(dest, detail, kind)
	}

	rows := make([]*ProjectModel, 0, len(cached))
	for _, c := range cached {
		// Both families below are IDs this client invented itself, never one a
		// daemon reported: a synthetic ID would collide with the placeholder
		// synthesised afresh for a projects-unaware daemon (destSupportsProjects
		// would then answer from a stale observation), and an offline-row ID
		// would collide with another destination's own stand-in row — a daemon's
		// cache is keyed by ITS OWN id, with no guarantee it avoids the
		// "proj-offline@<dest>" shape another host's stand-in happens to use.
		if c.ID == "" || isClientInventedProjectID(c.ID) {
			continue
		}
		rows = append(rows, &ProjectModel{
			ID:      c.ID,
			Name:    c.Name,
			RootDir: c.RootDir,
			Dest:    dest,
			Offline: state,
		})
	}
	if len(rows) == 0 {
		rows = append(rows, &ProjectModel{
			ID:      offlineProjectIDFor(dest),
			Name:    label,
			Dest:    dest,
			Offline: state,
		})
	}

	m.projects = append(kept, rows...)
}

// onlyOfflineProjects reports that every project the client holds is a stand-in,
// which is the seeded-but-not-yet-connected state at launch.
//
// It exists because applyWorkspaceState adopts the daemon's remembered active
// project only when there is no active project yet, and seeded rows make cur()
// non-nil before the first broadcast lands. Without this, a launch with a dead
// remote opens focused on a row that cannot show anything.
func (m Model) onlyOfflineProjects() bool {
	if len(m.projects) == 0 {
		return false
	}
	for _, p := range m.projects {
		if p.Offline == nil {
			return false
		}
	}
	return true
}

// offlineProjectIDPrefix marks an ID this client invented for a destination
// with no cached projects, never one a daemon reported. Shared with
// isClientInventedProjectID so the two cannot drift apart.
const offlineProjectIDPrefix = "proj-offline@"

// offlineProjectIDFor names the row invented for a destination with no cached
// projects. Qualified by destination for the reason interimProjectIDFor is:
// indexOfProject resolves by ID alone, so two unnamed rows sharing an ID would
// hand focus back and forth.
func offlineProjectIDFor(dest string) string { return offlineProjectIDPrefix + dest }

// isClientInventedProjectID reports whether id was minted by THIS client
// rather than reported by any daemon — either the interim placeholder
// (isSyntheticProject, for a projects-unaware daemon) or an offline stand-in
// row's own ID (offlineProjectIDFor). Both families exist only in this
// process's memory, so a value replayed from a CACHE — which is keyed by
// whatever ID the reporting daemon used — can collide with either one:
// offlineProjectIDFor is qualified by destination, but nothing stops a
// daemon on host A from having, at some point, reported a real project ID
// that happens to take the "proj-offline@<dest>" shape host B's own stand-in
// row uses. indexOfProject and resolveActiveProjectIndex both resolve the
// FIRST match for an ID, so a collision reads as focus landing on the wrong
// host's row rather than a crash — quiet enough that grouping both checks
// under one predicate is what keeps a future caller from checking only one.
func isClientInventedProjectID(id string) bool {
	return isSyntheticProject(id) || strings.HasPrefix(id, offlineProjectIDPrefix)
}

// projectForDest returns any project belonging to dest, for the arms that need
// to read a destination's offline state rather than one project's.
func (m Model) projectForDest(dest string) *ProjectModel {
	for _, p := range m.projects {
		if p.Dest == dest {
			return p
		}
	}
	return nil
}

// offlineDestMsg starts the reconnect ladder for a destination that never
// connected, and it is a type of its own rather than a synthesised linkLostMsg.
//
// That arm re-arms listenForMessages for a router, because a REAL link loss is
// produced BY the listen loop, which stopped in order to deliver it. Nothing
// stops when this message is constructed, so reusing that arm would leave the
// original reader parked in Receive and add a second permanent one — and two
// readers of r.in reorder pane output and workspace_state with no error
// anywhere. finishReconnect's own doc names the same hazard.
type offlineDestMsg struct{ dest string }

// wakeOfflineDests emits one offlineDestMsg per offline destination whose kind
// can be repaired by retrying, and marks them so it does not fire twice.
//
// Returns nil when there is nothing to wake, which is the ordinary case.
func (m *Model) wakeOfflineDests() tea.Cmd {
	var cmds []tea.Cmd
	for _, p := range m.projects {
		if p.Offline == nil || p.Dest == "" || !p.Offline.Kind.laddered() {
			continue
		}
		if m.offlineWoken[p.Dest] {
			continue
		}
		if m.offlineWoken == nil {
			m.offlineWoken = map[string]bool{}
		}
		m.offlineWoken[p.Dest] = true
		dest := p.Dest
		cmds = append(cmds, func() tea.Msg { return offlineDestMsg{dest: dest} })
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}
