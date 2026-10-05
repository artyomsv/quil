package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/instances"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/plugin"
	"github.com/artyomsv/quil/internal/sandbox"
	"github.com/artyomsv/quil/internal/webgw"
)

// AC-8 (spec 5b §8): every option of the browser's create-pane dialog, sent
// as the page sends it — a split_pane_req frame on the WebSocket — through
// the real gateway (gate, saved-instance expansion) to the real daemon
// handler, asserted on the created pane's configuration. Each row fails when
// its option is dropped at the frame, the gateway or the daemon: the value it
// checks exists nowhere but in that option.

const ac8Plugin = `
[plugin]
name = "ac8-agent"
display_name = "AC8 Agent"
category = "ai"

[command]
cmd = "cat"
prompts_cwd = true
sessions = "claude"

[[command.toggles]]
name = "fast"
label = "Fast"
args_when_on = ["--fast"]
group = "mode"

[[command.toggles]]
name = "slow"
label = "Slow"
args_when_on = ["--slow"]
group = "mode"

[[command.toggles]]
name = "verbose"
label = "Verbose"
args_when_on = ["--verbose"]

[persistence]
strategy = "none"
`

// ac8SSH manages saved instances. Not an "ai" plugin: the daemon refuses
// instance arguments for one, since they would replace an agent's own.
const ac8SSH = `
[plugin]
name = "ac8-ssh"
display_name = "AC8 SSH"
category = "remote"

[command]
cmd = "cat"
arg_template = ["--host", "{host}"]

[[command.form_fields]]
name = "name"
label = "Name"
required = true

[[command.form_fields]]
name = "host"
label = "Host"
required = true

[persistence]
strategy = "none"
`

const ac8Kube = `
[plugin]
name = "ac8-kube"
display_name = "AC8 Kube"
category = "tools"

[command]
cmd = "cat"
discover = "kube"

[persistence]
strategy = "none"
`

type ac8 struct {
	t    *testing.T
	h    *authHarness
	w    *webTab
	tab  string
	pane string // the split target
	n    int
}

