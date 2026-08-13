package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"regexp"

	"github.com/artyomsv/quil/internal/claudehook"
	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/hookevents"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/logger"
	memreport "github.com/artyomsv/quil/internal/memreport"
	"github.com/artyomsv/quil/internal/opencodehook"
	"github.com/artyomsv/quil/internal/panehistory"
	"github.com/artyomsv/quil/internal/persist"
	"github.com/artyomsv/quil/internal/plugin"
	apty "github.com/artyomsv/quil/internal/pty"
	"github.com/artyomsv/quil/internal/ringbuf"
	"github.com/artyomsv/quil/internal/shellinit"
	"github.com/artyomsv/quil/internal/version"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/google/uuid"
)

// oscBellRe matches OSC sequences terminated by BEL (\x07), e.g., \x1b]0;title\x07.
// Used to strip these before bell detection so OSC terminators aren't treated as bells.
var oscBellRe = regexp.MustCompile(`\x1b\][^\x07]*\x07`)

type Daemon struct {
	cfg          config.Config
	server       *ipc.Server
	session      *SessionManager
	registry     *plugin.Registry
	shutdown     chan struct{}
	shutdownOnce sync.Once
	stopOnce     sync.Once
	snapshotCh   chan struct{} // buffered channel for snapshot requests
	restored     bool          // true if workspace was loaded from disk
	events       *eventQueue   // notification center event queue
	gitCache     *gitCache     // per-checkout branch/worktree/divergence, refreshed on a ticker
	// clientCWD is the last-known CWD from a TUI client, used as the
	// default working directory for new panes/tabs. Read by defaultCWD()
	// from any IPC dispatch goroutine and written by handleAttach on each
	// connect — atomic.Pointer is what keeps that race-free.
	clientCWD atomic.Pointer[string]

	memReport   *memreport.Collector
	collectorWG sync.WaitGroup

	// snapGens records, per pane, the OutputBuf generation captured by the
	// last buffer flush. Equal generation ⇒ identical contents ⇒ the on-disk
	// file already matches and the write is skipped. Only touched inside
	// snapshot(), which is serialized (debounce loop, then the final flush
	// after the loop exits) — no lock needed.
	snapGens map[string]uint64

	// hookIngester translates hookevents.Payload (from spool reads / future
	// IPC submissions) into PaneEvents via emitHookEvent. Lazily initialised
	// in Start once the events dir is ready; nil before Start.
	hookIngester *hookevents.Ingester
	// hookSpool reads $QUIL_HOME/events/<paneID>.jsonl appended by the
	// Claude .sh / opencode .js hook scripts. Polled by hookEventsWatcher
	// every 200 ms while the daemon runs.
	hookSpool *hookevents.Spool

	// lastSnapshotDone is the UnixNano of the last completed snapshot().
	// The snapshot loop is the daemon's liveness canary — it acquires the
	// same locks (sm.mu, per-pane PluginMu) every wedge so far has parked
	// on. snapshotWatchdog dumps all goroutine stacks when this goes stale.
	lastSnapshotDone atomic.Int64

	// updateMu guards updateInfo, the currently-announced newer release
	// (nil = up to date). updateStaging is the single-flight guard for the
	// download/stage pipeline (daily tick vs on-demand request).
	updateMu      sync.Mutex
	updateInfo    *ipc.UpdateInfo
	updateStaging atomic.Bool

	// sessionScanning is the single-flight guard for the Claude session
	// listing (MsgClaudeSessionsReq). One scan reads up to 200 transcript
	// heads off disk — far more work than parsing the frame that requested
	// it — so an unguarded handler lets a looping client accumulate worker
	// goroutines until the daemon is starved.
	sessionScanning atomic.Bool

	// sessionDetailReading is the single-flight guard for the per-session deep
	// read (MsgClaudeSessionDetailReq). Separate from sessionScanning on
	// purpose: sharing one slot would make opening the info panel fail
	// whenever a directory listing happened to be in flight, which is exactly
	// when the user opens it.
	sessionDetailReading atomic.Bool

	// browseScanning is the single-flight guard for the directory listing
	// (MsgBrowseDirReq). Its own slot for the same reason
	// sessionDetailReading has one: the setup dialog browses a directory and
	// then scans it for sessions, so sharing would make each step fail
	// whenever it followed the other closely enough.
	browseScanning atomic.Bool

	// gitDiscovering is the single-flight guard for git repo discovery
	// (MsgGitReposReq). Separate from browseScanning because the setup dialog
	// resolves a directory and then discovers repos inside it, so one shared
	// slot would make each step fail whenever it closely followed the other.
	gitDiscovering atomic.Bool

	// kubeDiscovering is the single-flight guard for kube context discovery
	// (MsgKubeCtxReq). Its own slot, not shared with gitDiscovering: a
	// discover="kube" plugin and a discover="git" one can be opened back to
	// back, and one guard would make each fail whenever it followed the other.
	kubeDiscovering atomic.Bool

	// dirsChecking is the single-flight guard for the recent-locations
	// existence check (MsgDirsExistReq). Its own slot rather than sharing
	// browseScanning, and the reason is timing rather than tidiness: that check
	// hands over to the directory browser when it gives up, the client gives up
	// after 8 s, and this slot is held for as long as the syscall really runs —
	// up to browseTimeout (10 s) when a stat parks on a dead mount. One shared
	// guard would make the handover's browse requests rejected by the very
	// request that just timed out, dead-ending the dialog in exactly the case
	// the bound exists for.
	dirsChecking atomic.Bool

	// worktreeScanning single-flights MsgWorktreeListReq. Its own slot — see
	// handleWorktreeListReq for why it is not browseScanning's.
	worktreeScanning atomic.Bool

	// worktreeAdding serialises worktree CREATION. Its own slot — see
	// beginWorktreeAdd for why it is not worktreeScanning — and it doubles as
	// the permit budget for that path: one add at a time daemon-wide means at
	// most one blocking-FS permit held for worktreeAddTimeout.
	worktreeAdding atomic.Bool

	// resumeClaimMu serializes the claim of a Claude session by a new pane.
	// The occupancy test and the write that acts on it must be one atomic
	// step: handleCreatePane runs on the requesting conn's dispatch
	// goroutine, so two clients creating panes for the same session would
	// otherwise both observe it free and both spawn `claude --resume` on one
	// transcript — the corruption this feature exists to prevent.
	resumeClaimMu sync.Mutex

	// overlayPolicyState holds the live overlay retention settings (idle
	// timeout, max live). Seeded from config at construction; F1 → Settings
	// pushes runtime updates via MsgOverlayPolicy without a daemon restart.
	overlayPolicyState overlayPolicyState

	// attachedConns maps each conn that has sent MsgAttach — the clients, as
	// distinct from every conn (see markClientAttached) — to the set of overlay
	// panes that client currently has ON SCREEN.
	//
	// Visibility is per client rather than one daemon-wide field because
	// otherwise whichever conn spoke last defines it: with two TUIs attached,
	// the second one merely switching tabs reports an overlay hidden while the
	// first has it on screen, and the sweep destroys a lazygit the user is
	// looking at five minutes later. An overlay is hidden only when NO client
	// claims it, which is also what makes a detached session fall out for free
	// — no clients, no claims, everything hidden.
	//
	// Written from each conn's own dispatch goroutine and from the disconnect
	// callback, so it carries its own mutex: sm.mu is the wrong lock here,
	// since a reader parked behind an RWMutex writer is the failure mode this
	// package keeps being bitten by. Nothing that takes PluginMu may be called
	// while it is held.
	attachedMu    sync.Mutex
	attachedConns map[*ipc.Conn]map[string]bool
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
		gitCache:   newGitCache(),
		snapGens:   make(map[string]uint64),
	}
	d.memReport = memreport.NewCollector(d.session, 5*time.Second)
	d.overlayPolicyState.set(ipc.OverlayPolicyPayload{
		IdleTimeoutMinutes: cfg.Overlay.IdleTimeoutMinutes,
		MaxLive:            cfg.Overlay.MaxLive,
	})
	return d
}

