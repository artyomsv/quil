package tui

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/artyomsv/aethel/internal/clipboard"
	"github.com/artyomsv/aethel/internal/config"
	"github.com/artyomsv/aethel/internal/ipc"
	"github.com/artyomsv/aethel/internal/plugin"
)

// chromeHeight is the vertical space consumed by tab bar (1) + status bar (1).
const chromeHeight = 2

// Minimum terminal dimensions for rendering.
const (
	minTermWidth  = 40
	minTermHeight = 10
)

// Messages from daemon
type PaneOutputMsg struct {
	PaneID string
	Data   []byte
	Ghost  bool
}

type WorkspaceStateMsg struct {
	ActiveTab string
	Tabs      []TabInfo
	Panes     []PaneInfo
}

type TabInfo struct {
	ID     string
	Name   string
	Color  string
	Panes  []string
	Layout json.RawMessage
}

type PaneInfo struct {
	ID    string
	TabID string
	CWD   string
	Name  string
	Type  string
}

// resizeTickMsg fires after the debounce delay; seq tracks freshness.
type resizeTickMsg struct {
	seq int
}

// PluginErrorMsg is received when the daemon detects a plugin error pattern.
type PluginErrorMsg struct {
	PaneID  string
	Title   string
	Message string
}

// setActivePaneMsg is sent when MCP requests focus on a specific pane.
type setActivePaneMsg struct {
	PaneID string
}

// paneEventMsg delivers a notification event from the daemon.
type paneEventMsg ipc.PaneEventPayload

// pasteRefreshMsg triggers a re-render after paste so the cursor updates.
type pasteRefreshMsg struct{}

// sidebarTickMsg triggers a periodic sidebar re-render to update relative timestamps.
type sidebarTickMsg struct{}

// PaneRef stores a pane location for navigation history.
type PaneRef struct {
	TabIndex int
	PaneID   string
}

// highlightPaneMsg triggers an orange border highlight on a pane for MCP interactions.
type highlightPaneMsg struct {
	PaneID string
}

// clearHighlightMsg clears the orange border highlight after the timer expires.
// Seq must match the pane's current sequence to avoid clearing a renewed highlight.
type clearHighlightMsg struct {
	PaneID string
	Seq    int
}

// spinnerTickMsg advances the resuming spinner animation for a pane.
type spinnerTickMsg struct {
	paneID string
	frame  int
}

// dialogPasteMsg delivers clipboard content to the active dialog input field.
type dialogPasteMsg string

var tabColors = []string{
	"",    // default (no custom color)
	"1",   // red
	"2",   // green
	"3",   // yellow
	"4",   // blue
	"5",   // magenta
	"6",   // cyan
	"208", // orange
}

type dialogScreen int

const (
	dialogNone dialogScreen = iota
	dialogAbout
	dialogSettings
	dialogShortcuts
	dialogConfirm
	dialogCreatePane
	dialogPluginError
	dialogInstanceForm
	dialogPlugins
	dialogTOMLEditor
	dialogDisclaimer
)

type Model struct {
	tabs                 []*TabModel
	activeTab            int
	width                int
	height               int
	client               *ipc.Client
	cfg                  config.Config
	version              string
	attached             bool
	renaming             bool
	renameInput          string
	renamingPane         bool
	paneRenameInput      string
	pendingWidth         int
	pendingHeight        int
	resizeSeq            int
	pendingSplit         map[string]*LayoutNode // tabID → placeholder node awaiting pane from daemon
	dialog               dialogScreen           // active dialog screen
	dialogCursor         int                    // highlighted item in dialog
	dialogEdit           bool                   // editing a settings value
	dialogInput          string                 // text input buffer for editing
	confirmKind          string                 // "pane" or "tab"
	confirmID            string                 // ID of pane/tab to delete
	confirmName          string                 // display name for confirmation
	devMode              bool                   // true when AETHEL_HOME is set
	pluginRegistry       *plugin.Registry       // plugin registry (shared with daemon)
	lastWidth            int                    // last known window width (for persistence)
	lastHeight           int                    // last known window height (for persistence)
	createPaneStep       int                    // 0=category, 1=plugin, 2=split direction
	selectedCategory     int                    // selected category index in create pane dialog
	selectedPlugin       string                 // selected plugin name in create pane dialog
	pluginErrorTitle     string                 // title for plugin error dialog
	pluginErrorMessage   string                 // message for plugin error dialog
	instanceStore        InstanceStore          // saved plugin instances (loaded from instances.json)
	instanceFormValues   []string               // form field values (indexed by FormField position)
	instanceFormCursor   int                    // active field in instance form
	selectedInstanceArgs []string               // args from selected instance (for IPC)
	selectedInstanceName string                 // name from selected instance (for IPC)
	tomlEditor           *TextEditor            // active TOML editor (nil when not editing)
	selection            *Selection             // active text selection (nil when none)
	mouseDown            bool                   // true while left mouse button is held
	mouseStartX          int                    // screen X of mouse press
	mouseStartY          int                    // screen Y of mouse press
	configChanged        bool                   // true when config needs saving on exit
	disclaimerTipIdx     int                    // random tip index for disclaimer dialog
	mcpHighlights        map[string]bool        // pane IDs with active MCP highlight
	mcpHighlightSeq      map[string]int         // sequence number for highlight timer reset
	notifications        *NotificationCenter    // notification sidebar
	paneHistory          []PaneRef              // navigation history (bounded, 20 max)
	sidebarFocused       bool                   // true when notification sidebar has keyboard focus
	notesMode            bool                   // true when pane notes editor is open for the active pane
	notesEditor          *NotesEditor           // active notes editor (nil when notesMode is false)
}

func NewModel(client *ipc.Client, cfg config.Config, version string, registry *plugin.Registry) Model {
	m := Model{
		client:         client,
		cfg:            cfg,
		version:        version,
		devMode:        os.Getenv("AETHEL_HOME") != "",
		pluginRegistry: registry,
		instanceStore:  LoadInstances(config.InstancesPath()),
		mcpHighlights:   make(map[string]bool),
		mcpHighlightSeq: make(map[string]int),
		notifications:   NewNotificationCenter(cfg.Notification.SidebarWidth, cfg.Notification.MaxEvents),
	}
	if cfg.UI.ShowDisclaimer && len(disclaimerTips) > 0 {
		m.dialog = dialogDisclaimer
		m.disclaimerTipIdx = rand.Intn(len(disclaimerTips))
	}
	return m
}

// WindowSize returns the last known window dimensions for persistence.
func (m Model) WindowSize() (width, height int) {
	return m.lastWidth, m.lastHeight
}

// Config returns the current config (may be modified by user actions).
func (m Model) Config() config.Config { return m.cfg }

// FlushNotes writes any pending notes edits to disk. Safe to call when notes
// mode is inactive (no-op).
//
// Precondition: must be invoked AFTER tea.Program.Run has returned, when the
// Update goroutine is no longer pumping events. Calling concurrently with the
// Update loop is unsafe — the editor is mutable shared state.
func (m Model) FlushNotes() {
	if m.notesEditor != nil {
		if err := m.notesEditor.Close(); err != nil {
			log.Printf("flush notes on exit: %v", err)
		}
	}
}

// ConfigChanged reports whether the config was modified and needs saving.
func (m Model) ConfigChanged() bool { return m.configChanged }

