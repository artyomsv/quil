package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// The MCP bridge used to hold ONE daemon connection: the local socket. The
// TUI, meanwhile, drives every `[[destinations]]` host through its Router,
// and a project on such a host was invisible to every MCP tool.
//
// mcpRouter is the bridge's equivalent: one mcpBridge per configured host,
// dialled in the background at startup with the same ssh transport, version
// gate and hello the TUI's background dials use, and re-dialled lazily with
// a backoff when a tool lists hosts or needs a host that is down. Tools address a host
// explicitly (`host`), or through the id→host cache every list and create
// fills, or fall back to the local daemon.

// hostRedialBackoff is how long a failed dial holds before a tool call may
// try that host again. A tool that names a dead host every second must not
// spawn an ssh per second.
const hostRedialBackoff = 30 * time.Second

// hostDialFn dials one destination and returns a connected, hello'd client.
// A seam so the router's routing logic tests without ssh.
type hostDialFn func(cfg config.Config, d config.Destination) (*ipc.Client, error)

// dialMCPHost is the production dial: batch ssh (no prompts — the bridge has
// no terminal), the version gate, and the bridge-role hello.
func dialMCPHost(cfg config.Config, d config.Destination) (*ipc.Client, error) {
	ctx, cancel := context.WithTimeout(context.Background(), extraDialTimeout)
	defer cancel()
	sink := &sshStderrLogger{dest: d.Dest}
	client, link, err := dialRemoteTransportFn(ctx, cfg, d.Dest, true, sink)
	if err != nil {
		return nil, err
	}
	if client == nil {
		return nil, errors.New("ssh dial returned no connection")
	}
	if gateErr := gateExtraVersion(d, client, link); gateErr != nil {
		client.Close()
		return nil, classifyDialFailure(link, gateErr)
	}
	sendClientHello(client, helloRoleBridge)
	return client, nil
}

type hostConn struct {
	dest    config.Destination
	mu      sync.Mutex
	bridge  *mcpBridge
	client  *ipc.Client
	cancel  context.CancelFunc
	err     error
	lastTry time.Time
	// dialing marks a dial in flight, which runs with mu RELEASED; dialDone is
	// closed when it finishes. Together they keep the dial single-flight
	// without holding the mutex every status read takes.
	dialing  bool
	dialDone chan struct{}
	// retryScheduled reserves a background retry before its goroutine starts.
	// Repeated status reads must not queue waiters behind the same slow dial.
	retryScheduled bool
	// reqErr is the last error a request to this CONNECTED host produced
	// during an unscoped aggregation, which skips the host rather than
	// failing the whole list. list_hosts shows it so the skip is visible.
	// Cleared by the next request that succeeds.
	reqErr error
}

// hostStatus is the list_hosts view.
type hostStatus struct {
	Host      string `json:"host"`
	Label     string `json:"label,omitempty"`
	Connected bool   `json:"connected"`
	// DaemonVersion is what the host's daemon reported at dial; empty when
	// it did not answer. Tools that need mcpDaemonMinVersion refuse an older
	// release by name instead of timing out.
	DaemonVersion string `json:"daemon_version,omitempty"`
	Error         string `json:"error,omitempty"`
}

type mcpRouter struct {
	local   *mcpBridge
	cfg     config.Config
	dial    hostDialFn
	backoff time.Duration

	mu     sync.Mutex
	hosts  map[string]*hostConn
	order  []string
	idHost map[string]string

	// selfPane is the pane this bridge runs inside — QUIL_PANE_ID, which the
	// daemon sets on every AI pane's child and the bridge inherits as that
	// child's child. Empty for a bridge spawned outside any pane.
	selfPane string
}

