package daemon

import (
	"log"
	"path/filepath"
	"regexp"

	"github.com/artyomsv/quil/internal/claudesessions"
	"github.com/artyomsv/quil/internal/ipc"
)

// resumeSessionIDRe is the canonical UUID shape, which is what Claude actually
// mints for a session id. The value arrives over IPC and becomes the operand of
// `--resume` in the spawned process's argv, so it is validated rather than
// trusted: any client can reach the socket.
//
// Deliberately stricter than claudehook's `^[0-9a-fA-F-]{32,64}$`, which admits
// tokens that are all dashes or start with one — flag-shaped argv the arg
// parser downstream would have to reject. Nothing legitimate is lost: every id
// this is compared against comes from a `<uuid>.jsonl` filename.
var resumeSessionIDRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// applyResumeSessionID records a caller-supplied resume target on a freshly
// created pane. resolveSpawnArgs turns it into `--resume <id>` in place of the
// preassign_id strategy's `--session-id <new-uuid>`.
//
// Two ways a value is dropped, both degrading to a fresh session rather than
// failing the spawn — the user asked for a pane, and a fresh pane is a working
// pane:
//
//  1. Malformed shape. Logged by LENGTH only, never by value, so a hostile
//     string cannot forge log lines.
//  2. The session is already open in a live pane. The TUI greys these rows out
//     and refuses Enter, but that is presentation: this is where the guarantee
//     is actually made. Without it any IPC client could hand us a session a
//     running pane holds, and the two claude processes would interleave appends
//     into one transcript until the loser's history is gone. It also closes the
//     TOCTOU the TUI cannot: its listing is fetched at T0 and committed at T1,
//     and another pane can claim the session in between.
func (d *Daemon) applyResumeSessionID(pane *Pane, raw string) {
	if raw == "" {
		return
	}
	if !resumeSessionIDRe.MatchString(raw) {
		log.Printf("create pane: ignoring malformed resume_session_id (len=%d); starting a fresh session", len(raw))
		return
	}
	if holder, busy := d.liveClaudeSessionIDs()[raw]; busy && holder != pane.ID {
		log.Printf("create pane: session already open in pane %s; starting a fresh session instead", holder)
		return
	}
	// CreatePane may already have published the pane into the session maps, so
	// a concurrent snapshot goroutine can be reading PluginState — same lock
	// discipline as the Overlay/Muted writes alongside this call.
	pane.PluginMu.Lock()
	if pane.PluginState == nil {
		pane.PluginState = make(map[string]string)
	}
	pane.PluginState["resume_session_id"] = raw
	pane.PluginMu.Unlock()
}

// listClaudeSessionsFn is the discovery seam. Package-level var (not a direct
// call) so tests can enumerate sessions without a real ~/.claude directory —
// same pattern as claudeSessionExistsFn / readHookSessionIDFn.
var listClaudeSessionsFn = claudesessions.List

// handleClaudeSessionsReq answers the pane setup dialog's session picker.
//
// The scan runs on a WORKER GOROUTINE, never the dispatch goroutine: it reads
// up to MaxSessions transcript heads off disk, and a slow or cold filesystem
// would otherwise stall IPC for every pane behind it. Same rule
// handleStageUpdateReq follows for its network call.
//
// Single-flight, for the same reason handleStageUpdateReq guards with
// updateStaging: one request costs up to MaxSessions × titleScanBytes of disk
// reads, which is orders of magnitude more than parsing the frame that asked
// for it. Without the guard a client looping on this message type accumulates
// goroutines that each hold an fd and a 64 KiB buffer until the daemon is
// starved — and a claude pane running an injected script is exactly such a
// client, already inside the 0600 socket. The TUI only ever issues one request
// per directory change, so it never trips this.
func (d *Daemon) handleClaudeSessionsReq(conn *ipc.Conn, msg *ipc.Message) {
	if !d.sessionScanning.CompareAndSwap(false, true) {
		respondTo(conn, msg.ID, ipc.MsgClaudeSessionsResp, ipc.ClaudeSessionsRespPayload{
			CWD:   claudeSessionsReqCWD(msg),
			Error: "another session scan is already running",
		})
		return
	}
	go func() {
		defer d.sessionScanning.Store(false)
		respondTo(conn, msg.ID, ipc.MsgClaudeSessionsResp, d.claudeSessionsResponse(msg))
	}()
}

// claudeSessionsReqCWD extracts just the CWD from a request so a rejected
// response can still echo it. The echo is what the TUI matches on; an error
// carrying the wrong CWD would be dropped as stale and the field would sit on
// "Scanning…" until its timeout instead of showing the reason.
func claudeSessionsReqCWD(msg *ipc.Message) string {
	var req ipc.ClaudeSessionsReqPayload
	if err := msg.DecodePayload(&req); err != nil {
		return ""
	}
	return req.CWD
}

