package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeIPCDaemon answers the requests the tool tests need and records what it
// was sent, so a test can assert on the wire payload a tool produced.
type fakeIPCDaemon struct {
	mu       sync.Mutex
	paneID   string
	received []*ipc.Message
	sock     string
	// version is what the fake reports to the bridge's probe. Empty means
	// "do not answer", the pre-versioning daemon shape.
	version string
	// requests is what the fake advertises as handled (VersionRespPayload
	// .Requests). Nil is the daemon that predates the field, where the
	// version floor still decides.
	requests []string
}

func newFakeIPCDaemon(t *testing.T, paneID string) *fakeIPCDaemon {
	return newFakeIPCDaemonVersion(t, paneID, mcpDaemonMinVersion)
}

// newFakeIPCDaemonRequests is newFakeIPCDaemonVersion for a daemon new enough
// to advertise which gated request types it handles.
func newFakeIPCDaemonRequests(t *testing.T, paneID, version string, requests ...string) *fakeIPCDaemon {
	t.Helper()
	f := newFakeIPCDaemonVersion(t, paneID, version)
	f.mu.Lock()
	f.requests = requests
	f.mu.Unlock()
	return f
}

func newFakeIPCDaemonVersion(t *testing.T, paneID, version string) *fakeIPCDaemon {
	t.Helper()
	shortenVersionProbe(t)
	f := &fakeIPCDaemon{paneID: paneID, sock: filepath.Join(t.TempDir(), "fake.sock"), version: version}
	srv := ipc.NewServer(f.sock, func(conn *ipc.Conn, m *ipc.Message) {
		f.mu.Lock()
		f.received = append(f.received, m)
		f.mu.Unlock()
		var resp *ipc.Message
		switch m.Type {
		case ipc.MsgVersionReq:
			if f.version == "" {
				return
			}
			resp, _ = ipc.NewMessage(ipc.MsgVersionResp, ipc.VersionRespPayload{Version: f.version, Requests: f.requests})
		case ipc.MsgListPanesReq:
			resp, _ = ipc.NewMessage(ipc.MsgListPanesResp, ipc.ListPanesRespPayload{Panes: []ipc.PaneInfo{{ID: f.paneID, TabID: "tab-" + f.paneID, AgentState: "idle"}}})
		case ipc.MsgPaneInput:
			resp, _ = ipc.NewMessage(ipc.MsgPaneInputResp, ipc.PaneInputRespPayload{PaneID: f.paneID, Delivered: true})
		case ipc.MsgDelegateTaskReq:
			var req ipc.DelegateTaskReqPayload
			_ = m.DecodePayload(&req)
			resp, _ = ipc.NewMessage(ipc.MsgDelegateTaskResp, ipc.DelegateTaskRespPayload{Task: ipc.TaskInfo{ID: "task-1", ToPane: req.ToPane, FromPane: req.FromPane, State: "sent"}})
		case ipc.MsgListProjectsReq:
			resp, _ = ipc.NewMessage(ipc.MsgListProjectsResp, ipc.ListProjectsRespPayload{Projects: []ipc.ProjectInfo{{ID: "proj-" + f.paneID, Name: f.paneID}}})
		case ipc.MsgCreateFromTemplateReq:
			var req ipc.CreateFromTemplateReqPayload
			if err := m.DecodePayload(&req); err != nil {
				t.Error(err)
				return
			}
			payload := ipc.CreateFromTemplateRespPayload{TabID: "tab-" + f.paneID, PaneIDs: []string{f.paneID}, PreparingWorktree: req.Branch}
			if req.Template == "unknown" {
				payload = ipc.CreateFromTemplateRespPayload{Error: "unknown template"}
			}
			var err error
			resp, err = ipc.NewMessage(ipc.MsgCreateFromTemplateResp, payload)
			if err != nil {
				t.Error(err)
				return
			}
		default:
			return
		}
		resp.ID = m.ID
		conn.Send(resp)
	}, nil)
	if err := srv.Start(); err != nil {
		t.Fatalf("server: %v", err)
	}
	t.Cleanup(func() { srv.Stop() })
	return f
}

func (f *fakeIPCDaemon) inputs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, m := range f.received {
		if m.Type == ipc.MsgPaneInput {
			var p ipc.PaneInputPayload
			_ = m.DecodePayload(&p)
			out = append(out, string(p.Data))
		}
	}
	return out
}

