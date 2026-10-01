//go:build windows

package ipc

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/artyomsv/quil/internal/winjob"
)

func foreignOn(t *testing.T, path string) []string {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	sid, err := winjob.CurrentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	return foreignAllowSIDs(sd.String(), sid, "SY", "S-1-5-18")
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
