package daemon

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/artyomsv/aethel/internal/config"
	"github.com/artyomsv/aethel/internal/ipc"
	"github.com/artyomsv/aethel/internal/persist"
	apty "github.com/artyomsv/aethel/internal/pty"
	"github.com/artyomsv/aethel/internal/ringbuf"
	"github.com/artyomsv/aethel/internal/shellinit"
)

type Daemon struct {
	cfg      config.Config
	server   *ipc.Server
	session  *SessionManager
	shutdown chan struct{}
	restored bool // true if workspace was loaded from disk

	snapshotMu   sync.Mutex
	lastSnapshot time.Time
}

func New(cfg config.Config) *Daemon {
	// Buffer size: MaxLines * 512 bytes per line (generous for ANSI-rich output)
	bufSize := cfg.GhostBuffer.MaxLines * 512
	if bufSize <= 0 {
		bufSize = 500 * 512 // 256KB default
	}
	return &Daemon{
		cfg:      cfg,
		session:  NewSessionManager(bufSize),
		shutdown: make(chan struct{}),
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

	// Restore workspace from disk if available
	if err := d.restoreWorkspace(); err != nil {
		log.Printf("warning: failed to restore workspace: %v", err)
	}

	sockPath := config.SocketPath()
	d.server = ipc.NewServer(sockPath, d.handleMessage)

	if err := d.server.Start(); err != nil {
		return fmt.Errorf("start IPC server: %w", err)
	}

	// Respawn shells for restored panes
	if d.restored {
		d.respawnShells()
	}

	log.Printf("aetheld started, listening on %s", sockPath)
	return nil
}

func (d *Daemon) Wait() {
	// Start periodic snapshot timer
	interval, err := time.ParseDuration(d.cfg.Daemon.SnapshotInterval)
	if err != nil {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

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
		}
	}
}

