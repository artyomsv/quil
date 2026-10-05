package daemon

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/clientauth"
	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	apty "github.com/artyomsv/quil/internal/pty"
)

// argvSession is a live fake PTY that records the argv the spawn path
// STARTS (spawnPane → ptySession.Start) — the seam the command line is
// asserted at, because Pane.InstanceArgs alone says nothing about what the
// plugin's own arguments did to it. Start runs on the create handler's
// goroutine, the assertion on the test's, hence mu.
type argvSession struct {
	*liveFakeSession
	mu   sync.Mutex
	argv []string
}

func (s *argvSession) Start(cmd string, args ...string) error {
	s.mu.Lock()
	s.argv = append([]string{cmd}, args...)
	s.mu.Unlock()
	return nil
}

// argvSpawns swaps newSessionFn (after newAuthHarness installed its own) for
// one that records each spawn's argv; spawned() returns them in order.
func argvSpawns(t *testing.T) (spawned func() [][]string) {
	t.Helper()
	var mu sync.Mutex
	var sessions []*argvSession
	prev := newSessionFn
	newSessionFn = func(cols, rows int) apty.Session {
		s := &argvSession{liveFakeSession: newLiveFakeSession()}
		mu.Lock()
		sessions = append(sessions, s)
		mu.Unlock()
		return s
	}
	t.Cleanup(func() {
		newSessionFn = prev
		mu.Lock()
		defer mu.Unlock()
		for _, s := range sessions {
			s.Close() // release the exit watchers parked on WaitExit
		}
	})
	return func() [][]string {
		mu.Lock()
		defer mu.Unlock()
		out := make([][]string, 0, len(sessions))
		for _, s := range sessions {
			s.mu.Lock()
			out = append(out, append([]string(nil), s.argv...))
			s.mu.Unlock()
		}
		return out
	}
}

// containsRun reports whether want appears in argv as one contiguous run:
// the resolved selections, in order, wherever the plugin's own args put them.
func containsRun(argv, want []string) bool {
	for i := 0; i+len(want) <= len(argv); i++ {
		if reflect.DeepEqual(argv[i:i+len(want)], want) {
			return true
		}
	}
	return false
}

// lastSpawnHas waits for a spawn whose argv carries want as one run.
func lastSpawnHas(t *testing.T, spawned func() [][]string, want []string) {
	t.Helper()
	waitUntil(t, "a spawn carrying "+strings.Join(want, " "), func() bool {
		all := spawned()
		return len(all) > 0 && containsRun(all[len(all)-1], want)
	})
}

func paneArgs(p *Pane) (string, []string) {
	p.PluginMu.Lock()
	defer p.PluginMu.Unlock()
	return p.Type, append([]string(nil), p.InstanceArgs...)
}

func newPaneIn(t *testing.T, d *Daemon, tabID string, before map[string]bool) *Pane {
	t.Helper()
	var found *Pane
	waitUntil(t, "the new pane", func() bool {
		for _, p := range d.session.Panes(tabID) {
			if !before[p.ID] {
				found = p
				return true
			}
		}
		return false
	})
	return found
}

func idsIn(d *Daemon, tabID string) map[string]bool {
	out := map[string]bool{}
	for _, p := range d.session.Panes(tabID) {
		out[p.ID] = true
	}
	return out
}

