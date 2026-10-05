package main

import (
	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/tui"
	"github.com/artyomsv/quil/internal/webgw"
)

// webClientExtras is the config-derived part of GET /api/client. The config
// is the one quil web loaded at start; the remembered sandbox image is read
// per request, so an image the TUI saved meanwhile pre-fills the next dialog.
// Task 8 adds the keymap and the notification filter here.
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
		return webgw.ClientExtras{SandboxSignIn: cfg.Sandbox.DefaultSignIn(), SandboxImage: img}
	}
}