func (d *Daemon) Start() error {
	quilDir := config.QuilDir()
	if err := probeExistingDaemon(config.SocketPath()); err != nil {
		return err
	}
	if err := os.MkdirAll(quilDir, 0700); err != nil {
		return fmt.Errorf("create quil dir: %w", err)
	}

	if err := shellinit.EnsureInitDir(quilDir); err != nil {
		log.Printf("warning: failed to write shell init scripts: %v", err)
	}

	// Claude's hook is a native quild subcommand (see claudeHookSpawnPrep) —
	// no script files to write. OpenCode still ships an embedded JS plugin.
	if err := opencodehook.EnsureScripts(quilDir); err != nil {
		log.Printf("warning: failed to write opencode hook scripts: %v", err)
	}
	if err := os.MkdirAll(config.SessionsDir(), 0700); err != nil {
		log.Printf("warning: failed to create sessions dir: %v", err)
	}

	d.seedUpdateInfoFromState()

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

	// Respawn panes for the restored workspace BEFORE the IPC server starts
	// listening. respawnPanes writes pane.Pending (unlocked) and the lazy-spawn
	// path (ensureTabSpawned → ensurePaneSpawned, reachable from a client's
	// MsgSwitchTab) reads/writes it under spawnMu. Doing all spawn/defer
	// decisions before any client can connect makes those writes happen-before
	// every possible concurrent reader — eliminating the data race and the logic
	// gap where a tab switched-to before its Pending flag was set wouldn't spawn.
	// respawnPanes only needs d.session + d.registry; emitEvent (via the
	// streamPTYOutput goroutine) guards on d.server != nil, so a not-yet-assigned
	// server is safe here.
	if d.restored {
		d.respawnPanes()
	}

	sockPath := config.SocketPath()
	d.server = ipc.NewServer(sockPath, d.handleMessage, d.onClientDisconnect)

	if err := d.server.Start(); err != nil {
		return fmt.Errorf("start IPC server: %w", err)
	}

	go d.idleChecker()
	go d.updateChecker()
	go d.hookEventsWatcher()
	go d.gitWatcher()
	// Arm the liveness canary only once a first snapshot is plausible.
	d.lastSnapshotDone.Store(time.Now().UnixNano())
	go d.snapshotWatchdog()

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
			var hookID, transcript string
			switch pane.Type {
			case "claude-code":
				if rec, err := readHookSessionFn(pane.ID); err == nil {
					hookID, transcript = rec.ID, rec.TranscriptPath
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
			// The transcript path is stored as one unit with the id it belongs
			// to: only that pairing makes it evidence. A path left behind while
			// the id moves on would vouch for a transcript nobody checked, so an
			// id arriving without one drops the key rather than keeping the
			// previous session's. This is what lets a restore locate the session
			// after the hook file itself is lost — the path cannot be rebuilt
			// from the pane's CWD, which is the whole reason it is recorded.
			if transcript != "" {
				pane.PluginState["transcript_path"] = transcript
			} else {
				delete(pane.PluginState, "transcript_path")
			}
			// The hook has now confirmed this pane's own session, so the
			// creation-time resume target has served its only purpose: covering
			// the window before the first SessionStart hook fired. Retiring it
			// here keeps it from outliving that window — otherwise a pane that
			// resumed session X, then moved on via /clear, could be pulled back
			// into X by a later restore that finds no hook file. "The
			// conversation I cleared came back" is a surprise worth preventing.
			delete(pane.PluginState, "resume_session_id")
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

// markClientAttached records a connection that has sent MsgAttach.
//
// ATTACHMENT, not connection, is what "a client is here" means, and the
// difference is not academic: every live MCP bridge holds an IPC conn for its
// whole lifetime (cmd/quil/mcp.go dials once and closes on exit), and a bridge
// is a child of the claude process in a PANE — so bridges routinely outlive the
// TUI. Counting raw conns therefore answered "is anything connected", which in
// any session with a claude pane wired to `quil mcp` is permanently yes (21
// conns in the session that reported 7 live overlays), and the detached-session
// stamp below never fired in exactly the configuration it was designed for.
// Re-attaching on the same conn keeps that client's existing overlay claims:
// the entry is created only when absent.
func (d *Daemon) markClientAttached(conn *ipc.Conn) {
	if conn == nil {
		return
	}
	d.attachedMu.Lock()
	if d.attachedConns == nil {
		d.attachedConns = make(map[*ipc.Conn]map[string]bool)
	}
	if _, ok := d.attachedConns[conn]; !ok {
		d.attachedConns[conn] = map[string]bool{}
	}
	d.attachedMu.Unlock()
}

// forgetAttachedClient drops a disconnecting conn, and with it every overlay
// that client claimed visible. A conn that never attached is not in the set, so
// dropping it changes nothing.
func (d *Daemon) forgetAttachedClient(conn *ipc.Conn) {
	d.attachedMu.Lock()
	delete(d.attachedConns, conn)
	d.attachedMu.Unlock()
}

// onClientDisconnect is ipc.Server's disconnect callback.
//
// handleConn's defer removes the disconnecting conn (removeConn) before
// invoking this, and the attached set is keyed on that same conn — so the state
// here is already exclusive of the client that just left.
func (d *Daemon) onClientDisconnect(conn *ipc.Conn) {
	d.requestSnapshot()
	d.events.RemoveWatchersByConn(conn)
	d.forgetAttachedClient(conn)
	if n := d.hideUnclaimedOverlays(time.Now()); n > 0 {
		log.Printf("overlay: %d marked hidden (no client has them on screen)", n)
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

	// Take ONE consistent view of the session and reuse it for both the
	// workspace JSON and the ghost-buffer flush. Calling SnapshotState
	// twice (once via buildWorkspaceState, once for the buffer loop)
	// allowed a pane create/destroy between the two calls to slip through
	// — the workspace.json said N panes while the buffer flush iterated
	// N±1, surfacing as the "snapshot pane count oscillation" bug.
	activeTab, tabs, panesByTab, projects, activeProject := d.session.SnapshotState()
	state := d.workspaceStateFromSnapshot(activeTab, tabs, panesByTab, projects, activeProject, false)

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
			// Type and Overlay are PluginMu-protected (spawnRestoredPane
			// mutates Type on the lazy-spawn fallback path, handleCreatePane
			// sets Overlay post-publication, both concurrently with this
			// snapshot).
			pane.PluginMu.Lock()
			typ := pane.Type
			isOverlay := pane.Overlay
			pane.PluginMu.Unlock()
			// Overlay panes are ephemeral: never write their ghost buffer to
			// disk (they are excluded from workspace.json by design and must
			// not leave orphaned buffer files behind).
			if isOverlay {
				continue
			}
			// Skip ghost buffer save for plugins with GhostBuffer disabled
			if p := d.registry.Get(typ); p != nil && !p.Persistence.GhostBuffer {
				continue
			}
			if pane.OutputBuf != nil {
				gen := pane.OutputBuf.Gen()
				if prev, ok := d.snapGens[pane.ID]; ok && prev == gen {
					continue // unchanged since last flush — file already matches
				}
				if data := pane.OutputBuf.Bytes(); len(data) > 0 {
					buffers[pane.ID] = data
					totalBytes += len(data)
					d.snapGens[pane.ID] = gen
				}
			}
		}
	}

	// Prune generations of destroyed panes so the map tracks only live ones.
	// snapshot() is the sole reader/writer of snapGens (serialized), so this
	// sweep is race-free.
	live := make(map[string]bool, len(activePaneIDs))
	for _, id := range activePaneIDs {
		live[id] = true
	}
	for id := range d.snapGens {
		if !live[id] {
			delete(d.snapGens, id)
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
	d.lastSnapshotDone.Store(time.Now().UnixNano())
}

// snapshotWatchdog turns a wedged daemon into a diagnosable incident. The
// periodic snapshot runs every 30s on the Wait goroutine and takes the same
// locks every production wedge has parked on; if no snapshot completes for
// stallAfter, dump every goroutine stack to the log (throttled) so the
// blocking site is identifiable post-mortem instead of being lost in a
// silent freeze (incidents 2026-06-11/12: zero log evidence of the holder).
func (d *Daemon) snapshotWatchdog() {
	const (
		checkEvery = 30 * time.Second
		stallAfter = 2 * time.Minute
		dumpEvery  = 10 * time.Minute
	)
	var lastDump time.Time
	ticker := time.NewTicker(checkEvery)
	defer ticker.Stop()
	for {
		select {
		case <-d.shutdown:
			return
		case <-ticker.C:
			last := d.lastSnapshotDone.Load()
			if last == 0 {
				continue
			}
			stale := time.Since(time.Unix(0, last))
			if stale < stallAfter || time.Since(lastDump) < dumpEvery {
				continue
			}
			lastDump = time.Now()
			buf := make([]byte, 1<<20)
			n := runtime.Stack(buf, true)
			log.Printf("WATCHDOG: no snapshot completed for %v — daemon may be wedged; goroutine dump follows\n%s",
				stale.Round(time.Second), buf[:n])
		}
	}
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

	// A workspace snapshot written before projects existed has no "projects"
	// key (or an empty one — a snapshot built entirely from RestoreTab, which
	// never touches sm.projects, produces the same shape). Migrating in place
	// here means every tab's "project_id" is stamped by the time the restore
	// loop below reads it, exactly as a post-migration snapshot would have
	// written it directly.
	migrateToDefaultProject(state)

	log.Println("restoring workspace from disk...")

	activeTab, _ := state["active_tab"].(string)
	tabs, _ := state["tabs"].([]any)
	panes, _ := state["panes"].([]any)
	activeProject, _ := state["active_project"].(string)

	d.session.RestoreProjects(parseRestoredProjects(state["projects"]), activeProject)

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
		// A rebuilt *Tab with an empty ProjectID makes DestroyTab's project
		// de-registration a silent no-op, leaving a dangling ID in
		// Project.TabIDs that only surfaces after this restart, when someone
		// closes the tab. migrateToDefaultProject above guarantees every tab
		// map carries "project_id" by the time this loop runs.
		tab.ProjectID, _ = tabMap["project_id"].(string)

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

				// Restore last known size (JSON numbers decode as float64;
				// absent on pre-size snapshots → stays 0 and respawn falls
				// back to the default PTY dimensions).
				cols, _ := paneData["cols"].(float64)
				rows, _ := paneData["rows"].(float64)
				muted, _ := paneData["muted"].(bool)
				eager, _ := paneData["eager"].(bool)
				pinnedAttention, _ := paneData["pinned_attention"].(bool)
				worktreeOwned, _ := paneData["worktree_owned"].(bool)

				pane := &Pane{
					ID:           paneID,
					TabID:        tabID,
					CWD:          cwd,
					Name:         name,
					Type:         paneType,
					Cols:         int(cols),
					Rows:         int(rows),
					PluginState:  pluginState,
					InstanceName: instanceName,
					InstanceArgs: instanceArgs,
					OutputBuf:    ringbuf.NewRingBuffer(d.session.bufSize),
					Muted:        muted,
					Eager:        eager,
					// Absent on pre-pin snapshots → false, which is the only
					// safe default: inventing a mark the user never set would
					// put a "look here" on a pane with nothing to look at, and
					// the mark is deliberately un-clearable by anything but the
					// user.
					PinnedAttention: pinnedAttention,
					// Absent on pre-worktree snapshots → false, which is the
					// right default: a pane nobody recorded as owning a
					// worktree keeps the ordinary CWD fallback.
					WorktreeOwned: worktreeOwned,
				}

				// Load ghost buffer from disk
				if bufData, err := persist.LoadBuffer(bufDir, paneID); err == nil && len(bufData) > 0 {
					pane.OutputBuf.Write(bufData)
					pane.ghostSeeded = true
					pane.GhostSnap = make([]byte, len(bufData))
					copy(pane.GhostSnap, bufData)
					pane.HistoryLines = bytes.Count(bufData, []byte{'\n'})
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

// parseRestoredProjects turns the disk-loaded "projects" list (already
// present pre-migration, or synthesized in place by migrateToDefaultProject)
// back into typed *Project values for SessionManager.RestoreProjects.
//
// An empty RootDir is filled from the daemon's own os.Getwd() — resolved at
// most once for the whole call, not per project — because
// migrateToDefaultProject deliberately leaves RootDir blank rather than
// guessing it (see that function's doc comment).
func parseRestoredProjects(raw any) []*Project {
	rawList, ok := raw.([]any)
	if !ok {
		return nil
	}
	var cwd string
	var cwdResolved bool
	projects := make([]*Project, 0, len(rawList))
	for _, rp := range rawList {
		pm, ok := rp.(map[string]any)
		if !ok {
			continue
		}
		id, _ := pm["id"].(string)
		if id == "" {
			continue
		}
		name, _ := pm["name"].(string)
		rootDir, _ := pm["root_dir"].(string)
		if rootDir == "" {
			if !cwdResolved {
				cwd, _ = os.Getwd()
				cwdResolved = true
			}
			rootDir = cwd
		}
		activeTab, _ := pm["active_tab"].(string)
		bootstrap, _ := pm["bootstrap"].(bool)
		var tabIDs []string
		if rawIDs, ok := pm["tab_ids"].([]any); ok {
			tabIDs = make([]string, 0, len(rawIDs))
			for _, rid := range rawIDs {
				if s, ok := rid.(string); ok {
					tabIDs = append(tabIDs, s)
				}
			}
		}
		projects = append(projects, &Project{
			ID:        id,
			Name:      name,
			RootDir:   rootDir,
			TabIDs:    tabIDs,
			ActiveTab: activeTab,
			Bootstrap: bootstrap,
		})
	}
	return projects
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

// respawnPanes starts processes for restored panes. Only the active tab's
// panes and panes flagged Eager are spawned immediately; everything else is
// marked Pending and spawned lazily on first access (tab switch or MCP op).
// This keeps a large-workspace restart from launching N heavy children at once.
func (d *Daemon) respawnPanes() {
	active := d.session.ActiveTabID()
	for _, tab := range d.session.Tabs() {
		for _, pane := range d.session.Panes(tab.ID) {
			if pane.PTY != nil {
				continue // Already has a PTY
			}
			if tab.ID == active || pane.Eager {
				d.spawnRestoredPane(pane)
			} else {
				pane.Pending = true
				log.Printf("respawn: deferring pane %s (type=%s, tab=%s)", pane.ID, pane.Type, tab.ID)
			}
		}
	}
}

// refuseMissingWorktree reports whether a worktree-owned pane's directory is
// gone, and if so records why instead of spawning.
//
// A worktree-owned pane does NOT relocate. Blanking the CWD reaches
// d.defaultCWD(), so the pane would return in the main checkout on whatever
// branch that is — and for a claude pane that is worse than a wrong directory,
// because it still resumes its recorded session, continuing the conversation
// against the wrong tree. It comes up visibly broken instead: the pane exists,
// its CWD stands, and the failure is on screen rather than in a log nobody
// reads.
//
// Ordinary panes keep the blank-and-fall-back recovery. The losses are not the
// same: a stale browsed path costs a convenience, a missing worktree costs the
// isolation the pane exists for.
//
// Shared by restore AND restart deliberately. Alt+R is the remedy the error
// screen advertises, so it has to reach the same verdict — otherwise a retry
// while the worktree is still missing spawns a shell in the main checkout,
// which is the relocation this whole path exists to prevent.
func (d *Daemon) refuseMissingWorktree(pane *Pane) bool {
	pane.PluginMu.Lock()
	cwd, owned := pane.CWD, pane.WorktreeOwned
	pane.PluginMu.Unlock()
	if !owned || cwd == "" {
		return false
	}
	if info, err := os.Stat(cwd); err == nil && info.IsDir() {
		return false
	}
	log.Printf("pane %s: worktree %q gone, leaving the pane unspawned", pane.ID, cwd)
	pane.PluginMu.Lock()
	pane.SpawnError = fmt.Sprintf("worktree is gone: %s", cwd)
	pane.PluginMu.Unlock()
	return true
}

// spawnRestoredPane spawns a single restored pane, applying the saved-cwd
// sanity check and the fallback-to-terminal recovery. Extracted from
// respawnPanes so the lazy-spawn path (ensurePaneSpawned) reuses it verbatim.
func (d *Daemon) spawnRestoredPane(pane *Pane) {
	ptySession := newRestoredPTY(paneSize(pane))
	if d.refuseMissingWorktree(pane) {
		return
	}
	// !WorktreeOwned as well as the early return above, so the no-relocation
	// invariant is enforced AT the site that relocates. The two stats are
	// separate calls and can disagree — a race, or a transient error on a
	// network mount — and without this a worktree-owned pane could still be
	// blanked into d.defaultCWD(), the one outcome this path exists to
	// prevent.
	// Both fields read under PluginMu: every writer holds it, and this runs
	// with the IPC server live via ensurePaneSpawned, so an unlocked read
	// races snapshot()/buildPaneInfos.
	pane.PluginMu.Lock()
	cwd, worktreeOwned := pane.CWD, pane.WorktreeOwned
	pane.PluginMu.Unlock()
	if cwd != "" && !worktreeOwned {
		if info, err := os.Stat(cwd); err != nil || !info.IsDir() {
			log.Printf("pane %s: saved cwd %q gone, using default", pane.ID, cwd)
			// PluginMu-protected: snapshot()/buildPaneInfos/handlePaneStatusReq
			// read pane.CWD concurrently while the server is live (lazy spawn).
			// Single-field critical section — never nested with spawnPane (which
			// takes PluginMu itself).
			pane.PluginMu.Lock()
			pane.CWD = ""
			pane.PluginMu.Unlock()
		}
	}
	if err := d.spawnPane(pane, ptySession, true); err != nil {
		log.Printf("respawn pane %s (type=%s): %v — falling back to terminal", pane.ID, pane.Type, err)
		// PluginMu-protected: same concurrent readers as pane.CWD above.
		pane.PluginMu.Lock()
		pane.Type = "terminal"
		pane.PluginMu.Unlock()
		ptySession2 := newRestoredPTY(paneSize(pane))
		if err := d.spawnPane(pane, ptySession2, false); err != nil {
			log.Printf("fallback shell for pane %s also failed: %v", pane.ID, err)
		}
	} else {
		cols, rows := paneSize(pane)
		log.Printf("respawned pane %s (type=%s, cwd=%s, size=%dx%d)", pane.ID, pane.Type, pane.CWD, cols, rows)
	}
}

// ensurePaneSpawned spawns a deferred pane on first access. Idempotent and
// race-safe: the double-check under spawnMu means a tab switch and an MCP op
// hitting the same pending pane spawn it exactly once.
//
// spawnMu (not PluginMu) guards the guard: spawnPane locks PluginMu
// synchronously on this goroutine, so holding PluginMu here would self-deadlock.
func (d *Daemon) ensurePaneSpawned(pane *Pane) {
	pane.spawnMu.Lock()
	defer pane.spawnMu.Unlock()
	if pane.PTY != nil || !pane.Pending {
		return
	}
	d.spawnRestoredPane(pane)
	pane.Pending = false
}

// ensureTabSpawned spawns every deferred pane in a tab (handles splits).
func (d *Daemon) ensureTabSpawned(tabID string) {
	for _, pane := range d.session.Panes(tabID) {
		d.ensurePaneSpawned(pane)
	}
}

// newRestoredPTY creates the PTY for a restored pane at its persisted size,
// so the child boots at the real dimensions instead of the 80x24 default
// (which interactive TUIs latch onto when the first resize event is lost —
// see resizeKick). Falls back to the default constructor for pre-size
// snapshots where Cols/Rows were never recorded.
// paneSize reads a pane's last known dimensions under PluginMu.
//
// Every caller runs on a goroutine that can race handleResizePane, which writes
// the pair from the resizing conn's dispatch goroutine: attach, lazy spawn,
// restart, screenshot, snapshot, and the PTY output goroutine's resizeKick.
// Reading the two fields together also keeps them consistent — a torn pair
// (new cols, old rows) would size a PTY to a geometry that never existed.
//
// Callers already holding PluginMu must NOT use this; the mutex is not
// reentrant. Those sites read the fields directly inside their own span.
func paneSize(pane *Pane) (cols, rows int) {
	pane.PluginMu.Lock()
	defer pane.PluginMu.Unlock()
	return pane.Cols, pane.Rows
}

// Takes the size explicitly rather than reading it off the pane: this runs on
// the lazy-spawn path while the IPC server is live, so pane.Cols/Rows must be
// snapshotted under PluginMu by the caller (see paneSize).
func newRestoredPTY(cols, rows int) apty.Session {
	if cols > 0 && rows > 0 {
		return newSessionFn(cols, rows)
	}
	return newSessionFn(0, 0)
}

// newSessionFn constructs the PTY session for a restored pane. It is a
// package-level var (not a direct apty call) so tests can swap in a fake that
// avoids launching a real /bin/sh child — mirrors the claudeSessionExistsFn /
// readHookSessionIDFn seams used elsewhere in this file. Production passes
// (cols, rows); a zero pair means "no persisted size, use the default".
var newSessionFn = func(cols, rows int) apty.Session {
	if cols > 0 && rows > 0 {
		return apty.NewWithSize(cols, rows)
	}
	return apty.New()
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
		d.handleCreatePane(conn, msg)
	case ipc.MsgDestroyPane:
		d.handleDestroyPane(msg)
	case ipc.MsgUpdatePane:
		d.handleUpdatePane(conn, msg)
	case ipc.MsgUpdateLayout:
		d.handleUpdateLayout(msg)
	case ipc.MsgPaneInput:
		d.handlePaneInput(msg)
	case ipc.MsgResizePane:
		d.handleResizePane(msg)
	case ipc.MsgReloadPlugins:
		d.handleReloadPlugins()
	case ipc.MsgOverlayPolicy:
		var p ipc.OverlayPolicyPayload
		// Checked, not best-effort: both fields use 0 for "disabled", so a
		// malformed frame decoded into a zero struct would silently turn off
		// both retention policies.
		if err := msg.DecodePayload(&p); err != nil {
			log.Printf("overlay policy: malformed payload: %v", err)
			return
		}
		d.setOverlayPolicy(p)
	case ipc.MsgShutdown:
		d.shutdownOnce.Do(func() { close(d.shutdown) })

	// Project lifecycle
	case ipc.MsgCreateProject:
		var p ipc.CreateProjectPayload
		// Checked, unlike the four handlers below where a zero payload is a
		// harmless no-op: a decode failure here would create a NAMELESS project
		// rooted nowhere, ship it a shell, broadcast it and snapshot it — a
		// malformed frame turning into persistent workspace state.
		if err := msg.DecodePayload(&p); err != nil {
			log.Printf("create project: malformed payload: %v", err)
			return
		}
		proj := d.session.CreateProject(p.Name, p.RootDir)
		// A project ships with a shell, exactly like a fresh workspace. An
		// empty one renders as a blank screen the moment the user switches to
		// it, and there is no in-band way out: Ctrl+T files its tab against
		// the daemon's ACTIVE project, which a just-created one is not.
		d.recoverEmptyProject(proj.ID)
		d.broadcastState()
		d.requestSnapshot()

	case ipc.MsgDestroyProject:
		var p ipc.DestroyProjectPayload
		if err := msg.DecodePayload(&p); err != nil {
			log.Printf("destroy project: malformed payload: %v", err)
			return
		}
		detached := d.session.DestroyProject(p.ProjectID)
		// Destroying a project destroys every pane under it, so this is a
		// pane-destruction path and owes the same cleanup as destroy-pane and
		// destroy-tab: closing the PTY leaves the hook spool, the ingester's
		// coalescers and the session-id files behind, and the spool is
		// re-polled every 200 ms until the daemon restarts.
		for _, pane := range detached {
			d.cleanupPaneArtifacts(pane.ID)
		}
		releasePanes(detached)
		// Destroying the last project leaves nothing to render, and destroying
		// the active one can promote a project that is itself empty.
		d.recoverEmptyProject(d.session.ActiveProject())
		d.broadcastState()
		d.requestSnapshot()

	case ipc.MsgUpdateProject:
		var p ipc.UpdateProjectPayload
		if err := msg.DecodePayload(&p); err != nil {
			log.Printf("update project: malformed payload: %v", err)
			return
		}
		// Logged when it does NOT apply. The two ways that happens — an unknown
		// ID, and an adopt whose project someone named first — both look
		// identical from the client: a dialog that accepted a name and closed
		// on nothing changing. This package has already paid for one silently
		// ignored project message.
		if !d.session.UpdateProject(p.ProjectID, p.Name, p.RootDir, p.AdoptBootstrap) {
			log.Printf("update project %s: not applied (adopt=%v) — unknown id, or already named",
				p.ProjectID, p.AdoptBootstrap)
		}
		d.broadcastState()
		// Rename now also clears the persisted Bootstrap flag, so leaving this
		// to the periodic ticker means a daemon killed inside that window comes
		// back with the project called Default and adoptable again — the user's
		// rename lost. Create, destroy and switch all snapshot; this was the
		// one project mutation that did not.
		d.requestSnapshot()

	case ipc.MsgMergeProjects:
		var p ipc.MergeProjectsPayload
		if err := msg.DecodePayload(&p); err != nil {
			log.Printf("merge projects: malformed payload: %v", err)
			return
		}
		// Logged when it does NOT apply, for the reason MsgUpdateProject is:
		// the one failure — an unknown survivor ID — looks from the client like
		// a dialog that accepted a name and closed on nothing changing.
		if !d.session.MergeProjects(p.ProjectID, p.Absorb, p.Name) {
			log.Printf("merge %d projects into %q: not applied — unknown id",
				len(p.Absorb), p.ProjectID)
		} else {
			log.Printf("merged %d projects into %q (%q)", len(p.Absorb), p.ProjectID, p.Name)
		}
		// Folding projects that all hold zero tabs leaves the survivor empty,
		// which is a blank screen with no in-band way out — Ctrl+T files against
		// the ACTIVE project, and that is the empty one. Create and destroy both
		// recover here for the same reason.
		d.recoverEmptyProject(p.ProjectID)
		d.broadcastState()
		// Reassigns tabs and DROPS project records, so a daemon killed inside
		// the 30 s ticker window comes back holding the duplicates the user
		// just folded away — with the tabs pointing at projects that no longer
		// match the snapshot they were reassigned in.
		d.requestSnapshot()

	case ipc.MsgSwitchProject:
		var p ipc.SwitchProjectPayload
		if err := msg.DecodePayload(&p); err != nil {
			log.Printf("switch project: malformed payload: %v", err)
			return
		}
		// ensureTabSpawned is the whole point of the returned tab: after a
		// lazy restore only sm.activeTab's panes are running, so a background
		// project's panes are Pending until something switches to their tab —
		// and switching PROJECT never did, so they showed the restore
		// indicator with no process behind them, indefinitely.
		if tabID, ok := d.session.SwitchProject(p.ProjectID); ok && tabID != "" {
			d.ensureTabSpawned(tabID)
		}
		d.broadcastState()
		d.requestSnapshot()

	case ipc.MsgReorderProject:
		var p ipc.ReorderProjectPayload
		if err := msg.DecodePayload(&p); err != nil {
			log.Printf("reorder project: malformed payload: %v", err)
			return
		}
		d.session.ReorderProject(p.ProjectID, p.NewIndex)
		d.broadcastState()

	// MCP request-response
	case ipc.MsgListPanesReq:
		d.handleListPanesReq(conn, msg)
	case ipc.MsgReadPaneOutputReq:
		d.handleReadPaneOutputReq(conn, msg)
	case ipc.MsgPaneSearchReq:
		d.handlePaneSearchReq(conn, msg)
	case ipc.MsgClaudeSessionDetailReq:
		d.handleClaudeSessionDetailReq(conn, msg)
	case ipc.MsgClaudeSessionsReq:
		d.handleClaudeSessionsReq(conn, msg)
	case ipc.MsgBrowseDirReq:
		d.handleBrowseDirReq(conn, msg)
	case ipc.MsgDirsExistReq:
		d.handleDirsExistReq(conn, msg)
	case ipc.MsgGitReposReq:
		d.handleGitReposReq(conn, msg)
	case ipc.MsgWorktreeListReq:
		d.handleWorktreeListReq(conn, msg)
	case ipc.MsgKubeCtxReq:
		d.handleKubeCtxReq(conn, msg)
	case ipc.MsgPluginListReq:
		d.handlePluginListReq(conn, msg)
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
		d.broadcast(msg)

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

	// Auto-update
	case ipc.MsgStageUpdateReq:
		d.handleStageUpdateReq(conn, msg)

	// Pane input history
	case ipc.MsgPaneHistoryReq:
		d.handlePaneHistoryReq(conn, msg)
	case ipc.MsgPaneHistoryEntryReq:
		d.handlePaneHistoryEntryReq(conn, msg)

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

	// This is what makes the conn a CLIENT rather than just a connection — the
	// distinction the detached-session overlay stamp turns on. Recorded before
	// any of the work below, which has early returns of its own.
	d.markClientAttached(conn)

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
			// Type and GhostSnap are PluginMu-protected — a concurrent lazy
			// spawn (ensurePaneSpawned) can rewrite Type, and a second client's
			// attach (each IPC conn runs on its own goroutine) races the
			// take-and-clear of GhostSnap. Read Type, decide ghost eligibility,
			// and atomically take+clear GhostSnap in ONE lock span; then release
			// before the slow chunked replay (never hold PluginMu across I/O).
			pane.PluginMu.Lock()
			typ := pane.Type
			ghostEnabled := true
			if p := d.registry.Get(typ); p != nil && !p.Persistence.GhostBuffer {
				ghostEnabled = false
			}
			var ghost []byte
			source := "ghostsnap"
			if ghostEnabled {
				ghost = pane.GhostSnap
				if ghost == nil {
					ghost = pane.OutputBuf.Bytes() // reconnect — use full buffer
					source = "outputbuf"
				} else if restoresOwnHistory(d.registry.Get(typ)) {
					// GhostSnap non-nil means this is the first attach after a
					// daemon restore, so this pane's child is being respawned
					// rather than reattached — and a session-resume strategy
					// hands that child its own transcript, which it repaints
					// from the top. Replaying our copy as well puts the same
					// conversation in the grid twice, with the join corrupted:
					// the child's first rows land wherever the replay left the
					// cursor, so its banner overwrites the middle of the saved
					// prompt line (reported 2026-08-02 and 2026-08-03).
					//
					// Scoped to GhostSnap deliberately. The OutputBuf path is a
					// reattach to a LIVE child that will not repaint anything,
					// where the replay is the only history there is — that is
					// the case ghost_buffer = true was measured against and it
					// is unchanged.
					ghost = nil
					source = "skipped-child-repaints"
				}
				pane.GhostSnap = nil // take-and-clear under the lock
			}
			// Captured in the same span as Type/GhostSnap: the redraw kick below
			// needs a live PTY, and reading it separately would race a restart.
			// Same discipline as handleResizePane — pointer under the lock, the
			// Resize syscall outside it.
			kickRunning := pane.PTY != nil && pane.ExitCode == nil
			pane.PluginMu.Unlock()
			if !ghostEnabled || len(ghost) == 0 {
				if source == "skipped-child-repaints" {
					log.Printf("attach: skipped ghost replay pane %s (type=%s, child restores its own history)",
						pane.ID, typ)
				}
				// Nothing was replayed, so this pane's rectangle is blank on the
				// client that just attached — even though the process behind it
				// is alive and mid-conversation. Ask the child to repaint.
				if kickRunning {
					d.redrawKick(pane, typ)
				}
				continue
			}
			log.Printf("attach: ghost replay pane %s (type=%s, source=%s, bytes=%d)",
				pane.ID, typ, source, len(ghost))
			sendGhostChunked(conn, pane.ID, ghost, d.shutdown)
			if source == "ghostsnap" {
				// A DIFFERENT session's screen was just drawn, and the child
				// about to paint over it positions absolutely against a screen
				// it believes is its own. Push the replay into scrollback so
				// the two never share a row. Only on this path: the outputbuf
				// replay is this child's own byte stream, so it reproduces the
				// screen the child already thinks it has.
				_, rows := paneSize(pane)
				sendGhostChunked(conn, pane.ID, ghostScrollOut(rows), d.shutdown)
			}
		}
	}

	// Replay pending notification events, OLDEST FIRST — this is a replay of
	// state transitions, not a listing. The TUI rebuilds each pane's work
	// state by applying these in order, so the newest-first storage order has
	// to be reversed or the reconstruction ends on the oldest event's state.
	// Blocking send for the same reason as ghost replay: up to MaxEvents (200)
	// critical frames in a burst would overflow the 64-slot critical queue and
	// force-close a busy client.
	for _, e := range d.events.EventsOldestFirst() {
		payload := toPaneEventPayload(e)
		evtMsg, _ := ipc.NewMessage(ipc.MsgPaneEvent, payload)
		if err := conn.SendBlocking(evtMsg, d.shutdown); err != nil {
			return // client disconnected or daemon shutting down
		}
	}
}

// sendGhostChunked sends a ghost buffer in 8 KB chunks with a 2 ms yield
// between each chunk. This prevents the TUI's Bubble Tea event loop from
// being starved by a single massive message — keyboard events can interleave
// between chunks. The 2 ms delay matches the live-output coalescing interval
// in streamPTYOutput, so ghost replay feels identical to fast live output.
// The done channel allows early abort if the daemon is shutting down or the
// client disconnects mid-replay.
// ghostScrollOut is the byte sequence that pushes a replayed session off the
// VISIBLE screen and into the emulator's scrollback, leaving blank rows for
// the respawned child to paint on.
//
// This is what makes a restored terminal coherent at all. The child paints
// with ABSOLUTE cursor positioning against a screen it believes it owns —
// PSReadLine redrawing an input line emits `CSI 1;30H`, row 1, column 30, one
// past a 29-character prompt — so a replayed screen underneath it does not
// merely look stale, it gets painted through: the fresh prompt appears at the
// top of the pane while the previous session's rows sit below it (reported
// 2026-08-03). No amount of repair at the join fixes that, because the child's
// row 1 and ours are different screens.
//
// Scrolling rather than clearing is the point: LF at the bottom row moves a
// line into scrollback, where the user can still reach it, while `CSI 2J`
// would erase it. A full `rows` of them is deliberate over-scroll — the daemon
// cannot know how many rows the replay actually occupied on the client, since
// that depends on wrapping — so a short buffer leaves some blank rows above
// the new session. Blank scrollback is a cosmetic cost; an under-scroll is the
// bug returning.
//
// The trailing HOME is the other half, and scrolling alone was not enough:
// blanking the screen leaves the cursor on the BOTTOM row, so the child's
// prompt is drawn there while the child's own model still has it on row 1 —
// and the next absolute redraw goes to row 1, so the prompt sits at the bottom
// of the pane and typing appears at the top. Homing first makes our origin and
// the child's the same row, which is what the child's absolute positioning has
// been asserting all along.
func ghostScrollOut(rows int) []byte {
	if rows <= 0 {
		rows = 24 // pane never sized (deferred, no client geometry yet)
	}
	out := bytes.Repeat([]byte("\r\n"), rows)
	return append(out, '\x1b', '[', 'H')
}

// restoresOwnHistory reports whether a plugin's resume strategy hands the
// respawned child a session id, so the child paints its own transcript back
// instead of depending on Quil's replay.
//
// This is the resume-strategy question, not a plugin-name list: the two
// strategies below are exactly the ones resolveSpawnArgs expands into
// `--resume <id>` / `--session <id>`. `rerun` re-runs a command that starts
// from nothing, `cwd_only` respawns a shell that will not reprint a word of
// its scrollback, and both of those need the replay.
func restoresOwnHistory(p *plugin.PanePlugin) bool {
	if p == nil {
		return false
	}
	switch p.Persistence.Strategy {
	case "preassign_id", "session_scrape":
		return true
	}
	return false
}

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
		// Blocking send: replay volume routinely exceeds the critical queue
		// (32 chunks per full 256 KB buffer), and a freshly attached TUI is
		// busy applying workspace state. Backpressure must slow the replay,
		// not force-close the client (the production attach kick-loop).
		if err := conn.SendBlocking(msg, done); err != nil {
			return // client disconnected or daemon shutting down
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

	// Every tab needs a default pane with a shell, rooted at the OWNING
	// project's directory (see projectCWD).
	pane, _ := d.session.CreatePane(tab.ID, d.projectCWD(tab.ProjectID))
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
	// The owning project has to be read BEFORE the destroy: DestroyTab
	// de-registers the tab from it, so afterwards there is nothing left to
	// ask which project just lost a tab.
	projectID := ""
	if tab := d.session.Tab(payload.TabID); tab != nil {
		projectID = tab.ProjectID
	}
	// Capture the pane list before DestroyTab removes them from the session
	// maps, so we can clean up their artifacts after the tab is gone.
	panes := d.session.Panes(payload.TabID)
	d.session.DestroyTab(payload.TabID)
	for _, p := range panes {
		d.cleanupPaneArtifacts(p.ID)
	}

	d.recoverEmptyProject(projectID)

	d.broadcastState()
	d.requestSnapshot()
}

// recoverEmptyProject re-creates a shell tab for a project that has no tabs
// left, and returns without doing anything when it still has some.
//
// The guard is PER PROJECT, not per workspace. A workspace-wide
// `len(Tabs()) == 0` test leaves project A empty whenever project B still has
// tabs, and an empty ACTIVE project renders as a blank screen with no tab bar:
// there is nothing to click and nothing to close. "Deleting the last tab
// auto-creates a new Shell tab" is an always-on invariant of this codebase, and
// projects made it an invariant per project.
//
// projectID may be empty or unknown — a tab from a pre-project snapshot, or one
// whose project a racing DestroyProject removed. projectIsEmpty falls back to
// the workspace-wide test there, and createTabLocked resolves the empty ID to
// the active project or bootstraps one, so the workspace still recovers.
func (d *Daemon) recoverEmptyProject(projectID string) {
	if !d.projectIsEmpty(projectID) {
		return
	}
	tab := d.session.CreateTabInProject(projectID, "Shell")
	pane, err := d.session.CreatePane(tab.ID, d.projectCWD(tab.ProjectID))
	if err != nil {
		log.Printf("recover empty project %q: create pane: %v", projectID, err)
		return
	}
	pane.Type = "terminal"
	// Through newSessionFn rather than apty.NewWithSize directly: same 80×24
	// default in production, and the seam is what lets a test assert the
	// replacement shell's CWD without launching a child.
	if err := d.spawnPane(pane, newSessionFn(80, 24), false); err != nil {
		log.Printf("failed to start replacement shell: %v", err)
	}
	// DestroyTab moves the GLOBAL active tab to tabOrder[0], which can belong
	// to a different project. If the project we just repaired is the one the
	// user is looking at, the replacement is the tab they should land on.
	if tab.ProjectID == d.session.ActiveProject() {
		d.session.SwitchTab(tab.ID)
	}
}

// projectIsEmpty reports whether projectID names a project with no tabs left.
// An empty or unknown ID falls back to the workspace-wide test — the
// pre-project behaviour, and still the right answer for a tab that belongs to
// no project the daemon knows about.
func (d *Daemon) projectIsEmpty(projectID string) bool {
	if projectID != "" {
		for _, p := range d.session.Projects() {
			if p.ID == projectID {
				return len(p.TabIDs) == 0
			}
		}
	}
	return len(d.session.Tabs()) == 0
}

// projectCWD is the directory a new tab in projectID starts its shell in: the
// project's own RootDir when it names a real directory on THIS machine, else
// the daemon's default.
//
// Without this the root directory a user picks in the New Project dialog is
// collected, validated, persisted and editable while never being used for
// anything — every tab in every project opens in the daemon's own CWD. A stale
// value falls back rather than failing the spawn: a snapshot can outlive the
// directory it names, and can be restored on a machine where that path never
// existed.
func (d *Daemon) projectCWD(projectID string) string {
	if projectID == "" {
		return d.defaultCWD()
	}
	for _, p := range d.session.Projects() {
		if p.ID != projectID {
			continue
		}
		// Bounded: this runs on the conn's dispatch goroutine, and a root on a
		// dead mount would park every pane on the daemon behind it. A refused
		// permit or a timeout falls through to the default exactly as a stale
		// path does — the function already treats "cannot use this" as a
		// fallback case, so no new semantics.
		if dir := resolveSpawnDirWithin(p.RootDir, spawnDirProbeTimeout); dir != "" {
			return dir
		}
		break
	}
	return d.defaultCWD()
}

func (d *Daemon) handleSwitchTab(msg *ipc.Message) {
	var payload ipc.SwitchTabPayload
	if err := msg.DecodePayload(&payload); err != nil {
		return
	}
	log.Printf("tab switch: %s", payload.TabID)
	d.session.SwitchTab(payload.TabID)
	d.ensureTabSpawned(payload.TabID)
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
	} else if payload.ClearColor || payload.Name == "" {
		// Explicit clear (color cycle wrapped past the last color), or the
		// legacy heuristic: only the color field sent, as empty → clear.
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

func (d *Daemon) handleCreatePane(conn *ipc.Conn, msg *ipc.Message) {
	var payload ipc.CreatePanePayload
	if err := msg.DecodePayload(&payload); err != nil {
		return
	}

	// A create carrying a worktree spec goes to a WORKER goroutine and answers
	// the requester directly. `git worktree add` checks out a tree — seconds on
	// a large repository — and this function runs on the requesting conn's
	// dispatch goroutine, where that blocks every message from that client,
	// input included. Same hazard that moved browse, discover and
	// claudesessions onto workers.
	//
	// The response is a request-response pair rather than a broadcast: the
	// requester holds a layout placeholder armed before the send, and a
	// broadcast would put one client's failure in front of every other client
	// while giving the requester nothing correlatable to unwind with.
	if payload.Worktree != nil {
		go func() {
			respondTo(conn, msg.ID, ipc.MsgCreatePaneResp, d.worktreeAddAndCreate(payload))
		}()
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

	if _, err := d.createPaneAt(payload, cwd, paneType); err != nil {
		log.Printf("%v", err)
		return
	}
}

// createPaneAt is the pane construction every create path shares: allocate,
// apply the payload's plugin fields, claim a resume session, spawn, publish.
//
// Extracted rather than copied because the worktree path needs all of it and
// none of handleCreatePane's cwd fallback — see createPaneInWorktree, where
// substituting the daemon's directory for a missing worktree would defeat the
// isolation the pane exists for.
//
// A spawn failure returns the pane alongside the error, so a caller that must
// leave nothing behind can destroy it. handleCreatePane deliberately does not:
// its historical behaviour is to leave the PTY-less pane in place and log,
// and the next workspace broadcast shows it as exited.
func (d *Daemon) createPaneAt(payload ipc.CreatePanePayload, cwd, paneType string) (*Pane, error) {
	pane, err := d.session.CreatePane(payload.TabID, cwd)
	if err != nil {
		return nil, fmt.Errorf("create pane error: %w", err)
	}

	pane.Type = paneType
	pane.InstanceName = payload.InstanceName
	pane.InstanceArgs = payload.InstanceArgs
	if payload.Overlay {
		// CreatePane already PUBLISHED the pane into the session maps, so a
		// concurrent snapshot/broadcast goroutine may be reading it — both
		// writes go under PluginMu (same discipline as Muted).
		pane.PluginMu.Lock()
		pane.Overlay = true
		pane.OverlayShownAt = time.Now()
		// Overlay panes are muted at the source: a hidden lazygit
		// refreshing must not ping the notification sidebar.
		pane.Muted = true
		pane.PluginMu.Unlock()
		d.enforceOverlayCap(pane.ID)
	}
	d.applyResumeSessionID(pane, payload.ResumeSessionID)
	log.Printf("pane created: %s (type=%s, tab=%s, overlay=%v)", pane.ID, paneType, payload.TabID, payload.Overlay)

	// Through newSessionFn rather than apty.New() — identical in production
	// (the seam's zero pair IS apty.New()) and the smallest change that makes
	// this function drivable from a test. It is the ONLY production call site
	// of enforceOverlayCap, and a direct-call test of that function passes just
	// as happily against a createPaneAt that no longer calls it.
	ptySession := newSessionFn(0, 0)
	if err := d.spawnPane(pane, ptySession, false); err != nil {
		return pane, fmt.Errorf("start PTY error: %w", err)
	}
	d.broadcastState()
	d.requestSnapshot()
	return pane, nil
}

// handleReplacePane is the fire-and-forget entry point: it logs what
// replacePaneAt reports and returns. Kept so the ordinary replace path behaves
// exactly as it always has — errors are logged and the next broadcast shows
// the result — while the worktree path, which must ANSWER its requester, uses
// replacePaneAt directly.
func (d *Daemon) handleReplacePane(payload ipc.CreatePanePayload, cwd, paneType string) {
	if _, _, err := d.replacePaneAt(payload, cwd, paneType); err != nil {
		log.Printf("replace pane: %v", err)
	}
}

// replacePaneAt is the replace counterpart of createPaneAt: swap the old pane
// for a new one at cwd, and report what happened.
//
// Returning the error rather than logging it is what lets the worktree path
// reuse this. That path creates the worktree FIRST and only reaches here once
// git has succeeded, so the pane being replaced is never destroyed on behalf of
// a worktree that does not exist — which was the whole reason the combination
// used to be refused.
//
// The second return says whether the OLD pane was actually removed. It is not
// derivable from the error: the swap happens before the new pane's PTY spawns,
// so a spawn failure returns an error with the old pane already gone. The
// worktree path forwards it to the client as CreatePaneRespPayload.Swapped,
// which is what stops the client restoring a pane the daemon has destroyed.
func (d *Daemon) replacePaneAt(payload ipc.CreatePanePayload, cwd, paneType string) (*Pane, bool, error) {
	newPane := d.session.NewPane(cwd)
	newPane.Type = paneType
	newPane.InstanceName = payload.InstanceName
	newPane.InstanceArgs = payload.InstanceArgs
	log.Printf("pane replace: %s -> %s (type=%s)", payload.ReplacePaneID, newPane.ID, paneType)

	// Atomically swap old → new in the tab's pane list
	if err := d.session.ReplacePane(payload.ReplacePaneID, newPane); err != nil {
		return nil, false, fmt.Errorf("swap error: %w", err)
	}
	// From here on the old pane is GONE, whatever else fails.
	// The old pane is no longer reachable via the session — clean up its
	// hook artifacts so the spool watcher stops re-polling a dead file.
	d.cleanupPaneArtifacts(payload.ReplacePaneID)

	// Claim the resume target only once the pane is published, so the claim is
	// visible to a create racing on the same session (the occupancy scan walks
	// the session maps).
	d.applyResumeSessionID(newPane, payload.ResumeSessionID)

	ptySession := apty.New()
	if err := d.spawnPane(newPane, ptySession, false); err != nil {
		d.session.DestroyPane(newPane.ID)
		d.broadcastState()
		d.requestSnapshot()
		// swapped=true: the old pane went with the swap above and is not coming
		// back, even though this is an error.
		return nil, true, fmt.Errorf("start PTY (dead pane removed): %w", err)
	}
	d.broadcastState()
	d.requestSnapshot()
	return newPane, true, nil
}

// cleanupPaneArtifacts tears down everything keyed by paneID outside the
// session maps: the hook spool file + offset/parse-error entries, the
// ingester's pending coalescers and rate buckets, and the persisted
// session-id files. MUST be called on every pane-destruction path —
// destroy-pane, destroy-tab, replace — or the daemon leaks the map entries
// and re-polls the dead spool file every 200 ms until restart.
//
// Ordering relative to the PTY close does not matter for correctness: a
// dying hook process can recreate the spool/session-id file after cleanup,
// but emitHookEvent drops events for panes absent from the session, so the
// residue is a small bounded file until the next daemon restart (Init
// truncates stale spools). Call it before or after the session delete,
// whichever reads better at the call site.
func (d *Daemon) cleanupPaneArtifacts(paneID string) {
	// Overlay visibility claims are keyed by pane id, so a destroyed overlay
	// would otherwise leave its id in every live client's claim set — in a
	// daemon that runs for weeks, one entry per overlay ever opened.
	d.forgetOverlayClaimsFor(paneID)
	if d.hookSpool != nil {
		d.hookSpool.Cleanup(paneID)
	}
	if d.hookIngester != nil {
		d.hookIngester.Cancel(paneID)
	}
	for _, name := range []string{paneID + ".id", paneID + ".transcript", "opencode-" + paneID + ".id"} {
		p := filepath.Join(config.SessionsDir(), name)
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("cleanup pane %s: remove session id %s: %v", paneID, name, err)
		}
	}
	if err := os.Remove(panehistory.Path(config.QuilDir(), paneID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("cleanup pane %s: remove history: %v", paneID, err)
	}
}

func (d *Daemon) handleDestroyPane(msg *ipc.Message) {
	var payload ipc.DestroyPanePayload
	if err := msg.DecodePayload(&payload); err != nil {
		return
	}
	// Defense-in-depth: reject malformed PaneID at the IPC ingress so a
	// crafted payload (e.g. PaneID = "../../tmp/target") cannot escape the
	// spool directory in Spool.Cleanup or any other paneID-keyed FS path.
	// Production paneIDs are uuid-derived hex; anything else is invalid.
	if !isValidHexID(payload.PaneID, "pane-") {
		log.Printf("handleDestroyPane: rejected malformed PaneID %q", payload.PaneID)
		return
	}

	// Capture tab ID before destroying the pane
	var tabID string
	if pane := d.session.Pane(payload.PaneID); pane != nil {
		tabID = pane.TabID
	}
	log.Printf("pane destroy: %s (tab=%s)", payload.PaneID, tabID)

	// Tear down the pane's hook event spool file, ingester state, and
	// persisted session-id files before destroying the pane itself.
	d.cleanupPaneArtifacts(payload.PaneID)

	d.session.DestroyPane(payload.PaneID)

	// Auto-create replacement if the last NORMAL pane in the tab was
	// destroyed. Overlay panes don't count — a tab holding only a hidden
	// lazygit overlay would otherwise render an empty layout. Remaining
	// overlays are destroyed along with the tab's last normal pane.
	if tabID != "" {
		d.ensureTabNotEmpty(tabID)
	}

	d.broadcastState()
	d.requestSnapshot()
}

// ensureTabNotEmpty destroys orphaned overlay panes and spawns a fresh
// terminal pane when a tab has no normal panes left. Shared by the TUI
// destroy path (handleDestroyPane) and the MCP path (handleDestroyPaneReq).
func (d *Daemon) ensureTabNotEmpty(tabID string) {
	var overlays []*Pane
	normal := 0
	for _, p := range d.session.Panes(tabID) {
		p.PluginMu.Lock()
		isOverlay := p.Overlay
		p.PluginMu.Unlock()
		if isOverlay {
			overlays = append(overlays, p)
		} else {
			normal++
		}
	}
	if normal > 0 {
		return
	}
	for _, op := range overlays {
		log.Printf("pane destroy: orphaned overlay %s (tab=%s)", op.ID, tabID)
		d.cleanupPaneArtifacts(op.ID)
		d.session.DestroyPane(op.ID)
	}
	if newPane, err := d.session.CreatePane(tabID, d.defaultCWD()); err == nil {
		newPane.Type = "terminal"
		ptySession := apty.New()
		if err := d.spawnPane(newPane, ptySession, false); err != nil {
			log.Printf("failed to start replacement shell: %v", err)
		}
	}
}

func (d *Daemon) handlePaneInput(msg *ipc.Message) {
	var payload ipc.PaneInputPayload
	if err := msg.DecodePayload(&payload); err != nil {
		return
	}

	pane := d.session.Pane(payload.PaneID)
	if pane == nil {
		return
	}
	// A deferred pane (MCP send_to_pane / send_keys targeting a not-yet-spawned
	// restored pane) must be booted before its PTY can accept input.
	d.ensurePaneSpawned(pane)
	if pane.PTY == nil {
		return
	}
	// Never write the PTY here: a child that stopped reading stdin makes
	// Write block forever, and this runs on the conn's dispatch goroutine —
	// a single stuck pane froze input for every pane (2026-06-11/12 wedge).
	// EnqueueInput hands the data to the pane's own writer goroutine.
	if !pane.EnqueueInput(payload.Data) {
		d.notifyInputBlocked(pane)
	}
}

// notifyInputBlocked surfaces a full input queue — the pane's child has
// stopped reading stdin, so keystrokes are being dropped rather than
// freezing the daemon. One sidebar event per pane per cooldown window.
func (d *Daemon) notifyInputBlocked(pane *Pane) {
	const inputBlockedCooldown = 30 * time.Second
	pane.PluginMu.Lock()
	if !pane.LastInputBlockedAt.IsZero() && time.Since(pane.LastInputBlockedAt) < inputBlockedCooldown {
		pane.PluginMu.Unlock()
		return
	}
	pane.LastInputBlockedAt = time.Now()
	pane.PluginMu.Unlock()
	log.Printf("pane %s: input queue full — process not reading stdin, dropping keystrokes", pane.ID)
	d.emitEvent(PaneEvent{
		ID:        uuid.New().String(),
		PaneID:    pane.ID,
		TabID:     pane.TabID,
		PaneName:  pane.Name,
		Type:      "input_blocked",
		Title:     "Pane not accepting input",
		Message:   "The process stopped reading its input — keystrokes are being dropped. Restart the pane if it stays stuck.",
		Severity:  "warning",
		Timestamp: time.Now(),
	})
}

func (d *Daemon) handleResizePane(msg *ipc.Message) {
	var payload ipc.ResizePanePayload
	if err := msg.DecodePayload(&payload); err != nil {
		return
	}

	pane := d.session.Pane(payload.PaneID)
	if pane == nil {
		return
	}
	// Same-size guard: skip when this exact size was already applied to
	// the current PTY (the TUI re-sends all pane sizes on every workspace
	// broadcast). Guard fields are PluginMu-protected; the Resize syscall
	// runs outside the lock.
	pane.PluginMu.Lock()
	pty := pane.PTY
	typ := pane.Type
	same := pane.appliedCols == int(payload.Cols) && pane.appliedRows == int(payload.Rows)
	pane.PluginMu.Unlock()
	if pty == nil || same {
		return
	}
	if err := pty.Resize(payload.Rows, payload.Cols); err != nil {
		// Record nothing on failure: a transient Resize error must not make
		// the guard believe this size was applied, or the TUI's next
		// identical re-send would be skipped and the failed resize never
		// retried. Leaving appliedCols/Rows unchanged lets the next
		// broadcast retry.
		log.Printf("resize pane %s to %dx%d: %v", payload.PaneID, payload.Cols, payload.Rows, err)
		return
	}
	// Record only after the syscall succeeds. Cols/Rows are written INSIDE the
	// lock with the applied* guards: they used to be set just below it, which
	// made them a genuine data race — this runs on the resizing conn's dispatch
	// goroutine while handleAttach (another conn), the PTY output goroutine's
	// resizeKick, and snapshot() all read them concurrently.
	pane.PluginMu.Lock()
	pane.appliedCols = int(payload.Cols)
	pane.appliedRows = int(payload.Rows)
	pane.Cols = int(payload.Cols)
	pane.Rows = int(payload.Rows)
	pane.PluginMu.Unlock()

	d.repaintAfterResize(pane, typ)
}

// repaintAfterResize nudges a pane that has just been resized into repainting,
// for the plugins that will not do it on their own.
//
// A declared redraw_key MEANS "this program ignores SIGWINCH" — that is the
// contract redrawKick already relies on, and it is measured rather than
// assumed: claude-code re-lays-out on a resize but paints only on its own
// render tick, which INPUT drives, so the resize alone leaves the previous
// paint on screen at the previous width. The result is not a pane that looks
// stale for a moment; it is one whose old content stays wrapped at the old
// width underneath everything drawn afterwards, which is what produced the
// overlapping banner reported on 2026-08-02 (restored panes spawn at the
// persisted size, then the first client resize moves them).
//
// This is deliberately NOT the jiggle half of redrawKick: the caller has just
// performed a real resize, so a program that repaints on SIGWINCH has already
// been told everything a jiggle would tell it. Only the panes that declared
// they need input get input, which keeps the opt-in property intact — a plain
// terminal at a password prompt must never be sent a keystroke it would read
// as data.
// The registry nil-check is not defensive padding: handleResizePane is reached
// by tests that build a Daemon with only a session, and Registry.Get takes a
// mutex on the receiver, so a nil one panics rather than answering "no plugin".
func (d *Daemon) repaintAfterResize(pane *Pane, typ string) {
	if d.registry == nil {
		return
	}
	p := d.registry.Get(typ)
	if p == nil || p.Persistence.RedrawKey == "" {
		return
	}
	// EnqueueInput, never pane.PTY.Write: a child that has stopped reading
	// stdin blocks the writer forever, and this runs on the resizing conn's
	// dispatch goroutine.
	pane.EnqueueInput([]byte(p.Persistence.RedrawKey))
}

// handleUpdatePane applies a PARTIAL pane update. conn identifies the client
// that sent it, which only the overlay-visibility field needs: that field is a
// claim about one client's screen, not a daemon-wide fact.
func (d *Daemon) handleUpdatePane(conn *ipc.Conn, msg *ipc.Message) {
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
		// Defense-in-depth: skip UNC/device paths (\\host\share, //host/share,
		// \\?\..., \\.\...) that an attacker could inject via a crafted OSC7
		// sequence. The primary mitigation is in gitdiscover.canonical, which
		// prevents these paths from being probed at all; this guard additionally
		// prevents a UNC value from being persisted into workspace.json and later
		// handed to os.Stat in spawnRestoredPane.
		if !strings.HasPrefix(payload.CWD, `\\`) && !strings.HasPrefix(payload.CWD, `//`) {
			pane.CWD = payload.CWD
		} else {
			log.Printf("pane %s: rejected UNC CWD %q", pane.ID, payload.CWD)
		}
	}
	if payload.Muted != nil {
		pane.PluginMu.Lock()
		pane.Muted = *payload.Muted
		pane.PluginMu.Unlock()
		log.Printf("pane %s: muted=%v", pane.ID, *payload.Muted)
	}
	if payload.Eager != nil {
		pane.PluginMu.Lock()
		pane.Eager = *payload.Eager
		pane.PluginMu.Unlock()
		log.Printf("pane %s: eager=%v", pane.ID, *payload.Eager)
	}
	if payload.PinnedAttention != nil {
		pane.PluginMu.Lock()
		pane.PinnedAttention = *payload.PinnedAttention
		pane.PluginMu.Unlock()
		log.Printf("pane %s: pinned_attention=%v", pane.ID, *payload.PinnedAttention)
	}
	if payload.OverlayVisible != nil {
		d.applyOverlayVisibility(conn, pane, *payload.OverlayVisible)
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

// resizeKick re-applies a pane's last known size to its PTY, with a
// 1-column jiggle so the child receives a real size-change event.
//
// Windows ConPTY delivers resizes to the child as WINDOW_BUFFER_SIZE_EVENTs
// in its console input queue; events fired before the child starts reading
// input (claude/node mid-boot) are dropped and never replayed. The TUI's
// initial resize_pane lands ~25 ms after spawn and can be lost that way,
// leaving the child rendering at the spawn-time 80x24 until the next window
// resize. Called on the pane's first output — the child is alive and its
// console is wired up by then. No-op while the size is still unknown
// (resize_pane not yet received).
func resizeKick(pty apty.Session, cols, rows int) {
	if cols <= 0 || rows <= 0 {
		return
	}
	if cols > 1 {
		if err := pty.Resize(uint16(rows), uint16(cols-1)); err != nil {
			log.Printf("resize kick (jiggle): %v", err)
		}
	}
	if err := pty.Resize(uint16(rows), uint16(cols)); err != nil {
		log.Printf("resize kick: %v", err)
	}
}

// redrawKick asks a live child to repaint itself, for a pane that received no
// ghost replay on attach.
//
// Plugins with ghost_buffer = false (the AI panes) are deliberately excluded
// from replay: they run on the alternate screen, and a ring buffer of their
// output can begin mid-escape-sequence, so replaying it produces garbage rather
// than history. The cost is that a reconnecting client is sent nothing at all
// for such a pane — the PTY is attached and the process is mid-conversation,
// but the rectangle is blank until the child next writes something, and a
// full-screen program has no reason to do that unprompted. The pane reads as
// dead when it is perfectly healthy.
//
// There are TWO mechanisms, and the load-bearing fact is that NEITHER is
// universal — no single trigger repaints every full-screen program.
//
// A plugin's declared RedrawKey is written to the child's stdin. That is INPUT
// rather than a signal, which is why it is opt-in per plugin rather than
// applied to every replay-less pane: the plugin author is asserting both that
// their program treats the byte as "repaint" and that nothing else is reading
// its stdin. A pane sitting in `cat > file` or at a password prompt would
// receive it as data.
//
// Everything else gets a resize jiggle instead, which needs no opt-in because
// SIGWINCH is a signal and cannot be misread as data. Declaring a key therefore
// MEANS "I ignore SIGWINCH", and suppresses the jiggle.
//
// The measurements are why it is split this way, and they are counter-intuitive
// in both directions. On a real PTY: vim repaints with ~5 KB after a 1-column
// jiggle; claude-code emits 0 bytes from its main UI, because it re-lays-out on
// a resize but only paints on its own render tick, which input drives; opencode
// is the exact inverse of claude-code, emitting ~8 KB on SIGWINCH and NOTHING
// on Ctrl+L (measured 2026-07-31).
//
// Both halves of this have already shipped as bugs. The first version used only
// the jiggle and silently did nothing for claude-code. Its fix concluded from
// that one program that SIGWINCH "does not work" and made the key the ONLY
// mechanism — which left opencode and lazygit (ghost_buffer = false, no
// redraw_key) coming back blank from every reattach, locally as well as
// remotely, with a live process behind them. Adding redraw_key = "\f" to
// opencode, the obvious fix that the plugin schema invites, would have been a
// no-op.
//
// Writes go through EnqueueInput, never pane.PTY.Write directly: a child that
// has stopped reading stdin fills the kernel buffer and blocks the writer
// forever, and this runs on the attaching client's dispatch goroutine.
func (d *Daemon) redrawKick(pane *Pane, typ string) {
	if p := d.registry.Get(typ); p != nil && p.Persistence.RedrawKey != "" {
		log.Printf("attach: redraw kick pane %s (type=%s, no ghost replay, %d bytes)",
			pane.ID, typ, len(p.Persistence.RedrawKey))
		pane.EnqueueInput([]byte(p.Persistence.RedrawKey))
		return
	}

	// No key declared, so jiggle the size. This also covers a pane whose type is
	// not in the registry at all, which is the safe direction: a signal cannot
	// corrupt a program's input the way a guessed key could.
	//
	// The size is the PREVIOUS client's, because this runs inside handleAttach
	// and the new client's resize_pane has not arrived yet. A laptop attaching
	// after a desktop therefore gets one frame laid out for the old width. That
	// is transient rather than stuck: the real resize that follows is itself a
	// SIGWINCH, and a program that repaints on this one repaints on that one
	// too. Skipping the kick when the size is about to change is not available —
	// nothing here knows the incoming client's geometry.
	//
	// PTY and ExitCode are read in ONE span, together with the size. Taking them
	// separately would let onPaneExit land in between and leave this resizing a
	// closed PTY with a live-looking pointer.
	pane.PluginMu.Lock()
	pty, exited, cols, rows := pane.PTY, pane.ExitCode != nil, pane.Cols, pane.Rows
	pane.PluginMu.Unlock()
	if pty == nil || exited {
		return
	}
	log.Printf("attach: redraw kick pane %s (type=%s, no ghost replay, resize jiggle %dx%d)",
		pane.ID, typ, cols, rows)
	// Resize syscall outside the lock, same discipline as handleResizePane.
	resizeKick(pty, cols, rows)
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

	runCoalescer(dataCh,
		func() {
			// First output proves the child's console is wired up — re-apply
			// the size in case the initial resize event was dropped during
			// boot (see resizeKick).
			if pane := d.session.Pane(paneID); pane != nil {
				// paneSize, not a direct read: this is the PTY output goroutine
				// and handleResizePane writes the pair from a conn dispatch
				// goroutine.
				cols, rows := paneSize(pane)
				resizeKick(pty, cols, rows)
			}
		},
		func(b []byte) { d.flushPaneOutput(paneID, b) },
	)

	// dataCh closed: PTY EOF. Capture process exit code (protected by
	// PluginMu to avoid data race).
	if pane := d.session.Pane(paneID); pane != nil {
		code := pty.WaitExit()
		d.onPaneExit(pane, code)
	}
}

// onPaneExit records the exit state, emits the process_exit event, and
// auto-destroys overlay panes. Extracted from streamPTYOutput so it can be
// called and tested without a real PTY.
//
// Goroutine safety: called from the PTY-output goroutine. All sub-operations
// (PluginMu, session.mu, broadcast, requestSnapshot) take their own locks and
// are safe to call from any goroutine — the broadcast helpers already document
// this property (see the nil-guarded broadcast helper).
func (d *Daemon) onPaneExit(pane *Pane, code int) {
	pane.PluginMu.Lock()
	pane.ExitCode = &code
	pane.ExitedAt = time.Now()
	isOverlay := pane.Overlay
	pane.PluginMu.Unlock()
	log.Printf("pane %s: process exited with code %d", pane.ID, code)

	severity := "info"
	title := "Process exited (code 0)"
	if code != 0 {
		severity = "error"
		title = fmt.Sprintf("Process failed (code %d)", code)
	}
	d.emitEvent(withExcerpt(PaneEvent{
		ID:        uuid.New().String(),
		PaneID:    pane.ID,
		TabID:     pane.TabID,
		PaneName:  pane.Name,
		Type:      "process_exit",
		Title:     title,
		Severity:  severity,
		Timestamp: time.Now(),
		Data:      map[string]string{"exit_code": strconv.Itoa(code)},
	}, paneOutputExcerpt(pane, 5)))

	// Overlay panes are ephemeral: auto-destroy on exit so the TUI
	// reconciliation clears the slot and the next Alt+G creates fresh.
	// Normal panes survive as exited husks (existing behavior unchanged).
	if isOverlay {
		tabID := pane.TabID
		if err := d.session.DestroyPane(pane.ID); err != nil {
			// A retention eviction destroys the pane and the child exits
			// afterwards, so "not found" here means the pane was already
			// reaped — an ordinary outcome, and the pass that reclaimed it has
			// already cleaned up and broadcast. Reporting it as a failure would
			// put an error line and a redundant workspace-state frame on a
			// success path, in a daemon that runs for weeks.
			if !errors.Is(err, ErrPaneNotFound) {
				log.Printf("onPaneExit: destroy overlay %s: %v", pane.ID, err)
			}
			return
		}
		log.Printf("pane exit: auto-destroying overlay %s", pane.ID)
		d.cleanupPaneArtifacts(pane.ID)
		// ensureTabNotEmpty is a cheap no-op when normal panes remain (the
		// common case). It guards the degenerate case where an overlay was
		// somehow the tab's only pane — shouldn't happen because
		// handleDestroyPane calls ensureTabNotEmpty before we get here, but
		// defense in depth costs nothing.
		d.ensureTabNotEmpty(tabID)
		d.broadcastState()
		d.requestSnapshot()
	}
}

// mouseModeBroadcastCooldown throttles mouse-mode state broadcasts. Normal apps
// toggle mouse modes at most a couple times (startup enable, exit disable), so
// this never delays a real change; it only collapses a pathological or hostile
// PTY stream that alternates a mode every flush, which would otherwise force an
// unbounded full-snapshot broadcast storm.
const mouseModeBroadcastCooldown = 250 * time.Millisecond

func (d *Daemon) flushPaneOutput(paneID string, data []byte) {
	pane := d.session.Pane(paneID)
	if pane == nil {
		return
	}
	if pane.OutputBuf != nil {
		// Hand the buffer over from the restored session to this child on its
		// first byte. Until now OutputBuf held the PREVIOUS session's bytes so
		// a pane the user never opened still had history to persist and to
		// replay on reconnect; from here it is this child's stream, which is
		// what makes a reconnect replay reproduce the child's screen exactly
		// instead of laying it over a different session's.
		pane.PluginMu.Lock()
		seeded := pane.ghostSeeded
		pane.ghostSeeded = false
		pane.PluginMu.Unlock()
		if seeded {
			pane.OutputBuf.Reset()
		}
		pane.OutputBuf.Write(data)
	}

	// Update idle tracking + mouse-mode state (guarded by PluginMu).
	pane.PluginMu.Lock()
	now := time.Now()
	pane.LastOutputAt = now
	pane.IdleNotified = false
	scanData := data
	if len(pane.modeScanTail) > 0 {
		scanData = make([]byte, 0, len(pane.modeScanTail)+len(data))
		scanData = append(append(scanData, pane.modeScanTail...), data...)
	}
	newModes, tail := scanMouseModes(pane.MouseModes, scanData)
	pane.MouseModes = newModes
	// Copy: tail aliases scanData, which may alias the caller's reused read
	// buffer.
	pane.modeScanTail = append(pane.modeScanTail[:0], tail...)
	// A mouse-mode toggle (rare: once at startup, once at exit) must reach the
	// TUI so it can start/stop forwarding wheel events to the app. broadcastState
	// builds a full workspace snapshot, and the child fully controls its PTY
	// output, so we throttle: broadcast only when the state differs from what
	// clients last saw AND a cooldown has elapsed. Comparing against the
	// last-broadcast state (not the last-scanned state) means a change suppressed
	// inside the cooldown window is re-evaluated on the next flush and still
	// delivered once the window passes — normal apps never hit the window.
	var doMouseBroadcast bool
	if newModes != pane.mouseBroadcast && now.Sub(pane.lastMouseBroadcastAt) >= mouseModeBroadcastCooldown {
		pane.mouseBroadcast = newModes
		pane.lastMouseBroadcastAt = now
		doMouseBroadcast = true
	}
	pane.PluginMu.Unlock()
	if doMouseBroadcast {
		logger.Debug("pane %s: mouse-mode change tracking=%v sgr=%v", paneID, newModes.tracking(), newModes.sgr)
		d.broadcastState()
	}

	d.detectBellEvent(pane, paneID, data)
	d.detectOSC133Exit(pane, paneID, data)
	d.applyPluginHandlers(pane, paneID, data)

	msg, _ := ipc.NewMessage(ipc.MsgPaneOutput, ipc.PaneOutputPayload{
		PaneID: paneID,
		Data:   data,
	})
	d.broadcast(msg)
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
	// Cooldown bookkeeping under the lock; emit AFTER releasing it.
	// emitEvent re-locks this pane's PluginMu for the mute check, and Go
	// mutexes are not reentrant — emitting while holding the lock
	// self-deadlocked the output goroutine, and every subsystem touching
	// this pane's PluginMu queued behind it (snapshot loop, idle checker,
	// memreport, switch-tab broadcast): the daemon-wide freeze caught by
	// the snapshot watchdog's goroutine dump on 2026-06-12. Claude rings
	// the terminal bell exactly when it wants attention, arming this on
	// every "waiting for your input".
	pane.PluginMu.Lock()
	if !pane.LastBellEventAt.IsZero() && time.Since(pane.LastBellEventAt) < bellCooldown {
		pane.PluginMu.Unlock()
		return
	}
	pane.LastBellEventAt = time.Now()
	tabID := pane.TabID
	name := pane.Name
	pane.PluginMu.Unlock()
	d.emitEvent(withExcerpt(PaneEvent{
		ID: uuid.New().String(), PaneID: paneID, TabID: tabID,
		PaneName: name, Type: "bell",
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
		d.broadcast(errMsg)
	}
}

// broadcast sends a message to all connected clients, tolerating a nil server.
// d.server is nil during the early-startup window (respawnPanes runs before the
// IPC server is created), and a restored pane's streamPTYOutput goroutine can
// fire a broadcast in that window — so every broadcast site must nil-check.
func (d *Daemon) broadcast(msg *ipc.Message) {
	if d.server != nil {
		d.server.Broadcast(msg)
	}
}

func (d *Daemon) broadcastState() {
	if d.server == nil {
		return
	}
	state := d.buildWorkspaceState()
	resp, _ := ipc.NewMessage(ipc.MsgWorkspaceState, state)
	d.broadcast(resp)
}

func (d *Daemon) buildWorkspaceState() map[string]any {
	activeTab, tabs, panesByTab, projects, activeProject := d.session.SnapshotState()
	state := d.workspaceStateFromSnapshot(activeTab, tabs, panesByTab, projects, activeProject, true)
	// Broadcast-only (never persisted): announced newer release, if any.
	if info := d.currentUpdateInfo(); info != nil {
		state["update"] = info
	}
	return state
}

// workspaceStateFromSnapshot is the pure half of buildWorkspaceState — it
// turns an already-taken SnapshotState into the wire/persistence map. Callers
// that already hold a consistent snapshot (e.g. snapshot()) reuse it instead
// of calling SnapshotState a second time.
//
// includeOverlays controls whether ephemeral overlay panes are present in the
// output. Pass true for live broadcasts (TUI needs them for routing) and false
// for disk snapshots (overlays are intentionally ephemeral — gone on restart).
//
// projects/activeProject come from the SAME SnapshotState call as tabs/
// panesByTab — see SnapshotState's doc comment. They ride both the disk
// snapshot and the live broadcast because this function is shared by both
// (buildWorkspaceState and snapshot()); writing them only at the persist.Save
// call site would leave every broadcast project-less.
func (d *Daemon) workspaceStateFromSnapshot(activeTab string, tabs []*Tab, panesByTab map[string][]*Pane, projects []Project, activeProject string, includeOverlays bool) map[string]any {
	tabList := make([]map[string]any, 0, len(tabs))
	paneList := make([]map[string]any, 0)

	for _, tab := range tabs {
		// Overlay is PluginMu-guarded like Muted: handleCreatePane sets it
		// AFTER the pane is published to the session maps, concurrently with
		// this snapshot/broadcast. Capture one consistent view per tab here
		// and reuse it for both the pane-ID filter and the pane-loop skip.
		overlayIDs := make(map[string]bool)
		for _, pane := range panesByTab[tab.ID] {
			pane.PluginMu.Lock()
			isOverlay := pane.Overlay
			pane.PluginMu.Unlock()
			if isOverlay {
				overlayIDs[pane.ID] = true
			}
		}
		paneIDs := make([]string, 0, len(tab.Panes))
		for _, pid := range tab.Panes {
			if !includeOverlays && overlayIDs[pid] {
				continue
			}
			paneIDs = append(paneIDs, pid)
		}
		tabData := map[string]any{
			"id":         tab.ID,
			"name":       tab.Name,
			"color":      tab.Color,
			"panes":      paneIDs,
			"project_id": tab.ProjectID,
		}
		if len(tab.Layout) > 0 {
			tabData["layout"] = tab.Layout
		}
		tabList = append(tabList, tabData)

		for _, pane := range panesByTab[tab.ID] {
			// overlayIDs was captured under PluginMu above — reuse it so the
			// skip decision agrees with the pane-ID filter for this snapshot.
			if !includeOverlays && overlayIDs[pane.ID] {
				continue
			}
			paneData := map[string]any{
				"id":     pane.ID,
				"tab_id": pane.TabID,
			}
			if pane.Name != "" {
				paneData["name"] = pane.Name
			}
			// Type and CWD are PluginMu-protected: spawnRestoredPane mutates
			// them on the lazy-spawn error paths (CWD="" when the saved dir is
			// gone, Type="terminal" on spawn fallback) concurrently with this
			// snapshot. Capture both under the same lock as the other
			// PluginMu-guarded fields (Overlay included — see session.go).
			pane.PluginMu.Lock()
			typ := pane.Type
			cwd := pane.CWD
			isOverlay := pane.Overlay
			mouseTracking := pane.MouseModes.tracking()
			mouseSGR := pane.MouseModes.sgr
			bracketedPaste := pane.MouseModes.bracketedPaste
			sessionID := pane.PluginState["session_id"]
			historyLines := pane.HistoryLines
			lastModel := pane.LastModel
			lastContextTokens := pane.LastContextTokens
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
			if pane.Eager {
				paneData["eager"] = true
			}
			// PERSISTED for the reason the field exists: the mark is the user's
			// own, nothing re-derives it, and a hook edge that would set it
			// again is never coming. Written here rather than in the
			// includeOverlays block so ONE line serves both the disk snapshot
			// and the broadcast — the same arrangement muted has.
			if pane.PinnedAttention {
				paneData["pinned_attention"] = true
			}
			// PERSISTED, unlike SpawnError beside it: this is how restore tells
			// a missing worktree from a stale browsed directory, and without it
			// the snapshot carries only CWD, which cannot distinguish them.
			if pane.WorktreeOwned {
				paneData["worktree_owned"] = true
			}
			// SpawnError is captured for the BROADCAST only — see
			// includeOverlays below. It is never written to paneData.
			spawnErr := pane.SpawnError
			// Captured here rather than read below: this runs on the snapshot
			// goroutine while handleResizePane writes them from a conn dispatch
			// goroutine.
			snapCols, snapRows := pane.Cols, pane.Rows
			pane.PluginMu.Unlock()
			// Pending (deferred, not yet lazy-spawned) is spawnMu-guarded —
			// read it the same way list_panes does. The TUI uses it to show the
			// restore indicator on deferred panes and to re-arm the indicator
			// when the pane actually spawns (Pending→running on tab switch).
			// Broadcast-only: Pending is runtime state, never persisted to disk
			// (gated on includeOverlays, like overlay panes).
			if includeOverlays {
				pane.spawnMu.Lock()
				pending := pane.Pending
				pane.spawnMu.Unlock()
				if pending {
					paneData["pending"] = true
				}
			}
			// Broadcast-only restore-checklist hints (runtime, never persisted):
			// the tracked session id and the ghost-buffer line count the TUI
			// shows in the per-pane restore checklist.
			if includeOverlays {
				if sessionID != "" {
					paneData["session_id"] = sessionID
				}
				if historyLines > 0 {
					paneData["history_lines"] = historyLines
				}
				// Mouse-mode state is runtime-only (broadcast, never persisted):
				// it is re-derived from the live PTY stream on every spawn.
				if mouseTracking {
					paneData["mouse_tracking"] = true
				}
				if mouseSGR {
					paneData["mouse_sgr"] = true
				}
				if bracketedPaste {
					paneData["bracketed_paste"] = true
				}
				// Why the pane has no process, runtime-only: a fresh daemon
				// re-stats and re-derives it, and persisting it would
				// resurrect a complaint about a worktree the user has since
				// restored. Broadcast rather than logged because a relocation
				// nobody sees is the failure mode this replaces.
				if spawnErr != "" {
					paneData["spawn_error"] = spawnErr
				}
				// Model/context usage of the last completed AI turn is
				// runtime-only (broadcast, never persisted): a stale token
				// count from a previous daemon run would be wrong until the
				// next turn refreshes it.
				if lastModel != "" {
					paneData["model"] = lastModel
					paneData["context_tokens"] = lastContextTokens
				}
				// Git state is runtime-only for the same reason: a branch name
				// from a previous daemon run describes a checkout nobody has
				// re-probed. lookup() never probes, so this cannot slow a
				// broadcast down whatever the filesystem is doing.
				if info, ok, stale := d.gitCache.lookup(cwd); ok {
					if info.Branch != "" {
						paneData["git_branch"] = info.Branch
					}
					if info.Detached {
						paneData["git_detached"] = true
					}
					if info.LinkedWorktree {
						paneData["git_worktree"] = true
						// Conditional like git_branch: an absent key decodes
						// to the zero value on the client, where the copy is
						// unconditional and therefore clears it.
						if info.WorktreeName != "" {
							paneData["git_worktree_name"] = info.WorktreeName
						}
					}
					if info.HasUpstream {
						// Sent even at zero: "0 ahead, 0 behind" means in sync,
						// which is a different statement from having no
						// upstream to compare against.
						paneData["git_upstream"] = true
						paneData["git_ahead"] = info.Ahead
						paneData["git_behind"] = info.Behind
					}
					if stale {
						paneData["git_stale"] = true
					}
				}
			}
			paneData["cwd"] = cwd
			if typ != "" && typ != "terminal" {
				paneData["type"] = typ
			}
			if pane.InstanceName != "" {
				paneData["instance_name"] = pane.InstanceName
			}
			if len(pane.InstanceArgs) > 0 {
				paneData["instance_args"] = pane.InstanceArgs
			}
			// Persist last known size so respawnPanes can recreate the
			// ConPTY at the right dimensions instead of the 80x24 default
			// (children that boot before the first resize event would
			// otherwise render an 80-column UI — see resizeKick).
			if snapCols > 0 && snapRows > 0 {
				paneData["cols"] = snapCols
				paneData["rows"] = snapRows
			}
			if isOverlay {
				paneData["overlay"] = true
			}
			paneList = append(paneList, paneData)
		}
	}

	// []any (not []map[string]any, unlike tabList/paneList above): in-process
	// callers (buildWorkspaceState's own tests, e.g.) read this key with a
	// plain `.([]any)` assertion, matching what a JSON-decoded array becomes
	// on the wire — so the Go-side value already carries that shape rather
	// than the tabs/panes convention.
	projectList := make([]any, 0, len(projects))
	for _, p := range projects {
		projectList = append(projectList, map[string]any{
			"id":         p.ID,
			"name":       p.Name,
			"root_dir":   p.RootDir,
			"tab_ids":    p.TabIDs,
			"active_tab": p.ActiveTab,
			// Reaches the client AND the disk snapshot from here, because this
			// map is both. The client needs it to decide whether naming a
			// project adopts this one; the snapshot needs it so a restart does
			// not turn an un-adopted default into a real project.
			"bootstrap": p.Bootstrap,
		})
	}

	return map[string]any{
		"active_tab":     activeTab,
		"tabs":           tabList,
		"panes":          paneList,
		"projects":       projectList,
		"active_project": activeProject,
	}
}

// transcriptExistsFn probes an ABSOLUTE transcript path recorded by the hook.
// It reports (exists, answered): an unanswered probe is NOT evidence of absence,
// which is the distinction the caller's classification turns on.
//
// There is deliberately no CWD-derived probe any more. Deriving a transcript's
// project directory from the pane's CWD was the root cause of the bug this file
// now guards against — Claude keys that directory off the SESSION's working
// directory, so a session that moved into a git worktree is not under the pane's
// spawn CWD and the probe reported a live session as absent.
//
// Package var so tests never reach the real filesystem.
var transcriptExistsFn = statExistsWithinBudget

// readHookSessionFn reads the hook-recorded session record for a pane.
// Defaults to the real claudehook.ReadPersistedSession; tests override it so
// resolveSpawnArgs matrix tests never touch $QUIL_HOME/sessions/.
var readHookSessionFn = func(paneID string) (claudehook.SessionRecord, error) {
	return claudehook.ReadPersistedSession(config.QuilDir(), paneID)
}

// claudeHookExeFn resolves the path to the running quild binary, which the
// claude hook command invokes via its `claude-hook` subcommand. Defaults to
// os.Executable; tests override it to simulate the unresolvable-executable
// branch without depending on the test binary's real path.
var claudeHookExeFn = os.Executable

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
		"QUIL_HOOK_HOME=" + absQuilDir,
		"QUIL_HOOK_MODE=" + mode,
		"OPENCODE_CONFIG_CONTENT=" + cfg,
	}
}

// claudeHookSpawnPrep returns the --settings prefix args and env vars to add
// to a fresh claude-code spawn for hook registration. The hook command invokes
// the running quild binary's native `claude-hook` subcommand. Returns nil
// slices when the hook is unavailable (executable path unresolvable or settings
// JSON build fails) so the spawn proceeds without the hook — matching the
// pre-feature behaviour rather than failing the whole spawn. Logs a warning if
// userArgs already contain --settings; Claude treats later wins, so our
// prepend silently overrides the user's value.
func claudeHookSpawnPrep(quilDir, paneID, hookMode string, userArgs []string) (prefix, env []string) {
	exePath, err := claudeHookExeFn()
	if err != nil {
		log.Printf("warning: pane %s: cannot resolve quild executable: %v — session-id rotation tracking disabled", paneID, err)
		return nil, nil
	}
	js, err := claudehook.BuildSettingsJSON(claudehook.HookCommand(exePath))
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
	// QUIL_HOOK_HOME is passed explicitly so the hook subprocess writes to the
	// correct data dir; renamed from QUIL_HOME because children inherit the
	// pane env and an inherited QUIL_HOME retargeted dev builds at production.
	// Consumers fall back to QUIL_HOME for one release.
	return []string{"--settings", js}, []string{
		"QUIL_PANE_ID=" + paneID,
		"QUIL_HOOK_MODE=" + mode,
		"QUIL_HOOK_HOME=" + quilDir,
	}
}

// resumeTemplateFor returns the resume-arg template resolveSpawnArgs should
// expand on the restore branch. Dispatches by plugin name to plugin-specific
// promotion logic; default falls back to the plugin's configured ResumeArgs.
func resumeTemplateFor(p *plugin.PanePlugin, pane *Pane, claim sessionClaimFn) []string {
	switch {
	case p.Name == "claude-code" && p.Persistence.Strategy == "preassign_id":
		return claudeResumeTemplate(p, pane, claim)
	case p.Name == "opencode" && p.Persistence.Strategy == "session_scrape":
		return opencodeResumeTemplate(p, pane)
	default:
		return p.Persistence.ResumeArgs
	}
}

// sessionClaimFn atomically selects and CLAIMS the first candidate no other
// pane already holds, recording it on the pane.
//
// Selection and the write that records it MUST be one step. Two panes restoring
// concurrently (a tab switch and an MCP op arrive on different dispatch
// goroutines) would otherwise both observe the same session free and both spawn
// `claude --resume` on it — the duplicate-resume this guard exists to prevent,
// and reachable precisely because a pane that has not spawned yet has no PTY and
// so is invisible to a running-only test. Returns the first blocking holder when
// every candidate is taken.
//
// Threaded in as a parameter rather than read off the Daemon so the arg-merging
// matrix stays a table test; production passes (*Daemon).claimResumeSession.
type sessionClaimFn func(pane *Pane, cands []resumeCandidate) (chosen resumeCandidate, holder string, ok bool)

// candidateState is what we know about a candidate's transcript.
//
// The three-way split matters because "we could not find it" and "it is not
// there" are different facts, and conflating them is what produced the original
// bug: a session that had merely moved was treated as gone.
type candidateState int

const (
	candidateUnknown candidateState = iota // no evidence either way
	candidateLocated                       // the recorded path names this id and the file is there
	candidateMissing                       // the recorded path names this id and the file is NOT there
)

// resumeCandidate is one session a restored pane could attach to.
type resumeCandidate struct {
	id         string
	source     string
	transcript string
	state      candidateState
}

// claudeResumeTemplate decides which session a restored claude-code pane
// attaches to.
//
// The rule that matters: --continue is NOT a neutral fallback. It is Claude's
// most-recent-session-in-CWD lookup, so a pane whose own session we merely
// failed to LOCATE gets attached to a sibling pane's conversation — and on
// restore the sibling that respawned a second earlier is exactly the one it
// finds. Three panes appending into one transcript is a real incident this
// produced (2026-08-01).
//
// So a recorded id is resumed on its own authority. Candidates are ordered by
// SOURCE, not by whether we managed to find their transcript: locating a
// session is evidence it exists, but failing to locate one is not evidence it
// does not, and ranking a located low-authority id above an unlocated
// high-authority one reinstates the same silent swap in a narrower case. Only a
// candidate we can positively prove absent is skipped.
func claudeResumeTemplate(p *plugin.PanePlugin, pane *Pane, claim sessionClaimFn) []string {
	cands, sawRecorded := claudeResumeCandidates(pane)
	usable := usableResumeCandidates(cands)

	if len(usable) == 0 {
		if sawRecorded {
			// This pane HAS a session; we just cannot safely name one (every
			// candidate was proven gone, or was rejected as malformed). Falling
			// back to ResumeArgs here would be the --continue hijack.
			return freshClaudeSession(p, pane, "no usable recorded session")
		}
		// Genuinely no recorded session: nothing to preserve, so the plugin's
		// configured fallback stands. This is the one case --continue still owns.
		return p.Persistence.ResumeArgs
	}

	chosen, holder, ok := claim(pane, usable)
	if !ok {
		return freshClaudeSession(p, pane, "every candidate session is held by another pane (first: "+holder+")")
	}
	log.Printf("restore pane %s: resuming session %q (source=%s, located=%t)",
		pane.ID, chosen.id, chosen.source, chosen.state == candidateLocated)
	return []string{"--resume", "{session_id}"}
}

// freshClaudeSession gives the pane a brand-new session identity and returns the
// plugin's fresh-start args.
//
// Minting the id here is what "we will not resume" has to MEAN. Leaving the old
// value in PluginState would leave the pane advertising a session it is not in,
// so the occupancy map would report it as the holder — and a later Alt+R, which
// spawns with restoring=false, would hand that id straight to --session-id and
// adopt another pane's conversation for real.
func freshClaudeSession(p *plugin.PanePlugin, pane *Pane, why string) []string {
	id := uuid.New().String()
	pane.PluginMu.Lock()
	if pane.PluginState == nil {
		pane.PluginState = make(map[string]string)
	}
	pane.PluginState["session_id"] = id
	delete(pane.PluginState, "transcript_path")
	pane.PluginMu.Unlock()
	log.Printf("restore pane %s: starting a fresh session — %s", pane.ID, why)
	return p.Persistence.StartArgs
}

// claudeResumeCandidates lists the sessions a pane could resume, most
// authoritative first:
//
//  1. the hook record — the only source that tracks /clear, /resume and
//     compaction rotations, and the one that carries the transcript's real path;
//  2. PluginState["session_id"] from workspace.json — refreshed at shutdown, so
//     it lags a rotation;
//  3. PluginState["resume_session_id"] — the session the user picked at pane
//     creation, covering a restart before the first SessionStart hook fired.
//
// sawRecorded reports whether the pane named ANY session, including ones
// rejected as malformed. The caller needs that apart from the candidate count:
// "no session" and "a session we refuse to name" must not both fall through to
// --continue.
//
// Every pane field is captured under PluginMu and every probe runs after it is
// released: holding a pane mutex across a syscall is the shape of the daemon
// wedges this package documents at length.
func claudeResumeCandidates(pane *Pane) (cands []resumeCandidate, sawRecorded bool) {
	rec, err := readHookSessionFn(pane.ID)
	if err != nil {
		// A missing file is the ordinary case (no hook has fired yet). Anything
		// else means the authoritative source was silently discarded on the one
		// code path that exists to be trustworthy — say so.
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("restore pane %s: reading the hook session record failed: %v", pane.ID, err)
		}
		rec = claudehook.SessionRecord{}
	}

	pane.PluginMu.Lock()
	stateID := pane.PluginState["session_id"]
	statePath := pane.PluginState["transcript_path"]
	resumeID := pane.PluginState["resume_session_id"]
	pane.PluginMu.Unlock()

	add := func(id, source, transcript string) {
		if id == "" {
			return
		}
		sawRecorded = true
		// EVERY id is shape-checked, not just the unlocated ones. The id becomes
		// an argv token, and a recorded transcript path cannot stand in for that
		// check the way an on-disk <id>.jsonl filename does — the path is an
		// independent string, so any existing file would otherwise vouch for any
		// id. Logged by length, never by value, so a hostile string cannot forge
		// a log line (the daemon log is rendered by the F1 viewer, which does not
		// pass through a VT emulator).
		if !resumeSessionIDRe.MatchString(id) {
			log.Printf("restore pane %s: ignoring %s session id of length %d — not a canonical uuid",
				pane.ID, source, len(id))
			return
		}
		for _, c := range cands {
			if c.id == id {
				return // same session reached by two routes
			}
		}
		cands = append(cands, resumeCandidate{
			id: id, source: source, transcript: transcript, state: transcriptState(id, transcript),
		})
	}
	add(rec.ID, "hook", rec.TranscriptPath)
	// The workspace pair is written as one unit, so its path speaks only for its
	// own id. Both sources are consulted because either can be the survivor: the
	// hook file dies with a wiped $QUIL_HOME/sessions, the workspace copy with a
	// daemon killed before its shutdown refresh.
	add(stateID, "workspace", statePath)
	add(resumeID, "user-chosen", "")
	return cands, sawRecorded
}

// transcriptState classifies the evidence we hold for one candidate.
//
// A recorded path may only speak for the id it NAMES. Claude files a transcript
// as <session-id>.jsonl, so the filename is the binding; without that check an
// unrelated existing file would mark any id as located, and the id goes into
// argv. An unanswered probe is "no evidence", never "absent" — a stat that times
// out on a dead mount must not be read as proof a session was deleted.
func transcriptState(id, transcript string) candidateState {
	if transcript == "" || filepath.Base(transcript) != id+".jsonl" {
		return candidateUnknown
	}
	exists, answered := transcriptExistsFn(transcript)
	switch {
	case !answered:
		return candidateUnknown
	case exists:
		return candidateLocated
	default:
		return candidateMissing
	}
}

// usableResumeCandidates drops the candidates we can prove are gone, preserving
// SOURCE order for the rest. Order is never rearranged by whether a transcript
// was located — see claudeResumeTemplate for why that would reinstate the bug.
func usableResumeCandidates(cands []resumeCandidate) []resumeCandidate {
	usable := make([]resumeCandidate, 0, len(cands))
	for _, c := range cands {
		if c.state != candidateMissing {
			usable = append(usable, c)
		}
	}
	return usable
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
// resumeID is the pane's PluginState["resume_session_id"], captured by the
// caller UNDER PluginMu rather than read from the map here. Every other access
// to PluginState in this file is lock-guarded, and a concurrent map read and
// write in Go is a fatal runtime throw that would take the daemon and every
// pane's PTY down with it — the plugin scraper and refreshPluginStateFromHooks
// are both live writers to this same map. Passing the value in also keeps this
// function honestly pure for the arg-merging table tests.
// claim selects and records the session the restore branch will resume, refusing
// one another pane already holds. It is never nil: callers with no occupancy
// information pass claimAny, so a forgotten wiring fails in a test rather than
// silently dropping the guard in production.
func resolveSpawnArgs(p *plugin.PanePlugin, pane *Pane, restoring bool, resumeID string, claim sessionClaimFn) []string {
	args := append([]string{}, p.Command.Args...)

	// Instance-specific args override base args.
	if len(pane.InstanceArgs) > 0 {
		args = append([]string{}, pane.InstanceArgs...)
	}

	// Fresh start under preassign_id: append the plugin's StartArgs (after
	// {placeholder} expansion from PluginState).
	//
	// Unless the pane was created to resume an existing session, in which case
	// --resume REPLACES the start args rather than joining them: claude's
	// `--session-id <new>` (mint this id) and `--resume <existing>` (attach to
	// that id) are contradictory, and passing both is not a valid invocation.
	// The branch runs after the InstanceArgs override above, so runtime toggles
	// (--dangerously-skip-permissions, --enable-auto-mode, --chrome) still
	// compose with the resume exactly as they do with a fresh session.
	if !restoring && p.Persistence.Strategy == "preassign_id" {
		if resumeID != "" {
			args = append(args, "--resume", resumeID)
		} else if len(p.Persistence.StartArgs) > 0 {
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
			template := resumeTemplateFor(p, pane, claim)
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
		if dir := resolveSpawnDirWithin(*p, spawnDirProbeTimeout); dir != "" {
			return dir
		}
		// stale (directory removed since attach), or unreachable — fall through
	}
	// Best-effort; if Getwd fails we return "" and the spawn will fail
	// with a clear error from os/exec rather than silently land somewhere.
	cwd, _ := os.Getwd()
	return cwd
}

// spawnPane launches the appropriate process for a pane based on its plugin type.
// When restoring is true, resume strategies are applied (e.g., --resume for session_scrape).
func (d *Daemon) spawnPane(pane *Pane, ptySession apty.Session, restoring bool) error {
	// Default type. Type is PluginMu-protected — a lazy spawn (ensurePaneSpawned)
	// runs this concurrently with snapshot()/buildPaneInfos readers. Capture the
	// settled value under the lock; the assignment and read share one critical
	// section. (We hold spawnMu here, never PluginMu — single-field section, no
	// nesting with another spawnPane.)
	pane.PluginMu.Lock()
	if pane.Type == "" {
		pane.Type = "terminal"
	}
	// Cleared HERE rather than at each caller, because the field means "this
	// pane has no process" and this is the one function that gives it one.
	// The restart path (handleRestartPaneReq → spawnPane) does NOT go through
	// spawnRestoredPane, so clearing it there alone left Alt+R — the remedy
	// the error screen itself advertises — reviving the pane while the stale
	// error stayed painted over it, and the user typing blind into a live
	// shell they could not see.
	pane.SpawnError = ""
	typ := pane.Type
	pane.PluginMu.Unlock()

	p := d.registry.Get(typ)
	if p == nil {
		p = d.registry.Get("terminal") // fallback
	}

	cmd := p.Command.Cmd

	// Generate a session UUID for fresh preassign_id panes before computing
	// args, since resolveSpawnArgs expects PluginState["session_id"] to be
	// populated for the {session_id} expansion.
	//
	// A pane created with a resume target adopts THAT id instead of minting a
	// new one: the session it is about to join is its real session, so every
	// downstream consumer (refreshPluginStateFromHooks, the model/context
	// status segment, the restore probe) sees a coherent id from the first
	// instant rather than a UUID that never existed.
	//
	// resumeID is also captured here, under the same lock, and handed to
	// resolveSpawnArgs — see the note on that function for why it must not
	// read PluginState itself.
	var resumeID string
	if !restoring && p.Persistence.Strategy == "preassign_id" {
		// This branch is not only pane creation: handleRestartPaneReq (Alt+R)
		// also calls spawnPane with restoring=false. So a creation-time resume
		// pick must not be replayed blindly here — if the pane has since
		// recorded its own session (a /clear rotates it, and the hook writes
		// the new id), a restart has to reattach to THAT conversation, not the
		// one chosen days ago. The stale pick is retired at the same time so it
		// cannot resurface on a later restore either. Read off-lock: never hold
		// PluginMu across a file read.
		hookID := ""
		if p.Name == "claude-code" {
			if rec, err := readHookSessionFn(pane.ID); err == nil {
				hookID = rec.ID
			}
		}
		pane.PluginMu.Lock()
		if pane.PluginState == nil {
			pane.PluginState = make(map[string]string)
		}
		resumeID = pane.PluginState["resume_session_id"]
		if resumeID != "" && hookID != "" && hookID != resumeID {
			delete(pane.PluginState, "resume_session_id")
			pane.PluginState["session_id"] = hookID
			resumeID = ""
		}
		if pane.PluginState["session_id"] == "" {
			if resumeID != "" {
				pane.PluginState["session_id"] = resumeID
			} else {
				pane.PluginState["session_id"] = uuid.New().String()
			}
		}
		pane.PluginMu.Unlock()
	}

	args := resolveSpawnArgs(p, pane, restoring, resumeID, d.claimResumeSession)

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

	// Generic opt-in: any plugin whose hook producer records input history
	// gets the gate env. The hook subprocess reads QUIL_RECORD_HISTORY and
	// appends submitted prompts to the per-pane history store.
	if p.Command.RecordHistory {
		envVars = append(envVars, "QUIL_RECORD_HISTORY=1")
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
	// PluginMu protects pane.PTY (per the Pane struct doc): the memReport
	// collector reads it on a 5s timer goroutine. PluginMu is free here (taken
	// and released above at the PluginState init); lock ordering stays
	// spawnMu→PluginMu, consistent with ensurePaneSpawned.
	pane.PluginMu.Lock()
	pane.PTY = ptySession
	// Fresh PTY: reset the same-size guard so its first resize_pane is
	// always applied (see handleResizePane).
	pane.appliedCols, pane.appliedRows = 0, 0
	pane.PluginMu.Unlock()
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
	d.broadcast(msg)
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
				// Muting suppresses the visible notification (sidebar card,
				// MCP get_notifications), but a work-state transition
				// (turn start/stop/abort) must still reach live clients —
				// otherwise the TUI's per-pane `working` flag (and its
				// spinner) goes stale for the whole mute window and never
				// resyncs on unmute, since no later event exists to correct
				// it against. Bypass the persisted queue, still broadcast.
				if hookevents.ClassifyWorkEvent(e.Type) == hookevents.WorkEventNone {
					return
				}
				payload := toPaneEventPayload(e)
				msg, _ := ipc.NewMessage(ipc.MsgPaneEvent, payload)
				d.broadcast(msg)
				return
			}
		}
	}
	d.events.Push(e)
	payload := toPaneEventPayload(e)
	msg, _ := ipc.NewMessage(ipc.MsgPaneEvent, payload)
	d.broadcast(msg)
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
	logger.Info("hook events watcher started (200 ms tick, spool=%s)", config.EventsDir())
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
			payloads := d.hookSpool.Tick()
			if len(payloads) > 0 {
				logger.Debug("hook events tick: read %d payloads from spool", len(payloads))
			}
			for _, p := range payloads {
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
	// Daemon-side enforcement of the hooks tier — covers the case where
	// the script-side gate did not fire (older script on disk, env var
	// stripped by an opencode wrapper, etc.). "off" drops the event;
	// "default"/"verbose"/anything else fall through. Storm diagnostics
	// always pass — they're the rate limiter's own internal signal.
	if p.HookEvent != hookevents.EventStorm {
		var mode string
		switch p.Source {
		case hookevents.SourceClaude:
			mode = d.cfg.Notification.Hooks.Claude
		case hookevents.SourceOpenCode:
			mode = d.cfg.Notification.Hooks.OpenCode
		}
		if mode == "off" {
			return
		}
	}

	pane := d.session.Pane(p.PaneID)
	if pane == nil {
		logger.Debug("hook event for unknown pane=%s src=%s hook_event=%s",
			p.PaneID, p.Source, p.HookEvent)
		return
	}
	logger.Debug("emit hook event pane=%s src=%s hook_event=%s title=%q",
		p.PaneID, p.Source, p.HookEvent, p.Title)

	// Only real hook events count toward "this pane is hook-healthy". The
	// rate limiter's own synthetic storm diagnostic would otherwise flip
	// HookHealthy=true precisely when the pane has stopped delivering real
	// events — silencing the legacy idle excerpt during the 30 s window
	// when it's the user's last remaining notification surface.
	if p.HookEvent != hookevents.EventStorm {
		pane.PluginMu.Lock()
		pane.HookHealthy = true
		pane.LastHookEventAt = time.Now()
		// Model/context usage rides the turn-boundary events (claude
		// Stop/PostCompact, opencode session.idle) as Data keys. Stored on
		// the pane (runtime-only) so the workspace snapshot can deliver the
		// values to a client that attaches between turns.
		if p.Data["compacting"] == "1" {
			// PostCompact reset: the true reduced context size isn't known
			// until the next completed turn (the compaction summary carries no
			// assistant usage). Reset to the compacting sentinel — keeping the
			// model — so the status bar shows "<model> · compacting" instead of
			// the stale pre-compaction count until the next Stop reports the
			// real size.
			pane.LastContextTokens = ipc.ContextTokensCompacting
		} else if model := p.Data["model"]; model != "" {
			if tokens, err := strconv.ParseInt(p.Data["context_tokens"], 10, 64); err == nil && tokens >= 0 {
				pane.LastModel = model
				pane.LastContextTokens = tokens
			}
		}
		pane.PluginMu.Unlock()
	}

	// Compose the PaneEvent. The Type field encodes the source so MCP
	// consumers can filter by "hook.claude.*" or "hook.opencode.*" without
	// parsing the title. Severity defaults to info when the hook omitted it.
	severity := p.Severity
	if severity == "" {
		severity = hookevents.SeverityInfo
	}
	eventType := "hook." + p.Source + "." + p.HookEvent
	// Clamp timestamp to a sane window. A hook with a clock skew (container
	// with wrong NTP, malicious payload) might carry TsMs years off; the
	// sidebar would pin the event at the top or bottom forever. Accept ±1h
	// of the daemon's clock; anything else falls back to now.
	now := time.Now()
	var ts time.Time
	if p.TsMs == 0 {
		ts = now
	} else {
		ts = time.UnixMilli(p.TsMs)
		if ts.Before(now.Add(-time.Hour)) || ts.After(now.Add(time.Minute)) {
			ts = now
		}
	}

	// Build the Data map in a single allocation. Always carries the
	// source-tracking metadata so MCP consumers don't have to parse the
	// Type prefix.
	data := make(map[string]string, len(p.Data)+2)
	for k, v := range p.Data {
		data[k] = v
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
			d.sweepIdleOverlays(time.Now())
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
	// Tail copies only the trailing window — Bytes() copied the full ring on
	// every event emit. The extra 512 bytes give trimToNewlineSafe a scan
	// margin: with exactly 4096 bytes its len <= maxTail early-return would
	// skip the partial-escape-sequence trim entirely.
	raw := pane.OutputBuf.Tail(4096 + 512)
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
	// Tail copies only the trailing window — Bytes() copied the full ring on
	// every event emit. The extra 512 bytes give trimToNewlineSafe a scan
	// margin: with exactly 4096 bytes its len <= maxTail early-return would
	// skip the partial-escape-sequence trim entirely.
	raw := pane.OutputBuf.Tail(4096 + 512)
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

// buildPaneInfos snapshots every pane across all tabs into ipc.PaneInfo. A
// deferred (Pending) pane has no PTY yet and must report Running=false +
// Pending=true so an MCP client can see it exists without booting it.
//
// Locking: ExitCode and PTY are PluginMu-guarded; Pending is spawnMu-guarded.
// We read each under its own lock in separate critical sections — never both
// at once — so there's no lock-ordering hazard. This is a best-effort snapshot:
// a pane could in principle flip Pending→spawned between the two reads, but the
// list is informational and the worst case is a momentarily stale flag.
func (d *Daemon) buildPaneInfos() []ipc.PaneInfo {
	_, tabs, panesByTab, _, _ := d.session.SnapshotState()

	var panes []ipc.PaneInfo
	for _, tab := range tabs {
		for _, pane := range panesByTab[tab.ID] {
			// Type, CWD, ExitCode, PTY are PluginMu-protected (spawnRestoredPane
			// mutates Type/CWD on the lazy-spawn error paths). Read them all in
			// one critical section.
			pane.PluginMu.Lock()
			typ := pane.Type
			cwd := pane.CWD
			running := pane.PTY != nil && pane.ExitCode == nil
			pane.PluginMu.Unlock()
			if typ == "" {
				typ = "terminal"
			}
			pane.spawnMu.Lock()
			pending := pane.Pending
			pane.spawnMu.Unlock()
			panes = append(panes, ipc.PaneInfo{
				ID:           pane.ID,
				TabID:        tab.ID,
				TabName:      tab.Name,
				Name:         pane.Name,
				Type:         typ,
				CWD:          cwd,
				Running:      running,
				Pending:      pending,
				InstanceName: pane.InstanceName,
			})
		}
	}
	return panes
}

func (d *Daemon) handleListPanesReq(conn *ipc.Conn, msg *ipc.Message) {
	respondTo(conn, msg.ID, ipc.MsgListPanesResp, ipc.ListPanesRespPayload{
		Panes: d.buildPaneInfos(),
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
	d.ensurePaneSpawned(pane)
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

// buildPaneStatus returns the status payload for a single pane without
// spawning it. It mirrors the locking discipline of buildPaneInfos: PluginMu
// guards Type/CWD/PTY/ExitCode; spawnMu guards Pending. Both critical
// sections are kept separate (no lock-ordering hazard).
func (d *Daemon) buildPaneStatus(pane *Pane) ipc.PaneStatusRespPayload {
	pane.PluginMu.Lock()
	typ := pane.Type
	cwd := pane.CWD
	exitCode := pane.ExitCode
	running := pane.PTY != nil && exitCode == nil
	pane.PluginMu.Unlock()
	if typ == "" {
		typ = "terminal"
	}
	pane.spawnMu.Lock()
	pending := pane.Pending
	pane.spawnMu.Unlock()

	return ipc.PaneStatusRespPayload{
		PaneID:   pane.ID,
		Running:  running,
		Pending:  pending,
		ExitCode: exitCode,
		Type:     typ,
		CWD:      cwd,
		Name:     pane.Name,
	}
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

	// Match buildPaneInfos: a deferred pane (PTY==nil) reports Running=false even
	// though ExitCode is nil, so get_pane_status and list_panes agree. This
	// handler stays non-spawning by design — see buildPaneStatus.
	respondTo(conn, msg.ID, ipc.MsgPaneStatusResp, d.buildPaneStatus(pane))
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
	// Clear any deferred state first so the restart below operates on a normal
	// live pane (Pending=false) rather than racing the lazy-spawn guard.
	d.ensurePaneSpawned(pane)
	d.highlightPane(pane.ID)

	// Close existing PTY. pane.PTY is PluginMu-protected (the 5 s memReport
	// collector reads it under PluginMu) — swap it out under the lock, then
	// run the Close() syscall after releasing it (never hold a mutex across a
	// syscall).
	pane.PluginMu.Lock()
	old := pane.PTY
	pane.PTY = nil
	pane.PluginMu.Unlock()
	if old != nil {
		// Async: Close → cmd.Wait blocks until the child is reaped, and a
		// wedged child is precisely when restart_pane gets called — a
		// synchronous Close would park this conn's dispatch goroutine.
		go func(s apty.Session) {
			if err := s.Close(); err != nil {
				logger.Debug("restart pane %s: old PTY close: %v", pane.ID, err)
			}
		}(old)
	}

	// Reset exit state
	pane.PluginMu.Lock()
	pane.ExitCode = nil
	pane.ExitedAt = time.Time{}
	// Clear mouse-mode state: the respawned child re-emits its own enable burst,
	// so carrying the old tracking=true forward would make the TUI forward wheel
	// notches as escape bytes to the pre-burst screen (e.g. a shell prompt).
	// Mirrors the TUI's ResetVT clearing its local flags on respawn. Reset the
	// broadcast bookkeeping too so the fresh (empty) state is delivered promptly.
	pane.MouseModes = mouseModeState{}
	pane.modeScanTail = nil
	pane.mouseBroadcast = mouseModeState{}
	pane.lastMouseBroadcastAt = time.Time{}
	// Clear model/context usage: the respawned child starts a fresh (or
	// resumed) conversation and the next completed turn re-reports it.
	pane.LastModel = ""
	pane.LastContextTokens = 0
	pane.PluginMu.Unlock()

	// Clear output buffer
	if pane.OutputBuf != nil {
		pane.OutputBuf.Reset()
	}

	// Respawn with same config, using last known dimensions. Under PluginMu:
	// another client can be resizing this pane while this restart runs.
	cols, rows := paneSize(pane)
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	success := true
	// Alt+R on a pane whose worktree is still missing must reach the same
	// verdict restore does — spawning here would put a shell in the daemon's
	// default directory, the relocation the SpawnError path exists to prevent.
	// spawnPane clears SpawnError as it STARTS — not on success — so a retry
	// that finds the worktree back recovers with no extra bookkeeping, and a
	// retry that fails for some other reason reports Success:false with the
	// stale worktree complaint correctly gone.
	if d.refuseMissingWorktree(pane) {
		success = false
	} else {
		ptySession := apty.NewWithSize(cols, rows)
		if err := d.spawnPane(pane, ptySession, false); err != nil {
			log.Printf("handleRestartPaneReq: spawn: %v", err)
			success = false
		}
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
	d.ensurePaneSpawned(pane)
	d.highlightPane(pane.ID)

	// Snapshotted together under PluginMu — a concurrent resize would
	// otherwise render the screenshot at a geometry that never existed.
	paneCols, paneRows := paneSize(pane)

	width := req.Width
	if width <= 0 {
		width = paneCols
	}
	if width <= 0 {
		width = 80
	}
	if width > 500 {
		width = 500
	}
	height := req.Height
	if height <= 0 {
		height = paneRows
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
	d.ensureTabSpawned(req.TabID)
	d.broadcastState()
	d.requestSnapshot()

	respondTo(conn, msg.ID, ipc.MsgSwitchTabResp, ipc.SwitchTabRespPayload{
		TabID: req.TabID,
	})
}

func (d *Daemon) handleListTabsReq(conn *ipc.Conn, msg *ipc.Message) {
	activeTab, tabs, panesByTab, _, _ := d.session.SnapshotState()

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
	// Mirror handleDestroyPane's PaneID validation so this MCP-facing path
	// is also defended against path-traversal payloads.
	if !isValidHexID(req.PaneID, "pane-") {
		log.Printf("handleDestroyPaneReq: rejected malformed PaneID %q", req.PaneID)
		respondTo(conn, msg.ID, ipc.MsgDestroyPaneResp, ipc.DestroyPaneRespPayload{})
		return
	}

	pane := d.session.Pane(req.PaneID)
	if pane == nil {
		respondTo(conn, msg.ID, ipc.MsgDestroyPaneResp, ipc.DestroyPaneRespPayload{})
		return
	}
	d.highlightPane(pane.ID)

	// Same cleanup as handleDestroyPane: spool file, ingester state, and
	// persisted session-id files before the pane disappears.
	d.cleanupPaneArtifacts(req.PaneID)

	tabID := pane.TabID
	if err := d.session.DestroyPane(req.PaneID); err != nil {
		log.Printf("handleDestroyPaneReq: %v", err)
		respondTo(conn, msg.ID, ipc.MsgDestroyPaneResp, ipc.DestroyPaneRespPayload{})
		return
	}

	// Auto-create replacement if the last normal pane in the tab was
	// destroyed. Delegates to ensureTabNotEmpty (shared with handleDestroyPane)
	// so overlay-pane accounting and auto-recovery are identical on both paths.
	d.ensureTabNotEmpty(tabID)

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
	d.ensurePaneSpawned(pane)

	// Switch to the pane's tab
	d.session.SwitchTab(pane.TabID)

	// Broadcast to TUI clients so they can set focus
	broadcast, _ := ipc.NewMessage(ipc.MsgSetActivePane, ipc.SetActivePanePayload{
		PaneID: req.PaneID,
	})
	d.broadcast(broadcast)

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
	activeTab, tabs, panesByTab, _, _ := d.session.SnapshotState()
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

// historyPreviewBytes caps each list row's flattened preview. The TUI renders
// one row per entry and truncates to the dialog's cell width, so this only has
// to comfortably exceed the widest row a terminal can show — 512 bytes covers
// ~500 columns of ASCII and ~170 of 3-byte UTF-8, while keeping the response
// for a full 200-entry ring around 100 KB rather than unbounded.
const historyPreviewBytes = 512

// handlePaneHistoryReq serves the per-pane input-history preview list, newest
// first. It compacts the file to the ring cap before reading so the on-disk
// history never grows unbounded.
func (d *Daemon) handlePaneHistoryReq(conn *ipc.Conn, msg *ipc.Message) {
	var p ipc.PaneHistoryReqPayload
	if err := msg.DecodePayload(&p); err != nil {
		return
	}
	resp := ipc.PaneHistoryRespPayload{PaneID: p.PaneID}
	if !isValidHexID(p.PaneID, "pane-") {
		respondTo(conn, msg.ID, ipc.MsgPaneHistoryResp, resp)
		return
	}
	dir := config.QuilDir()
	if err := panehistory.Compact(dir, p.PaneID, panehistory.MaxEntries); err != nil {
		log.Printf("history compact pane %s: %v", p.PaneID, err)
	}
	entries, err := panehistory.Read(dir, p.PaneID)
	if err != nil {
		log.Printf("history read pane %s: %v", p.PaneID, err)
	}
	for i := len(entries) - 1; i >= 0; i-- { // newest first
		e := entries[i]
		resp.Entries = append(resp.Entries, ipc.HistoryEntryMeta{
			TsMs:    e.TsMs,
			Preview: panehistory.PreviewLine(e.Text, historyPreviewBytes),
		})
	}
	respondTo(conn, msg.ID, ipc.MsgPaneHistoryResp, resp)
}

// handlePaneHistoryEntryReq serves one history entry's full text, looked up by
// its TsMs id. Found=false when no entry matches.
func (d *Daemon) handlePaneHistoryEntryReq(conn *ipc.Conn, msg *ipc.Message) {
	var p ipc.PaneHistoryEntryReqPayload
	if err := msg.DecodePayload(&p); err != nil {
		return
	}
	resp := ipc.PaneHistoryEntryRespPayload{PaneID: p.PaneID, TsMs: p.TsMs}
	if !isValidHexID(p.PaneID, "pane-") {
		respondTo(conn, msg.ID, ipc.MsgPaneHistoryEntryResp, resp)
		return
	}
	entries, err := panehistory.Read(config.QuilDir(), p.PaneID)
	if err != nil {
		log.Printf("history read pane %s: %v", p.PaneID, err)
	}
	for _, e := range entries {
		if e.TsMs == p.TsMs {
			resp.Text = e.Text
			resp.Found = true
			break
		}
	}
	respondTo(conn, msg.ID, ipc.MsgPaneHistoryEntryResp, resp)
}
