package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"regexp"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/google/uuid"
	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/persist"
	"github.com/artyomsv/quil/internal/plugin"
	apty "github.com/artyomsv/quil/internal/pty"
	"github.com/artyomsv/quil/internal/ringbuf"
	"github.com/artyomsv/quil/internal/shellinit"
)

// oscBellRe matches OSC sequences terminated by BEL (\x07), e.g., \x1b]0;title\x07.
// Used to strip these before bell detection so OSC terminators aren't treated as bells.
var oscBellRe = regexp.MustCompile(`\x1b\][^\x07]*\x07`)

type Daemon struct {
	cfg        config.Config
	server     *ipc.Server
	session    *SessionManager
	registry   *plugin.Registry
	shutdown     chan struct{}
	shutdownOnce sync.Once
	snapshotCh   chan struct{} // buffered channel for snapshot requests
	restored     bool         // true if workspace was loaded from disk
	events       *eventQueue  // notification center event queue
}

func New(cfg config.Config) *Daemon {
	// Buffer size: MaxLines * 512 bytes per line (generous for ANSI-rich output)
	bufSize := cfg.GhostBuffer.MaxLines * 512
	if bufSize <= 0 {
		bufSize = 500 * 512 // 256KB default
	}

	reg := plugin.NewRegistry()

	maxEvents := cfg.Notification.MaxEvents
	if maxEvents <= 0 {
		maxEvents = 50
	}

	return &Daemon{
		cfg:        cfg,
		session:    NewSessionManager(bufSize),
		registry:   reg,
		shutdown:   make(chan struct{}),
		snapshotCh: make(chan struct{}, 1),
		events:     newEventQueue(maxEvents),
	}
}

func (d *Daemon) Start() error {
	quilDir := config.QuilDir()
	if err := os.MkdirAll(quilDir, 0700); err != nil {
		return fmt.Errorf("create quil dir: %w", err)
	}

	if err := shellinit.EnsureInitDir(quilDir); err != nil {
		log.Printf("warning: failed to write shell init scripts: %v", err)
	}

	// Write default plugin TOML files if missing, then load all plugins
	if err := plugin.EnsureDefaultPlugins(config.PluginsDir()); err != nil {
		log.Printf("warning: failed to write default plugins: %v", err)
	}
	if err := d.registry.LoadFromDir(config.PluginsDir()); err != nil {
		log.Printf("warning: failed to load plugins: %v", err)
	}
	d.registry.DetectAvailability()

	// Restore workspace from disk if available
	if err := d.restoreWorkspace(); err != nil {
		log.Printf("warning: failed to restore workspace: %v", err)
	}

	sockPath := config.SocketPath()
	d.server = ipc.NewServer(sockPath, d.handleMessage, func(conn *ipc.Conn) {
		d.requestSnapshot()
		d.events.RemoveWatchersByConn(conn)
	})

	if err := d.server.Start(); err != nil {
		return fmt.Errorf("start IPC server: %w", err)
	}

	// Respawn panes for restored workspace
	if d.restored {
		d.respawnPanes()
	}

	go d.idleChecker()

	log.Printf("quild started, listening on %s", sockPath)
	return nil
}