// A standard token's TUI sends names; the daemon resolves them. A toggle AND
// a kube context through each of the three carriers — create, the replace
// form, and a new tab's first pane — asserted twice: on the pane record and
// in the argv the spawn path STARTED. The same conn sending raw
// instance_args is still refused: names are the only way in.
func TestToggles_StandardCreateResolvesNames(t *testing.T) {
	h := newAuthHarness(t)
	spawned := argvSpawns(t)
	std, _ := h.login(t, h.mint(t, "std", clientauth.LevelStandard, nil))
	tab := h.d.session.CreateTab("t")

	before := idsIn(h.d, tab.ID)
	sendNoID(t, std, ipc.MsgCreatePane, ipc.CreatePanePayload{TabID: tab.ID, Type: "k9s",
		Toggles: []string{"readonly"}, KubeContext: "prod"})
	// A pane is published before its fields are written, and spawned only
	// after; so the spawn is awaited first and the record read after it.
	p := newPaneIn(t, h.d, tab.ID, before)
	lastSpawnHas(t, spawned, []string{"--context", "prod", "--readonly"})
	if typ, args := paneArgs(p); typ != "k9s" || !reflect.DeepEqual(args, []string{"--context", "prod", "--readonly"}) {
		t.Fatalf("create: type=%s args=%v", typ, args)
	}

	// Replace form.
	before = idsIn(h.d, tab.ID)
	sendNoID(t, std, ipc.MsgCreatePane, ipc.CreatePanePayload{TabID: tab.ID, Type: "k9s",
		ReplacePaneID: p.ID, Toggles: []string{"readonly"}, KubeContext: "staging"})
	r := newPaneIn(t, h.d, tab.ID, before)
	lastSpawnHas(t, spawned, []string{"--context", "staging", "--readonly"})
	if _, args := paneArgs(r); !reflect.DeepEqual(args, []string{"--context", "staging", "--readonly"}) {
		t.Fatalf("replace args = %v", args)
	}

	// First pane of a new tab.
	tabsBefore := map[string]bool{}
	for _, tb := range h.d.session.Tabs() {
		tabsBefore[tb.ID] = true
	}
	sendNoID(t, std, ipc.MsgCreateTab, ipc.CreateTabPayload{Name: "n",
		FirstPane: &ipc.FirstPaneSpec{Type: "k9s", Toggles: []string{"readonly"}, KubeContext: "dev"}})
	// The tab is published a moment before its pane is constructed, so wait
	// for the pane, not merely for the tab.
	var first []*Pane
	waitUntil(t, "the new tab's pane", func() bool {
		for _, tb := range h.d.session.Tabs() {
			if !tabsBefore[tb.ID] {
				first = h.d.session.Panes(tb.ID)
				return len(first) > 0
			}
		}
		return false
	})
	if len(first) != 1 {
		t.Fatalf("new tab has %d panes", len(first))
	}
	lastSpawnHas(t, spawned, []string{"--context", "dev", "--readonly"})
	if _, args := paneArgs(first[0]); !reflect.DeepEqual(args, []string{"--context", "dev", "--readonly"}) {
		t.Fatalf("first pane args = %v", args)
	}

	// A plugin's own toggle of another kind: claude-code's --chrome.
	before = idsIn(h.d, tab.ID)
	sendNoID(t, std, ipc.MsgCreatePane, ipc.CreatePanePayload{TabID: tab.ID, Type: "claude-code",
		Toggles: []string{"chrome"}})
	newPaneIn(t, h.d, tab.ID, before)
	lastSpawnHas(t, spawned, []string{"--chrome"})

	// The raw form of the same choice, from the same conn, is still refused.
	before = idsIn(h.d, tab.ID)
	resp := roundTrip(t, std, ipc.MsgCreatePane, ipc.MsgError, ipc.CreatePanePayload{TabID: tab.ID, Type: "k9s",
		InstanceArgs: []string{"--context", "prod", "--readonly"}})
	if e := decodeInto[ipc.ErrorPayload](t, resp); e.Code != ipc.ErrCodeRefused || e.Message != "raw instance arguments need full rights" {
		t.Fatalf("raw instance_args answered %+v, want refused", e)
	}
	finishDispatch(t, std)
	if got := idsIn(h.d, tab.ID); len(got) != len(before) {
		t.Fatalf("raw instance_args created a pane: %d panes, had %d", len(got), len(before))
	}
}

