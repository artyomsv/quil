//go:build windows

package ipc

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/artyomsv/quil/internal/winjob"
)

func foreignOn(t *testing.T, path string) []string {
	t.Helper()
	foreign, err := foreignAllowOn(path)
	if err != nil {
		t.Fatal(err)
	}
	return foreign
}

func TestCreatePrivateFile_OwnerOnlyDACL(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tokens.json.tmp-1")
	f, err := CreatePrivateFile(p)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if foreign := foreignOn(t, p); len(foreign) != 0 {
		t.Fatalf("a file created private grants %v", foreign)
	}
}

// CREATE_NEW is the exclusivity the token store's temp file relies on: a
// path that already exists — a file planted there, or a stale temp — must be
// refused, never opened and truncated, and must keep its bytes.
func TestCreatePrivateFile_RefusesAnExistingFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tokens.json.tmp-1")
	if err := os.WriteFile(p, []byte("planted"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := CreatePrivateFile(p)
	if err == nil {
		f.Close()
		t.Fatal("CreatePrivateFile opened a file that already existed")
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "planted" {
		t.Fatalf("existing file now holds %q, want it untouched", got)
	}
}

// Covers token temp files and every rotated audit.log: anything created in a
// protected directory AFTER ProtectDir inherits owner-only access.
func TestProtectDir_NewFileInherits(t *testing.T) {
	dir := t.TempDir()
	if err := ProtectDir(dir); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "audit-20261001-120000.log")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if foreign := foreignOn(t, p); len(foreign) != 0 {
		t.Fatalf("a file created in a protected dir grants %v", foreign)
	}
	if foreign := foreignOn(t, dir); len(foreign) != 0 {
		t.Fatalf("the protected dir itself grants %v", foreign)
	}
}

// The read-back must recognise the DACL ProtectDir writes as Windows itself
// renders it (D:PAI, aliases), or every start rewrites the tree again.
func TestProtectDir_ReadsBackOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	if ok, err := dirOwnerOnly(dir); err != nil || ok {
		t.Fatalf("an inheriting temp dir: ok=%v err=%v, want false", ok, err)
	}
	if err := ProtectDir(dir); err != nil {
		t.Fatal(err)
	}
	if ok, err := dirOwnerOnly(dir); err != nil || !ok {
		sd, _ := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		t.Fatalf("after ProtectDir: ok=%v err=%v, want true; DACL %v", ok, err, sd)
	}
}

// An owner-only folder is not written again, and so not re-checked: a file
// quil did not write does not make ProtectDir fail once the folder is
// protected.
func TestProtectDir_SkipsAnOwnerOnlyFolder(t *testing.T) {
	dir := t.TempDir()
	if err := ProtectDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report.docx"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ProtectDir(dir); err != nil {
		t.Fatalf("second ProtectDir on an owner-only folder: %v", err)
	}
}

// A folder holding files quil did not write keeps its access list, and the
// refusal is ErrNotQuilHome (the daemon then starts no TCP listener).
func TestProtectDir_LeavesAnArbitraryFolderAlone(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "report.docx"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	if err := ProtectDir(dir); !errors.Is(err, ErrNotQuilHome) {
		t.Fatalf("ProtectDir on an arbitrary folder: %v, want ErrNotQuilHome", err)
	}
	after, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	if before.String() != after.String() {
		t.Fatalf("DACL changed: %s -> %s", before, after)
	}
}

// A fresh home already holds what quild wrote before Start: still protected.
func TestProtectDir_ProtectsAFreshHome(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"quild.lock", "quild.pid", "quild.log", "quil.log"} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := ProtectDir(dir); err != nil {
		t.Fatal(err)
	}
	if foreign := foreignOn(t, dir); len(foreign) != 0 {
		t.Fatalf("a fresh home grants %v", foreign)
	}
}

// The owner and LocalSystem match by SID value, whichever way they are
// spelled; anything else, or an unparseable SID, is foreign.
func TestOwnSIDs_MatchBySIDValue(t *testing.T) {
	own, err := ownSIDs()
	if err != nil {
		t.Fatal(err)
	}
	user, err := winjob.CurrentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{user, "SY", "S-1-5-18"} {
		if !own(s) {
			t.Errorf("own(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"BU", "BA", "WD", "S-1-5-32-545", "not-a-sid"} {
		if own(s) {
			t.Errorf("own(%q) = true, want false", s)
		}
	}
}

func TestDirAccessWarning_NullDACL(t *testing.T) {
	dir := t.TempDir()
	if err := applySDDL(dir, "D:NO_ACCESS_CONTROL"); err != nil {
		t.Fatal(err)
	}
	if foreign := foreignOn(t, dir); !reflect.DeepEqual(foreign, []string{nullDACL}) {
		t.Fatalf("NULL DACL: foreign = %v, want [%s]", foreign, nullDACL)
	}
	if w, err := DirAccessWarning(dir); err != nil || w == "" {
		t.Fatalf("NULL DACL: warning=%q err=%v, want a warning", w, err)
	}
}

// A socket ACL that cannot be applied stops the daemon when the directory is
// open to others too: the two guards must not fail open together.
func TestProtectSocket_FatalWhenDirectoryIsOpen(t *testing.T) {
	dir := t.TempDir()
	user, err := winjob.CurrentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	if err := applySDDL(dir, "D:P(A;OICI;FA;;;"+user+")(A;OICI;FA;;;SY)(A;OICI;0x1301bf;;;BU)"); err != nil {
		t.Fatal(err)
	}
	// A missing file makes ProtectFile fail without touching anything else.
	if err := protectSocket(filepath.Join(dir, "missing.sock")); err == nil {
		t.Fatal("socket ACL failure in an open directory was only a warning")
	}
}

// ...and stays a warning when the directory was verified owner-only.
func TestProtectSocket_WarnsWhenDirectoryIsOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	if err := ProtectDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := protectSocket(filepath.Join(dir, "missing.sock")); err != nil {
		t.Fatalf("owner-only directory: got %v, want a warning only", err)
	}
}
