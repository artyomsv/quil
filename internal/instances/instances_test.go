package instances

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
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

// A value is never expanded again, whatever the map order: "{host}" typed
// into the user field stays literal.
func TestBuildArgs_ValueIsNotReExpanded(t *testing.T) {
	fields := map[string]string{"user": "{host}", "host": "h", "a": "{b}", "b": "x"}
	for i := 0; i < 50; i++ {
		got := BuildArgs([]string{"{user}@{host}", "{a}{b}", "{none}", "{", "{{b}"}, fields)
		want := []string{"{host}@h", "{b}x", "{none}", "{", "{x"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("BuildArgs = %q, want %q", got, want)
		}
	}
}

// The TUI and the gateway are two processes saving one file. Each save must
// use its own temp file, and none may be left behind.
func TestSave_ConcurrentSavesAndReadsLeaveNoTemp(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "instances.json")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				// A Windows rename can still lose to a reader twice in a row;
				// the property under test is the temp files, not the error.
				_ = Save(p, Store{"ssh": {{ID: fmt.Sprintf("%d-%d", i, j)}}})
			}
		}(i)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_, _ = Load(p)
			}
		}()
	}
	wg.Wait()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if e.Name() != "instances.json" {
			t.Errorf("left behind: %s", e.Name())
		}
	}
	if _, err := Load(p); err != nil {
		t.Fatalf("final file does not parse: %v", err)
	}
}

// A failed first rename (a Windows reader holding the file) is retried once.
func TestSave_RetriesTheRenameOnce(t *testing.T) {
	p := filepath.Join(t.TempDir(), "instances.json")
	calls := 0
	orig := renameFn
	renameFn = func(a, b string) error {
		calls++
		if calls == 1 {
			return errors.New("sharing violation")
		}
		return orig(a, b)
	}
	t.Cleanup(func() { renameFn = orig })
	if err := Save(p, Store{"ssh": {{ID: "a"}}}); err != nil {
		t.Fatalf("Save after one failed rename: %v", err)
	}
	if calls != 2 {
		t.Fatalf("rename calls = %d, want 2", calls)
	}

	renameFn = func(string, string) error { return errors.New("always") }
	if err := Save(p, Store{}); err == nil {
		t.Fatal("Save reported success with every rename failing")
	}
	ents, _ := os.ReadDir(filepath.Dir(p))
	if len(ents) != 1 {
		t.Fatalf("a failed save left %d entries, want only instances.json", len(ents))
	}
}