func newAC8(t *testing.T) *ac8 {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test")
	}
	h := webHarness(t)
	// One plugins directory serves both sides, as on one machine: the daemon
	// spawns from it, the gateway lists and expands from it (spec 5b E7).
	// The shipped plugins are written beside the test's, since a load prunes
	// every plugin without a file.
	plugins := filepath.Join(h.home, "ac8-plugins")
	if _, err := plugin.EnsureDefaultPlugins(plugins); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"ac8-agent.toml": ac8Plugin, "ac8-ssh.toml": ac8SSH, "ac8-kube.toml": ac8Kube} {
		if err := os.WriteFile(filepath.Join(plugins, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.d.registry.LoadFromDir(plugins); err != nil {
		t.Fatal(err)
	}
	inst := filepath.Join(h.home, "instances.json")
	if err := instances.Save(inst, instances.Store{
		"ac8-ssh": {{ID: "inst0001", Name: "box", Fields: map[string]string{"name": "box", "host": "h.example"}}},
	}); err != nil {
		t.Fatal(err)
	}
	// Docker is never asked: an unusable engine refuses the SPAWN of a
	// sandbox pane (SpawnError), after the pane holds its configuration.
	prevProbe := sandboxProbeFn
	sandboxProbeFn = func(context.Context) (sandbox.Info, error) { return sandbox.Info{}, nil }
	t.Cleanup(func() { sandboxProbeFn = prevProbe })

	r := newWebRigConfig(t, webgw.Config{
		Dial: func(ctx context.Context, clientID string) (webgw.DaemonConn, string, error) {
			c, err := ipc.NewClient(h.sock)
			if err != nil {
				return nil, "", err
			}
			return c, ipc.RightsFull, nil
		},
		PluginsDir:    plugins,
		InstancesPath: inst,
	})
	w := r.open(t)
	w.attach()
	a := &ac8{t: t, h: h, w: w}
	a.tab = h.d.session.CreateTab("ac8").ID
	// The target pane is made by the page too, with no target: the tab's
	// first leaf.
	resp, _ := a.splitRaw(map[string]any{"tab_id": a.tab, "placement": "right", "pane": map[string]any{"cwd": t.TempDir()}})
	if resp.Error != "" || resp.PaneID == "" {
		t.Fatalf("seed pane: %+v", resp)
	}
	a.pane = resp.PaneID
	return a
}

// splitRaw sends one split_pane_req as the page does and returns the answer,
// or the gateway's refusal text (an error envelope with the request id).
func (a *ac8) splitRaw(req map[string]any) (ipc.SplitPaneRespPayload, string) {
	a.t.Helper()
	a.n++
	id := fmt.Sprintf("ac8-%d", a.n)
	a.w.send(ipc.MsgSplitPaneReq, id, req)
	var got *ipc.Message
	a.w.until("the answer to "+id, func(f webFrame) bool {
		a.w.ack(f)
		if f.msg != nil && f.msg.ID == id && (f.msg.Type == ipc.MsgSplitPaneResp || f.msg.Type == ipc.MsgError) {
			got = f.msg
			return true
		}
		return false
	})
	if got.Type == ipc.MsgError {
		var e ipc.ErrorPayload
		_ = got.DecodePayload(&e)
		if e.Message == "" {
			e.Message = "refused: " + e.Code
		}
		return ipc.SplitPaneRespPayload{}, e.Message
	}
	var resp ipc.SplitPaneRespPayload
	if err := json.Unmarshal(got.Payload, &resp); err != nil {
		a.t.Fatal(err)
	}
	return resp, ""
}

// split places a pane right of the target and returns its state as the
// daemon broadcasts it, the answer's reason (a refusal's error, or for a
// created pane its notice, which carries a spawn error), and whether a pane
// was created.
func (a *ac8) split(pane map[string]any) (ipc.PaneState, string, bool) {
	a.t.Helper()
	resp, refused := a.splitRaw(map[string]any{"target_pane_id": a.pane, "placement": "right", "pane": pane})
	if refused != "" {
		return ipc.PaneState{}, refused, false
	}
	if resp.PaneID == "" {
		return ipc.PaneState{}, resp.Error, false
	}
	if resp.Error != "" {
		a.t.Fatalf("pane %s created with error %q: a created pane's problem is a notice, or the page reads it as refused", resp.PaneID, resp.Error)
	}
	return a.state(resp.PaneID), resp.Notice, true
}

func (a *ac8) state(id string) ipc.PaneState {
	a.t.Helper()
	for _, p := range a.h.d.buildWorkspaceState().Panes {
		if p.ID == id {
			return p
		}
	}
	a.t.Fatalf("pane %s not in the state", id)
	return ipc.PaneState{}
}

func (a *ac8) paneCount() int { return len(a.h.d.buildWorkspaceState().Panes) }

func TestAC8_BrowserDialogOptionsReachTheSpawnedPane(t *testing.T) {
	a := newAC8(t)
	cwd := t.TempDir()

	t.Run("type and folder", func(t *testing.T) {
		p, why, ok := a.split(map[string]any{"type": "ac8-agent", "cwd": cwd})
		if !ok || why != "" || p.Type != "ac8-agent" || p.CWD != cwd {
			t.Fatalf("pane %+v %q", p, why)
		}
	})
	t.Run("toggles by name", func(t *testing.T) {
		p, why, ok := a.split(map[string]any{"type": "ac8-agent", "cwd": cwd, "toggles": []string{"slow", "verbose"}})
		if !ok || why != "" || strings.Join(p.InstanceArgs, " ") != "--slow --verbose" {
			t.Fatalf("args %v %q", p.InstanceArgs, why)
		}
	})
	t.Run("two toggles of one group are refused, nothing created", func(t *testing.T) {
		before := a.paneCount()
		if _, why, ok := a.split(map[string]any{"type": "ac8-agent", "cwd": cwd, "toggles": []string{"fast", "slow"}}); ok || why == "" {
			t.Fatalf("not refused: created=%v %q", ok, why)
		}
		if after := a.paneCount(); after != before {
			t.Fatalf("panes %d -> %d", before, after)
		}
	})
	t.Run("saved instance: args from disk, page args stripped", func(t *testing.T) {
		p, why, ok := a.split(map[string]any{"type": "ac8-ssh", "cwd": cwd, "instance_id": "inst0001", "instance_args": []string{"--evil"}})
		if !ok || why != "" || strings.Join(p.InstanceArgs, " ") != "--host h.example" || p.InstanceName != "box" {
			t.Fatalf("pane %+v %q", p, why)
		}
	})
	t.Run("page args without an instance never reach the daemon", func(t *testing.T) {
		p, why, ok := a.split(map[string]any{"type": "ac8-ssh", "cwd": cwd, "instance_args": []string{"--evil"}})
		if !ok || why != "" || len(p.InstanceArgs) != 0 {
			t.Fatalf("pane %+v %q", p, why)
		}
	})
	t.Run("an unknown instance id is refused, nothing created", func(t *testing.T) {
		before := a.paneCount()
		if _, why, ok := a.split(map[string]any{"type": "ac8-ssh", "cwd": cwd, "instance_id": "nope"}); ok || why == "" {
			t.Fatalf("not refused: created=%v %q", ok, why)
		}
		if after := a.paneCount(); after != before {
			t.Fatalf("panes %d -> %d", before, after)
		}
	})
	t.Run("kube context", func(t *testing.T) {
		p, why, ok := a.split(map[string]any{"type": "ac8-kube", "cwd": cwd, "kube_context": "prod-1"})
		if !ok || why != "" || strings.Join(p.InstanceArgs, " ") != "--context prod-1" {
			t.Fatalf("args %v %q", p.InstanceArgs, why)
		}
	})
	t.Run("resume session", func(t *testing.T) {
		sid := "0d4c2f9e-1b7a-4c3e-9f5d-2a6b8c0e1f3a"
		prev := transcriptExistsFn
		transcriptExistsFn = func(p string) (bool, bool) { return strings.HasSuffix(p, sid+".jsonl"), true }
		t.Cleanup(func() { transcriptExistsFn = prev })
		p, why, ok := a.split(map[string]any{"type": "ac8-agent", "cwd": cwd, "resume_session_id": sid})
		if !ok || why != "" || p.PluginState["resume_session_id"] != sid {
			t.Fatalf("pane %+v %q", p, why)
		}
	})
	t.Run("existing worktree via existing_path; new branch via branch (R-A)", func(t *testing.T) {
		repo := worktreeRepo(t)
		stubAdd(t, func(_ context.Context, _, path, _ string) error { return os.MkdirAll(path, 0o755) })
		wt := filepath.Join(filepath.Dir(repo), "repo-existing-wt")
		if err := os.MkdirAll(wt, 0o755); err != nil {
			t.Fatal(err)
		}
		// cwd is deliberately the MAIN checkout: the pane must open in the
		// worktree because of existing_path, so dropping the field fails this.
		p, why, ok := a.split(map[string]any{"type": "ac8-agent", "cwd": repo, "worktree": map[string]any{"existing_path": wt}})
		if !ok || why != "" || p.CWD != wt {
			t.Fatalf("existing: %+v %q", p, why)
		}
		resp, refused := a.splitRaw(map[string]any{"target_pane_id": a.pane, "placement": "right",
			"pane": map[string]any{"type": "ac8-agent", "cwd": repo, "worktree": map[string]any{"branch": "ac8-new"}}})
		if refused != "" || resp.Error != "" || !resp.Preparing {
			t.Fatalf("new branch answer %+v %q, want preparing", resp, refused)
		}
		waitUntil(t, "a pane in the new worktree", func() bool {
			for _, p := range a.h.d.buildWorkspaceState().Panes {
				if p.WorktreeOwned && p.Type == "ac8-agent" && strings.HasSuffix(p.CWD, "ac8-new") {
					return true
				}
			}
			return false
		})
	})
	t.Run("sandbox image, sign-in and config", func(t *testing.T) {
		p, why, ok := a.split(map[string]any{"type": "ac8-agent", "cwd": cwd,
			"sandbox": map[string]any{"image": "localhost/quil-ac8:none", "auth": "browser", "claude_config": "shared"}})
		// The engine is unusable here, so the spawn itself is refused — with
		// the pane in its slot carrying the configuration it was asked for,
		// never a host process (its type stays the sandboxed plugin's).
		if !ok || p.SandboxImage != "localhost/quil-ac8:none" || p.SandboxAuth == nil || *p.SandboxAuth != "browser" ||
			p.SandboxClaudeConfig == nil || *p.SandboxClaudeConfig != config.SandboxClaudeConfigShared {
			t.Fatalf("pane %+v %q", p, why)
		}
		if !strings.Contains(why, "sandbox") {
			t.Fatalf("answer %q, want the sandbox spawn refusal", why)
		}
	})
	t.Run("sandbox token sign-in", func(t *testing.T) {
		p, _, ok := a.split(map[string]any{"type": "ac8-agent", "cwd": cwd,
			"sandbox": map[string]any{"image": "localhost/quil-ac8:none", "auth": "token", "claude_config": "own"}})
		if !ok || p.SandboxAuth == nil || *p.SandboxAuth != "token" || p.SandboxClaudeConfig == nil || *p.SandboxClaudeConfig != "own" {
			t.Fatalf("pane %+v", p)
		}
	})
	t.Run("an invalid sandbox is refused and never falls back to a host pane", func(t *testing.T) {
		before := a.paneCount()
		if _, why, ok := a.split(map[string]any{"type": "ac8-agent", "cwd": cwd, "sandbox": map[string]any{"image": "not an image!"}}); ok || why == "" {
			t.Fatalf("not refused: created=%v %q", ok, why)
		}
		if after := a.paneCount(); after != before {
			t.Fatalf("panes %d -> %d: a host pane was created", before, after)
		}
	})
	t.Run("new tab from the dialog", func(t *testing.T) {
		resp, refused := a.splitRaw(map[string]any{"placement": "new_tab", "new_tab": map[string]any{"name": "dlg"},
			"pane": map[string]any{"type": "ac8-agent", "cwd": cwd, "toggles": []string{"fast"}}})
		if refused != "" || resp.Error != "" || resp.PaneID == "" || resp.TabID == a.tab {
			t.Fatalf("new tab %+v %q", resp, refused)
		}
		if p := a.state(resp.PaneID); p.Type != "ac8-agent" || p.CWD != cwd || strings.Join(p.InstanceArgs, " ") != "--fast" {
			t.Fatalf("pane %+v", p)
		}
	})
}
