package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	apty "github.com/artyomsv/quil/internal/pty"
)

type templateWireState struct {
	Tabs []struct {
		ID     string   `json:"id"`
		Name   string   `json:"name"`
		Panes  []string `json:"panes"`
		Layout string   `json:"template_layout"`
		Main   string   `json:"template_main"`
	} `json:"tabs"`
	Panes []struct {
		ID        string `json:"id"`
		Preparing string `json:"preparing_worktree"`
	} `json:"panes"`
}

type templateFrames struct {
	mu     sync.Mutex
	states []templateWireState
}

func observeTemplateFrames(t *testing.T) *templateFrames {
	t.Helper()
	c, err := ipc.NewClient(filepath.Join(config.QuilDir(), "s.sock"))
	if err != nil {
		t.Fatal(err)
	}
	roundTrip(t, c, ipc.MsgListTabsReq, ipc.MsgListTabsResp, nil)
	f := &templateFrames{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			msg, err := c.Receive()
			if err != nil {
				return
			}
			if msg.Type != ipc.MsgWorkspaceState {
				continue
			}
			var state templateWireState
			if err := msg.DecodePayload(&state); err != nil {
				return
			}
			f.mu.Lock()
			f.states = append(f.states, state)
			f.mu.Unlock()
		}
	}()
	t.Cleanup(func() { c.Close(); <-done })
	// The round trip above establishes registration before observing frames.
	return f
}

func (f *templateFrames) snapshot() []templateWireState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]templateWireState(nil), f.states...)
}

func templateRequest(t *testing.T, c *ipc.Client, req ipc.CreateFromTemplateReqPayload) ipc.CreateFromTemplateRespPayload {
	t.Helper()
	return decodeInto[ipc.CreateFromTemplateRespPayload](t, roundTrip(t, c, ipc.MsgCreateFromTemplateReq, ipc.MsgCreateFromTemplateResp, req))
}

func saveTestTemplate(t *testing.T, tpl config.Template) {
	t.Helper()
	if err := config.WriteTemplates(config.Templates{Templates: []config.Template{tpl}}); err != nil {
		t.Fatal(err)
	}
}

func TestTemplateIPC_Pair_CreatesNamedPanesInOneFrame(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	d, c := flowTestDaemon(t)
	keep := d.session.CreateTab("keep focus")
	frames := observeTemplateFrames(t)
	dir := t.TempDir()
	resp := templateRequest(t, c, ipc.CreateFromTemplateReqPayload{Template: "pair", Task: "Fix The Login!", CWD: dir})
	if resp.Error != "" || len(resp.PaneIDs) != 2 {
		t.Fatal(resp)
	}
	if d.session.ActiveTabID() != keep.ID {
		t.Fatal("template stole focus")
	}
	panes := d.session.Panes(resp.TabID)
	for i, want := range []struct {
		name, typ string
		args      []string
	}{
		{"claude", "claude-code", []string{"--dangerously-skip-permissions"}},
		{"codex", "codex", []string{"-a", "never", "-s", "workspace-write"}},
	} {
		p := panes[i]
		p.PluginMu.Lock()
		name, typ, cwd := p.Name, p.Type, p.CWD
		args := append([]string(nil), p.InstanceArgs...)
		p.PluginMu.Unlock()
		if p.ID != resp.PaneIDs[i] || name != want.name || typ != want.typ || !reflect.DeepEqual(args, want.args) || !sameDir(t, cwd, dir) {
			t.Fatalf("pane %d: name=%s type=%s args=%v cwd=%s", i, name, typ, args, cwd)
		}
	}
	waitUntil(t, "template frame", func() bool { return len(frames.snapshot()) >= 1 })
	time.Sleep(100 * time.Millisecond) // Catch an extra per-pane frame after the response.
	states := frames.snapshot()
	if len(states) != 1 {
		t.Fatalf("got %d frames, want 1", len(states))
	}
	var found bool
	for _, tab := range states[0].Tabs {
		if tab.ID == resp.TabID {
			found = tab.Name == "fix-the-login" && tab.Layout == "columns" && tab.Main == resp.PaneIDs[0] && reflect.DeepEqual(tab.Panes, resp.PaneIDs)
		}
	}
	if !found {
		t.Fatalf("tab metadata missing from frame: %+v", states)
	}
}

