package daemon

import (
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/logger"
	"github.com/artyomsv/quil/internal/memreport"
	"github.com/artyomsv/quil/internal/proctree"
	"github.com/artyomsv/quil/internal/version"
)

// procTickInterval is how often the process collector re-enumerates while it is
// running. Matches the memory collector's cadence.
const procTickInterval = 5 * time.Second

// procGateWindow is how long one tree request keeps the collector alive.
//
// The collector is gated by a DECAYING DEADLINE that clients renew, not by an
// explicit close message. A close message is a message that can be lost: a
// client that crashes, is killed, or drops its connection mid-dialog never
// sends one, and the collector would then run forever with nobody watching. A
// deadline that must be continuously renewed cannot leak, because the failure
// mode of every lost message is that the collector STOPS — the safe direction.
//
// Three times the tick interval, so one skipped tick or one dropped frame does
// not stop it.
const procGateWindow = 15 * time.Second

// procSnapshot is one collection pass.
type procSnapshot struct {
	At    time.Time
	Trees map[string]*proctree.Node // by pane ID
}

// procCollector enumerates process trees for the panes the daemon owns, on
// demand.
//
// Unlike memreport's collector this one is NOT always on: enumerating the whole
// process table every five seconds forever — and, on Darwin, forking ps to do
// it — is real cost for a dialog nobody has open.
type procCollector struct {
	lister memreport.PaneLister
	now    func() time.Time

	mu       sync.Mutex
	sampler  *proctree.Sampler
	src      proctree.Sources
	last     *procSnapshot
	deadline time.Time
	running  bool
}

func newProcCollector(lister memreport.PaneLister, rss func([]int) map[int]uint64) *procCollector {
	return &procCollector{
		lister:  lister,
		now:     time.Now,
		sampler: proctree.NewSampler(),
		src:     proctree.DefaultSources(rss),
	}
}

// Sampled reports whether this platform's CPU figure is a delta over our own
// sample window rather than a kernel-computed average.
func (c *procCollector) Sampled() bool { return c.src.Sampled }

// Renew extends the collector's deadline and starts it if it is not running.
//
// Called for every request that asks for trees, and only those: a status-bar
// poll must not start a process enumeration.
func (c *procCollector) Renew() {
	c.mu.Lock()
	c.deadline = c.now().Add(procGateWindow)
	start := !c.running
	if start {
		c.running = true
	}
	c.mu.Unlock()

	if start {
		// Collect once immediately so the first report is not empty; the CPU
		// column still reads unknown until the second pass, which is what the
		// dialog renders as an em dash.
		c.collect()
		go c.run()
	}
}

// Latest returns the most recent snapshot, or nil if none has completed.
func (c *procCollector) Latest() *procSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last
}

// expired reports whether the gate has lapsed, and clears running when it has.
func (c *procCollector) expired() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.now().Before(c.deadline) {
		return false
	}
	c.running = false
	// Drop the CPU history with the collector. On the next open the first pass
	// has nothing to delta against anyway, and keeping stale samples across a
	// gap of unknown length would produce a percentage computed over an
	// interval nobody measured.
	c.sampler = proctree.NewSampler()
	c.last = nil
	return true
}

func (c *procCollector) run() {
	t := time.NewTicker(procTickInterval)
	defer t.Stop()
	for range t.C {
		if c.expired() {
			return
		}
		c.collect()
	}
}

// collect runs one enumeration pass.
//
// No lock is held across any syscall. PaneSources takes the session's RLock and
// returns; each pane snapshot takes that pane's PluginMu and releases it; the
// enumeration, RSS and CPU reads all happen afterwards on this goroutine. That
// discipline is why a hung ps on a wedged machine costs a stale dialog rather
// than a wedged daemon.
func (c *procCollector) collect() {
	panes := c.lister.PaneSources()

	roots := make([]int, 0, len(panes))
	paneOf := make(map[int]string, len(panes))
	for _, p := range panes {
		s := p.Snapshot()
		if !s.Alive || s.PID <= 0 {
			continue
		}
		roots = append(roots, s.PID)
		paneOf[s.PID] = s.PaneID
	}

	c.mu.Lock()
	sampler, src := c.sampler, c.src
	c.mu.Unlock()

	now := c.now()
	trees, err := sampler.Collect(now, roots, src)
	if err != nil {
		logger.Debug("proctree: enumeration failed: %v", err)
		return
	}

	byPane := make(map[string]*proctree.Node, len(trees))
	for pid, root := range trees {
		if paneID, ok := paneOf[pid]; ok {
			byPane[paneID] = root
		}
	}

	c.mu.Lock()
	c.last = &procSnapshot{At: now, Trees: byPane}
	c.mu.Unlock()
}

