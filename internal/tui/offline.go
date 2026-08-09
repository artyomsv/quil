package tui

import (
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

	rows := make([]*ProjectModel, 0, len(cached))
	for _, c := range cached {
		// A synthetic ID exists only in the process that invented it. Replaying
		// one would collide with the placeholder the client synthesises afresh
		// for a projects-unaware daemon, and would make destSupportsProjects
		// answer from a stale observation.
		if c.ID == "" || isSyntheticProject(c.ID) {
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

// offlineProjectIDFor names the row invented for a destination with no cached
// projects. Qualified by destination for the reason interimProjectIDFor is:
// indexOfProject resolves by ID alone, so two unnamed rows sharing an ID would
// hand focus back and forth.
func offlineProjectIDFor(dest string) string { return "proj-offline@" + dest }

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
