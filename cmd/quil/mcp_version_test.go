package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

func TestRequireDaemon_RefusesOnlyAnOlderRelease(t *testing.T) {
	cases := []struct {
		version string
		refused bool
	}{
		{"", false},        // no answer: unknown, never a reason to refuse
		{"dev", false},     // a developer's own daemon
		{"1.71.0", true},   // the release measured against: drops the new types
		{"1.71.9", true},   //
		{"1.72.0", false},  // existing project/tab/task floor
		{"1.73.0", false},  // newer release with template creation
		{"1.80.3", false},  // newer
		{"garbage", false}, // unparseable is unknown
	}
	for _, tc := range cases {
		b := &mcpBridge{daemonVersion: tc.version}
		err := b.requireDaemon("list_projects")
		if (err != nil) != tc.refused {
			t.Errorf("version %q: refused=%v (%v), want %v", tc.version, err != nil, err, tc.refused)
			continue
		}
		if err != nil && (!strings.Contains(err.Error(), tc.version) || !strings.Contains(err.Error(), mcpDaemonMinVersion)) {
			t.Errorf("version %q: error names neither side: %v", tc.version, err)
		}
	}
}

func TestCreateFromTemplate_OldDaemon_RefusesWithoutDisablingReleasedTools(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	local := newFakeIPCDaemonVersion(t, "pane-local", "1.72.0")
	remote := newFakeIPCDaemonVersion(t, "pane-remote", "1.72.0")
	session, _ := toolHarness(t, local, remote)
	for _, host := range []string{"", "gpu"} {
		_, err := callTool(t, session, "create_from_template", map[string]any{"host": host, "template": "pair"})
		if err == nil || !strings.Contains(err.Error(), "1.73.0") || !strings.Contains(err.Error(), "1.72.0") {
			t.Fatalf("host %q: expected named version refusal, got %v", host, err)
		}
		if _, err := callTool(t, session, "list_projects", map[string]any{"host": host}); err != nil {
			t.Fatalf("released list_projects refused 1.72.0 on %q: %v", host, err)
		}
	}
	if !local.sawNo(ipc.MsgCreateFromTemplateReq) || !remote.sawNo(ipc.MsgCreateFromTemplateReq) {
		t.Fatal("refused template request reached an old daemon")
	}
}

func TestCreateFromTemplate_CurrentDaemon_AllowsRequest(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	local := newFakeIPCDaemonVersion(t, "pane-local", "1.73.0")
	session, _ := toolHarness(t, local, nil)
	if _, err := callTool(t, session, "create_from_template", map[string]any{"template": "pair"}); err != nil {
		t.Fatal(err)
	}
	if local.sawNo(ipc.MsgCreateFromTemplateReq) {
		t.Fatal("allowed request was not sent")
	}
}

func TestProbeDaemonVersion_ReadsTheAnswerOrGivesUp(t *testing.T) {
	answering := newFakeIPCDaemonVersion(t, "pane-a", "1.75.0")
	client, err := ipc.NewClient(answering.sock)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if got := probeDaemonVersion(client, time.Second); got != "1.75.0" {
		t.Fatalf("probe = %q, want 1.75.0", got)
	}
	silent := newFakeIPCDaemonVersion(t, "pane-b", "")
	client2, err := ipc.NewClient(silent.sock)
	if err != nil {
		t.Fatal(err)
	}
	defer client2.Close()
	start := time.Now()
	if got := probeDaemonVersion(client2, 150*time.Millisecond); got != "" {
		t.Fatalf("probe of a silent daemon = %q, want empty", got)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("probe did not give up at its timeout")
	}
}

