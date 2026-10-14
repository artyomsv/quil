package config_test

import (
	"testing"

	"github.com/artyomsv/quil/internal/config"
)

func TestMergeChanged(t *testing.T) {
	launch := config.Default()
	current := config.Clone(launch)
	current.UI.SidebarWidth = 40                                               // outside the table's dialogs' usual path: still carries
	current.UI.UnfocusedDim = 0.35                                             // a float64 leaf
	current.Remote.Hosts = map[string]config.RemoteHost{"box": {Binary: "/q"}} // a map carries whole

	fresh := config.Default()
	fresh.UI.ScrollbackLines = 777 // the browser's change, untouched by the TUI
	fresh.Remote.Hosts = map[string]config.RemoteHost{"other": {Binary: "/o"}}

	config.MergeChanged(&fresh, launch, current)
	if fresh.UI.SidebarWidth != 40 || fresh.UI.UnfocusedDim != 0.35 {
		t.Fatalf("TUI changes lost: %+v", fresh.UI)
	}
	if fresh.UI.ScrollbackLines != 777 {
		t.Fatal("an untouched field overwrote the fresh value")
	}
	if len(fresh.Remote.Hosts) != 1 || fresh.Remote.Hosts["box"].Binary != "/q" {
		t.Fatalf("map leaf: %v", fresh.Remote.Hosts)
	}
	current.Remote.Hosts["box"] = config.RemoteHost{Binary: "/changed"}
	if fresh.Remote.Hosts["box"].Binary != "/q" {
		t.Fatal("fresh shares the map with current")
	}
}

func TestMergeChanged_ToggleBackKeepsFresh(t *testing.T) {
	launch := config.Default()
	current := config.Clone(launch) // the TUI flipped a value and flipped it back
	fresh := config.Default()
	fresh.Update.Auto = !launch.Update.Auto
	config.MergeChanged(&fresh, launch, current)
	if fresh.Update.Auto == launch.Update.Auto {
		t.Fatal("a toggle-back overwrote the browser's value")
	}
}

func TestMergeChanged_UntouchedRemoteSurvives(t *testing.T) {
	launch := config.Default()
	current := config.Clone(launch)
	current.UI.ScrollbackLines = 5000
	fresh := config.Default()
	fresh.Remote.Hosts = map[string]config.RemoteHost{"gpu": {Binary: "/home/u/.local/bin/quil"}}
	config.MergeChanged(&fresh, launch, current)
	if fresh.Remote.Hosts["gpu"].Binary == "" {
		t.Fatal("[remote] recorded behind the TUI's back was erased")
	}
}

func TestMergeChanged_SliceIsCopied(t *testing.T) {
	launch := config.Default()
	current := config.Clone(launch)
	current.Destinations = []config.Destination{{Dest: "a"}}
	fresh := config.Default()
	config.MergeChanged(&fresh, launch, current)
	current.Destinations[0].Dest = "mutated"
	if fresh.Destinations[0].Dest != "a" {
		t.Fatal("fresh shares the slice with current")
	}
}

func TestClone_DoesNotShareReferences(t *testing.T) {
	c := config.Default()
	c.Destinations = []config.Destination{{Dest: "a"}}
	c.Remote.Hosts = map[string]config.RemoteHost{"a": {Binary: "/a"}}
	d := config.Clone(c)
	c.Destinations[0].Dest = "x"
	c.Remote.Hosts["a"] = config.RemoteHost{Binary: "/x"}
	if d.Destinations[0].Dest != "a" || d.Remote.Hosts["a"].Binary != "/a" {
		t.Fatal("Clone shares memory")
	}
}
