//go:build windows

package winjob

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modadvapi32               = windows.NewLazySystemDLL("advapi32.dll")
	procCreateRestrictedToken = modadvapi32.NewProc("CreateRestrictedToken")
)

const (
	disableMaxPrivilege = 0x1 // DISABLE_MAX_PRIVILEGE
	luaToken            = 0x4 // LUA_TOKEN
	mediumRID           = 0x2000
)

// tokenOwner and tokenDefaultDACL mirror TOKEN_OWNER and TOKEN_DEFAULT_DACL,
// which x/sys does not define.
type tokenOwner struct{ Owner *windows.SID }
type tokenDefaultDACL struct{ DefaultDacl *windows.ACL }

// LoweredToken returns a primary token for this process's user at MEDIUM
// integrity with the Administrators group deny-only — the rights a normal
// desktop program has. The caller closes it.
//
// Step order is load-bearing and was measured (issue #236 probes 15-16):
// without resetting the token OWNER and DEFAULT DACL to the user, objects the
// daemon creates are owned by the now deny-only Administrators group and
// ConPTY's pipes fail with "Access is denied". Every failure aborts; there is
// never a fallback to the unlowered token.
//
// DISABLE_MAX_PRIVILEGE also drops SeShutdownPrivilege and the other normal
// user privileges, so `shutdown /r` in a limited pane fails. Expected.
func LoweredToken() (windows.Token, error) {
	var self windows.Token
	access := uint32(windows.TOKEN_DUPLICATE | windows.TOKEN_QUERY | windows.TOKEN_ASSIGN_PRIMARY |
		windows.TOKEN_ADJUST_DEFAULT | windows.TOKEN_ADJUST_SESSIONID | windows.TOKEN_ADJUST_PRIVILEGES)
	if err := windows.OpenProcessToken(windows.CurrentProcess(), access, &self); err != nil {
		return 0, fmt.Errorf("OpenProcessToken: %w", err)
	}
	defer self.Close()

	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return 0, fmt.Errorf("administrators SID: %w", err)
	}
	disable := []windows.SIDAndAttributes{{Sid: admins}}
	var restricted windows.Token
	r, _, e := procCreateRestrictedToken.Call(uintptr(self), disableMaxPrivilege|luaToken,
		1, uintptr(unsafe.Pointer(&disable[0])), 0, 0, 0, 0, uintptr(unsafe.Pointer(&restricted)))
	if r == 0 {
		return 0, fmt.Errorf("CreateRestrictedToken: %w", e)
	}
	ok := false
	defer func() {
		if !ok {
			restricted.Close()
		}
	}()

	user, err := restricted.GetTokenUser()
	if err != nil {
		return 0, fmt.Errorf("token user: %w", err)
	}
	owner := tokenOwner{Owner: user.User.Sid}
	if err := windows.SetTokenInformation(restricted, windows.TokenOwner,
		(*byte)(unsafe.Pointer(&owner)), uint32(unsafe.Sizeof(owner))); err != nil {
		return 0, fmt.Errorf("set token owner: %w", err)
	}

	sd, err := windows.SecurityDescriptorFromString("D:(A;;GA;;;" + user.User.Sid.String() + ")(A;;GA;;;SY)")
	if err != nil {
		return 0, fmt.Errorf("default DACL descriptor: %w", err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return 0, fmt.Errorf("default DACL: %w", err)
	}
	def := tokenDefaultDACL{DefaultDacl: dacl}
	if err := windows.SetTokenInformation(restricted, windows.TokenDefaultDacl,
		(*byte)(unsafe.Pointer(&def)), uint32(unsafe.Sizeof(def))); err != nil {
		return 0, fmt.Errorf("set default DACL: %w", err)
	}

	rid, err := integrityRID(restricted)
	if err != nil {
		return 0, err
	}
	if rid > mediumRID {
		medium, err := windows.CreateWellKnownSid(windows.WinMediumLabelSid)
		if err != nil {
			return 0, fmt.Errorf("medium label SID: %w", err)
		}
		tml := windows.Tokenmandatorylabel{Label: windows.SIDAndAttributes{Sid: medium, Attributes: windows.SE_GROUP_INTEGRITY}}
		if err := windows.SetTokenInformation(restricted, windows.TokenIntegrityLevel,
			(*byte)(unsafe.Pointer(&tml)), tml.Size()); err != nil {
			return 0, fmt.Errorf("set integrity level: %w", err)
		}
	}
	ok = true
	return restricted, nil
}

// AboveMedium reports whether this process runs above Medium integrity (an
// admin over ssh, or an elevated shell).
func AboveMedium() (bool, error) {
	var self windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &self); err != nil {
		return false, fmt.Errorf("OpenProcessToken: %w", err)
	}
	defer self.Close()
	rid, err := integrityRID(self)
	return rid > mediumRID, err
}

// CurrentUserSID is this process's user SID as a string.
func CurrentUserSID() (string, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("token user: %w", err)
	}
	return u.User.Sid.String(), nil
}

func integrityRID(t windows.Token) (uint32, error) {
	var n uint32
	_ = windows.GetTokenInformation(t, windows.TokenIntegrityLevel, nil, 0, &n) // sizes n
	if n == 0 {
		return 0, fmt.Errorf("token integrity: size query returned 0")
	}
	buf := make([]byte, n)
	if err := windows.GetTokenInformation(t, windows.TokenIntegrityLevel, &buf[0], n, &n); err != nil {
		return 0, fmt.Errorf("token integrity: %w", err)
	}
	tml := (*windows.Tokenmandatorylabel)(unsafe.Pointer(&buf[0]))
	sid := tml.Label.Sid
	return sid.SubAuthority(uint32(sid.SubAuthorityCount()) - 1), nil
}
