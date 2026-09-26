package daemon

import (
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/logger"
)

// Multi-client sync: the registry of ATTACHED clients and the size-master
// election over it.
//
// Several TUIs can attach to one daemon. Each PTY has one size, so exactly one
// client may set it: the size master. The master is the OLDEST attached client
// whose raw window is paintable. A master whose link is LOST keeps its slot for
// a grace time, but only while another client that saw it leave is still
// attached, because the grace protects those followers from a resize. A clean
// exit (MsgDetach) skips the grace. A daemon restart keeps the previous
// master's slot for a short reserve, so the reattach ladder resizes nothing.

const (
	// daemonMinClientCols and daemonMinClientRows are the TUI's minTermWidth
	// and minTermHeight (internal/tui/model.go), the threshold of its
	// terminalPaintable gate. The two pairs MUST stay in step: the TUI reports
	// 0x0 below its own floor, and a daemon floor lower than the TUI's would
	// elect a window the TUI itself refuses to size panes from. The daemon
	// cannot import internal/tui.
	daemonMinClientCols = 40
	daemonMinClientRows = 10

	// restartReserveCap bounds the slot a restored size_master is kept for.
	// TUIs reattach within seconds of a daemon restart, so a longer reserve
	// only delays the election when the previous master is not coming back.
	restartReserveCap = 30 * time.Second

	// maxClientIDLen bounds a client's self-reported id. A UUID is 36 bytes;
	// the id is used only as a map key and a display value.
	maxClientIDLen = 64

	// maxClientDim bounds a client's self-reported window size, in cells, on
	// each axis. The size feeds a new pane's PTY, so a value above it is taken
	// as the ceiling rather than trusted verbatim.
	maxClientDim = 1000
)

// clientRecord is one attached client. The registry is keyed by conn, and a
// conn that never sent MsgAttach (an MCP bridge) has no record.
type clientRecord struct {
	id         string
	conn       *ipc.Conn
	attachedAt time.Time // first attach of this id; kept across a graced reconnect
	cols, rows int       // RAW, never defaulted
	cwd        string
	// lastInputAt is stamped by every user-originated message on this conn.
	// Zero means the client never sent one.
	lastInputAt time.Time
	// overlays is the set of overlay panes this client has ON SCREEN — see
	// setOverlayClaim for why visibility is per client.
	overlays map[string]bool
}

// reservation keeps a lost master's slot until `until`.
type reservation struct {
	id    string
	until time.Time
	// attachedAt is the lost master's first attach, handed back to it when it
	// returns, so it stays the oldest client. Zero for a restart reserve.
	attachedAt time.Time
	// protects holds the ids attached at the loss. The slot is kept only while
	// one of them is still attached. nil for a restart reserve, which has no
	// such condition.
	protects map[string]bool
}

// clientChange is what one attach, detach or lost link changed. The state
// carries both the master id and the attached-client count, so either change
// is news to the other clients.
type clientChange struct {
	master bool // masterID changed
	count  bool // the number of attached clients changed
}

func (c clientChange) any() bool { return c.master || c.count }

// clientRegistry is the set of attached clients plus the master bookkeeping.
//
// Its mutex is a LEAF: never sm.mu, never a pane's PluginMu, and never held
// while broadcasting or sending. It is written from every conn's dispatch
// goroutine, the disconnect callback and the grace timer.
type clientRegistry struct {
	mu       sync.Mutex
	byConn   map[*ipc.Conn]*clientRecord
	masterID string
	reserved *reservation

	// now and afterFn are seams over time.Now and time.AfterFunc. New sets
	// the real ones; a zero registry falls back to them too.
	now       func() time.Time
	afterFn   func(time.Duration, func()) (stop func() bool)
	timerStop func() bool
	grace     time.Duration

	// onChange runs, outside mu, when a timer expiry changes the master. The
	// other elections run on a dispatch goroutine, which broadcasts itself.
	onChange func()
}

func realAfterFunc(d time.Duration, f func()) func() bool {
	return time.AfterFunc(d, f).Stop
}

func (r *clientRegistry) clock() time.Time {
	if r.now == nil {
		return time.Now()
	}
	return r.now()
}

// eligible reports whether a client's RAW window is paintable. The raw value
// matters: handleAttach defaults a 0x0 attach to 80x24 for clientSize, and a
// console-less client electing itself on that default is the 1x1 incident.
func eligible(rec *clientRecord) bool {
	return rec.cols >= daemonMinClientCols && rec.rows >= daemonMinClientRows
}

