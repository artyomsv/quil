package webgw

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/instances"
)

func TestServer_ExpandInstance_ReadsBothFilesFromDisk(t *testing.T) {
	dir := t.TempDir()
	plugins := filepath.Join(dir, "plugins")
	if err := os.MkdirAll(plugins, 0o755); err != nil {
		t.Fatal(err)
	}
	// form_fields make it an instance-managing plugin; Task 7's catalog
	// lookup refuses a plugin without them, and this test must survive that.
	toml := "[plugin]\nname = \"x-ssh\"\ndisplay_name = \"X\"\ncategory = \"remote\"\n\n[command]\ncmd = \"ssh\"\narg_template = [\"{user}@{host}\"]\n\n" +
		"[[command.form_fields]]\nname = \"user\"\nlabel = \"User\"\n\n[[command.form_fields]]\nname = \"host\"\nlabel = \"Host\"\nrequired = true\n"
	if err := os.WriteFile(filepath.Join(plugins, "x-ssh.toml"), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	inst := filepath.Join(dir, "instances.json")
	if err := instances.Save(inst, instances.Store{"x-ssh": {{ID: "i1", Name: "box", Fields: map[string]string{"user": "u", "host": "h"}}}}); err != nil {
		t.Fatal(err)
	}
	s := New(Config{PluginsDir: plugins, InstancesPath: inst, Version: "test"})
	name, args, err := s.expandInstance("x-ssh", "i1")
	if err != nil || name != "box" || !reflect.DeepEqual(args, []string{"u@h"}) {
		t.Fatalf("expand: %q %v %v", name, args, err)
	}
	if _, _, err := s.expandInstance("x-ssh", "nope"); err == nil {
		t.Fatal("unknown id expanded")
	}
	if _, _, err := s.expandInstance("not-a-plugin", "i1"); err == nil {
		t.Fatal("unknown plugin expanded")
	}
}

// The expander reads plugin definitions through the catalog, the same source
// /api/client lists: a plugin added after start is seen by both (fingerprint
// reload), and a plugin with no form fields — which the dialog never offers
// instances for — is never expanded.
func TestServer_ExpandInstance_UsesTheCatalog(t *testing.T) {
	dir := t.TempDir()
	plugins := filepath.Join(dir, "plugins")
	if err := os.MkdirAll(plugins, 0o755); err != nil {
		t.Fatal(err)
	}
	inst := filepath.Join(dir, "instances.json")
	if err := instances.Save(inst, instances.Store{
		"late-ssh": {{ID: "i1", Name: "box", Fields: map[string]string{"host": "h"}}},
		"no-form":  {{ID: "i2", Name: "x", Fields: map[string]string{"host": "h"}}},
	}); err != nil {
		t.Fatal(err)
	}
	s := New(Config{PluginsDir: plugins, InstancesPath: inst, Version: "test"})
	if _, _, err := s.expandInstance("late-ssh", "i1"); err == nil {
		t.Fatal("expanded a plugin that does not exist yet")
	}
	late := "[plugin]\nname = \"late-ssh\"\ndisplay_name = \"L\"\ncategory = \"remote\"\n\n[command]\ncmd = \"ssh\"\narg_template = [\"{host}\"]\n\n[[command.form_fields]]\nname = \"host\"\nlabel = \"Host\"\n"
	noForm := "[plugin]\nname = \"no-form\"\ndisplay_name = \"N\"\ncategory = \"tools\"\n\n[command]\ncmd = \"true\"\narg_template = [\"{host}\"]\n"
	for name, body := range map[string]string{"late-ssh.toml": late, "no-form.toml": noForm} {
		if err := os.WriteFile(filepath.Join(plugins, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, args, err := s.expandInstance("late-ssh", "i1"); err != nil || len(args) != 1 || args[0] != "h" {
		t.Fatalf("late plugin: %v %v", args, err)
	}
	if _, _, err := s.expandInstance("no-form", "i2"); err == nil {
		t.Fatal("expanded a plugin that manages no instances")
	}
	// A tab's gate holds the same expander: a bridge built from the server's
	// limits expands through the catalog too, on both of its paths.
	b := newBridge(newFakeDaemon(), s.limits, s.budget, time.Now, func(string, ...any) {})
	b.setLease("web-x-1")
	b.gateMu.Lock()
	expand := b.gate.expand
	b.gateMu.Unlock()
	if expand == nil {
		t.Fatal("the gate has no expander")
	}
	if _, args, err := expand("late-ssh", "i1"); err != nil || len(args) != 1 || args[0] != "h" {
		t.Fatalf("gate expander: %v %v", args, err)
	}
	if _, _, err := expand("no-form", "i2"); err == nil {
		t.Fatal("the gate expanded a plugin that manages no instances")
	}
}

// An unreadable instances.json is refused in fixed text: the os error names
// an absolute path on the gateway's machine, which goes to web.log only.
func TestServer_ExpandInstance_UnreadableFileNamesNoPath(t *testing.T) {
	dir := t.TempDir()
	plugins := filepath.Join(dir, "plugins")
	if err := os.MkdirAll(plugins, 0o755); err != nil {
		t.Fatal(err)
	}
	toml := "[plugin]\nname = \"x-ssh\"\ndisplay_name = \"X\"\ncategory = \"remote\"\n\n[command]\ncmd = \"ssh\"\narg_template = [\"{host}\"]\n\n[[command.form_fields]]\nname = \"host\"\nlabel = \"Host\"\n"
	if err := os.WriteFile(filepath.Join(plugins, "x-ssh.toml"), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	inst := filepath.Join(dir, "instances.json")
	if err := os.WriteFile(inst, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	var logged []string
	s := New(Config{PluginsDir: plugins, InstancesPath: inst, Version: "test",
		Logf: func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }})
	_, _, err := s.expandInstance("x-ssh", "i1")
	if !errors.Is(err, errInstancesUnreadable) {
		t.Fatalf("err = %v, want errInstancesUnreadable", err)
	}
	if strings.Contains(err.Error(), dir) || strings.Contains(err.Error(), "instances.json") {
		t.Fatalf("refusal text names a path: %q", err)
	}
	if len(logged) == 0 {
		t.Fatal("the cause was not logged")
	}
}
