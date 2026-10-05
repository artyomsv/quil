package webgw

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testPluginTOML = `
[plugin]
name = "e2e-ssh"
display_name = "E2E SSH"
category = "remote"

[command]
cmd = "cat"
args = ["--secret-arg"]
env = ["SECRET_ENV=1"]
arg_template = ["-p", "{port}", "{user}@{host}"]
raw_keys = ["shift+tab"]

[[command.form_fields]]
name = "name"
label = "Name"
required = true

[[command.form_fields]]
name = "host"
label = "Host"
required = true

[[command.form_fields]]
name = "user"
label = "User"

[[command.form_fields]]
name = "port"
label = "Port"
default = "22"

[[command.toggles]]
name = "a"
label = "A"
args_when_on = ["--a"]
group = "g"

[[command.toggles]]
name = "b"
label = "B"
args_when_on = ["--b"]
group = "g"
`

func writePlugin(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func findDef(defs []PluginDef, name string) *PluginDef {
	for i := range defs {
		if defs[i].Name == name {
			return &defs[i]
		}
	}
	return nil
}

func TestCatalog_ListsBuiltinsAndTOMLWithoutCommands(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "e2e-ssh.toml", testPluginTOML)
	c := newCatalog(dir)
	defs := c.plugins()
	if findDef(defs, "terminal") == nil {
		t.Fatal("the built-in terminal is not listed")
	}
	got := findDef(defs, "e2e-ssh")
	if got == nil {
		t.Fatal("e2e-ssh not listed")
	}
	if len(got.FormFields) != 4 || got.FormFields[3].Default != "22" || !got.FormFields[0].Required {
		t.Fatalf("form fields = %+v", got.FormFields)
	}
	if len(got.Toggles) != 2 || got.Toggles[1].Group != "g" {
		t.Fatalf("toggles = %+v", got.Toggles)
	}
	if len(got.RawKeys) != 1 || got.RawKeys[0] != "shift+tab" {
		t.Fatalf("raw keys = %v", got.RawKeys)
	}
	if args, ok := c.argTemplate("e2e-ssh"); !ok || len(args) != 3 {
		t.Fatalf("arg template = %v %v", args, ok)
	}
	// The page never sees a command, its arguments, environment or template.
	raw, _ := json.Marshal(defs)
	for _, secret := range []string{`"cat"`, `"--secret-arg"`, `"SECRET_ENV=1"`, `"-p"`, `"--a"`} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("plugin list leaks %q: %s", secret, raw)
		}
	}
}

func TestCatalog_NoInstancesWithoutFormFields(t *testing.T) {
	c := newCatalog(t.TempDir())
	if _, ok := c.argTemplate("terminal"); ok {
		t.Fatal("terminal manages no instances")
	}
	if c.formFieldNames("terminal") != nil || c.formFieldNames("nope") != nil {
		t.Fatal("field names for a plugin without a form")
	}
}

func TestCatalog_ReloadsWhenThePluginFilesChange(t *testing.T) {
	dir := t.TempDir()
	c := newCatalog(dir)
	has := func() bool { return findDef(c.plugins(), "e2e-ssh") != nil }
	if has() {
		t.Fatal("listed before the file exists")
	}
	writePlugin(t, dir, "e2e-ssh.toml", testPluginTOML)
	future := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(filepath.Join(dir, "e2e-ssh.toml"), future, future)
	if !has() {
		t.Fatal("not reloaded after a new TOML appeared")
	}
	if err := os.Remove(filepath.Join(dir, "e2e-ssh.toml")); err != nil {
		t.Fatal(err)
	}
	if has() {
		t.Fatal("still listed after its TOML was removed")
	}
}