func newMCPRouter(local *mcpBridge, cfg config.Config, dial hostDialFn) *mcpRouter {
	r := &mcpRouter{
		local:    local,
		cfg:      cfg,
		dial:     dial,
		backoff:  hostRedialBackoff,
		hosts:    make(map[string]*hostConn),
		idHost:   make(map[string]string),
		selfPane: os.Getenv("QUIL_PANE_ID"),
	}
	seen := map[string]bool{}
	for _, d := range cfg.Destinations {
		if d.Dest == "" || seen[d.Dest] {
			continue
		}
		seen[d.Dest] = true
		r.hosts[d.Dest] = &hostConn{dest: d}
		r.order = append(r.order, d.Dest)
	}
	return r
}

// connectAll dials every configured host in the background. Best effort:
// the bridge serves the local daemon whether or not any remote answers.
func (r *mcpRouter) connectAll() {
	for _, dest := range r.order {
		h := r.hosts[dest]
		go func() {
			if err := r.connect(h, true); err != nil {
				log.Printf("mcp: host %s: %v", h.dest.Label(), err)
			}
		}()
	}
}

// connect ensures h has a live bridge, dialling if needed. `initial` skips
// the backoff so the startup sweep always tries once.
//
// The DIAL runs with h.mu released, and that is the whole shape of this
// function. An ssh dial is budgeted at extraDialTimeout (60 s), and both
// connected() and statuses() take the same mutex — so holding it across the
// dial made the background connectAll() park every unqualified list_panes,
// list_projects and list_hosts behind the slowest destination configured.
// `dialing` keeps it single-flight; a caller that arrives mid-dial waits on
// dialDone rather than starting a second ssh, exactly as it used to wait on
// the mutex, while the status readers skip it and report "connecting".
func (r *mcpRouter) connect(h *hostConn, initial bool) error {
	h.mu.Lock()
	if h.bridge != nil && !h.bridge.dead.Load() {
		h.mu.Unlock()
		return nil
	}
	if h.dialing {
		wait := h.dialDone
		h.mu.Unlock()
		<-wait
		return r.dialOutcome(h)
	}
	// The read loop ended: the link dropped. Release the old client — off the
	// lock, below, with everything else that can block.
	var oldClient *ipc.Client
	var oldCancel context.CancelFunc
	if h.bridge != nil {
		oldClient, oldCancel = h.client, h.cancel
		h.bridge, h.client, h.cancel = nil, nil, nil
		h.err = errors.New("connection lost")
		// A LOSS earns one immediate retry: the backoff exists for a host
		// that refused the last dial, and this host accepted it.
		h.lastTry = time.Time{}
	}
	if !initial && h.err != nil && time.Since(h.lastTry) < r.backoff {
		err, retryIn := h.err, (r.backoff - time.Since(h.lastTry)).Round(time.Second)
		h.mu.Unlock()
		releaseHostConn(oldClient, oldCancel)
		return fmt.Errorf("host %s unreachable: %v (retry in %s)", h.dest.Label(), err, retryIn)
	}
	h.lastTry = time.Now()
	h.dialing = true
	h.dialDone = make(chan struct{})
	done := h.dialDone
	h.mu.Unlock()

	releaseHostConn(oldClient, oldCancel)
	client, err := r.dial(r.cfg, h.dest)
	var bridge *mcpBridge
	var cancel context.CancelFunc
	if err == nil {
		bridge, cancel = startHostBridge(client, h.dest.Label())
	}

	h.mu.Lock()
	h.dialing = false
	close(done)
	if err != nil {
		h.err = err
		h.mu.Unlock()
		return fmt.Errorf("host %s unreachable: %w", h.dest.Label(), err)
	}
	h.bridge, h.client, h.cancel, h.err = bridge, client, cancel, nil
	h.reqErr = nil
	h.mu.Unlock()
	return nil
}

// dialOutcome reports the result of a dial another goroutine just finished.
func (r *mcpRouter) dialOutcome(h *hostConn) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.bridge != nil && !h.bridge.dead.Load() {
		return nil
	}
	err := h.err
	if err == nil {
		err = errors.New("dial produced no connection")
	}
	return fmt.Errorf("host %s unreachable: %w", h.dest.Label(), err)
}