func (d *Daemon) Wait() {
	// Periodic snapshot timer
	interval, err := time.ParseDuration(d.cfg.Daemon.SnapshotInterval)
	if err != nil {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Debounce timer for event-driven snapshot requests
	var debounceTimer *time.Timer
	debounceCh := make(chan struct{}, 1)

	for {
		select {
		case <-sigCh:
			log.Println("shutting down (signal)...")
			d.Stop()
			return
		case <-d.shutdown:
			log.Println("shutting down (IPC)...")
			d.Stop()
			return
		case <-ticker.C:
			d.snapshot()
		case <-d.snapshotCh:
			// Debounce: collapse rapid requests into one snapshot after 500ms
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			debounceTimer = time.AfterFunc(500*time.Millisecond, func() {
				select {
				case debounceCh <- struct{}{}:
				default:
				}
			})
		case <-debounceCh:
			d.snapshot()
		}
	}
}

func (d *Daemon) Stop() {
	log.Print("daemon stopping, writing final snapshot...")
	// Final snapshot before shutdown
	d.snapshot()

	if d.server != nil {
		d.server.Stop()
	}
	for _, tab := range d.session.Tabs() {
		for _, pane := range d.session.Panes(tab.ID) {
			if pane.PTY != nil {
				pane.PTY.Close()
			}
		}
	}
}

// requestSnapshot sends a non-blocking snapshot request to the event loop.
// The event loop debounces multiple requests and executes one snapshot.
func (d *Daemon) requestSnapshot() {
	select {
	case d.snapshotCh <- struct{}{}:
	default: // already pending
	}
}

// snapshot persists workspace state and ghost buffers to disk.
func (d *Daemon) snapshot() {
	start := time.Now()
	state := d.buildWorkspaceState()

	if err := persist.Save(config.WorkspacePath(), state); err != nil {
		log.Printf("snapshot workspace: %v", err)
	}

	// Flush ghost buffers using consistent snapshot
	_, tabs, panesByTab := d.session.SnapshotState()
	bufDir := config.BufferDir()
	buffers := make(map[string][]byte)
	var activePaneIDs []string
	var totalBytes int

	for _, tab := range tabs {
		for _, pane := range panesByTab[tab.ID] {
			activePaneIDs = append(activePaneIDs, pane.ID)
			// Skip ghost buffer save for plugins with GhostBuffer disabled
			if p := d.registry.Get(pane.Type); p != nil && !p.Persistence.GhostBuffer {
				continue
			}
			if pane.OutputBuf != nil {
				if data := pane.OutputBuf.Bytes(); len(data) > 0 {
					buffers[pane.ID] = data
					totalBytes += len(data)
				}
			}
		}
	}

	if err := persist.SaveAllBuffers(bufDir, buffers); err != nil {
		log.Printf("snapshot buffers: %v", err)
	}
	if err := persist.CleanBuffers(bufDir, activePaneIDs); err != nil {
		log.Printf("clean buffers: %v", err)
	}

	log.Printf("snapshot: %d tabs, %d panes, %d buffers (%d bytes), took %v",
		len(tabs), len(activePaneIDs), len(buffers), totalBytes, time.Since(start).Round(time.Millisecond))
}

// restoreWorkspace loads workspace state from disk.
func (d *Daemon) restoreWorkspace() error {
	wsPath := config.WorkspacePath()
	state, err := persist.Load(wsPath)
	if err != nil {
		return err
	}
	if state == nil {
		return nil // Fresh workspace
	}

	log.Println("restoring workspace from disk...")

	activeTab, _ := state["active_tab"].(string)
	tabs, _ := state["tabs"].([]any)
	panes, _ := state["panes"].([]any)

	// Build pane lookup
	panesByID := make(map[string]map[string]any, len(panes))
	for _, p := range panes {
		paneMap, ok := p.(map[string]any)
		if !ok {
			continue
		}
		id, _ := paneMap["id"].(string)
		if id != "" {
			panesByID[id] = paneMap
		}
	}

	bufDir := config.BufferDir()
	restoredPanes := 0

	// Restore tabs and panes
	for _, t := range tabs {
		tabMap, ok := t.(map[string]any)
		if !ok {
			continue
		}
		tabID, _ := tabMap["id"].(string)
		tabName, _ := tabMap["name"].(string)
		tabColor, _ := tabMap["color"].(string)
		if !isValidHexID(tabID, "tab-") {
			log.Printf("restore: skipping invalid tab ID: %q", tabID)
			continue
		}

		tab := &Tab{
			ID:    tabID,
			Name:  tabName,
			Color: tabColor,
		}

		// Restore layout
		if layoutRaw, ok := tabMap["layout"]; ok {
			layoutBytes, err := json.Marshal(layoutRaw)
			if err == nil {
				tab.Layout = json.RawMessage(layoutBytes)
			}
		}

		// Restore panes for this tab
		var tabPanes []*Pane
		if paneIDs, ok := tabMap["panes"].([]any); ok {
			for _, pid := range paneIDs {
				paneID, _ := pid.(string)
				if !isValidHexID(paneID, "pane-") {
					log.Printf("restore: skipping invalid pane ID: %q", paneID)
					continue
				}
				tab.Panes = append(tab.Panes, paneID)

				// Create pane object (nil-safe lookup)
				paneData := panesByID[paneID]
				if paneData == nil {
					paneData = map[string]any{}
				}
				cwd, _ := paneData["cwd"].(string)
				name, _ := paneData["name"].(string)
				paneType, _ := paneData["type"].(string)
				if paneType == "" {
					paneType = "terminal" // backward compatible
				}
				instanceName, _ := paneData["instance_name"].(string)

				// Restore plugin state
				var pluginState map[string]string
				if ps, ok := paneData["plugin_state"].(map[string]any); ok {
					pluginState = make(map[string]string, len(ps))
					for k, v := range ps {
						if s, ok := v.(string); ok {
							pluginState[k] = s
						}
					}
				}

				// Restore instance args
				var instanceArgs []string
				if ia, ok := paneData["instance_args"].([]any); ok {
					for _, a := range ia {
						if s, ok := a.(string); ok {
							instanceArgs = append(instanceArgs, s)
						}
					}
				}

				pane := &Pane{
					ID:           paneID,
					TabID:        tabID,
					CWD:          cwd,
					Name:         name,
					Type:         paneType,
					PluginState:  pluginState,
					InstanceName: instanceName,
					InstanceArgs: instanceArgs,
					OutputBuf:    ringbuf.NewRingBuffer(d.session.bufSize),
				}

				// Load ghost buffer from disk
				if bufData, err := persist.LoadBuffer(bufDir, paneID); err == nil && len(bufData) > 0 {
					pane.OutputBuf.Write(bufData)
					pane.GhostSnap = make([]byte, len(bufData))
					copy(pane.GhostSnap, bufData)
					log.Printf("restore: loaded ghost buffer %s (%d bytes)", paneID, len(bufData))
				} else if err != nil {
					log.Printf("restore: ghost buffer load error %s: %v", paneID, err)
				}

				tabPanes = append(tabPanes, pane)
			}
		}

		// Insert tab and all its panes under a single lock hold
		d.session.RestoreTab(tab, tabPanes)
		restoredPanes += len(tabPanes)
	}

	if activeTab != "" {
		d.session.SwitchTab(activeTab)
	}

	d.restored = true
	log.Printf("restored %d tabs, %d panes from disk", len(tabs), restoredPanes)
	return nil
}

// isValidHexID checks that an ID matches the format prefix + 8 hex chars (e.g. "pane-a1b2c3d4").
func isValidHexID(id, prefix string) bool {
	if len(id) != len(prefix)+8 {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		if id[i] != prefix[i] {
			return false
		}
	}
	for i := len(prefix); i < len(id); i++ {
		c := id[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// respawnPanes starts a process in each pane that was restored from disk,
// dispatching to the appropriate resume strategy based on plugin type.
func (d *Daemon) respawnPanes() {
	for _, tab := range d.session.Tabs() {
		for _, pane := range d.session.Panes(tab.ID) {
			if pane.PTY != nil {
				continue // Already has a PTY
			}

			ptySession := apty.New()
			if pane.CWD != "" {
				if info, err := os.Stat(pane.CWD); err != nil || !info.IsDir() {
					log.Printf("pane %s: saved cwd %q gone, using default", pane.ID, pane.CWD)
					pane.CWD = ""
				} else {
					ptySession.SetCWD(pane.CWD)
				}
			}

			if err := d.spawnPane(pane, ptySession, true); err != nil {
				log.Printf("respawn pane %s (type=%s): %v — falling back to terminal", pane.ID, pane.Type, err)
				pane.Type = "terminal"
				ptySession2 := apty.New()
				if pane.CWD != "" {
					ptySession2.SetCWD(pane.CWD)
				}
				if err := d.spawnPane(pane, ptySession2, false); err != nil {
					log.Printf("fallback shell for pane %s also failed: %v", pane.ID, err)
				}
			} else {
				log.Printf("respawned pane %s (type=%s, cwd=%s)", pane.ID, pane.Type, pane.CWD)
			}
		}
	}
}

func (d *Daemon) handleMessage(conn *ipc.Conn, msg *ipc.Message) {
	// Log all IPC messages except high-frequency ones (input, resize, layout)
	switch msg.Type {
	case ipc.MsgPaneInput, ipc.MsgResizePane, ipc.MsgUpdateLayout:
		// skip logging — too noisy
	default:
		log.Printf("ipc recv: %s", msg.Type)
	}

	switch msg.Type {
	case ipc.MsgAttach:
		d.handleAttach(conn, msg)
	case ipc.MsgCreateTab:
		d.handleCreateTab(conn, msg)
	case ipc.MsgDestroyTab:
		d.handleDestroyTab(msg)
	case ipc.MsgSwitchTab:
		d.handleSwitchTab(msg)
	case ipc.MsgUpdateTab:
		d.handleUpdateTab(msg)
	case ipc.MsgCreatePane:
		d.handleCreatePane(msg)
	case ipc.MsgDestroyPane:
		d.handleDestroyPane(msg)
	case ipc.MsgUpdatePane:
		d.handleUpdatePane(msg)
	case ipc.MsgUpdateLayout:
		d.handleUpdateLayout(msg)
	case ipc.MsgPaneInput:
		d.handlePaneInput(msg)
	case ipc.MsgResizePane:
		d.handleResizePane(msg)
	case ipc.MsgReloadPlugins:
		d.handleReloadPlugins()
	case ipc.MsgShutdown:
		d.shutdownOnce.Do(func() { close(d.shutdown) })

	// MCP request-response
	case ipc.MsgListPanesReq:
		d.handleListPanesReq(conn, msg)
	case ipc.MsgReadPaneOutputReq:
		d.handleReadPaneOutputReq(conn, msg)
	case ipc.MsgPaneStatusReq:
		d.handlePaneStatusReq(conn, msg)
	case ipc.MsgCreatePaneReq:
		d.handleCreatePaneReq(conn, msg)
	case ipc.MsgRestartPaneReq:
		d.handleRestartPaneReq(conn, msg)
	case ipc.MsgScreenshotPaneReq:
		d.handleScreenshotPaneReq(conn, msg)
	case ipc.MsgSwitchTabReq:
		d.handleSwitchTabReq(conn, msg)
	case ipc.MsgListTabsReq:
		d.handleListTabsReq(conn, msg)
	case ipc.MsgDestroyPaneReq:
		d.handleDestroyPaneReq(conn, msg)
	case ipc.MsgSetActivePane:
		d.handleSetActivePane(conn, msg)
	case ipc.MsgCloseTUI:
		d.server.Broadcast(msg)

	// Notification center
	case ipc.MsgDismissEvent:
		d.handleDismissEvent(msg)
	case ipc.MsgGetNotificationsReq:
		d.handleGetNotificationsReq(conn, msg)
	case ipc.MsgWatchNotificationsReq:
		d.handleWatchNotificationsReq(conn, msg)
	}
}

func (d *Daemon) handleAttach(conn *ipc.Conn, msg *ipc.Message) {
	var attach ipc.AttachPayload
	if err := msg.DecodePayload(&attach); err != nil {
		log.Printf("handleAttach: decode: %v", err)
		return
	}

	cols, rows := attach.Cols, attach.Rows
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}

	log.Printf("attach: client connected (%dx%d), tabs=%d, restored=%v",
		cols, rows, len(d.session.Tabs()), d.restored)

	// Create default workspace if empty (no tabs — neither fresh nor restored)
	if len(d.session.Tabs()) == 0 {
		log.Print("attach: creating default workspace (no tabs)")
		tab := d.session.CreateTab("Shell")
		cwd, _ := os.Getwd()
		pane, _ := d.session.CreatePane(tab.ID, cwd)
		pane.Type = "terminal"

		ptySession := apty.NewWithSize(cols, rows)
		if err := d.spawnPane(pane, ptySession, false); err != nil {
			log.Printf("failed to start PTY: %v", err)
		}
	}

	state := d.buildWorkspaceState()
	resp, _ := ipc.NewMessage(ipc.MsgWorkspaceState, state)
	conn.Send(resp)

	// Replay buffered output so reconnecting clients see previous terminal content.
	// Skip ghost replay for plugins with GhostBuffer disabled.
	// Prefer GhostSnap (pure disk-loaded data) over OutputBuf on first connect
	// after restore — OutputBuf may be contaminated by respawned shell init output
	// (e.g., ConPTY clear screen sequences) that would wipe the historical content.
	for _, tab := range d.session.Tabs() {
		for _, pane := range d.session.Panes(tab.ID) {
			if pane.OutputBuf == nil {
				continue
			}
			if p := d.registry.Get(pane.Type); p != nil && !p.Persistence.GhostBuffer {
				continue
			}
			ghost := pane.GhostSnap
			source := "ghostsnap"
			if ghost == nil {
				ghost = pane.OutputBuf.Bytes() // reconnect — use full buffer
				source = "outputbuf"
			}
			if len(ghost) == 0 {
				continue
			}
			log.Printf("attach: ghost replay pane %s (type=%s, source=%s, bytes=%d)",
				pane.ID, pane.Type, source, len(ghost))
			histMsg, _ := ipc.NewMessage(ipc.MsgPaneOutput, ipc.PaneOutputPayload{
				PaneID: pane.ID,
				Data:   ghost,
				Ghost:  true,
			})
			conn.Send(histMsg)
			pane.GhostSnap = nil // clear after first replay
		}
	}

	// Replay pending notification events
	for _, e := range d.events.Events() {
		payload := toPaneEventPayload(e)
		evtMsg, _ := ipc.NewMessage(ipc.MsgPaneEvent, payload)
		conn.Send(evtMsg)
	}
}

func (d *Daemon) handleCreateTab(conn *ipc.Conn, msg *ipc.Message) {
	var payload ipc.CreateTabPayload
	if err := msg.DecodePayload(&payload); err != nil {
		return
	}

	tab := d.session.CreateTab(payload.Name)
	d.session.SwitchTab(tab.ID)
	log.Printf("tab created: %s %q", tab.ID, tab.Name)

	// Every tab needs a default pane with a shell
	cwd, _ := os.Getwd()
	pane, _ := d.session.CreatePane(tab.ID, cwd)
	pane.Type = "terminal"

	ptySession := apty.New()
	if err := d.spawnPane(pane, ptySession, false); err != nil {
		log.Printf("failed to start PTY for new tab: %v", err)
	}

	d.broadcastState()
	d.requestSnapshot()
}

func (d *Daemon) handleDestroyTab(msg *ipc.Message) {
	var payload ipc.DestroyTabPayload
	if err := msg.DecodePayload(&payload); err != nil {
		return
	}
	log.Printf("tab destroy: %s", payload.TabID)
	d.session.DestroyTab(payload.TabID)

	// Auto-create replacement if last tab was destroyed
	if len(d.session.Tabs()) == 0 {
		tab := d.session.CreateTab("Shell")
		cwd, _ := os.Getwd()
		pane, _ := d.session.CreatePane(tab.ID, cwd)
		pane.Type = "terminal"
		ptySession := apty.NewWithSize(80, 24)
		if err := d.spawnPane(pane, ptySession, false); err != nil {
			log.Printf("failed to start replacement shell: %v", err)
		}
	}

	d.broadcastState()
	d.requestSnapshot()
}

func (d *Daemon) handleSwitchTab(msg *ipc.Message) {
	var payload ipc.SwitchTabPayload
	if err := msg.DecodePayload(&payload); err != nil {
		return
	}
	log.Printf("tab switch: %s", payload.TabID)
	d.session.SwitchTab(payload.TabID)
	d.broadcastState()
	d.requestSnapshot()
}

func (d *Daemon) handleUpdateTab(msg *ipc.Message) {
	var payload ipc.UpdateTabPayload
	if err := msg.DecodePayload(&payload); err != nil {
		return
	}

	tab := d.session.Tab(payload.TabID)
	if tab == nil {
		return
	}
	if payload.Name != "" {
		tab.Name = payload.Name
	}
	if payload.Color != "" {
		tab.Color = payload.Color
	} else if payload.Name == "" {
		// Only color field sent as empty → clear color
		tab.Color = ""
	}

	d.broadcastState()
}

func (d *Daemon) handleCreatePane(msg *ipc.Message) {
	var payload ipc.CreatePanePayload
	if err := msg.DecodePayload(&payload); err != nil {
		return
	}

	cwd := payload.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}

	// Determine pane type
	paneType := payload.Type
	if paneType == "" {
		paneType = "terminal"
	}

	// Replace mode: atomically swap old pane for new one
	if payload.ReplacePaneID != "" {
		d.handleReplacePane(payload, cwd, paneType)
		return
	}

	pane, err := d.session.CreatePane(payload.TabID, cwd)
	if err != nil {
		log.Printf("create pane error: %v", err)
		return
	}

	pane.Type = paneType
	pane.InstanceName = payload.InstanceName
	pane.InstanceArgs = payload.InstanceArgs
	log.Printf("pane created: %s (type=%s, tab=%s)", pane.ID, paneType, payload.TabID)

	ptySession := apty.New()
	if err := d.spawnPane(pane, ptySession, false); err != nil {
		log.Printf("start PTY error: %v", err)
		return
	}
	d.broadcastState()
	d.requestSnapshot()
}

func (d *Daemon) handleReplacePane(payload ipc.CreatePanePayload, cwd, paneType string) {
	newPane := d.session.NewPane(cwd)
	newPane.Type = paneType
	newPane.InstanceName = payload.InstanceName
	newPane.InstanceArgs = payload.InstanceArgs
	log.Printf("pane replace: %s -> %s (type=%s)", payload.ReplacePaneID, newPane.ID, paneType)

	// Atomically swap old → new in the tab's pane list
	if err := d.session.ReplacePane(payload.ReplacePaneID, newPane); err != nil {
		log.Printf("replace pane: swap error: %v", err)
		return
	}

	ptySession := apty.New()
	if err := d.spawnPane(newPane, ptySession, false); err != nil {
		log.Printf("replace pane: start PTY error: %v, removing dead pane", err)
		d.session.DestroyPane(newPane.ID)
		d.broadcastState()
		d.requestSnapshot()
		return
	}
	d.broadcastState()
	d.requestSnapshot()
}

func (d *Daemon) handleDestroyPane(msg *ipc.Message) {
	var payload ipc.DestroyPanePayload
	if err := msg.DecodePayload(&payload); err != nil {
		return
	}

	// Capture tab ID before destroying the pane
	var tabID string
	if pane := d.session.Pane(payload.PaneID); pane != nil {
		tabID = pane.TabID
	}
	log.Printf("pane destroy: %s (tab=%s)", payload.PaneID, tabID)

	d.session.DestroyPane(payload.PaneID)

	// Auto-create replacement if last pane in tab was destroyed
	if tabID != "" {
		if panes := d.session.Panes(tabID); len(panes) == 0 {
			cwd, _ := os.Getwd()
			if newPane, err := d.session.CreatePane(tabID, cwd); err == nil {
				newPane.Type = "terminal"
				ptySession := apty.New()
				if err := d.spawnPane(newPane, ptySession, false); err != nil {
					log.Printf("failed to start replacement shell: %v", err)
				}
			}
		}
	}

	d.broadcastState()
	d.requestSnapshot()
}

func (d *Daemon) handlePaneInput(msg *ipc.Message) {
	var payload ipc.PaneInputPayload
	if err := msg.DecodePayload(&payload); err != nil {
		return
	}

	pane := d.session.Pane(payload.PaneID)
	if pane == nil || pane.PTY == nil {
		return
	}
	pane.PTY.Write(payload.Data)
}

func (d *Daemon) handleResizePane(msg *ipc.Message) {
	var payload ipc.ResizePanePayload
	if err := msg.DecodePayload(&payload); err != nil {
		return
	}

	pane := d.session.Pane(payload.PaneID)
	if pane == nil || pane.PTY == nil {
		return
	}
	pane.PTY.Resize(payload.Rows, payload.Cols)
	pane.Cols = int(payload.Cols)
	pane.Rows = int(payload.Rows)
}

func (d *Daemon) handleUpdatePane(msg *ipc.Message) {
	var payload ipc.UpdatePanePayload
	if err := msg.DecodePayload(&payload); err != nil {
		return
	}

	pane := d.session.Pane(payload.PaneID)
	if pane == nil {
		return
	}
	if payload.Name != "" {
		pane.Name = payload.Name
	}
	if payload.CWD != "" {
		pane.CWD = payload.CWD
	}
	d.broadcastState()
}

func (d *Daemon) handleReloadPlugins() {
	if err := plugin.EnsureDefaultPlugins(config.PluginsDir()); err != nil {
		log.Printf("reload: ensure defaults: %v", err)
	}
	if err := d.registry.LoadFromDir(config.PluginsDir()); err != nil {
		log.Printf("reload: load plugins: %v", err)
	}
	d.registry.DetectAvailability()
	log.Printf("plugins reloaded")
}

func (d *Daemon) handleUpdateLayout(msg *ipc.Message) {
	var payload ipc.UpdateLayoutPayload
	if err := msg.DecodePayload(&payload); err != nil {
		return
	}

	tab := d.session.Tab(payload.TabID)
	if tab == nil {
		return
	}
	tab.Layout = payload.Layout
	// No broadcastState() — avoids feedback loop.
	// Snapshot ensures layout is persisted to disk.
	d.requestSnapshot()
}

func (d *Daemon) streamPTYOutput(paneID string, pty apty.Session) {
	readBuf := make([]byte, 32*1024)
	dataCh := make(chan []byte, 64)

	// Reader goroutine: continuously reads from PTY
	go func() {
		defer close(dataCh)
		for {
			n, err := pty.Read(readBuf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, readBuf[:n])
				dataCh <- chunk
			}
			if err != nil {
				return
			}
		}
	}()

	// Coalescing loop: accumulates data, flushes on short timer
	const coalesceDelay = 2 * time.Millisecond
	var acc []byte

	flushTimer := time.NewTimer(0)
	if !flushTimer.Stop() {
		<-flushTimer.C
	}

	flush := func() {
		if len(acc) == 0 {
			return
		}
		d.flushPaneOutput(paneID, acc)
		acc = acc[:0]
	}

	for {
		select {
		case chunk, ok := <-dataCh:
			if !ok {
				flush()
				// Capture process exit code (protected by PluginMu to avoid data race)
				if pane := d.session.Pane(paneID); pane != nil {
					code := pty.WaitExit()
					pane.PluginMu.Lock()
					pane.ExitCode = &code
					pane.ExitedAt = time.Now()
					pane.PluginMu.Unlock()
					log.Printf("pane %s: process exited with code %d", paneID, code)

					severity := "info"
					title := "Process exited (code 0)"
					if code != 0 {
						severity = "error"
						title = fmt.Sprintf("Process failed (code %d)", code)
					}
					d.emitEvent(PaneEvent{
						ID:        uuid.New().String(),
						PaneID:    paneID,
						TabID:     pane.TabID,
						PaneName:  pane.Name,
						Type:      "process_exit",
						Title:     title,
						Severity:  severity,
						Timestamp: time.Now(),
						Data:      map[string]string{"exit_code": strconv.Itoa(code)},
					})
				}
				return
			}
			acc = append(acc, chunk...)
			if !flushTimer.Stop() {
				select {
				case <-flushTimer.C:
				default:
				}
			}
			flushTimer.Reset(coalesceDelay)

		case <-flushTimer.C:
			flush()
		}
	}
}

func (d *Daemon) flushPaneOutput(paneID string, data []byte) {
	pane := d.session.Pane(paneID)
	if pane == nil {
		return
	}
	if pane.OutputBuf != nil {
		pane.OutputBuf.Write(data)
	}

	// Update idle tracking (guarded by PluginMu for goroutine safety)
	pane.PluginMu.Lock()
	pane.LastOutputAt = time.Now()
	pane.IdleNotified = false
	pane.PluginMu.Unlock()

	d.detectBellEvent(pane, paneID, data)
	d.detectOSC133Exit(pane, paneID, data)
	d.applyPluginHandlers(pane, paneID, data)

	msg, _ := ipc.NewMessage(ipc.MsgPaneOutput, ipc.PaneOutputPayload{
		PaneID: paneID,
		Data:   data,
	})
	d.server.Broadcast(msg)
}

// detectBellEvent checks for standalone bell characters (not OSC terminators).
func (d *Daemon) detectBellEvent(pane *Pane, paneID string, data []byte) {
	const bellCooldown = 30 * time.Second
	if !bytes.Contains(data, []byte{0x07}) {
		return
	}
	cleaned := oscBellRe.ReplaceAll(data, nil)
	if !bytes.Contains(cleaned, []byte{0x07}) {
		return
	}
	pane.PluginMu.Lock()
	defer pane.PluginMu.Unlock()
	if !pane.LastBellEventAt.IsZero() && time.Since(pane.LastBellEventAt) < bellCooldown {
		return
	}
	pane.LastBellEventAt = time.Now()
	d.emitEvent(PaneEvent{
		ID: uuid.New().String(), PaneID: paneID, TabID: pane.TabID,
		PaneName: pane.Name, Type: "bell",
		Title: "Attention", Severity: "warning", Timestamp: time.Now(),
	})
}

// detectOSC133Exit parses OSC 133;D (command complete) sequences from shell integration.
func (d *Daemon) detectOSC133Exit(pane *Pane, paneID string, data []byte) {
	idx := bytes.Index(data, []byte("\x1b]133;D;"))
	if idx < 0 {
		return
	}
	rest := data[idx+8:]
	end := bytes.IndexAny(rest, "\x07\x1b")
	if end <= 0 {
		return
	}
	code, err := strconv.Atoi(string(rest[:end]))
	if err != nil {
		return
	}
	severity := "info"
	title := "Command completed"
	if code != 0 {
		severity = "error"
		title = fmt.Sprintf("Command failed (code %d)", code)
	}
	d.emitEvent(PaneEvent{
		ID: uuid.New().String(), PaneID: paneID, TabID: pane.TabID,
		PaneName: pane.Name, Type: "command_complete",
		Title: title, Severity: severity, Timestamp: time.Now(),
		Data: map[string]string{"exit_code": strconv.Itoa(code)},
	})
}

// applyPluginHandlers runs scraping, error matching for non-terminal plugins.
func (d *Daemon) applyPluginHandlers(pane *Pane, paneID string, data []byte) {
	if pane.Type == "" || pane.Type == "terminal" {
		return
	}
	p := d.registry.Get(pane.Type)
	if scraped := plugin.ScrapeOutput(p, data); scraped != nil {
		pane.PluginMu.Lock()
		if pane.PluginState == nil {
			pane.PluginState = make(map[string]string)
		}
		for k, v := range scraped {
			pane.PluginState[k] = v
			log.Printf("pane %s: scraped %s=%.8s...", paneID, k, v)
		}
		pane.PluginMu.Unlock()
	}
	if eh := plugin.MatchError(p, data); eh != nil && eh.Action == "dialog" {
		message := plugin.ExpandMessage(eh.Message, pane.InstanceArgs)
		errMsg, _ := ipc.NewMessage(ipc.MsgPluginError, ipc.PluginErrorPayload{
			PaneID:  paneID,
			Title:   eh.Title,
			Message: message,
		})
		d.server.Broadcast(errMsg)
	}
}

func (d *Daemon) broadcastState() {
	state := d.buildWorkspaceState()
	resp, _ := ipc.NewMessage(ipc.MsgWorkspaceState, state)
	d.server.Broadcast(resp)
}

func (d *Daemon) buildWorkspaceState() map[string]any {
	activeTab, tabs, panesByTab := d.session.SnapshotState()
	tabList := make([]map[string]any, 0, len(tabs))
	paneList := make([]map[string]any, 0)

	for _, tab := range tabs {
		paneIDs := make([]string, len(tab.Panes))
		copy(paneIDs, tab.Panes)
		tabData := map[string]any{
			"id":    tab.ID,
			"name":  tab.Name,
			"color": tab.Color,
			"panes": paneIDs,
		}
		if len(tab.Layout) > 0 {
			tabData["layout"] = tab.Layout
		}
		tabList = append(tabList, tabData)

		for _, pane := range panesByTab[tab.ID] {
			paneData := map[string]any{
				"id":     pane.ID,
				"tab_id": pane.TabID,
				"cwd":    pane.CWD,
			}
			if pane.Name != "" {
				paneData["name"] = pane.Name
			}
			if pane.Type != "" && pane.Type != "terminal" {
				paneData["type"] = pane.Type
			}
			pane.PluginMu.Lock()
			if len(pane.PluginState) > 0 {
				// Copy to avoid holding lock during JSON marshal
				ps := make(map[string]string, len(pane.PluginState))
				for k, v := range pane.PluginState {
					ps[k] = v
				}
				paneData["plugin_state"] = ps
			}
			pane.PluginMu.Unlock()
			if pane.InstanceName != "" {
				paneData["instance_name"] = pane.InstanceName
			}
			if len(pane.InstanceArgs) > 0 {
				paneData["instance_args"] = pane.InstanceArgs
			}
			paneList = append(paneList, paneData)
		}
	}

	return map[string]any{
		"active_tab": activeTab,
		"tabs":       tabList,
		"panes":      paneList,
	}
}

// spawnPane launches the appropriate process for a pane based on its plugin type.
// When restoring is true, resume strategies are applied (e.g., --resume for session_scrape).
func (d *Daemon) spawnPane(pane *Pane, ptySession apty.Session, restoring bool) error {
	// Default type
	if pane.Type == "" {
		pane.Type = "terminal"
	}

	p := d.registry.Get(pane.Type)
	if p == nil {
		p = d.registry.Get("terminal") // fallback
	}

	cmd := p.Command.Cmd
	args := append([]string{}, p.Command.Args...) // copy

	// Instance-specific args override base args
	if len(pane.InstanceArgs) > 0 {
		args = pane.InstanceArgs
	}

	// Pre-assign ID strategy: generate UUID on fresh start
	if !restoring && p.Persistence.Strategy == "preassign_id" {
		pane.PluginMu.Lock()
		if pane.PluginState == nil {
			pane.PluginState = make(map[string]string)
		}
		if pane.PluginState["session_id"] == "" {
			pane.PluginState["session_id"] = uuid.New().String()
		}
		pane.PluginMu.Unlock()
		if len(p.Persistence.StartArgs) > 0 {
			startArgs := plugin.ExpandResumeArgs(p.Persistence.StartArgs, pane.PluginState)
			if startArgs != nil {
				args = append(args, startArgs...)
			}
		}
	}

	// Resume logic for restoration
	if restoring {
		switch p.Persistence.Strategy {
		case "preassign_id", "session_scrape":
			if len(p.Persistence.ResumeArgs) > 0 && len(pane.PluginState) > 0 {
				args = plugin.ExpandResumeArgs(p.Persistence.ResumeArgs, pane.PluginState)
			}
		case "rerun":
			// args already set from InstanceArgs above
		case "none":
			// Don't restore — but we still spawn for now (pane exists in workspace)
		// "cwd_only": just start fresh with CWD (default behavior)
		}
	}

	// Shell integration (only for terminal-type panes)
	if p.Command.ShellIntegration {
		shellCfg := shellinit.Configure(cmd, config.QuilDir())
		if shellCfg != nil {
			ptySession.SetEnv(shellCfg.Env)
			cmd = shellCfg.Cmd
			args = shellCfg.Args
		}
	}

	// Plugin-specific env vars
	if len(p.Command.Env) > 0 {
		ptySession.SetEnv(p.Command.Env)
	}

	// Initialize plugin state map
	pane.PluginMu.Lock()
	if pane.PluginState == nil {
		pane.PluginState = make(map[string]string)
	}
	pane.PluginMu.Unlock()

	// Resolve command to absolute path so CWD doesn't interfere with lookup
	if resolved, err := exec.LookPath(cmd); err == nil {
		cmd = resolved
	}

	log.Printf("spawn: pane %s cmd=%s args=%v restoring=%v", pane.ID, cmd, args, restoring)
	if err := ptySession.Start(cmd, args...); err != nil {
		return err
	}
	pane.PTY = ptySession
	go d.streamPTYOutput(pane.ID, ptySession)
	return nil
}

// respondTo sends a response message to a specific connection with the same
// request ID for correlation. Used by MCP request-response handlers.
func respondTo(conn *ipc.Conn, requestID, msgType string, payload any) {
	resp, err := ipc.NewMessage(msgType, payload)
	if err != nil {
		log.Printf("respondTo: marshal %s: %v", msgType, err)
		return
	}
	resp.ID = requestID
	conn.Send(resp)
}

// highlightPane broadcasts a highlight message to TUI clients so they can
// visually indicate MCP interaction on a pane.
func (d *Daemon) highlightPane(paneID string) {
	if paneID == "" {
		return
	}
	msg, _ := ipc.NewMessage(ipc.MsgHighlightPane, ipc.HighlightPanePayload{
		PaneID: paneID,
	})
	d.server.Broadcast(msg)
}

// respondToAndHighlight sends a response and broadcasts a highlight for the pane.
func (d *Daemon) respondToAndHighlight(conn *ipc.Conn, requestID, msgType string, payload any, paneID string) {
	respondTo(conn, requestID, msgType, payload)
	d.highlightPane(paneID)
}

// emitEvent pushes an event to the queue and broadcasts to all clients.
func (d *Daemon) emitEvent(e PaneEvent) {
	d.events.Push(e)
	payload := toPaneEventPayload(e)
	msg, _ := ipc.NewMessage(ipc.MsgPaneEvent, payload)
	d.server.Broadcast(msg)
}

// idleChecker runs a periodic check for panes that have gone idle.
func (d *Daemon) idleChecker() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-d.shutdown:
			return
		case <-ticker.C:
			d.checkIdlePanes()
		}
	}
}