// helloRecord is one client's self-description plus when it arrived.
type helloRecord struct {
	payload  ipc.ClientHelloPayload
	received time.Time
}

// helloRegistry tracks which connections have identified themselves.
//
// Its own mutex, never sm.mu — the same rule attachedConns follows, and for the
// same reason: this is read while assembling a report and written from every
// dispatch goroutine, and coupling it to the session lock would put a second
// writer in front of the snapshot loop.
type helloRegistry struct {
	// openedAtOf reads a conn's accept time. A field so tests can age a
	// connection without a real socket; production always uses the real
	// accessor, and internal/ipc has its own test that the accessor is
	// actually populated -- a seam that nothing verifies at the call site is
	// how a passing unit test hides a broken one.
	openedAtOf func(*ipc.Conn) time.Time
	mu         sync.Mutex
	byConn     map[*ipc.Conn]helloRecord
	nowFunc    func() time.Time
}

func newHelloRegistry() *helloRegistry {
	return &helloRegistry{
		byConn:     map[*ipc.Conn]helloRecord{},
		nowFunc:    time.Now,
		openedAtOf: (*ipc.Conn).OpenedAt,
	}
}

func (r *helloRegistry) put(conn *ipc.Conn, p ipc.ClientHelloPayload) {
	if conn == nil {
		return
	}
	r.mu.Lock()
	r.byConn[conn] = helloRecord{payload: p, received: r.nowFunc()}
	r.mu.Unlock()
}

func (r *helloRegistry) forget(conn *ipc.Conn) {
	r.mu.Lock()
	delete(r.byConn, conn)
	r.mu.Unlock()
}

// procUnidentifiedAge is how old a connection must be before its silence counts
// as "this client cannot identify itself".
//
// Without this bound the count lies. Connections without a hello are not only
// old bridges: `quil status`, the version-gate probe, `quild guard` and the
// daemonctl probes all hold a connection for a second or two and, by design,
// never identify themselves. A report taken while one is in flight would claim
// an unidentified client that predates the feature. A probe never survives a
// minute; a stale bridge has been alive for days.
const procUnidentifiedAge = 60 * time.Second

// describe returns quil's own processes and the count of connections that could
// not identify themselves.
//
// daemonVersion is compared against each client's to mark stale binaries — a
// string comparison, with no process query anywhere in it.
func (r *helloRegistry) describe(conns []*ipc.Conn, daemonVersion string, now time.Time) ([]ipc.QuilProcInfo, int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]ipc.QuilProcInfo, 0, len(conns))
	var unidentified int

	for _, conn := range conns {
		rec, ok := r.byConn[conn]
		if !ok {
			if now.Sub(r.openedAtOf(conn)) >= procUnidentifiedAge {
				unidentified++
			}
			continue
		}
		out = append(out, ipc.QuilProcInfo{
			Role:    rec.payload.Role,
			PID:     rec.payload.PID,
			Version: rec.payload.Version,
			ExeName: rec.payload.ExeName,
			// Uptime is extrapolated from the DURATION the client reported at
			// hello, plus the time since. Deriving it from a client-supplied
			// timestamp would be wrong by the clock skew between two machines
			// in remote mode.
			UptimeMS: rec.payload.UptimeMS + now.Sub(rec.received).Milliseconds(),
			Stale:    versionIsStale(rec.payload.Version, daemonVersion),
		})
	}
	return out, unidentified
}

// versionIsStale reports whether a client's version differs from the daemon's.
//
// An empty version on either side is never stale: it means the answer is
// unknown, and marking every unknown as stale would put a warning next to
// perfectly current processes.
func versionIsStale(clientVersion, daemonVersion string) bool {
	if clientVersion == "" || daemonVersion == "" {
		return false
	}
	return clientVersion != daemonVersion
}

