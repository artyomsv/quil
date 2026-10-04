package instances

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoad_MissingIsEmptyMalformedIsAnError(t *testing.T) {
	dir := t.TempDir()
	s, err := Load(filepath.Join(dir, "none.json"))
	if err != nil || len(s) != 0 {
		t.Fatalf("missing file: %v %v", s, err)
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(bad); err == nil {
		t.Fatal("a malformed file must be an error, or a write would replace it with one entry")
	}
	if got := LoadOrEmpty(bad); len(got) != 0 {
		t.Fatalf("LoadOrEmpty(bad) = %v", got)
	}
}

func TestSaveLoad_RoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "instances.json")
	in := Store{"ssh": {{ID: "ab12cd34", Name: "box", Fields: map[string]string{"host": "h", "user": "u"}}}}
	if err := Save(p, in); err != nil {
		t.Fatal(err)
	}
	out, err := Load(p)
	if err != nil || !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip: %v %v", out, err)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 && os.PathSeparator == '/' {
		t.Fatalf("mode %v, want 0600", fi.Mode().Perm())
	}
}

func TestBuildArgs(t *testing.T) {
	got := BuildArgs([]string{"-p", "{port}", "{user}@{host}"}, map[string]string{"port": "22", "user": "u", "host": "h"})
	if !reflect.DeepEqual(got, []string{"-p", "22", "u@h"}) {
		t.Fatalf("BuildArgs = %v", got)
	}
	if BuildArgs(nil, nil) != nil {
		t.Fatal("no template, no args")
	}
}

// Expand matches the stable id, never the name: two instances may share one.
func TestExpand(t *testing.T) {
	s := Store{"ssh": {
		{ID: "i1", Name: "dup", Fields: map[string]string{"user": "u", "host": "a"}},
		{ID: "i2", Name: "dup", Fields: map[string]string{"user": "u", "host": "b"}},
	}}
	tmpl := []string{"{user}@{host}"}
	name, args, err := Expand(s, tmpl, "ssh", "i2")
	if err != nil || name != "dup" || !reflect.DeepEqual(args, []string{"u@b"}) {
		t.Fatalf("i2: %q %v %v", name, args, err)
	}
	if _, _, err := Expand(s, tmpl, "ssh", "nope"); err == nil {
		t.Fatal("an unknown id was expanded")
	}
	if _, _, err := Expand(s, tmpl, "stripe", "i1"); err == nil {
		t.Fatal("another plugin's instance was used")
	}
}