func TestToggles_RefusedSelectionsCreateNothing(t *testing.T) {
	h := newAuthHarness(t)
	local := h.local(t)
	tab := h.d.session.CreateTab("t")
	for name, p := range map[string]ipc.CreatePanePayload{
		"unknown toggle":         {TabID: tab.ID, Type: "claude-code", Toggles: []string{"nope"}},
		"two of one group":       {TabID: tab.ID, Type: "claude-code", Toggles: []string{"dangerously_skip_permissions", "enable_auto_mode"}},
		"context on non-kube":    {TabID: tab.ID, Type: "claude-code", KubeContext: "prod"},
		"context flag injection": {TabID: tab.ID, Type: "k9s", KubeContext: "--kubeconfig=/x"},
		"context with bidi":      {TabID: tab.ID, Type: "k9s", KubeContext: "a" + string(rune(0x202e)) + "b"},
	} {
		before := len(h.d.session.Panes(tab.ID))
		sendNoID(t, local, ipc.MsgCreatePane, p)
		roundTrip(t, local, ipc.MsgVersionReq, ipc.MsgVersionResp, struct{}{})
		time.Sleep(20 * time.Millisecond)
		if n := len(h.d.session.Panes(tab.ID)); n != before {
			t.Errorf("%s: a pane was created", name)
		}
	}

	// A refused first pane refuses the whole tab: the selections are
	// resolved before the tab is minted, so nothing is left half-made.
	tabsBefore := len(h.d.session.Tabs())
	sendNoID(t, local, ipc.MsgCreateTab, ipc.CreateTabPayload{Name: "n",
		FirstPane: &ipc.FirstPaneSpec{Type: "k9s", KubeContext: "-x"}})
	roundTrip(t, local, ipc.MsgVersionReq, ipc.MsgVersionResp, struct{}{})
	time.Sleep(20 * time.Millisecond)
	if n := len(h.d.session.Tabs()); n != tabsBefore {
		t.Errorf("a refused first pane still created a tab (%d tabs, had %d)", n, tabsBefore)
	}
}

// The MCP request path resolves the kube context by the same rule: placed
// before the toggles, and refused for a plugin that does not discover kube.
func TestCreatePaneReq_KubeContextOrder(t *testing.T) {
	d, client := mcpTestDaemon(t)
	tab := d.session.CreateTab("t")

	resp := decodeInto[ipc.CreatePaneRespPayload](t, roundTrip(t, client, ipc.MsgCreatePaneReq, ipc.MsgCreatePaneResp,
		ipc.CreatePaneReqPayload{TabID: tab.ID, Type: "k9s", Toggles: []string{"readonly"}, KubeContext: "prod"}))
	pane := d.session.Pane(resp.PaneID)
	if pane == nil {
		t.Fatalf("pane not created: %+v", resp)
	}
	if _, args := paneArgs(pane); !reflect.DeepEqual(args, []string{"--context", "prod", "--readonly"}) {
		t.Fatalf("args = %v", args)
	}

	resp = decodeInto[ipc.CreatePaneRespPayload](t, roundTrip(t, client, ipc.MsgCreatePaneReq, ipc.MsgCreatePaneResp,
		ipc.CreatePaneReqPayload{TabID: tab.ID, Type: "claude-code", KubeContext: "prod"}))
	if resp.PaneID != "" || !strings.Contains(resp.Error, "kube") {
		t.Fatalf("kube context on claude-code: %+v", resp)
	}
}

