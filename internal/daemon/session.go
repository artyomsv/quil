package daemon

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/artyomsv/quil/internal/logger"
	memreport "github.com/artyomsv/quil/internal/memreport"
	apty "github.com/artyomsv/quil/internal/pty"
	"github.com/artyomsv/quil/internal/ringbuf"
	"github.com/google/uuid"
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
	HistoryLines int                 // Ghost-buffer line count, snapshotted at restore (immutable after; broadcast-only restore-checklist hint)
	Type         string              // Plugin name (default: "terminal")
	PluginState  map[string]string   // Scraped values (e.g., "session_id": "abc123")
	// PluginMu protects every mutable field that can be read or written
	// concurrently with the daemon's PTY-output goroutine: PluginState,
	// GhostSnap, PTY (the pointer itself + Pid lookups), ExitCode, and
	// ExitedAt. Type and CWD also join this set: the lazy-restore path
	// (spawnRestoredPane via ensurePaneSpawned) rewrites them on its error
	// branches (CWD="" when the saved dir is gone, Type="terminal" on spawn
	// fallback) WHILE the IPC server is live, racing snapshot() /
	// workspaceStateFromSnapshot / buildPaneInfos / handlePaneStatusReq
	// readers. Immutable post-creation fields (ID, TabID, OutputBuf pointer,
	// Cols/Rows once set) are read without it.
	PluginMu        sync.Mutex
	InstanceName    string    // Which instance config was used
	InstanceArgs    []string  // Args used to start (for rerun strategy)
	ExitCode        *int      // nil = still running, non-nil = exited
	ExitedAt        time.Time // When the process exited (zero if running)
	Cols            int       // Last known terminal width (0 = unknown)
	Rows            int       // Last known terminal height (0 = unknown)
	LastOutputAt    time.Time // Updated on every flushPaneOutput
	IdleNotified    bool      // Prevents re-firing for same idle period
	LastIdleEventAt time.Time // Cooldown: last time a idle event was emitted
	LastBellEventAt time.Time // Cooldown: last time a bell event was emitted
	// LastInputBlockedAt: cooldown for the input_blocked event emitted when
	// the input queue overflows (child stopped reading stdin). Under PluginMu.
	LastInputBlockedAt time.Time
	// Input pipeline: all PTY stdin writes go through a dedicated per-pane
	// goroutine (inputWriter). A child that stops reading its stdin fills
	// the kernel PTY buffer and makes Write block forever; on the IPC
	// dispatch goroutine that froze input for EVERY pane (the 2026-06-11/12
	// production wedge). The writer goroutine parks harmlessly instead, and
	// EnqueueInput never blocks. Channels are created lazily by
	// EnsureInputWriter; StopInput ends the writer on pane teardown.
	inputCh       chan []byte
	inputDone     chan struct{}
	inputOnce     sync.Once
	inputStopOnce sync.Once
	// Muted suppresses notification events sourced from this pane. Set via
	// MsgUpdatePane{Muted: true} from the TUI (default keybinding Alt+M).
	// Persisted in the workspace snapshot so mute survives restart. Read
	// under PluginMu in emitEvent.
	Muted bool
	// Eager, when true, makes this pane respawn immediately on daemon restart
	// instead of being deferred until first access. Toggled via
	// MsgUpdatePane{Eager: true} (default keybinding Alt+Shift+E), persisted in
	// the workspace snapshot, and marked on the tab label. Read under PluginMu.
	Eager bool
	// Overlay marks an ephemeral TUI overlay pane (lazygit toggle view).
	// Guarded by PluginMu like Muted (set in handleCreatePane after the
	// pane is already published to the session maps; concurrent snapshots
	// may read it). Excluded from disk snapshots.
	Overlay bool
	// MouseModes mirrors the child app's DEC mouse-mode state, scanned from the
	// PTY output stream (scanMouseModes) in flushPaneOutput. The daemon is the
	// only component that sees the one-time mouse-enable burst on every attach,
	// so it is authoritative. Broadcast (not persisted) in the workspace snapshot
	// so the TUI can forward wheel events to apps that handle their own
	// scrolling. mouseBroadcast / lastMouseBroadcastAt are throttle bookkeeping:
	// broadcasts are gated by a cooldown so a hostile PTY stream that alternates
	// a mode every flush cannot force a full-snapshot broadcast storm (the
	// suppressed change is re-delivered on the next flush past the cooldown, or
	// by any other broadcastState caller). All three guarded by PluginMu.
	MouseModes           mouseModeState
	mouseBroadcast       mouseModeState
	lastMouseBroadcastAt time.Time
	// Pending is true between restore and first spawn for a deferred pane: the
	// model + ghost buffer exist but no PTY has been created yet. Runtime-only,
	// never persisted. Cleared by ensurePaneSpawned.
	Pending bool
	// spawnMu serializes the lazy-spawn idempotency guard in ensurePaneSpawned.
	// It is deliberately SEPARATE from PluginMu: spawnPane locks PluginMu
	// synchronously on the calling goroutine (preassign_id + PluginState init),
	// so reusing PluginMu here would self-deadlock (Go mutexes are non-reentrant).
	// spawnMu is held only across the spawn decision + spawnRestoredPane call, so
	// two callers (tab switch + MCP op) racing the same Pending pane spawn it
	// exactly once.
	spawnMu sync.Mutex
	// LastHookEventAt is the wall-clock time of the most recent hook event
	// the daemon translated into a PaneEvent for this pane. Used by
	// checkIdlePanes to skip the legacy idle excerpt heuristic when hook
	// events are actively flowing (the AI tool itself is the ground truth
	// for what "idle" means once hooks are wired up).
	LastHookEventAt time.Time
	// HookHealthy flips true the first time a hook event is received for
	// this pane. Provides the legacy-idle fallback: panes whose hooks
	// never load (plugin throws at module init, settings JSON malformed,
	// etc.) remain HookHealthy=false and the idle checker stays active —
	// the user always sees SOME notification surface, even if not the
	// hook-driven one.
	HookHealthy bool
}