// startHostBridge wraps a fresh client in a bridge and starts its read loop,
// handing the caller the cancel that stops it. The version probe runs first,
// while nothing else reads the connection.
func startHostBridge(client *ipc.Client, label string) (*mcpBridge, context.CancelFunc) {
	bridge := newMCPBridge(client)
	bridge.daemonVersion, bridge.daemonRequests = probeDaemonVersion(client, daemonVersionProbeTimeout)
	if err := bridge.declinePaneOutput(); err != nil {
		log.Printf("mcp: host %s: decline pane output: %v", label, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go bridge.readLoop(ctx)
	return bridge, cancel
}

func releaseHostConn(client *ipc.Client, cancel context.CancelFunc) {
	if cancel != nil {
		cancel()
	}
	if client != nil {
		client.Close()
	}
}

// bridgeFor resolves which daemon a tool call is aimed at.
//
//  1. An explicit host wins. "local" and "" both mean the local daemon.
//  2. Else any id the call carries that the cache has filed under a host.
//  3. Else local.
//
// The cache is filled by every list and create, so an agent that discovered
// a pane through list_panes can act on it without repeating the host — and
// an id it never saw is assumed local, which is where every pre-existing
// caller's ids live.
func (r *mcpRouter) bridgeFor(host string, ids ...string) (*mcpBridge, string, error) {
	if host == "" {
		r.mu.Lock()
		for _, id := range ids {
			if id == "" {
				continue
			}
			if h, ok := r.idHost[id]; ok {
				host = h
				break
			}
		}
		r.mu.Unlock()
	}
	if host == "" || host == "local" {
		return r.local, "", nil
	}
	r.mu.Lock()
	h, ok := r.hosts[host]
	r.mu.Unlock()
	if !ok {
		return nil, "", fmt.Errorf("unknown host %q: not in [[destinations]] (see list_hosts)", host)
	}
	if err := r.connect(h, false); err != nil {
		return nil, "", err
	}
	h.mu.Lock()
	b := h.bridge
	h.mu.Unlock()
	return b, host, nil
}

// remember files ids under a host so later calls route without naming it.
// Local ids are filed too, so a stale remote entry for a reused id is
// overwritten rather than kept.
func (r *mcpRouter) remember(host string, ids ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range ids {
		if id == "" {
			continue
		}
		if host == "" {
			delete(r.idHost, id)
		} else {
			r.idHost[id] = host
		}
	}
}

// connected returns the local bridge first, then every remote host that is
// currently connected, in config order. Hosts that are down are skipped —
// an aggregate list must not block on a dead ssh.
func (r *mcpRouter) connected() []hostBridge {
	out := []hostBridge{{host: "", bridge: r.local}}
	for _, dest := range r.order {
		h := r.hosts[dest]
		h.mu.Lock()
		if h.bridge != nil && !h.bridge.dead.Load() {
			out = append(out, hostBridge{host: dest, bridge: h.bridge})
		} else {
			r.retryDisconnectedLocked(h)
		}
		h.mu.Unlock()
	}
	return out
}

// retryDisconnectedLocked schedules one retry without waiting for network I/O.
// The caller holds h.mu. A dropped live bridge earns the same immediate retry
// as a named call; a failed dial must wait out the backoff.
func (r *mcpRouter) retryDisconnectedLocked(h *hostConn) {
	if h.dialing || h.retryScheduled {
		return
	}
	if h.bridge == nil && h.err != nil && time.Since(h.lastTry) < r.backoff {
		return
	}
	h.retryScheduled = true
	go func() {
		if err := r.connect(h, false); err != nil {
			log.Printf("mcp: host %s: %v", h.dest.Label(), err)
		}
		h.mu.Lock()
		h.retryScheduled = false
		h.mu.Unlock()
	}()
}

type hostBridge struct {
	host   string
	bridge *mcpBridge
}

// targets is what an aggregating tool iterates: every connected host when no
// host is named, else that one host (dialling it if needed).
//
// A NAMED host that is unknown or unreachable is an ERROR, never an empty
// list. Swallowing it turned list_panes, list_tabs, list_projects, list_tasks
// and get_notifications into successful empty arrays for a misspelled or dead
// host — an unavailable workspace reported as an empty one, which is the
// answer an agent acts on. Unscoped aggregation still skips hosts that are
// down: there the caller asked for whatever is reachable.
func (r *mcpRouter) targets(host string) ([]hostBridge, error) {
	if host == "" {
		return r.connected(), nil
	}
	b, h, err := r.bridgeFor(host)
	if err != nil {
		return nil, err
	}
	return []hostBridge{{host: h, bridge: b}}, nil
}

// forEachHost runs fn against every target of an aggregating tool.
//
// A NAMED host's failure is the tool's failure. An UNSCOPED aggregation
// skips a remote that fails — a connected host whose daemon is too old for
// the request, or one whose link is wedged — records the error for
// list_hosts, and still returns every other host's entries: one host must not
// make the whole workspace look empty or broken. The LOCAL daemon's failure
// propagates either way; there is nothing to fall back to.
func (r *mcpRouter) forEachHost(host string, fn func(hb hostBridge) error) error {
	hosts, err := r.targets(host)
	if err != nil {
		return err
	}
	for _, hb := range hosts {
		err := fn(hb)
		if err == nil {
			r.noteHostError(hb.host, nil)
			continue
		}
		if host == "" && hb.host != "" {
			r.noteHostError(hb.host, err)
			log.Printf("mcp: host %s skipped: %v", hb.host, err)
			continue
		}
		return err
	}
	return nil
}

// noteHostError records (or clears, with nil) the last request failure on a
// connected remote host. A no-op for the local daemon and unknown hosts.
func (r *mcpRouter) noteHostError(host string, err error) {
	if host == "" {
		return
	}
	r.mu.Lock()
	h, ok := r.hosts[host]
	r.mu.Unlock()
	if !ok {
		return
	}
	h.mu.Lock()
	h.reqErr = err
	h.mu.Unlock()
}

// watchTargets picks the hosts a watch spans: the named host; else the hosts
// the watched pane ids were discovered on; else every connected host.
func (r *mcpRouter) watchTargets(host string, paneIDs []string) ([]hostBridge, error) {
	if host != "" {
		return r.targets(host)
	}
	seen := map[string]bool{}
	var out []hostBridge
	r.mu.Lock()
	hosts := make([]string, 0, len(paneIDs))
	for _, id := range paneIDs {
		h, ok := r.idHost[id]
		if !ok {
			h = ""
		}
		if !seen[h] {
			seen[h] = true
			hosts = append(hosts, h)
		}
	}
	r.mu.Unlock()
	if len(paneIDs) == 0 {
		return r.connected(), nil
	}
	for _, h := range hosts {
		b, hh, err := r.bridgeFor(h)
		if err != nil {
			continue
		}
		out = append(out, hostBridge{host: hh, bridge: b})
	}
	return out, nil
}

// statuses is the list_hosts answer: every configured host, connected or not.
func (r *mcpRouter) statuses() []hostStatus {
	out := make([]hostStatus, 0, len(r.order))
	for _, dest := range r.order {
		h := r.hosts[dest]
		h.mu.Lock()
		st := hostStatus{Host: dest, Label: h.dest.Name}
		st.Connected = h.bridge != nil && !h.bridge.dead.Load()
		if !st.Connected {
			r.retryDisconnectedLocked(h)
		}
		switch {
		case st.Connected:
			st.DaemonVersion = h.bridge.daemonVersion
			if h.reqErr != nil {
				st.Error = "last request failed: " + h.reqErr.Error()
			}
		case h.dialing || h.retryScheduled:
			// A dial in flight is neither connected nor failed, and this read
			// must never wait for it to decide which.
			st.Error = "connecting"
		case h.err != nil:
			st.Error = h.err.Error()
		}
		h.mu.Unlock()
		out = append(out, st)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Connected && !out[j].Connected })
	return out
}