// A pre-versioning local daemon ignores the probe but still answers the
// original tools. Exercise the constructor runMCP uses, including its actual
// startup budget and the connection's usability after the probe times out.
func TestLocalMCPBridge_SilentVersionProbeKeepsLegacyToolsAvailable(t *testing.T) {
	client, err := ipc.NewClient(echoServer(t, "pane-legacy"))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ready := make(chan *mcpBridge, 1)
	go func() { ready <- newLocalMCPBridge(client) }()
	var bridge *mcpBridge
	select {
	case bridge = <-ready:
	case <-time.After(3 * time.Second): // 2 s local budget plus scheduling slack
		client.Close()
		<-ready
		t.Fatal("local MCP startup waited beyond its local probe budget")
	}
	if bridge.daemonVersion != "" {
		t.Fatalf("silent daemon version = %q, want unknown", bridge.daemonVersion)
	}
	go bridge.readLoop(context.Background())
	if got := firstPaneID(t, bridge); got != "pane-legacy" {
		t.Fatalf("legacy list_panes after the probe = %q", got)
	}
}

// An unscoped list skips a remote whose daemon is too old and still returns
// the local entries; list_hosts shows why the remote is missing. A NAMED old
// remote is a clear refusal, not a timeout.
func TestAggregation_SkipsAnOldRemoteAndReportsWhy(t *testing.T) {
	local := newFakeIPCDaemon(t, "pane-local")
	remote := newFakeIPCDaemonVersion(t, "pane-remote", "1.71.0")
	session, _ := toolHarness(t, local, remote)

	text, err := callTool(t, session, "list_projects", nil)
	if err != nil {
		t.Fatalf("unscoped list_projects failed on the old remote: %v", err)
	}
	var projects []hostedProject
	if err := json.Unmarshal([]byte(text), &projects); err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].Host != "" {
		t.Fatalf("projects = %+v, want only the local one", projects)
	}

	hosts, err := callTool(t, session, "list_hosts", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(hosts, `"daemon_version": "1.71.0"`) || !strings.Contains(hosts, "last request failed") || !strings.Contains(hosts, mcpDaemonMinVersion) {
		t.Fatalf("list_hosts does not explain the skipped host:\n%s", hosts)
	}

	_, err = callTool(t, session, "list_projects", map[string]any{"host": "gpu"})
	if err == nil || !strings.Contains(err.Error(), "1.71.0") || !strings.Contains(err.Error(), mcpDaemonMinVersion) {
		t.Fatalf("named old remote: %v, want a version refusal", err)
	}

	// A request every daemon answers still aggregates the old remote.
	panes, err := callTool(t, session, "list_panes", nil)
	if err != nil || !strings.Contains(panes, "pane-remote") {
		t.Fatalf("list_panes lost the old remote: err=%v\n%s", err, panes)
	}
	// After a success the recorded failure is cleared.
	if _, err := callTool(t, session, "list_panes", map[string]any{"host": "gpu"}); err != nil {
		t.Fatal(err)
	}
}

// create_pane's bare form reaches an old daemon; the dialog options do not,
// because an old daemon would silently drop them.
func TestCreatePane_DialogOptionsNeedANewDaemon(t *testing.T) {
	local := newFakeIPCDaemon(t, "pane-local")
	remote := newFakeIPCDaemonVersion(t, "pane-remote", "1.71.0")
	session, _ := toolHarness(t, local, remote)

	_, err := callTool(t, session, "create_pane", map[string]any{"host": "gpu", "type": "claude-code", "toggles": []string{"chrome"}})
	if err == nil || !strings.Contains(err.Error(), "1.71.0") {
		t.Fatalf("create_pane with toggles on an old daemon: %v, want a version refusal", err)
	}
	if !remote.sawNo(ipc.MsgCreatePaneReq) {
		t.Fatal("the refused create still reached the old daemon")
	}
}

func (f *fakeIPCDaemon) sawNo(typ string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.received {
		if m.Type == typ {
			return false
		}
	}
	return true
}

// The tool count guard lives in the schema test; this pins that the schema
// test's session still initialises with a probing bridge (a regression here
// would be every tool waiting a probe timeout per call).
func TestToolHarness_ProbesTheLocalVersionOnce(t *testing.T) {
	local := newFakeIPCDaemonVersion(t, "pane-local", "2.0.0")
	session, r := toolHarness(t, local, nil)
	if _, err := session.ListTools(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if r.local.daemonVersion != "2.0.0" {
		t.Fatalf("local daemonVersion = %q", r.local.daemonVersion)
	}
}