type SessionManager struct {
	tabs      map[string]*Tab
	tabOrder  []string
	panes     map[string]*Pane
	activeTab string
	bufSize   int // ring buffer capacity per pane (bytes)
	mu        sync.RWMutex
}

// inputQueueSize bounds the per-pane stdin queue. Generous for interactive
// typing and paste bursts; only a child that has stopped draining stdin can
// fill it, at which point further input is dropped (with a sidebar event)
// instead of blocking the daemon.
const inputQueueSize = 256

// EnsureInputWriter lazily starts the pane's dedicated PTY input goroutine.
func (p *Pane) EnsureInputWriter() {
	p.inputOnce.Do(func() {
		p.inputCh = make(chan []byte, inputQueueSize)
		p.inputDone = make(chan struct{})
		go p.inputWriter()
	})
}

// EnqueueInput hands data to the input writer without ever blocking.
// Returns false when the queue is full — the child is not reading stdin
// and the caller decides how to surface the drop.
func (p *Pane) EnqueueInput(data []byte) bool {
	p.EnsureInputWriter()
	select {
	case p.inputCh <- data:
		return true
	default:
		return false
	}
}

// StopInput terminates the input writer. Idempotent; safe to call even if
// the writer never started.
func (p *Pane) StopInput() {
	p.EnsureInputWriter()
	p.inputStopOnce.Do(func() { close(p.inputDone) })
}

func (p *Pane) inputWriter() {
	for {
		select {
		case <-p.inputDone:
			return
		case data := <-p.inputCh:
			p.PluginMu.Lock()
			pty := p.PTY
			p.PluginMu.Unlock()
			if pty == nil {
				continue
			}
			// May block until the child reads or the PTY is closed — both
			// are fine here, on the pane's own goroutine. A close while
			// blocked errors the Write, and the next loop sees inputDone.
			if _, err := pty.Write(data); err != nil {
				logger.Debug("pane %s: input write: %v", p.ID, err)
			}
		}
	}
}