// claudeSessionsResponse decodes a request, enumerates the sessions for its
// CWD, and annotates the ones a live pane already occupies. Split from the
// handler so the decode → scan → annotate → echo seam is testable without an
// ipc.Conn.
//
// CONTRACT: CWD echoes req.CWD VERBATIM — never cleaned, resolved, or
// case-folded. The TUI drops any response whose echoed CWD differs from the
// directory currently highlighted in the dialog, so normalizing here would make
// a legitimate request look permanently stale and hang the field on "Scanning…".
func (d *Daemon) claudeSessionsResponse(msg *ipc.Message) ipc.ClaudeSessionsRespPayload {
	var req ipc.ClaudeSessionsReqPayload
	if err := msg.DecodePayload(&req); err != nil {
		log.Printf("handleClaudeSessionsReq: decode: %v", err)
		return ipc.ClaudeSessionsRespPayload{Error: "malformed request"}
	}
	if req.CWD == "" {
		return ipc.ClaudeSessionsRespPayload{CWD: req.CWD}
	}

	// Scan the SAME directory the pane will actually spawn in. handleCreatePane
	// runs EvalSymlinks before spawning, and claude keys its project directory
	// off the resolved path — so scanning the raw value would list the wrong
	// directory whenever the user browsed through a symlink (`~/proj` →
	// `/mnt/work/proj`): an empty picker for a folder with a hundred sessions,
	// or worse, offering ids that `--resume` then cannot find. On Windows this
	// also normalizes path case, which EscapeCWD is sensitive to.
	//
	// The ECHO still carries req.CWD verbatim — that contract is about matching
	// the TUI's staleness check, not about which directory was read.
	scanCWD := req.CWD
	if resolved, err := filepath.EvalSymlinks(req.CWD); err == nil {
		scanCWD = resolved
	}

	sessions, truncated, err := listClaudeSessionsFn(scanCWD)
	if err != nil {
		log.Printf("claude sessions: list %q: %v", scanCWD, err)
		return ipc.ClaudeSessionsRespPayload{CWD: req.CWD, Error: "could not read session history"}
	}

	inUse := d.liveClaudeSessionIDs()
	out := make([]ipc.ClaudeSessionInfo, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, ipc.ClaudeSessionInfo{
			ID:          s.ID,
			Title:       s.Title,
			ModifiedMs:  s.Modified.UnixMilli(),
			InUsePaneID: inUse[s.ID],
		})
	}
	return ipc.ClaudeSessionsRespPayload{
		CWD:       req.CWD,
		Sessions:  out,
		Truncated: truncated,
	}
}

// liveClaudeSessionIDs maps each session id currently held by a running
// claude-code pane to that pane's id.
//
// Two phases, deliberately: every pane field is captured first, then the lock
// is released BEFORE any file is touched. Reading hook files while holding a
// pane's PluginMu would put filesystem latency inside a critical section the
// snapshot loop, attach, and every hook event contend on — the exact shape of
// the wedges documented in CLAUDE.md. (SnapshotState releases sm.mu on return,
// so the per-pane captures below already run outside the session-manager lock;
// only PluginMu is held here, and only for the field reads.)
//
// The hook file is authoritative: it tracks /clear, /resume, and compaction
// rotations, whereas PluginState["session_id"] is only refreshed at shutdown
// and would report a pre-rotation id for a long-running pane. PluginState is
// the fallback for a pane whose SessionStart hook has not fired yet.
//
// Only panes with a LIVE process count. A pane whose claude exited (Ctrl+D, a
// crash) still sits in its tab holding an id in PluginState, and counting that
// as "in use" would block the session permanently behind a message telling the
// user to close a pane that has nothing running in it.
func (d *Daemon) liveClaudeSessionIDs() map[string]string {
	type claudePane struct {
		paneID  string
		stateID string
	}
	_, tabs, panesByTab := d.session.SnapshotState()

	var captured []claudePane
	for _, tab := range tabs {
		for _, pane := range panesByTab[tab.ID] {
			pane.PluginMu.Lock()
			typ := pane.Type
			stateID := pane.PluginState["session_id"]
			// Same liveness test buildPaneInfos uses for PaneInfo.Running.
			running := pane.PTY != nil && pane.ExitCode == nil
			pane.PluginMu.Unlock()
			if typ != "claude-code" || !running {
				continue
			}
			captured = append(captured, claudePane{paneID: pane.ID, stateID: stateID})
		}
	}

	// Off-lock from here: hook file reads.
	inUse := make(map[string]string, len(captured))
	for _, p := range captured {
		id := p.stateID
		if hookID, err := readHookSessionIDFn(p.paneID); err == nil && hookID != "" {
			id = hookID
		}
		if id != "" {
			inUse[id] = p.paneID
		}
	}
	return inUse
}
