package tui

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/google/uuid"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/gitworktree"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/keymap"
	"github.com/artyomsv/quil/internal/logger"
	"github.com/artyomsv/quil/internal/plugin"
)

// dialogWidth is the standard width used by About, Settings, Shortcuts, and
// the non-plugin Confirm dialogs. Set wide enough that:
//   - Settings rows fit `cursor (2) + label (24) + value (24)` without
//     wrapping the longest description (e.g. "(closes this TUI window)").
//   - The Stop-daemon confirm body line "Panes will respawn from the
//     snapshot on next launch." fits on a single rendered row.
//
// Matched to disclaimerWidth so the visual style is consistent across all
// fixed-width modal dialogs.
const dialogWidth = 60
const disclaimerWidth = 60

// gitRepoPickWidth is the multi-repo lazygit picker width. Repo paths are long,
// so it runs 50% wider than the standard dialog to keep distinguishing tails
// readable without aggressive left-truncation.
const gitRepoPickWidth = 90

// disclaimerTips are shown randomly in the startup disclaimer dialog.
// Each body line is rendered as a bullet point.
var disclaimerTips = []struct {
	title string
	items []string
}{
	{"Quick Navigation", []string{
		"Ctrl+Arrow jumps words",
		"Ctrl+Alt+Arrow jumps 3 words",
		"Ctrl+Up/Down jumps paragraphs",
	}},
	{"Split Panes", []string{
		"Alt+H splits horizontal",
		"Alt+V splits vertical",
		"Tab/Shift+Tab cycles between panes",
	}},
	{"Typed Panes", []string{
		"Ctrl+N creates typed panes (SSH, Claude)",
		"Plugins configurable via F1 > Plugins",
	}},
	{"Session Persistence", []string{
		"Workspace survives reboots",
		"Tabs, panes, and history auto-restored",
	}},
	{"Pane Management", []string{
		"F2 renames tabs, Alt+F2 renames panes",
		"Ctrl+W closes pane, Alt+W closes tab",
		"Ctrl+E toggles focus mode",
	}},
	{"Text Selection", []string{
		"Shift+Arrows selects text",
		"Enter copies selection to clipboard",
		"Ctrl+V pastes from clipboard",
	}},
	{"Customization", []string{
		"Edit ~/.quil/config.toml for settings",
		"All keybindings are configurable",
		"Press F1 for help and shortcuts",
	}},
}

// dialogBoxChrome is what dialogBorder costs every content line: the two
// vertical border glyphs plus Padding(1, 2)'s 4 horizontal cells. lipgloss
// draws the border INSIDE Style.Width, so a dialog's usable content width is
// its box width minus this — budgeting only the padding leaves rows two cells
// too wide and reflow wraps each one onto a second line.
const dialogBoxChrome = 6

// dialogKeyColWidth is the fixed cell budget dialogKeyStyle gives the key half
// of a key/description row. Named rather than inlined into the style because
// the shortcuts list sizes its descriptions against what is LEFT of the row
// after it — two numbers that have to move together, and a description budget
// computed against a stale one wraps the row it was meant to fit.
//
// 22, not 16: "pane.rename" is the one action with two default bindings
// (the macOS-friendly Alt+Shift+R fallback beside Alt+F2), and
// Keymap.Display joins them as "alt+f2 / alt+shift+r" — 20 cells. At 16 that
// wrapped onto a second line, which is the same row-count-vs-line-count
// mismatch shortcutsDescWidth's own doc warns about, just in the other
// column. 22 leaves 2 cells of headroom on the key side.
//
// The description side is where this comment used to claim 2 cells of headroom
// that were not there: the row also pays shortcutsRowIndent, so at the old
// 74-cell box the budget was 74 - dialogBoxChrome - 2 - 22 = 44, against a
// longest description ("Remove active project (destroy / disconnect)") of
// exactly 44 — zero margin, and the next label one cell longer would have
// wrapped. The headroom comes from shortcutsDialogWidth, which the conflict
// rows widened anyway; see its own comment.
const dialogKeyColWidth = 22

// dialogInnerWidth is the usable content width for a dialog whose box is boxW
// columns wide in a termW-column terminal. It applies renderDialog's own clamp
// and then subtracts dialogBoxChrome, so a caller sizing its rows against this
// can never exceed the box lipgloss draws.
//
// One function rather than the copy each dialog used to keep: the clamp and the
// chrome have to agree with renderDialog, and a copy that drifts reintroduces
// exactly the two-cells-too-wide reflow that made the history list unreadable —
// silently, with the other copies' tests still green. termW <= 2 means the
// terminal size is not known yet (before the first WindowSizeMsg), where
// renderDialog also skips the clamp.
func dialogInnerWidth(termW, boxW int) int {
	if termW > 2 && boxW > termW-2 {
		boxW = termW - 2
	}
	if inner := boxW - dialogBoxChrome; inner > 1 {
		return inner
	}
	return 1
}

var (
	dialogBorder = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("63")).
			Padding(1, 2)

	dialogTitle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("230"))

	dialogSubtle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241"))

	dialogSelected = lipgloss.NewStyle().
			Foreground(lipgloss.Color("230")).
			Bold(true)

	// dialogSelectedIdle marks a row that holds the field's committed value
	// while the field itself is NOT focused. Bold (so the choice stays
	// readable at a glance) but not the bright white of dialogSelected, so it
	// never competes with the row the cursor is actually on.
	dialogSelectedIdle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("250")).
				Bold(true)

	dialogNormal = lipgloss.NewStyle().
			Foreground(lipgloss.Color("250"))

	// dialogWarn marks the one thing in a dialog that destroys data — today,
	// the close confirm's worktree row and the uncommitted-change count under
	// it. 214 is the amber this codebase already reserves for "this needs your
	// attention" (blockedTabStyle, sidebarBlockedStyle) rather than the red it
	// uses for a failure, because an armed toggle is a choice, not an error.
	dialogWarn = lipgloss.NewStyle().
			Foreground(lipgloss.Color("214"))

	dialogKeyStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("63")).
			Width(dialogKeyColWidth)

	dialogValStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("250"))

	dialogEditStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("230")).
			Background(lipgloss.Color("238"))

	dialogLabelStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("250")).
				Width(24)

	dialogErrorStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("196")).
				Bold(true)
)

// settingsField describes one row in the Settings dialog. Each row edits a
// config value: label + get + set, optionally isBool for toggles. Non-config
// actions (e.g. "Stop daemon") live on the F1 → About root menu instead — see
// handleAboutKey.
type settingsField struct {
	label  string
	get    func(m *Model) string
	set    func(m *Model, val string)
	isBool bool
	// relayout marks a row whose value changes the pane geometry, so
	// handleSettingsKey follows the set with resizeTabs + resizeAllPanes +
	// ClearScreen. A flag rather than a label comparison at the call site:
	// the row that needs it is the row that declares it, and renaming a
	// label cannot silently drop the resize.
	relayout bool
}

// settingsFields returns the editable Settings rows. Every setter that
// actually mutates persistent state must set m.configChanged = true so
// the change is written to ~/.quil/config.toml when the TUI exits — without
// it, edits are silently lost. Live re-application (e.g. switching the log
// level for the running process) is intentionally NOT done here: changes
// take effect on the next launch.
func settingsFields() []settingsField {
	return []settingsField{
		{
			label: "Snapshot interval",
			get:   func(m *Model) string { return m.cfg.Daemon.SnapshotInterval },
			set: func(m *Model, v string) {
				if d, err := time.ParseDuration(v); err == nil && d > 0 && m.cfg.Daemon.SnapshotInterval != v {
					m.cfg.Daemon.SnapshotInterval = v
					m.configChanged = true
				}
			},
		},
		{
			label: "Ghost dimmed",
			get:   func(m *Model) string { return boolStr(m.cfg.GhostBuffer.Dimmed) },
			set: func(m *Model, _ string) {
				m.cfg.GhostBuffer.Dimmed = !m.cfg.GhostBuffer.Dimmed
				m.configChanged = true
			},
			isBool: true,
		},
		{
			label: "Ghost buffer lines",
			get:   func(m *Model) string { return strconv.Itoa(m.cfg.GhostBuffer.MaxLines) },
			set: func(m *Model, v string) {
				if n, err := strconv.Atoi(v); err == nil && n > 0 && m.cfg.GhostBuffer.MaxLines != n {
					m.cfg.GhostBuffer.MaxLines = n
					m.configChanged = true
				}
			},
		},
		{
			label: "Mouse scroll lines",
			get:   func(m *Model) string { return strconv.Itoa(m.cfg.UI.MouseScrollLines) },
			set: func(m *Model, v string) {
				if n, err := strconv.Atoi(v); err == nil && n > 0 && m.cfg.UI.MouseScrollLines != n {
					m.cfg.UI.MouseScrollLines = n
					m.configChanged = true
				}
			},
		},
		{
			label: "Page scroll lines",
			get:   func(m *Model) string { return strconv.Itoa(m.cfg.UI.PageScrollLines) },
			set: func(m *Model, v string) {
				if n, err := strconv.Atoi(v); err == nil && n >= 0 && m.cfg.UI.PageScrollLines != n {
					m.cfg.UI.PageScrollLines = n
					m.configChanged = true
				}
			},
		},
		{
			// The one Settings row that applies LIVE. The comment on
			// settingsFields says changes take effect on the next launch,
			// which is right for the log level — its file handle lives in
			// main.go and is not re-plumbed into the Model — and wrong here:
			// the sidebar width is read from m.sidebarWidth on every render,
			// so a visible layout control that did nothing until relaunch
			// would read as a broken dialog. handleSettingsKey follows the
			// set with the resize sequence toggleProjectSidebar documents.
			label: "Sidebar width",
			get:   func(m *Model) string { return strconv.Itoa(m.cfg.UI.SidebarWidth) },
			set: func(m *Model, v string) {
				n, err := strconv.Atoi(v)
				// Refused rather than written-and-corrected: sidebarWidth()
				// clamps at render, so a stored value outside the usable range
				// would be displayed while the layout used something else. The
				// dialog must never show a number the layout is not using.
				//
				// Bounded by the SAME limits the edge drag enforces —
				// minSidebarWidth below, and sidebarWidth's own
				// total-minTermWidth clamp above — so the two entry points
				// cannot disagree about what is settable.
				if err != nil || n < minSidebarWidth || n == m.cfg.UI.SidebarWidth {
					return
				}
				// Bounded against the CAP only, and measured at a width the
				// sidebar can actually occupy. Comparing against
				// sidebarWidth(m.width, …) directly made the row inert on any
				// terminal under minWidthForSidebar: it returns 0 there
				// whatever is asked, so every value mismatched and the
				// setting silently could not be changed — while the value is
				// still perfectly legal for the wider terminal the user will
				// resize to.
				// Measured at a width the sidebar can actually occupy.
				// Comparing against m.width alone made the row inert below
				// minWidthForSidebar (sidebarWidth returns 0 there whatever is
				// asked, so every value mismatched); skipping the check there
				// instead let 100000 be stored and shown while the layout used
				// width-minTermWidth. Both violate the setter's own rule that
				// the dialog never displays a number the layout is not using.
				usable := m.width
				if usable < minWidthForSidebar {
					usable = minWidthForSidebar
				}
				if n > usable-minTermWidth {
					return
				}
				m.cfg.UI.SidebarWidth = n
				m.sidebarWidth = n
				m.configChanged = true
			},
			relayout: true,
		},
		{
			// Overlay retention (idle timeout + live cap) is pushed to the
			// daemon LIVE via overlayPolicyCmd — see handleSettingsKey and
			// attachAllDests — unlike the rest of this table, which the
			// settingsFields doc comment says takes effect on the next
			// launch. The stored value here is still the persisted one; the
			// push is what makes an already-running daemon honour it now.
			label: "Overlay idle timeout (min)",
			get:   func(m *Model) string { return strconv.Itoa(m.cfg.Overlay.IdleTimeoutMinutes) },
			set: func(m *Model, v string) {
				// Refused rather than clamped, like Sidebar width: a stored
				// value the daemon would not honour must never be displayed.
				// 0 is legal and means "never evict".
				n, err := strconv.Atoi(v)
				if err != nil || n < 0 || n == m.cfg.Overlay.IdleTimeoutMinutes {
					return
				}
				m.cfg.Overlay.IdleTimeoutMinutes = n
				m.configChanged = true
			},
		},
		{
			// Reports STATE, not the flag. With Enabled defaulting true,
			// enabled-but-unregistered is the default on a fresh Windows
			// install — a bare "on" there would claim toasts are working when
			// no toast can be displayed at all. Same rule the Sidebar width
			// setter states: never show a value the system is not using.
			//
			// The row deliberately does NOT perform registration. Writing a
			// Start Menu shortcut and an HKCU key as a side effect of a config
			// toggle is exactly the auto-register behaviour this design
			// rejected; it names the command instead.
			//
			// Applies LIVE, unlike most rows here: raiseAttentionToast reads
			// m.cfg.Notification.Desktop on every edge, so there is no apply
			// step. An on/off switch that did nothing until relaunch would read
			// as a broken dialog — the same reason Sidebar width is live.
			label: "Desktop notifications",
			get:   func(m *Model) string { return m.desktopState().label() },
			set: func(m *Model, _ string) {
				m.cfg.Notification.Desktop.Enabled = !m.cfg.Notification.Desktop.Enabled
				m.configChanged = true
			},
			isBool: true,
		},
		{
			label: "Max live overlays",
			get:   func(m *Model) string { return strconv.Itoa(m.cfg.Overlay.MaxLive) },
			set: func(m *Model, v string) {
				// 0 is legal and means "no cap".
				n, err := strconv.Atoi(v)
				if err != nil || n < 0 || n == m.cfg.Overlay.MaxLive {
					return
				}
				m.cfg.Overlay.MaxLive = n
				m.configChanged = true
			},
		},
		{
			label: "Log level",
			get:   func(m *Model) string { return m.cfg.Logging.Level },
			set: func(m *Model, v string) {
				switch v {
				case "debug", "info", "warn", "error":
					if m.cfg.Logging.Level != v {
						m.cfg.Logging.Level = v
						m.configChanged = true
					}
				}
			},
		},
		{
			label: "Show disclaimer",
			get:   func(m *Model) string { return boolStr(m.cfg.UI.ShowDisclaimer) },
			set: func(m *Model, _ string) {
				m.cfg.UI.ShowDisclaimer = !m.cfg.UI.ShowDisclaimer
				m.configChanged = true
			},
			isBool: true,
		},
		{
			label: "Update check",
			get:   func(m *Model) string { return boolStr(m.cfg.Update.Check) },
			set: func(m *Model, _ string) {
				m.cfg.Update.Check = !m.cfg.Update.Check
				m.configChanged = true
			},
			isBool: true,
		},
		{
			label: "Update auto-download",
			get:   func(m *Model) string { return boolStr(m.cfg.Update.Auto) },
			set: func(m *Model, _ string) {
				m.cfg.Update.Auto = !m.cfg.Update.Auto
				m.configChanged = true
			},
			isBool: true,
		},
	}
}

// confirmKindShutdown is the discriminator on confirmKind for the
// "stop daemon" confirmation. Kept as a named constant so the handler and
// renderer cannot drift from each other on a typo.
const confirmKindShutdown = "shutdown"

// confirmKindRestartPane is the discriminator on confirmKind for the
// restart-pane confirm (default Alt+R): kill + respawn the pane's process
// in place via the same MsgRestartPaneReq the MCP restart_pane tool uses.
const confirmKindRestartPane = "restart-pane"

// confirmKindApplyUpdate is the discriminator on confirmKind for the
// "apply staged update now" confirm (About → Update / startup notice →
// Update now). Accepting quits the TUI with an apply-intent flag;
// cmd/quil/main.go runs the swap after tea.Program exits.
const confirmKindApplyUpdate = "apply-update"

// confirmKindDestroyProject is the discriminator on confirmKind for the
// "destroy project" confirm (sidebar context menu → Destroy project…).
// Destroying a project takes every tab and pane under it, so — unlike a
// single pane/tab close — it never fires straight off a keystroke; see
// confirmDestroyProject in projectdialog.go.
const confirmKindDestroyProject = "destroy-project"

// confirmKindUpgradeDest is the discriminator on confirmKind for the
// "this host runs an older quil" prompt. confirmID carries the DEST.
//
// The offer itself is not new — installOffer and installDest have handled both
// ErrRemoteQuilMissing and ErrRemoteVersionMismatch since the runtime-connect
// work. What was missing is an entry point for the two paths a RESTART goes
// through: the launch dial and the reconnect ladder both classified the
// mismatch, seeded an offline row, and stopped. The launch path is right not to
// install unprompted — that would make provisioning another machine a side
// effect of opening the client — but a prompt is not a side effect, and without
// one the user got a parked row and no way to act on it inside the tool.
const confirmKindUpgradeDest = "upgrade-dest"

// shortcutRow is one line of the F1 -> Shortcuts list: a chord and what it
// does, or — when full is set — one sentence that takes the whole inner width
// because it has no key half worth a fixed column.
type shortcutRow struct {
	key  string
	desc string
	full bool
}

// srow is an ordinary two-column row. Only conflicts set full, and they build
// their row explicitly, so every other call site says so by using this.
func srow(key, desc string) shortcutRow { return shortcutRow{key: key, desc: desc} }

// shortcutsList derives the F1 -> Shortcuts rows from the action registry, so
// the dialog cannot drift from what the keys actually do (it used to be a
// hand-maintained line per action — TestShortcutsList_CoversEveryProjectBinding
// existed because seven of eight project bindings were added to the config
// but never copied into this function).
func shortcutsList(m *Model) []shortcutRow {
	var list []shortcutRow

	// Conflicts first: a dropped binding is the one thing a user cannot
	// discover any other way, so it has to be the first thing they see, not
	// buried under 40-odd rows of bindings that DO work. full: the message is
	// a sentence and needs the cells the key column would take.
	for _, c := range m.keyConflicts {
		list = append(list, shortcutRow{key: "!", desc: c.String(), full: true})
	}

	// m.keymap.Display renders every binding on an action joined with " / "
	// so the help text stays readable when an action has multiple bindings
	// (e.g. the macOS-friendly fallback on Rename pane), and canonically
	// ("ctrl+v", not "Ctrl+V") because that is the chord form Display parses
	// to and reports.
	//
	// Display returns the binding an action REQUESTED, not whether it WON
	// dispatch — an action that lost a duplicate-binding conflict still
	// shows the chord it asked for, one that will never fire because the
	// conflict winner owns it at runtime. That is deliberate: the conflict
	// row above already carries the truth for that chord, and showing the
	// requested binding next to a "! duplicate binding" warning is more
	// useful than blanking the row, which would read as "unbound" rather
	// than "misconfigured" — the two are different problems with different
	// fixes.
	groups, byGroup := keymap.ActionsByGroup()
	for _, g := range groups {
		var bucket []shortcutRow
		for _, a := range byGroup[g] {
			if a.Hidden {
				continue // e.g. json.transform: registered, no dispatch site
			}
			if keys := m.keymap.Display(a.ID); keys != "" {
				bucket = append(bucket, srow(keys, a.Label))
			}
		}
		if len(bucket) == 0 {
			continue // never render an empty heading
		}
		list = append(list, srow("", g))
		list = append(list, bucket...)
	}

	// Non-action rows: behaviour with no registry action behind it —
	// handleKey intercepts these outside the two dispatch tiers (Ctrl+N,
	// Alt+1..9, F1 itself), or they belong to terminal/editor selection,
	// which the registry does not model. Carried verbatim from the
	// pre-registry list.
	list = append(list,
		srow("", ""),
		srow("", "── Built-in keys ──"),
		srow("Ctrl+N", "New typed pane"),
		srow("Alt+1..9", "Switch to tab N"),
		srow("F1", "Help / About"),
		srow("Tab / Shift+Tab", "→ PTY (shell completion, Claude Code modes)"),
		srow("Shift+Arrows", "Select text"),
		srow("Ctrl+Shift+←→", "Select word"),
		srow("Ctrl+Alt+Shift+←→", "Select 3 words"),
		srow("Ctrl+←→", "Jump word"),
		srow("Ctrl+Alt+←→", "Jump 3 words"),
		srow("Enter", "Copy selection"),
		srow("Right-click", "Copy selection"),
		srow("Esc", "Clear selection"),
		srow("", ""),
		srow("", "── Editor ──"),
		srow("Shift+Arrows", "Select text (editor)"),
		srow("Ctrl+Shift+←→", "Select word (editor)"),
		srow("Ctrl+Alt+Shift+←→", "Select 3 words (editor)"),
		srow("Enter", "Copy selection (editor)"),
		srow("Ctrl+X", "Cut selection (editor)"),
		srow("Ctrl+V", "Paste (editor)"),
		srow("Ctrl+A", "Select all (editor)"),
		srow("Ctrl+S", "Save (editor)"),
	)
	return list
}

// --- Input handling ---

func (m Model) handleDialogKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch m.dialog {
	case dialogAbout:
		return m.handleAboutKey(msg)
	case dialogSettings:
		return m.handleSettingsKey(msg)
	case dialogShortcuts:
		return m.handleShortcutsKey(msg)
	case dialogConfirm:
		return m.handleConfirmKey(msg)
	case dialogCreatePane:
		return m.handleCreatePaneKey(msg)
	case dialogCreatePaneSetup:
		return m.handleCreatePaneSetupKey(msg)
	case dialogPluginError:
		return m.handlePluginErrorKey(msg)
	case dialogInstanceForm:
		return m.handleInstanceFormKey(msg)
	case dialogPlugins:
		return m.handlePluginsKey(msg)
	case dialogTOMLEditor:
		return m.handleTOMLEditorKey(msg)
	case dialogLogViewer:
		return m.handleLogViewerKey(msg)
	case dialogDisclaimer:
		return m.handleDisclaimerKey(msg)
	case dialogPluginMigration:
		return m.handleMigrationKey(msg)
	case dialogMemory:
		return m.handleMemoryDialogKey(msg)
	case dialogGitRepoPick:
		return m.handleGitRepoPickKey(msg)
	case dialogCommandHistory:
		return m.handleCommandHistoryKey(msg)
	case dialogUpdateNotice:
		return m.handleUpdateNoticeKey(msg)
	case dialogCommandPalette:
		return m.handleCommandPaletteKey(msg)
	case dialogProjectNew, dialogProjectRename:
		return m.handleProjectDialogKey(msg)
	case dialogProjectPick:
		return m.handleProjectPickKey(msg)
	case dialogWhatsNew:
		return m.handleWhatsNewKey(msg)
	}
	return m, nil
}

// handleCommandHistoryKey processes a key press while the input-history modal
// is open. Mirrors handleMemoryDialogKey's raw-string key idiom. Every cursor
// move ends in syncHistoryScroll: the list holds up to 200 entries against a
// window of ~30 rows, so navigation that does not move the window can only walk
// off-screen.
func (m Model) handleCommandHistoryKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	last := len(m.history.entries) - 1
	// A page is the visible window minus one row of overlap, so the row the
	// user was reading stays on screen and gives them their bearings.
	page := m.historyVisibleRows() - 1
	if page < 1 {
		page = 1
	}
	switch msg.String() {
	case "esc":
		m.dialog = dialogNone
		return m, tea.ClearScreen
	case "r":
		// Retry after a timeout. Guarded on timedOut so "r" stays free for a
		// future binding in the normal list state.
		if m.history.timedOut {
			m.history.timedOut = false
			m.history.loading = true
			return m, m.requestHistory(m.history.paneID)
		}
		return m, nil
	case "up", "k":
		m.history.cursor--
	case "down", "j":
		m.history.cursor++
	case "pgup":
		m.history.cursor -= page
	case "pgdown":
		m.history.cursor += page
	case "home":
		m.history.cursor = 0
	case "end":
		m.history.cursor = last
	case "enter":
		if m.history.supported && m.history.cursor >= 0 && m.history.cursor <= last {
			return m, m.requestHistoryEntry(m.history.paneID, m.history.entries[m.history.cursor].TsMs)
		}
		return m, nil
	default:
		return m, nil
	}
	// Clamp once, here, rather than per branch — a page jump near either end
	// lands past the list by design and is expected to stop at it.
	if m.history.cursor > last {
		m.history.cursor = last
	}
	if m.history.cursor < 0 {
		m.history.cursor = 0
	}
	m.syncHistoryScroll()
	return m, nil
}

// aboutUpdateIndex is the row index of the dynamic update row in the F1 →
// About (root) menu; aboutStopDaemonIndex sits below it. Named constants
// so handleAboutKey, lastAboutItem, and the confirm-dialog Esc handlers
// cannot drift on the indices.
const aboutUpdateIndex = 7

// aboutWhatsNewIndex is the row index of "What's New" in the F1 → About (root)
// menu. It sits directly below the dynamic update row because the two are the
// same subject seen from either side of an upgrade.
const aboutWhatsNewIndex = 8

// aboutStopDaemonIndex is the row index of "Stop daemon" in the F1 → About
// (root) menu. Stop daemon was promoted from the nested Settings list to the
// root menu so it sits alongside Settings/Shortcuts/Plugins. Kept as a named
// constant so handleAboutKey, lastAboutItem, and the confirm-dialog Esc
// handler cannot drift on the index.
const aboutStopDaemonIndex = 9