func TestTemplateIPC_ArgumentsAndMetadata_SurviveTemplateEditAndRestore(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	d, c := flowTestDaemon(t)
	tpl, _ := config.DefaultTemplates().ByName("pair")
	tpl.Panes[0].Model = "claude-test"
	tpl.Panes[1].Model = "codex-test"
	tpl.Panes[1].Main = true
	tpl.Panes[1].Muted = true
	tpl.Panes[1].QuilMCP = true
	saveTestTemplate(t, tpl)
	resp := templateRequest(t, c, ipc.CreateFromTemplateReqPayload{Template: "pair", CWD: t.TempDir()})
	if resp.Error != "" || len(resp.PaneIDs) != 2 {
		t.Fatal(resp)
	}
	wantArgs := [][]string{
		{"--dangerously-skip-permissions", "--model", "claude-test"},
		{"-a", "never", "-s", "workspace-write", "-m", "codex-test"},
	}
	for i, p := range d.session.Panes(resp.TabID) {
		p.PluginMu.Lock()
		args, muted, mcp := append([]string(nil), p.InstanceArgs...), p.Muted, p.QuilMCP
		p.PluginMu.Unlock()
		if !reflect.DeepEqual(args, wantArgs[i]) || muted != (i == 1) || mcp != (i == 1) {
			t.Fatalf("pane %d: %v muted=%v mcp=%v", i, args, muted, mcp)
		}
	}
	active, tabs, panes, projects, project, flows := d.session.snapshotStateWithFlows()
	data, err := json.Marshal(d.workspaceStateFromSnapshot(active, tabs, panes, projects, project, false, flows))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.WorkspacePath(), data, 0600); err != nil {
		t.Fatal(err)
	}
	tpl.Panes[0].Model = "later-model"
	tpl.Panes[1].Toggles = []string{"bypass_approvals_and_sandbox"}
	tpl.Panes[1].QuilMCP = false
	saveTestTemplate(t, tpl)
	restored := New(config.Default())
	registerShippedPlugins(t, restored)
	for _, typ := range []string{"claude-code", "codex"} {
		restored.registry.Get(typ).Available = true
	}
	if err := restored.restoreWorkspace(); err != nil {
		t.Fatal(err)
	}
	tab := restored.session.Tab(resp.TabID)
	if tab.TemplateLayout != "columns" || tab.TemplateMain != resp.PaneIDs[1] {
		t.Fatalf("restored tab: %+v", tab)
	}
	for i, p := range restored.session.Panes(resp.TabID) {
		if !reflect.DeepEqual(p.InstanceArgs, wantArgs[i]) || p.QuilMCP != (i == 1) || p.Muted != (i == 1) {
			t.Fatalf("restored pane %d: %+v", i, p)
		}
		fake := &fakeSession{}
		if err := restored.spawnPane(p, fake, true); err != nil {
			t.Fatal(err)
		}
		argv := strings.Join(fake.startArgs, " ")
		if !strings.Contains(argv, strings.Join(wantArgs[i], " ")) || strings.Contains(argv, "later-model") || strings.Contains(argv, "--dangerously-bypass") {
			t.Fatalf("respawn changed frozen args: %s", argv)
		}
	}
}

type templatePromptWrite struct {
	text          string
	paneCount     int
	earlierQueued bool
}

type templatePromptSession struct {
	liveFakeSession
	d      *Daemon
	index  int
	mu     sync.Mutex
	writes []templatePromptWrite
}

