//go:build windows

package notify

import (
	"fmt"
	"runtime"

	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Raw COM/WinRT interop, following the house pattern in
// internal/clipboard/clipboard_windows.go rather than taking a dependency.
//
// Every GUID and vtable index below was confirmed empirically by the spike
// recorded in docs/superpowers/plans/2026-08-13-desktop-notifications.md. They
// are NOT reachable by CI, which builds Linux only — so anything added here
// needs the same treatment: prove it on a real host, then write it down.

var (
	ole32   = windows.NewLazySystemDLL("ole32.dll")
	combase = windows.NewLazySystemDLL("combase.dll")
	shell32 = windows.NewLazySystemDLL("shell32.dll")

	procSetCurrentProcessExplicitAppUserModelID = shell32.NewProc("SetCurrentProcessExplicitAppUserModelID")

	procCoInitializeEx            = ole32.NewProc("CoInitializeEx")
	procCoCreateInstance          = ole32.NewProc("CoCreateInstance")
	procRoInitialize              = combase.NewProc("RoInitialize")
	procRoGetActivationFactory    = combase.NewProc("RoGetActivationFactory")
	procRoActivateInstance        = combase.NewProc("RoActivateInstance")
	procWindowsCreateString       = combase.NewProc("WindowsCreateString")
	procWindowsDeleteString       = combase.NewProc("WindowsDeleteString")
	procWindowsGetStringRawBuffer = combase.NewProc("WindowsGetStringRawBuffer")
)

var (
	clsidShellLink   = windows.GUID{Data1: 0x00021401, Data2: 0x0000, Data3: 0x0000, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidShellLinkW    = windows.GUID{Data1: 0x000214F9, Data2: 0x0000, Data3: 0x0000, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidPersistFile   = windows.GUID{Data1: 0x0000010B, Data2: 0x0000, Data3: 0x0000, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidPropertyStore = windows.GUID{Data1: 0x886D8EEB, Data2: 0x8CF2, Data3: 0x4446, Data4: [8]byte{0x8D, 0x02, 0xCD, 0xBA, 0x1D, 0xBD, 0xCF, 0x99}}

	// PKEY_AppUserModel_ID = {9F4C2855-9F79-4B39-A8D0-E1D42DE1D5F3}, PID 5
	pkeyAppUserModelIDFmtID = windows.GUID{Data1: 0x9F4C2855, Data2: 0x9F79, Data3: 0x4B39, Data4: [8]byte{0xA8, 0xD0, 0xE1, 0xD4, 0x2D, 0xE1, 0xD5, 0xF3}}

	iidToastNotificationManagerStatics  = windows.GUID{Data1: 0x50AC103F, Data2: 0xD235, Data3: 0x4598, Data4: [8]byte{0xBB, 0xEF, 0x98, 0xFE, 0x4D, 0x1A, 0x3A, 0xD4}}
	iidToastNotificationManagerStatics2 = windows.GUID{Data1: 0x7AB93C52, Data2: 0x0E48, Data3: 0x4750, Data4: [8]byte{0xBA, 0x9D, 0x1A, 0x41, 0x13, 0x98, 0x18, 0x47}}
	iidToastNotificationFactory         = windows.GUID{Data1: 0x04124B20, Data2: 0x82C6, Data3: 0x4229, Data4: [8]byte{0xB1, 0x09, 0xFD, 0x9E, 0xD4, 0x66, 0x2B, 0x53}}
	iidXMLDocumentIO                    = windows.GUID{Data1: 0x6CD0E74E, Data2: 0xEE65, Data3: 0x4489, Data4: [8]byte{0x9E, 0xBF, 0xCA, 0x43, 0xE8, 0x7B, 0xA6, 0x37}}
	iidToastNotification2               = windows.GUID{Data1: 0x9DFB9FD1, Data2: 0x143A, Data3: 0x490E, Data4: [8]byte{0x90, 0xBF, 0xB9, 0xFB, 0xA7, 0x13, 0x2D, 0xE7}}
)

// Vtable indices. IUnknown occupies 0-2 and IInspectable adds 3-5, so a WinRT
// interface's own members start at 6.
const (
	idxQueryInterface = 0
	idxRelease        = 2

	idxShellLinkSetIconLocation = 17
	idxShellLinkSetPath         = 20
	idxPropertyStoreSetValue    = 6
	idxPropertyStoreCommit      = 7
	idxPersistFileSave          = 6

	idxXMLDocumentIOLoadXml     = 6
	idxToastFactoryCreate       = 6
	idxToastStaticsNotifierWith = 7
	idxToastStatics2History     = 6

	idxToastNotifierShow    = 6
	idxToastNotifierSetting = 8

	// IToastNotification2 declares put_ BEFORE get_ for each property. Using
	// the get_ index as a setter returns S_OK and sets NOTHING — an HSTRING
	// passed where an HSTRING* out-param is expected is accepted silently, and
	// the only symptom is that a later withdraw reports ERROR_NOT_FOUND. That
	// cost the spike two runs to find, which is why notifyLocked reads the tag
	// back after setting it.
	idxToastPutTag   = 6
	idxToastGetTag   = 7
	idxToastPutGroup = 8

	// Confirmed by probing: index 8 is the WithId variant. The 2-arg
	// RemoveGroupedTag resolves the AUMID from the calling process, which for
	// an unpackaged binary is not the one registered, so it silently removes
	// nothing.
	idxHistoryRemoveGroupedTagWithID = 8
)

type propVariant struct {
	vt        uint16
	reserved1 uint16
	reserved2 uint16
	reserved3 uint16
	val       uintptr
	_         uintptr
}

type propertyKey struct {
	fmtid windows.GUID
	pid   uint32
}

const vtLPWSTR = 31

// initCOMThisThread initialises COM/WinRT for the CALLING OS THREAD.
//
// Deliberately NOT guarded by a sync.Once. Apartment state is per-thread, so a
// process-wide guard would initialise the first thread to arrive and silently
// leave every later one uninitialised — which is exactly the bug a Go program
// walks into, since goroutines migrate between threads. Callers that need a
// stable apartment must hold the thread (runtime.LockOSThread) themselves;
// winNotifier.serve and lockedCOM do.
//
// Repeat calls on an already-initialised thread return S_FALSE or
// RPC_E_CHANGED_MODE, both harmless — we need the runtime up, not a particular
// apartment model.
func initCOMThisThread() {
	// RO_INIT_MULTITHREADED
	procRoInitialize.Call(1)
	// COINIT_APARTMENTTHREADED, for the shell-link work.
	procCoInitializeEx.Call(0, 2)
}

// lockedCOM runs fn on a thread pinned for its duration with COM initialised.
//
// For one-shot work in a short-lived process (quil notify setup): the caller
// does not need a long-lived apartment, only a stable one for the call.
func lockedCOM(fn func() error) error {
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		initCOMThisThread()
		done <- fn()
	}()
	return <-done
}

// setProcessAUMID declares which registered application this process IS.
//
// Documented as required for an unpackaged executable: without it Windows has
// no association between the running process and the AUMID.
//
// It is NOT sufficient on its own, which is worth stating because the obvious
// reading is that it would be. Measured on Windows 10 19045: a Start Menu
// shortcut created seconds earlier, plus this call, still left get_Setting
// answering ERROR_NOT_FOUND for at least 45 s — while an AUMID whose shortcut
// had existed for some minutes worked immediately. The remaining variable is
// Windows' own indexing of the Start Menu, which nothing here can force.
//
// Kept because it is the documented contract and costs one call; the practical
// consequence is that the first toast after `quil notify setup` may be dropped,
// which the setup command says out loud.
//
// Must run before the first notifier is created, which is why New calls it.
func setProcessAUMID(aumid string) error {
	p, err := windows.UTF16PtrFromString(aumid)
	if err != nil {
		return fmt.Errorf("notify: encoding AUMID: %w", err)
	}
	r, _, _ := procSetCurrentProcessExplicitAppUserModelID.Call(uintptr(unsafe.Pointer(p)))
	return hrErr("SetCurrentProcessExplicitAppUserModelID", r)
}

// vtbl reads the Nth function pointer out of a COM object's vtable.
//
// COM objects are held as uintptr because that is what SyscallN takes and what
// the OS writes into our out-params. The offset uses unsafe.Add rather than
// uintptr arithmetic — these addresses are OS-owned and outside the Go heap,
// but expressing the step as pointer arithmetic keeps that fact checkable.
func vtbl(obj uintptr, index int) uintptr {
	vt := *(*unsafe.Pointer)(unsafe.Pointer(obj))
	return *(*uintptr)(unsafe.Add(vt, uintptr(index)*unsafe.Sizeof(uintptr(0))))
}

// call invokes a COM method by vtable index.
//
// The //go:uintptrescapes directive is REQUIRED, not decorative. Callers pass
// Go pointers as uintptr (`uintptr(unsafe.Pointer(&out))`), and that conversion
// is precisely the one escape analysis does NOT treat as escaping — so without
// the directive those locals may stay on the stack, and a stack copy triggered
// in this function's prologue or by the append below would leave COM writing an
// out-param to a stale address.
//
// x/sys/windows marks (*Proc).Call and (*LazyProc).Call the same way, which is
// what makes the direct procXxx.Call sites in this file safe; this helper
// bypasses those, so it needs its own. The append is safe only because the
// directive covers the whole call duration, inner calls included.
//
// Note this is NOT the pattern in internal/clipboard/clipboard_windows.go —
// that file never passes a Go pointer through .Call at all, only handles and
// GlobalAlloc'd memory, so it is not precedent for code that does.
//
//go:uintptrescapes
func call(obj uintptr, index int, args ...uintptr) uintptr {
	full := append([]uintptr{obj}, args...)
	ret, _, _ := syscall.SyscallN(vtbl(obj, index), full...)
	return ret
}

func release(obj uintptr) {
	if obj != 0 {
		call(obj, idxRelease)
	}
}

func hrErr(name string, code uintptr) error {
	if int32(code) < 0 {
		return fmt.Errorf("notify: %s: HRESULT 0x%08X", name, uint32(code))
	}
	return nil
}

func queryInterface(obj uintptr, iid *windows.GUID, name string) (uintptr, error) {
	var out uintptr
	if err := hrErr("QueryInterface("+name+")", call(obj, idxQueryInterface,
		uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&out)),
	)); err != nil {
		return 0, err
	}
	return out, nil
}

func hstring(s string) (uintptr, error) {
	if s == "" {
		return 0, nil
	}
	u, err := windows.UTF16FromString(s)
	if err != nil {
		return 0, fmt.Errorf("notify: encoding %q: %w", s, err)
	}
	var h uintptr
	r, _, _ := procWindowsCreateString.Call(
		uintptr(unsafe.Pointer(&u[0])),
		uintptr(len(u)-1),
		uintptr(unsafe.Pointer(&h)),
	)
	if err := hrErr("WindowsCreateString", r); err != nil {
		return 0, err
	}
	return h, nil
}

func freeHString(h uintptr) {
	if h != 0 {
		procWindowsDeleteString.Call(h)
	}
}

// hstringValue reads an HSTRING back into a Go string. Needed because a setter
// called at a wrong vtable index fails silently — reading the value back is the
// only way to know it landed.
func hstringValue(h uintptr) string {
	if h == 0 {
		return ""
	}
	var n uint32
	p, _, _ := procWindowsGetStringRawBuffer.Call(h, uintptr(unsafe.Pointer(&n)))
	if p == 0 || n == 0 {
		return ""
	}
	return windows.UTF16ToString(unsafe.Slice((*uint16)(unsafe.Pointer(p)), n))
}

func activationFactory(class string, iid *windows.GUID) (uintptr, error) {
	h, err := hstring(class)
	if err != nil {
		return 0, err
	}
	defer freeHString(h)

	var factory uintptr
	r, _, _ := procRoGetActivationFactory.Call(h, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&factory)))
	if err := hrErr("RoGetActivationFactory("+class+")", r); err != nil {
		return 0, err
	}
	return factory, nil
}
