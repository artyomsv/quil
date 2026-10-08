package main

import (
	"log"

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
		tpls, terr := webTemplates()
		return webgw.ClientExtras{
			SandboxSignIn:  cfg.Sandbox.DefaultSignIn(),
			SandboxImage:   img,
			Keymap:         webKeymap(cfg),
			Notifications:  webNotify(cfg),
			Templates:      tpls,
			TemplatesError: terr,
			Connect:        connect,
		}
	}
}

// webTemplatesError is the page's text for a templates.toml that does not
// read; the detail (it can name an absolute path) goes to web.log.
const webTemplatesError = "templates.toml does not read; fix it in the TUI (F1 → Settings → Templates)"

// webTemplates lists the templates the TUI's dialog offers, from the same
// file (the embedded defaults when it is absent). Read per request: a
// template saved in the TUI meanwhile is offered at the next open.
func webTemplates() ([]webgw.TemplateDef, string) {
	t, err := config.LoadTemplates()
	if err != nil {
		log.Printf("templates.toml: %v", err)
		return nil, webTemplatesError
	}
	out := make([]webgw.TemplateDef, 0, len(t.Templates))
	for _, tpl := range t.Templates {
		out = append(out, webgw.TemplateDef{Name: tpl.Name, Description: tpl.Description})
	}
	return out, ""
}

// webNotifyInfo is what the page needs to file pane events as the TUI does.
type webNotifyInfo struct {
	Shown         map[string]bool   `json:"shown"`
	HookGroups    map[string]string `json:"hook_groups"`
	PlainGroups   map[string]string `json:"plain_groups"`
	DefaultGroup  string            `json:"default_group"`
	WorkStateOnly []string          `json:"work_state_only"`
}

// bindingsUnreadableNotice is the conflict line the page shows for a
// bindings.toml that does not read.
const bindingsUnreadableNotice = "bindings.toml is unreadable; using config.toml"

// webKeymap resolves the keymap with the decision cmd/quil/main.go makes for
// the TUI (config.ActiveBindings): bindings.toml when it exists and reads,
// else the legacy [keybindings] table. Read per request, so a preset
// switched in F1 reaches the page at its next load. quil web never migrates
// the table itself: writing the user's keymap file is the TUI's job.
func webKeymap(cfg config.Config) keymap.WebKeymap {
	b, legacy, err := config.ActiveBindings()
	if !legacy {
		return keymap.ForWeb(keymap.FromSettings(b.Settings()))
	}
	km, conflicts := config.LegacyKeymap(cfg.Keybindings)
	w := keymap.ForWeb(keymap.Resolved{Keymap: km, Conflicts: conflicts, Preset: keymap.DefaultPresetName})
	// The page gets fixed text: the error names an absolute path on this
	// machine, which a browser on another one has no use for. The detail goes
	// to web.log.
	if err != nil {
		log.Printf("bindings.toml unreadable: %v", err)
		w.Conflicts = append([]string{bindingsUnreadableNotice}, w.Conflicts...)
	}
	return w
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