// handleClientHello records a durable client's self-description.
//
// Fire and forget: there is no response, because nothing the client does
// depends on the answer. A malformed payload is dropped rather than reported —
// the only consequence is that this process is counted as unidentified, which
// is exactly what it is.
func (d *Daemon) handleClientHello(conn *ipc.Conn, msg *ipc.Message) {
	var p ipc.ClientHelloPayload
	if err := msg.DecodePayload(&p); err != nil {
		logger.Debug("client hello: bad payload: %v", err)
		return
	}
	if p.Role == "" || p.PID <= 0 {
		logger.Debug("client hello: incomplete (role=%q pid=%d)", p.Role, p.PID)
		return
	}
	d.hellos.put(conn, p)
}

// handleResourceReportReq answers with the workspace's resource state.
//
// Trees are included only when asked for, and asking is also what keeps the
// process collector running — see procGateWindow. The status bar polls this
// every 5 s without WithTrees and therefore neither pays for enumeration nor
// puts trees on the wire.
func (d *Daemon) handleResourceReportReq(conn *ipc.Conn, msg *ipc.Message) {
	var req ipc.ResourceReportReqPayload
	if len(msg.Payload) > 0 {
		if err := msg.DecodePayload(&req); err != nil {
			logger.Debug("resource report: bad payload: %v", err)
		}
	}

	resp := ipc.ResourceReportRespPayload{}

	if snap := d.memReport.Latest(); snap != nil {
		resp.SnapshotAt = snap.At.UnixNano()
		resp.Total = snap.Total
		resp.Panes = make([]ipc.PaneResourceInfo, len(snap.Panes))
		for i, p := range snap.Panes {
			resp.Panes[i] = ipc.PaneResourceInfo{
				PaneID:      p.PaneID,
				TabID:       p.TabID,
				GoHeapBytes: p.GoHeapBytes,
				PTYRSSBytes: p.PTYRSSBytes,
				TotalBytes:  p.Total,
			}
		}
	}

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

	if req.WithTrees {
		d.procReport.Renew()
		resp.CPUSampled = d.procReport.Sampled()
		resp.CPUSupported = true

		if ps := d.procReport.Latest(); ps != nil {
			resp.TreesAt = ps.At.UnixNano()
			for i := range resp.Panes {
				if root, ok := ps.Trees[resp.Panes[i].PaneID]; ok {
					resp.Panes[i].Tree = wireNode(root)
				}
			}
		}

		now := time.Now()
		if d.server != nil {
			resp.Quil, resp.Unidentified = d.hellos.describe(
				d.server.ConnsSnapshot(), version.Current(), now)
		}
		// The daemon's own row is not a hello — it reads itself directly.
		resp.Quil = append(resp.Quil, ipc.QuilProcInfo{
			Role:     "daemon",
			PID:      os.Getpid(),
			Version:  version.Current(),
			ExeName:  daemonExeName(),
			UptimeMS: now.Sub(d.startedAt).Milliseconds(),
		})
	}

	respondTo(conn, msg.ID, ipc.MsgResourceReportResp, resp)
}

// wireNode converts a tree to its wire form.
func wireNode(n *proctree.Node) *ipc.ProcNode {
	if n == nil {
		return nil
	}
	out := &ipc.ProcNode{
		PID:      n.PID,
		Name:     n.Name,
		RSSBytes: n.RSSBytes,
		CPUPct:   n.CPUPct,
		Depth:    n.Depth,
	}
	if !n.Start.IsZero() {
		out.StartMS = n.Start.UnixMilli()
	}
	for _, c := range n.Children {
		if w := wireNode(c); w != nil {
			out.Children = append(out.Children, *w)
		}
	}
	return out
}

// killRefusal describes why a kill did not happen. Every one of these is a
// normal outcome, not an error.
const (
	refuseNoPane      = "that pane is no longer running"
	refuseNotFound    = "that process is no longer in the pane's process tree"
	refuseNotChild    = "only processes started inside the pane can be stopped here"
	refuseUnknownTime = "cannot confirm this is still the same process"
	refuseChanged     = "that PID now belongs to a different process"
	refuseUnsupported = "stopping processes is not supported on this platform"
)

