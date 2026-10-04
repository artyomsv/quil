package webgw

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

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
	s := &Server{cfg: Config{PluginsDir: plugins, InstancesPath: inst}}
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