func (m Model) Init() tea.Cmd {
	log.Print("TUI Init — starting listener")
	return m.listenForMessages()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		log.Printf("WindowSizeMsg: %dx%d", msg.Width, msg.Height)
		m.pendingWidth = msg.Width
		m.pendingHeight = msg.Height
		m.lastWidth = msg.Width
		m.lastHeight = msg.Height
		m.resizeSeq++

		// First resize: apply immediately for initial attach
		if !m.attached {
			m.attached = true
			m.width = msg.Width
			m.height = msg.Height
			m.resizeTabs()
			log.Print("first WindowSizeMsg — attaching to daemon")
			return m, tea.Batch(m.resizeAllPanes(), m.attachToDaemon())
		}

		// Debounce subsequent resizes
		seq := m.resizeSeq
		return m, tea.Tick(150*time.Millisecond, func(t time.Time) tea.Msg {
			return resizeTickMsg{seq: seq}
		})

	case resizeTickMsg:
		if msg.seq != m.resizeSeq {
			return m, nil // stale tick, newer resize pending
		}
		m.width = m.pendingWidth
		m.height = m.pendingHeight
		m.resizeTabs()
		return m, m.resizeAllPanes()

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case tea.MouseClickMsg:
		if msg.Mod.Contains(tea.ModCtrl) {
			return m, nil
		}
		// Right-click: copy selection to clipboard
		if msg.Button == tea.MouseRight && m.selection != nil {
			tab := m.activeTabModel()
			if tab != nil {
				if pane := tab.ActivePaneModel(); pane != nil {
					text := extractText(pane, m.selection)
					m.selection = nil
					if text != "" {
						return m, func() tea.Msg {
							clipboard.Write(text)
							return nil
						}
					}
					return m, nil
				}
			}
			m.selection = nil
			return m, nil
		}
		if msg.Button == tea.MouseLeft {
			if msg.Y == 0 {
				// Tab bar click
				m.mouseDown = false
				if idx := m.hitTestTab(msg.X); idx >= 0 {
					cmd := m.switchTab(idx)
					return m, cmd
				}
			} else if msg.Y < m.height-1 {
				// Pane area — start tracking for drag selection
				m.mouseDown = true
				m.mouseStartX = msg.X
				m.mouseStartY = msg.Y
				m.selection = nil // clear previous selection
			}
		}
		return m, nil

	case tea.MouseMotionMsg:
		if m.mouseDown {
			tab := m.activeTabModel()
			if tab != nil && tab.Root != nil {
				tabH := m.height - chromeHeight
				m.updateMouseSelection(tab, msg.X, msg.Y, tabH)
			}
		}
		return m, nil

	case tea.MouseReleaseMsg:
		if m.mouseDown {
			m.mouseDown = false
			if m.selection == nil {
				// No drag — treat as click for pane focus. Skip when notes
				// mode is active so the editor stays bound to its pane
				// regardless of where the user clicks.
				tab := m.activeTabModel()
				if tab != nil && tab.Root != nil && !tab.FocusMode() && !m.notesMode {
					tabH := m.height - chromeHeight
					if pane := tab.Root.FindPaneAt(m.mouseStartX, m.mouseStartY, 0, 1, m.paneAreaWidth(), tabH); pane != nil {
						if old := tab.ActivePaneModel(); old != nil {
							old.Active = false
						}
						pane.Active = true
						tab.ActivePane = pane.ID
					}
				}
			}
		}
		return m, nil

	case tea.MouseWheelMsg:
		lines := m.cfg.UI.MouseScrollLines
		if lines < 1 {
			lines = 3
		}
		if tab := m.activeTabModel(); tab != nil {
			if pane := tab.ActivePaneModel(); pane != nil {
				if msg.Button == tea.MouseWheelUp {
					pane.ScrollUp(lines)
				} else if msg.Button == tea.MouseWheelDown {
					pane.ScrollDown(lines)
				}
			}
		}
		return m, nil

	case tea.PasteMsg:
		if m.dialog == dialogTOMLEditor && m.tomlEditor != nil {
			text := strings.ReplaceAll(msg.Content, "\r", "")
			m.tomlEditor.InsertMultiLine(text)
			m.tomlEditor.Dirty = true
			return m, nil
		} else if m.dialog != dialogNone && m.dialogEdit {
			m.dialogInput += sanitizeDialogInput(msg.Content)
			return m, nil
		} else if m.notesMode && m.notesEditor != nil {
			text := strings.ReplaceAll(msg.Content, "\r", "")
			m.notesEditor.HandlePaste(text)
			return m, nil
		} else {
			m.sendClipboardToPane(msg.Content)
			// Schedule re-render after PTY echo arrives to update cursor position
			return m, tea.Tick(100*time.Millisecond, func(_ time.Time) tea.Msg { return pasteRefreshMsg{} })
		}

	case pasteRefreshMsg:
		return m, nil // triggers re-render with updated VT emulator cursor

	case dialogPasteMsg:
		m.dialogInput += sanitizeDialogInput(string(msg))
		return m, nil

	case editorPasteMsg:
		if m.dialog == dialogTOMLEditor && m.tomlEditor != nil {
			text := strings.ReplaceAll(string(msg), "\r", "")
			m.tomlEditor.InsertMultiLine(text)
			m.tomlEditor.Dirty = true
		} else if m.notesMode && m.notesEditor != nil {
			text := strings.ReplaceAll(string(msg), "\r", "")
			m.notesEditor.HandlePaste(text)
		}
		return m, nil

	case PaneOutputMsg:
		cmd := m.handlePaneOutput(msg)
		if cmd != nil {
			return m, tea.Batch(cmd, m.listenForMessages())
		}
		return m, m.listenForMessages()

	case spinnerTickMsg:
		// Advance spinner frame for the resuming/preparing pane
		for _, tab := range m.tabs {
			if tab.Root == nil {
				continue
			}
			if leaf := tab.Root.FindLeaf(msg.paneID); leaf != nil && (leaf.Pane.resuming || leaf.Pane.preparing) {
				// Auto-clear after minimum display + no more ghost state
				if !leaf.Pane.ghost && time.Since(leaf.Pane.resumeStart) >= 2*time.Second {
					leaf.Pane.resuming = false
					leaf.Pane.preparing = false
					return m, nil
				}
				leaf.Pane.spinnerFrame = msg.frame
				nextFrame := msg.frame + 1
				paneID := msg.paneID
				return m, tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
					return spinnerTickMsg{paneID: paneID, frame: nextFrame}
				})
			}
		}
		return m, nil

	case PluginErrorMsg:
		m.dialog = dialogPluginError
		m.pluginErrorTitle = msg.Title
		m.pluginErrorMessage = msg.Message
		return m, tea.Batch(tea.ClearScreen, m.listenForMessages())

	case WorkspaceStateMsg:
		log.Printf("WorkspaceState: %d tabs, %d panes", len(msg.Tabs), len(msg.Panes))
		newPaneIDs := m.applyWorkspaceState(msg)
		m.resizeTabs()
		cmds := []tea.Cmd{m.listenForMessages(), m.resizeAllPanes(), m.sendAllLayouts()}
		// Start spinner ticks for newly restored panes
		for _, paneID := range newPaneIDs {
			id := paneID
			cmds = append(cmds, tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
				return spinnerTickMsg{paneID: id, frame: 1}
			}))
		}
		return m, tea.Batch(cmds...)

	case setActivePaneMsg:
		// Find which tab contains this pane and switch to it
		for i, tab := range m.tabs {
			if tab.Root != nil && tab.Root.PaneIDs()[msg.PaneID] {
				m.activeTab = i
				tab.ActivePane = msg.PaneID
				log.Printf("set_active_pane: switched to tab %d pane %s", i, msg.PaneID)
				return m, m.listenForMessages()
			}
		}
		log.Printf("set_active_pane: pane %s not found", msg.PaneID)
		return m, m.listenForMessages()

	case highlightPaneMsg:
		m.mcpHighlights[msg.PaneID] = true
		m.mcpHighlightSeq[msg.PaneID]++
		seq := m.mcpHighlightSeq[msg.PaneID]
		dur, err := time.ParseDuration(m.cfg.MCP.HighlightDuration)
		if err != nil || dur <= 0 {
			dur = 10 * time.Second
		}
		if dur > 60*time.Second {
			dur = 60 * time.Second
		}
		paneID := msg.PaneID
		return m, tea.Batch(
			m.listenForMessages(),
			tea.Tick(dur, func(_ time.Time) tea.Msg {
				return clearHighlightMsg{PaneID: paneID, Seq: seq}
			}),
		)

	case clearHighlightMsg:
		// Only clear if sequence matches (a newer highlight hasn't replaced us)
		if m.mcpHighlightSeq[msg.PaneID] == msg.Seq {
			delete(m.mcpHighlights, msg.PaneID)
		}
		return m, nil

	case paneEventMsg:
		m.notifications.AddEvent(ipc.PaneEventPayload(msg))
		cmds := []tea.Cmd{m.listenForMessages()}
		// Refresh sidebar tick if visible (no auto-show — user controls with Alt+N)
		if m.notifications.visible {
			cmds = append(cmds, m.sidebarTick())
		}
		return m, tea.Batch(cmds...)

	case sidebarTickMsg:
		// Re-render sidebar to update relative timestamps; schedule next tick if still visible
		if m.notifications.visible && m.notifications.Count() > 0 {
			return m, m.sidebarTick()
		}
		return m, nil

	case notesTickMsg:
		// Debounce check: save if dirty and idle for >= notesDebounceWindow.
		if m.notesMode && m.notesEditor != nil {
			m.notesEditor.MaybeAutoSave()
			return m, m.notesTick()
		}
		return m, nil

	case listenContinueMsg:
		return m, m.listenForMessages()
	}

	return m, nil
}

func (m Model) handleNotificationKey(key string) (tea.Model, tea.Cmd) {
	action, eventID, paneID := m.notifications.HandleKey(key)
	switch action {
	case "navigate":
		// Push current location to history, then jump to event's pane in focus mode
		m.pushPaneHistory()
		for i, tab := range m.tabs {
			if tab.Root != nil && tab.Root.PaneIDs()[paneID] {
				m.activeTab = i
				tab.ActivePane = paneID
				if !tab.FocusMode() {
					tab.ToggleFocus()
				}
				m.sidebarFocused = false
				break
			}
		}
		return m, nil
	case "dismiss":
		if eventID != "" {
			if msg, err := ipc.NewMessage(ipc.MsgDismissEvent, ipc.DismissEventPayload{EventID: eventID}); err == nil {
				if err := m.client.Send(msg); err != nil {
					log.Printf("dismiss event send: %v", err)
				}
			}
		}
		return m, nil
	case "dismiss_all":
		if msg, err := ipc.NewMessage(ipc.MsgDismissEvent, ipc.DismissEventPayload{}); err == nil {
			if err := m.client.Send(msg); err != nil {
				log.Printf("dismiss all send: %v", err)
			}
		}
		return m, nil
	case "unfocus":
		m.sidebarFocused = false
		return m, nil
	}
	return m, nil
}

func (m *Model) pushPaneHistory() {
	if tab := m.activeTabModel(); tab != nil && tab.ActivePane != "" {
		ref := PaneRef{TabIndex: m.activeTab, PaneID: tab.ActivePane}
		m.paneHistory = append(m.paneHistory, ref)
		if len(m.paneHistory) > 20 {
			m.paneHistory = m.paneHistory[len(m.paneHistory)-20:]
		}
	}
}

