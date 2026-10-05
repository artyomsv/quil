package main

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/tui"
)

func TestWebClientExtras_SandboxDefaults(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	cfg := config.Default()
	cfg.Sandbox.DefaultImage = "img:1"
	cfg.Sandbox.SharedClaudeConfig = true
	ex := webClientExtras(cfg, false)()
	if ex.SandboxSignIn != "shared" || ex.SandboxImage != "img:1" {
		t.Fatalf("extras = %+v", ex)
	}
	// A remembered local image wins in local mode, read per request.
	if err := tui.SaveSandboxImage(config.SandboxImagePath(""), "img:2"); err != nil {
		t.Fatal(err)
	}
	if got := webClientExtras(cfg, false)().SandboxImage; got != "img:2" {
		t.Fatalf("local image = %q", got)
	}
	// --connect: the remembered file belongs to the local daemon; use the config.
	if got := webClientExtras(cfg, true)().SandboxImage; got != "img:1" {
		t.Fatalf("connect image = %q", got)
	}
	cfg.Sandbox.Auth = string(config.SandboxAuthToken)
	if got := webClientExtras(cfg, false)().SandboxSignIn; got != "token" {
		t.Fatalf("token config sign-in = %q", got)
	}
}

func TestWebClientInfo_KeymapFollowsBindingsFile(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	if err := os.WriteFile(config.BindingsPath(), []byte("preset = \"tmux\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w := webKeymap(config.Default())
	if w.Preset != "tmux" {
		t.Fatalf("preset = %q, want tmux", w.Preset)
	}
	found := false
	for _, a := range w.Actions {
		if a.ID == "pane.split_h" {
			found = true
			if !slices.Equal(a.Keys, []string{"ctrl+b %"}) {
				t.Errorf("pane.split_h = %v", a.Keys)
			}
		}
	}
	if !found {
		t.Error("pane.split_h missing")
	}
}

func TestWebClientInfo_UnreadableBindingsFallsBackToConfig(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	if err := os.WriteFile(config.BindingsPath(), []byte("[bindings\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w := webKeymap(config.Default())
	if len(w.Conflicts) == 0 || !strings.Contains(w.Conflicts[0], "unreadable") {
		t.Errorf("conflicts = %v, want the unreadable notice first", w.Conflicts)
	}
}

func TestWebClientInfo_NotificationTables(t *testing.T) {
	n := webNotify(config.Default())
	if n.DefaultGroup != "system" || n.HookGroups["Stop"] != "agent_turn" || n.Shown["commands"] {
		t.Errorf("notify info = %+v", n)
	}
	if !slices.Contains(n.WorkStateOnly, "hook.claude.PostToolUse") {
		t.Errorf("work_state_only = %v", n.WorkStateOnly)
	}
}

// The extras closure is what /api/client serves: both halves are filled.
func TestWebClientExtras_CarryKeymapAndNotifications(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	ex := webClientExtras(config.Default(), false)()
	data, err := json.Marshal(map[string]any{"keymap": ex.Keymap, "notifications": ex.Notifications})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"preset":"default"`, `"actions":[`, `"hook_groups":{`, `"work_state_only":[`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("extras JSON lacks %s: %s", want, data)
		}
	}
}
