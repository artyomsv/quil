package opencodehook

import (
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/hookevents"
)

// The plugin's regex literal is the Go pattern, byte for byte: two copies of
// one rule drift, and the JS copy is untestable from Go except by its text.
//
// Fails if either copy changes without the other.
func TestOpencodeScript_ProbeRegexMatchesGo(t *testing.T) {
	body, err := scripts.ReadFile("scripts/" + pluginScriptName)
	if err != nil {
		t.Fatal(err)
	}
	want := "/" + hookevents.WorktreeCommandPattern + "/i"
	if !strings.Contains(string(body), want) {
		t.Errorf("the plugin does not carry %s", want)
	}
	for _, s := range []string{`"tool.execute.after"`, `"WorktreeProbe"`} {
		if !strings.Contains(string(body), s) {
			t.Errorf("the plugin lacks %s", s)
		}
	}
	if hookevents.EventWorktreeProbe != "WorktreeProbe" {
		t.Errorf("the Go event name drifted from the plugin's: %q", hookevents.EventWorktreeProbe)
	}
}