func (m Model) popPaneHistory() (tea.Model, tea.Cmd) {
	for len(m.paneHistory) > 0 {
		ref := m.paneHistory[len(m.paneHistory)-1]
		m.paneHistory = m.paneHistory[:len(m.paneHistory)-1]
		if ref.TabIndex < len(m.tabs) {
			tab := m.tabs[ref.TabIndex]
			if tab.Root != nil && tab.Root.PaneIDs()[ref.PaneID] {
				m.activeTab = ref.TabIndex
				tab.ActivePane = ref.PaneID
				return m, nil
			}
		}
	}
	return m, nil
}

// paneAreaWidth returns the width available for pane content, accounting for sidebar.
func (m Model) paneAreaWidth() int {
	if m.notifications.visible && m.dialog == dialogNone {
		if tab := m.activeTabModel(); tab != nil && !tab.FocusMode() {
			sw := m.notifications.width
			if m.width-sw >= minTermWidth {
				return m.width - sw
			}
		}
	}
	return m.width
}

func (m Model) sidebarTick() tea.Cmd {
	return tea.Tick(10*time.Second, func(_ time.Time) tea.Msg {
		return sidebarTickMsg{}
	})
}

// notesTick schedules a debounce check while notes mode is active.
func (m Model) notesTick() tea.Cmd {
	return tea.Tick(notesTickInterval, func(_ time.Time) tea.Msg {
		return notesTickMsg{}
	})
}

// toggleNotesMode opens the notes editor for the active pane, or closes
// (and flushes) it if notes mode is already active. Exits focus mode first
// if it is active — focus and notes are mutually exclusive.
func (m Model) toggleNotesMode() (tea.Model, tea.Cmd) {
	if m.notesMode && m.notesEditor != nil {
		return m.exitNotesMode()
	}
	tab := m.activeTabModel()
	if tab == nil {
		return m, nil
	}
	pane := tab.ActivePaneModel()
	if pane == nil {
		return m, nil
	}
	if tab.FocusMode() {
		tab.ToggleFocus()
	}
	// Initial dimensions are placeholders — View() will Resize the editor
	// to fit the actual notes panel area on the next render pass.
	editor, err := NewNotesEditor(config.NotesDir(), pane.ID, pane.Name, 1, 1)
	if err != nil {
		log.Printf("open notes: %v", err)
		return m, nil
	}
	m.notesMode = true
	m.notesEditor = editor
	return m, tea.Batch(tea.ClearScreen, m.resizeAllPanes(), m.notesTick())
}

// notesKeyExempt reports whether a key should bypass the notes editor and
// reach the normal global handlers (navigation, structural changes, dialogs).
// Anything not on this list is consumed by the editor as text input.
func (m Model) notesKeyExempt(key string) bool {
	kb := m.cfg.Keybindings
	switch key {
	// Structural — close/split implicitly destroys the bound pane and must
	// flush + exit notes before running.
	case kb.ClosePane, kb.CloseTab, kb.SplitHorizontal, kb.SplitVertical:
		return true
	// Tab and pane navigation.
	case kb.NextPane, kb.PrevPane, kb.NewTab:
		return true
	// Tab management.
	case kb.RenameTab, kb.RenamePane, kb.CycleTabColor:
		return true
	// Other modes.
	case kb.FocusPane:
		return true
	// Notification center.
	case kb.NotificationToggle, kb.NotificationFocus, kb.GoBack:
		return true
	// Tools and dialogs.
	case kb.JSONTransform, kb.QuickActions, "f1", "ctrl+n":
		return true
	}
	// Alt+1..9 tab switching.
	switch key {
	case "alt+1", "alt+2", "alt+3", "alt+4",
		"alt+5", "alt+6", "alt+7", "alt+8", "alt+9":
		return true
	}
	return false
}

// exitNotesMode flushes any pending notes edits and closes the editor.
func (m Model) exitNotesMode() (tea.Model, tea.Cmd) {
	if m.notesEditor != nil {
		if err := m.notesEditor.Close(); err != nil {
			log.Printf("save notes on exit: %v", err)
		}
	}
	m.notesMode = false
	m.notesEditor = nil
	return m, tea.Batch(tea.ClearScreen, m.resizeAllPanes())
}

