package tui

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/google/uuid"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/kubediscover"
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

	dialogKeyStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("63")).
			Width(16)

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

func shortcutsList(m *Model) []struct{ key, desc string } {
	kb := m.cfg.Keybindings
	// kbDisplay renders comma-separated multi-bindings as "a / b" so the
	// help text stays readable when an action has multiple bindings (e.g.
	// the macOS-friendly fallback on Rename pane).
	list := []struct{ key, desc string }{
		{kbDisplay(kb.Quit), "Quit"},
		{kbDisplay(kb.CommandPalette), "Command palette (fuzzy-find any action)"},
		{kbDisplay(kb.NewTab), "New tab"},
		{kbDisplay(kb.ClosePane), "Close pane"},
		{kbDisplay(kb.QuickActions), "Pane context menu (also mouse right-click)"},
		{kbDisplay(kb.CloseTab), "Close tab"},
		{kbDisplay(kb.SplitHorizontal), "Split side-by-side"},
		{kbDisplay(kb.SplitVertical), "Split top/bottom"},
		{kbDisplay(kb.PaneLeft), "Focus pane left"},
		{kbDisplay(kb.PaneRight), "Focus pane right"},
		{kbDisplay(kb.PaneUp), "Focus pane up"},
		{kbDisplay(kb.PaneDown), "Focus pane down"},
	}
	// Legacy linear pane cycling (unbound by default — hide when empty).
	if kb.NextPane != "" {
		list = append(list, struct{ key, desc string }{kbDisplay(kb.NextPane), "Next pane"})
	}
	if kb.PrevPane != "" {
		list = append(list, struct{ key, desc string }{kbDisplay(kb.PrevPane), "Previous pane"})
	}
	list = append(list, []struct{ key, desc string }{
		{kbDisplay(kb.RenameTab), "Rename tab"},
		{kbDisplay(kb.RenamePane), "Rename pane"},
		{kbDisplay(kb.CycleTabColor), "Cycle tab color"},
		{kbDisplay(kb.ScrollPageUp), "Scroll page up"},
		{kbDisplay(kb.ScrollPageDown), "Scroll page down"},
		{kbDisplay(kb.Paste), "Paste clipboard"},
		{kbDisplay(kb.FocusPane), "Toggle focus mode"},
		{kbDisplay(kb.NotesToggle), "Toggle pane notes"},
		{kbDisplay(kb.Redraw), "Force screen redraw"},
		{kbDisplay(kb.MutePane), "Mute / unmute pane notifications"},
		{kbDisplay(kb.RestartPane), "Restart pane process (sessions resume)"},
		{kbDisplay(kb.ToggleEager), "Toggle eager restore (active pane)"},
		{kbDisplay(kb.ToggleWrap), "Toggle preview soft-wrap (AI pane)"},
		{kbDisplay(kb.ToggleLazygit), "Toggle lazygit overlay for current repo"},
		{kbDisplay(kb.NotificationToggle), "Toggle notification sidebar"},
		{kbDisplay(kb.NotificationFocus), "Focus notification sidebar"},
		{kbDisplay(kb.GoBack), "Pane history back"},
		{"Ctrl+N", "New typed pane"},
		{"Alt+1..9", "Switch to tab N"},
		{"F1", "Help / About"},
		{"Tab / Shift+Tab", "→ PTY (shell completion, Claude Code modes)"},
		{"Shift+Arrows", "Select text"},
		{"Ctrl+Shift+←→", "Select word"},
		{"Ctrl+Alt+Shift+←→", "Select 3 words"},
		{"Ctrl+←→", "Jump word"},
		{"Ctrl+Alt+←→", "Jump 3 words"},
		{"Enter", "Copy selection"},
		{"Right-click", "Copy selection"},
		{"Esc", "Clear selection"},
		{"", ""},
		{"", "── Editor ──"},
		{"Shift+Arrows", "Select text (editor)"},
		{"Ctrl+Shift+←→", "Select word (editor)"},
		{"Ctrl+Alt+Shift+←→", "Select 3 words (editor)"},
		{"Enter", "Copy selection (editor)"},
		{"Ctrl+X", "Cut selection (editor)"},
		{"Ctrl+V", "Paste (editor)"},
		{"Ctrl+A", "Select all (editor)"},
		{"Ctrl+S", "Save (editor)"},
	}...)
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

// aboutStopDaemonIndex is the row index of "Stop daemon" in the F1 → About
// (root) menu. Stop daemon was promoted from the nested Settings list to the
// root menu so it sits alongside Settings/Shortcuts/Plugins. Kept as a named
// constant so handleAboutKey, lastAboutItem, and the confirm-dialog Esc
// handler cannot drift on the index.
const aboutStopDaemonIndex = 8

func (m Model) handleAboutKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	const lastAboutItem = aboutStopDaemonIndex // 0:Settings 1:Shortcuts 2:Plugins 3:Memory 4:Client 5:Daemon 6:MCP 7:Update 8:Stop daemon
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
		case aboutStopDaemonIndex:
			// Stop daemon: route to the shutdown confirm. Enter here only
			// opens the confirm; the confirm itself requires `y` to fire
			// MsgShutdown (see handleConfirmKey).
			m.dialog = dialogConfirm
			m.confirmKind = confirmKindShutdown
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
			fields[m.dialogCursor].set(&m, m.dialogInput)
			m.dialogEdit = false
			m.dialogInput = ""
		case key == "backspace":
			if len(m.dialogInput) > 0 {
				m.dialogInput = m.dialogInput[:len(m.dialogInput)-1]
			}
		case key == m.cfg.Keybindings.Paste:
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
	switch msg.String() {
	case "esc":
		m.dialog = dialogAbout
		m.dialogCursor = 0
	}
	return m, nil
}