func (r *clientRegistry) recordByID(id string) *clientRecord {
	if id == "" {
		return nil
	}
	for _, rec := range r.byConn {
		if rec.id == id {
			return rec
		}
	}
	return nil
}

func (r *clientRegistry) reserveStillProtects(res *reservation) bool {
	if res.protects == nil {
		return true
	}
	for _, rec := range r.byConn {
		if res.protects[rec.id] {
			return true
		}
	}
	return false
}

// oldestEligibleLocked returns the eligible client with the smallest
// attachedAt. A tie goes to the smaller id, so the result is deterministic.
func (r *clientRegistry) oldestEligibleLocked() *clientRecord {
	var best *clientRecord
	for _, rec := range r.byConn {
		if !eligible(rec) {
			continue
		}
		if best == nil || rec.attachedAt.Before(best.attachedAt) ||
			(rec.attachedAt.Equal(best.attachedAt) && rec.id < best.id) {
			best = rec
		}
	}
	return best
}

// electLocked runs the election. Called with r.mu held. It returns whether
// masterID changed; the caller broadcasts once, after releasing r.mu.
func (r *clientRegistry) electLocked() bool {
	now := r.clock()
	if rec := r.recordByID(r.masterID); rec != nil && eligible(rec) {
		return false // rule 1: a connected, eligible master keeps the slot
	}
	if res := r.reserved; res != nil && now.Before(res.until) && r.reserveStillProtects(res) {
		if r.masterID != res.id {
			r.masterID = res.id
			return true
		}
		return false // rule 2: the reserved slot is kept
	}
	r.clearReservationLocked()
	best := r.oldestEligibleLocked() // rule 3
	id := ""                         // rule 4: nobody eligible, no master
	if best != nil {
		id = best.id
	}
	changed := id != r.masterID
	r.masterID = id
	return changed
}

func (r *clientRegistry) clearReservationLocked() {
	r.reserved = nil
	if r.timerStop != nil {
		r.timerStop()
		r.timerStop = nil
	}
}

// reserveLocked replaces any reservation with res and arms the timer that
// re-runs the election when it lapses.
func (r *clientRegistry) reserveLocked(res *reservation, d time.Duration) {
	r.clearReservationLocked()
	r.reserved = res
	after := r.afterFn
	if after == nil {
		after = realAfterFunc
	}
	r.timerStop = after(d, r.expire)
}

// stopTimer disarms the grace or reserve timer at daemon shutdown, so it never
// fires into a stopped daemon. The reservation itself is kept: the final
// snapshot still writes the reserved id as size_master.
func (r *clientRegistry) stopTimer() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.timerStop != nil {
		r.timerStop()
		r.timerStop = nil
	}
}

// expire is the timer callback. A stale fire (the timer was replaced or the
// master returned) is harmless: the election is idempotent.
func (r *clientRegistry) expire() {
	r.mu.Lock()
	changed := r.electLocked()
	onChange := r.onChange
	r.mu.Unlock()
	if changed && onChange != nil {
		onChange()
	}
}

// attach records conn as the client id at a RAW geometry and elects. An empty
// id is minted as "anon-<uuid>", scoped to the conn: a re-attach on the same
// conn keeps it.
//
// reattach is the payload's Reattach flag: false on a process's first attach
// to this daemon, which a restart reserve yields to when it is alone.
func (r *clientRegistry) attach(conn *ipc.Conn, id string, cols, rows int, cwd string, reattach bool) clientChange {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byConn == nil {
		r.byConn = make(map[*ipc.Conn]*clientRecord)
	}
	before := len(r.byConn)
	rec, existed := r.byConn[conn]
	if id == "" {
		if existed {
			id = rec.id
		} else {
			id = "anon-" + uuid.NewString()
		}
	}
	if !existed {
		rec = &clientRecord{conn: conn, overlays: map[string]bool{}}
		r.byConn[conn] = rec
	}
	if !existed || rec.id != id {
		rec.attachedAt = r.clock()
		// The id belongs to one process, which holds one conn per daemon. A
		// second conn with the same id means the first is a dead link the
		// server has not reaped yet: this record replaces it, and keeps its age.
		for c, other := range r.byConn {
			if c != conn && other.id == id {
				rec.attachedAt = other.attachedAt
				delete(r.byConn, c)
			}
		}
		if res := r.reserved; res != nil && res.id == id && !res.attachedAt.IsZero() {
			rec.attachedAt = res.attachedAt
		}
	}
	rec.id = id
	rec.cols, rec.rows = cols, rows
	rec.cwd = cwd
	if res := r.reserved; res != nil && res.id == id {
		// The reserved client is back, so the slot has nothing left to wait
		// for. The election below keeps it when the client is eligible.
		r.clearReservationLocked()
	} else if res != nil && res.protects == nil && !reattach && len(r.byConn) == 1 {
		// A restart reserve waits for TUIs RECONNECTING after the restart. A
		// new process attaching alone is a cold start after an unclean stop
		// (reboot, kill): the previous master went with its process, and
		// waiting would make the only client a follower for the reserve.
		r.clearReservationLocked()
	}
	return clientChange{master: r.electLocked(), count: len(r.byConn) != before}
}