func (d *Daemon) checkIdlePanes() {
	const threshold = 5 * time.Second
	const cooldown = 30 * time.Second
	now := time.Now()
	for _, tab := range d.session.Tabs() {
		for _, pane := range d.session.Panes(tab.ID) {
			// Single lock span: read + conditionally write to avoid race with flushPaneOutput
			pane.PluginMu.Lock()
			shouldFire := !pane.IdleNotified &&
				!pane.LastOutputAt.IsZero() &&
				pane.ExitCode == nil &&
				(pane.LastIdleEventAt.IsZero() || now.Sub(pane.LastIdleEventAt) >= cooldown) &&
				now.Sub(pane.LastOutputAt) >= threshold
			if shouldFire {
				pane.IdleNotified = true
				pane.LastIdleEventAt = now
			}
			pane.PluginMu.Unlock()

			if !shouldFire {
				continue
			}

			title, severity := d.analyzeIdleTitle(pane)
			d.emitEvent(PaneEvent{
				ID:        uuid.New().String(),
				PaneID:    pane.ID,
				TabID:     pane.TabID,
				PaneName:  pane.Name,
				Type:      "output_idle",
				Title:     title,
				Severity:  severity,
				Timestamp: now,
			})
		}
	}
}

// analyzeIdleTitle determines the notification title/severity by matching
// the last few lines of pane output against plugin idle handlers.
func (d *Daemon) analyzeIdleTitle(pane *Pane) (title, severity string) {
	title = "Output idle"
	severity = "info"

	p := d.registry.Get(pane.Type)
	if p == nil {
		p = d.registry.Get("terminal")
	}
	if p == nil {
		return
	}
	if p.Category == "ai" {
		title = "Waiting for input"
		severity = "warning"
	}
	if pane.OutputBuf != nil && len(p.IdleHandlers) > 0 {
		raw := pane.OutputBuf.Bytes()
		// Limit to last 4KB for performance
		if len(raw) > 4096 {
			raw = raw[len(raw)-4096:]
		}
		stripped := ansi.Strip(string(raw))
		text := lastNLines(stripped, 5)
		if ih := plugin.MatchIdle(p, text); ih != nil {
			title = ih.Title
			severity = ih.Severity
		}
	}
	return
}

