package plugin

import (
	"os"
	"path/filepath"
	"testing"
)

// wide_canvas ([display] section) marks pane types whose PTY/emulator stay
// window-sized regardless of the pane's layout rect (the TUI renders a
// soft-wrapped preview in small rects). Shipped enabled for claude-code and
// opencode; everything else defaults to 1:1 sizing.

func writePluginTOML(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "p.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write plugin toml: %v", err)
	}
	return path
}

func TestLoadPluginTOML_WideCanvasFlag(t *testing.T) {
	path := writePluginTOML(t, `
[plugin]
name = "wc-test"
schema_version = 1

[command]
cmd = "true"

[display]
wide_canvas = true
`)
	p, err := loadPluginTOML(path)
	if err != nil {
		t.Fatalf("loadPluginTOML: %v", err)
	}
	if !p.Display.WideCanvas {
		t.Error("wide_canvas = true not parsed into DisplayConfig.WideCanvas")
	}
}

func TestLoadPluginTOML_WideCanvasDefaultFalse(t *testing.T) {
	path := writePluginTOML(t, `
[plugin]
name = "wc-default"
schema_version = 1

[command]
cmd = "true"
`)
	p, err := loadPluginTOML(path)
	if err != nil {
		t.Fatalf("loadPluginTOML: %v", err)
	}
	if p.Display.WideCanvas {
		t.Error("WideCanvas must default to false")
	}
}

func TestDefaultPlugins_AgentPluginsShipWideCanvas(t *testing.T) {
	for _, name := range []string{"claude-code", "opencode"} {
		data, err := defaultPlugins.ReadFile("defaults/" + name + ".toml")
		if err != nil {
			t.Fatalf("read embedded default %s: %v", name, err)
		}
		path := writePluginTOML(t, string(data))
		p, err := loadPluginTOML(path)
		if err != nil {
			t.Fatalf("parse embedded default %s: %v", name, err)
		}
		if !p.Display.WideCanvas {
			t.Errorf("%s default must ship wide_canvas = true", name)
		}
	}
}