// lose drops conn after a LOST link: the conn closed with no MsgDetach. A
// master that leaves this way keeps its slot for the grace time, but only
// while another client is attached. With nobody to protect, the grace would
// only make a relaunched TUI (which has a new id) wait.
func (r *clientRegistry) lose(conn *ipc.Conn) clientChange {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.byConn[conn]
	if !ok {
		return clientChange{}
	}
	delete(r.byConn, conn)
	if rec.id == r.masterID && r.grace > 0 && r.recordByID(rec.id) == nil {
		protects := make(map[string]bool, len(r.byConn))
		for _, other := range r.byConn {
			protects[other.id] = true
		}
		if len(protects) > 0 {
			r.reserveLocked(&reservation{
				id:         rec.id,
				until:      r.clock().Add(r.grace),
				attachedAt: rec.attachedAt,
				protects:   protects,
			}, r.grace)
		}
	}
	return clientChange{master: r.electLocked(), count: true}
}

// detach drops conn after a clean exit and elects with NO reservation. The
// disconnect that follows finds no record and does nothing more.
func (r *clientRegistry) detach(conn *ipc.Conn) clientChange {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byConn[conn]; !ok {
		return clientChange{}
	}
	delete(r.byConn, conn)
	return clientChange{master: r.electLocked(), count: true}
}

// setGeometry records a client's new RAW window size and elects: a master
// shrunk below the paintable floor loses the slot at once.
func (r *clientRegistry) setGeometry(conn *ipc.Conn, cols, rows int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.byConn[conn]
	if !ok {
		return false
	}
	rec.cols, rec.rows = cols, rows
	return r.electLocked()
}

// takeControl makes conn's client the master when it is attached and
// eligible. accepted is false when the request was ignored.
func (r *clientRegistry) takeControl(conn *ipc.Conn) (changed, accepted bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.byConn[conn]
	if !ok || !eligible(rec) {
		return false, false
	}
	// An explicit request overrides a slot kept for someone else.
	r.clearReservationLocked()
	changed = r.masterID != rec.id
	r.masterID = rec.id
	return changed, true
}

// reserveAfterRestart keeps a restored size_master's slot for
// min(grace, restartReserveCap), with no follower condition: after a restart
// nobody is attached yet, and the TUIs reattach with their same ids. A new
// process's FIRST attach, alone on the daemon, ends it early (see attach).
func (r *clientRegistry) reserveAfterRestart(id string) {
	id = truncateField(id, maxClientIDLen)
	d := min(r.grace, restartReserveCap)
	if id == "" || d <= 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reserveLocked(&reservation{id: id, until: r.clock().Add(d)}, d)
	r.electLocked()
}

// registerClient records conn as an attached client, from its attach payload.
//
// ATTACHMENT, not connection, is what "a client is here" means, and the
// difference is not academic: every live MCP bridge holds an IPC conn for its
// whole lifetime (cmd/quil/mcp.go dials once and closes on exit), and a bridge
// is a child of the claude process in a PANE — so bridges routinely outlive the
// TUI. Counting raw conns therefore answered "is anything connected", which in
// any session with a claude pane wired to `quil mcp` is permanently yes (21
// conns in the session that reported 7 live overlays), and the detached-session
// overlay stamp never fired in exactly the configuration it was designed for.
// Re-attaching on the same conn keeps that client's existing overlay claims.
//
// The geometry is the RAW one from the payload, taken before handleAttach
// defaults it. It returns whether the master changed.
func (d *Daemon) registerClient(conn *ipc.Conn, attach ipc.AttachPayload) bool {
	return d.attachClient(conn, attach).master
}

// attachClient is registerClient reporting the count change too, for
// handleAttach.
func (d *Daemon) attachClient(conn *ipc.Conn, attach ipc.AttachPayload) clientChange {
	if conn == nil {
		return clientChange{}
	}
	id := truncateField(attach.ClientID, maxClientIDLen)
	return d.clients.attach(conn, id, clampClientDim(attach.Cols), clampClientDim(attach.Rows), attach.CWD, attach.Reattach)
}

