package daemon

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	apty "github.com/artyomsv/aethel/internal/pty"
	"github.com/artyomsv/aethel/internal/ringbuf"
)

type Tab struct {
	ID     string
	Name   string
	Color  string
	Panes  []string        // Pane IDs in order
	Layout json.RawMessage // Opaque layout tree from TUI
}

type Pane struct {
	ID           string
	TabID        string
	CWD          string
	Name         string // User-set name (empty = use CWD)
	PTY          apty.Session
	OutputBuf    *ringbuf.RingBuffer // Captures PTY output for replay on reconnect
	GhostSnap    []byte              // Pure disk-loaded ghost buffer, cleared after first client replay
	Type         string              // Plugin name (default: "terminal")
	PluginState  map[string]string   // Scraped values (e.g., "session_id": "abc123")
	PluginMu     sync.Mutex          // Protects PluginState from concurrent access
	InstanceName string              // Which instance config was used
	InstanceArgs []string            // Args used to start (for rerun strategy)
	ExitCode     *int                // nil = still running, non-nil = exited
	ExitedAt     time.Time           // When the process exited (zero if running)
	Cols         int                 // Last known terminal width (0 = unknown)
	Rows         int                 // Last known terminal height (0 = unknown)
	LastOutputAt    time.Time        // Updated on every flushPaneOutput
	IdleNotified    bool             // Prevents re-firing for same idle period
	LastIdleEventAt time.Time        // Cooldown: last time an idle event was emitted
	LastBellEventAt time.Time        // Cooldown: last time a bell event was emitted
}

type SessionManager struct {
	tabs      map[string]*Tab
	tabOrder  []string
	panes     map[string]*Pane
	activeTab string
	bufSize   int // ring buffer capacity per pane (bytes)
	mu        sync.RWMutex
}

func NewSessionManager(bufSize int) *SessionManager {
	return &SessionManager{
		tabs:    make(map[string]*Tab),
		panes:   make(map[string]*Pane),
		bufSize: bufSize,
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
		ID:        id,
		TabID:     tabID,
		CWD:       cwd,
		OutputBuf: ringbuf.NewRingBuffer(sm.bufSize),
	}

	sm.panes[id] = pane
	tab.Panes = append(tab.Panes, id)
	return pane, nil
}

// NewPane creates a Pane object with a unique ID and ring buffer, but does
// NOT add it to any tab. Use with ReplacePane for atomic swaps.
func (sm *SessionManager) NewPane(cwd string) *Pane {
	id := "pane-" + uuid.New().String()[:8]
	return &Pane{
		ID:        id,
		CWD:       cwd,
		OutputBuf: ringbuf.NewRingBuffer(sm.bufSize),
	}
}

// ReplacePane atomically swaps an old pane for a new one at the same
// position in the tab's pane list. The old pane's PTY is closed.
func (sm *SessionManager) ReplacePane(oldPaneID string, newPane *Pane) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	oldPane, ok := sm.panes[oldPaneID]
	if !ok {
		return fmt.Errorf("pane not found: %s", oldPaneID)
	}

	if oldPane.PTY != nil {
		oldPane.PTY.Close()
	}

	// Replace in tab's pane list at the same index
	if tab, ok := sm.tabs[oldPane.TabID]; ok {
		for i, id := range tab.Panes {
			if id == oldPaneID {
				tab.Panes[i] = newPane.ID
				break
			}
		}
	}

	newPane.TabID = oldPane.TabID
	delete(sm.panes, oldPaneID)
	sm.panes[newPane.ID] = newPane
	return nil
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

// RestoreTab inserts a pre-built tab and its panes into the session.
// Used during workspace restore from disk. All insertions happen under
// a single lock hold to prevent orphaned panes.
func (sm *SessionManager) RestoreTab(tab *Tab, panes []*Pane) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.tabs[tab.ID] = tab
	sm.tabOrder = append(sm.tabOrder, tab.ID)

	for _, pane := range panes {
		sm.panes[pane.ID] = pane
	}
}

// SnapshotState returns a consistent view of the entire session state under
// a single RLock hold. This prevents torn reads when tabs/panes are
// created or destroyed concurrently.
func (sm *SessionManager) SnapshotState() (activeTab string, tabs []*Tab, panesByTab map[string][]*Pane) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	activeTab = sm.activeTab
	tabs = make([]*Tab, 0, len(sm.tabOrder))
	panesByTab = make(map[string][]*Pane)

	for _, id := range sm.tabOrder {
		tab := sm.tabs[id]
		tabs = append(tabs, tab)

		tabPanes := make([]*Pane, 0, len(tab.Panes))
		for _, pid := range tab.Panes {
			if pane, ok := sm.panes[pid]; ok {
				tabPanes = append(tabPanes, pane)
			}
		}
		panesByTab[id] = tabPanes
	}
	return
}