func (s *templatePromptSession) Write(data []byte) (int, error) {
	panes := s.d.session.AllPanes()
	earlier := true
	for _, p := range panes {
		p.PluginMu.Lock()
		name := p.Name
		p.PluginMu.Unlock()
		if s.index > 0 && name == "first" {
			earlier = p.inputEnqueued.Load() > 0
		}
	}
	s.mu.Lock()
	s.writes = append(s.writes, templatePromptWrite{string(data), len(panes), earlier})
	s.mu.Unlock()
	return len(data), nil
}

func (s *templatePromptSession) recorded() []templatePromptWrite {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]templatePromptWrite(nil), s.writes...)
}

func TestTemplateIPC_Prompts_QueueInOrderAfterAllPanesExist(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	d, c := flowTestDaemon(t)
	tpl := config.Template{Name: "prompts", Panes: []config.TemplatePane{
		{Type: "terminal", Name: "first", Prompt: "FIRST {{task}}\n{{panes}}"},
		{Type: "terminal", Name: "second", Prompt: "SECOND {{dir}} {{branch}}\n{{panes}}"},
	}}
	saveTestTemplate(t, tpl)
	prev := newSessionFn
	var sessions []*templatePromptSession
	newSessionFn = func(_, _ int) apty.Session {
		s := &templatePromptSession{liveFakeSession: liveFakeSession{done: make(chan struct{})}, d: d, index: len(sessions)}
		sessions = append(sessions, s)
		return s
	}
	t.Cleanup(func() {
		newSessionFn = prev
		for _, s := range sessions {
			s.Close()
		}
	})
	dir := t.TempDir()
	resp := templateRequest(t, c, ipc.CreateFromTemplateReqPayload{Template: "prompts", Task: "literal {{panes}}", CWD: dir})
	if resp.Error != "" || len(resp.PaneIDs) != 2 {
		t.Fatal(resp)
	}
	for i, session := range sessions {
		waitUntil(t, "recorded prompt", func() bool { return len(session.recorded()) > 0 })
		writes := session.recorded()
		if len(writes) != 1 || writes[0].paneCount != 2 || !writes[0].earlierQueued {
			t.Fatalf("pane %d: %+v", i, writes)
		}
		for j, name := range []string{"first", "second"} {
			if !strings.Contains(writes[0].text, name+"  "+resp.PaneIDs[j]+"  terminal") {
				t.Fatalf("incomplete roster: %q", writes[0].text)
			}
		}
		if i == 0 && !strings.Contains(writes[0].text, "FIRST literal {{panes}}") {
			t.Fatal("task was expanded twice", writes)
		}
		if i == 1 && !strings.Contains(writes[0].text, "SECOND "+dir) {
			t.Fatal("directory placeholder missing", writes)
		}
	}
}

