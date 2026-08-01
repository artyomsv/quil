package daemon

import (
	"context"
	"log"
	"path/filepath"
	"regexp"
	"time"

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
	// The occupancy test and the write it gates are ONE atomic step. Without
	// the lock, two clients creating panes for the same session on their own
	// dispatch goroutines would both find it free — neither has spawned yet, so
	// neither is visible to the other — and both would spawn `claude --resume`
	// on one transcript. The caller must have published the pane into the
	// session maps before calling, or its own claim is invisible to the racing
	// caller.
	d.resumeClaimMu.Lock()
	defer d.resumeClaimMu.Unlock()

	if holder, busy := d.claimedClaudeSessionIDs()[raw]; busy && holder != pane.ID {
		log.Printf("create pane: session already claimed by pane %s; starting a fresh session instead", holder)
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
// discoveryTimeout bounds one filesystem discovery call. Both users hold a
// single-flight slot, so an unbounded read wedges the feature rather than just
// the request that triggered it.
const discoveryTimeout = 10 * time.Second

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
	rejection, ok := d.beginClaudeSessionsScan(msg)
	if !ok {
		respondTo(conn, msg.ID, ipc.MsgClaudeSessionsResp, rejection)
		return
	}
	go func() {
		defer d.sessionScanning.Store(false)
		respondTo(conn, msg.ID, ipc.MsgClaudeSessionsResp, d.claudeSessionsResponse(msg))
	}()
}

// beginClaudeSessionsScan claims the single-flight slot. When the slot is
// already taken it returns the rejection payload the caller must send back and
// ok=false; the caller releases the slot with d.sessionScanning.Store(false).
//
// Split out from the handler because ipc.Conn cannot be constructed outside its
// own package, so the handler wrapper is untestable — but the decision it makes
// is exactly the part worth pinning, and this way a test can drive it.
func (d *Daemon) beginClaudeSessionsScan(msg *ipc.Message) (ipc.ClaudeSessionsRespPayload, bool) {
	if d.sessionScanning.CompareAndSwap(false, true) {
		return ipc.ClaudeSessionsRespPayload{}, true
	}
	return ipc.ClaudeSessionsRespPayload{
		CWD:   claudeSessionsReqCWD(msg),
		Error: "another session scan is already running",
	}, false
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

	// Bounded because this call holds the single-flight slot: a scan that never
	// returns does not just stall its own request, it makes every later one
	// answer "another session scan is already running" for the life of the
	// daemon. The budget is generous — 200 transcripts × 64 KiB of reads is
	// normally well under a second.
	ctx, cancel := context.WithTimeout(context.Background(), discoveryTimeout)
	defer cancel()

	sessions, truncated, err := listClaudeSessionsFn(ctx, scanCWD)
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

// readClaudeSessionDetailFn is the deep-read seam, mirroring
// listClaudeSessionsFn so tests never touch a real ~/.claude directory.
var readClaudeSessionDetailFn = claudesessions.ReadDetail

// handleClaudeSessionDetailReq answers the picker's info key for one session.
//
// Worker goroutine and single-flight for the same reasons as
// handleClaudeSessionsReq, but on a SEPARATE atomic: a detail read and a
// directory listing are different requests, and making one wait on the other
// would have opening the info panel fail merely because a listing was in
// flight — which is exactly when the user opens it.
//
// One request streams a whole transcript, which is unbounded in a way the
// listing is not (that one head-reads a fixed 64 KiB per file). The pre-filter
// in ReadDetail keeps it a byte scan rather than a full unmarshal, but the
// single-flight is what stops a client looping on this message type from
// stacking concurrent multi-MB reads.
func (d *Daemon) handleClaudeSessionDetailReq(conn *ipc.Conn, msg *ipc.Message) {
	rejection, ok := d.beginClaudeSessionDetailRead(msg)
	if !ok {
		respondTo(conn, msg.ID, ipc.MsgClaudeSessionDetailResp, rejection)
		return
	}
	go func() {
		defer d.sessionDetailReading.Store(false)
		respondTo(conn, msg.ID, ipc.MsgClaudeSessionDetailResp, claudeSessionDetailResponse(msg))
	}()
}

// beginClaudeSessionDetailRead claims the single-flight slot, returning the
// rejection payload to send back when it is already taken. Split from the
// handler for the same reason beginClaudeSessionsScan is: ipc.Conn cannot be
// constructed outside its package, so the decision is only testable on its own.
func (d *Daemon) beginClaudeSessionDetailRead(msg *ipc.Message) (ipc.ClaudeSessionDetailRespPayload, bool) {
	if d.sessionDetailReading.CompareAndSwap(false, true) {
		return ipc.ClaudeSessionDetailRespPayload{}, true
	}
	cwd, id := claudeSessionDetailReqKey(msg)
	return ipc.ClaudeSessionDetailRespPayload{
		CWD:       cwd,
		SessionID: id,
		Error:     "another session read is already running",
	}, false
}

// claudeSessionDetailReqKey extracts the echo key from a request so a rejection
// still identifies the row it belongs to. Without it the TUI drops the error as
// stale and the panel sits on "Reading…" until its timeout.
func claudeSessionDetailReqKey(msg *ipc.Message) (cwd, sessionID string) {
	var req ipc.ClaudeSessionDetailReqPayload
	if err := msg.DecodePayload(&req); err != nil {
		return "", ""
	}
	return req.CWD, req.SessionID
}

// claudeSessionDetailResponse decodes a request and summarizes the one
// transcript it names. No receiver: unlike the listing, nothing here consults
// daemon state — occupancy is already carried by the row the user is looking at.
//
// CONTRACT: CWD and SessionID echo the request VERBATIM, the same staleness
// contract claudeSessionsResponse documents. The directory READ is the
// symlink-resolved one, for the same reason: claude keys its project directory
// off the resolved path, so a CWD reached through a symlink would otherwise
// look like a session that does not exist.
//
// The session id is validated against the canonical UUID shape before it
// reaches the filesystem. ReadDetail rejects separators on its own, but this
// value arrives over a socket any pane's child process can reach, and a
// filename component built from unvalidated input is worth refusing twice.
func claudeSessionDetailResponse(msg *ipc.Message) ipc.ClaudeSessionDetailRespPayload {
	var req ipc.ClaudeSessionDetailReqPayload
	if err := msg.DecodePayload(&req); err != nil {
		log.Printf("handleClaudeSessionDetailReq: decode: %v", err)
		return ipc.ClaudeSessionDetailRespPayload{Error: "malformed request"}
	}
	echo := ipc.ClaudeSessionDetailRespPayload{CWD: req.CWD, SessionID: req.SessionID}
	if req.CWD == "" || req.SessionID == "" {
		echo.Error = "no session selected"
		return echo
	}
	if !resumeSessionIDRe.MatchString(req.SessionID) {
		// By length only, never by value: the id is attacker-controlled text.
		log.Printf("claude session detail: rejecting malformed session id (len=%d)", len(req.SessionID))
		echo.Error = "invalid session id"
		return echo
	}

	readCWD := req.CWD
	if resolved, err := filepath.EvalSymlinks(req.CWD); err == nil {
		readCWD = resolved
	}

	// Same reasoning as the listing: this holds sessionDetailReading, and the
	// read streams a whole transcript (88 MB observed) because the last prompt
	// is only knowable at the end.
	ctx, cancel := context.WithTimeout(context.Background(), discoveryTimeout)
	defer cancel()

	detail, err := readClaudeSessionDetailFn(ctx, readCWD, req.SessionID)
	if err != nil {
		log.Printf("claude session detail: read %q: %v", req.SessionID, err)
		echo.Error = "could not read this session's transcript"
		return echo
	}

	echo.FirstPrompt = detail.FirstPrompt
	echo.LastPrompt = detail.LastPrompt
	echo.UserPrompts = detail.UserPrompts
	echo.SizeBytes = detail.SizeBytes
	if !detail.Started.IsZero() {
		echo.StartedMs = detail.Started.UnixMilli()
	}
	if !detail.Modified.IsZero() {
		echo.ModifiedMs = detail.Modified.UnixMilli()
	}
	return echo
}

// liveClaudeSessionIDs maps each session id currently held by a RUNNING
// claude-code pane to that pane's id. This is what the picker displays as
// "open in another pane"; use claimedClaudeSessionIDs for the admission check,
// which additionally covers panes that have claimed a session but not yet
// spawned into it.
func (d *Daemon) liveClaudeSessionIDs() map[string]string {
	return d.claudeSessionIDs(false)
}

// claimedClaudeSessionIDs is liveClaudeSessionIDs plus the pending claims: a
// pane that has been told to resume a session but whose process has not started
// yet. Those panes have no PTY, so a running-only test cannot see them — which
// is precisely the window two concurrent creates would slip through.
//
// It deliberately does NOT include exited panes: their transcript is free
// again, and holding it would block the session behind a pane with nothing
// running in it.
func (d *Daemon) claimedClaudeSessionIDs() map[string]string {
	return d.claudeSessionIDs(true)
}

// claimResumeSession is the restore path's occupancy guard — the sessionClaimFn
// spawnPane hands to resolveSpawnArgs. It takes the first candidate no other
// pane holds and records it, or reports the holder that blocked the first one.
//
// The whole walk runs under resumeClaimMu, for the same reason
// applyResumeSessionID does: a lazy spawn (tab switch, MCP op) runs on its
// caller's goroutine and can race another pane's spawn for the same session.
// Querying occupancy under the lock and writing the claim after it were TWO
// steps, so both panes could see the session free and both resume it — the
// duplicate-resume the guard exists to prevent, and reachable in the ordinary
// case where two panes were left holding the same id.
//
// The map is built ONCE for the whole list rather than per candidate: building
// it walks every pane and reads every hook record, and respawnPanes calls this
// once per restored pane inside the pre-listen readiness budget.
func (d *Daemon) claimResumeSession(pane *Pane, cands []resumeCandidate) (resumeCandidate, string, bool) {
	d.resumeClaimMu.Lock()
	defer d.resumeClaimMu.Unlock()

	held := d.claimedClaudeSessionIDs()
	blocker := ""
	for _, c := range cands {
		if holder, busy := held[c.id]; busy && holder != pane.ID {
			if blocker == "" {
				blocker = holder
			}
			continue
		}
		recordResumeSession(pane, c)
		return c, "", true
	}
	return resumeCandidate{}, blocker, false
}

// recordResumeSession writes the chosen id and its transcript path as ONE unit.
//
// The pair is only meaningful together: a path left behind while the id moves on
// would vouch for a transcript nobody checked, so a candidate with no path
// DELETES the key rather than inheriting the previous session's. Callers hold
// resumeClaimMu; PluginMu nests inside it, matching applyResumeSessionID.
func recordResumeSession(pane *Pane, c resumeCandidate) {
	pane.PluginMu.Lock()
	defer pane.PluginMu.Unlock()
	if pane.PluginState == nil {
		pane.PluginState = make(map[string]string)
	}
	pane.PluginState["session_id"] = c.id
	if c.transcript != "" {
		pane.PluginState["transcript_path"] = c.transcript
	} else {
		delete(pane.PluginState, "transcript_path")
	}
}

// claimAny is the sessionClaimFn for callers with no occupancy information: it
// takes the first candidate unconditionally. Used by the pure arg-matrix tests.
//
// It exists so sessionClaimFn is never nil. A nullable behaviour-changing
// callback survives exactly as long as nobody adds a second production call
// site; with this, a forgotten wiring fails loudly in a test rather than
// silently dropping the guard in production.
func claimAny(pane *Pane, cands []resumeCandidate) (resumeCandidate, string, bool) {
	if len(cands) == 0 {
		return resumeCandidate{}, "", false
	}
	recordResumeSession(pane, cands[0])
	return cands[0], "", true
}

// claudeSessionIDs maps each session id held by a claude-code pane to that
// pane's id.
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
// An EXITED pane never counts. Its claude is gone (Ctrl+D, a crash) but the
// pane still sits in its tab holding an id in PluginState, and counting that
// would block the session permanently behind a message telling the user to
// close a pane with nothing running in it.
//
// includePending decides whether a pane that has not started yet counts. Such a
// pane has no PTY, so it looks identical to "not running" — but it carries a
// resume_session_id it is about to spawn into, and that is exactly the claim a
// concurrent create must be able to see.
func (d *Daemon) claudeSessionIDs(includePending bool) map[string]string {
	type claudePane struct {
		paneID   string
		stateID  string
		resumeID string
		pending  bool
	}
	_, tabs, panesByTab := d.session.SnapshotState()

	var captured []claudePane
	for _, tab := range tabs {
		for _, pane := range panesByTab[tab.ID] {
			pane.PluginMu.Lock()
			typ := pane.Type
			stateID := pane.PluginState["session_id"]
			resumeID := pane.PluginState["resume_session_id"]
			hasPTY := pane.PTY != nil
			// Same liveness test buildPaneInfos uses for PaneInfo.Running.
			running := hasPTY && pane.ExitCode == nil
			pane.PluginMu.Unlock()

			if typ != "claude-code" {
				continue
			}
			// No PTY at all = never spawned: either just created (its claim is
			// live) or deferred by lazy restore (it will spawn into that
			// session on first access). Both are genuine claims.
			pending := !hasPTY
			if !running && !(pending && includePending) {
				continue
			}
			captured = append(captured, claudePane{
				paneID: pane.ID, stateID: stateID, resumeID: resumeID, pending: pending,
			})
		}
	}

	// Off-lock from here: hook file reads.
	inUse := make(map[string]string, len(captured))
	for _, p := range captured {
		id := p.stateID
		if rec, err := readHookSessionFn(p.paneID); err == nil && rec.ID != "" {
			id = rec.ID
		}
		// A pane that has not spawned yet has no session_id until spawnPane
		// seeds one, so its claim lives in resume_session_id.
		if id == "" && p.pending {
			id = p.resumeID
		}
		if id != "" {
			inUse[id] = p.paneID
		}
	}
	return inUse
}
