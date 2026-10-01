//go:build windows

package ipc

import (
	"fmt"
	"net"
	"os"
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
	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return "", err
	}
	sid, err := winjob.CurrentUserSID()
	if err != nil {
		return "", err
	}
	if foreign := foreignAllowSIDs(sd.String(), sid, "SY", "S-1-5-18"); len(foreign) > 0 {
		return fmt.Sprintf("%s also grants access to %v", dir, foreign), nil
	}
	return "", nil
}

func listenUnixPrivate(path string) (net.Listener, error) { return net.Listen("unix", path) }

// protectSocket applies the owner-only DACL to the socket file. A failure is
// a WARNING here, not fatal as on Unix: the directory DACL (ProtectDir) is
// the guard that already covers the socket, and whether AF_UNIX on Windows
// enforces a socket file's own ACL is what the manual two-account test
// (TestACLTwoAccount) settles.
func protectSocket(path string) error {
	if err := ProtectFile(path); err != nil {
		logger.Warn("ipc: socket ACL not applied (%v); the QUIL_HOME directory ACL remains the guard", err)
	}
	return nil
}