// lastNLines returns the last n non-empty lines from text.
func lastNLines(text string, n int) string {
	lines := strings.Split(text, "\n")
	var result []string
	for i := len(lines) - 1; i >= 0 && len(result) < n; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed != "" {
			result = append([]string{trimmed}, result...)
		}
	}
	return strings.Join(result, "\n")
}

func (d *Daemon) handleListPanesReq(conn *ipc.Conn, msg *ipc.Message) {
	_, tabs, panesByTab := d.session.SnapshotState()

	var panes []ipc.PaneInfo
	for _, tab := range tabs {
		for _, pane := range panesByTab[tab.ID] {
			typ := pane.Type
			if typ == "" {
				typ = "terminal"
			}
			pane.PluginMu.Lock()
			running := pane.ExitCode == nil
			pane.PluginMu.Unlock()
			panes = append(panes, ipc.PaneInfo{
				ID:           pane.ID,
				TabID:        tab.ID,
				TabName:      tab.Name,
				Name:         pane.Name,
				Type:         typ,
				CWD:          pane.CWD,
				Running:      running,
				InstanceName: pane.InstanceName,
			})
		}
	}

	respondTo(conn, msg.ID, ipc.MsgListPanesResp, ipc.ListPanesRespPayload{
		Panes: panes,
	})
}