func (m Model) handleAboutKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	const lastAboutItem = aboutStopDaemonIndex // 0:Settings 1:Shortcuts 2:Plugins 3:Memory 4:Client 5:Daemon 6:MCP 7:Update 8:What's New 9:Stop daemon
	switch msg.String() {
	case "esc":
		m.dialog = dialogNone
	case "up", "k":
		if m.dialogCursor > 0 {
			m.dialogCursor--
		}
	case "down", "j":
		if m.dialogCursor < lastAboutItem {
			m.dialogCursor++
		}
	case "enter":
		switch m.dialogCursor {
		case 0:
			m.dialog = dialogSettings
			m.dialogCursor = 0
		case 1:
			m.dialog = dialogShortcuts
			m.dialogCursor = 0
		case 2:
			m.dialog = dialogPlugins
			m.dialogCursor = 0
		case 3:
			m = m.openMemoryDialog()
			return m, m.refreshMemory()
		case 4:
			return m.openLogViewer("Client log", filepath.Join(config.QuilDir(), "quil.log"))
		case 5:
			return m.openLogViewer("Daemon log", filepath.Join(config.QuilDir(), "quild.log"))
		case 6:
			return m.openMCPLogsViewer()
		case aboutUpdateIndex:
			return m.handleUpdateAction()
		case aboutWhatsNewIndex:
			// Opened by hand rather than by an upgrade, so the window is the
			// single most recent release.
			if w, ok := latestWindow(); ok {
				m.openWhatsNew(w)
			}
			return m, nil
		case aboutStopDaemonIndex:
			// Stop daemon: route to the shutdown confirm. Enter here only
			// opens the confirm; the confirm itself requires `y` to fire
			// MsgShutdown (see handleConfirmKey).
			m.dialog = dialogConfirm
			m.confirmKind = confirmKindShutdown
			m.resetConfirmWorktrees()
			m.confirmID = ""
			m.confirmName = ""
			m.dialogCursor = 0
		}
	}
	return m, nil
}

func (m Model) handleDisclaimerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.dialog = dialogNone
		m.dialogCursor = 0
		return m, tea.ClearScreen
	case "enter":
		if m.dialogCursor == 1 {
			// "Don't show again"
			m.cfg.UI.ShowDisclaimer = false
			m.configChanged = true
		}
		m.dialog = dialogNone
		m.dialogCursor = 0
		return m, tea.ClearScreen
	case "left":
		if m.dialogCursor > 0 {
			m.dialogCursor--
		}
	case "right":
		if m.dialogCursor < 1 {
			m.dialogCursor++
		}
	case "tab":
		m.dialogCursor = (m.dialogCursor + 1) % 2
	}
	return m, nil
}

func (m Model) handleSettingsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	fields := settingsFields()
	key := msg.String()

	if m.dialogEdit {
		switch {
		case key == "esc":
			m.dialogEdit = false
			m.dialogInput = ""
		case key == "enter":
			field := fields[m.dialogCursor]
			field.set(&m, m.dialogInput)
			m.dialogEdit = false
			m.dialogInput = ""
			// Pushed after ANY settings commit, not just the two overlay
			// rows: the payload is two ints, so singling out which row was
			// edited buys nothing and risks missing a future row that also
			// wants live application. overlayPolicyCmd is nil when there is
			// no connection (tests), so this never adds a spurious command.
			policyCmd := m.overlayPolicyCmd()
			if field.relayout {
				// Same sequence, and same ordering, as toggleProjectSidebar:
				// resizeTabs FIRST because it is what WRITES pane.Width and
				// tab.CanvasW — resizeAllPanes only reads and ships them, so
				// without it every background tab keeps its pre-edit PTY
				// size. ClearScreen because every column right of the strip
				// shifts in one frame, which is the shift Bubble Tea's cell
				// diff mis-tracks.
				m.resizeTabs()
				return m, tea.Batch(tea.ClearScreen, m.resizeAllPanes(), policyCmd)
			}
			return m, policyCmd
		case key == "backspace":
			if len(m.dialogInput) > 0 {
				m.dialogInput = m.dialogInput[:len(m.dialogInput)-1]
			}
		case m.isAction(key, "pane.paste"):
			return m, m.pasteToDialog()
		default:
			if len(key) == 1 {
				m.dialogInput += key
			}
		}
		return m, nil
	}

	switch key {
	case "esc":
		m.dialog = dialogAbout
		m.dialogCursor = 0
	case "up", "k":
		if m.dialogCursor > 0 {
			m.dialogCursor--
		}
	case "down", "j":
		if m.dialogCursor < len(fields)-1 {
			m.dialogCursor++
		}
	case "enter", " ":
		f := fields[m.dialogCursor]
		if f.isBool {
			f.set(&m, "")
		} else {
			m.dialogEdit = true
			m.dialogInput = f.get(&m)
		}
	}
	return m, nil
}

func (m Model) handleShortcutsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	total := len(shortcutsList(&m))
	page := m.shortcutsVisibleRows()
	switch msg.String() {
	case "esc":
		m.dialog = dialogAbout
		m.dialogCursor = 0
		// Reset so re-opening starts at the top rather than wherever the last
		// visit left off — the list is reference material, not a work queue.
		m.shortcutsCursor, m.shortcutsScroll = 0, 0
	case "up", "k", "ctrl+p":
		if m.shortcutsCursor > 0 {
			m.shortcutsCursor--
		}
		m.syncShortcutsScroll()
	case "down", "j", "ctrl+n":
		if m.shortcutsCursor < total-1 {
			m.shortcutsCursor++
		}
		m.syncShortcutsScroll()
	case "pgup":
		m.shortcutsCursor -= page
		if m.shortcutsCursor < 0 {
			m.shortcutsCursor = 0
		}
		m.syncShortcutsScroll()
	case "pgdown":
		m.shortcutsCursor += page
		if m.shortcutsCursor > total-1 {
			m.shortcutsCursor = total - 1
		}
		m.syncShortcutsScroll()
	case "home", "g":
		m.shortcutsCursor = 0
		m.syncShortcutsScroll()
	case "end", "G":
		m.shortcutsCursor = total - 1
		m.syncShortcutsScroll()
	}
	return m, nil
}

func (m Model) handleConfirmKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case " ", "space", "w":
		// Both spellings, because Bubble Tea v2 reports the space key as
		// "space" while a pasted or synthesised one can arrive as " " — the
		// same pair the settings dialog matches.
		//
		// Toggles the "also delete the worktree" row. A no-op when the dialog
		// offers none, so the other confirm kinds are unaffected — and space
		// rather than a letter because it is what a checkbox answers to, with
		// `w` as the discoverable alias the footer names.
		//
		// Deliberately NOT a second key that also confirms: arming and
		// confirming must stay two separate keystrokes, or the toggle is no
		// safer than a plain destructive default.
		if len(m.confirmWorktrees) > 0 {
			m.confirmRemoveWorktree = !m.confirmRemoveWorktree
		}
		return m, nil
	case "esc", "n":
		// Cleared on EVERY route out, including the branches below that return
		// early to another dialog. These fields outlive the dialog, and an
		// inherited arm is a deletion the next close never offered.
		m.resetConfirmWorktrees()
		// Return to appropriate dialog based on confirm kind
		if m.confirmKind == "instance" {
			m.dialog = dialogCreatePane
			m.createPaneStep = 2
			m.dialogCursor = 0
			return m, nil
		}
		if m.confirmKind == confirmKindShutdown {
			// Back to the About (root) menu where Stop daemon was triggered,
			// cursor restored to that row.
			m.dialog = dialogAbout
			m.dialogCursor = aboutStopDaemonIndex
			return m, nil
		}
		if m.confirmKind == confirmKindApplyUpdate {
			// Back to where the press came from — the About menu with the
			// cursor on the update row when the user is still there, otherwise
			// the panes, because this confirm can open by itself. The detail
			// line describes ONE answer from the daemon, so it must not
			// outlive this dialog and reappear on an unrelated later confirm.
			m.dialog = m.applyConfirmReturn
			m.dialogCursor = 0
			if m.dialog == dialogAbout {
				m.dialogCursor = aboutUpdateIndex
			}
			m.confirmDetail = ""
			return m, tea.ClearScreen
		}
		m.dialog = dialogNone
		if m.confirmKind == confirmKindUpgradeDest {
			// Declining leaves the host parked — the row keeps saying what is
			// wrong — and does NOT mark it installed, so a later reconnect may
			// ask again. What it must not do is swallow the rest of the queue:
			// a client update leaves every configured host stale at once, and
			// dismissing the first would otherwise hide the others entirely.
			m.confirmDetail = ""
			m.promptNextUpgrade()
		}
		return m, nil
	case "enter", "y":
		kind := m.confirmKind
		id := m.confirmID
		// Read BEFORE the reset below, like every other value this branch
		// carries into its command.
		removeWorktree := m.confirmRemoveWorktree
		m.resetConfirmWorktrees()

		// Stop-daemon requires explicit `y` — Enter is the universal
		// select/commit key across every menu and toggle, so accepting it
		// here would let finger-memory Enter (these dialogs are keyboard-only)
		// kill the daemon + SIGHUP every pane child. `y` is a deliberate
		// keystroke a user does not press accidentally.
		if kind == confirmKindShutdown && msg.String() != "y" {
			return m, nil
		}

		// Upgrade a host: the same push `quil remote setup` performs, run from
		// inside the tool so the user never leaves it for a shell.
		//
		// Requires explicit `y` for the reason shutdown does, and more so here:
		// this dialog opens BY ITSELF at launch, so it can be on screen when
		// the user's hands are already moving. Enter is the universal commit
		// key, and accepting it would let one reflex restart a remote daemon and
		// kill whatever was running in its panes.
		if kind == confirmKindUpgradeDest {
			if msg.String() != "y" {
				return m, nil
			}
			m.dialog = dialogNone
			m.confirmDetail = ""
			// The once-per-host guard is shared with the New Project dialog's
			// offer: a daemon still reporting the old version after an upgrade
			// did not restart, and pushing the same archive again cannot change
			// that — install, retry, same error, install.
			if m.installedDests == nil {
				m.installedDests = map[string]bool{}
			}
			m.installedDests[id] = true
			if p := m.projectForDest(id); p != nil && p.Offline != nil {
				p.Offline.Detail = "upgrading — the daemon on that host restarts…"
			}
			return m, m.installDest(id)
		}

		// Stop-daemon: fire MsgShutdown and quit the TUI. The daemon's
		// MsgShutdown handler writes the final snapshot in its stop defers,
		// and panes respawn from that snapshot on next launch. Send is
		// performed synchronously (not via tea.Batch which gives no
		// ordering guarantee) so the message lands BEFORE tea.Quit returns
		// control to cmd/quil/main.go where `defer client.Close()` would
		// otherwise close the socket out from under the in-flight write.
		// One-shot ~150-byte write to a local Unix socket — no UI-thread
		// concern. Send errors are logged but do not block the quit; the
		// user explicitly asked to stop, the TUI exits either way.
		if kind == confirmKindShutdown {
			m.dialog = dialogNone
			if m.client != nil {
				req, _ := ipc.NewMessage(ipc.MsgShutdown, nil)
				if sendErr := m.client.Send(req); sendErr != nil {
					log.Printf("stop daemon: send shutdown: %v", sendErr)
				}
			}
			return m, tea.Quit
		}

		// Restart-pane: fire the same request the MCP restart_pane tool
		// uses — the daemon kills the old PTY and respawns with the
		// plugin's resume strategy (AI panes resume their session). Sent
		// synchronously like MsgShutdown above; the listener logs the
		// daemon's MsgRestartPaneResp.
		if kind == confirmKindRestartPane {
			m.dialog = dialogNone
			if m.client != nil {
				req, reqErr := ipc.NewMessage(ipc.MsgRestartPaneReq, ipc.RestartPaneReqPayload{PaneID: id})
				if reqErr != nil {
					log.Printf("restart pane %s: marshal: %v", id, reqErr)
					return m, nil
				}
				if sendErr := m.sendForPane(id, req); sendErr != nil {
					log.Printf("restart pane %s: send: %v", id, sendErr)
				}
			}
			return m, nil
		}

		// Apply-update: quit the TUI with the apply intent set; main.go
		// performs verify → swap → respawn after the program exits (the
		// terminal must be released before the wrapper respawn).
		//
		// Requires explicit `y` for the reason shutdown does, and for one more:
		// this confirm is opened by the daemon's answer to a stage request, so
		// it can arrive a whole download after the keypress that asked for it,
		// onto a user who has gone back to typing in a pane.
		if kind == confirmKindApplyUpdate {
			if msg.String() != "y" {
				return m, nil
			}
			m.dialog = dialogNone
			m.confirmDetail = ""
			m.applyUpdateOnExit = true
			return m, tea.Quit
		}

		// Destroy-project: fires MsgDestroyProject at the OWNING daemon —
		// resolved here, not inside confirmDestroyProject, for the same
		// reason the pane/tab dest is resolved below rather than closed over:
		// a broadcast between opening the confirm and accepting it could have
		// pruned the project. sendForDest, not a raw Origin assignment — see
		// its doc comment on why an unstamped local send would be wrong the
		// moment a remote project is active.
		if kind == confirmKindDisconnectHost {
			m.dialog = dialogNone
			// confirmID carries the DEST here, not a project id: disconnecting
			// takes every project on that machine, so the one that happened to
			// be right-clicked is not the target.
			m.disconnectDest(id)
			log.Printf("disconnected host %q", id)
			return m, tea.ClearScreen
		}

		if kind == confirmKindDestroyProject {
			m.dialog = dialogNone
			if m.client != nil {
				if !m.projectActionable(m.projectByID(id)) {
					log.Printf("destroy project %s: refused, its host is offline", id)
					return m, nil
				}
				req, reqErr := ipc.NewMessage(ipc.MsgDestroyProject, ipc.DestroyProjectPayload{ProjectID: id})
				if reqErr != nil {
					log.Printf("destroy project %s: marshal: %v", id, reqErr)
					return m, nil
				}
				dest := m.destOfProject(id)
				// Logged on success too. A destroy that appears not to happen
				// is ambiguous from outside — the daemon cannot be left with
				// no project, so destroying the last one on a host bootstraps
				// a fresh "Default" that looks exactly like a delete which did
				// nothing. This line is what separates the two afterwards.
				if sendErr := m.sendForDest(dest, req); sendErr != nil {
					log.Printf("destroy project %s on dest %q: send: %v", id, dest, sendErr)
				} else {
					log.Printf("destroy project %s on dest %q: sent", id, dest)
				}
			}
			return m, nil
		}

		// Handle instance deletion locally (no IPC needed)
		if kind == "instance" {
			pluginName := m.selectedPlugin
			instances := m.instanceStore[pluginName]
			for i, inst := range instances {
				if inst.ID == id {
					m.instanceStore[pluginName] = append(instances[:i], instances[i+1:]...)
					break
				}
			}
			if err := SaveInstances(config.InstancesPath(), m.instanceStore); err != nil {
				log.Printf("save instances: %v", err)
			}
			m.dialog = dialogCreatePane
			m.createPaneStep = 2
			m.dialogCursor = 0
			return m, nil
		}

		m.dialog = dialogNone
		// The dest is resolved HERE, on the Update goroutine, rather than
		// inside the command: the confirm is what named the target, and by the
		// time the command runs a broadcast may already have pruned it.
		var dest string
		switch kind {
		case "pane":
			dest = m.destOfPane(id)
		case "tab":
			dest = m.destOfTab(id)
		}
		return m, func() tea.Msg {
			switch kind {
			case "pane":
				req, _ := ipc.NewMessage(ipc.MsgDestroyPane, ipc.DestroyPanePayload{
					PaneID: id,
					// The only field on this wire that deletes a directory. It
					// is a bool: the daemon re-derives WHICH worktree from its
					// own ownership record, so nothing here names a path.
					RemoveWorktree: removeWorktree,
				})
				m.sendForDest(dest, req)
			case "tab":
				req, _ := ipc.NewMessage(ipc.MsgDestroyTab, ipc.DestroyTabPayload{
					TabID:          id,
					RemoveWorktree: removeWorktree,
				})
				m.sendForDest(dest, req)
			}
			return nil
		}
	}
	return m, nil
}

// handleGitRepoPickKey drives the Alt+G / Alt+H multi-repo picker: a plain
// list of git repos found near the active pane's CWD. Enter opens the overlay
// for the highlighted repo, running whichever tool opened the picker
// (m.repoPickPlugin); Esc cancels.
func (m Model) handleGitRepoPickKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "esc":
		m.dialog = dialogNone
		m.repoPickCandidates = nil
		m.repoPickPlugin = ""
		return m, tea.ClearScreen
	case "up", "k":
		if m.dialogCursor > 0 {
			m.dialogCursor--
		}
		return m, nil
	case "down", "j":
		if m.dialogCursor < len(m.repoPickCandidates)-1 {
			m.dialogCursor++
		}
		return m, nil
	case "enter":
		if m.dialogCursor >= len(m.repoPickCandidates) {
			return m, nil
		}
		repo := m.repoPickCandidates[m.dialogCursor]
		pick := m.repoPickPlugin
		m.dialog = dialogNone
		m.repoPickCandidates = nil
		m.repoPickPlugin = ""
		tab := m.activeTabModel()
		if tab == nil {
			return m, tea.ClearScreen
		}
		// createOverlay uses a pointer receiver so it mutates m directly
		// (Go takes &m on a value-receiver local variable). The returned m
		// reflects all mutations including pendingOverlayShow.
		return m, tea.Batch(tea.ClearScreen, m.createOverlay(tab, repo, pick))
	}
	return m, nil
}

// --- Rendering ---

