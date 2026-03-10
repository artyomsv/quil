package daemon

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/stukans/aethel/internal/config"
	"github.com/stukans/aethel/internal/ipc"
	apty "github.com/stukans/aethel/internal/pty"
)

type Daemon struct {
	cfg     config.Config
	server  *ipc.Server
	session *SessionManager
}

func New(cfg config.Config) *Daemon {
	return &Daemon{
		cfg:     cfg,
		session: NewSessionManager(),
	}
}

func (d *Daemon) Start() error {
	aethelDir := config.AethelDir()
	if err := os.MkdirAll(aethelDir, 0700); err != nil {
		return fmt.Errorf("create aethel dir: %w", err)
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
	<-sigCh
	log.Println("shutting down...")
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
		d.handleAttach(conn)
	case ipc.MsgCreateTab:
		d.handleCreateTab(conn, msg)
	case ipc.MsgDestroyTab:
		d.handleDestroyTab(msg)
	case ipc.MsgSwitchTab:
		d.handleSwitchTab(msg)
	case ipc.MsgCreatePane:
		d.handleCreatePane(msg)
	case ipc.MsgDestroyPane:
		d.handleDestroyPane(msg)
	case ipc.MsgPaneInput:
		d.handlePaneInput(msg)
	case ipc.MsgResizePane:
		d.handleResizePane(msg)
	case ipc.MsgShutdown:
		d.Stop()
		os.Exit(0)
	}
}

func (d *Daemon) handleAttach(conn *ipc.Conn) {
	// Create default workspace if empty
	if len(d.session.Tabs()) == 0 {
		tab := d.session.CreateTab("Shell")
		cwd, _ := os.Getwd()
		pane, _ := d.session.CreatePane(tab.ID, cwd)

		shell := defaultShell()
		ptySession := apty.New()
		if err := ptySession.Start(shell); err == nil {
			pane.PTY = ptySession
			go d.streamPTYOutput(pane.ID, ptySession)
		}
	}

	state := d.buildWorkspaceState()
	resp, _ := ipc.NewMessage(ipc.MsgWorkspaceState, state)
	conn.Send(resp)
}

func (d *Daemon) handleCreateTab(conn *ipc.Conn, msg *ipc.Message) {
	var payload ipc.CreateTabPayload
	msg.DecodePayload(&payload)

	tab := d.session.CreateTab(payload.Name)

	update := map[string]any{"action": "tab_created", "tab_id": tab.ID, "name": tab.Name}
	resp, _ := ipc.NewMessage(ipc.MsgStateUpdate, update)
	d.server.Broadcast(resp)
}

func (d *Daemon) handleDestroyTab(msg *ipc.Message) {
	var payload ipc.DestroyTabPayload
	msg.DecodePayload(&payload)
	d.session.DestroyTab(payload.TabID)

	update := map[string]any{"action": "tab_destroyed", "tab_id": payload.TabID}
	resp, _ := ipc.NewMessage(ipc.MsgStateUpdate, update)
	d.server.Broadcast(resp)
}

func (d *Daemon) handleSwitchTab(msg *ipc.Message) {
	var payload ipc.SwitchTabPayload
	msg.DecodePayload(&payload)
	d.session.SwitchTab(payload.TabID)
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

	shell := defaultShell()
	ptySession := apty.New()
	if err := ptySession.Start(shell); err != nil {
		log.Printf("start PTY error: %v", err)
		return
	}
	pane.PTY = ptySession

	go d.streamPTYOutput(pane.ID, ptySession)

	update := map[string]any{"action": "pane_created", "pane_id": pane.ID, "tab_id": payload.TabID}
	resp, _ := ipc.NewMessage(ipc.MsgStateUpdate, update)
	d.server.Broadcast(resp)
}

func (d *Daemon) handleDestroyPane(msg *ipc.Message) {
	var payload ipc.DestroyPanePayload
	msg.DecodePayload(&payload)
	d.session.DestroyPane(payload.PaneID)

	update := map[string]any{"action": "pane_destroyed", "pane_id": payload.PaneID}
	resp, _ := ipc.NewMessage(ipc.MsgStateUpdate, update)
	d.server.Broadcast(resp)
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

func (d *Daemon) streamPTYOutput(paneID string, pty apty.Session) {
	buf := make([]byte, 4096)
	for {
		n, err := pty.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])
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

func (d *Daemon) buildWorkspaceState() map[string]any {
	tabs := d.session.Tabs()
	tabList := make([]map[string]any, 0, len(tabs))
	paneList := make([]map[string]any, 0)

	for _, tab := range tabs {
		paneIDs := make([]string, len(tab.Panes))
		copy(paneIDs, tab.Panes)
		tabList = append(tabList, map[string]any{
			"id":    tab.ID,
			"name":  tab.Name,
			"panes": paneIDs,
		})

		for _, pane := range d.session.Panes(tab.ID) {
			paneList = append(paneList, map[string]any{
				"id":     pane.ID,
				"tab_id": pane.TabID,
				"cwd":    pane.CWD,
			})
		}
	}

	return map[string]any{
		"active_tab": d.session.ActiveTabID(),
		"tabs":       tabList,
		"panes":      paneList,
	}
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