func (d *Daemon) handleReadPaneOutputReq(conn *ipc.Conn, msg *ipc.Message) {
	var req ipc.ReadPaneOutputReqPayload
	if err := msg.DecodePayload(&req); err != nil {
		log.Printf("handleReadPaneOutputReq: decode: %v", err)
		respondTo(conn, msg.ID, ipc.MsgReadPaneOutputResp, ipc.ReadPaneOutputRespPayload{})
		return
	}

	pane := d.session.Pane(req.PaneID)
	if pane == nil {
		respondTo(conn, msg.ID, ipc.MsgReadPaneOutputResp, ipc.ReadPaneOutputRespPayload{
			PaneID: req.PaneID,
			Text:   "",
			Lines:  0,
		})
		return
	}
	d.highlightPane(pane.ID)

	lastLines := req.LastLines
	if lastLines <= 0 {
		lastLines = 50
	}
	if lastLines > 1000 {
		lastLines = 1000
	}

	raw := pane.OutputBuf.Bytes()
	stripped := ansi.Strip(string(raw))

	// Extract last N lines
	allLines := strings.Split(stripped, "\n")
	// Trim trailing empty line from final newline
	if len(allLines) > 0 && allLines[len(allLines)-1] == "" {
		allLines = allLines[:len(allLines)-1]
	}
	if len(allLines) > lastLines {
		allLines = allLines[len(allLines)-lastLines:]
	}
	text := strings.Join(allLines, "\n")

	respondTo(conn, msg.ID, ipc.MsgReadPaneOutputResp, ipc.ReadPaneOutputRespPayload{
		PaneID: req.PaneID,
		Text:   text,
		Lines:  len(allLines),
	})
}