// clampClientDim bounds one axis of a self-reported window size to
// [0, maxClientDim]. It never defaults: 0 stays 0, which eligibility reads as
// "not paintable".
func clampClientDim(v int) int {
	return max(0, min(v, maxClientDim))
}

// forgetAttachedClient drops a conn whose link was lost, and with it every
// overlay that client claimed visible. A conn that never attached, or that
// already sent MsgDetach, is not in the set, so dropping it changes nothing.
// It returns whether the master changed.
func (d *Daemon) forgetAttachedClient(conn *ipc.Conn) bool {
	return d.clients.lose(conn).master
}

// detachClient handles a clean client exit. It returns whether the master
// changed.
func (d *Daemon) detachClient(conn *ipc.Conn) bool {
	return d.clients.detach(conn).master
}

// setClientGeometry records a client's RAW window size. It returns whether the
// master changed.
func (d *Daemon) setClientGeometry(conn *ipc.Conn, cols, rows int) bool {
	return d.clients.setGeometry(conn, cols, rows)
}

// takeControl makes the sender the master if it is attached and eligible. It
// returns whether the master changed.
func (d *Daemon) takeControl(conn *ipc.Conn) bool {
	changed, accepted := d.clients.takeControl(conn)
	if !accepted {
		logger.Debug("take_control: ignored (sender not attached or not paintable)")
	}
	return changed
}

// shuttingDown reports whether Stop or MsgShutdown has begun.
func (d *Daemon) shuttingDown() bool {
	select {
	case <-d.shutdown:
		return true
	default:
		return false
	}
}

// sendStateToOtherClients sends the workspace state to every attached client
// except the one given, after an attach, detach or lost link changed the
// master or the attached-client count. Each TUI shows [master]/[follower] only
// while that count is 2 or more, so a count left stale is a wrong status bar.
//
// Not a broadcast. A broadcast also reached the attaching conn, which then got
// two state frames back to back: its own attach state and this one, which says
// nothing new. That is pressure on its must-deliver queue for no information.
// A conn that never attached (an MCP bridge) has no use for either value.
func (d *Daemon) sendStateToOtherClients(except *ipc.Conn) {
	d.clients.mu.Lock()
	var conns []*ipc.Conn
	for _, rec := range d.clients.sortedRecordsLocked() {
		if rec.conn != except {
			conns = append(conns, rec.conn)
		}
	}
	d.clients.mu.Unlock()
	if len(conns) == 0 {
		return
	}
	msg, err := ipc.NewMessage(ipc.MsgWorkspaceState, d.buildWorkspaceState())
	if err != nil {
		logger.Error("attach: build state for other clients: %v", err)
		return
	}
	for _, c := range conns {
		c.Send(msg)
	}
}

// handleDetach removes a cleanly exiting client. The detach always changes
// the attached count, so the other clients always get one state frame.
func (d *Daemon) handleDetach(conn *ipc.Conn) {
	if d.clients.detach(conn).any() {
		d.sendStateToOtherClients(conn)
	}
}

func (d *Daemon) handleClientGeometry(conn *ipc.Conn, msg *ipc.Message) {
	var p ipc.ClientGeometryPayload
	if err := msg.DecodePayload(&p); err != nil {
		return
	}
	if d.setClientGeometry(conn, clampClientDim(p.Cols), clampClientDim(p.Rows)) {
		d.broadcastState()
	}
}

func (d *Daemon) handleTakeControl(conn *ipc.Conn) {
	if d.takeControl(conn) {
		d.broadcastState()
	}
}

// masterConn returns the master's conn, or nil when there is no connected
// master (none elected, or its slot is reserved while its link is lost).
func (d *Daemon) masterConn() *ipc.Conn {
	d.clients.mu.Lock()
	defer d.clients.mu.Unlock()
	if rec := d.clients.recordByID(d.clients.masterID); rec != nil {
		return rec.conn
	}
	return nil
}

func (d *Daemon) isMasterConn(c *ipc.Conn) bool {
	if c == nil {
		return false
	}
	d.clients.mu.Lock()
	defer d.clients.mu.Unlock()
	rec, ok := d.clients.byConn[c]
	return ok && d.clients.masterID != "" && rec.id == d.clients.masterID
}

