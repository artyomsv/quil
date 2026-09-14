package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// echoServer is a daemon stand-in that answers list_panes_req with one pane
// whose id names the server, so a test can tell WHICH daemon a request
// reached.
func echoServer(t *testing.T, paneID string) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "echo.sock")
	srv := ipc.NewServer(sock, func(conn *ipc.Conn, m *ipc.Message) {
		if m.Type != ipc.MsgListPanesReq {
			return
		}
		resp, _ := ipc.NewMessage(ipc.MsgListPanesResp, ipc.ListPanesRespPayload{Panes: []ipc.PaneInfo{{ID: paneID}}})
		resp.ID = m.ID
		conn.Send(resp)
	}, nil)
	if err := srv.Start(); err != nil {
		t.Fatalf("server: %v", err)
	}
	t.Cleanup(func() { srv.Stop() })
	return sock
}

func bridgeTo(t *testing.T, sock string) *mcpBridge {
	t.Helper()
	client, err := ipc.NewClient(sock)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	b := newMCPBridge(client)
	// As runMCP does: the probe reads its own reply, so it runs before the
	// read loop owns the connection.
	b.daemonVersion, b.daemonRequests = probeDaemonVersion(client, daemonVersionProbeTimeout)
	go b.readLoop(context.Background())
	return b
}

