package logger

import (
	"bytes"
	"log"
	"log/slog"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug":   slog.LevelDebug,
		"DEBUG":   slog.LevelDebug,
		" debug ": slog.LevelDebug,
		"info":    slog.LevelInfo,
		"INFO":    slog.LevelInfo,
		"":        slog.LevelInfo, // unknown -> info
		"warn":    slog.LevelWarn,
		"warning": slog.LevelWarn,
		"err":     slog.LevelError,
		"error":   slog.LevelError,
		"garbage": slog.LevelInfo, // unknown -> info
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			if got := ParseLevel(in); got != want {
				t.Errorf("ParseLevel(%q) = %v, want %v", in, got, want)
			}
		})
	}
}

func TestInit_FiltersByLevel(t *testing.T) {
	var buf bytes.Buffer
	Init("warn", &buf)

	// All four log lines flow through the same handler. With level=warn,
	// debug and info should be dropped; warn and error should be kept.
	Debug("this is a debug line")
	Info("this is an info line")
	Warn("this is a warn line")
	Error("this is an error line")

	out := buf.String()
	if strings.Contains(out, "debug line") {
		t.Errorf("expected debug to be filtered out at warn level, got: %q", out)
	}
	if strings.Contains(out, "info line") {
		t.Errorf("expected info to be filtered out at warn level, got: %q", out)
	}
	if !strings.Contains(out, "warn line") {
		t.Errorf("expected warn line to appear, got: %q", out)
	}
	if !strings.Contains(out, "error line") {
		t.Errorf("expected error line to appear, got: %q", out)
	}
}

func TestInit_BridgesStdlibLogAtInfoLevel(t *testing.T) {
	var buf bytes.Buffer
	Init("info", &buf)

	// Existing log.Printf call sites should be routed through slog at info
	// level. They should appear in the output when level=info.
	log.Printf("legacy stdlib log call: value=%d", 42)

	out := buf.String()
	if !strings.Contains(out, "legacy stdlib log call: value=42") {
		t.Errorf("expected legacy log.Printf to bridge through slog, got: %q", out)
	}
}

func TestInit_StdlibLogDroppedBelowLevel(t *testing.T) {
	var buf bytes.Buffer
	Init("warn", &buf)

	// At warn level, stdlib log.Printf (which bridges as info) should be filtered.
	log.Printf("dropped legacy line")

	out := buf.String()
	if strings.Contains(out, "dropped legacy line") {
		t.Errorf("expected legacy log.Printf to be filtered at warn level, got: %q", out)
	}
}

func TestDebug_BeforeInit_NoOp(t *testing.T) {
	// Reset to nil to simulate "before Init"
	mu.Lock()
	sl = nil
	mu.Unlock()
	defer func() {
		// Re-init to a discard sink so subsequent tests have a working logger
		Init("info", &bytes.Buffer{})
	}()

	// These should not panic.
	Debug("nothing")
	Info("nothing")
	Warn("nothing")
	Error("nothing")
}
