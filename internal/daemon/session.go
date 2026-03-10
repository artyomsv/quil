package daemon

import (
	"fmt"
	"sync"

	"github.com/google/uuid"
	apty "github.com/stukans/aethel/internal/pty"
)

type Tab struct {
	ID    string
	Name  string
	Panes []string // Pane IDs in order
}

type Pane struct {
	ID    string
	TabID string
	CWD   string
	PTY   apty.Session
}

type SessionManager struct {
	tabs      map[string]*Tab
	tabOrder  []string
	panes     map[string]*Pane
	activeTab string
	mu        sync.RWMutex
}

func NewSessionManager() *SessionManager {
	return &SessionManager{
		tabs:  make(map[string]*Tab),
		panes: make(map[string]*Pane),
	}
}

func (sm *SessionManager) CreateTab(name string) *Tab {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	id := "tab-" + uuid.New().String()[:8]
	tab := &Tab{ID: id, Name: name}
	sm.tabs[id] = tab
	sm.tabOrder = append(sm.tabOrder, id)

	if sm.activeTab == "" {
		sm.activeTab = id
	}
	return tab
}

func (sm *SessionManager) DestroyTab(tabID string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	tab, ok := sm.tabs[tabID]
	if !ok {
		return fmt.Errorf("tab not found: %s", tabID)
	}

	for _, paneID := range tab.Panes {
		if pane, ok := sm.panes[paneID]; ok {
			if pane.PTY != nil {
				pane.PTY.Close()
			}
			delete(sm.panes, paneID)
		}
	}

	delete(sm.tabs, tabID)
	for i, id := range sm.tabOrder {
		if id == tabID {
			sm.tabOrder = append(sm.tabOrder[:i], sm.tabOrder[i+1:]...)
			break
		}
	}

	if sm.activeTab == tabID {
		if len(sm.tabOrder) > 0 {
			sm.activeTab = sm.tabOrder[0]
		} else {
			sm.activeTab = ""
		}
	}
	return nil
}

func (sm *SessionManager) CreatePane(tabID string, cwd string) (*Pane, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	tab, ok := sm.tabs[tabID]
	if !ok {
		return nil, fmt.Errorf("tab not found: %s", tabID)
	}

	id := "pane-" + uuid.New().String()[:8]
	pane := &Pane{
		ID:    id,
		TabID: tabID,
		CWD:   cwd,
	}

	sm.panes[id] = pane
	tab.Panes = append(tab.Panes, id)
	return pane, nil
}

func (sm *SessionManager) DestroyPane(paneID string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	pane, ok := sm.panes[paneID]
	if !ok {
		return fmt.Errorf("pane not found: %s", paneID)
	}

	if pane.PTY != nil {
		pane.PTY.Close()
	}

	if tab, ok := sm.tabs[pane.TabID]; ok {
		for i, id := range tab.Panes {
			if id == paneID {
				tab.Panes = append(tab.Panes[:i], tab.Panes[i+1:]...)
				break
			}
		}
	}

	delete(sm.panes, paneID)
	return nil
}

func (sm *SessionManager) Tabs() []*Tab {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	tabs := make([]*Tab, 0, len(sm.tabOrder))
	for _, id := range sm.tabOrder {
		tabs = append(tabs, sm.tabs[id])
	}
	return tabs
}

func (sm *SessionManager) Tab(id string) *Tab {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.tabs[id]
}

func (sm *SessionManager) Panes(tabID string) []*Pane {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	tab, ok := sm.tabs[tabID]
	if !ok {
		return nil
	}

	panes := make([]*Pane, 0, len(tab.Panes))
	for _, id := range tab.Panes {
		if pane, ok := sm.panes[id]; ok {
			panes = append(panes, pane)
		}
	}
	return panes
}

func (sm *SessionManager) Pane(id string) *Pane {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.panes[id]
}

func (sm *SessionManager) ActiveTabID() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.activeTab
}

func (sm *SessionManager) SwitchTab(tabID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if _, ok := sm.tabs[tabID]; ok {
		sm.activeTab = tabID
	}
}