func (d *Daemon) handlePaneStatusReq(conn *ipc.Conn, msg *ipc.Message) {
	var req ipc.PaneStatusReqPayload
	if err := msg.DecodePayload(&req); err != nil {
		log.Printf("handlePaneStatusReq: decode: %v", err)
		respondTo(conn, msg.ID, ipc.MsgPaneStatusResp, ipc.PaneStatusRespPayload{})
		return
	}

	pane := d.session.Pane(req.PaneID)
	if pane == nil {
		respondTo(conn, msg.ID, ipc.MsgPaneStatusResp, ipc.PaneStatusRespPayload{
			PaneID: req.PaneID,
		})
		return
	}
	d.highlightPane(pane.ID)

	typ := pane.Type
	if typ == "" {
		typ = "terminal"
	}

	pane.PluginMu.Lock()
	exitCode := pane.ExitCode
	running := exitCode == nil
	pane.PluginMu.Unlock()

	respondTo(conn, msg.ID, ipc.MsgPaneStatusResp, ipc.PaneStatusRespPayload{
		PaneID:   pane.ID,
		Running:  running,
		ExitCode: exitCode,
		Type:     typ,
		CWD:      pane.CWD,
		Name:     pane.Name,
	})
}

func (d *Daemon) handleCreatePaneReq(conn *ipc.Conn, msg *ipc.Message) {
	var req ipc.CreatePaneReqPayload
	if err := msg.DecodePayload(&req); err != nil {
		log.Printf("handleCreatePaneReq: decode: %v", err)
		respondTo(conn, msg.ID, ipc.MsgCreatePaneResp, ipc.CreatePaneRespPayload{})
		return
	}

	tabID := req.TabID
	if tabID == "" {
		tabID = d.session.ActiveTabID()
	}
	if tabID == "" {
		log.Print("handleCreatePaneReq: no active tab")
		respondTo(conn, msg.ID, ipc.MsgCreatePaneResp, ipc.CreatePaneRespPayload{})
		return
	}

	cwd := req.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}

	// Validate CWD exists and is a directory
	if info, err := os.Stat(cwd); err != nil || !info.IsDir() {
		log.Printf("handleCreatePaneReq: invalid cwd %q: %v", cwd, err)
		cwd, _ = os.Getwd()
	}

	pane, err := d.session.CreatePane(tabID, cwd)
	if err != nil {
		log.Printf("handleCreatePaneReq: create pane: %v", err)
		respondTo(conn, msg.ID, ipc.MsgCreatePaneResp, ipc.CreatePaneRespPayload{})
		return
	}
	d.highlightPane(pane.ID)

	pane.Type = req.Type
	if pane.Type == "" {
		pane.Type = "terminal"
	}
	pane.InstanceName = req.InstanceName
	pane.InstanceArgs = req.InstanceArgs

	ptySession := apty.NewWithSize(80, 24)
	if err := d.spawnPane(pane, ptySession, false); err != nil {
		log.Printf("handleCreatePaneReq: spawn: %v", err)
		// Pane exists but has no running process — caller can check via get_pane_status
	}

	d.broadcastState()
	d.requestSnapshot()

	respondTo(conn, msg.ID, ipc.MsgCreatePaneResp, ipc.CreatePaneRespPayload{
		PaneID: pane.ID,
		TabID:  tabID,
	})
}