func (m Model) renderDialog() string {
	// Determine dialog width: plugin-specific for instance screens only
	width := dialogWidth
	if m.dialog == dialogTOMLEditor {
		width = 74
	} else if m.dialog == dialogShortcuts {
		width = shortcutsDialogWidth
	} else if m.selectedPlugin != "" && (m.dialog == dialogInstanceForm || (m.dialog == dialogCreatePane && m.createPaneStep == 2)) {
		if p := m.pluginRegistry.Get(m.selectedPlugin); p != nil && p.Display.DialogWidth > 0 {
			width = p.Display.DialogWidth
		}
	}

	var content string

	switch m.dialog {
	case dialogAbout:
		content = m.renderAboutDialog()
	case dialogSettings:
		content = m.renderSettingsDialog()
	case dialogShortcuts:
		content = m.renderShortcutsDialog()
	case dialogConfirm:
		content = m.renderConfirmDialog()
	case dialogCreatePane:
		content = m.renderCreatePaneDialog()
		// Auto-fit so plugin rows never wrap — e.g. a greyed entry that
		// appends a homepage URL can exceed the default width. Padding(1,2)
		// costs 4 cells; +2 margin guards reflow's long-token edge case.
		if w := maxContentLineWidth(content) + 6; w > width {
			width = w
		}
	case dialogCreatePaneSetup:
		width = m.setupDialogWidth() // grows to fit the longest toggle label
		content = m.renderCreatePaneSetupDialog()
	case dialogPluginError:
		content = m.renderPluginErrorDialog()
	case dialogInstanceForm:
		content = m.renderInstanceFormDialog()
	case dialogPlugins:
		content = m.renderPluginsDialog()
	case dialogTOMLEditor:
		// Rendered in View() as full-screen, not here
	case dialogDisclaimer:
		width = disclaimerWidth
		content = m.renderDisclaimerDialog()
	case dialogMemory:
		width = 80
		content = m.renderMemoryDialog()
	case dialogGitRepoPick:
		width = gitRepoPickWidth
		content = m.renderGitRepoPickDialog()
	case dialogCommandHistory:
		width = historyDialogWidth
		content = m.renderCommandHistory()
	case dialogUpdateNotice:
		width = dialogWidth
		content = m.renderUpdateNoticeDialog()
	case dialogWhatsNew:
		width = whatsNewWidth(m.lastWidth)
		content = m.renderWhatsNewDialog()
	case dialogCommandPalette:
		width = paletteWidth
		content = renderCommandPalette(m)
	case dialogProjectNew, dialogProjectRename:
		content = m.renderProjectDialog()
	case dialogProjectPick:
		width = projectPickWidth
		content = m.renderProjectPickDialog()
	}

	// Never render wider than the terminal (border adds +2 outside Width).
	if m.width > 2 && width > m.width-2 {
		width = m.width - 2
	}

	box := dialogBorder.Width(width).Render(content)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

// maxContentLineWidth returns the widest visible line in content (ANSI-aware),
// used to auto-size a dialog box to its content.
func maxContentLineWidth(content string) int {
	max := 0
	for _, line := range strings.Split(content, "\n") {
		if w := lipgloss.Width(line); w > max {
			max = w
		}
	}
	return max
}

func (m Model) renderAboutDialog() string {
	var b strings.Builder

	title := dialogTitle.Render("Quil v" + m.version)
	link := dialogSubtle.Render("github.com/artyomsv/quil")

	b.WriteString(lipgloss.PlaceHorizontal(dialogWidth, lipgloss.Center, title))
	b.WriteByte('\n')
	b.WriteString(lipgloss.PlaceHorizontal(dialogWidth, lipgloss.Center, link))
	b.WriteString("\n\n")

	items := []string{
		"Settings",
		"Shortcuts",
		"Plugins",
		"Memory",
		"View client log",
		"View daemon log",
		"View MCP logs",
		aboutUpdateLabel(m.activeUpdateInfo(), m.version, m.remoteModeFor(m.activeDest())),
		"What's New",
		"Stop daemon",
	}
	for i, item := range items {
		cursor := "  "
		style := dialogNormal
		if i == m.dialogCursor {
			cursor = "> "
			style = dialogSelected
		}
		b.WriteString(cursor + style.Render(item) + "\n")
	}

	b.WriteByte('\n')
	b.WriteString(dialogSubtle.Render("Esc close"))

	return b.String()
}

func (m Model) renderDisclaimerDialog() string {
	var b strings.Builder
	w := disclaimerWidth

	// Title
	title := dialogTitle.Render("Quil v" + m.version + " -- Early Beta")
	b.WriteString(lipgloss.PlaceHorizontal(w, lipgloss.Center, title))
	b.WriteString("\n\n")

	// Beta notice
	b.WriteString(dialogSubtle.Render("  This software is in early beta. Some features may"))
	b.WriteByte('\n')
	b.WriteString(dialogSubtle.Render("  not work as expected. Linux and macOS support has"))
	b.WriteByte('\n')
	b.WriteString(dialogSubtle.Render("  not been fully tested yet."))
	b.WriteString("\n\n")

	// Separator
	b.WriteString(dialogSubtle.Render("  " + strings.Repeat("-", w-4)))
	b.WriteString("\n\n")

	// Random tip
	tip := disclaimerTips[m.disclaimerTipIdx]
	b.WriteString(dialogSelected.Render("  Tip: " + tip.title))
	b.WriteByte('\n')
	for _, item := range tip.items {
		b.WriteString(dialogNormal.Render("    - " + item))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')

	// Separator
	b.WriteString(dialogSubtle.Render("  " + strings.Repeat("-", w-4)))
	b.WriteByte('\n')

	// Buttons
	okLabel := "  OK  "
	dontShowLabel := "  Don't show again  "
	if m.dialogCursor == 0 {
		okLabel = dialogSelected.Render("[" + okLabel + "]")
		dontShowLabel = dialogSubtle.Render(" " + dontShowLabel + " ")
	} else {
		okLabel = dialogSubtle.Render(" " + okLabel + " ")
		dontShowLabel = dialogSelected.Render("[" + dontShowLabel + "]")
	}
	buttons := okLabel + "    " + dontShowLabel
	b.WriteString(lipgloss.PlaceHorizontal(w, lipgloss.Center, buttons))
	b.WriteByte('\n')

	// Hint
	b.WriteByte('\n')
	b.WriteString(dialogSubtle.Render("  Tab/Arrows navigate   Enter select   Esc close"))

	return b.String()
}

func (m Model) renderSettingsDialog() string {
	var b strings.Builder

	b.WriteString(dialogTitle.Render("Settings"))
	b.WriteByte('\n')
	b.WriteString(dialogSubtle.Render("  some changes persist to config.toml"))
	b.WriteString("\n\n")

	fields := settingsFields()
	for i, f := range fields {
		cursor := "  "
		labelStyle := dialogLabelStyle
		if i == m.dialogCursor {
			cursor = "> "
			labelStyle = labelStyle.Foreground(lipgloss.Color("230")).Bold(true)
		}

		val := f.get(&m)
		var valRendered string
		if m.dialogEdit && i == m.dialogCursor {
			valRendered = dialogEditStyle.Render(m.dialogInput + "│")
		} else {
			valRendered = dialogValStyle.Render(val)
		}

		b.WriteString(cursor + labelStyle.Render(f.label) + valRendered + "\n")
	}

	b.WriteByte('\n')
	b.WriteString(dialogSubtle.Render("  Update check/auto-download apply on next daemon restart"))
	b.WriteByte('\n')
	hint := "↑↓ navigate  Enter edit  Esc back"
	b.WriteString(dialogSubtle.Render(hint))

	return b.String()
}

// shortcutsChromeRows is every row the Shortcuts modal spends outside the list:
// the rounded border (2), dialogBorder's Padding(1,2) top and bottom (2), the
// title, the blank row under it, the blank row above the footer, the footer,
// and one spare so the centered box never sits flush against the terminal edge.
const shortcutsChromeRows = 8

// shortcutsDialogWidth runs wider than the standard 60. dialogKeyStyle is a
// fixed dialogKeyColWidth cells, so at 60 a description gets 32 — and eight
// entries already exceeded that, including "Command palette (fuzzy-find any
// action)" and the Tab → PTY note. Each wrapped onto a second line, which is
// why counting ENTRIES against the height budget under-counted the box and let
// it overflow even after a window was added. One entry must be one line for
// the row arithmetic to mean anything.
//
// 100, not the 74 that was enough for the bindings alone: the conflict rows
// carry a sentence, not a label — kind, chord, winner, loser and consequence —
// and 74 cut every one of them mid-clause ("duplicate binding: \"ctrl+w\"
// resolves to pan…"), which is the half of the message that says what to do.
// The longest one the registry can produce runs ~88 cells; a conflict row
// spends nothing on the key column (shortcutsFullRowWidth), so at 100 it gets
// 90 and that sentence lands whole. It also takes the ordinary description
// budget from 44 — exactly the longest label, i.e. no headroom at all — to 70.
// 100 is the width historyDialogWidth already uses for the same reason.
const shortcutsDialogWidth = 100

// shortcutsRowIndent is the two spaces every shortcut row starts with.
const shortcutsRowIndent = 2

// shortcutsDescWidth is what is left for the description after the box chrome,
// the row indent and the fixed-width key column — at the width the box ACTUALLY
// gets, which on a narrow terminal is not the preferred one.
//
// It goes through dialogInnerWidth for the reason that helper exists: renderDialog
// clamps the box to m.width-2, and a budget derived from the preferred 74 keeps
// truncating to a width the box no longer has. The rows then wrap, and the height
// arithmetic that counts one line per entry under-counts — which is the overflow
// the window was added to fix, returning below 76 columns. Measured at 40: nine
// rows past the bottom edge.
func (m Model) shortcutsDescWidth() int {
	if w := dialogInnerWidth(m.width, shortcutsDialogWidth) - shortcutsRowIndent - dialogKeyColWidth; w > 1 {
		return w
	}
	return 1
}

// shortcutsFullRowWidth is the budget for a row that has no key half — the
// conflict warnings, whose "key" is a bare "!". Spending dialogKeyColWidth on
// one exclamation mark cost those rows 22 of the cells their message needed,
// and they are the only rows in this dialog whose text a user cannot get any
// other way: a binding row that truncates still shows its chord and its label,
// a truncated conflict shows neither the winner nor the consequence.
func (m Model) shortcutsFullRowWidth() int {
	if w := dialogInnerWidth(m.width, shortcutsDialogWidth) - shortcutsRowIndent; w > 1 {
		return w
	}
	return 1
}

// shortcutsMinRows is 1 for the reason historyMinRows is: renderDialog's
// lipgloss.Place does NOT clip, so any floor above the height actually
// available manufactures the overflow it looks like it prevents.
const shortcutsMinRows = 1

// shortcutsVisibleRows is how many shortcut lines fit at the current terminal
// height. The list is the only element that can give, so it absorbs a short
// terminal rather than pushing the footer off-screen.
func (m Model) shortcutsVisibleRows() int {
	if avail := m.height - shortcutsChromeRows; avail > shortcutsMinRows {
		return avail
	}
	return shortcutsMinRows
}

// syncShortcutsScroll stores the origin shortcutsWindow would pick. Called
// after every cursor move.
func (m *Model) syncShortcutsScroll() {
	m.shortcutsScroll, _ = historyWindow(
		len(shortcutsList(m)), m.shortcutsCursor, m.shortcutsScroll, m.shortcutsVisibleRows())
}

// renderShortcutsDialog draws one window of the shortcut list.
//
// It used to write every row unconditionally — 60-odd of them once the project
// bindings were added — and lipgloss.Place does not clip, so on any terminal
// shorter than the list the box was drawn past the bottom edge. What fell off
// was the footer and, worse, whichever rows the user opened the dialog to find:
// the newest entries are appended last, so a shortcut was unreachable in exactly
// the release that introduced it.
func (m Model) renderShortcutsDialog() string {
	var b strings.Builder

	list := shortcutsList(&m)
	visible := m.shortcutsVisibleRows()
	// historyWindow rather than a second implementation: it already re-derives
	// the origin from the cursor and clamps to the end of a shrunken list, in
	// that order, and render must not depend on Update having run — a
	// WindowSizeMsg can change the row budget between them.
	start, end := historyWindow(len(list), m.shortcutsCursor, m.shortcutsScroll, visible)

	b.WriteString(dialogTitle.Render("Shortcuts"))
	b.WriteString("\n\n")

	desc := m.shortcutsDescWidth()
	full := m.shortcutsFullRowWidth()
	indent := strings.Repeat(" ", shortcutsRowIndent)
	for _, s := range list[start:end] {
		// A conflict row is one sentence, not a key/label pair — it takes the
		// whole inner width, marker included, and is red because it reports
		// something the user has to go and fix.
		if s.full {
			b.WriteString(indent)
			b.WriteString(dialogErrorStyle.Render(truncateToWidth(s.key+" "+s.desc, full)))
			b.WriteByte('\n')
			continue
		}
		// At the preferred width this truncation is a guard — every current
		// description fits — but on a narrower terminal it is the mechanism, and
		// that is why the budget has to be the box's real one. Either way a row
		// that wraps breaks the height arithmetic, which counts one line per
		// entry; that is the failure this dialog already had.
		// The key half is truncated too, and lipgloss.Width is why: dialogKeyStyle
		// is Width(dialogKeyColWidth), which PADS a short value and does nothing
		// at all to a long one. A legal chord list overflows it by honest typo —
		// `rename_pane = "alt+f2,alt+shift+r,alt+shift+q,ctrl+alt+shift+f4"`
		// renders 42 lines against a height of 40 at 100x40, wrapping one entry
		// across three and breaking the one-row-one-line arithmetic this whole
		// dialog is built on. (Escapes are handled at the parser — see
		// keymap.ParseChord — because truncateToWidth is ANSI-aware and would
		// carry one through intact.)
		b.WriteString(fmt.Sprintf("%s%s%s\n",
			indent,
			dialogKeyStyle.Render(truncateToWidth(s.key, dialogKeyColWidth)),
			dialogValStyle.Render(truncateToWidth(s.desc, desc))))
	}

	b.WriteByte('\n')
	inner := dialogInnerWidth(m.width, shortcutsDialogWidth)
	footer := "Esc back"
	// Say so when there is more, and where you are — otherwise a clipped list
	// is indistinguishable from a complete one, which is the state this dialog
	// was already in.
	if len(list) > visible {
		footer = fmt.Sprintf("↑↓ scroll · %d-%d of %d · Esc back", start+1, end, len(list))
		if lipgloss.Width(footer) > inner {
			// A shorter FORM rather than a cut, because the tail is the half
			// that says how to leave. At minTermWidth the full one is a cell
			// too wide and reflows onto a second line — which costs a row the
			// height budget already spent, so the box overflows by exactly the
			// line that was supposed to report the overflow.
			footer = fmt.Sprintf("%d-%d/%d · Esc back", start+1, end, len(list))
		}
	}
	b.WriteString(dialogSubtle.Render(truncateToWidth(footer, inner)))

	return b.String()
}

func (m Model) renderConfirmDialog() string {
	var b strings.Builder

	b.WriteString(dialogTitle.Render("Confirm"))
	b.WriteString("\n\n")

	// Footer text varies by kind: shutdown requires explicit `y` to
	// distinguish it from the Enter that commits Settings toggles, so the
	// help line must say `y confirm` to match the handler.
	footer := "Enter confirm    Esc cancel"
	switch m.confirmKind {
	case confirmKindShutdown:
		b.WriteString("  " + dialogNormal.Render("Stop the daemon?"))
		b.WriteString("\n\n")
		b.WriteString("  " + dialogSubtle.Render("This TUI window will close."))
		b.WriteString("\n")
		b.WriteString("  " + dialogSubtle.Render("Panes will respawn from the snapshot on next launch."))
		footer = "y confirm    Esc cancel"
	case confirmKindRestartPane:
		b.WriteString("  " + dialogNormal.Render(fmt.Sprintf("Restart pane %q?", m.confirmName)))
		b.WriteString("\n\n")
		b.WriteString("  " + dialogSubtle.Render("The process is killed and respawned in place."))
		b.WriteString("\n")
		b.WriteString("  " + dialogSubtle.Render("AI panes resume their recorded session."))
	case confirmKindApplyUpdate:
		// Sanitized and bounded like confirmKindDestroyProject's and
		// confirmKindUpgradeDest's names: this one now comes from a stage
		// RESPONSE, and under --remote that is a host the user may not control.
		// It is also the consent text for swapping the binaries on this machine.
		b.WriteString("  " + dialogNormal.Render(fmt.Sprintf("Apply update v%s now?",
			truncateToWidth(sanitizeRemoteText(m.confirmName), confirmDetailCap))))
		b.WriteString("\n\n")
		// Present only when the newest-release check did not settle — the
		// version above is then what is on disk rather than a confirmed
		// latest, and that is the one thing the user needs to know before
		// spending a restart on it. Same sanitize+bound treatment as the
		// upgrade confirm: under --remote this text came off another host.
		if d := m.confirmDetail; d != "" {
			b.WriteString("  " + dialogSubtle.Render(truncateToWidth(sanitizeRemoteText(d), confirmDetailCap)))
			b.WriteString("\n\n")
		}
		b.WriteString("  " + dialogSubtle.Render("The TUI restarts and the daemon respawns all panes."))
		b.WriteString("\n")
		b.WriteString("  " + dialogSubtle.Render("Claude sessions resume; running shell commands are killed."))
		// `y`, not Enter — this confirm can now appear on its own, seconds or
		// minutes after the press that started the download, by which time the
		// user's hands are back on a pane. Enter is the universal commit key,
		// so accepting it would let one reflex quit the TUI and swap binaries.
		// Same rule, and the same reason, as the shutdown and upgrade confirms.
		footer = "y confirm    Esc cancel"
	case confirmKindDestroyProject:
		b.WriteString("  " + dialogNormal.Render(fmt.Sprintf("Destroy project %q?", sanitizeRemoteText(m.confirmName))))
		b.WriteString("\n\n")
		b.WriteString("  " + dialogSubtle.Render("Every tab and pane in this project is destroyed too."))
	case confirmKindUpgradeDest:
		b.WriteString("  " + dialogNormal.Render(fmt.Sprintf("Upgrade quil on %s?", sanitizeRemoteText(m.confirmName))))
		b.WriteString("\n\n")
		// The version pair comes from the daemon's own handshake and is the
		// answer to "why can I not reach it" — the reason the row parked. It is
		// remote-influenced text, so it is sanitized AND bounded at render like
		// every other value from a host the user may not control.
		if d := m.confirmDetail; d != "" {
			b.WriteString("  " + dialogSubtle.Render(truncateToWidth(sanitizeRemoteText(d), confirmDetailCap)))
			b.WriteString("\n\n")
		}
		b.WriteString("  " + dialogSubtle.Render("Quil pushes this build over ssh."))
		b.WriteString("\n")
		// Named explicitly because an upgrade is not free the way a first
		// install is: the push stops the remote daemon, so panes over there
		// respawn and whatever was running in their shells is killed. The CLI
		// says the same before asking; this is the same warning in the place
		// the user is actually being asked.
		b.WriteString("  " + dialogSubtle.Render("Its daemon RESTARTS: panes there respawn and"))
		b.WriteString("\n")
		b.WriteString("  " + dialogSubtle.Render("anything running in their shells is killed."))
		footer = "y upgrade    Esc not now"
	case confirmKindDisconnectHost:
		b.WriteString("  " + dialogNormal.Render(fmt.Sprintf("Disconnect %q?", sanitizeRemoteText(m.confirmName))))
		b.WriteString("\n\n")
		// Says what is NOT destroyed, because that is the question the user is
		// actually asking after trying Destroy and watching a fresh "Default"
		// take its place.
		b.WriteString("  " + dialogSubtle.Render("Its projects leave the sidebar. Nothing on that"))
		b.WriteString("\n")
		b.WriteString("  " + dialogSubtle.Render("machine stops — reconnect to get it all back."))
	default:
		label := fmt.Sprintf("Close %s %q?", m.confirmKind, m.confirmName)
		b.WriteString("  " + dialogNormal.Render(label))
		if rows := m.renderConfirmWorktrees(); rows != "" {
			b.WriteString("\n\n")
			b.WriteString(rows)
			// "space also delete worktree …" is 57 cells, 59 with the indent,
			// against a 54-cell budget — lipgloss wraps rather than clips, so it
			// stranded `Esc cancel` on a row the box never budgeted, stacked on
			// top of the worktree list. dialogBoxChrome documents this exact
			// hazard; this footer is the one that walked into it.
			// "toggle" rather than "worktree": a footer lists key → ACTION, and
			// there is exactly one checkbox on screen for it to refer to.
			footer = "space toggle    Enter confirm    Esc cancel"
		}
	}
	b.WriteString("\n\n")

	// Truncated like every other value-bearing row in this package's dialogs:
	// the box pads but never clips, so a footer that outgrows the budget wraps
	// and adds a row nothing accounted for.
	b.WriteString("  " + dialogSubtle.Render(truncateToWidth(footer, dialogWidth-dialogBoxChrome-confirmRowIndent)))

	return b.String()
}

// confirmRowIndent is the two-space gutter every confirm row is written with.
// Counted against the width budget because it is part of the rendered line.
const confirmRowIndent = 2

// renderConfirmWorktrees draws the "also delete the worktree" checkbox and one
// line per worktree, or "" when the close would delete none.
//
// Returning "" for the ordinary case is the point: every close that touches no
// worktree renders exactly the dialog it always has, down to the footer.
func (m Model) renderConfirmWorktrees() string {
	if len(m.confirmWorktrees) == 0 {
		return ""
	}
	box := "[ ]"
	if m.confirmRemoveWorktree {
		box = "[x]"
	}
	head := fmt.Sprintf("%s Also delete %s", box, pluralWorktrees(len(m.confirmWorktrees)))
	style := dialogNormal
	if m.confirmRemoveWorktree {
		style = dialogWarn
	}

	var b strings.Builder
	b.WriteString("  " + style.Render(head))
	// CAPPED, because lipgloss pads but never clips and renderDialog's
	// lipgloss.Place does not either: two lines per worktree grows the box past
	// the terminal and pushes the footer — and the Enter it documents — off
	// screen. The cap is a DISPLAY bound only; the header counts every worktree
	// and the toggle still covers all of them.
	shown := m.confirmWorktrees
	if len(shown) > confirmWorktreeMaxRows {
		shown = shown[:confirmWorktreeMaxRows]
	}
	for _, w := range shown {
		b.WriteString("\n")
		// The label is daemon-chosen text — a branch name, or a path when the
		// pane's shell has drifted — so it is sanitized AND bounded at render,
		// like every other value from a machine the user may not control.
		// Bounding is a separate job from sanitizing: sanitizeRemoteText
		// removes escapes without shortening anything.
		//
		// Elided in the MIDDLE, because the identifying half of a path is its
		// TAIL: truncateCells cuts the end, so every worktree under one parent
		// would render as the same `E:\Projects\…\quil-worktrees\…` prefix. The
		// pane's own spawn-error line elides for exactly this reason.
		b.WriteString("      " + dialogSubtle.Render(
			elideMiddle(sanitizeRemoteText(w.label), confirmWorktreeLabelCap)))
		b.WriteString("\n")
		b.WriteString("      " + m.renderWorktreeChanges(w))
	}
	if rest := len(m.confirmWorktrees) - len(shown); rest > 0 {
		b.WriteString("\n")
		b.WriteString("      " + dialogSubtle.Render(fmt.Sprintf("…and %d more", rest)))
	}
	// Said once, under the list, because it is a property of the operation
	// rather than of any one worktree — and said whether or not the check
	// answered, since it is true either way. "everything else here" is deliberate
	// and covers the case a count alone hides: ignored files, a .env among them,
	// which exist in no branch and cannot be recovered from one.
	//
	// Separated by a BLANK line: butted against the last row it reads as that
	// worktree's own status line rather than as a statement about the operation.
	b.WriteString("\n\n")
	b.WriteString("  " + dialogSubtle.Render("The branch is kept; everything else here goes."))
	return b.String()
}

// confirmWorktreeMaxRows bounds the rendered list. A tab can legitimately hold
// more worktree panes than a dialog has room for, and the box does not clip.
const confirmWorktreeMaxRows = 6

// renderWorktreeChanges is the one line that says what the removal would cost.
//
// Four states, all rendered apart, because collapsing any two of them tells the
// user something that is not true: still checking, could not check, clean, and
// a count. "Could not check" must never look like "clean" — clean is the one
// answer that invites the toggle.
func (m Model) renderWorktreeChanges(w confirmWorktree) string {
	switch {
	case !w.loaded:
		return dialogSubtle.Render("checking for uncommitted work…")
	case w.err != "":
		return dialogWarn.Render("⚠ " + truncateCells(sanitizeRemoteText(w.err), confirmWorktreeLabelCap))
	case w.changes == 0:
		return dialogSubtle.Render("clean")
	case w.changes == 1:
		return dialogWarn.Render("⚠ 1 uncommitted or ignored file will be lost")
	default:
		// "or ignored" is not padding: the count includes ignored entries
		// (gitworktree.Status), and those are the ones with no branch to
		// recover them from. Saying only "uncommitted" would have the number
		// describe something narrower than what the removal takes.
		return dialogWarn.Render(fmt.Sprintf("⚠ %d uncommitted or ignored files will be lost", w.changes))
	}
}

func pluralWorktrees(n int) string {
	if n == 1 {
		return "its worktree"
	}
	return fmt.Sprintf("%d worktrees", n)
}

// confirmWorktreeLabelCap bounds a branch name and a daemon-supplied error in
// the dialog. The confirm box does not truncate on its own and lipgloss WRAPS,
// so an unbounded value becomes many rendered lines — the same reason the
// project form caps its message line.
const confirmWorktreeLabelCap = 46

func (m Model) renderGitRepoPickDialog() string {
	var b strings.Builder

	// Names the tool the picker will actually spawn: both overlay toggles share
	// this dialog, and a title that always said "lazygit" would be a lie half
	// the time — on the one screen where the user commits to a repository.
	//
	// Deliberately NO fallback for an empty repoPickPlugin. Substituting a tool
	// name here would have the title disagree with Enter, which passes the raw
	// value to createOverlay and refuses it — a dialog that named a tool and
	// then did nothing. The field is set immediately before this dialog opens
	// and cleared on both exits, so an empty value is a wiring bug, and a
	// visibly broken title is how it should surface.
	b.WriteString(dialogTitle.Render("Open " + m.repoPickPlugin + " for which repo?"))
	b.WriteString("\n\n")

	// gitRepoPickWidth - 4: 2 for cursor marker, 2 for dialog border padding.
	const pickMaxWidth = gitRepoPickWidth - 4
	for i, repo := range m.repoPickCandidates {
		cursor := "  "
		style := dialogNormal
		if i == m.dialogCursor {
			cursor = "> "
			style = dialogSelected
		}
		b.WriteString(cursor + style.Render(leftTruncPath(repo, pickMaxWidth)) + "\n")
	}

	b.WriteByte('\n')
	b.WriteString(dialogSubtle.Render("Enter open · Esc cancel"))

	return b.String()
}

// leftTruncPath truncates s to at most maxWidth runes, preserving the
// rightmost characters (the repo basename / distinguishing tail).
// A leading "…" is prepended when truncation occurs.
//
// Both of this function's callers draw daemon-supplied repository paths (the
// setup dialog's CWD pick list and the Alt+G repo picker both render
// GitReposRespPayload.Repos), so s is sanitized here rather than at each call
// site — one point neither caller can forget, and it is a no-op for the
// local, already-trusted strings the pick list also carries (recent CWDs).
//
// Sanitized BEFORE truncating, not after: maxWidth is a budget on the runes
// that will actually be drawn, and truncating first would spend that budget
// on runes sanitizeRemoteText is about to delete anyway. A name front-loaded
// with control bytes ahead of a few real characters would have those real
// characters truncated away by a length count that includes bytes with no
// final width at all — the same "confidently wrong" failure this codebase's
// other truncate-vs-cap orderings already avoid (see browseDirResponse's
// sort-before-cap in internal/daemon/browse.go). Sanitizing first also means
// the "…" prefix always sits against genuinely visible text.
func leftTruncPath(s string, maxWidth int) string {
	s = sanitizeRemoteText(s)
	runes := []rune(s)
	if len(runes) <= maxWidth {
		return s
	}
	return "…" + string(runes[len(runes)-maxWidth+1:])
}

func boolStr(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

// --- Pane creation dialog ---

// createPaneCategories returns the ordered list of categories with their plugins
// for the pane creation dialog.
func (m *Model) createPaneCategories() []struct {
	key     string
	label   string
	plugins []*plugin.PanePlugin
} {
	if m.pluginRegistry == nil {
		return nil
	}
	// All plugins (not just available): unavailable ones are shown greyed with
	// their homepage so users discover what to install, instead of silently
	// vanishing from the list.
	byCategory := m.pluginRegistry.ByCategory()
	order := plugin.CategoryOrder()

	var result []struct {
		key     string
		label   string
		plugins []*plugin.PanePlugin
	}
	for _, cat := range order {
		plugins := byCategory[cat.Key]
		if len(plugins) == 0 {
			continue
		}
		// Available plugins first, then alphabetical — keeps the actionable
		// entries on top and greyed (not-installed) ones below.
		sortPluginsAvailableFirst(plugins)
		result = append(result, struct {
			key     string
			label   string
			plugins []*plugin.PanePlugin
		}{cat.Key, cat.Label, plugins})
	}

	// Add any categories not in the standard order
	for cat, plugins := range byCategory {
		found := false
		for _, o := range order {
			if o.Key == cat {
				found = true
				break
			}
		}
		if !found {
			sortPluginsAvailableFirst(plugins)
			result = append(result, struct {
				key     string
				label   string
				plugins []*plugin.PanePlugin
			}{cat, cat, plugins})
		}
	}
	return result
}

// sortPluginsAvailableFirst orders available plugins ahead of unavailable
// ones, alphabetical by display name within each group.
func sortPluginsAvailableFirst(plugins []*plugin.PanePlugin) {
	sort.SliceStable(plugins, func(i, j int) bool {
		if plugins[i].Available != plugins[j].Available {
			return plugins[i].Available // available (true) sorts before unavailable
		}
		return plugins[i].DisplayName < plugins[j].DisplayName
	})
}

func (m Model) handleCreatePaneKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	switch key {
	case "esc":
		if m.createPaneStep > 0 {
			// Go back one step; skip instance list if plugin has no form fields
			m.createPaneStep--
			if m.createPaneStep == 2 {
				p := m.pluginRegistry.Get(m.selectedPlugin)
				if p == nil || len(p.Command.FormFields) == 0 {
					m.createPaneStep = 1 // skip instance step
				}
			}
			m.dialogCursor = 0
			return m, nil
		}
		m.dialog = dialogNone
		m.createPaneStep = 0
		if m.createPaneTarget == paneTargetNewTab {
			// Cancelling the PICKER is not cancelling the tab: Ctrl+T Esc stays
			// the two-keystroke path to the shell tab this key produced before
			// the picker existed. The bare create carries no spec, so the daemon
			// takes exactly the path every other producer takes.
			return m, m.createTerminalTab()
		}
		return m, nil

	case "up", "k":
		if m.dialogCursor > 0 {
			m.dialogCursor--
		}
		return m, nil

	case "down", "j":
		items := m.createPaneItemCount()
		if m.dialogCursor < items-1 {
			m.dialogCursor++
		}
		return m, nil

	case "enter":
		return m.handleCreatePaneSelect()

	case "delete", "backspace":
		// Delete saved instance from the list (step 2, cursor > 0)
		if m.createPaneStep == 2 && m.dialogCursor > 0 {
			instances := m.instanceStore[m.selectedPlugin]
			idx := m.dialogCursor - 1
			if idx < len(instances) {
				m.confirmKind = "instance"
				m.resetConfirmWorktrees()
				m.confirmID = instances[idx].ID
				m.confirmName = instances[idx].Name
				m.dialog = dialogConfirm
			}
			return m, nil
		}
	}

	return m, nil
}

func (m *Model) createPaneItemCount() int {
	cats := m.createPaneCategories()
	switch m.createPaneStep {
	case 0:
		return len(cats)
	case 1:
		if m.selectedCategory < len(cats) {
			return len(cats[m.selectedCategory].plugins)
		}
	case 2: // instance list
		return 1 + len(m.instanceStore[m.selectedPlugin]) // "+ New" + saved
	case 3: // split direction
		if m.createPaneTarget == paneTargetNewTab {
			// Unreachable through advanceFromPluginChoice, which submits rather
			// than landing here — answered anyway so a future path that does
			// reach it renders no rows instead of three that cannot act.
			return 0
		}
		return 3 // Horizontal, Vertical, Replace
	}
	return 0
}

func (m Model) handleCreatePaneSelect() (tea.Model, tea.Cmd) {
	cats := m.createPaneCategories()

	if m.createPaneStep == 0 {
		// Selected a category
		if m.dialogCursor < len(cats) {
			m.selectedCategory = m.dialogCursor
			m.createPaneStep = 1
			m.dialogCursor = 0
		}
		return m, nil
	}

	if m.createPaneStep == 1 {
		// Selected a plugin — check if it has form fields (instance management)
		if m.selectedCategory >= len(cats) {
			return m, nil
		}
		plugins := cats[m.selectedCategory].plugins
		if m.dialogCursor >= len(plugins) {
			return m, nil
		}
		// Unavailable plugins are greyed and not selectable — the binary isn't
		// installed, so there's nothing to spawn. The inline footer hint
		// (rendered while the cursor is on it) tells the user where to get it.
		if !plugins[m.dialogCursor].Available {
			return m, nil
		}
		m.selectedPlugin = plugins[m.dialogCursor].Name
		m.selectedInstanceArgs = nil
		m.selectedInstanceName = ""
		m.dialogCursor = 0

		// If plugin has form fields → instance list (step 2)
		p := m.pluginRegistry.Get(m.selectedPlugin)
		if p != nil && len(p.Command.FormFields) > 0 {
			// If no saved instances, jump directly to the form
			if len(m.instanceStore[m.selectedPlugin]) == 0 {
				m.openInstanceForm(p)
				return m, tea.ClearScreen // width changes — force full redraw
			}
			m.createPaneStep = 2
		} else {
			// No form fields — either show setup dialog (CWD/toggles) or jump to split.
			cmd := m.enterSetupOrSplit(p)
			if cmd != nil {
				return m, cmd
			}
		}
		// Plugin may have custom dialog_width — force redraw to avoid stale border cells
		if p != nil && p.Display.DialogWidth > 0 {
			return m, tea.ClearScreen
		}
		return m, nil
	}

	if m.createPaneStep == 2 {
		// Selected from instance list
		instances := m.instanceStore[m.selectedPlugin]
		if m.dialogCursor == 0 {
			// "+ New" — open instance form
			p := m.pluginRegistry.Get(m.selectedPlugin)
			if p != nil {
				m.openInstanceForm(p)
			}
			return m, tea.ClearScreen // width changes — force full redraw
		}
		// Select existing instance
		idx := m.dialogCursor - 1
		var p *plugin.PanePlugin
		if idx < len(instances) {
			inst := instances[idx]
			p = m.pluginRegistry.Get(m.selectedPlugin)
			if p != nil {
				m.selectedInstanceArgs = BuildArgs(p.Command.ArgTemplate, inst.Fields)
			}
			m.selectedInstanceName = inst.Name
		}
		m.dialogCursor = 0
		// Either show setup dialog (CWD/toggles) or jump straight to split.
		// Mirror the same routing as the no-form-fields branch above and the
		// instance form submit path; otherwise saved instances would silently
		// skip the setup dialog while "+ New" wouldn't.
		// enterSetupOrSplit routes to the setup dialog, the placement step or a
		// new-tab submit — no step assignment belongs here.
		return m, m.enterSetupOrSplit(p)
	}

	// Step 3: selected placement (split direction)
	return m.handleCreatePaneSplit()
}

// recentCWDsDest is the destination a committed directory is filed under.
//
// createPaneDest when the dialog pinned one, because that is the machine whose
// disk was browsed and the machine the pane is going to. It is deliberately NOT
// used when empty: "" there means one of the startup windows (see
// pinnableDest), where the router picks the destination and the client cannot
// know which — filing under "" would put the entry in the unscoped list, which
// is the LOCAL daemon's, and that is the same guess Router.Send makes.
func (m Model) recentCWDsDest() string {
	if m.createPaneDest != "" {
		return m.createPaneDest
	}
	return m.activeDest()
}

// setupDiscoveryBase is the directory the setup dialog starts looking from.
//
// For a SPLIT it is the active pane's OSC 7 CWD: the pane being split is the
// context, and that is where the user already is. Deliberately not
// lastSelectedCWD — that memory belongs to the generic browser, and a stale
// last-choice from another project seeds candidates for the wrong repository.
//
// For a NEW TAB the context is the PROJECT. A new tab is not "beside this
// pane", and the daemon roots one at projectCWD for exactly the same reason, so
// discovering from wherever the last shell happened to cd to would put the two
// halves of one create on different directories. The active pane is still the
// fallback, for a project with no root recorded.
func (m Model) setupDiscoveryBase() string {
	if m.createPaneTarget == paneTargetNewTab {
		if p := m.cur(); p != nil && p.RootDir != "" {
			return p.RootDir
		}
	}
	if tab := m.activeTabModel(); tab != nil {
		if pane := tab.ActivePaneModel(); pane != nil {
			return pane.CWD
		}
	}
	return ""
}

// advanceFromPluginChoice runs once the plugin, its instance and its setup
// answers have all been collected — the one place that decides what "done
// choosing" means for each target.
//
// For a split there is a step left: WHERE the pane goes relative to the active
// one. A new tab has no pane to be relative to, so those three rows cannot mean
// anything and the choice is complete — it submits instead. Routing both
// through one function is what stops the four call sites that used to assign
// createPaneStep = 3 from having to agree about it.
func (m *Model) advanceFromPluginChoice() tea.Cmd {
	if m.createPaneTarget == paneTargetNewTab {
		out, cmd := m.handleCreatePaneSplit()
		*m = out.(Model)
		return cmd
	}
	m.dialog = dialogCreatePane
	m.createPaneStep = 3
	m.dialogCursor = 0
	return nil
}

// openInstanceForm initializes the instance form dialog with default values.
func (m *Model) openInstanceForm(p *plugin.PanePlugin) {
	m.instanceFormValues = make([]string, len(p.Command.FormFields))
	for i, ff := range p.Command.FormFields {
		m.instanceFormValues[i] = ff.Default
	}
	m.instanceFormCursor = 0
	m.dialogEdit = false
	m.dialogInput = ""
	m.dialog = dialogInstanceForm
}

// handleCreatePaneSplit handles the final split direction selection (step 3).
func (m Model) handleCreatePaneSplit() (tea.Model, tea.Cmd) {
	target := m.createPaneTarget
	pluginName := m.selectedPlugin
	instanceName := m.selectedInstanceName
	instanceArgs := m.selectedInstanceArgs
	resumeSessionID := m.selectedSessionID
	cwd := m.selectedCWD
	// Captured with the other choices, BEFORE the teardown below clears them.
	// Reading these after that reset yields the zero value, so the spec would
	// silently never be sent and every "new branch" would spawn an ordinary
	// pane in the repository root — the exact silent relocation this feature
	// exists to prevent.
	//
	// The repo root is the DAEMON's answer (worktreeState.root, the main
	// checkout it reported), never the browsed directory. `git worktree list`
	// succeeds from any subdirectory, so the field is offered while browsing
	// e.g. <repo>/internal/tui — and DerivePath would then put a full second
	// checkout at <repo>/internal/tui-worktrees/<branch>, NESTED inside the
	// first. That is precisely what the sibling layout exists to prevent: a
	// `git clean -xfd` in the main checkout deletes another pane's live work,
	// and every tree-walking tool traverses it. protocol.go says outright that
	// the client must never compute this value.
	newBranch, newBranchRepo := m.worktreeNewBranch, m.worktrees.root
	if cwd != "" {
		m.lastSelectedCWD = cwd
		m.recentCWDs = pushRecentCWD(m.recentCWDs, cwd, recentCWDMax)
		// Scoped to the daemon the DIALOG was opened against, not the active
		// project's: the directory was browsed on that machine's disk, so filing
		// it anywhere else offers a path that does not exist there. The two
		// differ exactly when createPaneDest exists to matter — the active
		// project moved while the dialog was open — and the pane itself goes to
		// createPaneDest, so the recent list has to follow it.
		if err := SaveRecentCWDs(config.RecentCWDsPath(m.recentCWDsDest()), m.recentCWDs); err != nil {
			log.Printf("create pane: save recent cwds: %v", err)
		}
	}
	m.dialog = dialogNone
	m.createPaneStep = 0
	m.selectedCWD = ""
	m.cwdInputError = ""
	m.toggleStates = nil
	m.setupFieldCursor = 0
	m.cwdBrowseDir = ""
	m.cwdBrowseEntries = nil
	m.cwdBrowseCursor = 0
	m.cwdBrowseScroll = 0
	// The worktree field's state joins the teardown for the same reason the
	// rest of it does: without this the NEXT Ctrl+N inherits the branch name
	// and the chosen worktree from the pane just created, and spawns in a
	// repository the dialog is no longer showing. Third recurrence of this
	// class in one feature.
	m.selectedWorktree = ""
	m.worktreeNewBranch = ""
	m.worktreeNaming = false
	m.worktreeErr = ""
	m.worktreeCursor = 0
	m.worktreeScroll = 0
	m.worktrees = worktreeState{}
	m.resetSessionSelection()

	// The new-tab branch returns HERE — after the teardown, before everything
	// below it — and both halves of that position are load-bearing.
	//
	// After the teardown, so a new-tab create leaves exactly as little behind as
	// a split does. Before the rest, because none of it applies: m.dialogCursor
	// is read below to pick horizontal/vertical/replace and holds whatever the
	// last list left there (the placement step never ran), the active-tab and
	// active-pane requirements describe a pane to split FROM — which is also
	// what keeps this working during the startup window, when there is no tab
	// yet — and every piece of bookkeeping past this point (pendingSplit,
	// worktreeCreates, worktreeReplaced, the give-up tick) is keyed by a tab id
	// that will not exist until the daemon answers. Nothing is detached and no
	// leaf is reserved, so there is nothing to unwind and nothing to time out:
	// the tab arrives whole on the next broadcast.
	if target == paneTargetNewTab {
		var spec *ipc.WorktreeSpec
		if newBranch != "" {
			if newBranchRepo == "" {
				// Same refusal the split path makes just below, for the same
				// reason: falling back to the browsed directory is the nested-
				// worktree bug.
				logger.Debug("create tab: REFUSED, branch %q has no known repository root", newBranch)
				m.setFlash("worktree not created: the repository root is not known yet")
				return m, m.flashCmd()
			}
			spec = &ipc.WorktreeSpec{RepoRoot: newBranchRepo, Branch: newBranch}
			// Armed so the daemon's answer can be reported. Keyed by BRANCH, not
			// by tab: this create has no tab id until the daemon mints one, which
			// is also why it arms none of the tab-keyed bookkeeping the split
			// path uses. applyCreatePaneResp consumes it.
			if m.newTabWorktrees == nil {
				m.newTabWorktrees = make(map[string]bool)
			}
			m.newTabWorktrees[newBranch] = true
		}
		logger.Debug("create tab: submitting cwd=%q type=%s instance=%s branch=%q repo=%q",
			cwd, pluginName, instanceName, newBranch, newBranchRepo)
		return m, m.sendCreateTab(&ipc.FirstPaneSpec{
			Type:            pluginName,
			CWD:             cwd,
			InstanceName:    instanceName,
			InstanceArgs:    instanceArgs,
			ResumeSessionID: resumeSessionID,
			Worktree:        spec,
		})
	}

	tab := m.activeTabModel()
	if tab == nil {
		return m, nil
	}
	pane := tab.ActivePaneModel()
	if pane == nil {
		return m, nil
	}

	// The new pane belongs to the tab it is being created in, so the tab's own
	// dest is the routing answer — not the active project's, which a background
	// tab does not share.
	tabID, tabDest := tab.ID, tab.Dest

	// "submitting", NOT "sending IPC". Three paths below return without ever
	// sending, so a line claiming the send has happened is a lie the log tells
	// on exactly the runs somebody is reading the log to explain. It cost a
	// full investigation once: the daemon had no create_pane, the client
	// insisted it had sent one, and the truth was a silent refusal underneath.
	// Each of those paths now logs its own reason, so the next occurrence is
	// one grep rather than a bisect of the handler.
	logger.Debug("create pane: submitting cwd=%q type=%s instance=%s branch=%q repo=%q split=%d",
		cwd, pluginName, instanceName, newBranch, newBranchRepo, m.dialogCursor)

	// Refused BEFORE anything destructive or stateful happens — before the
	// replace path disposes a pane, and before the split path arms a
	// placeholder. Refusing after the split would leave pendingSplit armed
	// with nothing to retire it (no send, so no response and no timeout),
	// which is the stranded-placeholder bug this feature already had once.
	if newBranch != "" && newBranchRepo == "" {
		// The listing never answered, so the repository root is unknown.
		// Refused rather than falling back to the browsed directory: that
		// fallback is exactly the nested-worktree bug.
		logger.Debug("create pane: REFUSED, branch %q has no known repository root (worktrees loaded=%v pending=%v repo=%v path=%q)",
			newBranch, m.worktrees.loaded, m.worktrees.pending, m.worktrees.repo, m.worktrees.path)
		m.setFlash("worktree not created: the repository root is not known yet")
		return m, m.flashCmd()
	}

	// ONE worktree create per tab at a time. worktreeCreates, worktreeReplaced
	// and pendingSplit are all keyed by TAB, so a second create on the same tab
	// overwrites all three: the first create's held pane is never disposed and
	// never restored, and its reserved leaf is replaced by the second's — so
	// the first's response restores a pane into the wrong slot.
	//
	// Widening the maps to hold both was the alternative and is the wrong
	// trade: the daemon's worktreeAdding single-flight already refuses a
	// concurrent add, so the second create could not have succeeded anyway.
	// Refusing here just says so at the moment the user asks, instead of
	// seconds later and less legibly. Reachable from the keyboard because the
	// dialog closes on submit and ActivePaneModel falls back to another leaf
	// once the first replace detaches its pane.
	if inflight := m.worktreeCreates[tab.ID]; inflight != "" {
		logger.Debug("create pane: REFUSED, tab %s already has a worktree create in flight (branch %q)", tab.ID, inflight)
		m.setFlash("still creating the worktree for " + truncateCells(sanitizeRemoteText(inflight), createErrFlashCap) + " — wait for it to finish")
		return m, m.flashCmd()
	}

	// Option 2: Replace current pane
	if m.dialogCursor == 2 {
		oldPaneID := pane.ID
		// The spec, when the user asked for a new branch — replace carries one
		// exactly as split does. The daemon creates the worktree BEFORE it
		// touches the pane being replaced, so a failed add costs nothing there.
		var spec *ipc.WorktreeSpec
		if newBranch != "" {
			spec = &ipc.WorktreeSpec{RepoRoot: newBranchRepo, Branch: newBranch}
		}

		if leaf := tab.Root.FindLeaf(oldPaneID); leaf != nil {
			// Detach immediately either way: the leaf must be reserved so the
			// arriving pane lands WHERE THE OLD ONE WAS rather than through the
			// root-insert fallback, and rendering resolves panes via FindLeaf,
			// which skips nil-Pane leaves.
			old := leaf.Pane
			leaf.Pane = nil
			tab.invalidateLeaves()
			if spec == nil {
				// Ordinary replace: the daemon destroys the old pane the moment
				// it handles this message, so the model is never rendered again.
				// Disposing here — not via the reconciliation sweep — keeps the
				// leaves cache honest: a stale cache was previously what fed the
				// detached pane into the sweep's existingPanes.
				if old != nil {
					old.Dispose()
				}
			} else if old != nil {
				// A worktree replace is ANSWERED, not fire-and-forget, and the
				// answer can be a failure seconds later. Disposing now would
				// make a failed `git worktree add` cost a live pane — the exact
				// hazard this combination used to be refused over, which is a
				// property of WHEN we dispose rather than of the operation. The
				// model is held until the daemon says the swap really happened;
				// applyCreatePaneResp disposes it on success and puts it back on
				// failure.
				if m.worktreeReplaced == nil {
					m.worktreeReplaced = make(map[string]*PaneModel)
				}
				m.worktreeReplaced[tab.ID] = old
			}
			if m.pendingSplit == nil {
				m.pendingSplit = make(map[string]*LayoutNode)
			}
			m.pendingSplit[tab.ID] = leaf
		}

		if spec != nil {
			// Same bookkeeping the split path arms: keyed by tab, because two
			// creates can be in flight and their responses can arrive in either
			// order.
			if m.worktreeCreates == nil {
				m.worktreeCreates = make(map[string]string)
			}
			m.worktreeCreates[tab.ID] = newBranch
		}

		send := func() tea.Msg {
			msg, _ := ipc.NewMessage(ipc.MsgCreatePane, ipc.CreatePanePayload{
				TabID:           tabID,
				CWD:             cwd,
				Type:            pluginName,
				InstanceName:    instanceName,
				InstanceArgs:    instanceArgs,
				ReplacePaneID:   oldPaneID,
				ResumeSessionID: resumeSessionID,
				Worktree:        spec,
			})
			m.sendForDest(tabDest, msg)
			return nil
		}
		if spec == nil {
			return m, send
		}
		return m, tea.Batch(send, tea.Tick(createPaneTimeout, func(time.Time) tea.Msg {
			return createPaneTimeoutMsg{tabID: tabID}
		}))
	}

	// Options 0/1: Split horizontal or vertical
	var dir SplitDir
	if m.dialogCursor == 0 {
		dir = SplitHorizontal
	} else {
		dir = SplitVertical
	}

	placeholder := tab.SplitAtPane(pane.ID, dir)
	if placeholder == nil {
		// The only one of these paths that used to surface NOTHING — no send,
		// no flash, no log. The dialog closed on submit, so the user saw it
		// vanish and no pane appear, which is indistinguishable from the pane
		// having been created somewhere they cannot see. It means the active
		// pane is not in its own tab's layout tree, so say so.
		logger.Debug("create pane: REFUSED, SplitAtPane found no leaf for pane %s in tab %s", pane.ID, tabID)
		m.setFlash("pane not created: the active pane is not in this tab's layout")
		return m, m.flashCmd()
	}

	if m.pendingSplit == nil {
		m.pendingSplit = make(map[string]*LayoutNode)
	}
	m.pendingSplit[tab.ID] = placeholder

	// The spec, when the user asked for a new branch. RepoRoot is the main
	// checkout the DAEMON reported, so no path built on this machine reaches
	// the far one and the worktree cannot land inside the repository.
	var spec *ipc.WorktreeSpec
	if newBranch != "" {
		// newBranchRepo is non-empty here: the unknown-root case was refused
		// above, before the split armed a placeholder.
		spec = &ipc.WorktreeSpec{RepoRoot: newBranchRepo, Branch: newBranch}
		// Keyed by TAB, not a single slot: two worktree creates can be in
		// flight at once (the dialog closes on submit, so a second Ctrl+N is
		// immediate), and the daemon's single-flight rejects the second
		// INSTANTLY while the first is still checking out — so responses
		// routinely arrive out of order. A scalar would be overwritten by the
		// second create and leave the first's placeholder stranded forever.
		if m.worktreeCreates == nil {
			m.worktreeCreates = make(map[string]string)
		}
		m.worktreeCreates[tab.ID] = newBranch
	}

	send := func() tea.Msg {
		msg, _ := ipc.NewMessage(ipc.MsgCreatePane, ipc.CreatePanePayload{
			TabID:           tabID,
			CWD:             cwd,
			Type:            pluginName,
			InstanceName:    instanceName,
			InstanceArgs:    instanceArgs,
			ResumeSessionID: resumeSessionID,
			Worktree:        spec,
		})
		m.sendForDest(tabDest, msg)
		return nil
	}
	if spec == nil {
		return m, send
	}
	// Only a worktree create is armed with a give-up: an ordinary create is
	// answered by the next workspace broadcast and holds its placeholder for
	// microseconds, so a timer there would be a timer that never usefully
	// fires.
	return m, tea.Batch(send, tea.Tick(createPaneTimeout, func(time.Time) tea.Msg {
		return createPaneTimeoutMsg{tabID: tabID}
	}))
}

func (m Model) renderCreatePaneDialog() string {
	var b strings.Builder

	cats := m.createPaneCategories()

	switch m.createPaneStep {
	case 0:
		// Step 0: Select category
		b.WriteString(dialogTitle.Render("New Pane"))
		b.WriteString("\n\n")

		for i, cat := range cats {
			cursor := "  "
			style := dialogNormal
			if i == m.dialogCursor {
				cursor = "> "
				style = dialogSelected
			}
			b.WriteString(cursor + style.Render(cat.label) + "\n")
		}

		if len(cats) == 0 {
			b.WriteString("  " + dialogSubtle.Render("No plugins available") + "\n")
		}

		b.WriteByte('\n')
		b.WriteString(dialogSubtle.Render("Esc cancel"))

	case 1:
		// Step 1: Select plugin within category
		if m.selectedCategory < len(cats) {
			cat := cats[m.selectedCategory]
			b.WriteString(dialogTitle.Render(cat.label))
			b.WriteString("\n\n")

			cursorOnUnavailable := false
			for i, p := range cat.plugins {
				cursor := "  "
				selected := i == m.dialogCursor
				var line string
				if !p.Available {
					// Greyed, not selectable — show why and where to get it.
					if selected {
						cursor = "> "
						cursorOnUnavailable = true
					}
					note := "(not installed"
					if p.Homepage != "" {
						note += " — " + p.Homepage
					}
					note += ")"
					line = dialogSubtle.Render(p.DisplayName + "  " + note)
				} else {
					style := dialogNormal
					if selected {
						cursor = "> "
						style = dialogSelected
					}
					line = style.Render(p.DisplayName)
					if p.Description != "" {
						line += "  " + dialogSubtle.Render(p.Description)
					}
				}
				b.WriteString(cursor + line + "\n")
			}

			b.WriteByte('\n')
			if cursorOnUnavailable {
				b.WriteString(dialogErrorStyle.Render("Not installed — install it (link above), then restart") + "\n")
			}
			b.WriteString(dialogSubtle.Render("Esc back"))
		}

	case 2:
		// Step 2: Instance list
		p := m.pluginRegistry.Get(m.selectedPlugin)
		title := "Instances"
		if p != nil {
			title = p.DisplayName
		}
		b.WriteString(dialogTitle.Render(title))
		b.WriteString("\n\n")

		instances := m.instanceStore[m.selectedPlugin]

		// First item: "+ New"
		cursor := "  "
		style := dialogNormal
		if m.dialogCursor == 0 {
			cursor = "> "
			style = dialogSelected
		}
		b.WriteString(cursor + style.Render("+ New Connection") + "\n")

		// Saved instances
		for i, inst := range instances {
			cursor = "  "
			style = dialogNormal
			if i+1 == m.dialogCursor {
				cursor = "> "
				style = dialogSelected
			}
			line := style.Render(inst.Name)
			if addr := inst.DisplayAddr(); addr != "" {
				line += "  " + dialogSubtle.Render(addr)
			}
			if inst.Description != "" {
				line += "  " + dialogSubtle.Render(inst.Description)
			}
			b.WriteString(cursor + line + "\n")
		}

		b.WriteByte('\n')
		b.WriteString(dialogSubtle.Render("Enter select  Del remove  Esc back"))

	case 3:
		// Step 3: Select split direction
		b.WriteString(dialogTitle.Render("Split Direction"))
		b.WriteString("\n\n")

		dirs := []string{"Horizontal  (left | right)", "Vertical    (top / bottom)", "Replace current pane"}
		for i, d := range dirs {
			cursor := "  "
			style := dialogNormal
			if i == m.dialogCursor {
				cursor = "> "
				style = dialogSelected
			}
			b.WriteString(cursor + style.Render(d) + "\n")
		}

		b.WriteByte('\n')
		b.WriteString(dialogSubtle.Render("Esc back"))
	}

	return b.String()
}

// --- Instance form dialog ---

func (m Model) handleInstanceFormKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := m.pluginRegistry.Get(m.selectedPlugin)
	if p == nil {
		m.dialog = dialogNone
		return m, nil
	}
	fields := p.Command.FormFields
	totalItems := len(fields) + 1 // fields + "Create" button
	key := msg.String()

	if m.dialogEdit {
		switch {
		case key == "esc":
			m.dialogEdit = false
			m.dialogInput = ""
		case key == "enter":
			if m.instanceFormCursor < len(fields) {
				m.instanceFormValues[m.instanceFormCursor] = m.dialogInput
			}
			m.dialogEdit = false
			m.dialogInput = ""
		case key == "backspace":
			if len(m.dialogInput) > 0 {
				m.dialogInput = m.dialogInput[:len(m.dialogInput)-1]
			}
		case key == "tab":
			// Commit and advance
			if m.instanceFormCursor < len(fields) {
				m.instanceFormValues[m.instanceFormCursor] = m.dialogInput
			}
			m.dialogEdit = false
			m.dialogInput = ""
			if m.instanceFormCursor < totalItems-1 {
				m.instanceFormCursor++
			}
		case m.isAction(key, "pane.paste"):
			return m, m.pasteToDialog()
		default:
			if len(key) == 1 {
				m.dialogInput += key
			} else if key == "space" {
				m.dialogInput += " "
			}
		}
		return m, nil
	}

	switch key {
	case "esc":
		// Return to instance list or plugin selection
		m.dialog = dialogCreatePane
		if len(m.instanceStore[m.selectedPlugin]) > 0 {
			m.createPaneStep = 2
		} else {
			m.createPaneStep = 1
		}
		m.dialogCursor = 0
		return m, nil

	case "up", "k":
		if m.instanceFormCursor > 0 {
			m.instanceFormCursor--
		}
		return m, nil

	case "down", "j":
		if m.instanceFormCursor < totalItems-1 {
			m.instanceFormCursor++
		}
		return m, nil

	case "tab":
		m.instanceFormCursor = (m.instanceFormCursor + 1) % totalItems
		return m, nil

	case "enter":
		if m.instanceFormCursor < len(fields) {
			// Start editing this field
			m.dialogEdit = true
			m.dialogInput = m.instanceFormValues[m.instanceFormCursor]
			return m, nil
		}
		// "Create" button — validate and save
		return m.submitInstanceForm(p)
	}

	return m, nil
}

func (m Model) submitInstanceForm(p *plugin.PanePlugin) (tea.Model, tea.Cmd) {
	fields := p.Command.FormFields

	// Validate required fields
	fieldMap := make(map[string]string)
	for i, ff := range fields {
		val := m.instanceFormValues[i]
		if ff.Required && val == "" {
			// Move cursor to the first empty required field
			m.instanceFormCursor = i
			return m, nil
		}
		fieldMap[ff.Name] = val
	}

	// Create saved instance
	name := fieldMap["name"]
	if name == "" {
		name = "unnamed"
	}
	desc := fieldMap["description"]

	inst := SavedInstance{
		ID:          uuid.New().String()[:8],
		Name:        name,
		Fields:      fieldMap,
		Description: desc,
	}

	// Save to store
	if m.instanceStore == nil {
		m.instanceStore = make(InstanceStore)
	}
	m.instanceStore[m.selectedPlugin] = append(m.instanceStore[m.selectedPlugin], inst)
	if err := SaveInstances(config.InstancesPath(), m.instanceStore); err != nil {
		log.Printf("save instances: %v", err)
	}

	// Build args from template
	m.selectedInstanceArgs = BuildArgs(p.Command.ArgTemplate, fieldMap)
	m.selectedInstanceName = name

	// Either show setup dialog (CWD/toggles) or finish choosing.
	return m, m.enterSetupOrSplit(p)
}

func (m Model) renderInstanceFormDialog() string {
	var b strings.Builder

	p := m.pluginRegistry.Get(m.selectedPlugin)
	if p == nil {
		return ""
	}
	fields := p.Command.FormFields

	title := "New " + p.DisplayName
	b.WriteString(dialogTitle.Render(title))
	b.WriteString("\n\n")

	for i, ff := range fields {
		cursor := "  "
		labelStyle := dialogLabelStyle
		if i == m.instanceFormCursor {
			cursor = "> "
			labelStyle = labelStyle.Foreground(lipgloss.Color("230")).Bold(true)
		}

		val := m.instanceFormValues[i]
		var valRendered string
		if m.dialogEdit && i == m.instanceFormCursor {
			valRendered = dialogEditStyle.Render(m.dialogInput + "│")
		} else if val != "" {
			valRendered = dialogValStyle.Render(val)
		} else {
			valRendered = dialogSubtle.Render("—")
		}

		label := ff.Label
		if ff.Required {
			label += "*"
		}

		b.WriteString(cursor + labelStyle.Render(label) + valRendered + "\n")
	}

	// "Create" button
	b.WriteByte('\n')
	btnCursor := "  "
	btnStyle := dialogNormal
	if m.instanceFormCursor == len(fields) {
		btnCursor = "> "
		btnStyle = dialogSelected
	}
	b.WriteString(btnCursor + btnStyle.Render("[Create]") + "\n")

	b.WriteByte('\n')
	b.WriteString(dialogSubtle.Render("Tab next  Enter edit/confirm  Esc back"))

	return b.String()
}

// --- Plugins management dialog ---

func (m Model) handlePluginsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	allPlugins := m.sortedPlugins()
	totalItems := len(allPlugins) + 2 // plugins + Reload + Reset

	switch msg.String() {
	case "esc":
		m.dialog = dialogAbout
		m.dialogCursor = 2
		return m, nil

	case "up", "k":
		if m.dialogCursor > 0 {
			m.dialogCursor--
		}
		return m, nil

	case "down", "j":
		if m.dialogCursor < totalItems-1 {
			m.dialogCursor++
		}
		return m, nil

	case "enter":
		if m.dialogCursor < len(allPlugins) {
			// Open TOML editor for selected plugin
			p := allPlugins[m.dialogCursor]
			if p.Name == "terminal" {
				// Terminal is built-in Go, no TOML to edit
				return m, nil
			}
			filePath := filepath.Join(config.PluginsDir(), p.Name+".toml")
			data, err := os.ReadFile(filePath)
			if err != nil {
				return m, nil
			}
			// Calculate editor viewport from available space
			viewH := m.height - 10 // title + footer + borders + padding
			if viewH < 5 {
				viewH = 5
			}
			viewW := 70
			m.tomlEditor = NewTextEditor(string(data), filePath, viewW, viewH)
			m.dialog = dialogTOMLEditor
			return m, nil
		}

		btnIdx := m.dialogCursor - len(allPlugins)
		if btnIdx == 1 {
			plugin.EnsureDefaultPlugins(config.PluginsDir())
		}
		// Both buttons: reload plugins
		if err := m.pluginRegistry.LoadFromDir(config.PluginsDir()); err != nil {
			log.Printf("reload plugins: %v", err)
		}
		// Local detection only in local mode — in remote mode this would
		// discard whatever availability answer the daemon already supplied
		// with a detection pass over the wrong machine. reloadPluginsThenAskCmd
		// below re-asks the daemon instead, once its own reload has finished.
		//
		// remoteModeFor(activeDest()), not RemoteMode(): the daemon this
		// dialog reloads is the ACTIVE one, and this is the daemon-scoped
		// counterpart to the same guard elsewhere.
		if !m.remoteModeFor(m.activeDest()) {
			m.pluginRegistry.DetectAvailability()
		}
		m.dialog = dialogNone
		return m, reloadPluginsThenAskCmd(m.client)
	}

	return m, nil
}