func TestTemplateIPC_InvalidRequest_CreatesNothing(t *testing.T) {
	for _, tc := range []string{"unknown_template", "unknown_plugin", "unavailable_plugin", "unknown_toggle", "conflicting_toggles", "missing_permission", "missing_directory", "file_directory", "pane_directory", "first_pane_directory", "branch_later_pane_directory", "symlink_escape", "unsupported_mcp", "bad_branch", "bad_project", "unsafe_task"} {
		t.Run(tc, func(t *testing.T) {
			t.Setenv("QUIL_HOME", t.TempDir())
			d, c := flowTestDaemon(t)
			tpl, _ := config.DefaultTemplates().ByName("pair")
			req := ipc.CreateFromTemplateReqPayload{Template: "pair", CWD: t.TempDir()}
			want := ""
			switch tc {
			case "unknown_template":
				req.Template = "missing"
				want = "missing"
			case "unknown_plugin":
				tpl.Panes[1].Type = "absent"
				want = "absent"
			case "unavailable_plugin":
				d.registry.Get("codex").Available = false
				want = "unavailable"
			case "unknown_toggle":
				tpl.Panes[1].Toggles = []string{"misspelled"}
				want = "misspelled"
			case "conflicting_toggles":
				tpl.Panes[1].Toggles = []string{"auto_workspace_write", "bypass_approvals_and_sandbox"}
				want = "mutually exclusive"
			case "missing_permission":
				tpl.Panes[1].Toggles = nil
				want = "permission_mode"
			case "missing_directory":
				req.CWD = filepath.Join(req.CWD, "missing")
				want = "directory"
			case "file_directory":
				req.CWD = filepath.Join(req.CWD, "file")
				if err := os.WriteFile(req.CWD, []byte("x"), 0600); err != nil {
					t.Fatal(err)
				}
				want = "directory"
			case "pane_directory":
				tpl.Panes[1].CWD = "missing"
				want = "directory"
			case "first_pane_directory":
				tpl.Panes[0].CWD = "missing"
				want = "directory"
			case "branch_later_pane_directory":
				req.Branch = "feat/subdir"
				tpl.Panes[1].CWD = "missing"
				want = "directory"
			case "symlink_escape":
				if err := os.Symlink(t.TempDir(), filepath.Join(req.CWD, "outside")); err != nil {
					t.Skip(err)
				}
				tpl.Panes[1].CWD = "outside"
				want = "leaves"
			case "unsupported_mcp":
				tpl.Panes[1] = config.TemplatePane{Type: "terminal", QuilMCP: true}
				want = "MCP"
			case "bad_branch":
				req.Branch = "-bad"
				want = "branch"
			case "bad_project":
				req.ProjectID = "missing"
				want = "project"
			case "unsafe_task":
				req.Task = "bad\rinput"
				want = "control"
			}
			saveTestTemplate(t, tpl)
			var called bool
			stubAdd(t, func(context.Context, string, string, string) error {
				called = true
				return errors.New("should not run")
			})
			resp := templateRequest(t, c, req)
			if !strings.Contains(resp.Error, want) || resp.TabID != "" || len(resp.PaneIDs) != 0 {
				t.Fatalf("want %q, got %+v", want, resp)
			}
			if len(d.session.Tabs()) != 0 || len(d.session.AllPanes()) != 0 || called {
				t.Fatal("refused request created state")
			}
		})
	}
}

func TestTemplateIPC_Worktree_PublishesPreparingSwapAndCompleteFrames(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	d, c := flowTestDaemon(t)
	tpl, _ := config.DefaultTemplates().ByName("pair")
	// Main is first, so its placeholder ID must be replaced by the real ID.
	saveTestTemplate(t, tpl)
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	stubAdd(t, func(_ context.Context, _, path, _ string) error { <-release; return os.MkdirAll(path, 0700) })
	frames := observeTemplateFrames(t)
	resp := templateRequest(t, c, ipc.CreateFromTemplateReqPayload{Template: "pair", CWD: t.TempDir(), Branch: "feat/template"})
	if resp.Error != "" || resp.PreparingWorktree != "feat/template" || len(resp.PaneIDs) != 1 {
		t.Fatal(resp)
	}
	placeholder := d.session.Pane(resp.PaneIDs[0])
	placeholder.PluginMu.Lock()
	preparing, pty := placeholder.PreparingWorktree, placeholder.PTY
	placeholder.PluginMu.Unlock()
	if preparing != resp.PreparingWorktree || pty != nil {
		t.Fatal("placeholder not visible and PTY-less")
	}
	waitUntil(t, "preparing frame", func() bool { return len(frames.snapshot()) == 1 })
	once.Do(func() { close(release) })
	waitUntil(t, "completed template frame", func() bool { return len(frames.snapshot()) >= 3 })
	time.Sleep(100 * time.Millisecond)
	states := frames.snapshot()
	if len(states) != 3 {
		t.Fatalf("got %d frames, want 3", len(states))
	}
	for _, state := range states {
		if len(state.Tabs) != 1 || len(state.Tabs[0].Panes) == 0 {
			t.Fatalf("published an empty tab: %+v", state)
		}
	}
	if states[0].Panes[0].Preparing != "feat/template" {
		t.Fatalf("first frame has no preparing pane: %+v", states[0])
	}
	final := states[2].Tabs[0]
	if len(final.Panes) != 2 || final.Main != final.Panes[0] || final.Main == placeholder.ID || final.Layout != "columns" {
		t.Fatalf("bad final anchor: %+v", final)
	}
	if d.session.Pane(placeholder.ID) != nil {
		t.Fatal("placeholder survived swap")
	}
	for _, p := range d.session.Panes(resp.TabID) {
		p.PluginMu.Lock()
		cwd := p.CWD
		p.PluginMu.Unlock()
		if !strings.Contains(cwd, "feat-template") {
			t.Fatalf("pane outside worktree: %s", cwd)
		}
	}
}

