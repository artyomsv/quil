package main

import (
	"testing"

	"github.com/artyomsv/quil/internal/config"
)

// AC-3: a browser change written between launch and exit survives a TUI exit
// that changed a different setting; the TUI's change lands too.
func TestSaveExitConfig_KeepsAConcurrentChange(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	path := config.ConfigPath()
	launch := config.Default()
	if err := config.Save(path, launch); err != nil {
		t.Fatal(err)
	}
	// The browser saves while the TUI runs.
	if err := config.Mutate(path, func(c *config.Config) { c.UI.ScrollbackLines = 4242 }); err != nil {
		t.Fatal(err)
	}
	current := config.Clone(launch)
	current.Logging.Level = "debug"
	if err := saveExitConfig(path, launch, current); err != nil {
		t.Fatal(err)
	}
	got, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.UI.ScrollbackLines != 4242 || got.Logging.Level != "debug" {
		t.Fatalf("scrollback %d, level %q", got.UI.ScrollbackLines, got.Logging.Level)
	}
}

// A destination connected at runtime (written by dialdest.go at once)
// survives the exit save when the browser changed an unrelated row.
func TestSaveExitConfig_RuntimeDestinationSurvives(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	path := config.ConfigPath()
	launch := config.Default()
	if err := config.Save(path, launch); err != nil {
		t.Fatal(err)
	}
	current := config.Clone(launch)
	current.Destinations = []config.Destination{{Name: "gpu", Dest: "u@gpu"}}
	if err := config.Mutate(path, func(c *config.Config) { c.Destinations = current.Destinations }); err != nil {
		t.Fatal(err)
	}
	if err := config.Mutate(path, func(c *config.Config) { c.Update.Check = !c.Update.Check }); err != nil {
		t.Fatal(err)
	}
	if err := saveExitConfig(path, launch, current); err != nil {
		t.Fatal(err)
	}
	got, _ := config.Load(path)
	if len(got.Destinations) != 1 || got.Destinations[0].Dest != "u@gpu" || got.Update.Check == launch.Update.Check {
		t.Fatalf("destinations %v, check %v", got.Destinations, got.Update.Check)
	}
}