// sortedPlugins returns all plugins sorted by display name.
func (m Model) sortedPlugins() []*plugin.PanePlugin {
	all := m.pluginRegistry.All()
	sort.Slice(all, func(i, j int) bool {
		return all[i].DisplayName < all[j].DisplayName
	})
	return all
}

func (m Model) renderPluginsDialog() string {
	var b strings.Builder

	b.WriteString(dialogTitle.Render("Plugins"))
	b.WriteString("\n\n")

	allPlugins := m.sortedPlugins()

	// Plugin list (selectable — Enter opens editor)
	for i, p := range allPlugins {
		cursor := "  "
		style := dialogNormal
		if i == m.dialogCursor {
			cursor = "> "
			style = dialogSelected
		}

		avail := dialogSubtle.Render("[x]")
		if p.Available {
			avail = lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Render("[ok]")
		}

		name := style.Render(p.DisplayName)
		cat := dialogSubtle.Render(p.Category)
		label := ""
		if p.Name == "terminal" {
			label = dialogSubtle.Render("  (built-in)")
		}
		b.WriteString(fmt.Sprintf("%s%s  %s  %s%s\n", cursor, name, cat, avail, label))
	}

	// Action buttons
	b.WriteByte('\n')
	btnLabels := []string{"Reload Plugins", "Restore Missing Defaults"}
	for i, label := range btnLabels {
		btnIdx := len(allPlugins) + i
		cursor := "  "
		style := dialogNormal
		if btnIdx == m.dialogCursor {
			cursor = "> "
			style = dialogSelected
		}
		b.WriteString(cursor + style.Render(label) + "\n")
	}

	b.WriteByte('\n')
	b.WriteString(dialogSubtle.Render("Enter edit/action  Esc back"))

	return b.String()
}