// sizeAuthorityOpen reports the legacy state in which any attached client may
// resize: no master elected and no slot reserved.
func (d *Daemon) sizeAuthorityOpen() bool {
	d.clients.mu.Lock()
	defer d.clients.mu.Unlock()
	return d.clients.masterID == "" && d.clients.reserved == nil
}

func (d *Daemon) masterID() string {
	d.clients.mu.Lock()
	defer d.clients.mu.Unlock()
	return d.clients.masterID
}

func (d *Daemon) clientCount() int {
	d.clients.mu.Lock()
	defer d.clients.mu.Unlock()
	return len(d.clients.byConn)
}

// sortedRecordsLocked returns the records oldest first, ties by id.
func (r *clientRegistry) sortedRecordsLocked() []*clientRecord {
	recs := make([]*clientRecord, 0, len(r.byConn))
	for _, rec := range r.byConn {
		recs = append(recs, rec)
	}
	sort.Slice(recs, func(i, j int) bool {
		if !recs[i].attachedAt.Equal(recs[j].attachedAt) {
			return recs[i].attachedAt.Before(recs[j].attachedAt)
		}
		return recs[i].id < recs[j].id
	})
	return recs
}

// followerConns returns the attached conns that are not the master, oldest
// first, leaving out except.
func (d *Daemon) followerConns(except *ipc.Conn) []*ipc.Conn {
	d.clients.mu.Lock()
	defer d.clients.mu.Unlock()
	var out []*ipc.Conn
	for _, rec := range d.clients.sortedRecordsLocked() {
		if rec.conn == except || (d.clients.masterID != "" && rec.id == d.clients.masterID) {
			continue
		}
		out = append(out, rec.conn)
	}
	return out
}

// mostRecentlyActiveConn returns the attached client with the latest input.
// When nobody has typed yet, it is the most recently attached client. nil when
// no client is attached.
func (d *Daemon) mostRecentlyActiveConn() *ipc.Conn {
	d.clients.mu.Lock()
	defer d.clients.mu.Unlock()
	var best *clientRecord
	for _, rec := range d.clients.sortedRecordsLocked() {
		if best == nil || !rec.lastInputAt.Before(best.lastInputAt) {
			best = rec
		}
	}
	if best == nil {
		return nil
	}
	return best.conn
}

// clientByConn returns a copy of conn's record.
func (d *Daemon) clientByConn(c *ipc.Conn) (clientRecord, bool) {
	d.clients.mu.Lock()
	defer d.clients.mu.Unlock()
	rec, ok := d.clients.byConn[c]
	if !ok {
		return clientRecord{}, false
	}
	cp := *rec
	cp.overlays = make(map[string]bool, len(rec.overlays))
	for k, v := range rec.overlays {
		cp.overlays[k] = v
	}
	return cp, true
}

// touchClientInput stamps a user-originated message on conn. A conn that is
// not an attached client (an MCP bridge) is not stamped.
func (d *Daemon) touchClientInput(c *ipc.Conn) {
	d.clients.mu.Lock()
	defer d.clients.mu.Unlock()
	if rec, ok := d.clients.byConn[c]; ok {
		rec.lastInputAt = d.clients.clock()
	}
}

// listClients describes every attached client, oldest first. The hello
// registry supplies role, pid and exe; it is read after clients.mu is
// released, because each registry keeps its own leaf lock.
func (d *Daemon) listClients() []ipc.ClientInfo {
	type row struct {
		conn *ipc.Conn
		info ipc.ClientInfo
	}
	d.clients.mu.Lock()
	recs := d.clients.sortedRecordsLocked()
	rows := make([]row, 0, len(recs))
	for _, rec := range recs {
		info := ipc.ClientInfo{
			Client:     rec.id,
			AttachedAt: rec.attachedAt.UTC().Format(time.RFC3339),
			Cols:       rec.cols,
			Rows:       rec.rows,
			Master:     d.clients.masterID != "" && rec.id == d.clients.masterID,
		}
		if !rec.lastInputAt.IsZero() {
			info.LastInputAt = rec.lastInputAt.UTC().Format(time.RFC3339)
		}
		rows = append(rows, row{conn: rec.conn, info: info})
	}
	d.clients.mu.Unlock()

	out := make([]ipc.ClientInfo, 0, len(rows))
	for _, r := range rows {
		if d.hellos != nil {
			if h, ok := d.hellos.helloOf(r.conn); ok {
				r.info.Role = h.Role
				r.info.PID = h.PID
				r.info.Exe = h.ExeName
			}
		}
		out = append(out, r.info)
	}
	return out
}