func (d *Daemon) handleRestartPaneReq(conn *ipc.Conn, msg *ipc.Message) {
	var req ipc.RestartPaneReqPayload
	if err := msg.DecodePayload(&req); err != nil {
		log.Printf("handleRestartPaneReq: decode: %v", err)
		respondTo(conn, msg.ID, ipc.MsgRestartPaneResp, ipc.RestartPaneRespPayload{})
		return
	}

	pane := d.session.Pane(req.PaneID)
	if pane == nil {
		respondTo(conn, msg.ID, ipc.MsgRestartPaneResp, ipc.RestartPaneRespPayload{PaneID: req.PaneID})
		return
	}
	d.highlightPane(pane.ID)

	// Close existing PTY
	if pane.PTY != nil {
		pane.PTY.Close()
		pane.PTY = nil
	}

	// Reset exit state
	pane.PluginMu.Lock()
	pane.ExitCode = nil
	pane.ExitedAt = time.Time{}
	pane.PluginMu.Unlock()

	// Clear output buffer
	if pane.OutputBuf != nil {
		pane.OutputBuf.Reset()
	}

	// Respawn with same config, using last known dimensions
	cols, rows := pane.Cols, pane.Rows
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	ptySession := apty.NewWithSize(cols, rows)
	success := true
	if err := d.spawnPane(pane, ptySession, false); err != nil {
		log.Printf("handleRestartPaneReq: spawn: %v", err)
		success = false
	}

	d.broadcastState()
	d.requestSnapshot()

	respondTo(conn, msg.ID, ipc.MsgRestartPaneResp, ipc.RestartPaneRespPayload{
		PaneID:  pane.ID,
		Success: success,
	})
}

func (d *Daemon) handleScreenshotPaneReq(conn *ipc.Conn, msg *ipc.Message) {
	var req ipc.ScreenshotPaneReqPayload
	if err := msg.DecodePayload(&req); err != nil {
		log.Printf("handleScreenshotPaneReq: decode: %v", err)
		respondTo(conn, msg.ID, ipc.MsgScreenshotPaneResp, ipc.ScreenshotPaneRespPayload{})
		return
	}

	pane := d.session.Pane(req.PaneID)
	if pane == nil {
		respondTo(conn, msg.ID, ipc.MsgScreenshotPaneResp, ipc.ScreenshotPaneRespPayload{
			PaneID: req.PaneID,
		})
		return
	}
	d.highlightPane(pane.ID)

	width := req.Width
	if width <= 0 {
		width = pane.Cols
	}
	if width <= 0 {
		width = 80
	}
	if width > 500 {
		width = 500
	}
	height := req.Height
	if height <= 0 {
		height = pane.Rows
	}
	if height <= 0 {
		height = 24
	}
	if height > 200 {
		height = 200
	}

	raw := pane.OutputBuf.Bytes()

	// Feed ring buffer into a temporary VT emulator to get the screen state
	em := vt.NewSafeEmulator(width, height)
	em.Write(raw)

	// Extract text grid from emulator cells
	var lines []string
	for y := 0; y < height; y++ {
		var line strings.Builder
		for x := 0; x < width; x++ {
			cell := em.CellAt(x, y)
			if cell != nil && cell.Content != "" {
				line.WriteString(cell.Content)
			} else {
				line.WriteByte(' ')
			}
		}
		lines = append(lines, strings.TrimRight(line.String(), " "))
	}

	// Trim trailing empty lines
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	cursor := em.CursorPosition()

	respondTo(conn, msg.ID, ipc.MsgScreenshotPaneResp, ipc.ScreenshotPaneRespPayload{
		PaneID:  pane.ID,
		Text:    strings.Join(lines, "\n"),
		CursorX: cursor.X,
		CursorY: cursor.Y,
	})
}