func (m Model) handleConfirmKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "n":
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
			// Back to the About menu, cursor on the update row.
			m.dialog = dialogAbout
			m.dialogCursor = aboutUpdateIndex
			return m, nil
		}
		m.dialog = dialogNone
		return m, nil
	case "enter", "y":
		kind := m.confirmKind
		id := m.confirmID

		// Stop-daemon requires explicit `y` — Enter is the universal
		// select/commit key across every menu and toggle, so accepting it
		// here would let finger-memory Enter (these dialogs are keyboard-only)
		// kill the daemon + SIGHUP every pane child. `y` is a deliberate
		// keystroke a user does not press accidentally.
		if kind == confirmKindShutdown && msg.String() != "y" {
			return m, nil
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
				if sendErr := m.client.Send(req); sendErr != nil {
					log.Printf("restart pane %s: send: %v", id, sendErr)
				}
			}
			return m, nil
		}

		// Apply-update: quit the TUI with the apply intent set; main.go
		// performs verify → swap → respawn after the program exits (the
		// terminal must be released before the wrapper respawn).
		if kind == confirmKindApplyUpdate {
			m.dialog = dialogNone
			m.applyUpdateOnExit = true
			return m, tea.Quit
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
		client := m.client
		return m, func() tea.Msg {
			switch kind {
			case "pane":
				req, _ := ipc.NewMessage(ipc.MsgDestroyPane, ipc.DestroyPanePayload{
					PaneID: id,
				})
				client.Send(req)
			case "tab":
				req, _ := ipc.NewMessage(ipc.MsgDestroyTab, ipc.DestroyTabPayload{
					TabID: id,
				})
				client.Send(req)
			}
			return nil
		}
	}
	return m, nil
}

// handleGitRepoPickKey drives the Alt+G multi-repo picker: a plain list of
// git repos found near the active pane's CWD. Enter opens the lazygit
// overlay for the highlighted repo; Esc cancels.
func (m Model) handleGitRepoPickKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "esc":
		m.dialog = dialogNone
		m.repoPickCandidates = nil
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
		m.dialog = dialogNone
		m.repoPickCandidates = nil
		tab := m.activeTabModel()
		if tab == nil {
			return m, tea.ClearScreen
		}
		// createOverlay uses a pointer receiver so it mutates m directly
		// (Go takes &m on a value-receiver local variable). The returned m
		// reflects all mutations including pendingOverlayShow.
		return m, tea.Batch(tea.ClearScreen, m.createOverlay(tab, repo))
	}
	return m, nil
}

// --- Rendering ---

