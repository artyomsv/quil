package plugin

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The web gateway loads a throwaway registry per saved-instance expansion;
// the quiet load must still load, and write no per-plugin progress lines.
func TestLoadFromDirQuiet_LoadsWithoutProgressLines(t *testing.T) {
	dir := t.TempDir()
	toml := "[plugin]\nname = \"quiet-probe\"\n\n[command]\ncmd = \"ssh\"\narg_template = [\"{host}\"]\n"
	if err := os.WriteFile(filepath.Join(dir, "quiet-probe.toml"), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	orig, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	r := NewRegistry()
	err := r.LoadFromDirQuiet(dir)
	log.SetOutput(orig)
	log.SetFlags(flags)
	if err != nil {
		t.Fatal(err)
	}
	if p := r.Get("quiet-probe"); p == nil || len(p.Command.ArgTemplate) != 1 {
		t.Fatalf("quiet load did not load the plugin: %+v", p)
	}
	if strings.Contains(buf.String(), "quiet-probe") {
		t.Fatalf("quiet load logged: %s", buf.String())
	}
}