func (d *Daemon) handleSwitchTabReq(conn *ipc.Conn, msg *ipc.Message) {
	var req ipc.SwitchTabReqPayload
	if err := msg.DecodePayload(&req); err != nil {
		log.Printf("handleSwitchTabReq: decode: %v", err)
		respondTo(conn, msg.ID, ipc.MsgSwitchTabResp, ipc.SwitchTabRespPayload{})
		return
	}

	d.session.SwitchTab(req.TabID)
	d.broadcastState()
	d.requestSnapshot()

	respondTo(conn, msg.ID, ipc.MsgSwitchTabResp, ipc.SwitchTabRespPayload{
		TabID: req.TabID,
	})
}

func (d *Daemon) handleListTabsReq(conn *ipc.Conn, msg *ipc.Message) {
	activeTab, tabs, panesByTab := d.session.SnapshotState()

	var tabInfos []ipc.TabInfo
	for _, tab := range tabs {
		tabInfos = append(tabInfos, ipc.TabInfo{
			ID:        tab.ID,
			Name:      tab.Name,
			Color:     tab.Color,
			PaneCount: len(panesByTab[tab.ID]),
			Active:    tab.ID == activeTab,
		})
	}

	respondTo(conn, msg.ID, ipc.MsgListTabsResp, ipc.ListTabsRespPayload{
		Tabs: tabInfos,
	})
}

func (d *Daemon) handleDestroyPaneReq(conn *ipc.Conn, msg *ipc.Message) {
	var req ipc.DestroyPaneReqPayload
	if err := msg.DecodePayload(&req); err != nil {
		log.Printf("handleDestroyPaneReq: decode: %v", err)
		respondTo(conn, msg.ID, ipc.MsgDestroyPaneResp, ipc.DestroyPaneRespPayload{})
		return
	}

	pane := d.session.Pane(req.PaneID)
	if pane == nil {
		respondTo(conn, msg.ID, ipc.MsgDestroyPaneResp, ipc.DestroyPaneRespPayload{})
		return
	}
	d.highlightPane(pane.ID)

	tabID := pane.TabID
	if err := d.session.DestroyPane(req.PaneID); err != nil {
		log.Printf("handleDestroyPaneReq: %v", err)
		respondTo(conn, msg.ID, ipc.MsgDestroyPaneResp, ipc.DestroyPaneRespPayload{})
		return
	}

	// Auto-create replacement if last pane in tab (same as handleDestroyPane)
	tab := d.session.Tab(tabID)
	if tab != nil && len(tab.Panes) == 0 {
		cwd, _ := os.Getwd()
		newPane, _ := d.session.CreatePane(tabID, cwd)
		if newPane != nil {
			newPane.Type = "terminal"
			ptySession := apty.NewWithSize(80, 24)
			if err := d.spawnPane(newPane, ptySession, false); err != nil {
				log.Printf("handleDestroyPaneReq: auto-create: %v", err)
			}
		}
	}

	d.broadcastState()
	d.requestSnapshot()

	respondTo(conn, msg.ID, ipc.MsgDestroyPaneResp, ipc.DestroyPaneRespPayload{
		Success: true,
	})
}

func (d *Daemon) handleSetActivePane(conn *ipc.Conn, msg *ipc.Message) {
	var req ipc.SetActivePanePayload
	if err := msg.DecodePayload(&req); err != nil {
		log.Printf("handleSetActivePane: decode: %v", err)
		return
	}

	// Verify pane exists
	pane := d.session.Pane(req.PaneID)
	if pane == nil {
		log.Printf("handleSetActivePane: pane not found: %s", req.PaneID)
		return
	}

	// Switch to the pane's tab
	d.session.SwitchTab(pane.TabID)

	// Broadcast to TUI clients so they can set focus
	broadcast, _ := ipc.NewMessage(ipc.MsgSetActivePane, ipc.SetActivePanePayload{
		PaneID: req.PaneID,
	})
	d.server.Broadcast(broadcast)

	d.broadcastState()
	d.requestSnapshot()
}

// Notification center handlers

func (d *Daemon) handleDismissEvent(msg *ipc.Message) {
	var payload ipc.DismissEventPayload
	if err := msg.DecodePayload(&payload); err != nil {
		return
	}
	if payload.EventID == "" {
		d.events.DismissAll()
	} else {
		d.events.Dismiss(payload.EventID)
	}
}

func (d *Daemon) handleGetNotificationsReq(conn *ipc.Conn, msg *ipc.Message) {
	events := d.events.Events()
	var payloads []ipc.PaneEventPayload
	for _, e := range events {
		payloads = append(payloads, toPaneEventPayload(e))
	}
	respondTo(conn, msg.ID, ipc.MsgGetNotificationsResp, ipc.GetNotificationsRespPayload{
		Events: payloads,
	})
}

func (d *Daemon) handleWatchNotificationsReq(conn *ipc.Conn, msg *ipc.Message) {
	var req ipc.WatchNotificationsReqPayload
	if err := msg.DecodePayload(&req); err != nil {
		log.Printf("handleWatchNotificationsReq: decode: %v", err)
		respondTo(conn, msg.ID, ipc.MsgWatchNotificationsResp, ipc.WatchNotificationsRespPayload{Timeout: true})
		return
	}

	timeoutMs := req.TimeoutMs
	if timeoutMs <= 0 {
		timeoutMs = 60000 // 60s default
	}
	if timeoutMs > 300000 {
		timeoutMs = 300000 // 5 min max
	}

	paneFilter := make(map[string]bool)
	for _, id := range req.PaneIDs {
		paneFilter[id] = true
	}

	// Remove any existing watcher for this connection (limit 1 per connection)
	d.events.RemoveWatchersByConn(conn)

	watcher := &connWatcher{
		conn:    conn,
		paneIDs: paneFilter,
		ch:      make(chan *PaneEvent, 1),
	}
	d.events.AddWatcher(watcher)

	// Block in goroutine — respond when event fires or timeout
	go func() {
		timer := time.NewTimer(time.Duration(timeoutMs) * time.Millisecond)
		defer timer.Stop()

		select {
		case evt, ok := <-watcher.ch:
			if !ok {
				return // connection closed
			}
			payload := toPaneEventPayload(*evt)
			respondTo(conn, msg.ID, ipc.MsgWatchNotificationsResp, ipc.WatchNotificationsRespPayload{
				Event: &payload,
			})
		case <-timer.C:
			d.events.RemoveWatcher(watcher)
			respondTo(conn, msg.ID, ipc.MsgWatchNotificationsResp, ipc.WatchNotificationsRespPayload{
				Timeout: true,
			})
		case <-d.shutdown:
			d.events.RemoveWatcher(watcher)
		}
	}()
}