func (m Model) View() tea.View {
	var content string

	if m.width == 0 || m.height == 0 {
		content = "Connecting to aetheld..."
	} else if m.width < minTermWidth || m.height < minTermHeight {
		content = fmt.Sprintf("Terminal too small (%dx%d)\nMinimum: %dx%d",
			m.width, m.height, minTermWidth, minTermHeight)
		content = lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, content)
	} else if m.dialog == dialogTOMLEditor && m.tomlEditor != nil {
		// TOML editor takes over the full screen (bypasses dialog rendering)
		content = m.renderTOMLEditorFullScreen()
	} else if m.dialog != dialogNone {
		content = m.renderDialog()
	} else {
		var sections []string

		// Tab bar (1 line)
		sections = append(sections, m.renderTabBar())

		// Active tab content + optional notification sidebar
		tabH := m.height - chromeHeight
		sidebarW := 0
		if m.notifications.visible && m.dialog == dialogNone {
			if tab := m.activeTabModel(); tab != nil && !tab.FocusMode() {
				sidebarW = m.notifications.width
				if m.width-sidebarW < minTermWidth {
					sidebarW = 0 // too narrow for sidebar
				}
			}
		}
		if m.activeTab < len(m.tabs) {
			tab := m.tabs[m.activeTab]

			// Notes mode reserves the right portion of the tab area for the editor.
			notesW := 0
			if m.notesMode && m.notesEditor != nil {
				notesW = (m.width - sidebarW) * 2 / 5 // ~40%
				if notesW < 30 {
					notesW = 30
				}
				if m.width-sidebarW-notesW < minTermWidth {
					notesW = 0 // too narrow
				}
			}

			tab.Resize(m.width-sidebarW-notesW, tabH)
			// Pass per-frame state to panes for rendering
			if tab.Root != nil {
				for _, pane := range tab.Root.Leaves() {
					pane.activeSel = m.selection
					pane.focusMode = tab.FocusMode() && pane.ID == tab.ActivePane
					pane.mcpHighlight = m.mcpHighlights[pane.ID]
				}
			}
			tabContent := tab.View()
			if notesW > 0 {
				tabContent = lipgloss.JoinHorizontal(lipgloss.Top, tabContent, m.notesEditor.View(notesW, tabH))
			}
			if sidebarW > 0 {
				m.notifications.focused = m.sidebarFocused
				tabContent = lipgloss.JoinHorizontal(lipgloss.Top, tabContent, m.notifications.View(tabH))
			}
			sections = append(sections, tabContent)
		}

		// Status bar
		sections = append(sections, m.renderStatusBar())

		content = lipgloss.JoinVertical(lipgloss.Left, sections...)
	}

	// Hide the terminal cursor — we render our own via insertCursor()
	content += "\x1b[?25l"
	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	kb := m.cfg.Keybindings

	// Dialog mode: route input to dialog handler
	if m.dialog != dialogNone {
		return m.handleDialogKey(msg)
	}

	// Rename mode: capture input for tab/pane name editing
	if m.renaming {
		return m.handleRenameKey(msg)
	}
	if m.renamingPane {
		return m.handlePaneRenameKey(msg)
	}

	// Notes mode: while active, the notes editor owns text input but global
	// shortcuts (navigation, structural changes, dialogs) must still reach
	// the normal handlers. Anything that is not in the exempt set goes to
	// the editor.
	if m.notesMode && m.notesEditor != nil {
		switch key {
		case kb.NotesToggle:
			return m.exitNotesMode()
		case kb.Quit:
			if err := m.notesEditor.Close(); err != nil {
				log.Printf("save notes on quit: %v", err)
			}
			return m, tea.Quit
		}
		if m.notesKeyExempt(key) {
			// Structural / navigational shortcut — flush notes (in case the
			// action removes the active pane) then fall through to the
			// normal handlers.
			if err := m.notesEditor.Close(); err != nil {
				log.Printf("save notes on shortcut: %v", err)
			}
			m.notesMode = false
			m.notesEditor = nil
		} else {
			action, cmd := m.notesEditor.HandleKey(key)
			if action == notesActionExit {
				return m.exitNotesMode()
			}
			return m, cmd
		}
	}

	// Notification sidebar keybindings (always available)
	switch {
	case key == kb.NotificationToggle:
		// Alt+N: toggle visibility only, never focus
		m.notifications.visible = !m.notifications.visible
		m.sidebarFocused = false
		if m.notifications.visible {
			return m, tea.Batch(tea.ClearScreen, m.resizeAllPanes(), m.sidebarTick())
		}
		return m, tea.Batch(tea.ClearScreen, m.resizeAllPanes())
	case key == kb.NotificationFocus:
		// Ctrl+Alt+N: open (if hidden) and focus sidebar
		if !m.notifications.visible {
			m.notifications.visible = true
		}
		m.sidebarFocused = true
		return m, tea.Batch(tea.ClearScreen, m.resizeAllPanes(), m.sidebarTick())
	case key == kb.GoBack:
		return m.popPaneHistory()
	}

	// Sidebar focused: route keys to notification center
	if m.sidebarFocused && m.notifications.visible {
		return m.handleNotificationKey(key)
	}

	// Selection: Enter copies (tmux convention), Esc clears, Cmd+C for macOS
	if m.selection != nil && key == "esc" {
		m.selection = nil
		return m, nil
	}
	if m.selection != nil && (key == "enter" || key == "super+c") {
		tab := m.activeTabModel()
		if tab != nil {
			if pane := tab.ActivePaneModel(); pane != nil {
				text := extractText(pane, m.selection)
				m.selection = nil
				if text != "" {
					return m, func() tea.Msg {
						clipboard.Write(text)
						return nil
					}
				}
				return m, nil
			}
		}
		m.selection = nil
		return m, nil
	}

	// Selection: Shift+Arrow / Ctrl+Shift+Arrow
	if strings.HasPrefix(key, "shift+") || strings.HasPrefix(key, "ctrl+shift+") || strings.HasPrefix(key, "ctrl+alt+shift+") {
		return m.handleSelectionKey(key)
	}

	switch {
	case key == kb.Quit:
		return m, tea.Quit

	case key == kb.NewTab:
		return m, m.createTab()

	case key == kb.ClosePane:
		if tab := m.activeTabModel(); tab != nil {
			if pane := tab.ActivePaneModel(); pane != nil {
				m.dialog = dialogConfirm
				m.confirmKind = "pane"
				m.confirmID = pane.ID
				m.confirmName = pane.Name
				if m.confirmName == "" {
					m.confirmName = pane.CWD
				}
				if m.confirmName == "" {
					if len(pane.ID) > 8 {
						m.confirmName = pane.ID[:8]
					} else {
						m.confirmName = pane.ID
					}
				}
			}
		}
		return m, tea.ClearScreen

	case key == kb.CloseTab:
		if tab := m.activeTabModel(); tab != nil {
			m.dialog = dialogConfirm
			m.confirmKind = "tab"
			m.confirmID = tab.ID
			m.confirmName = tab.Name
		}
		return m, tea.ClearScreen

	case key == kb.SplitHorizontal:
		if tab := m.activeTabModel(); tab != nil && tab.FocusMode() {
			tab.ExitFocus()
		}
		return m, m.splitPane(SplitHorizontal)

	case key == kb.SplitVertical:
		if tab := m.activeTabModel(); tab != nil && tab.FocusMode() {
			tab.ExitFocus()
		}
		return m, m.splitPane(SplitVertical)

	case key == kb.RenameTab:
		if tab := m.activeTabModel(); tab != nil {
			m.renaming = true
			m.renameInput = tab.Name
		}
		return m, nil

	case key == kb.RenamePane:
		if tab := m.activeTabModel(); tab != nil {
			if pane := tab.ActivePaneModel(); pane != nil {
				m.renamingPane = true
				m.paneRenameInput = pane.Name
			}
		}
		return m, nil

	case key == kb.CycleTabColor:
		return m, m.cycleTabColor()

	case key == kb.ScrollPageUp:
		if tab := m.activeTabModel(); tab != nil {
			if pane := tab.ActivePaneModel(); pane != nil {
				lines := m.cfg.UI.PageScrollLines
				if lines <= 0 {
					lines = pane.vt.Height() / 2
				}
				pane.ScrollUp(lines)
			}
		}
		return m, nil

	case key == kb.ScrollPageDown:
		if tab := m.activeTabModel(); tab != nil {
			if pane := tab.ActivePaneModel(); pane != nil {
				lines := m.cfg.UI.PageScrollLines
				if lines <= 0 {
					lines = pane.vt.Height() / 2
				}
				pane.ScrollDown(lines)
			}
		}
		return m, nil

	case key == kb.NextPane:
		if tab := m.activeTabModel(); tab != nil && !tab.FocusMode() {
			tab.NextPane()
		}
		return m, nil

	case key == kb.PrevPane:
		if tab := m.activeTabModel(); tab != nil && !tab.FocusMode() {
			tab.PrevPane()
		}
		return m, nil

	case key == kb.Paste:
		return m, m.pasteClipboard()

	case key == kb.FocusPane:
		if tab := m.activeTabModel(); tab != nil && tab.Root != nil {
			tab.ToggleFocus()
			m.resizeTabs()
			return m, tea.Batch(tea.ClearScreen, m.resizeAllPanes())
		}
		return m, nil

	case key == kb.NotesToggle:
		return m.toggleNotesMode()

	case key == "ctrl+n":
		m.dialog = dialogCreatePane
		m.dialogCursor = 0
		m.createPaneStep = 0
		m.selectedCategory = 0
		return m, tea.ClearScreen

	case key == "f1":
		m.dialog = dialogAbout
		m.dialogCursor = 0
		return m, tea.ClearScreen

	case key == "alt+1" || key == "alt+2" || key == "alt+3" ||
		key == "alt+4" || key == "alt+5" || key == "alt+6" ||
		key == "alt+7" || key == "alt+8" || key == "alt+9":
		idx := int(key[len(key)-1] - '1')
		cmd := m.switchTab(idx)
		return m, cmd

	default:
		// Only process keys that produce PTY bytes.
		// Bare modifiers (shift, ctrl, alt, super) produce nil — ignore them.
		data := keyToBytes(msg)
		if data == nil {
			return m, nil
		}
		m.selection = nil
		if tab := m.activeTabModel(); tab != nil {
			if pane := tab.ActivePaneModel(); pane != nil {
				pane.ResetScroll()
			}
		}
		return m, m.forwardInputBytes(data)
	}
}

func (m Model) handleRenameKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	switch key {
	case "enter":
		m.renaming = false
		name := strings.TrimSpace(m.renameInput)
		if name != "" {
			if tab := m.activeTabModel(); tab != nil {
				tab.Name = name
				return m, m.updateTab(tab.ID, name, tab.Color)
			}
		}
		return m, nil

	case "escape":
		m.renaming = false
		return m, nil

	case "backspace":
		if len(m.renameInput) > 0 {
			m.renameInput = m.renameInput[:len(m.renameInput)-1]
		}
		return m, nil

	default:
		if len(key) == 1 {
			m.renameInput += key
		} else if key == "space" {
			m.renameInput += " "
		}
		return m, nil
	}
}

func (m Model) handlePaneRenameKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	switch key {
	case "enter":
		m.renamingPane = false
		name := strings.TrimSpace(m.paneRenameInput)
		if name != "" {
			if tab := m.activeTabModel(); tab != nil {
				if pane := tab.ActivePaneModel(); pane != nil {
					pane.Name = name
					return m, m.updatePane(pane.ID, name)
				}
			}
		}
		return m, nil

	case "escape":
		m.renamingPane = false
		return m, nil

	case "backspace":
		if len(m.paneRenameInput) > 0 {
			m.paneRenameInput = m.paneRenameInput[:len(m.paneRenameInput)-1]
		}
		return m, nil

	default:
		if len(key) == 1 {
			m.paneRenameInput += key
		} else if key == "space" {
			m.paneRenameInput += " "
		}
		return m, nil
	}
}

func (m *Model) handlePaneOutput(msg PaneOutputMsg) tea.Cmd {
	for _, tab := range m.tabs {
		if tab.Root == nil {
			continue
		}
		if leaf := tab.Root.FindLeaf(msg.PaneID); leaf != nil {
			oldCWD := leaf.Pane.CWD
			if msg.Ghost && m.cfg.GhostBuffer.Dimmed {
				if !leaf.Pane.ghost {
					log.Printf("pane %s: ghost=true (received %d bytes)", msg.PaneID, len(msg.Data))
				}
				leaf.Pane.ghost = true
			} else if !msg.Ghost {
				// Transitioning from ghost/restored to live output.
				// Reset VT only for non-terminal panes (e.g. Claude Code)
				// where ghost buffer ANSI sequences pollute cursor state.
				// Terminal panes keep their history — the ghost buffer IS
				// the terminal state and should be preserved.
				if leaf.Pane.ghost {
					// Only reset VT for TUI app panes (claude-code) where ghost
					// ANSI sequences pollute cursor state. Terminal-like panes
					// (terminal, ssh, etc.) preserve their ghost buffer as-is.
					if leaf.Pane.Type == "claude-code" {
						log.Printf("pane %s: ghost->live transition, resetting VT (type=%s)", msg.PaneID, leaf.Pane.Type)
						leaf.Pane.ResetVT()
					} else {
						log.Printf("pane %s: ghost->live transition, preserving VT (type=%q)", msg.PaneID, leaf.Pane.Type)
					}
				}
				leaf.Pane.ghost = false
				// Clear spinner labels after minimum display time (2s)
				if time.Since(leaf.Pane.resumeStart) >= 2*time.Second {
					leaf.Pane.resuming = false
					leaf.Pane.preparing = false
				}
			}
			leaf.Pane.AppendOutput(msg.Data)
			if leaf.Pane.CWD != oldCWD && leaf.Pane.CWD != "" {
				return m.updatePaneCWD(msg.PaneID, leaf.Pane.CWD)
			}
			return nil
		}
	}
	return nil
}

