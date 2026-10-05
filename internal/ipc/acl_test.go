package ipc

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const testSID = "S-1-5-21-1111111111-2222222222-3333333333-1001"

// ownTest stands in for the Windows matcher, which compares SID values.
func ownTest(sid string) bool { return sid == testSID || sid == "SY" }

func TestOwnerOnlySDDL_Forms(t *testing.T) {
	if got, want := ownerOnlySDDL(testSID, true), "D:P(A;OICI;FA;;;"+testSID+")(A;OICI;FA;;;SY)"; got != want {
		t.Errorf("inherit form = %q, want %q", got, want)
	}
	if got, want := ownerOnlySDDL(testSID, false), "D:P(A;;FA;;;"+testSID+")(A;;FA;;;SY)"; got != want {
		t.Errorf("file form = %q, want %q", got, want)
	}
}

func TestForeignAllowSIDs(t *testing.T) {
	sddl := "O:" + testSID + "G:" + testSID + "D:P(A;OICI;FA;;;" + testSID + ")(A;OICI;FA;;;SY)" +
		"(A;OICI;0x1301bf;;;BU)(D;;FA;;;WD)(A;ID;FA;;;BA)"
	got := foreignAllowSIDs(sddl, ownTest)
	if want := []string{"BU", "BA"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("foreign = %v, want %v (deny ACEs ignored)", got, want)
	}
	if got := foreignAllowSIDs(ownerOnlySDDL(testSID, true), ownTest); len(got) != 0 {
		t.Fatalf("owner-only DACL reported foreign entries %v", got)
	}
}

// A NULL or absent DACL lets everyone in, so it must never read as owner-only
// — while an EMPTY DACL (no entries) lets nobody in and is not foreign.
func TestForeignAllowSIDs_NullDACLIsForeign(t *testing.T) {
	for _, sddl := range []string{"D:NO_ACCESS_CONTROL", "D:PNO_ACCESS_CONTROL", "O:SYG:SYD:NO_ACCESS_CONTROL", ""} {
		if got := foreignAllowSIDs(sddl, ownTest); !reflect.DeepEqual(got, []string{nullDACL}) {
			t.Errorf("foreignAllowSIDs(%q) = %v, want [%s]", sddl, got, nullDACL)
		}
	}
	if got := foreignAllowSIDs("D:P", ownTest); len(got) != 0 {
		t.Errorf("empty DACL reported foreign entries %v", got)
	}
}

// userTest matches the account alone, not LocalSystem.
func userTest(sid string) bool { return sid == testSID }

// ownerOnlyDACL decides whether ProtectDir may skip its write, so every way
// a DACL can differ from the one it writes must read false.
func TestOwnerOnlyDACL(t *testing.T) {
	u := testSID
	for _, tc := range []struct {
		name string
		sddl string
		want bool
	}{
		{"exactly what ProtectDir writes", ownerOnlySDDL(u, true), true},
		{"auto-inherited flag beside P", "D:PAI(A;OICI;FA;;;" + u + ")(A;OICI;FA;;;SY)", true},
		{"owner and group sections first", "O:" + u + "G:" + u + "D:PAI(A;OICI;FA;;;" + u + ")(A;OICI;FA;;;SY)", true},
		{"flags in the other order", "D:P(A;CIOI;FA;;;" + u + ")", true},
		{"owner alone", "D:P(A;OICI;FA;;;" + u + ")", true},
		{"SYSTEM alone: the owner has no entry", "D:P(A;OICI;FA;;;SY)", false},
		{"not protected", "D:(A;OICI;FA;;;" + u + ")(A;OICI;FA;;;SY)", false},
		{"auto-inherited, not protected", "D:AI(A;OICI;FA;;;" + u + ")", false},
		{"no DACL", "O:" + u, false},
		{"NULL DACL", "D:NO_ACCESS_CONTROL", false},
		{"protected NULL DACL", "D:PNO_ACCESS_CONTROL", false},
		{"empty protected DACL", "D:P", false},
		{"foreign allow", ownerOnlySDDL(u, true) + "(A;OICI;FA;;;BU)", false},
		{"foreign read-only allow", ownerOnlySDDL(u, true) + "(A;OICI;0x1200a9;;;WD)", false},
		{"deny entry", ownerOnlySDDL(u, true) + "(D;OICI;FA;;;BU)", false},
		{"inherited entry", "D:P(A;OICIID;FA;;;" + u + ")", false},
		{"files only", "D:P(A;OI;FA;;;" + u + ")", false},
		{"no inheritance: the file form", ownerOnlySDDL(u, false), false},
		{"inherit-only", "D:P(A;OICIIO;FA;;;" + u + ")", false},
		{"partial rights", "D:P(A;OICI;0x1301bf;;;" + u + ")", false},
		{"object ACE", "D:P(OA;OICI;FA;00000000-0000-0000-0000-000000000000;;" + u + ")", false},
		{"text that is not an entry", ownerOnlySDDL(u, true) + "junk", false},
		{"a SACL after the DACL", ownerOnlySDDL(u, true) + "S:(AU;SA;FA;;;WD)", false},
	} {
		if got := ownerOnlyDACL(tc.sddl, ownTest, userTest); got != tc.want {
			t.Errorf("%s: ownerOnlyDACL(%q) = %v, want %v", tc.name, tc.sddl, got, tc.want)
		}
	}
}