// handleKillProcessReq stops a pane descendant, after re-deriving the tree.
//
// The client's request is a PROPOSAL. Nothing in it is trusted: not the depth,
// not the ancestry, not that the PID still means what it meant when the dialog
// drew it. Every check below runs against a table enumerated in this call.
func (d *Daemon) handleKillProcessReq(conn *ipc.Conn, msg *ipc.Message) {
	var req ipc.KillProcessReqPayload
	if err := msg.DecodePayload(&req); err != nil {
		respondTo(conn, msg.ID, ipc.MsgKillProcessResp,
			ipc.KillProcessRespPayload{Refused: "malformed request"})
		return
	}

	target, root, refused := d.resolveKillTarget(req)
	if refused != "" {
		respondTo(conn, msg.ID, ipc.MsgKillProcessResp,
			ipc.KillProcessRespPayload{Refused: refused})
		return
	}
	_ = root

	ops := proctree.DefaultKillOps()

	// The graceful pass is synchronous: it completes in microseconds and its
	// result is what the client is told. The escalation waits out the grace
	// period, so it runs on its own goroutine rather than parking this
	// dispatch goroutine for three seconds — the class of blocking that
	// daemon-lifecycle.md exists to prevent.
	term := proctree.TermPass(target, ops)

	go func() {
		killed := proctree.Escalate(term, proctree.KillGrace, ops)
		if killed > 0 {
			logger.Info("kill: escalated %d process(es) under pane %s", killed, req.PaneID)
		}
	}()

	logger.Info("kill: pane=%s pid=%d signalled=%d skipped=%d",
		req.PaneID, req.PID, term.Signalled, term.Skipped)

	respondTo(conn, msg.ID, ipc.MsgKillProcessResp, ipc.KillProcessRespPayload{
		Signalled: term.Signalled,
	})
}

// resolveKillTarget re-derives the pane's tree NOW and validates the request
// against it, in order. Returns the node to kill, its pane root, and a refusal
// reason when the kill must not proceed.
func (d *Daemon) resolveKillTarget(req ipc.KillProcessReqPayload) (*proctree.Node, *proctree.Node, string) {
	rootPID, ok := d.panePID(req.PaneID)
	if !ok {
		return nil, nil, refuseNoPane
	}

	sampler := proctree.NewSampler()
	trees, err := sampler.Collect(time.Now(), []int{rootPID}, proctree.DefaultSources(nil))
	if err != nil {
		return nil, nil, refuseUnsupported
	}
	root := trees[rootPID]
	if root == nil {
		return nil, nil, refuseNoPane
	}
	target, refused := validateKillTarget(root, req)
	return target, root, refused
}

// killStartTolerance is how far the daemon's freshly-read start time may differ
// from the one the client saw.
//
// One second, because both sides derive the value from the SAME daemon's
// enumeration — the client is echoing back a number this daemon sent it, not
// reading a clock of its own. A false pass therefore needs a PID to be recycled
// within the same second, inside the same pane's subtree.
const killStartTolerance = int64(1000)

// validateKillTarget applies the ordered checks against an ALREADY-DERIVED
// tree, and returns the node to kill or the reason not to.
//
// Pure and separate from enumeration so every branch is testable. The previous
// attempt at this feature had a kill path that no test reached at all —
// disabling its confirm branch left the whole suite green — and the checks
// below are the ones that decide whether the right process dies.
func validateKillTarget(root *proctree.Node, req ipc.KillProcessReqPayload) (*proctree.Node, string) {
	target := proctree.Find(root, req.PID)
	if target == nil {
		return nil, refuseNotFound
	}
	// Depth 1 is the pane's own shell or agent. Restarting that is a different
	// operation which already exists; this dialog does not offer it.
	if target.Depth < 2 {
		return nil, refuseNotChild
	}
	if target.Start.IsZero() || req.StartMS == 0 {
		return nil, refuseUnknownTime
	}
	// The PID-reuse defense. The client's snapshot can be seconds old, and a
	// recycled PID is a different process wearing the same number; the start
	// time is the only thing that tells them apart.
	diff := target.Start.UnixMilli() - req.StartMS
	if diff > killStartTolerance || diff < -killStartTolerance {
		return nil, refuseChanged
	}
	return target, ""
}

// panePID returns the OS PID of a pane's direct child.
func (d *Daemon) panePID(paneID string) (int, bool) {
	for _, p := range d.session.PaneSources() {
		s := p.Snapshot()
		if s.PaneID == paneID {
			if !s.Alive || s.PID <= 0 {
				return 0, false
			}
			return s.PID, true
		}
	}
	return 0, false
}

// daemonExeName is the basename of the running daemon binary.
//
// Same treatment as a client's self-reported ExeName: the basename, so a daemon
// still executing a renamed-aside binary after an in-place update is visible as
// such rather than silently reported as current.
func daemonExeName() string {
	exe, err := os.Executable()
	if err != nil {
		return "quild"
	}
	return filepath.Base(exe)
}
