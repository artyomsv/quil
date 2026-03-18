package daemon

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/artyomsv/aethel/internal/config"
	"github.com/artyomsv/aethel/internal/ipc"
	"github.com/artyomsv/aethel/internal/persist"
	"github.com/artyomsv/aethel/internal/plugin"
	apty "github.com/artyomsv/aethel/internal/pty"
	"github.com/artyomsv/aethel/internal/ringbuf"
	"github.com/artyomsv/aethel/internal/shellinit"
)

type Daemon struct {
	cfg        config.Config
	server     *ipc.Server
	session    *SessionManager
	registry   *plugin.Registry
	shutdown   chan struct{}
	snapshotCh chan struct{} // buffered channel for snapshot requests
	restored   bool         // true if workspace was loaded from disk
}

func New(cfg config.Config) *Daemon {
	// Buffer size: MaxLines * 512 bytes per line (generous for ANSI-rich output)
	bufSize := cfg.GhostBuffer.MaxLines * 512
	if bufSize <= 0 {
		bufSize = 500 * 512 // 256KB default
	}

	reg := plugin.NewRegistry()

	return &Daemon{
		cfg:        cfg,
		session:    NewSessionManager(bufSize),
		registry:   reg,
		shutdown:   make(chan struct{}),
		snapshotCh: make(chan struct{}, 1),
	}
}

func (d *Daemon) Start() error {
	aethelDir := config.AethelDir()
	if err := os.MkdirAll(aethelDir, 0700); err != nil {
		return fmt.Errorf("create aethel dir: %w", err)
	}

	if err := shellinit.EnsureInitDir(aethelDir); err != nil {
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
	d.server = ipc.NewServer(sockPath, d.handleMessage, func() { d.requestSnapshot() })

	if err := d.server.Start(); err != nil {
		return fmt.Errorf("start IPC server: %w", err)
	}

	// Respawn panes for restored workspace
	if d.restored {
		d.respawnPanes()
	}

	log.Printf("aetheld started, listening on %s", sockPath)
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
		close(d.shutdown)
	}
}

func (d *Daemon) handleAttach(conn *ipc.Conn, msg *ipc.Message) {
	var attach ipc.AttachPayload
	msg.DecodePayload(&attach)

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
}

func (d *Daemon) handleCreateTab(conn *ipc.Conn, msg *ipc.Message) {
	var payload ipc.CreateTabPayload
	msg.DecodePayload(&payload)

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
	msg.DecodePayload(&payload)
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
	msg.DecodePayload(&payload)
	log.Printf("tab switch: %s", payload.TabID)
	d.session.SwitchTab(payload.TabID)
	d.broadcastState()
	d.requestSnapshot()
}

func (d *Daemon) handleUpdateTab(msg *ipc.Message) {
	var payload ipc.UpdateTabPayload
	msg.DecodePayload(&payload)

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
	msg.DecodePayload(&payload)

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
	msg.DecodePayload(&payload)

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
	msg.DecodePayload(&payload)

	pane := d.session.Pane(payload.PaneID)
	if pane == nil || pane.PTY == nil {
		return
	}
	pane.PTY.Write(payload.Data)
}

func (d *Daemon) handleResizePane(msg *ipc.Message) {
	var payload ipc.ResizePanePayload
	msg.DecodePayload(&payload)

	pane := d.session.Pane(payload.PaneID)
	if pane == nil || pane.PTY == nil {
		return
	}
	pane.PTY.Resize(payload.Rows, payload.Cols)
}

func (d *Daemon) handleUpdatePane(msg *ipc.Message) {
	var payload ipc.UpdatePanePayload
	msg.DecodePayload(&payload)

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
	msg.DecodePayload(&payload)

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

	// Scrape plugin state from output (e.g., session IDs)
	if pane.Type != "" && pane.Type != "terminal" {
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

		// Check for error patterns
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

	msg, _ := ipc.NewMessage(ipc.MsgPaneOutput, ipc.PaneOutputPayload{
		PaneID: paneID,
		Data:   data,
	})
	d.server.Broadcast(msg)
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
		shellCfg := shellinit.Configure(cmd, config.AethelDir())
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