// The exact argv the selections become, in the order the dialog always
// built it: the instance's own args, then --context <ctx>, then each toggle's
// ArgsWhenOn in the order the caller named them (resolveToggles walks the
// names, not the plugin's declarations; this case names them in both orders
// at once).
func TestApplyNamedSelections_ExactOrder(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	d := New(config.Default())
	dir := t.TempDir()
	const toml = `
[plugin]
name = "kubetool"
category = "tools"

[command]
cmd = "kubetool"
discover = "kube"

[[command.toggles]]
name = "readonly"
label = "Read-only"
args_when_on = ["--readonly"]

[[command.toggles]]
name = "pods"
label = "Pods view"
args_when_on = ["--command", "pods"]
`
	if err := os.WriteFile(filepath.Join(dir, "kubetool.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := d.registry.LoadFromDir(dir); err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}

	got, err := d.applyNamedSelections("kubetool", []string{"--kubeconfig", "/k"}, []string{"readonly", "pods"}, "prod")
	want := []string{"--kubeconfig", "/k", "--context", "prod", "--readonly", "--command", "pods"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v err=%v, want %v", got, err, want)
	}

	// Nothing named leaves the instance args exactly as given.
	got, err = d.applyNamedSelections("kubetool", []string{"--kubeconfig", "/k"}, nil, "")
	if err != nil || !reflect.DeepEqual(got, []string{"--kubeconfig", "/k"}) {
		t.Fatalf("no selections: got %v err=%v", got, err)
	}
}

// awaitCreatePaneResp reads frames until a create_pane_resp arrives,
// skipping broadcasts.
func awaitCreatePaneResp(t *testing.T, c *ipc.Client) ipc.CreatePaneRespPayload {
	t.Helper()
	if err := c.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	defer c.SetReadDeadline(time.Time{})
	for {
		msg, err := c.Receive()
		if err != nil {
			t.Fatalf("no create_pane_resp arrived: %v", err)
		}
		if msg.Type == ipc.MsgCreatePaneResp {
			return decodeInto[ipc.CreatePaneRespPayload](t, msg)
		}
	}
}

// A refused selection on a WORKTREE create is still answered with the
// create_pane_resp that path always sends, even to an id-less request: the
// TUI holds a "creating worktree" placeholder (split, replace) or a branch
// entry (new tab) that only that answer unwinds. The answer echoes the tab
// and the spec, which are the keys the TUI matches on.
func TestToggles_RefusedWorktreeCreateIsAnswered(t *testing.T) {
	h := newAuthHarness(t)
	local := h.local(t)
	tab := h.d.session.CreateTab("t")
	spec := &ipc.WorktreeSpec{RepoRoot: "/repo", Branch: "feat/x"}

	for name, p := range map[string]ipc.CreatePanePayload{
		"split":   {TabID: tab.ID, Type: "claude-code", Toggles: []string{"nope"}, Worktree: spec},
		"replace": {TabID: tab.ID, Type: "claude-code", Toggles: []string{"nope"}, Worktree: spec, ReplacePaneID: "pane-x"},
	} {
		before := len(h.d.session.Panes(tab.ID))
		sendNoID(t, local, ipc.MsgCreatePane, p)
		resp := awaitCreatePaneResp(t, local)
		if resp.TabID != tab.ID || resp.Worktree == nil || resp.Worktree.Branch != "feat/x" ||
			!strings.Contains(resp.Error, "unknown toggle") || resp.PaneID != "" {
			t.Errorf("%s: resp = %+v", name, resp)
		}
		if n := len(h.d.session.Panes(tab.ID)); n != before {
			t.Errorf("%s: a pane was created", name)
		}
	}

	// New tab: no tab is minted, so the answer names none — the TUI keys
	// this create by its branch alone.
	tabsBefore := len(h.d.session.Tabs())
	sendNoID(t, local, ipc.MsgCreateTab, ipc.CreateTabPayload{Name: "n",
		FirstPane: &ipc.FirstPaneSpec{Type: "claude-code", Toggles: []string{"nope"}, Worktree: spec}})
	resp := awaitCreatePaneResp(t, local)
	if resp.TabID != "" || resp.Worktree == nil || resp.Worktree.Branch != "feat/x" || !strings.Contains(resp.Error, "unknown toggle") {
		t.Errorf("new tab: resp = %+v", resp)
	}
	if n := len(h.d.session.Tabs()); n != tabsBefore {
		t.Errorf("new tab: a tab was created (%d, had %d)", n, tabsBefore)
	}
}

// A refused ordinary create is answered only when the request carries an
// id, as an error frame; the TUI's id-less sends still get nothing.
func TestToggles_RefusedIDBearingCreateGetsError(t *testing.T) {
	h := newAuthHarness(t)
	std, _ := h.login(t, h.mint(t, "std", clientauth.LevelStandard, nil))
	tab := h.d.session.CreateTab("t")
	for _, typ := range []string{ipc.MsgCreatePane, ipc.MsgCreateTab} {
		var payload any = ipc.CreatePanePayload{TabID: tab.ID, Type: "claude-code", Toggles: []string{"nope"}}
		if typ == ipc.MsgCreateTab {
			payload = ipc.CreateTabPayload{FirstPane: &ipc.FirstPaneSpec{Type: "claude-code", Toggles: []string{"nope"}}}
		}
		e := decodeInto[ipc.ErrorPayload](t, roundTrip(t, std, typ, ipc.MsgError, payload))
		if e.Code != ipc.ErrCodeBadPayload || !strings.Contains(e.Message, "unknown toggle") {
			t.Errorf("%s: error = %+v", typ, e)
		}
	}
}

func TestValidKubeContext(t *testing.T) {
	if err := validKubeContext("arn:aws:eks:eu-west-1:1:cluster/x"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"-x", strings.Repeat("a", 254), "a\nb"} {
		if validKubeContext(bad) == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
