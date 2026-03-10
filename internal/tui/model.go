package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/artyomsv/aethel/internal/ipc"
)

// Messages from daemon
type PaneOutputMsg struct {
	PaneID string
	Data   []byte
}

type WorkspaceStateMsg struct {
	ActiveTab string
	Tabs      []TabInfo
	Panes     []PaneInfo
}

type TabInfo struct {
	ID    string
	Name  string
	Panes []string
}

type PaneInfo struct {
	ID    string
	TabID string
	CWD   string
}

type Model struct {
	tabs      []*TabModel
	activeTab int
	width     int
	height    int
	client    *ipc.Client
}

func NewModel(client *ipc.Client) Model {
	return Model{
		client: client,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.attachToDaemon(),
		m.listenForMessages(),
	)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resizeTabs()
		return m, m.resizeAllPanes()

	case tea.KeyMsg:
		return m.handleKey(msg)

	case PaneOutputMsg:
		m.handlePaneOutput(msg)
		return m, m.listenForMessages()

	case WorkspaceStateMsg:
		m.applyWorkspaceState(msg)
		m.resizeTabs()
		return m, tea.Batch(m.listenForMessages(), m.resizeAllPanes())

	case listenContinueMsg:
		return m, m.listenForMessages()
	}

	return m, nil
}

func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		return "Connecting to aetheld..."
	}

	var sections []string

	// Tab bar (1 line)
	sections = append(sections, m.renderTabBar())

	// Active tab content
	tabH := m.height - 2 // tab bar + status bar
	if m.activeTab < len(m.tabs) {
		tab := m.tabs[m.activeTab]
		tab.Resize(m.width, tabH)
		sections = append(sections, tab.View())
	}

	// Status bar
	sections = append(sections, m.renderStatusBar())

	return lipgloss.JoinVertical(lipgloss.Left, sections...)
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	switch key {
	case "ctrl+c", "ctrl+q":
		return m, tea.Quit

	case "ctrl+t":
		return m, m.createTab()

	case "ctrl+w":
		return m, m.closeActivePane()

	case "ctrl+shift+h":
		return m, m.splitPane(SplitHorizontal)

	case "ctrl+shift+v":
		return m, m.splitPane(SplitVertical)

	case "tab":
		if tab := m.activeTabModel(); tab != nil {
			tab.NextPane()
		}
		return m, nil

	case "shift+tab":
		if tab := m.activeTabModel(); tab != nil {
			tab.PrevPane()
		}
		return m, nil

	case "alt+1", "alt+2", "alt+3", "alt+4", "alt+5",
		"alt+6", "alt+7", "alt+8", "alt+9":
		idx := int(key[len(key)-1] - '1')
		if idx < len(m.tabs) {
			m.activeTab = idx
		}
		return m, nil

	default:
		// Forward keystrokes to active pane
		return m, m.forwardInput(msg)
	}
}

func (m *Model) handlePaneOutput(msg PaneOutputMsg) {
	for _, tab := range m.tabs {
		for _, pane := range tab.Panes {
			if pane.ID == msg.PaneID {
				pane.AppendOutput(msg.Data)
				return
			}
		}
	}
}

func (m *Model) applyWorkspaceState(state WorkspaceStateMsg) {
	m.tabs = nil
	paneMap := make(map[string]*PaneInfo)
	for i := range state.Panes {
		paneMap[state.Panes[i].ID] = &state.Panes[i]
	}

	for _, tabInfo := range state.Tabs {
		tab := NewTabModel(tabInfo.ID, tabInfo.Name)
		for _, paneID := range tabInfo.Panes {
			pane := NewPaneModel(paneID)
			if info, ok := paneMap[paneID]; ok {
				pane.Name = info.CWD
			}
			tab.AddPane(pane)
		}
		if len(tab.Panes) > 0 {
			tab.Panes[0].Active = true
		}
		m.tabs = append(m.tabs, tab)
	}

	for i, tab := range m.tabs {
		if tab.ID == state.ActiveTab {
			m.activeTab = i
			break
		}
	}
}

