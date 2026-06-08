package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"regexp"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/google/uuid"
	"github.com/artyomsv/quil/internal/claudehook"
	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/hookevents"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/logger"
	memreport "github.com/artyomsv/quil/internal/memreport"
	"github.com/artyomsv/quil/internal/opencodehook"
	"github.com/artyomsv/quil/internal/persist"
	"github.com/artyomsv/quil/internal/plugin"
	apty "github.com/artyomsv/quil/internal/pty"
	"github.com/artyomsv/quil/internal/ringbuf"
	"github.com/artyomsv/quil/internal/shellinit"
	"github.com/artyomsv/quil/internal/version"
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
	stopOnce     sync.Once
	snapshotCh   chan struct{} // buffered channel for snapshot requests
	restored     bool         // true if workspace was loaded from disk
	events       *eventQueue  // notification center event queue
	// clientCWD is the last-known CWD from a TUI client, used as the
	// default working directory for new panes/tabs. Read by defaultCWD()
	// from any IPC dispatch goroutine and written by handleAttach on each
	// connect — atomic.Pointer is what keeps that race-free.
	clientCWD    atomic.Pointer[string]

	memReport   *memreport.Collector
	collectorWG sync.WaitGroup

	// hookIngester translates hookevents.Payload (from spool reads / future
	// IPC submissions) into PaneEvents via emitHookEvent. Lazily initialised
	// in Start once the events dir is ready; nil before Start.
	hookIngester *hookevents.Ingester
	// hookSpool reads $QUIL_HOME/events/<paneID>.jsonl appended by the
	// Claude .sh / opencode .js hook scripts. Polled by hookEventsWatcher
	// every 200 ms while the daemon runs.
	hookSpool *hookevents.Spool
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

	d := &Daemon{
		cfg:        cfg,
		session:    NewSessionManager(bufSize),
		registry:   reg,
		shutdown:   make(chan struct{}),
		snapshotCh: make(chan struct{}, 1),
		events:     newEventQueue(maxEvents),
	}
	d.memReport = memreport.NewCollector(d.session, 5*time.Second)
	return d
}