// --- TOML editor dialog ---

func (m Model) handleTOMLEditorKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.tomlEditor == nil {
		m.dialog = dialogPlugins
		return m, nil
	}

	saved, closed, cmd := m.tomlEditor.HandleKey(msg.String())

	if saved {
		// Reload plugins after save, re-enable mouse
		if err := m.pluginRegistry.LoadFromDir(config.PluginsDir()); err != nil {
			log.Printf("reload plugins: %v", err)
		}
		// Local detection only in local mode — see the identical guard on the
		// Plugins dialog's Reload/Restore buttons above.
		if !m.remoteModeFor(m.activeDest()) {
			m.pluginRegistry.DetectAvailability()
		}
		m.tomlEditor = nil
		m.dialog = dialogPlugins
		m.dialogCursor = 0
		reloadCmd := reloadPluginsThenAskCmd(m.client)
		if cmd != nil {
			return m, tea.Batch(reloadCmd, cmd)
		}
		return m, tea.Batch(reloadCmd)
	}

	if closed {
		m.tomlEditor = nil
		m.dialog = dialogPlugins
		return m, nil
	}

	return m, cmd
}

// --- Log viewer dialog ---
//
// Reuses TextEditor in ReadOnly + HighlightPlain mode to show client/daemon/MCP
// log files. Opened from F1 → "View ... log". Esc returns to the F1 About menu.

// maxLogViewBytes caps how much of a log file we read into memory. Logs can
// grow unbounded; we tail the last N KB so the editor stays responsive.
const maxLogViewBytes = 256 * 1024

// openLogViewer reads the file at path (last maxLogViewBytes bytes) and opens
// the read-only TextEditor in dialogLogViewer mode. label is shown in the file
// path field so the user sees what they're looking at.
// logViewerViewport returns the viewport dimensions used by every log-viewer
// editor (client log, daemon log, MCP logs). Centralized so future tweaks to
// padding apply uniformly.
func (m Model) logViewerViewport() (w, h int) {
	w = m.width - 4
	if w < 40 {
		w = 40
	}
	h = m.height - 4
	if h < 5 {
		h = 5
	}
	return w, h
}

// newLogViewerEditor builds a read-only TextEditor pre-positioned at the end
// of the buffer (so the freshest log lines are in view) and stamped with the
// configured log-viewer page size.
// The receiver is a POINTER so this can also be the one place a stale mouse
// drag is cleared. Terminals routinely drop the release when the button comes
// up outside the window, and nothing on the close path resets viewerMouseDown —
// so without this, the first hover over the NEXT buffer resumed the previous
// one's drag, anchoring a selection at a document position from a different
// prompt with no button held. Every viewer entry point funnels through here, so
// clearing here cannot be forgotten by a future one.
func (m *Model) newLogViewerEditor(content, path string) *TextEditor {
	m.clearDragState()
	viewW, viewH := m.logViewerViewport()
	editor := NewTextEditor(content, path, viewW, viewH)
	editor.Highlight = HighlightPlain
	editor.ReadOnly = true
	editor.PageSize = m.cfg.UI.LogViewerPageLines
	editor.CursorRow = len(editor.Lines) - 1
	if editor.CursorRow < 0 {
		editor.CursorRow = 0
	}
	editor.CursorCol = 0
	editor.ensureCursorVisible()
	return editor
}

// openReadonlyText opens arbitrary in-memory content in the same full-screen
// read-only TextEditor used by the log viewer. label appears in the editor's
// path field.
//
// Two deliberate differences from the log viewer, both because the content is a
// prompt rather than a log: it soft-wraps, and it opens at the TOP.
//
// A prompt is frequently one very long logical line (a pasted paragraph, a
// stack trace, a JSON blob). Hard truncation puts everything past the right
// edge permanently out of reach — including out of reach of the selection the
// user opened the entry to copy, since there is no horizontal scroll. A log's
// lines are already short and its interesting end is the bottom, so it keeps
// truncation and its end-of-buffer landing.
func (m Model) openReadonlyText(label, content string) (tea.Model, tea.Cmd) {
	e := m.newLogViewerEditor(content, label)
	// Order matters: with SoftWrap on, ScrollTop is a VISUAL row index, so the
	// flag has to be set before the position is chosen.
	e.SoftWrap = true
	e.CursorRow = 0
	e.CursorCol = 0
	e.ScrollTop = 0
	m.tomlEditor = e
	m.dialog = dialogLogViewer
	// Esc returns to the history list this was opened from, not the About menu
	// (the log viewer's default parent).
	m.logViewerReturn = dialogCommandHistory
	return m, tea.ClearScreen
}

func (m Model) openLogViewer(label, path string) (tea.Model, tea.Cmd) {
	content, err := readLogTail(path, maxLogViewBytes)
	if err != nil {
		// Show the error inline in an empty editor so the user knows
		// what went wrong (file missing, permission denied, etc.).
		content = fmt.Sprintf("# %s\n# %s\n#\n# Could not read log file: %v\n",
			label, path, err)
	}
	m.tomlEditor = m.newLogViewerEditor(content, path)
	m.dialog = dialogLogViewer
	m.logViewerReturn = dialogAbout
	return m, tea.ClearScreen
}

// openMCPLogsViewer aggregates the per-pane MCP interaction logs from
// config.MCPLogDir() into a single read-only buffer with file-name headers,
// most-recently-modified file first.
func (m Model) openMCPLogsViewer() (tea.Model, tea.Cmd) {
	m.logViewerReturn = dialogAbout
	dir := config.MCPLogDir(m.cfg.MCP)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return m.openLogViewer("MCP logs", filepath.Join(dir, "(unavailable)"))
	}

	type logFile struct {
		name string
		mod  time.Time
		size int64
	}
	var files []logFile
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		files = append(files, logFile{name: e.Name(), mod: info.ModTime(), size: info.Size()})
	}
	// Most recently modified first.
	sort.Slice(files, func(i, j int) bool {
		return files[i].mod.After(files[j].mod)
	})

	if len(files) == 0 {
		empty := fmt.Sprintf("# MCP logs\n# %s\n#\n# No MCP interactions logged yet.\n", dir)
		m.tomlEditor = m.newLogViewerEditor(empty, dir)
		m.dialog = dialogLogViewer
		return m, tea.ClearScreen
	}

	// Build aggregated content. Cap each file to a reasonable share of
	// maxLogViewBytes so one huge file doesn't squeeze out the others.
	perFile := maxLogViewBytes / len(files)
	if perFile < 4*1024 {
		perFile = 4 * 1024
	}
	var b strings.Builder
	for _, f := range files {
		b.WriteString(fmt.Sprintf("\n========== %s  (%s, %d bytes) ==========\n\n",
			f.name, f.mod.Format("2006-01-02 15:04:05"), f.size))
		full := filepath.Join(dir, f.name)
		tail, terr := readLogTail(full, perFile)
		if terr != nil {
			b.WriteString(fmt.Sprintf("(read error: %v)\n", terr))
			continue
		}
		b.WriteString(tail)
		if !strings.HasSuffix(tail, "\n") {
			b.WriteByte('\n')
		}
	}

	m.tomlEditor = m.newLogViewerEditor(b.String(), dir)
	m.dialog = dialogLogViewer
	return m, tea.ClearScreen
}

// readLogTail reads up to maxBytes from the END of the given file. If the
// file is shorter than maxBytes the whole file is returned. Always reads from
// the start of a line (skipping any partial first line) so the result is
// well-formed.
//
// Symlinks are rejected: an Lstat is performed first and any non-regular
// file (symlink, device, named pipe, etc.) is refused. This prevents the log
// viewer from being redirected to read arbitrary files via a swapped link
// inside ~/.quil/. Same hardening pattern as internal/persist/notes.go.
func readLogTail(path string, maxBytes int) (string, error) {
	li, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !li.Mode().IsRegular() {
		return "", fmt.Errorf("refusing to read non-regular file %q (mode=%v)", path, li.Mode())
	}

	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	// Re-stat through the open handle to defeat a TOCTOU swap between Lstat
	// and Open. If the inode changed we refuse the read.
	stat, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !stat.Mode().IsRegular() {
		return "", fmt.Errorf("refusing to read non-regular file %q after open", path)
	}
	size := stat.Size()
	if size <= int64(maxBytes) {
		data, rerr := io.ReadAll(f)
		if rerr != nil {
			return "", rerr
		}
		return string(data), nil
	}

	// Seek to (size - maxBytes), read to end, then drop everything before
	// the first newline so we don't show a partial line.
	if _, err := f.Seek(size-int64(maxBytes), io.SeekStart); err != nil {
		return "", err
	}
	buf := make([]byte, maxBytes)
	n, err := f.Read(buf)
	if err != nil {
		return "", err
	}
	buf = buf[:n]
	if i := bytes.IndexByte(buf, '\n'); i >= 0 && i+1 < len(buf) {
		buf = buf[i+1:]
	}
	return "[... older lines truncated ...]\n" + string(buf), nil
}

// handleLogViewerKey routes editor keys to the read-only TextEditor for
// log viewing. Esc closes the viewer and returns to the F1 About menu.
// Save (Ctrl+S) is suppressed by TextEditor.ReadOnly so we never overwrite
// a log file by accident.
func (m Model) handleLogViewerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// Return target depends on where the viewer was opened from: the About menu
	// for logs, the history list for a history entry. Default to About for
	// safety if a caller forgot to set it.
	ret := m.logViewerReturn
	if ret == dialogNone {
		ret = dialogAbout
	}
	if m.tomlEditor == nil {
		m.dialog = ret
		m.logViewerReturn = dialogNone
		return m, nil
	}
	_, closed, cmd := m.tomlEditor.HandleKey(msg.String())
	if closed {
		m.tomlEditor = nil
		m.dialog = ret
		m.logViewerReturn = dialogNone
		// Cursor is at the position of the menu item the user came from
		// (3, 4, or 5). Don't reset it — feels jarring.
		return m, tea.ClearScreen
	}
	return m, cmd
}

// --- Plugin error dialog ---

func (m Model) handlePluginErrorKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "enter":
		m.dialog = dialogNone
		return m, nil
	}
	return m, nil
}

func (m Model) renderPluginErrorDialog() string {
	var b strings.Builder

	b.WriteString(dialogTitle.Render(m.pluginErrorTitle))
	b.WriteString("\n\n")

	// Render multi-line message
	lines := strings.Split(m.pluginErrorMessage, "\n")
	for _, line := range lines {
		b.WriteString("  " + dialogNormal.Render(line) + "\n")
	}

	b.WriteByte('\n')
	b.WriteString(dialogSubtle.Render("Enter/Esc dismiss"))

	return b.String()
}

// --- Pane setup dialog (CWD prompt + runtime toggles) ---
//
// Receiver convention for the helpers below: Bubble Tea's Update loop expects
// `func (m Model) Update(msg) (Model, Cmd)`, so the top-level handlers
// (handleCreatePaneSetupKey, handleSetupCWDKey, submitSetupDialog,
// renderCreatePaneSetupDialog, setupFieldCount, setupFieldKind) all use a
// value receiver and return the (modified) `m` for the framework to install
// as the next state. Helpers they call internally — enterSetupOrSplit,
// initSetupBrowser, browseTo, browseUp, adjustBrowseScroll — mutate state
// in place and use a pointer receiver, since they're invoked via the
// addressable local `m` inside a value-receiver method (Go takes its address
// implicitly). Mixing the two styles is intentional and matches the pattern
// already used for handleInstanceFormKey + openInstanceForm in this file.

// enterSetupOrSplit routes after a plugin or instance is picked: either show
// the setup dialog (if the plugin prompts for a CWD or has toggles) or finish
// choosing via advanceFromPluginChoice — which means the placement step for a
// split, and the submit itself for a new tab. Callers must NOT advance the step
// themselves; four of them used to, and keeping them in agreement is exactly
// what that helper exists to remove.
//
// Receiver is *Model because this method always mutates state — even on the
// "no setup" branch it must clear stale CWD/toggle state from a prior plugin
// (otherwise picking plugin A → setup → Esc → plugin B leaks A's CWD into
// B's spawn). See the matching comment near the rest of the setup helpers.
func (m *Model) enterSetupOrSplit(p *plugin.PanePlugin) tea.Cmd {
	// Always clear setup state first — even when the new plugin has no setup
	// dialog, leftover state from a prior plugin must not survive into the
	// next CreatePanePayload.
	m.selectedCWD = ""
	m.cwdInputError = ""
	m.toggleStates = nil
	m.setupFieldCursor = 0
	// Routes through onSetupCWDChanged rather than a bare assignment: a prior
	// plugin's chosen worktree (selectedWorktree, its cursor/scroll, and the
	// cached listing) is otherwise still sitting in Model when this dialog
	// session opens, and none of the resets below touch it. Re-entering
	// Ctrl+N after picking a worktree in repo A, then having repo B pre-fill
	// through the pick list without ever focusing the worktree field, would
	// submit repo A's worktree path for repo B.
	m.onSetupCWDChanged("")
	m.cwdBrowseEntries = nil
	m.cwdBrowseCursor = 0
	m.cwdBrowseScroll = 0
	m.cwdBrowseParent = ""
	m.cwdBrowseRoots = nil
	m.cwdBrowseTruncated = false
	m.cwdBrowseRootsTruncated = false
	m.browseCandidates = nil
	// Also drop any in-flight browse from the previous plugin, so its answer
	// cannot land in this dialog's listing. Safe to zero: no call site ever
	// requests the empty Path, so the zero value cannot match a real response.
	m.browse = browseState{}
	// Same reasoning for an in-flight git-repo scan — whether it was this
	// dialog's own pick-list request for a prior plugin, or a still-running
	// Alt+G overlay discovery, its answer must not land in whatever setup
	// session is open when it finally arrives. Losing an in-flight Alt+G scan
	// this way is a deliberate trade: the user has since moved into Ctrl+N, and
	// pressing Alt+G again costs one keypress against a wrong-repo overlay.
	m.repoScan = repoScanState{}
	m.repoCandidates = nil
	m.recentCandidates = nil
	// Same reasoning as the repoScan reset above: an existence check still in
	// flight from the previous plugin must not land in this dialog's pick list.
	m.recentScan = recentScanState{}
	m.kubeContexts = nil
	m.kubeCursor = 0
	m.kubeScan = kubeScanState{}
	m.kubeTruncated = false
	m.resetSessionSelection()

	needsSetup := p != nil && (p.Command.PromptsCWD || len(p.Command.Toggles) > 0 ||
		p.Command.Discover == "kube" || p.Command.Sessions == "claude")
	if !needsSetup {
		return m.advanceFromPluginChoice()
	}

	// The browser's pre-fill now costs a round trip, so the dialog opens first
	// and fills in when the daemon answers.
	var browseCmd tea.Cmd

	if p.Command.PromptsCWD {
		if p.Command.Discover == "git" {
			base := m.setupDiscoveryBase()
			// Asked of the DAEMON, never resolved here — gitdiscover run in this
			// process stats the machine drawing the UI, which is the wrong disk
			// whenever the daemon is remote (RD-021). Whether there turn out to
			// be any candidates isn't known until the answer lands, so the
			// recent-locations/browser fallback that used to run right below
			// this branch now runs in applyGitReposPickList instead.
			browseCmd = m.requestGitRepos(base, "", repoScanPickList, "")
		} else {
			browseCmd = m.fallbackToRecentOrBrowser()
		}
	}

	var kubeCmd tea.Cmd
	if p.Command.Discover == "kube" {
		kubeCmd = m.requestKubeContexts()
	}

	// Initialize toggle states from defaults. For mutually-exclusive groups
	// (toggles that share a non-empty Group value), at most one member can
	// default to true — if multiple are declared with default = true, the
	// last one in declaration order wins and earlier members are forced off.
	// This preserves the group invariant (at most one on) from the very
	// first render of the dialog.
	m.toggleStates = make([]bool, len(p.Command.Toggles))
	for i, t := range p.Command.Toggles {
		m.toggleStates[i] = t.Default
	}
	enforceToggleGroups(p.Command.Toggles, m.toggleStates, -1)

	m.dialogEdit = false // browser doesn't use edit mode
	m.dialog = dialogCreatePaneSetup
	return tea.Batch(tea.ClearScreen, browseCmd, kubeCmd)
}

// fallbackToRecentOrBrowser offers the recent-locations quick pick, falling
// back to the directory-browser pre-fill chain if none of those still exist
// (or there is nothing remembered at all).
//
// Shared by two callers: a PromptsCWD plugin with no git discovery goes
// straight here from enterSetupOrSplit, and the git pick list falls back to
// the exact same choice — from applyGitReposPickList — once a scan comes back
// with nothing to offer, whether that's a real "no repo here" or a failed
// scan. The two used to be one inline switch; discovery moving behind an RPC
// split it, because "did the scan find anything" is no longer known at the
// point enterSetupOrSplit returns.
func (m *Model) fallbackToRecentOrBrowser() tea.Cmd {
	if len(m.recentCWDs) > 0 {
		// Which of the remembered directories still exist is a question about
		// the DAEMON's disk. Answered here with os.Stat until RD-024, which
		// reads the machine drawing the UI: against a remote host every server
		// path failed that test and the pick list rendered silently empty —
		// indistinguishable from a feature that had never been used, because
		// structurally nothing had failed.
		//
		// The fallback to the browser moves with it, into applyExistingDirs:
		// whether anything survives is no longer known when this returns.
		return m.requestExistingDirs(m.recentCWDs)
	}
	return m.initSetupBrowser()
}

// initSetupBrowser seeds the directory browser using the standard pre-fill
// chain: last selected CWD -> active pane OSC7 CWD -> home.
//
// The daemon answers each candidate, so what was a loop is now a chain: this
// asks about the first candidate and applyBrowseDir advances to the next one
// when the answer carries an Error. Stale entries are still skipped (and
// lastSelectedCWD still cleared), just one round trip apart.
//
// The home fallback is the literal "~", expanded by the DAEMON. os.UserHomeDir
// names a directory on the machine DRAWING the dialog — against a remote host
// that path does not exist there, so the chain would exhaust on every remote
// session and leave the browser empty. Locally the two are the same directory.
func (m *Model) initSetupBrowser() tea.Cmd {
	candidates := []string{m.lastSelectedCWD}
	if tab := m.activeTabModel(); tab != nil {
		if pane := tab.ActivePaneModel(); pane != nil {
			candidates = append(candidates, pane.CWD)
		}
	}
	// The LITERAL "~", deliberately — not os.UserHomeDir(). See above.
	candidates = append(candidates, "~")

	// Consecutive duplicates are dropped. The remembered directory is very
	// often the active pane's CWD, and asking twice costs a round trip to be
	// told the same thing — or, when it fails, to fail identically. It also
	// keeps the chain from ever issuing two requests with the same (Path,
	// Child) key back to back, which is the one shape the browse client's
	// staleness key cannot tell apart.
	m.browseCandidates = dedupeAdjacent(candidates)
	return m.nextBrowseCandidate()
}