func (f *fakeIPCDaemon) delegate() *ipc.DelegateTaskReqPayload {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.received {
		if m.Type == ipc.MsgDelegateTaskReq {
			var p ipc.DelegateTaskReqPayload
			_ = m.DecodePayload(&p)
			return &p
		}
	}
	return nil
}

// toolHarness registers every tool on a server backed by a router over one
// local fake daemon and one remote fake daemon, and returns a client session.

func toolHarness(t *testing.T, local, remote *fakeIPCDaemon) (*mcp.ClientSession, *mcpRouter) {
	t.Helper()
	cfg := config.Default()
	dial := func(cfg config.Config, d config.Destination) (*ipc.Client, error) { return ipc.NewClient(remote.sock) }
	if remote != nil {
		cfg.Destinations = []config.Destination{{Dest: "gpu"}}
	}
	r := newMCPRouter(bridgeTo(t, local.sock), cfg, dial)
	if remote != nil {
		if _, _, err := r.bridgeFor("gpu"); err != nil {
			t.Fatalf("connect remote: %v", err)
		}
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "quil-test", Version: "0"}, nil)
	registerMCPTools(server, r, &mcpLogger{dir: t.TempDir()})
	ct, st := mcp.NewInMemoryTransports()
	if _, err := server.Connect(context.Background(), st, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	session, err := client.Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { session.Close() })
	return session, r
}

func callTool(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) (string, error) {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return "", err
	}
	var text string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text += tc.Text
		}
	}
	if res.IsError {
		return text, errToolFailed(text)
	}
	return text, nil
}

type toolFailure string

func (e toolFailure) Error() string { return string(e) }
func errToolFailed(s string) error  { return toolFailure(s) }

func TestListPanes_AggregatesHostsAndMarksSelf(t *testing.T) {
	t.Setenv("QUIL_PANE_ID", "pane-local")
	local := newFakeIPCDaemon(t, "pane-local")
	remote := newFakeIPCDaemon(t, "pane-remote")
	session, r := toolHarness(t, local, remote)

	text, err := callTool(t, session, "list_panes", nil)
	if err != nil {
		t.Fatalf("list_panes: %v", err)
	}
	var panes []hostedPane
	if err := json.Unmarshal([]byte(text), &panes); err != nil {
		t.Fatalf("decode: %v\n%s", err, text)
	}
	if len(panes) != 2 {
		t.Fatalf("panes = %+v", panes)
	}
	if panes[0].ID != "pane-local" || panes[0].Host != "" || !panes[0].Self {
		t.Fatalf("local entry = %+v", panes[0])
	}
	if panes[1].ID != "pane-remote" || panes[1].Host != "gpu" || panes[1].Self {
		t.Fatalf("remote entry = %+v", panes[1])
	}
	// Discovery filed the remote ids: a later call needs no host.
	if _, host, _ := r.bridgeFor("", "pane-remote"); host != "gpu" {
		t.Fatalf("pane-remote routes to %q", host)
	}
	if _, host, _ := r.bridgeFor("", "tab-pane-remote"); host != "gpu" {
		t.Fatalf("remote tab routes to %q", host)
	}
}

// A named host that does not exist is an ERROR from every aggregating tool.
// Swallowing it returned a successful empty array, so an unreachable or
// misspelled host was indistinguishable from an empty workspace — and that is
// the answer the agent acts on.
func TestAggregatingTools_NamedHostErrorIsNotAnEmptyList(t *testing.T) {
	local := newFakeIPCDaemon(t, "pane-local")
	remote := newFakeIPCDaemon(t, "pane-remote")
	session, _ := toolHarness(t, local, remote)

	for _, tool := range []string{"list_panes", "list_tabs", "list_projects", "list_tasks", "get_notifications"} {
		text, err := callTool(t, session, tool, map[string]any{"host": "missing"})
		if err == nil {
			t.Fatalf("%s with an unknown host returned %q instead of an error", tool, text)
		}
		if !strings.Contains(err.Error(), "unknown host") {
			t.Fatalf("%s error does not name the refusal: %v", tool, err)
		}
	}
	// A host that IS configured still aggregates normally.
	if _, err := callTool(t, session, "list_panes", map[string]any{"host": "gpu"}); err != nil {
		t.Fatalf("list_panes on a live host: %v", err)
	}
}