func TestForeignHomeEntry(t *testing.T) {
	quil := []string{
		// What exists before ProtectDir runs in a fresh folder.
		"quild.lock", "quild.pid", "quild.log", "quild.stderr.log", "quil.log",
		"conpty.dll", "OpenConsole.exe",
		// What a home in use holds.
		"workspace.json", "workspace.json.tmp", "workspace.json.bak", "config.toml",
		"bindings.toml", "templates.toml", "instances.json", "window.json",
		"recent-cwds.json", "recent-cwds-host-1a2b.json", "remote-projects.json",
		"project-groups.json", "project-groups.before-shared-x.json", "shared-import.json",
		"sandbox-image.json", "tokens.json", "tokens.json.tmp-1", "audit.log",
		"audit-20261001-120000.log", "quild-20261001-120000.log", "quil-20261001-120000.log",
		"web.log", "hook.log", "notify-activate.log", "quild.sock", ".quil-staging-123",
		// Dot-prefixed temps a crash can leave (config.SaveTemplates), and any
		// future one.
		".templates-123", ".anything",
		"buffers", "plugins", "sessions", "events", "shellinit", "paste", "notes",
		"notes-conflicts", "mcp-logs", "update", "history", "sandbox", "claudehook",
		"codexhook", "opencodehook",
	}
	if got := foreignHomeEntry(quil); got != "" {
		t.Errorf("a quil folder read foreign at %q", got)
	}
	if got := foreignHomeEntry(nil); got != "" {
		t.Errorf("an empty folder read foreign at %q", got)
	}
	// A folder QUIL_HOME was pointed at keeps its own files, so it reads
	// foreign even after quil wrote its own beside them.
	for _, name := range []string{"Documents", "desktop.ini", "notes.txt", "configuration", "quilt.txt", "workspaces"} {
		if got := foreignHomeEntry(append(append([]string{}, quil...), name)); got != name {
			t.Errorf("foreignHomeEntry with %q = %q, want %q", name, got, name)
		}
	}
}

func TestCheckQuilHome(t *testing.T) {
	empty := t.TempDir()
	if err := checkQuilHome(empty); err != nil {
		t.Errorf("empty folder: %v", err)
	}
	fresh := t.TempDir()
	for _, n := range []string{"quild.lock", "quild.pid", "quild.log"} {
		if err := os.WriteFile(filepath.Join(fresh, n), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := checkQuilHome(fresh); err != nil {
		t.Errorf("folder holding what quild wrote before ProtectDir: %v", err)
	}
	arbitrary := t.TempDir()
	for _, n := range []string{"quild.pid", "report.docx"} {
		if err := os.WriteFile(filepath.Join(arbitrary, n), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := checkQuilHome(arbitrary); !errors.Is(err, ErrNotQuilHome) || !strings.Contains(err.Error(), "report.docx") {
		t.Errorf("arbitrary folder: got %v, want ErrNotQuilHome naming report.docx", err)
	}
	if err := checkQuilHome(filepath.Join(empty, "missing")); err == nil || errors.Is(err, ErrNotQuilHome) {
		t.Errorf("unlistable folder: got %v, want a listing error", err)
	}
}

func TestSocketACLFailure(t *testing.T) {
	aclErr := errors.New("acl refused")
	if err := socketACLFailure("s", aclErr, "", nil); err != nil {
		t.Errorf("owner-only directory: got %v, want nil (a warning only)", err)
	}
	if err := socketACLFailure("s", aclErr, "dir also grants access to [BU]", nil); err == nil ||
		!errors.Is(err, aclErr) || !strings.Contains(err.Error(), "BU") {
		t.Errorf("open directory: got %v, want a fatal error naming both failures", err)
	}
	dirErr := errors.New("read denied")
	if err := socketACLFailure("s", aclErr, "", dirErr); err == nil ||
		!errors.Is(err, dirErr) || !strings.Contains(err.Error(), "acl refused") {
		t.Errorf("unreadable directory: got %v, want a fatal error naming both failures", err)
	}
}