// dedupeAdjacent drops runs of equal strings, preserving order. Compared
// verbatim: these are candidate paths bound for the daemon, and case-folding or
// separator-normalising them here would be this machine answering for the one
// holding the filesystem.
func dedupeAdjacent(in []string) []string {
	out := in[:0:0]
	for i, s := range in {
		if i > 0 && s == in[i-1] {
			continue
		}
		out = append(out, s)
	}
	return out
}

// nextBrowseCandidate asks about the next pre-fill candidate, skipping empty
// ones without spending a round trip on them.
//
// Returns nil once the chain is exhausted, which is what stops the browser from
// looping: the last error stays on screen and the listing stays empty, rather
// than the chain restarting from the top.
func (m *Model) nextBrowseCandidate() tea.Cmd {
	for len(m.browseCandidates) > 0 {
		dir := m.browseCandidates[0]
		m.browseCandidates = m.browseCandidates[1:]
		if dir == "" {
			continue
		}
		return m.requestBrowseDir(dir, "", "")
	}
	return nil
}

// applyBrowseResponse is the setup dialog's whole reaction to one browse
// answer: apply it, then let the pre-fill chain react to what it turned out to
// be.
//
// The chain policy lives HERE rather than inside applyBrowseDir so the
// direction of knowledge runs dialog → client and not the other way: the client
// half reports what it observed, and only this side knows what a failure means
// to a dialog that is still choosing its opening directory.
func (m *Model) applyBrowseResponse(resp ipc.BrowseDirRespPayload, gen string) tea.Cmd {
	switch m.applyBrowseDir(resp, gen) {
	case browseFailed:
		return m.advanceBrowseCandidates(resp.Path)
	case browseFilled:
		m.browseCandidates = nil // a candidate answered; the chain is done
		// The worktree field's choice and listing are scoped to whichever
		// directory this response just landed on. This is the directory
		// browser's actual commit point for Enter/Backspace navigation —
		// handleSetupCWDKey only issues the request, the answer lands here,
		// asynchronously — so it is where a worktree chosen for the
		// directory just left must be cleared, or submitSetupDialog would
		// read a stale worktree path against the new cwdBrowseDir. Gated
		// like applyGitReposPickList: this same client machinery answers the
		// project dialog's browser too, which has no worktree field to go
		// stale.
		if m.dialog == dialogCreatePaneSetup {
			return m.onSetupCWDChanged(m.cwdBrowseDir)
		}
	}
	return nil
}

// advanceBrowseCandidates continues the pre-fill chain past a candidate the
// daemon could not list. `failed` is the request path the response echoed.
//
// An empty chain means this failure was not a pre-fill attempt — the user
// navigated somewhere unreadable — so nothing advances and nothing is
// forgotten. That check comes first for exactly that reason: browseTo abandons
// the chain, so "chain non-empty" IS "we are still pre-filling", and only there
// does a failure say something about the REMEMBERED directory rather than about
// where the user just tried to go.
func (m *Model) advanceBrowseCandidates(failed string) tea.Cmd {
	if len(m.browseCandidates) == 0 {
		return nil
	}
	if failed != "" && failed == m.lastSelectedCWD {
		m.lastSelectedCWD = "" // clear stale memory
	}
	return m.nextBrowseCandidate()
}

// applyGitReposPickList is the setup dialog's whole reaction to a git pick-list
// discovery response that came back without an error: populate the candidate
// list, or — if the scan genuinely found nothing — fall back to the same
// recent-locations/browser chain a non-git PromptsCWD plugin uses.
//
// discover_client.go's applyGitRepos deliberately holds none of this policy —
// see its doc comment — because the pick list is only one of two callers and
// what a response MEANS to it (a candidate list to render, a cap to enforce,
// a fallback to run) is dialog-specific knowledge the client half has no
// business carrying.
//
// Dropped if the setup dialog has since closed (Esc, submit, or a different
// plugin selected — all of which change m.dialog away from
// dialogCreatePaneSetup): applying it would populate a pick list nobody is
// looking at, or worse, one belonging to whatever setup the user opened next.
func (m *Model) applyGitReposPickList(repos []string) tea.Cmd {
	if m.dialog != dialogCreatePaneSetup {
		return nil
	}
	m.repoCandidates = repos
	if len(m.repoCandidates) > maxRepoCandidates {
		m.repoCandidates = m.repoCandidates[:maxRepoCandidates]
	}
	if len(m.repoCandidates) == 0 {
		return m.fallbackToRecentOrBrowser()
	}
	// Pre-select the first git candidate so Enter-through submits it. Routed
	// through onSetupCWDChanged like every other site that commits a browsed
	// directory — reachable on dialog RE-ENTRY (a prior plugin's worktree
	// choice is otherwise still sitting in m.selectedWorktree here) even
	// though enterSetupOrSplit already clears it on ordinary entry; one call
	// site cannot get this wrong twice.
	m.onSetupCWDChanged(m.repoCandidates[0])
	m.cwdBrowseCursor = 0
	return nil
}

// applyGitReposPickListError is the pick list's reaction to a failed scan.
//
// The fallback is identical to applyGitReposPickList's "found nothing" case —
// the dialog still needs a CWD from somewhere — but a failure must not be
// indistinguishable from "no repositories here", which is a real, confidently
// reportable finding. The flash is what keeps the two apart; discover_client.go
// has already logged the underlying error.
func (m *Model) applyGitReposPickListError() tea.Cmd {
	if m.dialog != dialogCreatePaneSetup {
		return nil
	}
	// Already nil from enterSetupOrSplit's reset — set explicitly anyway so
	// this function states the "empty pick list" guarantee itself rather than
	// relying on that reset never changing.
	m.repoCandidates = nil
	m.setFlash("repo scan failed")
	return tea.Batch(m.flashCmd(), m.fallbackToRecentOrBrowser())
}

// browseTo issues a user-driven navigation request.
//
// It abandons any pre-fill chain still in flight: the user has said where they
// want to be, and bouncing them to a fallback directory because THIS request
// failed would move the browser somewhere nobody asked for. Also clears the
// clipboard error, which belongs to the previous keystroke.
func (m *Model) browseTo(path, child, selectName string) tea.Cmd {
	m.browseCandidates = nil
	m.cwdInputError = ""
	return m.requestBrowseDir(path, child, selectName)
}

// browseUp navigates one level up, using the daemon's own answer for what that
// means. Nothing here computes a parent: separators and the set of filesystem
// roots are properties of the machine holding the disk.
func (m *Model) browseUp() tea.Cmd {
	if m.cwdBrowseDir == "" {
		return nil // already showing the root list; nothing above it
	}
	if m.cwdBrowseParent == "" {
		// At a filesystem root. Above it is the root list when the daemon
		// reported one (Windows drives) and nothing at all when it did not
		// (Unix, where "/" has no parent).
		if len(m.cwdBrowseRoots) > 0 {
			m.showRootsList()
		}
		return nil
	}
	// selectName keeps the user oriented: the cursor lands on the folder they
	// just left rather than at the top of the parent listing.
	return m.browseTo(m.cwdBrowseParent, "", browseLeaf(m.cwdBrowseDir, m.cwdBrowseParent))
}