// applyWorkspaceState rebuilds the TUI state from daemon data.
// Returns IDs of newly created panes (for spinner activation).
func (m *Model) applyWorkspaceState(state WorkspaceStateMsg) []string {
	var newPaneIDs []string

	// Index existing tabs and panes for preservation.
	existingTabs := make(map[string]*TabModel)
	existingPanes := make(map[string]*PaneModel)
	for _, tab := range m.tabs {
		existingTabs[tab.ID] = tab
		if tab.Root != nil {
			for _, pane := range tab.Root.Leaves() {
				existingPanes[pane.ID] = pane
			}
		}
	}

	paneMap := make(map[string]*PaneInfo)
	for i := range state.Panes {
		paneMap[state.Panes[i].ID] = &state.Panes[i]
	}

	m.tabs = nil
	for _, tabInfo := range state.Tabs {
		// Reuse existing tab if possible (preserves layout tree).
		tab, exists := existingTabs[tabInfo.ID]
		if !exists {
			tab = NewTabModel(tabInfo.ID, tabInfo.Name)

			// New tab that doesn't exist locally — try to restore layout from daemon.
			if len(tabInfo.Layout) > 0 {
				tab = m.restoreTabLayout(tab, tabInfo, paneMap, existingPanes)
				// All panes in a restored tab are new
				for _, pid := range tabInfo.Panes {
					newPaneIDs = append(newPaneIDs, pid)
				}
				m.tabs = append(m.tabs, tab)
				continue
			}
		}
		tab.Name = tabInfo.Name
		tab.Color = tabInfo.Color

		// Build the set of panes the daemon says belong to this tab.
		daemonPaneSet := make(map[string]bool, len(tabInfo.Panes))
		for _, pid := range tabInfo.Panes {
			daemonPaneSet[pid] = true
		}

		// Prune panes the daemon removed.
		if tab.Root != nil {
			for id := range tab.Root.PaneIDs() {
				if !daemonPaneSet[id] {
					tab.RemovePane(id)
				}
			}
		}

		// Exit focus mode if the tree was reduced to a single pane or empty.
		if tab.FocusMode() && (tab.Root == nil || tab.Root.IsLeaf()) {
			tab.ExitFocus()
		}

		// Add panes the daemon has but the tree doesn't.
		treePaneIDs := make(map[string]bool)
		if tab.Root != nil {
			treePaneIDs = tab.Root.PaneIDs()
		}
		for _, paneID := range tabInfo.Panes {
			if treePaneIDs[paneID] {
				// Already in tree — just update metadata.
				if info, ok := paneMap[paneID]; ok {
					if leaf := tab.Root.FindLeaf(paneID); leaf != nil {
						leaf.Pane.Name = info.Name
						leaf.Pane.CWD = info.CWD
						leaf.Pane.Type = info.Type
					}
				}
				continue
			}

			// New pane — reuse model if it existed elsewhere, otherwise create.
			pane, ok := existingPanes[paneID]
			if !ok {
				pane = NewPaneModel(paneID, m.replayBufSize())
				pane.resumeStart = time.Now()
				if len(existingTabs) > 0 {
					pane.preparing = true // new pane created while TUI is running
					log.Printf("apply: new pane %s (preparing)", paneID)
				} else if len(tabInfo.Layout) > 0 {
					pane.resuming = true // restored pane with saved layout
					log.Printf("apply: new pane %s (resuming, has layout)", paneID)
				} else {
					log.Printf("apply: new pane %s (fresh, no layout)", paneID)
				}
				newPaneIDs = append(newPaneIDs, paneID)
			}
			if info, ok := paneMap[paneID]; ok {
				pane.Name = info.Name
				pane.CWD = info.CWD
				pane.Type = info.Type
			}

			// Try to fill a pending split placeholder first.
			if m.pendingSplit != nil {
				if placeholder, ok := m.pendingSplit[tab.ID]; ok {
					placeholder.Pane = pane
					delete(m.pendingSplit, tab.ID)
					// Focus the new pane (it replaced the previously active one)
					tab.ActivePane = pane.ID
					continue
				}
			}

			// Fallback: insert at root level.
			if tab.Root == nil {
				tab.Root = NewLeaf(pane)
			} else {
				// Split the root horizontally to accommodate the new pane.
				tab.Root.SplitLeaf(tab.Root.Leaves()[0].ID, SplitVertical)
				tab.Root.FillPlaceholder(pane)
			}
		}

		// Clean up any unfilled placeholders (e.g., rapid double-splits).
		if tab.Root != nil {
			tab.Root.PrunePlaceholders()
		}

		m.finalizeTabPanes(tab)
		m.tabs = append(m.tabs, tab)
	}

	for i, tab := range m.tabs {
		if tab.ID == state.ActiveTab {
			m.activeTab = i
			break
		}
	}
	if m.activeTab >= len(m.tabs) {
		m.activeTab = max(0, len(m.tabs)-1)
	}

	// Reconcile notes mode: if the pane our notes editor is bound to no
	// longer exists in any tab, flush whatever is in the editor and exit
	// notes mode so we don't keep writing to a now-orphaned notes file.
	if m.notesMode && m.notesEditor != nil {
		bound := m.notesEditor.PaneID()
		found := false
		for _, tab := range m.tabs {
			if tab.Root == nil {
				continue
			}
			if tab.Root.PaneIDs()[bound] {
				found = true
				break
			}
		}
		if !found {
			if err := m.notesEditor.Close(); err != nil {
				log.Printf("flush notes for removed pane %s: %v", bound, err)
			}
			m.notesMode = false
			m.notesEditor = nil
		}
	}

	return newPaneIDs
}

// restoreTabLayout rebuilds a tab's layout tree from serialized daemon state.
func (m *Model) restoreTabLayout(tab *TabModel, tabInfo TabInfo, paneMap map[string]*PaneInfo, existingPanes map[string]*PaneModel) *TabModel {
	log.Printf("restoreLayout: tab %s %q with %d panes", tab.ID, tabInfo.Name, len(tabInfo.Panes))
	tab.Name = tabInfo.Name
	tab.Color = tabInfo.Color

	// Build PaneModel objects for all panes in this tab.
	paneModels := make(map[string]*PaneModel, len(tabInfo.Panes))
	for _, paneID := range tabInfo.Panes {
		pane, ok := existingPanes[paneID]
		if !ok {
			pane = NewPaneModel(paneID, m.replayBufSize())
			pane.resuming = true
			pane.resumeStart = time.Now()
		}
		if info, ok := paneMap[paneID]; ok {
			pane.Name = info.Name
			pane.CWD = info.CWD
			pane.Type = info.Type
		}
		paneModels[paneID] = pane
	}

	// Deserialize the layout tree.
	serialized, err := UnmarshalLayout(tabInfo.Layout)
	if err == nil && serialized != nil {
		tab.Root = DeserializeLayout(serialized, paneModels)
		if tab.Root != nil {
			tab.Root.PrunePlaceholders()
		}
	}

	// Add any panes not in the deserialized tree (e.g., created while TUI was away).
	treePaneIDs := make(map[string]bool)
	if tab.Root != nil {
		treePaneIDs = tab.Root.PaneIDs()
	}
	for _, paneID := range tabInfo.Panes {
		if treePaneIDs[paneID] {
			continue
		}
		pane := paneModels[paneID]
		if tab.Root == nil {
			tab.Root = NewLeaf(pane)
		} else {
			tab.Root.SplitLeaf(tab.Root.Leaves()[0].ID, SplitVertical)
			tab.Root.FillPlaceholder(pane)
		}
	}

	m.finalizeTabPanes(tab)
	return tab
}

// finalizeTabPanes ensures the active pane is valid and focus flags are set.
func (m *Model) finalizeTabPanes(tab *TabModel) {
	if tab.Root == nil {
		return
	}
	leaves := tab.Root.Leaves()
	if len(leaves) == 0 {
		return
	}
	found := false
	for _, p := range leaves {
		if p.ID == tab.ActivePane {
			found = true
			p.Active = true
		} else {
			p.Active = false
		}
	}
	if !found {
		tab.ActivePane = leaves[0].ID
		leaves[0].Active = true
	}
}

// replayBufSize returns the byte capacity for per-pane replay buffers,
// matching the daemon's ring buffer sizing.
func (m *Model) replayBufSize() int {
	size := m.cfg.GhostBuffer.MaxLines * 512
	if size <= 0 {
		size = 500 * 512
	}
	return size
}

func (m *Model) resizeTabs() {
	tabH := m.height - chromeHeight
	for _, tab := range m.tabs {
		tab.Resize(m.paneAreaWidth(), tabH)
	}
}

func (m Model) activeTabModel() *TabModel {
	if m.activeTab < len(m.tabs) {
		return m.tabs[m.activeTab]
	}
	return nil
}

// switchTab sets the active tab locally and notifies the daemon so its
// active_tab stays in sync (prevents stale overwrites on broadcastState).
func (m *Model) switchTab(idx int) tea.Cmd {
	if idx < 0 || idx >= len(m.tabs) {
		return nil
	}
	// Switching tabs leaves the notes-bound pane behind. Flush and exit
	// notes mode so we don't keep an editor open against a pane the user
	// can no longer see.
	if m.notesMode && m.notesEditor != nil {
		if err := m.notesEditor.Close(); err != nil {
			log.Printf("save notes on tab switch: %v", err)
		}
		m.notesMode = false
		m.notesEditor = nil
	}
	m.activeTab = idx
	tabID := m.tabs[idx].ID
	return func() tea.Msg {
		msg, _ := ipc.NewMessage(ipc.MsgSwitchTab, ipc.SwitchTabPayload{
			TabID: tabID,
		})
		m.client.Send(msg)
		return nil
	}
}