func (d *Daemon) Stop() {
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

// snapshot persists workspace state and ghost buffers to disk.
func (d *Daemon) snapshot() {
	d.snapshotMu.Lock()
	defer d.snapshotMu.Unlock()

	state := d.buildWorkspaceState()
	wsPath := config.WorkspacePath()

	if err := persist.Save(wsPath, state); err != nil {
		log.Printf("snapshot workspace: %v", err)
	}

	// Flush ghost buffers
	bufDir := config.BufferDir()
	buffers := make(map[string][]byte)
	var activePaneIDs []string

	for _, tab := range d.session.Tabs() {
		for _, pane := range d.session.Panes(tab.ID) {
			activePaneIDs = append(activePaneIDs, pane.ID)
			if pane.OutputBuf != nil {
				if data := pane.OutputBuf.Bytes(); len(data) > 0 {
					buffers[pane.ID] = data
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

	d.lastSnapshot = time.Now()
	log.Printf("snapshot saved (%d tabs, %d panes)", len(d.session.Tabs()), len(activePaneIDs))
}

// snapshotDebounced triggers a snapshot unless one happened within the last second.
func (d *Daemon) snapshotDebounced() {
	d.snapshotMu.Lock()
	if time.Since(d.lastSnapshot) < time.Second {
		d.snapshotMu.Unlock()
		return
	}
	d.snapshotMu.Unlock()
	d.snapshot()
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

	// Restore tabs and panes
	for _, t := range tabs {
		tabMap, ok := t.(map[string]any)
		if !ok {
			continue
		}
		tabID, _ := tabMap["id"].(string)
		tabName, _ := tabMap["name"].(string)
		tabColor, _ := tabMap["color"].(string)
		if tabID == "" {
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

		// Restore pane IDs
		if paneIDs, ok := tabMap["panes"].([]any); ok {
			for _, pid := range paneIDs {
				paneID, _ := pid.(string)
				if paneID == "" {
					continue
				}
				tab.Panes = append(tab.Panes, paneID)

				// Create pane object
				paneData := panesByID[paneID]
				cwd, _ := paneData["cwd"].(string)
				name, _ := paneData["name"].(string)

				pane := &Pane{
					ID:        paneID,
					TabID:     tabID,
					CWD:       cwd,
					Name:      name,
					OutputBuf: ringbuf.NewRingBuffer(d.session.bufSize),
				}

				// Load ghost buffer from disk
				bufDir := config.BufferDir()
				if bufData, err := persist.LoadBuffer(bufDir, paneID); err == nil && len(bufData) > 0 {
					pane.OutputBuf.Write(bufData)
				}

				d.session.mu.Lock()
				d.session.panes[paneID] = pane
				d.session.mu.Unlock()
			}
		}

		d.session.mu.Lock()
		d.session.tabs[tabID] = tab
		d.session.tabOrder = append(d.session.tabOrder, tabID)
		d.session.mu.Unlock()
	}

	if activeTab != "" {
		d.session.SwitchTab(activeTab)
	}

	d.restored = true
	log.Printf("restored %d tabs, %d panes from disk", len(tabs), len(panes))
	return nil
}

// respawnShells starts a shell process in each pane that was restored from disk.
func (d *Daemon) respawnShells() {
	for _, tab := range d.session.Tabs() {
		for _, pane := range d.session.Panes(tab.ID) {
			if pane.PTY != nil {
				continue // Already has a PTY
			}

			ptySession := apty.New()
			if pane.CWD != "" {
				ptySession.SetCWD(pane.CWD)
			}

			if err := d.spawnShell(pane, ptySession); err != nil {
				log.Printf("respawn shell for pane %s: %v", pane.ID, err)
			} else {
				log.Printf("respawned shell in pane %s (cwd: %s)", pane.ID, pane.CWD)
			}
		}
	}
}

func (d *Daemon) handleMessage(conn *ipc.Conn, msg *ipc.Message) {
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

	// Create default workspace if empty (no tabs — neither fresh nor restored)
	if len(d.session.Tabs()) == 0 {
		tab := d.session.CreateTab("Shell")
		cwd, _ := os.Getwd()
		pane, _ := d.session.CreatePane(tab.ID, cwd)

		ptySession := apty.NewWithSize(cols, rows)
		if err := d.spawnShell(pane, ptySession); err != nil {
			log.Printf("failed to start PTY: %v", err)
		}
	}

	state := d.buildWorkspaceState()
	resp, _ := ipc.NewMessage(ipc.MsgWorkspaceState, state)
	conn.Send(resp)

	// Replay buffered output so reconnecting clients see previous terminal content
	for _, tab := range d.session.Tabs() {
		for _, pane := range d.session.Panes(tab.ID) {
			if pane.OutputBuf == nil {
				continue
			}
			history := pane.OutputBuf.Bytes()
			if len(history) == 0 {
				continue
			}
			histMsg, _ := ipc.NewMessage(ipc.MsgPaneOutput, ipc.PaneOutputPayload{
				PaneID: pane.ID,
				Data:   history,
			})
			conn.Send(histMsg)
		}
	}
}

func (d *Daemon) handleCreateTab(conn *ipc.Conn, msg *ipc.Message) {
	var payload ipc.CreateTabPayload
	msg.DecodePayload(&payload)

	tab := d.session.CreateTab(payload.Name)
	d.session.SwitchTab(tab.ID)

	// Every tab needs a default pane with a shell
	cwd, _ := os.Getwd()
	pane, _ := d.session.CreatePane(tab.ID, cwd)

	ptySession := apty.New()
	if err := d.spawnShell(pane, ptySession); err != nil {
		log.Printf("failed to start PTY for new tab: %v", err)
	}

	d.broadcastState()
	d.snapshotDebounced()
}

func (d *Daemon) handleDestroyTab(msg *ipc.Message) {
	var payload ipc.DestroyTabPayload
	msg.DecodePayload(&payload)
	d.session.DestroyTab(payload.TabID)
	d.broadcastState()
	d.snapshotDebounced()
}

func (d *Daemon) handleSwitchTab(msg *ipc.Message) {
	var payload ipc.SwitchTabPayload
	msg.DecodePayload(&payload)
	d.session.SwitchTab(payload.TabID)
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

	pane, err := d.session.CreatePane(payload.TabID, cwd)
	if err != nil {
		log.Printf("create pane error: %v", err)
		return
	}

	ptySession := apty.New()
	if err := d.spawnShell(pane, ptySession); err != nil {
		log.Printf("start PTY error: %v", err)
		return
	}
	d.broadcastState()
	d.snapshotDebounced()
}

func (d *Daemon) handleDestroyPane(msg *ipc.Message) {
	var payload ipc.DestroyPanePayload
	msg.DecodePayload(&payload)
	d.session.DestroyPane(payload.PaneID)
	d.broadcastState()
	d.snapshotDebounced()
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

func (d *Daemon) handleUpdateLayout(msg *ipc.Message) {
	var payload ipc.UpdateLayoutPayload
	msg.DecodePayload(&payload)

	tab := d.session.Tab(payload.TabID)
	if tab == nil {
		return
	}
	tab.Layout = payload.Layout
	// No broadcastState() — avoids feedback loop.
	// Layout is included in next natural state broadcast.
}

func (d *Daemon) streamPTYOutput(paneID string, pty apty.Session) {
	buf := make([]byte, 4096)
	for {
		n, err := pty.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])

			// Buffer for replay on reconnect
			if pane := d.session.Pane(paneID); pane != nil && pane.OutputBuf != nil {
				pane.OutputBuf.Write(data)
			}

			msg, _ := ipc.NewMessage(ipc.MsgPaneOutput, ipc.PaneOutputPayload{
				PaneID: paneID,
				Data:   data,
			})
			d.server.Broadcast(msg)
		}
		if err != nil {
			return
		}
	}
}

func (d *Daemon) broadcastState() {
	state := d.buildWorkspaceState()
	resp, _ := ipc.NewMessage(ipc.MsgWorkspaceState, state)
	d.server.Broadcast(resp)
}

func (d *Daemon) buildWorkspaceState() map[string]any {
	tabs := d.session.Tabs()
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

		for _, pane := range d.session.Panes(tab.ID) {
			paneData := map[string]any{
				"id":     pane.ID,
				"tab_id": pane.TabID,
				"cwd":    pane.CWD,
			}
			if pane.Name != "" {
				paneData["name"] = pane.Name
			}
			paneList = append(paneList, paneData)
		}
	}

	return map[string]any{
		"active_tab": d.session.ActiveTabID(),
		"tabs":       tabList,
		"panes":      paneList,
	}
}

func (d *Daemon) spawnShell(pane *Pane, ptySession apty.Session) error {
	shell := defaultShell()
	cfg := shellinit.Configure(shell, config.AethelDir())
	if cfg != nil {
		ptySession.SetEnv(cfg.Env)
		if err := ptySession.Start(cfg.Cmd, cfg.Args...); err != nil {
			return err
		}
	} else {
		if err := ptySession.Start(shell); err != nil {
			return err
		}
	}
	pane.PTY = ptySession
	go d.streamPTYOutput(pane.ID, ptySession)
	return nil
}

func defaultShell() string {
	if runtime.GOOS == "windows" {
		if ps, err := exec.LookPath("pwsh.exe"); err == nil {
			return ps
		}
		return "cmd.exe"
	}
	if shell := os.Getenv("SHELL"); shell != "" {
		return shell
	}
	return "/bin/sh"
}