func (m Model) renderDialog() string {
	// Determine dialog width: plugin-specific for instance screens only
	width := dialogWidth
	if m.dialog == dialogTOMLEditor {
		width = 74
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
	case dialogCommandPalette:
		width = paletteWidth
		content = renderCommandPalette(m)
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
		aboutUpdateLabel(m.updateInfo, m.version),
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

func (m Model) renderShortcutsDialog() string {
	var b strings.Builder

	b.WriteString(dialogTitle.Render("Shortcuts"))
	b.WriteString("\n\n")

	for _, s := range shortcutsList(&m) {
		b.WriteString(fmt.Sprintf("  %s%s\n",
			dialogKeyStyle.Render(s.key),
			dialogValStyle.Render(s.desc)))
	}

	b.WriteByte('\n')
	b.WriteString(dialogSubtle.Render("Esc back"))

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
		b.WriteString("  " + dialogNormal.Render(fmt.Sprintf("Apply update v%s now?", m.confirmName)))
		b.WriteString("\n\n")
		b.WriteString("  " + dialogSubtle.Render("The TUI restarts and the daemon respawns all panes."))
		b.WriteString("\n")
		b.WriteString("  " + dialogSubtle.Render("Claude sessions resume; running shell commands are killed."))
	default:
		label := fmt.Sprintf("Close %s %q?", m.confirmKind, m.confirmName)
		b.WriteString("  " + dialogNormal.Render(label))
	}
	b.WriteString("\n\n")

	b.WriteString("  " + dialogSubtle.Render(footer))

	return b.String()
}

func (m Model) renderGitRepoPickDialog() string {
	var b strings.Builder

	b.WriteString(dialogTitle.Render("Open lazygit for which repo?"))
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
		if cmd := m.enterSetupOrSplit(p); cmd != nil {
			return m, cmd
		}
		m.createPaneStep = 3
		return m, nil
	}

	// Step 3: selected placement (split direction)
	return m.handleCreatePaneSplit()
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
	pluginName := m.selectedPlugin
	instanceName := m.selectedInstanceName
	instanceArgs := m.selectedInstanceArgs
	resumeSessionID := m.selectedSessionID
	cwd := m.selectedCWD
	if cwd != "" {
		m.lastSelectedCWD = cwd
		m.recentCWDs = pushRecentCWD(m.recentCWDs, cwd, recentCWDMax)
		if err := SaveRecentCWDs(config.RecentCWDsPath(), m.recentCWDs); err != nil {
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
	m.resetSessionSelection()

	tab := m.activeTabModel()
	if tab == nil {
		return m, nil
	}
	pane := tab.ActivePaneModel()
	if pane == nil {
		return m, nil
	}

	tabID := tab.ID
	client := m.client

	logger.Debug("create pane: sending IPC with cwd=%q type=%s instance=%s", cwd, pluginName, instanceName)

	// Option 2: Replace current pane
	if m.dialogCursor == 2 {
		oldPaneID := pane.ID

		if leaf := tab.Root.FindLeaf(oldPaneID); leaf != nil {
			// Detach + dispose immediately: the daemon destroys the old pane
			// server-side and this PaneModel is never rendered again (output
			// and rendering resolve panes via FindLeaf, which skips nil-Pane
			// leaves). Disposing here — not via the reconciliation sweep —
			// keeps the leaves cache honest: a stale cache was previously
			// what fed the detached pane into the sweep's existingPanes.
			old := leaf.Pane
			leaf.Pane = nil
			tab.invalidateLeaves()
			if old != nil {
				old.Dispose()
			}
			if m.pendingSplit == nil {
				m.pendingSplit = make(map[string]*LayoutNode)
			}
			m.pendingSplit[tab.ID] = leaf
		}

		return m, func() tea.Msg {
			msg, _ := ipc.NewMessage(ipc.MsgCreatePane, ipc.CreatePanePayload{
				TabID:           tabID,
				CWD:             cwd,
				Type:            pluginName,
				InstanceName:    instanceName,
				InstanceArgs:    instanceArgs,
				ReplacePaneID:   oldPaneID,
				ResumeSessionID: resumeSessionID,
			})
			client.Send(msg)
			return nil
		}
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
		return m, nil
	}

	if m.pendingSplit == nil {
		m.pendingSplit = make(map[string]*LayoutNode)
	}
	m.pendingSplit[tab.ID] = placeholder

	return m, func() tea.Msg {
		msg, _ := ipc.NewMessage(ipc.MsgCreatePane, ipc.CreatePanePayload{
			TabID:           tabID,
			CWD:             cwd,
			Type:            pluginName,
			InstanceName:    instanceName,
			InstanceArgs:    instanceArgs,
			ResumeSessionID: resumeSessionID,
		})
		client.Send(msg)
		return nil
	}
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
		case key == m.cfg.Keybindings.Paste:
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

	// Either show setup dialog (CWD/toggles) or proceed to split direction.
	if cmd := m.enterSetupOrSplit(p); cmd != nil {
		return m, cmd
	}
	m.dialog = dialogCreatePane
	m.createPaneStep = 3
	m.dialogCursor = 0
	return m, nil
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
		m.pluginRegistry.DetectAvailability()
		client := m.client
		m.dialog = dialogNone
		return m, func() tea.Msg {
			msg, _ := ipc.NewMessage(ipc.MsgReloadPlugins, nil)
			client.Send(msg)
			return nil
		}
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
		m.pluginRegistry.DetectAvailability()
		m.tomlEditor = nil
		m.dialog = dialogPlugins
		m.dialogCursor = 0
		client := m.client
		reloadCmd := func() tea.Msg {
			msg, _ := ipc.NewMessage(ipc.MsgReloadPlugins, nil)
			client.Send(msg)
			return nil
		}
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
// the setup dialog (if plugin needs CWD prompt or has toggles) or jump to
// split-direction selection. Returns nil when no setup is needed, in which
// case the caller is responsible for advancing to step 3.
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
	m.cwdBrowseDir = ""
	m.cwdBrowseEntries = nil
	m.cwdBrowseCursor = 0
	m.cwdBrowseScroll = 0
	m.cwdBrowseParent = ""
	m.cwdBrowseRoots = nil
	m.cwdBrowseTruncated = false
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
	m.kubeContexts = nil
	m.kubeCursor = 0
	m.resetSessionSelection()

	needsSetup := p != nil && (p.Command.PromptsCWD || len(p.Command.Toggles) > 0 ||
		p.Command.Discover == "kube" || p.Command.Sessions == "claude")
	if !needsSetup {
		m.createPaneStep = 3
		return nil
	}

	// The browser's pre-fill now costs a round trip, so the dialog opens first
	// and fills in when the daemon answers.
	var browseCmd tea.Cmd

	if p.Command.PromptsCWD {
		if p.Command.Discover == "git" {
			// Discovery base is the active pane's OSC7 CWD directly — not
			// lastSelectedCWD (that memory belongs to the generic browser; a
			// stale last-choice from another project would seed wrong
			// candidates).
			var base string
			if tab := m.activeTabModel(); tab != nil {
				if pane := tab.ActivePaneModel(); pane != nil {
					base = pane.CWD
				}
			}
			// Asked of the DAEMON, never resolved here — gitdiscover run in this
			// process stats the machine drawing the UI, which is the wrong disk
			// whenever the daemon is remote (RD-021). Whether there turn out to
			// be any candidates isn't known until the answer lands, so the
			// recent-locations/browser fallback that used to run right below
			// this branch now runs in applyGitReposPickList instead.
			browseCmd = m.requestGitRepos(base, "", repoScanPickList)
		} else {
			browseCmd = m.fallbackToRecentOrBrowser()
		}
	}

	if p.Command.Discover == "kube" {
		m.kubeContexts = kubediscover.Contexts(context.Background())
		if len(m.kubeContexts) > maxKubeContexts {
			m.kubeContexts = m.kubeContexts[:maxKubeContexts]
		}
		m.kubeCursor = 0 // Default context row
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
	return tea.Batch(tea.ClearScreen, browseCmd)
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
		// Offer the last-used directories as a quick pick (skipping any that no
		// longer exist). Falls through to the browser if the filtered list is
		// empty.
		m.recentCandidates = existingDirs(m.recentCWDs)
		if len(m.recentCandidates) > 0 {
			m.cwdBrowseDir = m.recentCandidates[0]
			m.cwdBrowseCursor = 0
			return nil
		}
	}
	return m.initSetupBrowser()
}

// existingDirs filters paths down to those that still resolve to a directory,
// preserving order. Keeps stale (deleted) entries out of the recent-locations
// pick list.
func existingDirs(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			out = append(out, p)
		}
	}
	return out
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
	// Pre-select the first git candidate so Enter-through submits it.
	m.cwdBrowseDir = m.repoCandidates[0]
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
	// The root list is the daemon's complete answer — carrying the previous
	// directory's cap forward would warn about a listing no longer on screen.
	m.cwdBrowseTruncated = false
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
// session picker (if sessions="claude") + 1 for the Continue button.
func (m Model) setupFieldCount(p *plugin.PanePlugin) int {
	n := len(p.Command.Toggles) + 1 // +1 for Continue
	if p.Command.PromptsCWD {
		n++
	}
	if p.Command.Discover == "kube" {
		n++
	}
	if p.Command.Sessions == "claude" {
		n++
	}
	return n
}

// setupFieldKind reports what field is at the given cursor index in the setup
// dialog. Returns "cwd", "kube", "toggle" (with toggleIdx), "session", or
// "continue".
//
// Order is CWD → kube → toggles → session → Continue. The session picker stays
// downstream of the CWD field because its contents are scoped to that directory,
// and sits last because it is the only field that expands: keeping it below the
// fixed-height rows means focusing it does not shift them.
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

// onSetupFieldFocused runs the side effect the now-focused field needs. Only
// the session picker has one: its listing is fetched on first focus rather than
// when the dialog opens, so creating a pane with a fresh session — the common
// case — performs no session I/O at all.
func (m *Model) onSetupFieldFocused(p *plugin.PanePlugin) tea.Cmd {
	if kind, _ := m.setupFieldKind(p, m.setupFieldCursor); kind == "session" {
		return m.ensureSessionScan()
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
// submitSetupDialog's selectedCWD = cwdBrowseDir capture works unchanged.
func (m Model) handleSetupPickKey(p *plugin.PanePlugin, key string) (tea.Model, tea.Cmd) {
	pick, _ := m.activeCWDPick()
	rows := len(pick) + 1 // +1 for Browse…

	// syncSelection keeps cwdBrowseDir aligned with the highlighted candidate
	// row. Not called when the cursor is on the "Browse…" row.
	syncSelection := func() {
		if m.cwdBrowseCursor < len(pick) {
			m.cwdBrowseDir = pick[m.cwdBrowseCursor]
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
			m.cwdBrowseDir = ""
			m.cwdBrowseCursor = 0
			return m, m.initSetupBrowser()
		}
		// Selecting a candidate submits the dialog (the folder IS the answer
		// to the CWD question; toggles keep their defaults unless the user
		// tabbed to them first).
		m.cwdBrowseDir = pick[m.cwdBrowseCursor]
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

// submitSetupDialog commits the browser-selected directory and toggle states,
// then advances the create-pane flow to the split-direction step.
func (m Model) submitSetupDialog(p *plugin.PanePlugin) (tea.Model, tea.Cmd) {
	if p.Command.PromptsCWD {
		m.selectedCWD = m.cwdBrowseDir
		logger.Debug("setup dialog: captured cwd=%q from browser (plugin=%s)", m.selectedCWD, p.Name)
	}
	m.cwdInputError = ""

	// A resume target is only valid for the directory it was listed under. The
	// user can pick a session, Shift+Tab back to the browser, move to another
	// project, and press Continue without ever re-focusing the session field —
	// so the authoritative check belongs here, at the moment the choice is
	// committed, not only on the field's own focus path.
	if m.selectedSessionID != "" && m.sessionScanCWD != m.cwdBrowseDir {
		logger.Debug("setup dialog: dropping resume session (listed for %q, submitting %q)", m.sessionScanCWD, m.cwdBrowseDir)
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

	m.dialog = dialogCreatePane
	m.createPaneStep = 3
	m.dialogCursor = 0
	m.dialogEdit = false
	return m, tea.ClearScreen
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
			// may be remote — sanitize before it reaches a rendered row. The
			// raw value stays in m.cwdBrowseDir for the actual spawn CWD.
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
					// The directory the pane will actually spawn in, marked
					// whenever the caret above is not already on it: the field
					// is blurred, OR it is focused with the cursor parked on
					// the trailing "Browse…" row, which syncSelection
					// deliberately never commits. Both states otherwise read as
					// "nothing chosen" — the same hole, one just happens to be
					// reachable without leaving the field.
					//
					// Matched on cwdBrowseDir (the value submitSetupDialog
					// reads) rather than on the cursor, which is what makes the
					// Browse… case work at all. In pick mode cwdBrowseDir is
					// only ever assigned by copying an element out of this same
					// slice, so == is exact by construction and never depends on
					// pathEqual's case folding.
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
				displayName = sanitizeRemoteText(displayName)
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
			name := c.Name
			if c.Current {
				name = "● " + name
			}
			suffix := ""
			if c.Namespace != "" {
				suffix = "  (" + c.Namespace + ")"
			}
			renderRow(i+1, name, suffix)
		}
		if len(m.kubeContexts) == 0 {
			b.WriteString(dialogSubtle.Render("    (no kube contexts found — k9s uses its current context)") + "\n")
		} else {
			b.WriteString(dialogSubtle.Render("    ↑↓ navigate  Enter select") + "\n")
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