// browseLeaf returns the final element of dir, given the parent the daemon
// reported for it.
//
// Derived by subtracting one daemon-supplied string from the other rather than
// with filepath.Base, which splits on the LOCAL platform's separators: a Unix
// client would read `C:\srv\work` as a single element and a Windows client
// would mis-split a path containing a literal backslash. Both inputs come from
// the same machine, so their difference is the leaf whatever its separator is.
//
// Degrades to "" when the two do not line up, which simply leaves the cursor at
// the top of the parent listing.
func browseLeaf(dir, parent string) string {
	if parent == "" || !strings.HasPrefix(dir, parent) {
		return ""
	}
	return strings.Trim(dir[len(parent):], `/\`)
}

// showRootsList renders the daemon-reported filesystem roots as the listing.
// cwdBrowseDir = "" is the existing "showing the root list, not inside any
// directory" sentinel.
func (m *Model) showRootsList() {
	m.cwdBrowseDir = ""
	m.cwdBrowseParent = ""
	m.cwdBrowseEntries = m.cwdBrowseRoots
	m.cwdBrowseCursor = 0
	m.cwdBrowseScroll = 0
	m.cwdInputError = ""
	m.browse.err = ""
	// Swapped, not cleared. Carrying the previous directory's cap forward would
	// warn about a listing no longer on screen, but the root list is not
	// automatically complete either: the daemon abandons the sweep after a
	// couple of unresponsive drives, and a drive missing for that reason is
	// indistinguishable from one that was never mapped.
	m.cwdBrowseTruncated = m.cwdBrowseRootsTruncated

	// The root list IS a new browsed "directory" (cwdBrowseDir just changed to
	// "" above), reached by ordinary "up" navigation from a filesystem root —
	// browseUp calls this directly, without a round trip, so the reset belongs
	// here rather than only in applyBrowseResponse. Gated like that site: this
	// function is ALSO called from the project dialog's browseUp
	// (projectdialog.go), which has no worktree field to go stale, and must
	// stay untouched by setup-dialog policy.
	if m.dialog == dialogCreatePaneSetup {
		m.onSetupCWDChanged("")
	}
}

// applyBrowseListing fills the directory browser from an already-resolved
// listing. Only directories are shown; ".." is prepended when showUp is set.
//
// Split from the read above so the browser can be fed from the daemon (RD-020)
// without duplicating any of this. The signature is deliberately the shape a
// BrowseDirRespPayload arrives in — entries carry IsDir because the daemon
// reports files too, and showUp is a decision the SERVER makes: it owns both
// the "is this a root" test and, on Windows, the drive list, neither of which
// the client can compute for a filesystem it cannot see.
//
// Sorting is case-insensitive here rather than relying on the daemon's order.
// The daemon sorts directories first so its entry cap cannot strip every
// folder from a large listing; that is a cap-safety measure, not a
// presentation choice, and presentation belongs on this side.
func (m *Model) applyBrowseListing(resolved string, entries []ipc.BrowseEntry, showUp bool, selectName string) {
	dirs := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir {
			dirs = append(dirs, e.Name)
		}
	}
	sort.Slice(dirs, func(i, j int) bool {
		return strings.ToLower(dirs[i]) < strings.ToLower(dirs[j])
	})

	listing := make([]string, 0, len(dirs)+1)
	if showUp {
		listing = append(listing, "..")
	}
	listing = append(listing, dirs...)

	m.cwdBrowseDir = resolved
	m.cwdBrowseEntries = listing
	m.cwdBrowseCursor = 0
	m.cwdBrowseScroll = 0

	// Position the cursor on the requested entry if asked.
	if selectName != "" {
		for i, name := range listing {
			if name == selectName {
				m.cwdBrowseCursor = i
				m.adjustBrowseScroll()
				break
			}
		}
	}
}

// browserVisibleRows is the height of the directory browser viewport.
const browserVisibleRows = 12

// truncatedHintPrefix marks a listing the daemon capped.
//
// Deliberately says "hidden" rather than naming a number: the cap counts
// ENTRIES, the browser shows DIRECTORIES, and the daemon sorts directories
// ahead of files before capping — so a capped listing may have lost only files
// and still show every folder. The client cannot tell the two apart, and the
// honest claim it can always make is that the answer is partial. Quoting a
// count here would be precise about the wrong thing.
const truncatedHintPrefix = "⚠ capped, some entries hidden  "

// maxRepoCandidates bounds the setup-dialog pick list: the dialog has no
// scroll machinery for this mode, so the list must fit the box. Overflow
// repos remain reachable via the Browse… escape hatch.
const maxRepoCandidates = 10

// maxKubeContexts bounds the kube-context pick list (discover="kube"). Like
// the repo list it has no scroll machinery; "Default context" is always
// available, so an overflowing kubeconfig still spawns a usable pane.
const maxKubeContexts = 50

// adjustBrowseScroll keeps the cursor inside the visible window.
func (m *Model) adjustBrowseScroll() {
	if m.cwdBrowseCursor < m.cwdBrowseScroll {
		m.cwdBrowseScroll = m.cwdBrowseCursor
	}
	if m.cwdBrowseCursor >= m.cwdBrowseScroll+browserVisibleRows {
		m.cwdBrowseScroll = m.cwdBrowseCursor - browserVisibleRows + 1
	}
	if m.cwdBrowseScroll < 0 {
		m.cwdBrowseScroll = 0
	}
}

// enforceToggleGroups applies the mutual-exclusion invariant to `states`:
// within each non-empty Group, at most one member may be true. When a
// specific toggle has just been turned on, pass its index as `winner` so
// the other members of its group are turned off. When called without a
// specific winner (initial defaults), `winner` should be -1 and each
// group is collapsed to its last-declared true member (so declaration
// order acts as a tiebreaker when multiple defaults are true).
//
// Toggles whose Group is empty are never touched — they remain
// independent checkboxes.
func enforceToggleGroups(toggles []plugin.Toggle, states []bool, winner int) {
	if len(toggles) == 0 || len(states) == 0 {
		return
	}
	if winner >= 0 && winner < len(toggles) {
		group := toggles[winner].Group
		if group == "" {
			return
		}
		for i, t := range toggles {
			if i == winner {
				continue
			}
			if i < len(states) && t.Group == group {
				states[i] = false
			}
		}
		return
	}
	// No explicit winner: per group, keep only the last true member.
	lastOn := make(map[string]int)
	for i, t := range toggles {
		if t.Group == "" || i >= len(states) || !states[i] {
			continue
		}
		lastOn[t.Group] = i
	}
	for i, t := range toggles {
		if t.Group == "" || i >= len(states) || !states[i] {
			continue
		}
		if lastOn[t.Group] != i {
			states[i] = false
		}
	}
}

// setupFieldCount returns the number of focusable fields in the setup dialog:
// CWD (if PromptsCWD) + kube context (if discover="kube") + one per toggle +
// worktree picker (if PromptsCWD) + session picker (if sessions="claude") + 1
// for the Continue button.
func (m Model) setupFieldCount(p *plugin.PanePlugin) int {
	n := len(p.Command.Toggles) + 1 // +1 for Continue
	if p.Command.PromptsCWD {
		n++
	}
	if p.Command.Discover == "kube" {
		n++
	}
	if p.Command.PromptsCWD {
		n++ // the worktree field, scoped to the CWD above it
	}
	if p.Command.Sessions == "claude" {
		n++
	}
	return n
}

// setupFieldKind reports what field is at the given cursor index in the setup
// dialog. Returns "cwd", "kube", "toggle" (with toggleIdx), "worktree",
// "session", or "continue".
//
// Order is CWD → kube → toggles → worktree → session → Continue. The worktree
// picker stays downstream of CWD because its contents are scoped to that
// directory, and upstream of the session picker because the session listing is
// scoped to whichever directory the worktree choice settles on. The session
// picker sits last because it is the only field that expands: keeping it below
// the fixed-height rows means focusing it does not shift them.
func (m Model) setupFieldKind(p *plugin.PanePlugin, cursor int) (kind string, toggleIdx int) {
	i := cursor
	if p.Command.PromptsCWD {
		if i == 0 {
			return "cwd", -1
		}
		i--
	}
	if p.Command.Discover == "kube" {
		if i == 0 {
			return "kube", -1
		}
		i--
	}
	if i < len(p.Command.Toggles) {
		return "toggle", i
	}
	i -= len(p.Command.Toggles)
	if p.Command.PromptsCWD {
		if i == 0 {
			return "worktree", -1
		}
		i--
	}
	if p.Command.Sessions == "claude" {
		if i == 0 {
			return "session", -1
		}
		i--
	}
	return "continue", -1
}

// handleCreatePaneSetupKey handles keystrokes in dialogCreatePaneSetup.
func (m Model) handleCreatePaneSetupKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := m.pluginRegistry.Get(m.selectedPlugin)
	if p == nil {
		m.dialog = dialogCreatePane
		m.createPaneStep = 1
		return m, tea.ClearScreen
	}

	key := msg.String()
	kind, togIdx := m.setupFieldKind(p, m.setupFieldCursor)

	// The session detail panel takes Esc before the dialog does — while it is
	// open Esc means "close this panel", not "abandon the whole setup dialog".
	// Checked ahead of the shared Esc branch below because that one returns
	// unconditionally.
	if kind == "session" && m.sessionDetail.open && key == "esc" {
		m.closeSessionDetail()
		return m, nil
	}

	// The open name field takes Esc for the same reason, and needs it for a
	// sharper one: handleWorktreeNameKey documents Esc as abandoning the
	// new-branch row, but the shared branch below returns unconditionally, so
	// that case was unreachable from the keyboard — Esc backed out of pane
	// creation entirely and the typed branch was never abandoned at all. It is
	// the only way to undo a name now that tabbing away commits one; backspace
	// to empty was otherwise the whole vocabulary. Routed INTO the field
	// handler rather than clearing here, so "abandon" has one definition and
	// the field handler's own coverage describes something reachable.
	if kind == "worktree" && m.worktreeNaming && key == "esc" {
		return m.handleWorktreeNameKey(key)
	}

	// Esc and Tab/Shift+Tab work the same regardless of which field is focused.
	switch key {
	case "esc":
		if len(p.Command.FormFields) > 0 {
			m.dialog = dialogInstanceForm
			// The instance-form flow lives at step 2; restore that explicitly
			// rather than relying on whatever value happened to be left in
			// createPaneStep before the setup dialog was opened.
			m.createPaneStep = 2
		} else {
			m.dialog = dialogCreatePane
			m.createPaneStep = 1
		}
		m.cwdInputError = ""
		m.dialogEdit = false
		m.dialogCursor = 0
		return m, tea.ClearScreen

	case "tab":
		return m.moveSetupCursor(p, 1)

	case "shift+tab":
		return m.moveSetupCursor(p, -1)
	}

	// Field-specific behavior.
	switch kind {
	case "cwd":
		return m.handleSetupCWDKey(p, key)

	case "kube":
		return m.handleSetupKubeKey(p, key)

	case "toggle":
		switch key {
		case " ", "space":
			if togIdx >= 0 && togIdx < len(m.toggleStates) {
				m.toggleStates[togIdx] = !m.toggleStates[togIdx]
				// If this toggle belongs to a mutual-exclusion group and
				// was just turned ON, turn OFF the other members of the
				// same group. Turning OFF is always safe (leaves the
				// group fully unselected — a valid state).
				if m.toggleStates[togIdx] {
					enforceToggleGroups(p.Command.Toggles, m.toggleStates, togIdx)
				}
			}
			return m, nil
		case "up", "k":
			return m.moveSetupCursor(p, -1)
		case "down", "j":
			return m.moveSetupCursor(p, 1)
		case "enter":
			return m.submitSetupDialog(p)
		}
		return m, nil

	case "worktree":
		return m.handleSetupWorktreeKey(p, key)

	case "session":
		return m.handleSetupSessionKey(p, key)

	case "continue":
		switch key {
		case "up", "k":
			return m.moveSetupCursor(p, -1)
		case "down", "j":
			return m.moveSetupCursor(p, 1)
		case "enter":
			return m.submitSetupDialog(p)
		}
		return m, nil
	}
	return m, nil
}

// moveSetupCursor advances the setup-dialog field cursor by delta (wrapping)
// and runs whatever the newly focused field needs on arrival.
//
// Every cursor move routes through here — Tab, Shift+Tab, and the up/down keys
// on the toggle and Continue rows — which is what makes the session field's
// lazy scan fire no matter which direction the cursor arrives from.
func (m Model) moveSetupCursor(p *plugin.PanePlugin, delta int) (tea.Model, tea.Cmd) {
	n := m.setupFieldCount(p)
	if n <= 0 {
		return m, nil
	}
	m.setupFieldCursor = ((m.setupFieldCursor+delta)%n + n) % n
	// Sequenced deliberately rather than inlined into the return: the call has
	// a pointer receiver and mutates the same `m` being returned, and Go does
	// not specify the evaluation order of a return statement's operands
	// relative to a call among them. It happens to work inlined today; written
	// this way it cannot silently stop working, which would leave the field
	// stuck with a request in flight whose echoed CWD never matches.
	cmd := m.onSetupFieldFocused(p)
	return m, cmd
}

// onSetupFieldFocused runs the side effect the now-focused field needs. Two
// fields fetch lazily, on first focus, rather than when the dialog opens: a
// fresh pane with no worktree and no resumed session — the common case —
// performs no extra I/O at all.
func (m *Model) onSetupFieldFocused(p *plugin.PanePlugin) tea.Cmd {
	switch kind, _ := m.setupFieldKind(p, m.setupFieldCursor); kind {
	case "session":
		return m.ensureSessionScan()
	case "worktree":
		if m.cwdBrowseDir == "" {
			// Nothing browsed yet — or the Windows roots list, where every
			// row IS a root rather than a child of some directory. Asking
			// here would have the daemon answer from ITS OWN default CWD,
			// listing worktrees of a repository nothing on screen names.
			return nil
		}
		if m.worktrees.pending {
			return nil
		}
		if m.worktrees.loaded && m.worktrees.err == "" && m.worktrees.path == m.cwdBrowseDir {
			return nil
		}
		return m.requestWorktrees(m.cwdBrowseDir)
	}
	return nil
}

// handleSetupSessionKey processes keystrokes when the session picker is
// focused. Row 0 is "New session" (no --resume); rows 1..N are the sessions
// recorded for the selected directory, newest first.
//
// Enter on a selectable row commits it and submits the dialog, matching the
// kube field. Enter on a row another live pane already holds is refused — the
// cursor still lands there so the footer can explain why.
func (m Model) handleSetupSessionKey(p *plugin.PanePlugin, key string) (tea.Model, tea.Cmd) {
	last := m.sessionRowCount() - 1
	// Paging matters more here than in the directory browser: a long-lived
	// project can hold 200 sessions, and arrow-only navigation would cost 200
	// keypresses to reach the oldest.
	page := m.sessionVisibleRows()

	// Moving with the detail panel open re-reads for the newly highlighted row,
	// so the panel is a mode you browse in rather than something to reopen per
	// session. ensureSessionDetail is a no-op when the row has not changed, so
	// a clamped move at either end costs nothing.
	move := func(to int) (tea.Model, tea.Cmd) {
		if to < 0 {
			to = 0
		}
		if to > last {
			to = last
		}
		m.sessionCursor = to
		m.adjustSessionScroll()
		if m.sessionDetail.open {
			return m, m.ensureSessionDetail()
		}
		return m, nil
	}

	switch key {
	case "i":
		if m.sessionDetail.open {
			m.closeSessionDetail()
			return m, nil
		}
		m.sessionDetail.open = true
		return m, m.ensureSessionDetail()

	case "up", "k":
		return move(m.sessionCursor - 1)

	case "down", "j":
		return move(m.sessionCursor + 1)

	case "pgup":
		return move(m.sessionCursor - page)

	case "pgdown":
		return move(m.sessionCursor + page)

	case "home":
		return move(0)

	case "end":
		return move(last)

	case "enter":
		if !m.commitSessionSelection() {
			return m, nil
		}
		return m.submitSetupDialog(p)
	}
	return m, nil
}

// handleSetupCWDKey processes keystrokes when the CWD browser field is focused.
// The browser shows a scrollable directory listing; arrows navigate, Enter
// descends/ascends, and Ctrl+V pastes a path to jump there.
func (m Model) handleSetupCWDKey(p *plugin.PanePlugin, key string) (tea.Model, tea.Cmd) {
	if pick, _ := m.activeCWDPick(); len(pick) > 0 {
		return m.handleSetupPickKey(p, key)
	}
	if len(m.cwdBrowseEntries) == 0 {
		switch key {
		case "enter":
			// Browser failed to load — Enter still submits using empty
			// selectedCWD.
			return m.submitSetupDialog(p)
		case "ctrl+v":
			// Falls through to the paste branch below. There is nothing to
			// navigate, but a pasted path can still get the browser somewhere —
			// and after a pre-fill chain that failed on every candidate it is
			// the only way out of an empty listing.
		default:
			return m, nil
		}
	}

	switch key {
	case "up", "k":
		if m.cwdBrowseCursor > 0 {
			m.cwdBrowseCursor--
			m.adjustBrowseScroll()
		}
		return m, nil

	case "down", "j":
		if m.cwdBrowseCursor < len(m.cwdBrowseEntries)-1 {
			m.cwdBrowseCursor++
			m.adjustBrowseScroll()
		}
		return m, nil

	case "pgup":
		m.cwdBrowseCursor -= browserVisibleRows
		if m.cwdBrowseCursor < 0 {
			m.cwdBrowseCursor = 0
		}
		m.adjustBrowseScroll()
		return m, nil

	case "pgdown":
		m.cwdBrowseCursor += browserVisibleRows
		if m.cwdBrowseCursor > len(m.cwdBrowseEntries)-1 {
			m.cwdBrowseCursor = len(m.cwdBrowseEntries) - 1
		}
		m.adjustBrowseScroll()
		return m, nil

	case "home":
		m.cwdBrowseCursor = 0
		m.adjustBrowseScroll()
		return m, nil

	case "end":
		m.cwdBrowseCursor = len(m.cwdBrowseEntries) - 1
		m.adjustBrowseScroll()
		return m, nil

	case "enter", "right", "l":
		entry := m.cwdBrowseEntries[m.cwdBrowseCursor]
		switch {
		case entry == "..":
			return m, m.browseUp()
		case m.cwdBrowseDir == "":
			// Root list: every row is already a full root path, so it is asked
			// about directly rather than joined onto anything.
			return m, m.browseTo(entry, "", "")
		default:
			// Child, not a join. The daemon joins with its own separator — see
			// BrowseDirReqPayload — so a Windows TUI against a Linux daemon
			// cannot ask for a path shaped like its own filesystem.
			return m, m.browseTo(m.cwdBrowseDir, entry, "")
		}

	case "backspace", "left", "h":
		return m, m.browseUp()

	case "ctrl+v":
		// Through the clipboardReadText seam, like the pane paste path: the
		// real reader touches the OS clipboard, which no test can rely on.
		text, err := clipboardReadText()
		if err != nil {
			log.Printf("setup dialog: clipboard read: %v", err)
			m.cwdInputError = fmt.Sprintf("clipboard: %v", err)
			return m, nil
		}
		// sanitizePastedPath is the whole of the client-side cleaning: trim,
		// unquote (Windows "Copy as path" wraps in double quotes), and strip
		// control bytes so a clipboard payload cannot inject escapes into the
		// error line. Everything else the old path did here — ~ expansion, Abs,
		// and the existence check — is the DAEMON's answer to give: statting
		// locally is what made a perfectly valid remote path unpasteable, and
		// the response's Error reports a bad path honestly.
		path := sanitizePastedPath(text)
		if path == "" {
			return m, nil
		}
		return m, m.browseTo(path, "", "")
	}
	return m, nil
}

// activeCWDPick returns the pick list currently offered by the CWD field and
// whether it is the recent-locations list (vs. discovered git repos). Git
// candidates take priority when both are present. An empty result means the
// field is in directory-browser mode.
func (m Model) activeCWDPick() (pick []string, isRecent bool) {
	if len(m.repoCandidates) > 0 {
		return m.repoCandidates, false
	}
	return m.recentCandidates, len(m.recentCandidates) > 0
}

// handleSetupPickKey processes keystrokes when the CWD field is in pick-list
// mode — either discover="git" repo candidates or recent locations. Rows are
// the candidates plus one trailing "Browse…" escape hatch. cwdBrowseCursor is
// the row cursor and cwdBrowseDir mirrors the highlighted candidate so
// submitSetupDialog's selectedCWD = setupSpawnDir() capture sees it (via
// cwdBrowseDir, unless a worktree is also chosen — see setupSpawnDir).
func (m Model) handleSetupPickKey(p *plugin.PanePlugin, key string) (tea.Model, tea.Cmd) {
	pick, _ := m.activeCWDPick()
	rows := len(pick) + 1 // +1 for Browse…

	// syncSelection keeps cwdBrowseDir aligned with the highlighted candidate
	// row. Not called when the cursor is on the "Browse…" row. Routed through
	// onSetupCWDChanged, not a bare assignment: previewing a different
	// candidate is exactly "the browsed directory moved", and a worktree
	// chosen for the PREVIOUS highlight must not survive onto this one.
	syncSelection := func() {
		if m.cwdBrowseCursor < len(pick) {
			m.onSetupCWDChanged(pick[m.cwdBrowseCursor])
		}
	}

	switch key {
	case "up", "k":
		if m.cwdBrowseCursor > 0 {
			m.cwdBrowseCursor--
			syncSelection()
		}
		return m, nil

	case "down", "j":
		if m.cwdBrowseCursor < rows-1 {
			m.cwdBrowseCursor++
			syncSelection()
		}
		return m, nil

	case "enter":
		if m.cwdBrowseCursor == len(pick) {
			// Browse… — drop pick mode, fall back to the directory browser
			// with its normal pre-fill chain.
			m.repoCandidates = nil
			m.recentCandidates = nil
			m.onSetupCWDChanged("")
			m.cwdBrowseCursor = 0
			return m, m.initSetupBrowser()
		}
		// Selecting a candidate submits the dialog (the folder IS the answer
		// to the CWD question; toggles keep their defaults unless the user
		// tabbed to them first).
		m.onSetupCWDChanged(pick[m.cwdBrowseCursor])
		return m.submitSetupDialog(p)
	}
	return m, nil
}

// handleSetupKubeKey processes keystrokes when the kube-context field is
// focused (discover="kube"). Row 0 is the "Default context" (no --context
// flag — k9s uses the kubeconfig current-context); rows 1..N are the
// discovered contexts. kubeCursor is the row index; submitSetupDialog reads it
// to inject --context.
func (m Model) handleSetupKubeKey(p *plugin.PanePlugin, key string) (tea.Model, tea.Cmd) {
	rows := len(m.kubeContexts) + 1 // +1 for the Default context row
	switch key {
	case "up", "k":
		if m.kubeCursor > 0 {
			m.kubeCursor--
		}
		return m, nil

	case "down", "j":
		if m.kubeCursor < rows-1 {
			m.kubeCursor++
		}
		return m, nil

	case "enter":
		return m.submitSetupDialog(p)
	}
	return m, nil
}

// handleSetupWorktreeKey moves the cursor within the worktree list and commits
// a choice.
//
// A disabled row still ACCEPTS the cursor and refuses Enter, rather than being
// skipped: the row exists to explain why that worktree is unavailable, and a
// row the cursor cannot reach explains nothing. Same choice the resume picker
// makes for an in-use session.
//
// Enter here only COMMITS the choice — unlike the kube/session/pick fields it
// does not submit the dialog, since a chosen worktree is still one of several
// fields the user may want to adjust (toggles, session) before Continue.
func (m Model) handleSetupWorktreeKey(p *plugin.PanePlugin, key string) (tea.Model, tea.Cmd) {
	rows := m.worktreeRows()
	// Empty means the field is in one of its four one-line states — the same
	// gate the renderer applies. Inert rather than silently accumulating
	// keystrokes into a name nothing is displaying.
	if len(rows) == 0 {
		return m, nil
	}
	// Naming a new branch swallows the list keys: j/k are letters a branch name
	// may legitimately contain, so cursor movement and typing cannot share the
	// same handler state.
	if m.worktreeNaming {
		return m.handleWorktreeNameKey(key)
	}
	switch key {
	case "up", "k":
		if m.worktreeCursor > 0 {
			m.worktreeCursor--
		}
	case "down", "j":
		if m.worktreeCursor < len(rows)-1 {
			m.worktreeCursor++
		}
	case "enter":
		row := rows[m.worktreeCursor]
		if row.disabled {
			return m, nil
		}
		if row.path == worktreeNewRowPath {
			// Enter opens the name field rather than committing: the branch
			// IS the identity of the work, so it has to be typed before this
			// row means anything.
			m.worktreeNaming = true
			m.selectedWorktree = ""
			m.selectedSessionID = ""
			return m, nil
		}
		m.worktreeNaming = false
		m.worktreeNewBranch = ""
		m.selectedWorktree = row.path
		// The session listing is scoped to the directory the pane will spawn
		// in, which this just changed. Clearing here is the responsive half;
		// submitSetupDialog's guard is the load-bearing one, since the user
		// can reach Continue without re-focusing the session field.
		m.selectedSessionID = ""
		return m, nil
	default:
		return m, nil
	}
	// Reuses historyWindow rather than a second, near-identical
	// implementation: same shape — re-derive the scroll origin from the
	// cursor, don't trust a stored one, because render must not depend on
	// Update having run (a WindowSizeMsg can shrink worktreeVisibleRows()
	// between the two).
	m.worktreeScroll, _ = historyWindow(len(rows), m.worktreeCursor, m.worktreeScroll, m.worktreeVisibleRows())
	return m, nil
}

// handleWorktreeNameKey edits the new branch's name.
//
// Validated on Enter with gitworktree.ValidateBranch — the same function the
// daemon runs. The daemon is the authority (any IPC client can send anything),
// but a round trip to learn about a typo is a bad dialog, and here the message
// lands beside the field the user typed into.
//
// Esc abandons the new-branch row entirely rather than merely closing the
// input: a half-typed name left behind would be committed by the next Enter on
// Continue, spawning a pane on a branch the user backed out of.
func (m Model) handleWorktreeNameKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		m.worktreeNaming = false
		m.worktreeNewBranch = ""
		m.worktreeErr = ""
		return m, nil
	case "enter":
		if err := gitworktree.ValidateBranch(m.worktreeNewBranch); err != nil {
			m.worktreeErr = err.Error()
			return m, nil
		}
		m.worktreeErr = ""
		m.worktreeNaming = false
		return m, nil
	case "backspace":
		if n := len(m.worktreeNewBranch); n > 0 {
			// Rune-safe: a branch name may carry non-ASCII, and lopping a byte
			// off a multi-byte rune leaves invalid UTF-8 for lipgloss to
			// measure.
			_, size := utf8.DecodeLastRuneInString(m.worktreeNewBranch)
			m.worktreeNewBranch = m.worktreeNewBranch[:n-size]
		}
		m.worktreeErr = ""
		return m, nil
	default:
		if len(key) == 1 || utf8.RuneCountInString(key) == 1 {
			m.worktreeNewBranch += key
			m.worktreeErr = ""
		}
		return m, nil
	}
}

// submitSetupDialog commits the browser-selected directory and toggle states,
// then advances the create-pane flow to the split-direction step.
func (m Model) submitSetupDialog(p *plugin.PanePlugin) (tea.Model, tea.Cmd) {
	if p.Command.PromptsCWD {
		m.selectedCWD = m.setupSpawnDir()
		// Tab and Shift+Tab are handled by handleCreatePaneSetupKey BEFORE the
		// field dispatch, so they never reach handleWorktreeNameKey — the name
		// field cannot swallow them. Tabbing away therefore arrives here with
		// worktreeNaming still set and the branch intact, and the field goes on
		// RENDERING it: any non-empty name draws the "new branch <name>"
		// summary once the field is blurred.
		//
		// This used to clear the name on that basis ("still being typed is not
		// a choice"), which contradicted what was on screen — the create fell
		// through to an ordinary one and the pane spawned in the REPOSITORY
		// ROOT, with no worktree, no git invocation and no error. That is the
		// silent relocation the whole feature exists to prevent, reached by
		// nothing more exotic than Tab.
		//
		// Closing the mode is still right; keeping the name is what makes the
		// dialog do what it shows. A genuinely incomplete name is caught by the
		// validation below, which refuses the submit and says why.
		m.worktreeNaming = false
		// Validated again at submit, not only on the name field's Enter: the
		// user can change the browsed directory afterwards, and this is the
		// last point before the pane is created.
		if m.worktreeNewBranch != "" {
			if err := gitworktree.ValidateBranch(m.worktreeNewBranch); err != nil {
				m.worktreeErr = err.Error()
				return m, nil
			}
		}
		logger.Debug("setup dialog: captured cwd=%q from browser (plugin=%s)", m.selectedCWD, p.Name)
	}
	m.cwdInputError = ""

	// A resume target is only valid for the directory it was listed under. The
	// user can pick a session, Shift+Tab back to the browser, move to another
	// project — or change the worktree choice — and press Continue without
	// ever re-focusing the session field, so the authoritative check belongs
	// here, at the moment the choice is committed, not only on the field's own
	// focus path. Compared against setupSpawnDir(), not cwdBrowseDir: a
	// session listed for a worktree the user has since deselected (or
	// changed) is exactly as stale as one listed for a directory browsed away
	// from.
	if m.selectedSessionID != "" && m.sessionScanCWD != m.setupSpawnDir() {
		logger.Debug("setup dialog: dropping resume session (listed for %q, submitting %q)", m.sessionScanCWD, m.setupSpawnDir())
		m.selectedSessionID = ""
	}

	// Inject the chosen kube context (row 0 = Default = no --context flag).
	if p.Command.Discover == "kube" && m.kubeCursor > 0 && m.kubeCursor-1 < len(m.kubeContexts) {
		ctx := m.kubeContexts[m.kubeCursor-1].Name
		merged := make([]string, 0, len(m.selectedInstanceArgs)+2)
		merged = append(merged, m.selectedInstanceArgs...)
		merged = append(merged, "--context", ctx)
		m.selectedInstanceArgs = merged
	}

	// Append enabled-toggle args to whatever instance args came in.
	var extra []string
	for i, t := range p.Command.Toggles {
		if i < len(m.toggleStates) && m.toggleStates[i] {
			extra = append(extra, t.ArgsWhenOn...)
		}
	}
	if len(extra) > 0 {
		merged := make([]string, 0, len(m.selectedInstanceArgs)+len(extra))
		merged = append(merged, m.selectedInstanceArgs...)
		merged = append(merged, extra...)
		m.selectedInstanceArgs = merged
	}

	m.dialogEdit = false
	cmd := m.advanceFromPluginChoice()
	return m, tea.Batch(tea.ClearScreen, cmd)
}

// renderSetupSessionField draws the resume-session picker.
//
// Collapsed to a single summary line while unfocused, so a dialog that already
// carries a 12-row directory browser does not grow another 12 rows for a field
// most panes never touch; it expands into the scrolling list on focus.
func (m Model) renderSetupSessionField(focused bool) string {
	var b strings.Builder

	label := "Session:"
	if focused {
		b.WriteString(dialogSelected.Render("> "+label) + "\n")
	} else {
		// Collapsed: label and current value share one line.
		b.WriteString(dialogNormal.Render("  "+label) + "  " +
			dialogValStyle.Render(m.sessionSummaryLine()) +
			dialogSubtle.Render("   (Tab here to resume)") + "\n\n")
		return b.String()
	}

	switch m.sessionState {
	case sessionScanning:
		b.WriteString(dialogSubtle.Render("    Scanning session history…") + "\n\n")
		return b.String()
	case sessionScanTimedOut:
		b.WriteString(dialogSubtle.Render("    Timed out — is the daemon running?") + "\n\n")
		return b.String()
	case sessionScanFailed:
		b.WriteString(dialogErrorStyle.Render("    ✗ "+m.sessionError) + "\n\n")
		return b.String()
	}

	// The detail panel replaces the list rather than drawing over it: the field
	// keeps one height budget, and there is no compositing to get wrong.
	if m.sessionDetail.open {
		b.WriteString(m.renderSessionDetail())
		return b.String()
	}

	// Row budget matches the CWD pick list: box text area minus the "  > "
	// row prefix.
	maxWidth := m.setupTextWidth() - setupRowIndent
	rows := m.sessionRowCount()
	visible := m.sessionVisibleRows()
	start := m.sessionScroll
	end := start + visible
	if end > rows {
		end = rows
	}

	for i := start; i < end; i++ {
		text := "New session"
		blocked := false
		if s := m.sessionRowAt(i); s != nil {
			text = sessionRowLabel(*s)
			if s.InUsePaneID != "" {
				blocked = true
				marker := "  [open in another pane]"
				if label, ok := m.paneNavLabel(s.InUsePaneID); ok {
					marker = "  [open in " + label + "]"
				}
				// Truncate the title, not the marker. Appending first and
				// truncating the whole row drops the marker off any row with a
				// long title, leaving it indistinguishable from a selectable
				// one until Enter silently refuses it.
				text = truncateToWidth(text, maxWidth-lipgloss.Width(marker)) + marker
			}
		}
		text = truncateToWidth(text, maxWidth)
		switch {
		case i == m.sessionCursor && blocked:
			// Cursor may rest here so the footer can explain the block; the
			// row itself stays subdued to signal it is not actionable.
			b.WriteString("  > " + dialogSubtle.Render(text) + "\n")
		case i == m.sessionCursor:
			b.WriteString("  > " + dialogSelected.Render(text) + "\n")
		case blocked:
			b.WriteString("    " + dialogSubtle.Render(text) + "\n")
		default:
			b.WriteString("    " + dialogNormal.Render(text) + "\n")
		}
	}

	// Footer: explain a blocked row when the cursor is on it, otherwise show
	// position and keys.
	switch {
	case !m.sessionRowSelectable(m.sessionCursor):
		b.WriteString(m.renderSetupHint("    Already open in another pane — close it first, or pick another") + "\n")
	case len(m.sessionRows) == 0:
		b.WriteString(dialogSubtle.Render("    (no earlier sessions for this folder)") + "\n")
	case rows > visible:
		hint := fmt.Sprintf("    %d/%d  ↑↓ PgUp/PgDn move  Enter select  i details", m.sessionCursor+1, rows)
		if m.sessionTruncated {
			hint += "  (older sessions not listed)"
		}
		b.WriteString(m.renderSetupHint(hint) + "\n")
	default:
		b.WriteString(m.renderSetupHint("    ↑↓ move  Enter select  i details") + "\n")
	}
	b.WriteString("\n")
	return b.String()
}

// renderSetupWorktreeField draws the worktree picker.
//
// Collapsed to one summary line while unfocused, for the reason the session
// field is: a dialog already carrying a directory browser must not grow a
// second full-height list for a field most panes never touch.
//
// Four states render DIFFERENTLY on purpose. A scan still in flight showing
// "no worktrees" is a confidently wrong answer, and "this is not a
// repository" is a different fact from "the scan failed" — only one of them
// justifies telling the user there is nothing here.
func (m Model) renderSetupWorktreeField(focused bool) string {
	var b strings.Builder
	label := "  Worktree"
	if focused {
		label = "> Worktree"
	}

	switch {
	case m.worktrees.pending:
		b.WriteString(dialogNormal.Render(label + "    scanning…"))
		return b.String()
	case !m.worktrees.loaded:
		b.WriteString(dialogSubtle.Render(label + "    —"))
		return b.String()
	case m.worktrees.err != "":
		b.WriteString(dialogNormal.Render(label + "    "))
		b.WriteString(dialogSubtle.Render(truncateToWidth(
			sanitizeRemoteText(m.worktrees.err), m.setupTextWidth()-lipgloss.Width(label)-4)))
		return b.String()
	case !m.worktrees.repo:
		b.WriteString(dialogSubtle.Render(label + "    not a git repository"))
		return b.String()
	}

	if !focused {
		summary := "off"
		switch {
		case m.worktreeNewBranch != "":
			summary = "new branch " + sanitizeRemoteText(m.worktreeNewBranch)
		case m.selectedWorktree != "":
			summary = sanitizeRemoteText(worktreeLabel(m.worktrees.list, m.selectedWorktree))
		}
		b.WriteString(dialogNormal.Render(label + "    " + truncateToWidth(summary, m.setupTextWidth()-lipgloss.Width(label)-4)))
		return b.String()
	}

	b.WriteString(dialogNormal.Render(label))
	b.WriteString("\n")
	// Naming REPLACES the list rather than sitting under it, so focusing the
	// field cannot grow the dialog — the same constraint worktreeVisibleRows
	// exists for, and the pattern the session detail panel already follows.
	if m.worktreeNaming {
		// Budgeted against the LIST's own row count, not written out in full.
		// worktreeVisibleRows floors at 1, so on a short terminal the list is
		// two lines (label + one row) and an unconditional three-line block
		// here would be taller than the field it replaced — pushing
		// [Continue] off screen, which lipgloss.Place does not clip.
		budget := m.worktreeVisibleRows()
		b.WriteString(dialogNormal.Render("    new branch: "))
		b.WriteString(dialogEditStyle.Render(sanitizeRemoteText(m.worktreeNewBranch) + "│"))
		// The error outranks the hint: it says why Enter did nothing, where
		// the hint only repeats keys the footer already carries.
		if m.worktreeErr != "" && budget >= 2 {
			b.WriteString("\n")
			b.WriteString(dialogErrorStyle.Render("    " + truncateToWidth(m.worktreeErr, m.setupTextWidth()-setupRowIndent)))
			budget--
		}
		if budget >= 2 {
			b.WriteString("\n")
			b.WriteString(dialogSubtle.Render("    Enter accept   Esc cancel"))
		}
		return b.String()
	}
	rows := m.worktreeRows()
	visible := m.worktreeVisibleRows()
	// Re-derives the window from the cursor rather than trusting the stored
	// scroll, like renderCommandHistory: render must not depend on Update
	// having run first, since a WindowSizeMsg can shrink worktreeVisibleRows()
	// between the key handler that set worktreeScroll and this render.
	start, end := historyWindow(len(rows), m.worktreeCursor, m.worktreeScroll, visible)
	for i := start; i < end; i++ {
		mark := "    "
		switch {
		case i == m.worktreeCursor:
			mark = "  > "
		case m.worktreeNewBranch != "":
			// With a new branch pending, the committed choice is the
			// "+ new branch…" row — NOT the off row. Matching on
			// selectedWorktree alone marked "off" (both are ""), so the
			// expanded list contradicted the collapsed summary.
			if rows[i].path == worktreeNewRowPath {
				mark = setupRowIdleMark
			}
		case rows[i].path == m.selectedWorktree:
			mark = setupRowIdleMark
		}
		text := sanitizeRemoteText(rows[i].label)
		if rows[i].disabled {
			b.WriteString(dialogSubtle.Render(mark + truncateToWidth(text, m.setupTextWidth()-setupRowIndent)))
		} else {
			b.WriteString(dialogNormal.Render(mark + truncateToWidth(text, m.setupTextWidth()-setupRowIndent)))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// worktreeRow is one selectable line: row 0 is always "off".
type worktreeRow struct {
	label    string
	path     string // "" for the off row
	disabled bool
}

// worktreeRows builds the field's rows. The MAIN checkout is excluded: it is
// not a worktree to attach to, it is the directory the CWD field already
// chose. Locked and prunable entries are shown but refused, so the user learns
// why a row is unavailable rather than wondering where it went.
// worktreeNewRowPath is the sentinel path of the "+ new branch…" row. A
// sentinel rather than a bool on worktreeRow so the row list stays one flat
// slice the cursor walks — the same reason the sidebar builds paint order and
// hit-testing from a single slice.
//
// It cannot collide with a real worktree path: git reports absolute paths, and
// this is not one.
const worktreeNewRowPath = "\x00new"

// worktreeFieldInteractive reports whether the field is showing a LIST the
// cursor can walk, as opposed to one of the four one-line states
// (scanning / never asked / errored / not a repository).
//
// The renderer early-returns on those; the key handler must agree, or Down
// then Enter while the field reads "scanning…" arms naming mode with no UI at
// all and every later keystroke is silently appended to a branch name the user
// cannot see.
func (m Model) worktreeFieldInteractive() bool {
	return !m.worktrees.pending && m.worktrees.loaded && m.worktrees.err == "" && m.worktrees.repo
}

func (m Model) worktreeRows() []worktreeRow {
	if !m.worktreeFieldInteractive() {
		return nil
	}
	rows := []worktreeRow{{label: "off — use the directory above", path: ""}}
	// FIRST of the actionable rows, directly under the neutral default.
	//
	// It shipped last, to spare stage A's row fixtures an index shift — a
	// reason about the tests rather than about the dialog, and the wrong trade:
	// a repository with a dozen worktrees buries the row you reach for most,
	// and "off" is the only row that earns its place above it by being the
	// default rather than a choice. The fixtures moved instead.
	rows = append(rows, worktreeRow{label: "+ new branch…", path: worktreeNewRowPath})
	for _, w := range m.worktrees.list {
		if w.Main {
			continue
		}
		name := w.Branch
		if name == "" {
			name = "(detached)"
		}
		row := worktreeRow{label: name + "  " + w.Path, path: w.Path}
		switch {
		case w.Prunable:
			row.label, row.disabled = name+"  (directory is gone)", true
		case w.Locked:
			row.label, row.disabled = name+"  (locked)", true
		}
		rows = append(rows, row)
	}
	return rows
}

// worktreeLabel resolves a stored path back to its display name for the
// collapsed summary.
func worktreeLabel(list []ipc.WorktreeInfo, path string) string {
	for _, w := range list {
		if w.Path == path {
			if w.Branch != "" {
				return w.Branch
			}
			return w.Path
		}
	}
	return path
}

// setupSpawnDir is the directory the pane will actually start in: the chosen
// worktree when there is one, else the browsed directory. Every consumer of
// "where will this pane spawn" reads THIS, not cwdBrowseDir directly — the
// submitted CWD (submitSetupDialog) and the session-listing scope
// (ensureSessionScan) are the two that matter.
func (m Model) setupSpawnDir() string {
	if m.selectedWorktree != "" {
		return m.selectedWorktree
	}
	return m.cwdBrowseDir
}

// onSetupCWDChanged reacts to the browsed directory moving: the worktree
// choice and its listing both belong to the repository just left. Called from
// every site that commits a NEW browsed directory while the setup dialog is
// open — the pick-list field's cursor moves and selections, and
// applyBrowseResponse's browseFilled case, which is where the directory
// browser's own Enter/Backspace navigation actually lands (asynchronously,
// after the daemon round trip — handleSetupCWDKey itself only issues the
// request).
//
// Takes no plugin argument: an earlier version did, unused, and half its call
// sites had to pass nil for it anyway — a response-handler call site (like
// applyExistingDirs) is exercised by tests that build a Model with no
// pluginRegistry at all, so resolving a plugin just to hand it to a function
// that never reads it was a nil-pointer panic waiting for the day someone
// reached for it inside here.
func (m *Model) onSetupCWDChanged(dir string) tea.Cmd {
	m.cwdBrowseDir = dir
	m.selectedWorktree = ""
	// The new-branch state belongs to the repository that was on screen when
	// it was typed. Leaving it behind is the same class of bug the rest of
	// this reset exists for — the pane would branch from a repository the
	// dialog is no longer showing.
	m.worktreeNewBranch = ""
	m.worktreeNaming = false
	m.worktreeErr = ""
	m.worktreeCursor = 0
	m.worktreeScroll = 0
	m.worktrees = worktreeState{}
	return nil
}

// setupChromeRows is the fixed chrome the setup dialog spends on everything
// that is NOT one of the two expanding lists — title, CWD browser + hint,
// toggles, the Continue button, borders and padding — measured against the
// shipped claude-code layout. Both worktreeVisibleRows and sessionVisibleRows
// derive from this ONE constant so the two lists' budgets cannot drift apart.
const setupChromeRows = 26

// worktreeVisibleRows caps the worktree list. The floor is 1, never a
// friendlier number: lipgloss.Place does not clip, so any floor above the
// height actually available manufactures the overflow it looks like it
// prevents — the same reasoning as historyMinRows.
func (m Model) worktreeVisibleRows() int {
	// The worktree and session lists never render expanded at the same
	// time: only one setup-dialog field is focused at once (cursor ==
	// fieldIdx, a single int), and a field's list collapses to a one-line
	// summary while it is not the focused one. So the constraint the
	// terminal actually imposes is the MAX of the two lists' heights, not
	// their sum.
	//
	// The reservation below is SUM-based anyway: this list claims one row
	// more than the session field's own chrome (the collapsed worktree row
	// is always drawn, whether or not its list is), PLUS the session list's
	// floor, up front — regardless of whether the session field is even
	// focused right now. That is more conservative than the true
	// mutually-exclusive bound requires, but it is kept because it is
	// simple, safe for every terminal height, and pinned by tests —
	// master's looser bound overflowed by 3 rows across h=30..39.
	//
	// The reservation makes the combined SUM exact, not merely sufficient:
	// while this list is still ramping (this function's own "default:
	// return avail" branch — below its cap, above its floor),
	// worktreeVisibleRows(h) == h - surroundingRows, so sessionVisibleRows'
	// own avail (m.height - setupChromeRows - worktreeVisibleRows()) has
	// m.height cancel out of the subtraction entirely, leaving the CONSTANT
	// surroundingRows - setupChromeRows = 1 + sessionListMinRows. That
	// constant sits one row ABOVE the session floor — session is not
	// clamped there, it returns that value via its own default branch — so
	// worktree(h) + session(h) reduces to (h - surroundingRows) + (1 +
	// sessionListMinRows) = h - setupChromeRows: the arithmetic reserves
	// exactly the combined SUM for every height in the ramp — a safe
	// over-reservation, since the two lists are never both on screen.
	const surroundingRows = setupChromeRows + 1 + sessionListMinRows
	avail := m.height - surroundingRows
	switch {
	case avail >= worktreeListVisibleRows:
		return worktreeListVisibleRows
	case avail < worktreeListMinRows:
		return worktreeListMinRows
	default:
		return avail
	}
}

const (
	worktreeListVisibleRows = 6
	worktreeListMinRows     = 1
)

// renderSessionDetail draws the info panel that replaces the list while it is
// open. Sized against the same row budget the list uses, so opening it does not
// change the dialog's height.
func (m Model) renderSessionDetail() string {
	var b strings.Builder
	labelW := m.setupTextWidth() - setupRowIndent
	// The panel occupies the list's rows plus the footer line it also replaces.
	budget := m.sessionVisibleRows() + 1

	line := func(s string) {
		b.WriteString("    " + dialogNormal.Render(truncateToWidth(s, labelW)) + "\n")
		budget--
	}
	subtle := func(s string) {
		b.WriteString("    " + dialogSubtle.Render(truncateToWidth(s, labelW)) + "\n")
		budget--
	}
	footer := func(s string) {
		b.WriteString(m.renderSetupHint("    "+s) + "\n\n")
	}

	switch {
	case m.sessionDetail.id == "":
		subtle("New session — starts a fresh conversation in this folder.")
		footer("i or Esc back to the list")
		return b.String()
	case m.sessionDetail.state == sessionScanning:
		subtle("Reading transcript…")
		footer("i or Esc back to the list")
		return b.String()
	case m.sessionDetail.state == sessionScanTimedOut:
		subtle("Timed out — is the daemon running?")
		footer("i or Esc back  ·  ↑↓ another session")
		return b.String()
	case m.sessionDetail.state == sessionScanFailed:
		b.WriteString("    " + dialogErrorStyle.Render(truncateToWidth("✗ "+m.sessionDetail.data.Error, labelW)) + "\n")
		budget--
		footer("i or Esc back  ·  ↑↓ another session")
		return b.String()
	}

	d := m.sessionDetail.data
	line(d.SessionID)
	subtle(fmt.Sprintf("%-10s %s  (%s)", "Started", absoluteTime(d.StartedMs), relativeAge(d.StartedMs)))
	subtle(fmt.Sprintf("%-10s %s  (%s)", "Last used", absoluteTime(d.ModifiedMs), relativeAge(d.ModifiedMs)))
	// Size shares this row rather than taking one of its own: every row spent
	// on metadata is a row of prompt text the panel cannot show.
	subtle(fmt.Sprintf("%-10s %d typed · %s", "Prompts", d.UserPrompts, formatBytes(d.SizeBytes)))
	if s := m.sessionRowAt(m.sessionCursor); s != nil && s.InUsePaneID != "" {
		where := "another pane"
		if label, ok := m.paneNavLabel(s.InUsePaneID); ok {
			where = label
		}
		subtle(fmt.Sprintf("%-10s %s", "Open in", where))
	}

	// Every row left over goes to prompt text. The label lives in the left
	// gutter of the block's first row rather than on a header line of its own —
	// at this size a header plus a separating blank costs more rows than the
	// text it introduces.
	budget -= 2 // the footer, and the blank line above it
	b.WriteString("\n")
	if d.FirstPrompt == "" && d.LastPrompt == "" {
		subtle("(no typed prompt recorded)")
		footer("i or Esc back  ·  ↑↓ another session  ·  Enter select")
		return b.String()
	}

	// The first prompt is capped because it is already the row's title in the
	// list; the last prompt takes the remainder because "where did I leave off"
	// is what the panel is for and it appears nowhere else.
	const firstPromptCap = 3
	firstRows, lastRows := 0, 0
	switch {
	case d.FirstPrompt == "":
		lastRows = budget
	case d.LastPrompt == "":
		firstRows = budget
	default:
		firstRows = min(firstPromptCap, budget/2)
		lastRows = budget - firstRows
	}

	gutter := "  %-7s"
	promptW := m.setupTextWidth() - setupRowIndent - 7
	block := func(label, text string, rows int) {
		for i, ln := range wrapToLines(text, promptW, rows) {
			g := ""
			if i == 0 {
				g = label
			}
			b.WriteString("  " + dialogSubtle.Render(fmt.Sprintf(gutter, g)) +
				dialogValStyle.Render(ln) + "\n")
			budget--
		}
	}
	block("First", d.FirstPrompt, firstRows)
	block("Last", d.LastPrompt, lastRows)

	footer("i or Esc back  ·  ↑↓ another session  ·  Enter select")
	return b.String()
}

// wrapToLines word-wraps text to width and returns at most max lines, marking
// the last one with an ellipsis when text was cut. Trailing padding is stripped:
// lipgloss pads every wrapped line out to the full width, which would push each
// row to exactly the wrap limit and leave the panel one stray cell from the
// reflow that setupTextWidth exists to prevent.
func wrapToLines(text string, width, max int) []string {
	if text == "" || width <= 0 || max <= 0 {
		return nil
	}
	wrapped := lipgloss.NewStyle().Width(width).Render(text)
	lines := strings.Split(wrapped, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	if len(lines) > max {
		lines = lines[:max]
		lines[max-1] = truncateToWidth(lines[max-1]+" …", width)
	}
	return lines
}

// sanitizePastedPath strips common clipboard noise (whitespace, surrounding
// quotes, and any control bytes) so paths copied from GUI file managers are
// accepted cleanly. Control bytes are dropped to prevent terminal-escape
// injection: a clipboard payload containing OSC/CSI sequences would otherwise
// be echoed back inside a daemon error message and reach the rendered dialog.
//
// This is the whole of the client-side cleaning for a pasted path. It
// deliberately does NOT expand ~, make the path absolute, or check that it
// exists: all three are answers only the machine holding the filesystem can
// give, and giving them here is what made a valid remote path unpasteable.
func sanitizePastedPath(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `"`)
	// Some Linux file managers wrap paths in single quotes too.
	s = strings.Trim(s, `'`)

	// Drop any non-printable control bytes. We keep tab (0x09) since some
	// shells legitimately produce it inside paths via completion, even though
	// it is uncommon.
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '\t' {
			b.WriteRune(r)
			continue
		}
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// setupBoxChrome is what the dialog box costs every content line: the two
// border columns plus Padding(1,2)'s 2 cells per side. lipgloss draws the
// border INSIDE Style.Width, so a content line wider than width-6 wraps —
// width-4 leaves the line sitting two cells past the limit, which reflow then
// word-wraps, dropping the last word onto its own line at column 0. Aliases the
// shared dialogBoxChrome; kept as its own name because
// TestSetupBoxChrome_MatchesLipglossWrapLimit pins it two-sidedly against
// lipgloss's actual behaviour for the setup dialog.
const setupBoxChrome = dialogBoxChrome

// setupRowIndent is the cell cost of a list row's cursor prefix ("  > " or
// four spaces) in the CWD pick list and the session picker.
const setupRowIndent = 4

// setupRowIdleMark prefixes the row holding a field's committed value when the
// cursor caret is not already on it — the field is blurred, or it is focused
// with the cursor parked on a row that will never be submitted ("Browse…").
// Reads as a dimmer caret next to the "  > " cursor row, which is the intended
// relationship: one is where you are, the other is what you picked.
//
// Exactly setupRowIndent cells wide, so it costs a row no path budget
// (TestSetupRowIdleMark_MatchesRowIndent). Deliberately not "  • ": the kube
// list prefixes its current context with "● ", and "  • ● prod" puts two
// near-identical glyphs side by side with the newly-added one leading.
const setupRowIdleMark = "  ▸ "

// setupDialogWidth returns the box width for the pane setup dialog. Each toggle
// row costs 6 cells of chrome — the 2-char cursor prefix plus "[x] " — on top
// of its label, so a long label wraps onto a second line once the row exceeds
// the text area. Grow the box to fit the widest toggle row instead of
// hardcoding a constant that the next long label silently breaks. Floored at 70
// (keeps CWD paths comfortable) and capped to the terminal width so the box
// never renders off-screen.
func (m Model) setupDialogWidth() int {
	const (
		floor        = 70
		toggleChrome = 6 // 2 prefix ("> "/"  ") + 4 box+space ([x]/[ ]/(•)/( ) are all 4)
	)
	width := floor
	if p := m.pluginRegistry.Get(m.selectedPlugin); p != nil {
		for _, t := range p.Command.Toggles {
			if need := toggleChrome + lipgloss.Width(t.Label) + setupBoxChrome; need > width {
				width = need
			}
		}
	}
	// Skip the clamp until the first WindowSizeMsg sets m.width (>2 keeps
	// m.width-2 ≥ 1). The border counts inside Width, so the box occupies
	// exactly `width` columns; -2 leaves a one-cell margin on each side.
	if m.width > 2 && width > m.width-2 {
		width = m.width - 2
	}
	return width
}

// setupTextWidth is the widest a content line may be before the box wraps it.
// Every budget in this dialog derives from here — deriving them separately is
// what let the session picker's rows sit exactly two cells over the limit.
func (m Model) setupTextWidth() int {
	// setupDialogWidth has already applied the terminal clamp; dialogInnerWidth
	// is idempotent over it and is where the chrome subtraction lives for every
	// dialog, so the two cannot drift apart.
	return dialogInnerWidth(m.width, m.setupDialogWidth())
}

// renderSetupHint renders a subtle footer hint clamped to the box's text area
// so a long hint truncates with an ellipsis instead of wrapping onto a second
// line.
func (m Model) renderSetupHint(text string) string {
	return dialogSubtle.Render(truncateToWidth(text, m.setupTextWidth()))
}

// renderCreatePaneSetupDialog renders the setup dialog: a CWD directory
// browser (optional) + one checkbox per plugin Toggle + a Continue button.
// The focused field is highlighted; inside the browser the selected entry
// is highlighted.
func (m Model) renderCreatePaneSetupDialog() string {
	p := m.pluginRegistry.Get(m.selectedPlugin)
	if p == nil {
		return ""
	}

	var b strings.Builder
	b.WriteString(dialogTitle.Render(p.DisplayName + " — Setup"))
	b.WriteString("\n\n")

	cursor := m.setupFieldCursor
	fieldIdx := 0

	if p.Command.PromptsCWD {
		focused := cursor == fieldIdx
		label := "Working directory:"
		if focused {
			label = dialogSelected.Render("> " + label)
		} else {
			label = dialogNormal.Render("  " + label)
		}
		b.WriteString(label + "\n")

		pick, pickIsRecent := m.activeCWDPick()

		// In directory-browser mode, show the current path on its own line for
		// context above the listing. In pick-list mode the highlighted row IS
		// the selected path, so a separate line would duplicate it and merge
		// visually with the list — skip it and let the highlight do the work.
		if len(pick) == 0 {
			// Resolved is the daemon's answer (BrowseDirRespPayload.Resolved) and
			// may be remote — sanitize before it reaches a rendered row. The raw
			// value stays in m.cwdBrowseDir; this is what the CWD field itself
			// browsed to, not necessarily the actual spawn CWD (setupSpawnDir()
			// prefers a chosen worktree, rendered separately by the worktree
			// field below).
			path, prefix := sanitizeRemoteText(m.cwdBrowseDir), "    "
			switch {
			// No runtime.GOOS check: the roots come from the daemon, and only a
			// Windows daemon reports any, so their presence IS the condition.
			// Testing this machine's OS answered for the wrong one.
			case path == "" && len(m.cwdBrowseEntries) > 0:
				path = dialogSubtle.Render("Select drive:")
			case path == "":
				path = dialogSubtle.Render("(no directory loaded — daemon default will be used)")
			case focused:
				// While focused this line is just "where the listing is" — the
				// cursor row below owns the emphasis.
				path = dialogValStyle.Render(path)
			default:
				// Blurred: this line is now the only evidence of the chosen
				// directory, so it takes the same mark the pick list uses.
				// Bold alone would not carry it — dialogValStyle and
				// dialogNormal are the same declaration, and dialogSelectedIdle
				// differs from both by SGR 1 only, so on a terminal that drops
				// bold the mark is the entire signal.
				prefix = setupRowIdleMark
				path = dialogSelectedIdle.Render(path)
			}
			b.WriteString(prefix + path + "\n")
		}

		if m.cwdInputError != "" {
			b.WriteString("    " + dialogErrorStyle.Render("✗ "+m.cwdInputError) + "\n")
		}

		if len(pick) > 0 {
			// Pick-list mode: show discovered git repos or recent locations
			// plus a trailing "Browse…" escape hatch. Uses the same cursor-row
			// prefix/style as the directory browser. Path budget is the box
			// text area minus the 4-cell "  > " row prefix.
			setupPickMaxWidth := m.setupTextWidth() - setupRowIndent
			rows := len(pick) + 1 // +1 for Browse…
			for i := 0; i < rows; i++ {
				var displayName string
				if i < len(pick) {
					displayName = leftTruncPath(pick[i], setupPickMaxWidth)
				} else {
					displayName = "Browse…"
				}
				switch {
				case focused && i == m.cwdBrowseCursor:
					b.WriteString("  > " + dialogSelected.Render(displayName) + "\n")
				case i < len(pick) && pick[i] == m.cwdBrowseDir:
					// The directory this field has committed, marked whenever
					// the caret above is not already on it: the field is
					// blurred, OR it is focused with the cursor parked on the
					// trailing "Browse…" row, which syncSelection deliberately
					// never commits. Both states otherwise read as "nothing
					// chosen" — the same hole, one just happens to be reachable
					// without leaving the field. (What the pane will actually
					// spawn in is setupSpawnDir(), which prefers a chosen
					// worktree over this value — that choice is marked
					// separately, on the worktree field below.)
					//
					// Matched on cwdBrowseDir rather than on the cursor, which
					// is what makes the Browse… case work at all. In pick mode
					// cwdBrowseDir is only ever assigned by copying an element
					// out of this same slice, so == is exact by construction
					// and never depends on pathEqual's case folding.
					//
					// Same 4-cell prefix width as the other rows, so
					// leftTruncPath's budget is unchanged.
					b.WriteString(setupRowIdleMark + dialogSelectedIdle.Render(displayName) + "\n")
				default:
					b.WriteString("    " + dialogNormal.Render(displayName) + "\n")
				}
			}
			hint := "    ↑↓ move  Enter select  Browse… to type a path"
			if pickIsRecent {
				hint = "    ↑↓ move  Enter open  Browse… for another folder"
			}
			b.WriteString(m.renderSetupHint(hint) + "\n")
		} else {
			// Listing window — always allocate `browserVisibleRows` lines so the
			// dialog height stays stable across navigation.
			entries := m.cwdBrowseEntries
			visible := browserVisibleRows
			start := m.cwdBrowseScroll
			end := start + visible
			if end > len(entries) {
				end = len(entries)
			}

			for i := 0; i < visible; i++ {
				idx := start + i
				if idx >= len(entries) {
					b.WriteString("\n")
					continue
				}
				name := entries[idx]
				displayName := name
				if name != ".." && !strings.HasSuffix(name, `\`) {
					displayName = name + "/"
				}
				// name is BrowseEntry.Name off the wire and may be remote —
				// sanitized here, at render, on the DISPLAY copy only. The
				// ".." / trailing-"\" checks above run against the raw value on
				// purpose: they compare against the synthetic ".." marker this
				// package adds itself, not the daemon's bytes.
				// Truncated as well as sanitized. sanitizeRemoteText preserves
				// printable non-ASCII byte-for-byte and imposes no budget, and
				// renderDialog's lipgloss.Place does not clip — so one very long
				// remote filename soft-wraps into as many rows as it needs and
				// breaks the box out of its own height.
				displayName = truncateToWidth(sanitizeRemoteText(displayName), m.setupTextWidth()-setupRowIndent)
				if focused && idx == m.cwdBrowseCursor {
					b.WriteString("  > " + dialogSelected.Render(displayName) + "\n")
				} else {
					b.WriteString("    " + dialogNormal.Render(displayName) + "\n")
				}
			}

			// One line, whatever the state — the listing window above already
			// writes browserVisibleRows lines unconditionally, so the dialog's
			// height must not depend on which of these branches is taken.
			//
			// The error comes FIRST because it is the only branch reporting that
			// something went wrong, and it is reachable with a listing still on
			// screen: a failed descend deliberately leaves the previous listing
			// in place (applyBrowseDir), so ordering it behind the scroll hints
			// would render an ordinary hint and make the keypress look ignored.
			// Pending outranks the hints for the same reason — it is the only
			// feedback that the keypress registered at all.
			//
			// "(empty directory)" therefore stays reachable only when a listing
			// genuinely came back with nothing in it.
			switch {
			case m.browse.err != "":
				// browse.err is usually BrowseDirRespPayload.Error, which can
				// embed a remote path — sanitize on the way out, same as the
				// other two browse fields.
				b.WriteString(m.renderSetupHint("    ✗ "+sanitizeRemoteText(m.browse.err)) + "\n")
			case m.browse.pending:
				b.WriteString(dialogSubtle.Render("    (loading…)") + "\n")
			case len(entries) > 0:
				hint := "↑↓ move  Enter descend  ← up  Ctrl+V paste"
				if len(entries) > visible {
					// Scroll indicator — position inside the list.
					hint = fmt.Sprintf("%d/%d  %s", m.cwdBrowseCursor+1, len(entries), hint)
				}
				if m.cwdBrowseTruncated {
					// LEADING, so the width clamp can only ever eat the
					// navigation hints — which the user has already read —
					// rather than the one part of this line that is news. Same
					// reasoning as the session picker's [open in …] marker,
					// which is kept while the title truncates around it.
					hint = truncatedHintPrefix + hint
				}
				b.WriteString(m.renderSetupHint("    "+hint) + "\n")
			case m.cwdBrowseTruncated:
				// Capped, yet nothing to show: every entry that survived the cap
				// was a file. There is nothing to navigate, but the answer is
				// still partial and saying nothing would imply otherwise.
				b.WriteString(m.renderSetupHint("    "+truncatedHintPrefix) + "\n")
			default:
				b.WriteString(dialogSubtle.Render("    (empty directory)") + "\n")
			}
		}
		b.WriteString("\n")
		fieldIdx++
	}

	if p.Command.Discover == "kube" {
		focused := cursor == fieldIdx
		label := "Kube context:"
		if focused {
			label = dialogSelected.Render("> " + label)
		} else {
			label = dialogNormal.Render("  " + label)
		}
		b.WriteString(label + "\n")

		// Row 0 = Default context (current-context, no --context flag); rows
		// 1..N = discovered contexts, current-context marked with ●. The
		// namespace suffix is passed separately (not pre-rendered) so a
		// focused row's selection styling covers the whole line instead of
		// being truncated by a nested ANSI reset from dialogSubtle.
		renderRow := func(rowIdx int, text, suffix string) {
			switch {
			case focused && rowIdx == m.kubeCursor:
				b.WriteString("  > " + dialogSelected.Render(text+suffix) + "\n")
			case !focused && rowIdx == m.kubeCursor:
				// Blurred but still the context this pane will launch with —
				// same reasoning as the CWD pick list above. Unlike that list
				// there is no non-committal row (row 0, "Default context", is a
				// real choice), so kubeCursor IS the committed value and needs
				// no value indirection. The !focused is redundant with the case
				// order above and spelled out regardless: without it, reordering
				// these two cases would silently draw the idle mark on the
				// focused row, and nothing would fail.
				b.WriteString(setupRowIdleMark + dialogSelectedIdle.Render(text+suffix) + "\n")
			default:
				line := dialogNormal.Render(text)
				if suffix != "" {
					line += dialogSubtle.Render(suffix)
				}
				b.WriteString("    " + line + "\n")
			}
		}
		renderRow(0, "Default context", "")
		for i, c := range m.kubeContexts {
			// Budgeted as well as sanitized, for the same reason as the browser
			// entries above: sanitizeRemoteText imposes no length limit and
			// lipgloss.Place does not clip, so an over-long remote context name
			// would soft-wrap the box apart. applyKubeContexts already clamps
			// what enters state; this is the render-side half.
			budget := m.setupTextWidth() - setupRowIndent
			name := sanitizeRemoteText(c.Name)
			if c.Current {
				name = "● " + name
			}
			suffix := ""
			if ns := sanitizeRemoteText(c.Namespace); ns != "" {
				suffix = "  (" + ns + ")"
			}
			if lipgloss.Width(name+suffix) > budget {
				name = truncateToWidth(name, budget-lipgloss.Width(suffix))
			}
			renderRow(i+1, name, suffix)
		}
		switch {
		case len(m.kubeContexts) > 0:
			hint := "↑↓ navigate  Enter select"
			if m.kubeTruncated {
				// LEADING, same reasoning as the CWD browser's
				// truncatedHintPrefix use: the width clamp can only ever eat
				// the navigation hints, which the user has already read,
				// rather than the one part of this line that is news.
				hint = truncatedHintPrefix + hint
			}
			b.WriteString(dialogSubtle.Render("    "+hint) + "\n")
		case m.kubeScan.phase == kubeScanning:
			b.WriteString(dialogSubtle.Render("    Scanning for kube contexts…") + "\n")
		case m.kubeScan.phase == kubeScanFailed:
			b.WriteString(dialogSubtle.Render("    (kube context scan failed — k9s uses its current context)") + "\n")
		default:
			b.WriteString(dialogSubtle.Render("    (no kube contexts found — k9s uses its current context)") + "\n")
		}
		b.WriteString("\n")
		fieldIdx++
	}

	for i, t := range p.Command.Toggles {
		focused := cursor == fieldIdx
		// Grouped toggles render as radio buttons to signal mutual
		// exclusion; ungrouped ones stay as checkboxes.
		on := i < len(m.toggleStates) && m.toggleStates[i]
		var box string
		switch {
		case t.Group != "" && on:
			box = "(•)"
		case t.Group != "":
			box = "( )"
		case on:
			box = "[x]"
		default:
			box = "[ ]"
		}
		prefix := "  "
		lineStyle := dialogNormal
		if focused {
			prefix = "> "
			lineStyle = dialogSelected
		}
		b.WriteString(prefix + lineStyle.Render(box+" "+t.Label) + "\n")
		fieldIdx++
	}

	// Downstream of the toggles and upstream of the session field: the
	// worktree choice scopes what directory the session listing (if any) is
	// read from.
	if p.Command.PromptsCWD {
		b.WriteByte('\n')
		b.WriteString(m.renderSetupWorktreeField(cursor == fieldIdx))
		fieldIdx++
	}

	// Last field before Continue: the picker expands into a tall scrolling list
	// on focus, so it sits below the short fixed-height rows rather than pushing
	// them up and down as it opens and closes.
	if p.Command.Sessions == "claude" {
		b.WriteByte('\n')
		b.WriteString(m.renderSetupSessionField(cursor == fieldIdx))
		fieldIdx++
	}

	b.WriteByte('\n')
	btnCursor := "  "
	btnStyle := dialogNormal
	if cursor == fieldIdx {
		btnCursor = "> "
		btnStyle = dialogSelected
	}
	b.WriteString(btnCursor + btnStyle.Render("[Continue]") + "\n")

	b.WriteByte('\n')
	b.WriteString(dialogSubtle.Render("Tab next field  Space toggle  Enter submit  Esc back"))

	return b.String()
}
