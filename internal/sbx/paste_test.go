package sbx

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

const testPasteID = "20261022-120000-0011223344556677"

// The folder is made first (sbx 0.53 cp no longer creates it), and the copy
// is followed by a chmod run as root: sbx cp keeps the source's mode, which
// the agent cannot read.
func TestCopyIn_MakesFolderCopiesThenMakesTheFileReadable(t *testing.T) {
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
		{"sbx", "exec", "-u", "root", "cf-x-1", "sh", "-c", mkdirScript, "sh", dir},
		{"sbx", "cp", "/host/tmp/a.png", "cf-x-1:" + dir + "/image.png"},
		{"sbx", "exec", "-u", "root", "cf-x-1", "sh", "-c", readableScript, "sh", dir + "/image.png"},
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Errorf("calls =\n%v\nwant\n%v", f.calls, want)
	}
}

// A folder that is already there (the agent guessed or raced the name) stops
// the paste before anything is copied into it or chmodded as root.
func TestCopyIn_FailedMkdirStops(t *testing.T) {
	f := &fakeRunner{answers: func(argv []string) (string, error) {
		return "mkdir: cannot create directory: File exists", errors.New("exit status 1")
	}}
	_, err := (Client{Bin: "sbx", Runner: f}).CopyIn(context.Background(), "cf-x-1", "/h/a.png", testPasteID)
	if err == nil || !strings.Contains(err.Error(), "making the image folder") || !strings.Contains(err.Error(), "File exists") {
		t.Errorf("err = %v", err)
	}
	if len(f.calls) != 1 {
		t.Errorf("calls after a failed mkdir = %d, want 1", len(f.calls))
	}
}

// A failed copy stops before the chmod and says which step failed.
func TestCopyIn_FailedCopyStops(t *testing.T) {
	f := &fakeRunner{answers: func(argv []string) (string, error) {
		if len(argv) > 1 && argv[1] == "exec" {
			return "", nil
		}
		return "ERROR: sandbox not found\ntry: sbx cp --help", errors.New("exit status 1")
	}}
	_, err := (Client{Bin: "sbx", Runner: f}).CopyIn(context.Background(), "cf-x-1", "/h/a.png", testPasteID)
	if err == nil || !strings.Contains(err.Error(), "copying the image") || !strings.Contains(err.Error(), "sandbox not found") {
		t.Errorf("err = %v", err)
	}
	if len(f.calls) != 2 {
		t.Errorf("calls after a failed copy = %d, want 2", len(f.calls))
	}
}

// A failed chmod is reported as its own step, after the mkdir and the copy.
func TestCopyIn_FailedChmodSaysSo(t *testing.T) {
	f := &fakeRunner{answers: func(argv []string) (string, error) {
		if len(argv) > 6 && argv[6] == "-c" && argv[7] == readableScript {
			return "chmod: Operation not permitted", errors.New("exit status 1")
		}
		return "", nil
	}}
	_, err := (Client{Bin: "sbx", Runner: f}).CopyIn(context.Background(), "cf-x-1", "/h/a.png", testPasteID)
	if err == nil || !strings.Contains(err.Error(), "making the image readable") || !strings.Contains(err.Error(), "Operation not permitted") {
		t.Errorf("err = %v", err)
	}
	if len(f.calls) != 3 {
		t.Errorf("calls = %d, want 3", len(f.calls))
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