func firstPaneID(t *testing.T, b *mcpBridge) string {
	t.Helper()
	resp, err := b.request(ipc.MsgListPanesReq, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	var p ipc.ListPanesRespPayload
	if err := resp.DecodePayload(&p); err != nil {
		t.Fatal(err)
	}
	return p.Panes[0].ID
}

// shortenVersionProbe keeps a dial from waiting the production probe timeout
// against a fake that answers no version request.
func shortenVersionProbe(t *testing.T) {
	t.Helper()
	prev := daemonVersionProbeTimeout
	daemonVersionProbeTimeout = 200 * time.Millisecond
	t.Cleanup(func() { daemonVersionProbeTimeout = prev })
}

func testRouter(t *testing.T, dial hostDialFn, dests ...string) *mcpRouter {
	t.Helper()
	shortenVersionProbe(t)
	cfg := config.Default()
	for _, d := range dests {
		cfg.Destinations = append(cfg.Destinations, config.Destination{Dest: d, Name: "box-" + d})
	}
	local := bridgeTo(t, echoServer(t, "pane-local"))
	r := newMCPRouter(local, cfg, dial)
	r.backoff = 200 * time.Millisecond
	return r
}

func TestMCPRouter_ResolutionOrder(t *testing.T) {
	remoteSock := echoServer(t, "pane-remote")
	var dials atomic.Int32
	dial := func(cfg config.Config, d config.Destination) (*ipc.Client, error) {
		dials.Add(1)
		return ipc.NewClient(remoteSock)
	}
	r := testRouter(t, dial, "gpu")

	// 3. Nothing known → local.
	b, host, err := r.bridgeFor("", "pane-x")
	if err != nil || host != "" || firstPaneID(t, b) != "pane-local" {
		t.Fatalf("default: host=%q err=%v", host, err)
	}
	if dials.Load() != 0 {
		t.Fatal("a local resolution dialled a remote")
	}
	// 1. Explicit host wins, and dials lazily.
	b, host, err = r.bridgeFor("gpu", "pane-x")
	if err != nil || host != "gpu" || firstPaneID(t, b) != "pane-remote" {
		t.Fatalf("explicit: host=%q err=%v", host, err)
	}
	if dials.Load() != 1 {
		t.Fatalf("dials = %d, want 1", dials.Load())
	}
	// 2. A remembered id routes without the host.
	r.remember("gpu", "pane-x")
	b, host, err = r.bridgeFor("", "pane-x")
	if err != nil || host != "gpu" || firstPaneID(t, b) != "pane-remote" {
		t.Fatalf("cached: host=%q err=%v", host, err)
	}
	if dials.Load() != 1 {
		t.Fatalf("a connected host was dialled again: %d", dials.Load())
	}
	// "local" is an explicit name for the local daemon even for a cached id.
	if _, host, _ := r.bridgeFor("local", "pane-x"); host != "" {
		t.Fatalf("explicit local resolved to %q", host)
	}
	// Re-filing an id as local forgets the remote entry.
	r.remember("", "pane-x")
	if _, host, _ := r.bridgeFor("", "pane-x"); host != "" {
		t.Fatalf("id re-filed as local still routes to %q", host)
	}
}

func TestMCPRouter_UnknownAndUnreachableHosts(t *testing.T) {
	var dials atomic.Int32
	dial := func(cfg config.Config, d config.Destination) (*ipc.Client, error) {
		dials.Add(1)
		return nil, errors.New("ssh: connect timed out")
	}
	r := testRouter(t, dial, "gpu")

	if _, _, err := r.bridgeFor("nas"); err == nil || !strings.Contains(err.Error(), "unknown host") {
		t.Fatalf("unknown host: %v", err)
	}
	_, _, err := r.bridgeFor("gpu")
	if err == nil || !strings.Contains(err.Error(), "ssh: connect timed out") {
		t.Fatalf("unreachable host error should carry the dial error: %v", err)
	}
	// Inside the backoff: no second ssh.
	if _, _, err := r.bridgeFor("gpu"); err == nil || !strings.Contains(err.Error(), "retry in") {
		t.Fatalf("second call inside backoff: %v", err)
	}
	if dials.Load() != 1 {
		t.Fatalf("dials = %d, want 1 inside the backoff", dials.Load())
	}
	st := r.statuses()
	if len(st) != 1 || st[0].Connected || st[0].Host != "gpu" || st[0].Label != "box-gpu" || !strings.Contains(st[0].Error, "timed out") {
		t.Fatalf("statuses = %+v", st)
	}
	time.Sleep(r.backoff + 20*time.Millisecond)
	r.bridgeFor("gpu")
	if dials.Load() != 2 {
		t.Fatalf("dials = %d, want a retry after the backoff", dials.Load())
	}
	// connected() never includes a host that is down, but always the local.
	if c := r.connected(); len(c) != 1 || c[0].host != "" {
		t.Fatalf("connected = %+v", c)
	}
}

func TestMCPRouter_RedialsADeadBridge(t *testing.T) {
	remoteSock := echoServer(t, "pane-remote")
	var dials atomic.Int32
	dial := func(cfg config.Config, d config.Destination) (*ipc.Client, error) {
		dials.Add(1)
		return ipc.NewClient(remoteSock)
	}
	r := testRouter(t, dial, "gpu")
	b, _, err := r.bridgeFor("gpu")
	if err != nil {
		t.Fatal(err)
	}
	// Kill the link from the client side: the read loop exits and marks the
	// bridge dead.
	r.hosts["gpu"].client.Close()
	if !waitUntil(func() bool { return b.dead.Load() }, time.Second) {
		t.Fatal("bridge never marked dead after its client closed")
	}
	if c := r.connected(); len(c) != 1 {
		t.Fatalf("dead host still listed as connected: %+v", c)
	}
	b2, _, err := r.bridgeFor("gpu")
	if err != nil || b2 == b || firstPaneID(t, b2) != "pane-remote" {
		t.Fatalf("redial: err=%v same=%v", err, b2 == b)
	}
	if dials.Load() != 2 {
		t.Fatalf("dials = %d, want 2", dials.Load())
	}
}

// targets() reports a named host's failure instead of yielding nothing. An
// unscoped aggregate still skips whatever is down.
func TestMCPRouter_TargetsPropagatesANamedHostsError(t *testing.T) {
	dial := func(cfg config.Config, d config.Destination) (*ipc.Client, error) {
		return nil, errors.New("ssh: connect timed out")
	}
	r := testRouter(t, dial, "gpu")

	if _, err := r.targets("nas"); err == nil || !strings.Contains(err.Error(), "unknown host") {
		t.Fatalf("unknown host: %v", err)
	}
	if _, err := r.targets("gpu"); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("unreachable host: %v", err)
	}
	all, err := r.targets("")
	if err != nil || len(all) != 1 || all[0].host != "" {
		t.Fatalf("unscoped aggregate = %+v, err=%v", all, err)
	}
}

