package sbx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// The answer sbx 0.48 gives, measured: a bare list, the primary workspace
// first, read-only ones marked ":ro".
const listJSON048 = `[
  {
    "name": "team-box",
    "id": "7834be39-99c7-45f0-8a33-0a220577c6f2",
    "agent": "claude",
    "status": "stopped",
    "workspaces": ["/w/ticket", "/w/ticket/env.yaml:ro"],
    "created_at": "2026-10-15T14:12:49Z"
  }
]
`

func TestParseListJSON(t *testing.T) {
	want048 := []Listed{{Name: "team-box", Status: "stopped", Workspaces: []Workspace{
		{Path: "/w/ticket"}, {Path: "/w/ticket/env.yaml", ReadOnly: true},
	}}}
	tests := []struct {
		name string
		out  string
		want []Listed
	}{
		{"sbx 0.48 list", listJSON048, want048},
		{"a notice before the JSON", "✓ sandbox started\n" + listJSON048, want048},
		{"wrapped, with state and :rw", `{"sandboxes":[{"name":"b","state":"Running","workspaces":["/x:rw"]}]}`,
			[]Listed{{Name: "b", Status: "running", Workspaces: []Workspace{{Path: "/x"}}}}},
		{"a name Quil cannot pass to sbx is left out", `[{"name":"-x"},{"name":"ok"}]`, []Listed{{Name: "ok"}}},
		{"no sandboxes", "[]\n", nil},
		{"empty answer", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseListJSON(tt.out)
			if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseListJSON = %+v, %v; want %+v", got, err, tt.want)
			}
		})
	}
	for _, bad := range []string{"Error: not running\n", "[{\n"} {
		if _, err := ParseListJSON(bad); err == nil {
			t.Errorf("ParseListJSON(%q) accepted", bad)
		}
	}
}

func TestListSandboxes_AsksForJSON(t *testing.T) {
	f := &fakeRunner{answers: func(argv []string) (string, error) { return listJSON048, nil }}
	got, err := Client{Bin: "sbx", Runner: f}.ListSandboxes(context.Background())
	if err != nil || len(got) != 1 || got[0].Name != "team-box" {
		t.Fatalf("ListSandboxes = %+v, %v", got, err)
	}
	if !f.called("sbx", "ls", "--json") {
		t.Errorf("calls = %v", f.calls)
	}
	f = &fakeRunner{answers: func([]string) (string, error) { return "boom\n", fmt.Errorf("exit status 1") }}
	if _, err := (Client{Bin: "sbx", Runner: f}).ListSandboxes(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v, want sbx's own line", err)
	}
}

func TestCovers_WritableFoldersOnly(t *testing.T) {
	ticket := t.TempDir()
	sub := filepath.Join(ticket, "app", "src")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	l := Listed{Name: "team-box", Workspaces: []Workspace{
		{Path: ticket},
		{Path: other, ReadOnly: true},
		{Path: "relative/dir"},
	}}
	for dir, want := range map[string]bool{
		ticket:                           true,
		sub:                              true,
		other:                            false, // read-only
		filepath.Join(other, "x"):        false,
		filepath.Dir(ticket):             false, // above the folder
		ticket + "-sibling":              false, // a prefix is not a parent
		filepath.Join("relative", "dir"): false,
	} {
		if got := l.Covers(dir); got != want {
			t.Errorf("Covers(%q) = %v, want %v", dir, got, want)
		}
	}
}

func TestCovers_LinkOutIsNotCovered(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	ticket, outside := t.TempDir(), t.TempDir()
	link := filepath.Join(ticket, "out")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if (Listed{Workspaces: []Workspace{{Path: ticket}}}).Covers(link) {
		t.Error("a link out of the folder was covered")
	}
}

func TestFindListedAndCheckAttach(t *testing.T) {
	list := []Listed{{Name: "team-box", Workspaces: []Workspace{{Path: "/w/ticket"}, {Path: "/w/.git", ReadOnly: true}}}}
	if _, err := FindListed(list, "nope"); !errors.Is(err, ErrNoSandbox) {
		t.Errorf("FindListed(nope) = %v, want ErrNoSandbox", err)
	}
	l, err := FindListed(list, "team-box")
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckAttach(l, "/elsewhere"); err == nil || !strings.Contains(err.Error(), "/w/ticket") ||
		strings.Contains(err.Error(), ".git") {
		t.Errorf("CheckAttach = %v, want the writable folder named and the read-only one not", err)
	}
	if err := CheckAttach(Listed{Name: "ro", Workspaces: []Workspace{{Path: "/w", ReadOnly: true}}}, "/w"); err == nil ||
		!strings.Contains(err.Error(), "no writable folder") {
		t.Errorf("CheckAttach(read-only only) = %v", err)
	}
}

// A read-only folder inside a writable one is not writable, and a writable
// folder inside a read-only one is: the most specific folder decides.
func TestCovers_MostSpecificFolderDecides(t *testing.T) {
	root := t.TempDir()
	mk := func(rel string) string {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	app, ref, ro, notes := mk("app"), mk("app/reference"), mk("ro"), mk("ro/notes")
	l := Listed{Workspaces: []Workspace{
		{Path: app}, {Path: ref, ReadOnly: true}, {Path: ro, ReadOnly: true}, {Path: notes},
	}}
	for dir, want := range map[string]bool{
		app:                        true,
		ref:                        false,
		filepath.Join(ref, "deep"): false,
		ro:                         false,
		notes:                      true,
	} {
		if got := l.Covers(dir); got != want {
			t.Errorf("Covers(%q) = %v, want %v", dir, got, want)
		}
	}
	both := Listed{Workspaces: []Workspace{{Path: app}, {Path: app, ReadOnly: true}}}
	if both.Covers(app) {
		t.Error("one folder listed both ways counted as writable")
	}
}

// The probe asks the sandbox, passes the folder as an argument, and reads
// only its own marker line.
func TestWritableInside(t *testing.T) {
	const dir = "/w/ticket'; rm -rf / #"
	for _, tc := range []struct {
		name string
		out  string
		err  error
		want bool
	}{
		{"writable, after a start notice", "✓ sandbox started\nQUIL_SBX_WRITABLE\n", nil, true},
		{"not writable", "", nil, false},
		{"marker inside other text", "echo QUIL_SBX_WRITABLE\n", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeRunner{answers: func([]string) (string, error) { return tc.out, tc.err }}
			got, err := Client{Bin: "sbx", Runner: f}.WritableInside(context.Background(), "team-box", dir)
			if err != nil || got != tc.want {
				t.Errorf("WritableInside = %v, %v; want %v", got, err, tc.want)
			}
			if !f.called(append([]string{"sbx"}, WritableArgs("team-box", dir)...)...) {
				t.Errorf("calls = %v", f.calls)
			}
		})
	}
	args := WritableArgs("team-box", dir)
	if args[len(args)-1] != dir || strings.Contains(args[4], dir) {
		t.Errorf("the folder is not a separate argument: %q", args)
	}
	f := &fakeRunner{answers: func([]string) (string, error) { return "no such sandbox\n", fmt.Errorf("exit status 1") }}
	if _, err := (Client{Bin: "sbx", Runner: f}).WritableInside(context.Background(), "team-box", "/w"); err == nil ||
		!strings.Contains(err.Error(), "no such sandbox") {
		t.Errorf("err = %v, want sbx's own line", err)
	}
}
