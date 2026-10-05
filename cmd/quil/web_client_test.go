package main

import (
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