func (m Model) renderTabBar() string {
	if len(m.tabs) == 0 {
		return lipgloss.NewStyle().Width(m.width).Render("")
	}

	type renderedTab struct {
		text  string
		width int
	}

	// Pre-render all tabs
	all := make([]renderedTab, len(m.tabs))
	for i, tab := range m.tabs {
		name := tab.Name
		if m.renaming && i == m.activeTab {
			name = m.renameInput + "▎"
		} else {
			name = fmt.Sprintf("%d:%s", i+1, name)
		}

		style := inactiveTabStyle
		if i == m.activeTab {
			style = activeTabStyle
		}
		if tab.Color != "" {
			c := lipgloss.Color(tab.Color)
			if i == m.activeTab {
				style = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230")).Background(c).Padding(0, 1)
			} else {
				style = lipgloss.NewStyle().Foreground(lipgloss.Color("255")).Background(c).Padding(0, 1)
			}
		}

		rendered := style.Render(name)
		all[i] = renderedTab{text: rendered, width: lipgloss.Width(rendered)}
	}

	// Try to fit all tabs
	totalW := 0
	for i, rt := range all {
		totalW += rt.width
		if i > 0 {
			totalW++ // space separator
		}
	}

	if totalW <= m.width {
		// Everything fits
		tabs := make([]string, len(all))
		for i, rt := range all {
			tabs[i] = rt.text
		}
		bar := strings.Join(tabs, " ")
		return lipgloss.NewStyle().Width(m.width).Render(bar)
	}

	// Overflow: include active tab, expand outward, show indicator for hidden
	included := make([]bool, len(m.tabs))
	included[m.activeTab] = true
	usedW := all[m.activeTab].width

	// Reserve space for overflow indicator (e.g. " «3 more»")
	indicatorReserve := 12

	// Expand left, then right from active tab
	left := m.activeTab - 1
	right := m.activeTab + 1
	for left >= 0 || right < len(m.tabs) {
		if left >= 0 {
			need := all[left].width + 1 // +1 for separator
			if usedW+need+indicatorReserve <= m.width {
				included[left] = true
				usedW += need
				left--
			} else {
				left = -1 // stop expanding left
			}
		}
		if right < len(m.tabs) {
			need := all[right].width + 1
			if usedW+need+indicatorReserve <= m.width {
				included[right] = true
				usedW += need
				right++
			} else {
				right = len(m.tabs) // stop expanding right
			}
		}
	}

	// Build the bar with overflow indicators
	hidden := 0
	for _, inc := range included {
		if !inc {
			hidden++
		}
	}

	var parts []string
	for i, rt := range all {
		if included[i] {
			parts = append(parts, rt.text)
		}
	}
	bar := strings.Join(parts, " ")
	if hidden > 0 {
		indicator := fmt.Sprintf(" «%d more»", hidden)
		bar += lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Render(indicator)
	}

	return lipgloss.NewStyle().Width(m.width).Render(bar)
}

// hitTestTab returns the tab index at screen X coordinate, or -1 if none.
// Mirrors renderTabBar() width/overflow logic exactly.
func (m *Model) hitTestTab(x int) int {
	if len(m.tabs) == 0 {
		return -1
	}

	type renderedTab struct {
		width int
		index int
	}

	// Pre-render tab widths using the same styling as renderTabBar.
	all := make([]renderedTab, len(m.tabs))
	for i, tab := range m.tabs {
		name := tab.Name
		if m.renaming && i == m.activeTab {
			name = m.renameInput + "▎"
		} else {
			name = fmt.Sprintf("%d:%s", i+1, name)
		}

		style := inactiveTabStyle
		if i == m.activeTab {
			style = activeTabStyle
		}
		if tab.Color != "" {
			c := lipgloss.Color(tab.Color)
			if i == m.activeTab {
				style = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230")).Background(c).Padding(0, 1)
			} else {
				style = lipgloss.NewStyle().Foreground(lipgloss.Color("255")).Background(c).Padding(0, 1)
			}
		}

		rendered := style.Render(name)
		all[i] = renderedTab{width: lipgloss.Width(rendered), index: i}
	}

	// Determine which tabs are visible (same overflow logic).
	totalW := 0
	for i, rt := range all {
		totalW += rt.width
		if i > 0 {
			totalW++
		}
	}

	included := make([]bool, len(m.tabs))
	if totalW <= m.width {
		for i := range included {
			included[i] = true
		}
	} else {
		included[m.activeTab] = true
		usedW := all[m.activeTab].width
		indicatorReserve := 12

		left := m.activeTab - 1
		right := m.activeTab + 1
		for left >= 0 || right < len(m.tabs) {
			if left >= 0 {
				need := all[left].width + 1
				if usedW+need+indicatorReserve <= m.width {
					included[left] = true
					usedW += need
					left--
				} else {
					left = -1
				}
			}
			if right < len(m.tabs) {
				need := all[right].width + 1
				if usedW+need+indicatorReserve <= m.width {
					included[right] = true
					usedW += need
					right++
				} else {
					right = len(m.tabs)
				}
			}
		}
	}

	// Walk visible tabs and match X coordinate.
	cursor := 0
	for i, rt := range all {
		if !included[i] {
			continue
		}
		if cursor > 0 {
			cursor++ // space separator
		}
		if x >= cursor && x < cursor+rt.width {
			return i
		}
		cursor += rt.width
	}

	return -1
}

func (m Model) renderTOMLEditorFullScreen() string {
	e := m.tomlEditor
	e.ViewWidth = m.width
	e.ViewHeight = m.height - 2 // title bar + status bar

	var b strings.Builder

	// Title bar (raw ANSI — background color 236)
	title := "Edit: "
	if idx := strings.LastIndex(e.FilePath, "/"); idx >= 0 {
		title += e.FilePath[idx+1:]
	} else if idx := strings.LastIndex(e.FilePath, "\\"); idx >= 0 {
		title += e.FilePath[idx+1:]
	} else {
		title += e.FilePath
	}
	if e.Dirty {
		title += " *"
	}
	// Pad title to full width
	for len(title) < m.width {
		title += " "
	}
	b.WriteString("\x1b[48;5;236m\x1b[38;5;250m " + title + "\x1b[0m\n")

	// Editor content
	b.WriteString(e.Render())

	// Status bar — context-sensitive hints
	var status string
	if e.SaveErr != "" {
		status = fmt.Sprintf(" \x1b[31mError: %s\x1b[0m\x1b[48;5;236m\x1b[38;5;250m    Ln %d, Col %d", e.SaveErr, e.CursorRow+1, e.CursorCol+1)
	} else if e.Sel != nil && !e.Sel.IsEmpty() {
		status = fmt.Sprintf(" Enter copy  Ctrl+X cut  Esc clear    Ln %d, Col %d", e.CursorRow+1, e.CursorCol+1)
	} else {
		status = fmt.Sprintf(" Ctrl+S save  Ctrl+V paste  Esc close    Ln %d, Col %d", e.CursorRow+1, e.CursorCol+1)
	}
	for len(status) < m.width {
		status += " "
	}
	b.WriteString("\x1b[48;5;236m\x1b[38;5;250m" + status + "\x1b[0m")

	return b.String()
}

func (m Model) renderStatusBar() string {
	// Left side: pane info
	left := "aethel"
	if m.renamingPane {
		left = "Rename pane: " + m.paneRenameInput + "▎"
	} else if tab := m.activeTabModel(); tab != nil {
		paneCount := 0
		if tab.Root != nil {
			paneCount = len(tab.Root.Leaves())
		}
		paneInfo := fmt.Sprintf("tab %d/%d  panes:%d", m.activeTab+1, len(m.tabs), paneCount)

		if pane := tab.ActivePaneModel(); pane != nil {
			displayPath := pane.CWD
			if displayPath == "" {
				displayPath = pane.Name
			}
			if displayPath == "" {
				if len(pane.ID) > 8 {
					displayPath = pane.ID[:8]
				} else {
					displayPath = pane.ID
				}
			}
			left = fmt.Sprintf("%s  %s", displayPath, paneInfo)
			if tab.FocusMode() {
				left = "[focus] " + left
			}
			if m.notesMode && m.notesEditor != nil {
				marker := "[notes]"
				if m.notesEditor.Dirty() {
					marker = "[notes*]"
				}
				left = marker + " " + left
			}
			if pane.scrollBack > 0 {
				left += fmt.Sprintf("  ↑%d", pane.scrollBack)
			}
		} else {
			left = paneInfo
		}
	}

	// Right side: keybinding hints + version
	right := "^T tab | ^N pane | ^W close | F1 help | ^Q quit | v" + m.version
	if m.devMode {
		right = "[dev] " + right
	}
	if count := m.notifications.Count(); count > 0 && !m.notifications.visible {
		right = fmt.Sprintf("[%d events] ", count) + right
	}

	// Fit within width: left takes priority
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right) - 2 // 2 for padding
	if gap < 2 {
		// Not enough room for hints
		return statusBarStyle.Width(m.width).Render(left)
	}

	spacer := strings.Repeat(" ", gap)
	return statusBarStyle.Width(m.width).Render(left + spacer + right)
}

// Daemon communication commands