// The dial runs with the host mutex released. connectAll() dials every
// configured host in the background, and both connected() and statuses() take
// that mutex — so holding it across a 60 s ssh parked every unqualified
// list_panes / list_projects / list_hosts behind the slowest destination.
func TestMCPRouter_StatusReadsDoNotWaitForADial(t *testing.T) {
	release := make(chan struct{})
	dialing := make(chan struct{})
	var dials atomic.Int32
	remoteSock := echoServer(t, "pane-remote")
	dial := func(cfg config.Config, d config.Destination) (*ipc.Client, error) {
		if dials.Add(1) == 1 {
			close(dialing)
		}
		<-release
		return ipc.NewClient(remoteSock)
	}
	r := testRouter(t, dial, "gpu")

	go r.connectAll()
	<-dialing

	done := make(chan struct{})
	go func() {
		defer close(done)
		if c := r.connected(); len(c) != 1 || c[0].host != "" {
			t.Errorf("connected during a dial = %+v, want the local bridge only", c)
		}
		st := r.statuses()
		if len(st) != 1 || st[0].Connected || st[0].Error != "connecting" {
			t.Errorf("statuses during a dial = %+v", st)
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("connected()/statuses() blocked on a dial in progress")
	}

	// The dial is single-flight: a caller arriving mid-dial waits for the one
	// in flight rather than starting a second ssh.
	second := make(chan error, 1)
	go func() { _, _, err := r.bridgeFor("gpu"); second <- err }()
	close(release)
	if err := <-second; err != nil {
		t.Fatalf("mid-dial caller: %v", err)
	}
	if got := dials.Load(); got != 1 {
		t.Fatalf("dials = %d, want 1", got)
	}
	if c := r.connected(); len(c) != 2 {
		t.Fatalf("connected after the dial = %+v", c)
	}
}

func TestMCPRouter_SelfPaneComesFromEnv(t *testing.T) {
	t.Setenv("QUIL_PANE_ID", "pane-me")
	r := testRouter(t, nil)
	if r.selfPane != "pane-me" {
		t.Fatalf("selfPane = %q", r.selfPane)
	}
	t.Setenv("QUIL_PANE_ID", "")
	if r := testRouter(t, nil); r.selfPane != "" {
		t.Fatalf("selfPane outside a pane = %q", r.selfPane)
	}
}

func TestMCPRouter_StatusReadsRetryAfterBackoff(t *testing.T) {
	for _, read := range []string{"connected", "statuses"} {
		t.Run(read, func(t *testing.T) {
			remoteSock := echoServer(t, "pane-recovered")
			var dials atomic.Int32
			r := testRouter(t, func(config.Config, config.Destination) (*ipc.Client, error) {
				if dials.Add(1) == 1 {
					return nil, errors.New("runs an old version")
				}
				return ipc.NewClient(remoteSock)
			}, "gpu")
			r.backoff = time.Hour
			h := r.hosts["gpu"]
			if err := r.connect(h, true); err == nil {
				t.Fatal("initial dial should fail")
			}
			r.noteHostError("gpu", errors.New("old request failed"))
			readHosts := func() {
				if read == "connected" {
					r.connected()
				} else {
					r.statuses()
				}
			}
			readHosts()
			h.mu.Lock()
			scheduled := h.retryScheduled || h.dialing
			// Advance the failed attempt past the backoff without timing sleeps.
			h.lastTry = time.Now().Add(-2 * r.backoff)
			h.mu.Unlock()
			if scheduled || dials.Load() != 1 {
				t.Fatal("status read retried inside the backoff")
			}
			readHosts()
			if !waitUntil(func() bool {
				h.mu.Lock()
				defer h.mu.Unlock()
				return h.bridge != nil && !h.dialing && !h.retryScheduled
			}, 2*time.Second) {
				t.Fatal("status read did not reconnect the recovered host")
			}
			if dials.Load() != 2 {
				t.Fatalf("dials = %d, want 2", dials.Load())
			}
			if st := r.statuses(); !st[0].Connected || st[0].Error != "" {
				t.Fatalf("recovered status retains an error: %+v", st)
			}
			all := r.connected()
			if len(all) != 2 || firstPaneID(t, all[1].bridge) != "pane-recovered" {
				t.Fatalf("unscoped hosts after recovery: %+v", all)
			}
		})
	}
}

func waitUntil(cond func() bool, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}