// The actual new checkout determines whether pane 0's subdirectory is valid,
// including when the chosen source tree does not contain that directory.
func TestTemplateIPC_FirstWorktreeSubdir_ValidatesNewCheckout(t *testing.T) {
	for _, tc := range []string{"nested", "checkout_only", "empty", "missing", "escape", "inside_symlink"} {
		t.Run(tc, func(t *testing.T) {
			t.Setenv("QUIL_HOME", t.TempDir())
			d, c := flowTestDaemon(t)
			source, outside := t.TempDir(), t.TempDir()
			if tc != "checkout_only" {
				if err := os.Mkdir(filepath.Join(source, "nested"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if tc == "escape" || tc == "inside_symlink" {
				if err := os.Symlink(outside, filepath.Join(source, "probe-link")); err != nil {
					t.Skip(err)
				}
			}
			tpl, _ := config.DefaultTemplates().ByName("pair")
			if tc != "empty" {
				tpl.Panes[0].CWD = "nested"
			}
			saveTestTemplate(t, tpl)

			var spawns atomic.Int32
			oldSession := newSessionFn
			newSessionFn = func(cols, rows int) apty.Session {
				spawns.Add(1)
				return oldSession(cols, rows)
			}
			t.Cleanup(func() { newSessionFn = oldSession })
			created := make(chan string, 1)
			removed := make(chan string, 1)
			oldRemove := removeWorktreeFn
			removeWorktreeFn = func(_ context.Context, _, path, _ string) error {
				err := os.RemoveAll(path)
				removed <- path
				return err
			}
			t.Cleanup(func() { removeWorktreeFn = oldRemove })
			stubAdd(t, func(_ context.Context, _, path, _ string) error {
				if err := os.MkdirAll(path, 0700); err != nil {
					return err
				}
				var err error
				switch tc {
				case "nested", "checkout_only":
					err = os.Mkdir(filepath.Join(path, "nested"), 0700)
				case "escape":
					err = os.Symlink(outside, filepath.Join(path, "nested"))
				case "inside_symlink":
					if err = os.Mkdir(filepath.Join(path, "actual"), 0700); err == nil {
						err = os.Symlink("actual", filepath.Join(path, "nested"))
					}
				}
				created <- path
				return err
			})
			frames := observeTemplateFrames(t)
			resp := templateRequest(t, c, ipc.CreateFromTemplateReqPayload{Template: "pair", CWD: source, Branch: "feat/subdir"})
			if resp.Error != "" || resp.PreparingWorktree != "feat/subdir" || len(resp.PaneIDs) != 1 {
				t.Fatal(resp)
			}
			var checkout string
			select {
			case checkout = <-created:
			case <-time.After(3 * time.Second):
				t.Fatal("checkout did not run")
			}

			if tc == "missing" || tc == "escape" {
				// The immediate response named a placeholder; the refusal is
				// asynchronous and must remove that provisional tab and pane.
				waitUntil(t, "asynchronous directory refusal", func() bool {
					return hasEventType(d, resp.PaneIDs[0], "template_create_failed")
				})
				if len(d.session.Tabs()) != 0 || len(d.session.AllPanes()) != 0 || spawns.Load() != 0 {
					t.Fatal("invalid checkout directory left a tab, pane, or process")
				}
				select {
				case path := <-removed:
					if path != checkout {
						t.Fatalf("cleaned %s, want %s", path, checkout)
					}
				default:
					t.Fatal("checkout was not cleaned up")
				}
				if _, err := os.Stat(checkout); !os.IsNotExist(err) {
					t.Fatalf("checkout remains: %v", err)
				}
				want := "directory"
				if tc == "escape" {
					want = "leaves"
				}
				for _, e := range d.events.Events() {
					if e.Type == "template_create_failed" && (!strings.Contains(e.Message, "nested") || !strings.Contains(e.Message, want)) {
						t.Fatalf("unnamed refusal: %+v", e)
					}
				}
				waitUntil(t, "empty workspace frame", func() bool { return len(frames.snapshot()) >= 2 })
				if last := frames.snapshot(); len(last[len(last)-1].Tabs) != 0 {
					t.Fatal("removed template is still on the wire")
				}
				return
			}
			waitUntil(t, "completed subdirectory template", func() bool { return len(frames.snapshot()) >= 3 })
			panes := d.session.Panes(resp.TabID)
			if len(panes) != 2 || spawns.Load() != 2 {
				t.Fatalf("panes=%d spawns=%d", len(panes), spawns.Load())
			}
			firstCWD := checkout
			if tc == "nested" || tc == "checkout_only" {
				firstCWD = filepath.Join(checkout, "nested")
			}
			if tc == "inside_symlink" {
				firstCWD = filepath.Join(checkout, "actual")
			}
			for i, pane := range panes {
				want := checkout
				if i == 0 {
					want = firstCWD
				}
				pane.PluginMu.Lock()
				cwd := pane.CWD
				spawnCWD := pane.PTY.(*liveFakeSession).cwd
				pane.PluginMu.Unlock()
				if !sameDir(t, cwd, want) || !sameDir(t, spawnCWD, want) {
					t.Fatalf("pane %d: cwd=%s PTY cwd=%s want=%s", i, cwd, spawnCWD, want)
				}
			}
			owned := ownedWorktreePaths(panes)
			if len(owned) != 1 || !sameDir(t, owned[0], checkout) {
				t.Fatalf("ownership must name checkout root: %v", owned)
			}
			last := frames.snapshot()
			if len(last) != 3 || last[2].Tabs[0].Main != panes[0].ID || panes[0].ID == resp.PaneIDs[0] {
				t.Fatalf("bad frames or final anchor: %+v", last)
			}
		})
	}
}

func TestSpawnPane_QuilMCP_UsesOrdinaryToolsetUnlessFlowRoleWins(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	d := newTestDaemon(t)
	registerShippedPlugins(t, d)
	stubFlowCodexProbe(t)
	old := flowMCPExeFn
	flowMCPExeFn = func() (string, error) { return "/test/quil", nil }
	t.Cleanup(func() { flowMCPExeFn = old })
	for _, agent := range []string{"claude-code", "codex", "opencode"} {
		d.registry.Get(agent).Available = true
		for _, tc := range []struct {
			mcp  bool
			role string
		}{{false, ""}, {true, ""}, {false, "analyst"}, {true, "analyst"}} {
			fake := &fakeSession{}
			p := &Pane{ID: "pane-a1b2c3d4", Type: agent, CWD: t.TempDir(), QuilMCP: tc.mcp, FlowRole: tc.role}
			if err := d.spawnPane(p, fake, false); err != nil {
				t.Fatal(err)
			}
			all := strings.Join(append(append([]string(nil), fake.startArgs...), fake.env...), " ")
			if strings.Contains(all, "/test/quil") != (tc.mcp || tc.role != "") || strings.Contains(all, "--toolset") != (tc.role != "") {
				t.Fatalf("agent=%s mcp=%v role=%s args=%s", agent, tc.mcp, tc.role, all)
			}
			if tc.mcp && tc.role == "" && !strings.Contains(all, `"mcp"`) {
				t.Fatalf("missing ordinary mcp argv: %s", all)
			}
		}
	}
}
