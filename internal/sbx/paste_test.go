package sbx

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// The copy is followed by a chmod run as root: sbx cp leaves a root-owned
// copy with the source's mode, which the agent cannot read.
func TestCopyIn_CopiesThenMakesTheFileReadable(t *testing.T) {
	f := &fakeRunner{answers: func([]string) (string, error) { return "", nil }}
	c := Client{Bin: "sbx", Runner: f}
	got, err := c.CopyIn(context.Background(), "cf-x-1", "/host/tmp/a.png", "quil-paste-1.png")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/tmp/quil-paste/quil-paste-1.png" {
		t.Errorf("path = %q", got)
	}
	want := [][]string{
		{"sbx", "cp", "/host/tmp/a.png", "cf-x-1:/tmp/quil-paste/quil-paste-1.png"},
		{"sbx", "exec", "-u", "root", "cf-x-1", "sh", "-c", pasteScript, "sh", "/tmp/quil-paste", "/tmp/quil-paste/quil-paste-1.png"},
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Errorf("calls =\n%v\nwant\n%v", f.calls, want)
	}
}

// A failed copy stops before the chmod and says which step failed.
func TestCopyIn_FailedCopyStops(t *testing.T) {
	f := &fakeRunner{answers: func(argv []string) (string, error) {
		return "ERROR: sandbox not found\ntry: sbx cp --help", errors.New("exit status 1")
	}}
	_, err := (Client{Bin: "sbx", Runner: f}).CopyIn(context.Background(), "cf-x-1", "/h/a.png", "a.png")
	if err == nil || !strings.Contains(err.Error(), "copying the image") || !strings.Contains(err.Error(), "sandbox not found") {
		t.Errorf("err = %v", err)
	}
	if len(f.calls) != 1 {
		t.Errorf("calls after a failed copy = %d, want 1", len(f.calls))
	}
}

// Nothing runs for a name or file name sbx could misread.
func TestCopyIn_RefusesUnsafeNames(t *testing.T) {
	f := &fakeRunner{answers: func([]string) (string, error) { return "", nil }}
	c := Client{Bin: "sbx", Runner: f}
	for _, tc := range []struct{ name, base string }{
		{"-evil", "a.png"},
		{"ok", ""},
		{"ok", ".hidden.png"},
		{"ok", "../a.png"},
		{"ok", "a b.png"},
		{"ok", "a:b.png"},
		{"ok", "a;rm.png"},
	} {
		if _, err := c.CopyIn(context.Background(), tc.name, "/h/a.png", tc.base); err == nil {
			t.Errorf("CopyIn(%q, %q) was accepted", tc.name, tc.base)
		}
	}
	if len(f.calls) != 0 {
		t.Errorf("sbx was called %d times for refused names", len(f.calls))
	}
}
