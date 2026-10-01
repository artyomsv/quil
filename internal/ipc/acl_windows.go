//go:build windows

package ipc

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/artyomsv/quil/internal/logger"
	"github.com/artyomsv/quil/internal/winjob"
)

// The current user's SID comes from winjob.CurrentUserSID — the one copy of
// that lookup in the repo; winjob imports no internal package, so this adds
// no cycle.

// applySDDL sets path's DACL as PROTECTED, so nothing inherited from a parent
// can widen it later.
func applySDDL(path, sddl string) error {
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return fmt.Errorf("parse %q: %w", sddl, err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("dacl: %w", err)
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil)
}

// ProtectDir gives dir the inheritable owner-only DACL BEFORE anything
// sensitive is created in it.
func ProtectDir(dir string) error {
	sid, err := winjob.CurrentUserSID()
	if err != nil {
		return err
	}
	return applySDDL(dir, ownerOnlySDDL(sid, true))
}

// ProtectFile gives an existing file the owner-only DACL directly, so it
// stays protected if the directory ACL is later loosened by hand.
func ProtectFile(path string) error {
	sid, err := winjob.CurrentUserSID()
	if err != nil {
		return err
	}
	return applySDDL(path, ownerOnlySDDL(sid, false))
}

// CreatePrivateFile creates path exclusively WITH the owner-only security
// descriptor in CreateFile — it is never readable by anyone else, not even
// for the moment an after-the-fact ACL call would need.
func CreatePrivateFile(path string) (*os.File, error) {
	sid, err := winjob.CurrentUserSID()
	if err != nil {
		return nil, err
	}
	sd, err := windows.SecurityDescriptorFromString(ownerOnlySDDL(sid, false))
	if err != nil {
		return nil, err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	sa := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: sd,
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, sa,
		windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "create", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}

// DirAccessWarning reads dir's DACL back and names any other principal that
// still has an allow entry.
func DirAccessWarning(dir string) (string, error) {
	foreign, err := foreignAllowOn(dir)
	if err != nil {
		return "", err
	}
	if len(foreign) > 0 {
		return fmt.Sprintf("%s also grants access to %v", dir, foreign), nil
	}
	return "", nil
}

// foreignAllowOn lists every principal other than this account and
// LocalSystem that path's DACL lets in; a NULL or absent DACL is reported
// as nullDACL.
func foreignAllowOn(path string) ([]string, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return nil, err
	}
	// Checked on the descriptor itself as well as in its SDDL rendering, so
	// an open DACL can never depend on how it happens to be spelled.
	dacl, _, err := sd.DACL()
	if errors.Is(err, windows.ERROR_OBJECT_NOT_FOUND) || (err == nil && dacl == nil) {
		return []string{nullDACL}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("dacl: %w", err)
	}
	own, err := ownSIDs()
	if err != nil {
		return nil, err
	}
	return foreignAllowSIDs(sd.String(), own), nil
}

// ownSIDs returns a matcher for this account's SID and LocalSystem that
// compares SID VALUES: the SDDL rendering of a SID can be an alias (SY for
// S-1-5-18, LA for an RID-500 account), so comparing strings would report
// the owner as foreign. A SID string that does not parse is foreign.
func ownSIDs() (func(string) bool, error) {
	s, err := winjob.CurrentUserSID()
	if err != nil {
		return nil, err
	}
	user, err := windows.StringToSid(s)
	if err != nil {
		return nil, fmt.Errorf("parse user sid %q: %w", s, err)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return nil, fmt.Errorf("local system sid: %w", err)
	}
	return func(sid string) bool {
		x, err := windows.StringToSid(sid)
		if err != nil {
			return false
		}
		return x.Equals(user) || x.Equals(system)
	}, nil
}

func listenUnixPrivate(path string) (net.Listener, error) { return net.Listen("unix", path) }

// protectSocket applies the owner-only DACL to the socket file. Whether
// AF_UNIX on Windows enforces a socket file's own ACL is what the manual
// two-account test (TestACLTwoAccount) settles, so a failure here is only a
// warning while the directory around the socket is read back and found
// owner-only — that directory is then the guard. When the directory is open
// to others as well, or cannot be read, the two guards would fail open
// together and the daemon stops instead.
func protectSocket(path string) error {
	aclErr := ProtectFile(path)
	if aclErr == nil {
		return nil
	}
	warning, dirErr := DirAccessWarning(filepath.Dir(path))
	if err := socketACLFailure(path, aclErr, warning, dirErr); err != nil {
		return err
	}
	logger.Warn("ipc: socket ACL not applied (%v); the owner-only QUIL_HOME directory ACL remains the guard", aclErr)
	return nil
}