func (d *Daemon) Start() error {
	quilDir := config.QuilDir()
	if err := os.MkdirAll(quilDir, 0700); err != nil {
		return fmt.Errorf("create quil dir: %w", err)
	}

	if err := shellinit.EnsureInitDir(quilDir); err != nil {
		log.Printf("warning: failed to write shell init scripts: %v", err)
	}

	if err := claudehook.EnsureScripts(quilDir); err != nil {
		log.Printf("warning: failed to write claude hook scripts: %v", err)
	}
	if err := opencodehook.EnsureScripts(quilDir); err != nil {
		log.Printf("warning: failed to write opencode hook scripts: %v", err)
	}
	if err := os.MkdirAll(config.SessionsDir(), 0700); err != nil {
		log.Printf("warning: failed to create sessions dir: %v", err)
	}

	// Hook event ingest plumbing: spool reader + ingester (rate limit +
	// coalesce) feeding emitHookEvent. Init truncates stale spool files so
	// the daemon never replays notifications from a prior session.
	d.hookSpool = hookevents.NewSpool(config.EventsDir())
	if err := d.hookSpool.Init(); err != nil {
		log.Printf("warning: failed to init hook events spool: %v", err)
	}
	d.hookIngester = hookevents.NewIngester(d.emitHookEvent)

	// Write default plugin TOML files if missing, then load all plugins
	if _, err := plugin.EnsureDefaultPlugins(config.PluginsDir()); err != nil {
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
	go d.hookEventsWatcher()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-d.shutdown
		cancel()
	}()
	d.collectorWG.Add(1)
	go func() {
		defer d.collectorWG.Done()
		d.memReport.Run(ctx)
	}()

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
			// Periodic safety net MUST call snapshot() directly, not
			// requestSnapshot(). The debounce timer below resets on every
			// fresh request, so under sustained event traffic (resize spam,
			// MCP bursts, rapid PTY flushes) routing the ticker through the
			// debounced path would let the timer be perpetually rescheduled
			// and never fire — workspace.json would stop being flushed.
			// snapshot() is internally consistent (single SnapshotState),
			// so a coincidental overlap with the debounced path is wasteful
			// but correct: persist.Save uses atomic temp+rename.
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

// refreshPluginStateFromHooks copies the SessionStart-hook-recorded
// session id for every claude-code and opencode pane into
// PluginState["session_id"] so the final on-disk snapshot carries the
// live, rotated id rather than the initial preassigned one.
//
// Normal operation never updates PluginState["session_id"]: the hook file
// at ~/.quil/sessions/<pane-id>.id is the authoritative source of truth
// and resumeTemplateFor reads it at restore time. But that one-way flow
// means workspace.json can drift after /clear, /resume, or compaction —
// and if the hook file is later lost, the fallback PluginState id is
// stale. Refreshing on shutdown closes that gap: workspace.json becomes
// self-sufficient. F1 → Stop daemon and signal-driven shutdowns both
// run through here.
//
// Concurrency contract: caller must have already stopped the IPC server
// and waited on collectorWG so no goroutine can create, destroy, or
// mutate panes. PluginMu is still taken per pane to keep the assignment
// race-free against any future call site that does run concurrently
// with the PTY output goroutine.
//
// Empty/error hook reads preserve the existing PluginState["session_id"]
// — clobbering it with "" would force the next restore to fall back to
// --continue even when a usable preassigned id is still on disk.
func (d *Daemon) refreshPluginStateFromHooks() {
	for _, tab := range d.session.Tabs() {
		for _, pane := range d.session.Panes(tab.ID) {
			var hookID string
			switch pane.Type {
			case "claude-code":
				if id, err := readHookSessionIDFn(pane.ID); err == nil {
					hookID = id
				}
			case "opencode":
				if id, err := readOpencodeSessionIDFn(pane.ID); err == nil {
					hookID = id
				}
			default:
				continue
			}
			if hookID == "" {
				continue
			}
			pane.PluginMu.Lock()
			if pane.PluginState == nil {
				pane.PluginState = make(map[string]string)
			}
			pane.PluginState["session_id"] = hookID
			pane.PluginMu.Unlock()
		}
	}
}

func (d *Daemon) Stop() {
	// Close shutdown channel first so every long-running goroutine
	// (idleChecker, memReport ctx bridge, sendGhostChunked, etc.) wakes up
	// and exits, regardless of whether MsgShutdown or a signal beat us here.
	d.shutdownOnce.Do(func() { close(d.shutdown) })
	d.stopOnce.Do(func() {
		// Stop the IPC server FIRST so no new client mutations can land
		// after the final snapshot — otherwise an IPC handler can ACK a
		// pane create/destroy to the client that the on-disk snapshot
		// has already missed.
		if d.server != nil {
			d.server.Stop()
		}
		d.collectorWG.Wait()
		// Pull the latest hook-recorded session ids into PluginState so
		// the final snapshot survives even if the hook files are lost.
		d.refreshPluginStateFromHooks()
		log.Print("daemon stopping, writing final snapshot...")
		d.snapshot()
		for _, tab := range d.session.Tabs() {
			for _, pane := range d.session.Panes(tab.ID) {
				if pane.PTY != nil {
					pane.PTY.Close()
				}
			}
		}
	})
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

	// Take ONE consistent view of the session and reuse it for both the
	// workspace JSON and the ghost-buffer flush. Calling SnapshotState
	// twice (once via buildWorkspaceState, once for the buffer loop)
	// allowed a pane create/destroy between the two calls to slip through
	// — the workspace.json said N panes while the buffer flush iterated
	// N±1, surfacing as the "snapshot pane count oscillation" bug.
	activeTab, tabs, panesByTab := d.session.SnapshotState()
	state := d.workspaceStateFromSnapshot(activeTab, tabs, panesByTab)

	if err := persist.Save(config.WorkspacePath(), state); err != nil {
		log.Printf("snapshot workspace: %v", err)
	}

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

				muted, _ := paneData["muted"].(bool)

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
					Muted:        muted,
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
				}
			}

			if err := d.spawnPane(pane, ptySession, true); err != nil {
				log.Printf("respawn pane %s (type=%s): %v — falling back to terminal", pane.ID, pane.Type, err)
				pane.Type = "terminal"
				ptySession2 := apty.New()
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
	case ipc.MsgReorderTab:
		d.handleReorderTab(msg)
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

	// Memory reporting
	case ipc.MsgMemoryReportReq:
		d.handleMemoryReportReq(conn, msg)

	// Version negotiation — reply with the running daemon's version so the
	// client can gate attach on matching binaries.
	case ipc.MsgVersionReq:
		respondTo(conn, msg.ID, ipc.MsgVersionResp, ipc.VersionRespPayload{
			Version: version.Current(),
		})
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

	// Remember client CWD so new tabs/panes default to the TUI's directory
	// instead of the daemon's (which is frozen at daemon start time). An
	// empty value resets to "use daemon CWD" — preferable to retaining a
	// stale value from a previous client.
	cwd := attach.CWD
	d.clientCWD.Store(&cwd)

	// Create default workspace if empty (no tabs — neither fresh nor restored)
	if len(d.session.Tabs()) == 0 {
		log.Print("attach: creating default workspace (no tabs)")
		tab := d.session.CreateTab("Shell")
		pane, _ := d.session.CreatePane(tab.ID, d.defaultCWD())
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
			sendGhostChunked(conn, pane.ID, ghost, d.shutdown)
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

// sendGhostChunked sends a ghost buffer in 8 KB chunks with a 2 ms yield
// between each chunk. This prevents the TUI's Bubble Tea event loop from
// being starved by a single massive message — keyboard events can interleave
// between chunks. The 2 ms delay matches the live-output coalescing interval
// in streamPTYOutput, so ghost replay feels identical to fast live output.
// The done channel allows early abort if the daemon is shutting down or the
// client disconnects mid-replay.
func sendGhostChunked(conn *ipc.Conn, paneID string, data []byte, done <-chan struct{}) {
	const chunkSize = 8 * 1024 // 8 KB — typical PTY read size
	const chunkDelay = 2 * time.Millisecond

	for len(data) > 0 {
		n := chunkSize
		if n > len(data) {
			n = len(data)
		}
		msg, _ := ipc.NewMessage(ipc.MsgPaneOutput, ipc.PaneOutputPayload{
			PaneID: paneID,
			Data:   data[:n],
			Ghost:  true,
		})
		if err := conn.Send(msg); err != nil {
			return // client disconnected
		}
		data = data[n:]
		if len(data) > 0 {
			select {
			case <-done:
				return
			case <-time.After(chunkDelay):
			}
		}
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
	pane, _ := d.session.CreatePane(tab.ID, d.defaultCWD())
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
		pane, _ := d.session.CreatePane(tab.ID, d.defaultCWD())
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

func (d *Daemon) handleReorderTab(msg *ipc.Message) {
	var payload ipc.ReorderTabPayload
	if err := msg.DecodePayload(&payload); err != nil {
		return
	}
	if !d.session.ReorderTab(payload.TabID, payload.NewIndex) {
		// No-op: tab missing or already at requested index. Don't
		// broadcast — saves a wave of needless state-update traffic during
		// a drag that hasn't crossed a tab boundary yet.
		return
	}
	d.broadcastState()
	d.requestSnapshot()
}

func (d *Daemon) handleCreatePane(msg *ipc.Message) {
	var payload ipc.CreatePanePayload
	if err := msg.DecodePayload(&payload); err != nil {
		return
	}

	cwd := payload.CWD
	logger.Debug("create pane: received payload cwd=%q type=%s", cwd, payload.Type)
	// Validate the CWD before trusting it. The TUI dialog already validates
	// what it sends, but the IPC socket is reachable by other clients (the
	// MCP bridge, future tooling), and the daemon should be authoritative.
	// On any failure (gone / not a directory / stat error) we fall back to
	// the daemon's own working directory rather than aborting the spawn.
	//
	// Re-resolve symlinks here too: the TUI calls EvalSymlinks before sending
	// but a symlink swap between the TUI's Stat and the daemon's spawn would
	// otherwise redirect the child process to a different directory. Doing
	// the resolve once more on the daemon side closes that TOCTOU window for
	// every IPC client (TUI, MCP, future tooling).
	if cwd != "" {
		if info, err := os.Stat(cwd); err != nil || !info.IsDir() {
			log.Printf("create pane: rejecting cwd %q (err=%v); using daemon default", cwd, err)
			cwd = ""
		} else if resolved, evalErr := filepath.EvalSymlinks(cwd); evalErr == nil {
			cwd = resolved
		}
	}
	if cwd == "" {
		cwd = d.defaultCWD()
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

	// Tear down the pane's hook event spool file before destroying the
	// pane itself — the watcher's next tick must not pick up stale lines
	// from a destroyed pane.
	if d.hookSpool != nil {
		d.hookSpool.Cleanup(payload.PaneID)
	}

	d.session.DestroyPane(payload.PaneID)

	// Auto-create replacement if last pane in tab was destroyed
	if tabID != "" {
		if panes := d.session.Panes(tabID); len(panes) == 0 {
			if newPane, err := d.session.CreatePane(tabID, d.defaultCWD()); err == nil {
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
	if payload.Muted != nil {
		pane.PluginMu.Lock()
		pane.Muted = *payload.Muted
		pane.PluginMu.Unlock()
		log.Printf("pane %s: muted=%v", pane.ID, *payload.Muted)
	}
	d.broadcastState()
	d.requestSnapshot()
}

func (d *Daemon) handleReloadPlugins() {
	if _, err := plugin.EnsureDefaultPlugins(config.PluginsDir()); err != nil {
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
					d.emitEvent(withExcerpt(PaneEvent{
						ID:        uuid.New().String(),
						PaneID:    paneID,
						TabID:     pane.TabID,
						PaneName:  pane.Name,
						Type:      "process_exit",
						Title:     title,
						Severity:  severity,
						Timestamp: time.Now(),
						Data:      map[string]string{"exit_code": strconv.Itoa(code)},
					}, paneOutputExcerpt(pane, 5)))
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
	d.emitEvent(withExcerpt(PaneEvent{
		ID: uuid.New().String(), PaneID: paneID, TabID: pane.TabID,
		PaneName: pane.Name, Type: "bell",
		Title: "Attention", Severity: "warning", Timestamp: time.Now(),
	}, paneOutputExcerpt(pane, 3)))
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
	d.emitEvent(withExcerpt(PaneEvent{
		ID: uuid.New().String(), PaneID: paneID, TabID: pane.TabID,
		PaneName: pane.Name, Type: "command_complete",
		Title: title, Severity: severity, Timestamp: time.Now(),
		Data: map[string]string{"exit_code": strconv.Itoa(code)},
	}, paneOutputExcerpt(pane, 5)))
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
	if d.server == nil {
		return
	}
	state := d.buildWorkspaceState()
	resp, _ := ipc.NewMessage(ipc.MsgWorkspaceState, state)
	d.server.Broadcast(resp)
}

func (d *Daemon) buildWorkspaceState() map[string]any {
	activeTab, tabs, panesByTab := d.session.SnapshotState()
	return d.workspaceStateFromSnapshot(activeTab, tabs, panesByTab)
}

// workspaceStateFromSnapshot is the pure half of buildWorkspaceState — it
// turns an already-taken SnapshotState into the wire/persistence map. Callers
// that already hold a consistent snapshot (e.g. snapshot()) reuse it instead
// of calling SnapshotState a second time.
func (d *Daemon) workspaceStateFromSnapshot(activeTab string, tabs []*Tab, panesByTab map[string][]*Pane) map[string]any {
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
			if pane.Muted {
				paneData["muted"] = true
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

// claudeSessionExistsFn is the probe resolveSpawnArgs uses to decide whether
// a restored claude-code pane can use --resume <uuid> (unique session) or
// must fall back to --continue (Claude's most-recent-in-CWD lookup).
//
// Defaults to the real filesystem check; tests override with a stub so the
// arg-merging matrix never reaches ~/.claude.
var claudeSessionExistsFn = claudeSessionFileExists

// readHookSessionIDFn reads the hook-recorded session id for a pane. Defaults
// to the real claudehook.ReadPersistedSessionID; tests override it so
// resolveSpawnArgs matrix tests never touch $QUIL_HOME/sessions/.
var readHookSessionIDFn = func(paneID string) (string, error) {
	id, _, err := claudehook.ReadPersistedSessionID(config.QuilDir(), paneID)
	return id, err
}

// claudeHookScriptStatFn lets claudeHookSpawnPrep check whether the hook
// script exists on disk. Defaults to os.Stat; tests override to simulate the
// "EnsureScripts failed at startup" branch without touching the real FS.
var claudeHookScriptStatFn = func(path string) error {
	_, err := os.Stat(path)
	return err
}

// readOpencodeSessionIDFn mirrors readHookSessionIDFn for the opencode pane
// type. Tests override it so the spawn-args matrix never touches the real
// $QUIL_HOME/sessions/ directory.
var readOpencodeSessionIDFn = func(paneID string) (string, error) {
	id, _, err := opencodehook.ReadPersistedSessionID(config.QuilDir(), paneID)
	return id, err
}

// opencodeHookScriptStatFn mirrors claudeHookScriptStatFn for the opencode
// JS plugin. Defaults to os.Stat; tests override to simulate the
// "EnsureScripts failed at startup" branch.
var opencodeHookScriptStatFn = func(path string) error {
	_, err := os.Stat(path)
	return err
}

// opencodeSpawnPrep returns the env vars to add to a fresh opencode spawn so
// the bundled session-id-tracker plugin loads via OPENCODE_CONFIG_CONTENT.
// Returns nil when the plugin script is missing on disk so the spawn proceeds
// without session tracking — matching the pre-feature behaviour rather than
// failing the whole spawn.
//
// quilDir is absolutized before being embedded so the resulting JSON plugin
// path is unambiguous in the child opencode process — which resolves plugin
// entries against its own CWD, not the daemon's. With `prompts_cwd = true`
// the child CWD is user-chosen and may differ from where the daemon was
// launched, so a relative quilDir would silently break tracking.
func opencodeSpawnPrep(quilDir, paneID, hookMode string) []string {
	absQuilDir, err := filepath.Abs(quilDir)
	if err != nil {
		log.Printf("warning: pane %s: absolutize quilDir %q: %v — session-id rotation tracking disabled", paneID, quilDir, err)
		return nil
	}
	scriptPath := opencodehook.ScriptPath(absQuilDir)
	if err := opencodeHookScriptStatFn(scriptPath); err != nil {
		log.Printf("warning: pane %s: opencode plugin script unavailable (%s): %v — session-id rotation tracking disabled", paneID, scriptPath, err)
		return nil
	}
	cfg, err := opencodehook.BuildConfigContent(scriptPath)
	if err != nil {
		log.Printf("warning: pane %s: build opencode config content: %v — session-id rotation tracking disabled", paneID, err)
		return nil
	}
	mode := hookMode
	if mode == "" {
		mode = "default"
	}
	return []string{
		"QUIL_PANE_ID=" + paneID,
		"QUIL_HOME=" + absQuilDir,
		"QUIL_HOOK_MODE=" + mode,
		"OPENCODE_CONFIG_CONTENT=" + cfg,
	}
}

// claudeHookSpawnPrep returns the --settings prefix args and env vars to add
// to a fresh claude-code spawn for SessionStart hook registration. Returns
// nil slices when the hook is unavailable (script missing or settings JSON
// build fails) so the spawn proceeds without the hook — matching the
// pre-feature behaviour rather than failing the whole spawn. Logs a warning
// if userArgs already contain --settings; Claude treats later wins, so our
// prepend silently overrides the user's value.
func claudeHookSpawnPrep(quilDir, paneID, hookMode string, userArgs []string) (prefix, env []string) {
	scriptPath := claudehook.ScriptPath(quilDir)
	if err := claudeHookScriptStatFn(scriptPath); err != nil {
		log.Printf("warning: pane %s: claude hook script unavailable (%s): %v — session-id rotation tracking disabled", paneID, scriptPath, err)
		return nil, nil
	}
	js, err := claudehook.BuildSettingsJSON(claudehook.HookCommand(quilDir))
	if err != nil {
		log.Printf("warning: pane %s: build claude settings JSON: %v — session-id rotation tracking disabled", paneID, err)
		return nil, nil
	}
	for _, a := range userArgs {
		if a == "--settings" {
			log.Printf("warning: pane %s: claude-code args already contain --settings; Quil's hook entry will override (later-wins)", paneID)
			break
		}
	}
	mode := hookMode
	if mode == "" {
		mode = "default"
	}
	return []string{"--settings", js}, []string{
		"QUIL_PANE_ID=" + paneID,
		"QUIL_HOOK_MODE=" + mode,
	}
}

// escapeClaudeCWD mirrors Claude Code's on-disk naming for per-project
// session directories under ~/.claude/projects/. Path separators, the
// Windows drive-letter colon, AND the underscore all become '-'.
//
// The underscore case is the one that silently broke restore: a macOS
// home like /Users/Foo_Bar lands under ~/.claude/projects/-Users-Foo-Bar
// (not -Users-Foo_Bar), so the on-disk probe with an underscore-preserving
// encoding always returned false and every Claude pane fell back to
// --continue at restart. Confirmed against real directories on macOS
// (Jun 2026) and Windows (E:\Projects\Stukans → "E--Projects-Stukans").
//
// Other non-alphanumeric characters Claude may also encode (spaces, dots)
// are not handled here — no concrete examples observed in the wild yet.
// Extend the replacer when a real path forces the issue.
func escapeClaudeCWD(cwd string) string {
	r := strings.NewReplacer(":", "-", `\`, "-", "/", "-", "_", "-")
	return r.Replace(cwd)
}

// claudeSessionFileExists reports whether Claude has persisted a session
// file for the given CWD + session ID. Called on the restore path for
// claude-code panes; a true result means the pane can resume its own
// unique session, a false result forces the --continue fallback.
//
// Any os.Stat error (including permission denial or the home dir being
// unavailable) returns false — the fallback path is always safe.
func claudeSessionFileExists(cwd, sessionID string) bool {
	if cwd == "" || sessionID == "" {
		return false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	path := filepath.Join(home, ".claude", "projects", escapeClaudeCWD(cwd), sessionID+".jsonl")
	_, err = os.Stat(path)
	return err == nil
}

// resumeTemplateFor returns the resume-arg template resolveSpawnArgs should
// expand on the restore branch. Dispatches by plugin name to plugin-specific
// promotion logic; default falls back to the plugin's configured ResumeArgs.
func resumeTemplateFor(p *plugin.PanePlugin, pane *Pane) []string {
	switch {
	case p.Name == "claude-code" && p.Persistence.Strategy == "preassign_id":
		return claudeResumeTemplate(p, pane)
	case p.Name == "opencode" && p.Persistence.Strategy == "session_scrape":
		return opencodeResumeTemplate(p, pane)
	default:
		return p.Persistence.ResumeArgs
	}
}

// claudeResumeTemplate decides between --resume <id> (unique session) and
// the configured fallback (typically --continue) for a claude-code pane.
//
// Prefers the id recorded by the SessionStart hook — it reflects any /clear,
// /resume, or compaction rotation that happened after the original
// preassigned id was generated. A missing or empty value falls through to
// the original probe so panes on older Quil installs still work.
func claudeResumeTemplate(p *plugin.PanePlugin, pane *Pane) []string {
	if hookID, err := readHookSessionIDFn(pane.ID); err == nil && hookID != "" {
		if claudeSessionExistsFn(pane.CWD, hookID) {
			pane.PluginMu.Lock()
			if pane.PluginState == nil {
				pane.PluginState = make(map[string]string)
			}
			pane.PluginState["session_id"] = hookID
			pane.PluginMu.Unlock()
			return []string{"--resume", "{session_id}"}
		}
	}

	// Snapshot the preassigned id under PluginMu so a concurrent scraper
	// goroutine cannot mutate the map underneath us. Disk probe runs after
	// the lock is released — never hold a mutex across syscalls.
	pane.PluginMu.Lock()
	sessionID := ""
	if pane.PluginState != nil {
		sessionID = pane.PluginState["session_id"]
	}
	pane.PluginMu.Unlock()
	if sessionID != "" && claudeSessionExistsFn(pane.CWD, sessionID) {
		return []string{"--resume", "{session_id}"}
	}
	return p.Persistence.ResumeArgs
}

// opencodeResumeTemplate decides between --session <id> (resume exact
// conversation) and the configured fallback (--continue) for an opencode
// pane.
//
// Unlike the claude-code path we do not probe whether the session id still
// exists in opencode's SQLite DB before passing it: a stale id surfaces a
// clear, actionable error from opencode itself, while a probe would tie us
// to opencode's schema. If that proves too noisy in practice we can add a
// SQLite probe later (file: ~/.local/share/opencode/opencode.db).
//
// Shape-validates the recorded id via opencodehook.IsValidSessionID (mirror
// of the JS plugin's SESSION_ID_RE) before promoting — guards against a
// corrupted file, partial write surviving rename, or manual edit injecting
// arbitrary text into the spawn argv.
func opencodeResumeTemplate(p *plugin.PanePlugin, pane *Pane) []string {
	hookID, err := readOpencodeSessionIDFn(pane.ID)
	if err != nil || hookID == "" {
		return p.Persistence.ResumeArgs
	}
	if !opencodehook.IsValidSessionID(hookID) {
		log.Printf("warning: pane %s: recorded opencode session id failed shape validation (%q); falling back to %v", pane.ID, hookID, p.Persistence.ResumeArgs)
		return p.Persistence.ResumeArgs
	}
	pane.PluginMu.Lock()
	if pane.PluginState == nil {
		pane.PluginState = make(map[string]string)
	}
	pane.PluginState["session_id"] = hookID
	pane.PluginMu.Unlock()
	return []string{"--session", "{session_id}"}
}

// templateHasPlaceholder reports whether any entry contains a `{key}` token
// that ExpandResumeArgs would need to substitute. Used by resolveSpawnArgs
// to decide whether a static template can pass through without PluginState
// (covers the session_scrape fallback for opencode panes that never received
// a session event before the daemon restart).
func templateHasPlaceholder(template []string) bool {
	for _, a := range template {
		if strings.Contains(a, "{") && strings.Contains(a, "}") {
			return true
		}
	}
	return false
}

// resolveSpawnArgs computes the argv (excluding cmd) that spawnPane should use
// for the given pane and plugin, applying base args, the InstanceArgs override,
// preassign_id start args, and the restore-branch resume-args append. It is a
// pure function — no external state, no PTY, no UUID generation — so the
// arg-merging matrix can be table-tested. Callers (i.e. spawnPane) are
// responsible for populating pane.PluginState["session_id"] before invoking
// this function on the fresh-start preassign_id path.
func resolveSpawnArgs(p *plugin.PanePlugin, pane *Pane, restoring bool) []string {
	args := append([]string{}, p.Command.Args...)

	// Instance-specific args override base args.
	if len(pane.InstanceArgs) > 0 {
		args = append([]string{}, pane.InstanceArgs...)
	}

	// Fresh start under preassign_id: append the plugin's StartArgs (after
	// {placeholder} expansion from PluginState).
	if !restoring && p.Persistence.Strategy == "preassign_id" {
		if len(p.Persistence.StartArgs) > 0 {
			startArgs := plugin.ExpandResumeArgs(p.Persistence.StartArgs, pane.PluginState)
			if startArgs != nil {
				args = append(args, startArgs...)
			}
		}
	}

	// Resume branch: append ResumeArgs to whatever args already exist so
	// InstanceArgs (e.g., "--dangerously-skip-permissions" from a setup
	// toggle) survives daemon restart. Before this fix, args were replaced
	// outright, dropping any runtime toggles the user had enabled.
	if restoring {
		switch p.Persistence.Strategy {
		case "preassign_id", "session_scrape":
			template := resumeTemplateFor(p, pane)
			if len(template) > 0 {
				// Static templates (no {placeholder}) pass through directly so
				// a session_scrape pane that never received a hook event still
				// gets its --continue fallback. Templates with placeholders
				// require PluginState; ExpandResumeArgs returns nil if state
				// is missing or any placeholder is unresolved.
				if templateHasPlaceholder(template) {
					if len(pane.PluginState) > 0 {
						if resumeArgs := plugin.ExpandResumeArgs(template, pane.PluginState); resumeArgs != nil {
							args = append(args, resumeArgs...)
						}
					}
				} else {
					args = append(args, template...)
				}
			}
		case "rerun":
			// args already set from InstanceArgs above
		case "none":
			// Don't restore — but we still spawn for now (pane exists in workspace)
		// "cwd_only": just start fresh with CWD (default behavior)
		}
	}

	return args
}

// defaultCWD returns the best working directory for a new pane: the last
// known client CWD (from the most recent TUI attach) if it still points at
// an existing directory, falling back to the daemon's own working
// directory. Symlinks are resolved so all callers see the canonical path.
func (d *Daemon) defaultCWD() string {
	if p := d.clientCWD.Load(); p != nil && *p != "" {
		if info, err := os.Stat(*p); err == nil && info.IsDir() {
			if resolved, err := filepath.EvalSymlinks(*p); err == nil {
				return resolved
			}
			return *p
		}
		// stale (directory removed since attach) — fall through
	}
	// Best-effort; if Getwd fails we return "" and the spawn will fail
	// with a clear error from os/exec rather than silently land somewhere.
	cwd, _ := os.Getwd()
	return cwd
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

	// Generate a session UUID for fresh preassign_id panes before computing
	// args, since resolveSpawnArgs expects PluginState["session_id"] to be
	// populated for the {session_id} expansion.
	if !restoring && p.Persistence.Strategy == "preassign_id" {
		pane.PluginMu.Lock()
		if pane.PluginState == nil {
			pane.PluginState = make(map[string]string)
		}
		if pane.PluginState["session_id"] == "" {
			pane.PluginState["session_id"] = uuid.New().String()
		}
		pane.PluginMu.Unlock()
	}

	args := resolveSpawnArgs(p, pane, restoring)

	// Shell integration (only for terminal-type panes)
	if p.Command.ShellIntegration {
		shellCfg := shellinit.Configure(cmd, config.QuilDir())
		if shellCfg != nil {
			ptySession.SetEnv(shellCfg.Env)
			cmd = shellCfg.Cmd
			args = shellCfg.Args
		}
	}

	// Claude Code session-id rotation tracking: prepend --settings with an
	// inline JSON that registers a SessionStart hook. The hook receives
	// Claude's session_id and writes it to $QUIL_HOME/sessions/<paneID>.id,
	// which the restore path consults in resumeTemplateFor. QUIL_PANE_ID in
	// the PTY env lets the hook attribute the write to this specific pane.
	//
	// OpenCode session-id rotation tracking uses the same pattern but routes
	// through OPENCODE_CONFIG_CONTENT (inline JSON) referencing a JS plugin
	// under $QUIL_HOME/opencodehook/. OPENCODE_CONFIG_CONTENT merges with the
	// user's own opencode config so their plugins/agents/modes still apply.
	envVars := append([]string{}, p.Command.Env...)
	switch p.Name {
	case "claude-code":
		settingsArgs, hookEnv := claudeHookSpawnPrep(config.QuilDir(), pane.ID, d.cfg.Notification.Hooks.Claude, args)
		if len(settingsArgs) > 0 {
			args = append(settingsArgs, args...)
		}
		envVars = append(envVars, hookEnv...)
	case "opencode":
		envVars = append(envVars, opencodeSpawnPrep(config.QuilDir(), pane.ID, d.cfg.Notification.Hooks.OpenCode)...)
	}

	if len(envVars) > 0 {
		ptySession.SetEnv(envVars)
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

	ptySession.SetCWD(pane.CWD)
	log.Printf("spawn: pane %s cmd=%s args=%v cwd=%s restoring=%v", pane.ID, cmd, args, pane.CWD, restoring)
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

// findEventSince delegates to the event queue's catch-up scan. Returns the
// oldest queued event newer than sinceUnixMilli matching paneFilter (empty
// filter = any pane), or nil. Used by watch_notifications's race-closing
// short-circuit before a watcher is registered.
func (d *Daemon) findEventSince(sinceUnixMilli int64, paneFilter map[string]bool) *PaneEvent {
	return d.events.FindSince(sinceUnixMilli, paneFilter)
}

// emitEvent pushes an event to the queue and broadcasts to all clients.
// Events from muted panes are dropped entirely — neither queued nor broadcast.
// Mute is a per-pane signal-quality control: panes like `npm test --watch`
// fire idle handlers on every iteration, and the only sane treatment is to
// silence them at the source. Process-exit on a muted pane is also silenced —
// once you say "stop telling me about this pane", we honor it.
func (d *Daemon) emitEvent(e PaneEvent) {
	if e.PaneID != "" {
		if pane := d.session.Pane(e.PaneID); pane != nil {
			pane.PluginMu.Lock()
			muted := pane.Muted
			pane.PluginMu.Unlock()
			if muted {
				return
			}
		}
	}
	d.events.Push(e)
	payload := toPaneEventPayload(e)
	msg, _ := ipc.NewMessage(ipc.MsgPaneEvent, payload)
	if d.server != nil {
		d.server.Broadcast(msg)
	}
}

// idleChecker runs a periodic check for panes that have gone idle.
// hookEventsWatcher polls the hook event spool every 200 ms while the daemon
// runs, submitting each new payload to the Ingester which then forwards
// (after rate-limit + coalesce) to emitHookEvent. Mirrors idleChecker's
// shutdown discipline: select on d.shutdown so Stop() drains cleanly.
//
// 200 ms is a tradeoff between latency and CPU. With the spool being just
// stat+seek+read per file, ten panes cost ~50 µs/tick — negligible — while
// a 200 ms p99 latency from hook fire to sidebar render keeps the user's
// perception of "instant" intact.
func (d *Daemon) hookEventsWatcher() {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-d.shutdown:
			// Final drain so any in-flight bursts surface before close.
			if d.hookIngester != nil {
				d.hookIngester.FlushAll()
			}
			return
		case <-ticker.C:
			if d.hookSpool == nil || d.hookIngester == nil {
				continue
			}
			for _, p := range d.hookSpool.Tick() {
				d.hookIngester.Submit(p)
			}
		}
	}
}

// emitHookEvent is the bridge from hookevents.Payload (post rate-limit and
// coalesce) to the daemon's PaneEvent emission funnel. Looks up the pane
// to enrich with TabID/Name (which the hook side does not know), marks the
// pane HookHealthy so the legacy idle checker steps aside, then routes
// through the existing emitEvent so mute, aggregation, and the broadcast
// path all apply.
//
// A pane that has been destroyed between the hook write and the spool
// read silently drops here — the lookup returns nil and we return without
// emit. Same trust boundary as the rest of the IPC surface.
func (d *Daemon) emitHookEvent(p hookevents.Payload) {
	pane := d.session.Pane(p.PaneID)
	if pane == nil {
		logger.Debug("hook event for unknown pane=%s src=%s hook_event=%s",
			p.PaneID, p.Source, p.HookEvent)
		return
	}

	pane.PluginMu.Lock()
	pane.HookHealthy = true
	pane.LastHookEventAt = time.Now()
	pane.PluginMu.Unlock()

	// Compose the PaneEvent. The Type field encodes the source so MCP
	// consumers can filter by "hook.claude.*" or "hook.opencode.*" without
	// parsing the title. Severity defaults to info when the hook omitted it.
	severity := p.Severity
	if severity == "" {
		severity = hookevents.SeverityInfo
	}
	eventType := "hook." + p.Source + "." + p.HookEvent
	ts := time.UnixMilli(p.TsMs)
	if p.TsMs == 0 {
		ts = time.Now()
	}

	// Copy Data so the Payload's map is not aliased downstream — the
	// Ingester may still hold a reference, and emitEvent's aggregation may
	// mutate Data["count"].
	var data map[string]string
	if len(p.Data) > 0 {
		data = make(map[string]string, len(p.Data)+2)
		for k, v := range p.Data {
			data[k] = v
		}
	}
	// Enrich with source-tracking metadata so MCP consumers do not need to
	// re-parse the Type prefix.
	if data == nil {
		data = make(map[string]string, 2)
	}
	data["hook_source"] = p.Source
	data["hook_event"] = p.HookEvent

	d.emitEvent(PaneEvent{
		ID:        uuid.New().String(),
		PaneID:    p.PaneID,
		TabID:     pane.TabID,
		PaneName:  pane.Name,
		Type:      eventType,
		Title:     p.Title,
		Message:   data["preview"], // optional excerpt-like preview from the hook
		Severity:  severity,
		Timestamp: ts,
		Data:      data,
	})
}

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
			// Suppress the legacy idle excerpt when the pane's hook is
			// actively delivering ground-truth events. A 30 s grace period
			// catches the case where hooks load successfully but the AI
			// tool sits quiet for an extended turn — the legacy idle then
			// reactivates as a fallback so the user is never left with
			// zero notification signal.
			hookActive := pane.HookHealthy && now.Sub(pane.LastHookEventAt) < 30*time.Second
			shouldFire := !pane.IdleNotified &&
				!pane.LastOutputAt.IsZero() &&
				pane.ExitCode == nil &&
				!hookActive &&
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

			title, severity, excerpt := d.analyzeIdleTitle(pane)
			// Skip prompt-only idle events: shells legitimately idle at a
			// shell prompt are not a state change worth notifying. We only
			// suppress when the default "Output idle" title fired — if a
			// plugin idle handler matched (e.g. claude-code's "Needs your
			// approval"), the regex saw something meaningful in the excerpt
			// even though the surface chars collapse to a prompt rune.
			suppress := title == "Output idle" && isPromptOnlyExcerpt(excerpt)
			// Diagnostic: structural metadata only, NEVER the raw excerpt
			// content. Terminal panes can contain secrets (`echo $API_KEY`,
			// `mysql -p…`, `cat .env`) — even at debug level we must not log
			// user-provided content per observability-and-logging.md. Length
			// + line count + line-end class are sufficient to diagnose
			// suppression decisions (the OSC 0 leak case shows up as
			// excerpt_lines=1 line_end_class=text, normal shell prompts as
			// line_end_class=prompt_rune).
			logger.Debug("idle decision: pane=%s type=%s title=%q suppress=%v excerpt_bytes=%d excerpt_lines=%d",
				pane.ID, pane.Type, title, suppress, len(excerpt), countNonEmptyLines(excerpt))
			if suppress {
				// Roll back the cooldown bookkeeping: we DID NOT emit, so
				// the next real activity should fire promptly instead of
				// waiting out a fake 30 s cooldown. IdleNotified stays true
				// — flushPaneOutput resets it on the next byte from the PTY,
				// so we won't re-evaluate the same idle state every tick.
				pane.PluginMu.Lock()
				pane.LastIdleEventAt = time.Time{}
				pane.PluginMu.Unlock()
				continue
			}
			d.emitEvent(withExcerpt(PaneEvent{
				ID:        uuid.New().String(),
				PaneID:    pane.ID,
				TabID:     pane.TabID,
				PaneName:  pane.Name,
				Type:      "output_idle",
				Title:     title,
				Severity:  severity,
				Timestamp: now,
			}, excerpt))
		}
	}
}

// analyzeIdleTitle determines the notification title/severity by matching
// the last few lines of pane output against plugin idle handlers. The
// excerpt is the same text used for regex matching — returned so the caller
// can attach it to the event without a second buffer read.
func (d *Daemon) analyzeIdleTitle(pane *Pane) (title, severity, excerpt string) {
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
	if pane.OutputBuf == nil {
		return
	}
	raw := pane.OutputBuf.Bytes()
	if len(raw) == 0 {
		return
	}
	stripped := ansi.Strip(string(trimToNewlineSafe(raw, 4096)))
	excerpt = lastNLines(stripped, 5)
	if len(p.IdleHandlers) == 0 || excerpt == "" {
		return
	}
	if ih := plugin.MatchIdle(p, excerpt); ih != nil {
		title = ih.Title
		severity = ih.Severity
	}
	return
}

// lastNLines returns the last n non-empty lines from text, applying terminal
// carriage-return semantics per line. A real terminal interprets `\r` as
// "return to column 0 and overwrite from there" — so when ansi.Strip leaves
// `prompt   \r \r\rwindow-title-leak` in a single line, what the user
// actually SEES is the trailing segment after the last `\r`. Without this
// reset, excerpts capture text the user can never see (e.g. the prompt
// rune that was immediately overwritten) and miss the text they DO see.
func lastNLines(text string, n int) string {
	lines := strings.Split(text, "\n")
	var result []string
	for i := len(lines) - 1; i >= 0 && len(result) < n; i-- {
		line := lines[i]
		if cr := strings.LastIndex(line, "\r"); cr >= 0 {
			line = line[cr+1:]
		}
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			result = append([]string{trimmed}, result...)
		}
	}
	return strings.Join(result, "\n")
}

// paneOutputExcerpt extracts the last n non-empty stripped lines from a pane's
// ring buffer. Used to enrich notification events with the context that
// triggered them so the sidebar and MCP consumers can show something more
// informative than the title alone. Returns "" if the buffer is empty.
//
// Reads only the trailing 4 KiB of the ring buffer — enough for ~50 wrapped
// lines on a typical terminal, far more than n=3 needs, and bounded so the
// per-event cost stays negligible even for panes with very large buffers.
func paneOutputExcerpt(pane *Pane, n int) string {
	if pane == nil || pane.OutputBuf == nil {
		return ""
	}
	raw := pane.OutputBuf.Bytes()
	if len(raw) == 0 {
		return ""
	}
	return lastNLines(ansi.Strip(string(trimToNewlineSafe(raw, 4096))), n)
}

// trimToNewlineSafe returns the trailing window of raw, advancing past any
// partial ANSI escape sequence at the slice boundary. Without this guard, a
// 4 KiB tail slice can begin in the middle of a CSI sequence — the leading
// `\x1b[` ended up in the discarded prefix, but parameters like
// `2;30;30;30m` or `;18H` survive into the window and ansi.Strip can no
// longer recognise them as part of an escape. They then render to the user
// as raw garbage.
//
// We scan forward bounded by maxScan bytes looking for either:
//   - a newline (clean text restart), or
//   - an ESC byte (0x1b — start of a fresh ANSI sequence that ansi.Strip
//     will recognise in full).
//
// Whichever boundary comes first wins. Newline-only seek wasn't enough:
// some TUIs (Claude Code, opencode) emit one logical "screen paint" with
// few or no newlines in the trailing window, so the seek fell through and
// we returned the un-advanced slice — the original bug shape. ESC bytes
// are abundant in ANSI-rich panes, so finding one is fast.
//
// If neither boundary is found within maxScan, we accept the un-advanced
// slice — the chance of a leading partial sequence in 4 KiB of plain text
// is small relative to the bytes the user sees.
func trimToNewlineSafe(raw []byte, maxTail int) []byte {
	if len(raw) <= maxTail {
		return raw
	}
	start := len(raw) - maxTail
	const maxScan = 512
	upper := start + maxScan
	if upper > len(raw) {
		upper = len(raw)
	}
	for i := start; i < upper; i++ {
		switch raw[i] {
		case '\n':
			return raw[i+1:]
		case 0x1b:
			return raw[i:]
		}
	}
	return raw[start:]
}

// promptRunes are the canonical interactive shell prompt terminators.
// An idle excerpt that strips down to one of these (and nothing else) means
// the pane is sitting at a fresh prompt — a non-event from the user's POV,
// because they can see the prompt by looking at the pane.
var promptRunes = map[string]bool{
	"%": true, // zsh default
	"$": true, // bash / sh
	">": true, // PowerShell / cmd, also some Python REPLs
	"❯": true, // starship / pure / spaceship default
	"#": true, // root prompts
	"➜": true, // oh-my-zsh agnoster / af-magic
	"λ": true, // fish-friendly minimal themes
	"»": true, // bash-it powerline
}

// hostnameLikeRe matches user@host patterns (e.g. "user_name@host01")
// that leak into excerpts from OSC 0 window-title sequences when ansi.Strip
// or upstream emulators bail on an embedded CR. These leaks are
// indistinguishable from "the pane is at a prompt" because the underlying
// terminal state IS a fresh prompt — the title text is what survived the
// strip, not what the cursor is sitting on.
var hostnameLikeRe = regexp.MustCompile(`^[\w][\w.-]*@[\w][\w.-]+`)

// isPromptOnlyExcerpt reports whether the excerpt represents a pane sitting
// at an idle shell prompt. We classify a line as "prompt-like" when it is:
//
//   - a single canonical prompt rune (`%`, `$`, `❯`, etc.), OR
//   - short (< 200 chars) AND contains a prompt rune somewhere (e.g.
//     "user@host % git:(main)"), OR
//   - short AND starts with a user@host pattern — the OSC 0 leak signature.
//
// The excerpt is prompt-only when every non-empty line passes these checks.
// "Short" matters: a multi-line `ls` output that happens to contain a `%`
// in one filename should NOT collapse to "shell idle" — only lines that
// could realistically be a prompt qualify.
func isPromptOnlyExcerpt(excerpt string) bool {
	if excerpt == "" {
		return false
	}
	sawAny := false
	for _, line := range strings.Split(excerpt, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		sawAny = true
		if !isPromptLikeLine(trimmed) {
			return false
		}
	}
	return sawAny
}

// isPromptLikeLine encapsulates the per-line classification used by
// isPromptOnlyExcerpt. See that function's docs for the classification rules.
//
// Specifically, a line is "prompt-like" when, after trimming trailing
// whitespace, it is:
//
//   - the bare prompt rune by itself (e.g. "%"), OR
//   - a recognised prompt rune as the trailing token, preceded by whitespace
//     (e.g. "user@host %", "~/repo $ ", "❯ "), OR
//   - matches the user@host pattern that OSC 0 window-title leaks produce.
//
// The space-before-rune requirement is what distinguishes a real prompt
// from a number-with-percent (`"build complete: 100%"`) or a literal text
// ending in a prompt-like rune (`"x$"`). Without it the classifier would
// suppress legitimate command output that happens to end in a prompt rune.
// Long lines (> 200 chars) are presumed to be command output regardless of
// trailing chars — real prompts are short.
func isPromptLikeLine(line string) bool {
	if line == "" {
		return true
	}
	if promptRunes[line] {
		return true
	}
	if len(line) > 200 {
		return false
	}
	trimmed := strings.TrimRight(line, " \t")
	for r := range promptRunes {
		if !strings.HasSuffix(trimmed, r) {
			continue
		}
		// Bare prompt rune (e.g. trimmed == "%").
		if trimmed == r {
			return true
		}
		// Prompt rune preceded by whitespace (e.g. "user@host %"). The byte
		// immediately before the rune must be a space or tab — that's what
		// makes it a standalone prompt terminator instead of part of a
		// word like "100%" or "x$".
		runeStart := len(trimmed) - len(r)
		if runeStart > 0 {
			prev := trimmed[runeStart-1]
			if prev == ' ' || prev == '\t' {
				return true
			}
		}
	}
	if hostnameLikeRe.MatchString(line) {
		return true
	}
	return false
}

// countNonEmptyLines returns the number of non-blank lines in s. Used for
// structural diagnostics in the idle-decision debug log so we can surface
// excerpt shape ("N lines, M bytes") without echoing the raw content.
func countNonEmptyLines(s string) int {
	if s == "" {
		return 0
	}
	n := 0
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

// withExcerpt populates PaneEvent.Message and Data["excerpt"] from the pane's
// tail output. Idempotent: callers that already extracted the excerpt (e.g.
// the idle checker, which needs it for regex matching) can pass excerpt
// directly and skip the second buffer read.
func withExcerpt(e PaneEvent, excerpt string) PaneEvent {
	if excerpt == "" {
		return e
	}
	e.Message = excerpt
	if e.Data == nil {
		e.Data = make(map[string]string)
	}
	e.Data["excerpt"] = excerpt
	return e
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
		cwd = d.defaultCWD()
	}

	// Validate CWD exists and is a directory, then re-resolve symlinks so
	// the spawn can't be redirected by a swap between Stat and exec. Failure
	// of EvalSymlinks itself is non-fatal (Windows junctions etc.) — fall
	// back to the lexically validated path.
	if info, err := os.Stat(cwd); err != nil || !info.IsDir() {
		log.Printf("handleCreatePaneReq: invalid cwd %q: %v", cwd, err)
		cwd = d.defaultCWD()
	} else if resolved, evalErr := filepath.EvalSymlinks(cwd); evalErr == nil {
		cwd = resolved
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

	// Same hook-events cleanup as handleDestroyPane: kill the spool file
	// before the pane disappears so the watcher does not race the destroy.
	if d.hookSpool != nil {
		d.hookSpool.Cleanup(req.PaneID)
	}

	tabID := pane.TabID
	if err := d.session.DestroyPane(req.PaneID); err != nil {
		log.Printf("handleDestroyPaneReq: %v", err)
		respondTo(conn, msg.ID, ipc.MsgDestroyPaneResp, ipc.DestroyPaneRespPayload{})
		return
	}

	// Auto-create replacement if last pane in tab (same as handleDestroyPane)
	tab := d.session.Tab(tabID)
	if tab != nil && len(tab.Panes) == 0 {
		newPane, _ := d.session.CreatePane(tabID, d.defaultCWD())
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

	// since_timestamp short-circuit: scan the existing queue for any event
	// newer than the marker that also matches the pane filter. If one
	// exists, return it without ever registering a watcher. This closes the
	// race-on-registration window — events fired between the agent's prior
	// action and this watch call would otherwise be lost.
	if req.SinceTimestamp > 0 {
		if catchup := d.findEventSince(req.SinceTimestamp, paneFilter); catchup != nil {
			payload := toPaneEventPayload(*catchup)
			respondTo(conn, msg.ID, ipc.MsgWatchNotificationsResp, ipc.WatchNotificationsRespPayload{
				Event: &payload,
			})
			return
		}
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

func (d *Daemon) handleMemoryReportReq(conn *ipc.Conn, msg *ipc.Message) {
	snap := d.memReport.Latest()
	resp := ipc.MemoryReportRespPayload{}
	if snap != nil {
		resp.SnapshotAt = snap.At.UnixNano()
		resp.Total = snap.Total
		resp.Panes = make([]ipc.PaneMemInfo, len(snap.Panes))
		for i, p := range snap.Panes {
			resp.Panes[i] = ipc.PaneMemInfo{
				PaneID:      p.PaneID,
				TabID:       p.TabID,
				GoHeapBytes: p.GoHeapBytes,
				PTYRSSBytes: p.PTYRSSBytes,
				TotalBytes:  p.Total,
			}
		}
	}
	// Embed the current tab list so MCP callers don't need a second
	// MsgListTabsReq round-trip just to map tab IDs to human names.
	activeTab, tabs, panesByTab := d.session.SnapshotState()
	resp.Tabs = make([]ipc.TabInfo, 0, len(tabs))
	for _, tab := range tabs {
		resp.Tabs = append(resp.Tabs, ipc.TabInfo{
			ID:        tab.ID,
			Name:      tab.Name,
			Color:     tab.Color,
			PaneCount: len(panesByTab[tab.ID]),
			Active:    tab.ID == activeTab,
		})
	}
	respondTo(conn, msg.ID, ipc.MsgMemoryReportResp, resp)
}