func TestSendToPane_PasteWrapsAndThenEnters(t *testing.T) {
	local := newFakeIPCDaemon(t, "pane-local")
	session, _ := toolHarness(t, local, nil)

	if _, err := callTool(t, session, "send_to_pane", map[string]any{"pane_id": "pane-local", "input": "line one\nline two", "paste": true}); err != nil {
		t.Fatalf("send_to_pane: %v", err)
	}
	got := local.inputs()
	if len(got) != 2 || got[0] != "\x1b[200~line one\nline two\x1b[201~" || got[1] != "\r" {
		t.Fatalf("inputs = %q", got)
	}
	if _, err := callTool(t, session, "send_to_pane", map[string]any{"pane_id": "pane-local", "input": "ls"}); err != nil {
		t.Fatalf("send_to_pane: %v", err)
	}
	// CR, the byte Enter produces: LF is echoed but not executed by
	// PowerShell under ConPTY.
	if got := local.inputs(); got[len(got)-1] != "ls\r" {
		t.Fatalf("plain send = %q", got[len(got)-1])
	}
}

func TestDelegateTask_RequesterIsTheBridgesOwnPane(t *testing.T) {
	t.Setenv("QUIL_PANE_ID", "pane-me")
	local := newFakeIPCDaemon(t, "pane-worker")
	remote := newFakeIPCDaemon(t, "pane-far")
	session, _ := toolHarness(t, local, remote)

	text, err := callTool(t, session, "delegate_task", map[string]any{"pane_id": "pane-worker", "prompt": "fix <<REDACT>>secret<</REDACT>> now"})
	if err != nil {
		t.Fatalf("delegate_task: %v\n%s", err, text)
	}
	req := local.delegate()
	if req == nil || req.FromPane != "pane-me" || !req.Notify || req.Prompt != "fix secret now" {
		t.Fatalf("daemon received %+v", req)
	}
	// A remote target: the requester's pane cannot be typed into from there,
	// so no from_pane and no notify — but the task is still delegated.
	if _, err := callTool(t, session, "delegate_task", map[string]any{"pane_id": "pane-far", "host": "gpu", "prompt": "hi", "notify": true}); err != nil {
		t.Fatalf("remote delegate_task: %v", err)
	}
	far := remote.delegate()
	if far == nil || far.FromPane != "" || far.Notify {
		t.Fatalf("remote daemon received %+v", far)
	}
}

func TestListHosts_ReportsConfiguredHosts(t *testing.T) {
	local := newFakeIPCDaemon(t, "pane-local")
	remote := newFakeIPCDaemon(t, "pane-remote")
	session, _ := toolHarness(t, local, remote)
	text, err := callTool(t, session, "list_hosts", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, `"host": "gpu"`) || !strings.Contains(text, `"connected": true`) {
		t.Fatalf("list_hosts = %s", text)
	}
}

// create_pane's schema must expose the dialog options as top-level inputs, or
// an agent would have to guess a nested shape the daemon does not accept.
func TestCreatePaneSchema_ExposesDialogOptions(t *testing.T) {
	local := newFakeIPCDaemon(t, "pane-local")
	session, _ := toolHarness(t, local, nil)
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*mcp.Tool{}
	for _, tl := range tools.Tools {
		byName[tl.Name] = tl
	}
	for _, want := range []string{"create_pane", "create_tab", "create_from_template", "list_projects", "create_project", "update_project", "destroy_project",
		"switch_project", "rename_tab", "destroy_tab", "rename_pane", "list_plugins", "list_sessions", "list_hosts",
		"delegate_task", "get_task", "wait_task", "list_tasks"} {
		if byName[want] == nil {
			t.Errorf("tool %s not registered", want)
		}
	}
	schema, _ := json.Marshal(byName["create_pane"].InputSchema)
	for _, prop := range []string{`"toggles"`, `"worktree_branch"`, `"sandbox_image"`, `"resume_session_id"`, `"name"`, `"tab_id"`, `"host"`} {
		if !strings.Contains(string(schema), prop) {
			t.Errorf("create_pane schema lacks %s:\n%s", prop, schema)
		}
	}
	if len(tools.Tools) != 35 {
		t.Errorf("tool count = %d, want 35 (update docs/mcp.md and CLAUDE.md if this changed on purpose)", len(tools.Tools))
	}
}
