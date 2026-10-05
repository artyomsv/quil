package main

import (
	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/hookevents"
	"github.com/artyomsv/quil/internal/keymap"
	"github.com/artyomsv/quil/internal/notifyclass"
	"github.com/artyomsv/quil/internal/tui"
	"github.com/artyomsv/quil/internal/webgw"
)

// webClientExtras is the config-derived part of GET /api/client. The config
// is the one quil web loaded at start; the remembered sandbox image and the
// keymap are read per request, so an image the TUI saved meanwhile pre-fills
// the next dialog and a preset switched in F1 reaches the next page load.
//
// With --connect the remembered image belongs to the LOCAL daemon's dialog
// (config.SandboxImagePath("") is the local destination's file), not to the
// host the browser creates panes on, so only the config default is offered.
// The browser never writes that file (Task 6/7 rulings).
func webClientExtras(cfg config.Config, connect bool) func() webgw.ClientExtras {
	return func() webgw.ClientExtras {
		img := cfg.Sandbox.DefaultImage
		if !connect {
			if remembered := tui.LoadSandboxImage(config.SandboxImagePath("")); remembered != "" {
				img = remembered
			}
		}
		return webgw.ClientExtras{
			SandboxSignIn: cfg.Sandbox.DefaultSignIn(),
			SandboxImage:  img,
			Keymap:        webKeymap(cfg),
			Notifications: webNotify(cfg),
		}
	}
}

// webNotifyInfo is what the page needs to file pane events as the TUI does.
type webNotifyInfo struct {
	Shown         map[string]bool   `json:"shown"`
	HookGroups    map[string]string `json:"hook_groups"`
	PlainGroups   map[string]string `json:"plain_groups"`
	DefaultGroup  string            `json:"default_group"`
	WorkStateOnly []string          `json:"work_state_only"`
}

// webKeymap resolves the keymap the way cmd/quil/main.go does for the TUI:
// bindings.toml when it reads, else the legacy [keybindings] table. Read per
// request, so a preset switched in F1 reaches the page at its next load.
func webKeymap(cfg config.Config) keymap.WebKeymap {
	b, err := config.LoadBindings()
	if err != nil {
		km, conflicts := keymap.BuildLayered(keymap.DefaultLayer(), config.KeySpecsFromConfig(cfg.Keybindings))
		w := keymap.ForWeb(keymap.Resolved{Keymap: km, Conflicts: conflicts, Preset: keymap.DefaultPresetName})
		w.Conflicts = append([]string{"bindings.toml is unreadable; using config.toml: " + err.Error()}, w.Conflicts...)
		return w
	}
	return keymap.ForWeb(keymap.FromSettings(b.Settings()))
}

func webNotify(cfg config.Config) webNotifyInfo {
	return webNotifyInfo{
		Shown:         notifyclass.ShownGroups(cfg.Notification.Events),
		HookGroups:    notifyclass.HookGroups(),
		PlainGroups:   notifyclass.PlainGroups(),
		DefaultGroup:  notifyclass.DefaultGroup,
		WorkStateOnly: hookevents.WorkStateOnlyTypes(),
	}
}
