package sbx

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

const testPasteID = "20261022-120000-0011223344556677"

// The copy goes to a new folder of its own, and is followed by a chmod run as
// root: sbx cp leaves a root-owned copy with the source's mode, which the
// agent cannot read.
func TestCopyIn_CopiesThenMakesTheFileReadable(t *testing.T) {
	f := &fakeRunner{answers: func([]string) (string, error) { return "", nil }}
	c := Client{Bin: "sbx", Runner: f}
	got, err := c.CopyIn(context.Background(), "cf-x-1", "/host/tmp/a.png", testPasteID)
	if err != nil {
		t.Fatal(err)
	}
	dir := "/tmp/quil-paste-" + testPasteID
	if got != dir+"/image.png" {
		t.Errorf("path = %q", got)
	}
	want := [][]string{
		{"sbx", "cp", "/host/tmp/a.png", "cf-x-1:" + dir + "/image.png"},
		{"sbx", "exec", "-u", "root", "cf-x-1", "sh", "-c", pasteScript, "sh", dir, dir + "/image.png"},
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
	_, err := (Client{Bin: "sbx", Runner: f}).CopyIn(context.Background(), "cf-x-1", "/h/a.png", testPasteID)
	if err == nil || !strings.Contains(err.Error(), "copying the image") || !strings.Contains(err.Error(), "sandbox not found") {
		t.Errorf("err = %v", err)
	}
	if len(f.calls) != 1 {
		t.Errorf("calls after a failed copy = %d, want 1", len(f.calls))
	}
}

// Nothing runs for a name or folder id sbx or a shell could misread, or an id
// short enough to guess.
func TestCopyIn_RefusesUnsafeNames(t *testing.T) {
	f := &fakeRunner{answers: func([]string) (string, error) { return "", nil }}
	c := Client{Bin: "sbx", Runner: f}
	for _, tc := range []struct{ name, id string }{
		{"-evil", testPasteID},
		{"ok", ""},
		{"ok", "short"},
		{"ok", testPasteID + "/x"},
		{"ok", testPasteID + " x"},
		{"ok", testPasteID + ":x"},
		{"ok", testPasteID + "..x"},
		{"ok", strings.ToUpper(testPasteID) + "AB"},
	} {
		if _, err := c.CopyIn(context.Background(), tc.name, "/h/a.png", tc.id); err == nil {
			t.Errorf("CopyIn(%q, %q) was accepted", tc.name, tc.id)
		}
	}
	if len(f.calls) != 0 {
		t.Errorf("sbx was called %d times for refused names", len(f.calls))
	}
}

func TestValidPastePath(t *testing.T) {
	for p, want := range map[string]bool{
		PastePath(testPasteID):                            true,
		"/tmp/quil-paste-" + testPasteID + "/other.png":   false,
		"/tmp/quil-paste/" + testPasteID + "/image.png":   false,
		"/tmp/quil-paste-../image.png":                    false,
		"/tmp/quil-paste-" + testPasteID + "/image.png\r": false,
		"/etc/passwd": false,
		"/tmp/quil-paste-" + testPasteID + "/x/image.png": false,
		"x/tmp/quil-paste-" + testPasteID + "/image.png":  false,
	} {
		if got := ValidPastePath(p); got != want {
			t.Errorf("ValidPastePath(%q) = %v, want %v", p, got, want)
		}
	}
}