// releasePanes tears down pane PTYs OFF the session lock, each on its own
// goroutine. Close → cmd.Wait blocks until the child is reaped; doing that
// under sm.mu's write lock starved every reader (snapshot loop, attach,
// tab switch, hook enrichment) when a wedged child refused to die — the
// 2026-06-11/12 daemon wedge. Async close keeps even the calling handler
// responsive.
func releasePanes(panes []*Pane) {
	for _, p := range panes {
		p.StopInput()
		p.PluginMu.Lock()
		pty := p.PTY
		p.PluginMu.Unlock()
		if pty == nil {
			continue
		}
		go func(id string, s apty.Session) {
			if err := s.Close(); err != nil {
				logger.Debug("pane %s: PTY close: %v", id, err)
			}
		}(p.ID, pty)
	}
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

	tab, ok := sm.tabs[tabID]
	if !ok {
		sm.mu.Unlock()
		return fmt.Errorf("tab not found: %s", tabID)
	}

	// Detach panes under the lock; close their PTYs after releasing it
	// (releasePanes) — Close can block on reaping a wedged child.
	var orphans []*Pane
	for _, paneID := range tab.Panes {
		if pane, ok := sm.panes[paneID]; ok {
			orphans = append(orphans, pane)
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
	sm.mu.Unlock()
	releasePanes(orphans)
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

	oldPane, ok := sm.panes[oldPaneID]
	if !ok {
		sm.mu.Unlock()
		return fmt.Errorf("pane not found: %s", oldPaneID)
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
	sm.mu.Unlock()
	releasePanes([]*Pane{oldPane})
	return nil
}

func (sm *SessionManager) DestroyPane(paneID string) error {
	sm.mu.Lock()

	pane, ok := sm.panes[paneID]
	if !ok {
		sm.mu.Unlock()
		return fmt.Errorf("pane not found: %s", paneID)
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
	sm.mu.Unlock()
	releasePanes([]*Pane{pane})
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

// ReorderTab moves the tab with tabID to the given ordinal newIdx in the
// session's tabOrder. newIdx is clamped to [0, len(tabOrder)-1]; out-of-
// range values silently snap to the nearest valid slot rather than
// erroring, so a stale TUI doesn't have to race the daemon for an
// authoritative tab count.
//
// Returns true when the order actually changed (caller decides whether to
// snapshot/broadcast).
func (sm *SessionManager) ReorderTab(tabID string, newIdx int) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if _, ok := sm.tabs[tabID]; !ok {
		return false
	}
	from := -1
	for i, id := range sm.tabOrder {
		if id == tabID {
			from = i
			break
		}
	}
	if from < 0 {
		return false
	}
	if newIdx < 0 {
		newIdx = 0
	}
	if newIdx >= len(sm.tabOrder) {
		newIdx = len(sm.tabOrder) - 1
	}
	if from == newIdx {
		return false
	}
	// Slide the slice without allocating: pull tabID out, shift the gap
	// across the affected range, drop tabID into newIdx.
	if from < newIdx {
		copy(sm.tabOrder[from:newIdx], sm.tabOrder[from+1:newIdx+1])
	} else {
		copy(sm.tabOrder[newIdx+1:from+1], sm.tabOrder[newIdx:from])
	}
	sm.tabOrder[newIdx] = tabID
	return true
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

// paneSourceAdapter adapts *Pane to the memreport.PaneSource interface
// so the collector can read pane memory without importing daemon.
type paneSourceAdapter struct{ p *Pane }

// Snapshot fills a PaneSourceSnapshot under a single PluginMu acquisition
// so the GoHeap / PID / Alive trio is layer-consistent. ID, TabID, and the
// OutputBuf pointer are immutable after pane creation, so they are read
// outside the lock. OutputBuf.Len() is safe outside PluginMu because the
// ringbuf has its own internal mutex protecting its length.
func (a paneSourceAdapter) Snapshot() memreport.PaneSourceSnapshot {
	s := memreport.PaneSourceSnapshot{
		PaneID: a.p.ID,
		TabID:  a.p.TabID,
	}
	if a.p.OutputBuf != nil {
		s.HeapBytes += uint64(a.p.OutputBuf.Len())
	}
	a.p.PluginMu.Lock()
	defer a.p.PluginMu.Unlock()
	s.HeapBytes += uint64(len(a.p.GhostSnap))
	for k, v := range a.p.PluginState {
		s.HeapBytes += uint64(len(k) + len(v))
	}
	if a.p.PTY != nil {
		s.PID = a.p.PTY.Pid()
	}
	s.Alive = a.p.ExitCode == nil
	return s
}

// PaneSources returns an adapter per live pane. Implements
// memreport.PaneLister. Callers must not retain the returned slice beyond
// a single collection cycle.
func (sm *SessionManager) PaneSources() []memreport.PaneSource {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	out := make([]memreport.PaneSource, 0, len(sm.panes))
	for _, p := range sm.panes {
		out = append(out, paneSourceAdapter{p: p})
	}
	return out
}
