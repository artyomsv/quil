package daemon

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/artyomsv/aethel/internal/config"
	"github.com/artyomsv/aethel/internal/ipc"
	apty "github.com/artyomsv/aethel/internal/pty"
	"github.com/artyomsv/aethel/internal/shellinit"
)

type Daemon struct {
	cfg      config.Config
	server   *ipc.Server
	session  *SessionManager
	shutdown chan struct{}
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

	sockPath := config.SocketPath()
	d.server = ipc.NewServer(sockPath, d.handleMessage)

	if err := d.server.Start(); err != nil {
		return fmt.Errorf("start IPC server: %w", err)
	}

	log.Printf("aetheld started, listening on %s", sockPath)
	return nil
}

func (d *Daemon) Wait() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-sigCh:
		log.Println("shutting down (signal)...")
	case <-d.shutdown:
		log.Println("shutting down (IPC)...")
	}
	d.Stop()
}

func (d *Daemon) Stop() {
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

	// Create default workspace if empty
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
}

func (d *Daemon) handleDestroyTab(msg *ipc.Message) {
	var payload ipc.DestroyTabPayload
	msg.DecodePayload(&payload)
	d.session.DestroyTab(payload.TabID)
	d.broadcastState()
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
}

func (d *Daemon) handleDestroyPane(msg *ipc.Message) {
	var payload ipc.DestroyPanePayload
	msg.DecodePayload(&payload)
	d.session.DestroyPane(payload.PaneID)
	d.broadcastState()
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