func (m Model) attachToDaemon() tea.Cmd {
	return func() tea.Msg {
		// Subtract chrome (tab bar + status bar), then pane border (2)
		tabH := m.height - chromeHeight
		cols := m.width - 2
		rows := tabH - 2
		if cols < 1 {
			cols = 1
		}
		if rows < 1 {
			rows = 1
		}
		msg, _ := ipc.NewMessage(ipc.MsgAttach, ipc.AttachPayload{
			Cols: cols,
			Rows: rows,
		})
		m.client.Send(msg)
		return nil
	}
}

// listenContinueMsg signals the TUI to keep listening for daemon messages.
type listenContinueMsg struct{}

func (m Model) listenForMessages() tea.Cmd {
	return func() tea.Msg {
		msg, err := m.client.Receive()
		if err != nil {
			log.Printf("listen error: %v", err)
			return tea.QuitMsg{}
		}

		switch msg.Type {
		case ipc.MsgPaneOutput:
			var payload ipc.PaneOutputPayload
			msg.DecodePayload(&payload)
			return PaneOutputMsg{PaneID: payload.PaneID, Data: payload.Data, Ghost: payload.Ghost}

		case ipc.MsgWorkspaceState:
			log.Print("ipc recv: workspace_state")
			var raw map[string]any
			msg.DecodePayload(&raw)
			return parseWorkspaceState(raw)

		case ipc.MsgPluginError:
			log.Printf("ipc recv: plugin_error")
			var payload ipc.PluginErrorPayload
			msg.DecodePayload(&payload)
			return PluginErrorMsg{
				PaneID:  payload.PaneID,
				Title:   payload.Title,
				Message: payload.Message,
			}

		case ipc.MsgSetActivePane:
			var payload ipc.SetActivePanePayload
			msg.DecodePayload(&payload)
			log.Printf("ipc recv: set_active_pane %s", payload.PaneID)
			return setActivePaneMsg{PaneID: payload.PaneID}

		case ipc.MsgCloseTUI:
			log.Print("ipc recv: close_tui")
			return tea.QuitMsg{}

		case ipc.MsgHighlightPane:
			var payload ipc.HighlightPanePayload
			msg.DecodePayload(&payload)
			return highlightPaneMsg{PaneID: payload.PaneID}

		case ipc.MsgPaneEvent:
			var payload ipc.PaneEventPayload
			msg.DecodePayload(&payload)
			log.Printf("ipc recv: pane_event %s %s %s", payload.Type, payload.PaneID, payload.Title)
			return paneEventMsg(payload)

		default:
			log.Printf("ipc recv: unknown type %q", msg.Type)
			return listenContinueMsg{}
		}
	}
}

func parseWorkspaceState(raw map[string]any) WorkspaceStateMsg {
	state := WorkspaceStateMsg{}
	if at, ok := raw["active_tab"].(string); ok {
		state.ActiveTab = at
	}
	if tabs, ok := raw["tabs"].([]any); ok {
		for _, t := range tabs {
			if tm, ok := t.(map[string]any); ok {
				ti := TabInfo{}
				if id, ok := tm["id"].(string); ok {
					ti.ID = id
				}
				if name, ok := tm["name"].(string); ok {
					ti.Name = name
				}
				if color, ok := tm["color"].(string); ok {
					ti.Color = color
				}
				if panes, ok := tm["panes"].([]any); ok {
					for _, p := range panes {
						if s, ok := p.(string); ok {
							ti.Panes = append(ti.Panes, s)
						}
					}
				}
				if layout, ok := tm["layout"]; ok && layout != nil {
					// Re-marshal the nested map back to json.RawMessage
					if data, err := json.Marshal(layout); err == nil {
						ti.Layout = data
					}
				}
				state.Tabs = append(state.Tabs, ti)
			}
		}
	}
	if panes, ok := raw["panes"].([]any); ok {
		for _, p := range panes {
			if pm, ok := p.(map[string]any); ok {
				pi := PaneInfo{}
				if id, ok := pm["id"].(string); ok {
					pi.ID = id
				}
				if tabID, ok := pm["tab_id"].(string); ok {
					pi.TabID = tabID
				}
				if cwd, ok := pm["cwd"].(string); ok {
					pi.CWD = cwd
				}
				if name, ok := pm["name"].(string); ok {
					pi.Name = name
				}
				if typ, ok := pm["type"].(string); ok {
					pi.Type = typ
				}
				state.Panes = append(state.Panes, pi)
			}
		}
	}
	return state
}

func (m Model) createTab() tea.Cmd {
	return func() tea.Msg {
		msg, _ := ipc.NewMessage(ipc.MsgCreateTab, ipc.CreateTabPayload{
			Name: "New Tab",
		})
		m.client.Send(msg)
		return nil
	}
}

func (m *Model) splitPane(dir SplitDir) tea.Cmd {
	tab := m.activeTabModel()
	if tab == nil {
		return nil
	}
	pane := tab.ActivePaneModel()
	if pane == nil {
		return nil
	}

	// Split the active pane's leaf, creating a placeholder for the new pane.
	placeholder := tab.SplitAtPane(pane.ID, dir)
	if placeholder == nil {
		return nil
	}

	// Track the placeholder so applyWorkspaceState can fill it.
	if m.pendingSplit == nil {
		m.pendingSplit = make(map[string]*LayoutNode)
	}
	m.pendingSplit[tab.ID] = placeholder

	tabID := tab.ID
	return func() tea.Msg {
		msg, _ := ipc.NewMessage(ipc.MsgCreatePane, ipc.CreatePanePayload{
			TabID: tabID,
		})
		m.client.Send(msg)
		return nil
	}
}

func (m Model) updateTab(tabID, name, color string) tea.Cmd {
	return func() tea.Msg {
		msg, _ := ipc.NewMessage(ipc.MsgUpdateTab, ipc.UpdateTabPayload{
			TabID: tabID,
			Name:  name,
			Color: color,
		})
		m.client.Send(msg)
		return nil
	}
}

func (m Model) cycleTabColor() tea.Cmd {
	tab := m.activeTabModel()
	if tab == nil {
		return nil
	}

	// Find current color index and cycle to next
	idx := 0
	for i, c := range tabColors {
		if c == tab.Color {
			idx = i
			break
		}
	}
	idx = (idx + 1) % len(tabColors)
	tab.Color = tabColors[idx]

	return m.updateTab(tab.ID, tab.Name, tab.Color)
}

func (m Model) forwardInputBytes(data []byte) tea.Cmd {
	return func() tea.Msg {
		tab := m.activeTabModel()
		if tab == nil {
			return nil
		}
		pane := tab.ActivePaneModel()
		if pane == nil {
			return nil
		}

		msg, _ := ipc.NewMessage(ipc.MsgPaneInput, ipc.PaneInputPayload{
			PaneID: pane.ID,
			Data:   data,
		})
		m.client.Send(msg)
		return nil
	}
}

func (m Model) pasteClipboard() tea.Cmd {
	return func() tea.Msg {
		text, err := clipboard.Read()
		if err != nil {
			log.Printf("clipboard read: %v", err)
			return nil
		}
		if text == "" {
			return nil
		}
		tab := m.activeTabModel()
		if tab == nil {
			return nil
		}
		pane := tab.ActivePaneModel()
		if pane == nil {
			return nil
		}
		// Wrap in bracketed paste sequences so the shell treats newlines
		// as literal text, not as Enter presses.
		var data []byte
		data = append(data, "\x1b[200~"...)
		data = append(data, []byte(text)...)
		data = append(data, "\x1b[201~"...)
		msg, _ := ipc.NewMessage(ipc.MsgPaneInput, ipc.PaneInputPayload{
			PaneID: pane.ID,
			Data:   data,
		})
		m.client.Send(msg)
		// Wait for PTY echo to arrive before triggering re-render
		time.Sleep(100 * time.Millisecond)
		return pasteRefreshMsg{}
	}
}

func (m Model) pasteToDialog() tea.Cmd {
	return func() tea.Msg {
		text, err := clipboard.Read()
		if err != nil {
			log.Printf("clipboard read for dialog: %v", err)
			return nil
		}
		if text == "" {
			return nil
		}
		return dialogPasteMsg(text)
	}
}

func (m *Model) updateMouseSelection(tab *TabModel, curX, curY, tabH int) {
	if tab.Root == nil {
		return
	}

	var pane *PaneModel
	var ox, oy int

	if tab.FocusMode() {
		// Focus mode: active pane fills entire tab, tree splits don't apply
		pane = tab.ActivePaneModel()
		if pane == nil {
			return
		}
		ox = 0
		oy = 1 // tab bar
	} else {
		startRect := tab.Root.FindPaneRectAt(m.mouseStartX, m.mouseStartY, 0, 1, m.paneAreaWidth(), tabH)
		if startRect == nil {
			return
		}
		pane = startRect.Pane
		ox = startRect.OX
		oy = startRect.OY
	}

	sbLen := pane.vt.ScrollbackLen()

	// Convert start screen coords to pane-local
	startCol := m.mouseStartX - ox - 1
	startRow := m.mouseStartY - oy - 1
	startLine := sbLen - pane.scrollBack + startRow

	// Convert current screen coords to pane-local (clamp to same pane)
	curCol := curX - ox - 1
	curRow := curY - oy - 1
	curLine := sbLen - pane.scrollBack + curRow

	// Clamp
	w := pane.vt.Width()
	h := pane.vt.Height()
	if startCol < 0 {
		startCol = 0
	}
	if startCol >= w {
		startCol = w - 1
	}
	if curCol < 0 {
		curCol = 0
	}
	if curCol >= w {
		curCol = w - 1
	}
	if startLine < 0 {
		startLine = 0
	}
	if curLine < 0 {
		curLine = 0
	}
	maxLine := sbLen + h - 1
	if startLine > maxLine {
		startLine = maxLine
	}
	if curLine > maxLine {
		curLine = maxLine
	}

	m.selection = &Selection{
		PaneID: pane.ID,
		Anchor: SelectionAnchor{Col: startCol, Line: startLine},
		Cursor: SelectionAnchor{Col: curCol, Line: curLine},
	}
}