func (m *Model) resizeTabs() {
	tabH := m.height - 2
	for _, tab := range m.tabs {
		tab.Resize(m.width, tabH)
	}
}

func (m Model) activeTabModel() *TabModel {
	if m.activeTab < len(m.tabs) {
		return m.tabs[m.activeTab]
	}
	return nil
}

func (m Model) renderTabBar() string {
	var tabs []string
	for i, tab := range m.tabs {
		style := inactiveTabStyle
		if i == m.activeTab {
			style = activeTabStyle
		}
		tabs = append(tabs, style.Render(tab.Name))
	}
	bar := strings.Join(tabs, " ")
	return lipgloss.NewStyle().Width(m.width).Render(bar)
}

func (m Model) renderStatusBar() string {
	status := "aethel"
	if tab := m.activeTabModel(); tab != nil {
		if pane := tab.ActivePaneModel(); pane != nil {
			status = pane.Name
		}
	}
	return statusBarStyle.Width(m.width).Render(status)
}

// Daemon communication commands

func (m Model) attachToDaemon() tea.Cmd {
	return func() tea.Msg {
		msg, _ := ipc.NewMessage(ipc.MsgAttach, ipc.AttachPayload{
			Cols: m.width,
			Rows: m.height,
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
			return tea.Quit()
		}

		switch msg.Type {
		case ipc.MsgPaneOutput:
			var payload ipc.PaneOutputPayload
			msg.DecodePayload(&payload)
			return PaneOutputMsg{PaneID: payload.PaneID, Data: payload.Data}

		case ipc.MsgWorkspaceState:
			var raw map[string]any
			msg.DecodePayload(&raw)
			return parseWorkspaceState(raw)

		default:
			// Unknown message type — keep listening
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
				if panes, ok := tm["panes"].([]any); ok {
					for _, p := range panes {
						if s, ok := p.(string); ok {
							ti.Panes = append(ti.Panes, s)
						}
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

func (m Model) closeActivePane() tea.Cmd {
	return func() tea.Msg {
		tab := m.activeTabModel()
		if tab == nil {
			return nil
		}
		pane := tab.ActivePaneModel()
		if pane == nil {
			return nil
		}
		msg, _ := ipc.NewMessage(ipc.MsgDestroyPane, ipc.DestroyPanePayload{
			PaneID: pane.ID,
		})
		m.client.Send(msg)
		return nil
	}
}

func (m Model) splitPane(dir SplitDir) tea.Cmd {
	return func() tea.Msg {
		tab := m.activeTabModel()
		if tab == nil {
			return nil
		}
		tab.Split = dir
		msg, _ := ipc.NewMessage(ipc.MsgCreatePane, ipc.CreatePanePayload{
			TabID: tab.ID,
		})
		m.client.Send(msg)
		return nil
	}
}

func (m Model) forwardInput(keyMsg tea.KeyMsg) tea.Cmd {
	return func() tea.Msg {
		tab := m.activeTabModel()
		if tab == nil {
			return nil
		}
		pane := tab.ActivePaneModel()
		if pane == nil {
			return nil
		}

		data := keyToBytes(keyMsg)
		if data == nil {
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

func keyToBytes(keyMsg tea.KeyMsg) []byte {
	s := keyMsg.String()

	switch s {
	case "enter":
		return []byte("\r")
	case "backspace":
		return []byte{0x7f}
	case "space":
		return []byte(" ")
	case "escape":
		return []byte{0x1b}
	case "up":
		return []byte("\x1b[A")
	case "down":
		return []byte("\x1b[B")
	case "right":
		return []byte("\x1b[C")
	case "left":
		return []byte("\x1b[D")
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

	// Single printable character
	if len(s) == 1 {
		return []byte(s)
	}

	return nil
}

func (m Model) resizeAllPanes() tea.Cmd {
	return func() tea.Msg {
		tab := m.activeTabModel()
		if tab == nil {
			return nil
		}
		for _, pane := range tab.Panes {
			cols := pane.Width - 2  // subtract border
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
		return nil
	}
}