func (m Model) handleSelectionKey(key string) (tea.Model, tea.Cmd) {
	tab := m.activeTabModel()
	if tab == nil {
		return m, nil
	}
	pane := tab.ActivePaneModel()
	if pane == nil {
		return m, nil
	}

	sbLen := pane.vt.ScrollbackLen()

	// Initialize selection at VT cursor position if not started
	if m.selection == nil {
		pos := pane.vt.CursorPosition()
		absLine := sbLen + pos.Y
		m.selection = &Selection{
			PaneID: pane.ID,
			Anchor: SelectionAnchor{Col: pos.X, Line: absLine},
			Cursor: SelectionAnchor{Col: pos.X, Line: absLine},
		}
	}

	cur := m.selection.Cursor
	maxLine := lastContentLine(pane)
	switch key {
	case "shift+right":
		cur.Col++
	case "shift+left":
		cur.Col--
	case "shift+down":
		cur.Line++
	case "shift+up":
		cur.Line--
	case "ctrl+shift+right":
		cur = selWordJump(pane, cur, 1, 1, maxLine)
	case "ctrl+shift+left":
		cur = selWordJump(pane, cur, -1, 1, maxLine)
	case "ctrl+alt+shift+right":
		cur = selWordJump(pane, cur, 1, 3, maxLine)
	case "ctrl+alt+shift+left":
		cur = selWordJump(pane, cur, -1, 3, maxLine)
	default:
		// Unknown shift combo — clear selection, don't forward
		m.selection = nil
		return m, nil
	}

	// Clamp vertical
	if cur.Line < 0 {
		cur.Line = 0
	}
	if cur.Line > maxLine {
		cur.Line = maxLine
	}

	// Wrap horizontal: if past end of line, move to start of next line;
	// if before start, move to end of previous line.
	endCol := lineContentEnd(pane, cur.Line)
	if cur.Col < 0 {
		// Wrap to previous line
		if cur.Line > 0 {
			cur.Line--
			prevEnd := lineContentEnd(pane, cur.Line)
			if prevEnd >= 0 {
				cur.Col = prevEnd
			} else {
				cur.Col = 0
			}
		} else {
			cur.Col = 0
		}
	} else if endCol >= 0 && cur.Col > endCol {
		// Wrap to next line
		if cur.Line < maxLine {
			cur.Line++
			cur.Col = 0
		} else {
			cur.Col = endCol
		}
	} else if endCol < 0 {
		// Empty line — try wrapping
		if cur.Col > 0 && cur.Line < maxLine {
			cur.Line++
			cur.Col = 0
		} else {
			cur.Col = 0
		}
	}

	// Calculate delta from previous cursor to new cursor
	prevCur := m.selection.Cursor
	m.selection.Cursor = cur

	// Move shell cursor horizontally when staying on the same line.
	// Cross-line selection is visual only — sending Up/Down to PTY
	// would trigger command history navigation.
	if cur.Line == prevCur.Line {
		colDelta := cur.Col - prevCur.Col
		if colDelta != 0 {
			var moveBytes []byte
			for i := 0; i < colDelta; i++ {
				moveBytes = append(moveBytes, "\x1b[C"...)
			}
			for i := 0; i > colDelta; i-- {
				moveBytes = append(moveBytes, "\x1b[D"...)
			}
			return m, m.forwardInputBytes(moveBytes)
		}
	}
	return m, nil
}

// sanitizeDialogInput strips control characters from text before inserting
// into dialog input fields. Prevents ANSI escapes, null bytes, and newlines
// from reaching form values that may be used as command arguments.
func sanitizeDialogInput(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '\t' || r >= ' ' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// sendClipboardToPane sends text to the active pane as PTY input.
// NOTE: This does NOT wrap in bracketed paste sequences because it handles
// tea.PasteMsg events, which originate from the terminal's own bracketed paste
// — the terminal has already signaled paste mode to the shell.
func (m Model) sendClipboardToPane(text string) {
	if text == "" {
		return
	}
	tab := m.activeTabModel()
	if tab == nil {
		return
	}
	pane := tab.ActivePaneModel()
	if pane == nil {
		return
	}
	msg, _ := ipc.NewMessage(ipc.MsgPaneInput, ipc.PaneInputPayload{
		PaneID: pane.ID,
		Data:   []byte(text),
	})
	m.client.Send(msg)
}

func keyToBytes(keyMsg tea.KeyPressMsg) []byte {
	s := keyMsg.String()

	switch s {
	case "enter":
		return []byte("\r")
	case "tab":
		return []byte("\t")
	case "backspace":
		return []byte{0x7f}
	case "space":
		return []byte(" ")
	case "esc":
		return []byte{0x1b}
	case "up":
		return []byte("\x1b[A")
	case "down":
		return []byte("\x1b[B")
	case "right":
		return []byte("\x1b[C")
	case "left":
		return []byte("\x1b[D")
	case "ctrl+right":
		return []byte("\x1b[1;5C") // word jump right
	case "ctrl+left":
		return []byte("\x1b[1;5D") // word jump left
	case "ctrl+alt+right":
		// 3-word jump: send word-jump 3 times
		return []byte("\x1b[1;5C\x1b[1;5C\x1b[1;5C")
	case "ctrl+alt+left":
		return []byte("\x1b[1;5D\x1b[1;5D\x1b[1;5D")
	case "delete":
		return []byte("\x1b[3~")
	case "home":
		return []byte("\x1b[H")
	case "end":
		return []byte("\x1b[F")
	case "pgup":
		return []byte("\x1b[5~")
	case "pgdown":
		return []byte("\x1b[6~")
	case "insert":
		return []byte("\x1b[2~")
	case "f1":
		return []byte("\x1bOP")
	case "f2":
		return []byte("\x1bOQ")
	case "f3":
		return []byte("\x1bOR")
	case "f4":
		return []byte("\x1bOS")
	case "f5":
		return []byte("\x1b[15~")
	case "f6":
		return []byte("\x1b[17~")
	case "f7":
		return []byte("\x1b[18~")
	case "f8":
		return []byte("\x1b[19~")
	case "f9":
		return []byte("\x1b[20~")
	case "f10":
		return []byte("\x1b[21~")
	case "f11":
		return []byte("\x1b[23~")
	case "f12":
		return []byte("\x1b[24~")
	}

	// Ctrl+letter → raw control character (0x01-0x1a)
	if len(s) == 6 && s[:5] == "ctrl+" {
		ch := s[5]
		if ch >= 'a' && ch <= 'z' {
			return []byte{ch - 'a' + 1}
		}
	}

	// Printable text — handles single ASCII, multi-byte UTF-8, and multi-rune IME input.
	if keyMsg.Text != "" {
		return []byte(keyMsg.Text)
	}

	return nil
}

func (m Model) resizeAllPanes() tea.Cmd {
	return func() tea.Msg {
		for _, tab := range m.tabs {
			if tab.Root == nil {
				continue
			}
			for _, pane := range tab.Root.Leaves() {
				cols := pane.Width - 2 // subtract border
				rows := pane.Height - 2
				if cols < 1 {
					cols = 1
				}
				if rows < 1 {
					rows = 1
				}
				msg, _ := ipc.NewMessage(ipc.MsgResizePane, ipc.ResizePanePayload{
					PaneID: pane.ID,
					Cols:   uint16(cols),
					Rows:   uint16(rows),
				})
				m.client.Send(msg)
			}
		}
		return nil
	}
}

func (m Model) updatePane(paneID, name string) tea.Cmd {
	return func() tea.Msg {
		msg, _ := ipc.NewMessage(ipc.MsgUpdatePane, ipc.UpdatePanePayload{
			PaneID: paneID,
			Name:   name,
		})
		m.client.Send(msg)
		return nil
	}
}

func (m Model) updatePaneCWD(paneID, cwd string) tea.Cmd {
	return func() tea.Msg {
		msg, _ := ipc.NewMessage(ipc.MsgUpdatePane, ipc.UpdatePanePayload{
			PaneID: paneID,
			CWD:    cwd,
		})
		m.client.Send(msg)
		return nil
	}
}

func (m Model) sendAllLayouts() tea.Cmd {
	return func() tea.Msg {
		for _, tab := range m.tabs {
			if tab.Root == nil {
				continue
			}
			data, err := MarshalLayout(tab.Root)
			if err != nil {
				continue
			}
			msg, _ := ipc.NewMessage(ipc.MsgUpdateLayout, ipc.UpdateLayoutPayload{
				TabID:  tab.ID,
				Layout: data,
			})
			m.client.Send(msg)
		}
		return nil
	}
}
