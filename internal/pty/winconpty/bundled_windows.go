//go:build windows

package winconpty

// This package is charmbracelet/x/conpty (MIT) vendored with one change: the
// three pseudoconsole syscalls are routed through a BUNDLED conpty.dll +
// OpenConsole.exe (Microsoft.Windows.Console.ConPTY redistributable) instead of
// the inbox kernel32 CreatePseudoConsole. The Windows 10 inbox conhost
// re-serializes claude-code's incremental input render incorrectly (the
// "H ello" caret gap); the newer bundled OpenConsole renders it cleanly, the
// same way Windows Terminal (which ships its own OpenConsole) does.

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// BundledDLLPath overrides the default location of conpty.dll (next to the
// running executable). Set before first use; mainly for tests and by Extract.
var BundledDLLPath string

var (
	bundledMu     sync.Mutex
	bundledLoaded bool
	procCreate    *windows.LazyProc
	procResize    *windows.LazyProc
	procClose     *windows.LazyProc
)

func bundledDLLPath() string {
	if BundledDLLPath != "" {
		return BundledDLLPath
	}
	exe, err := os.Executable()
	if err != nil {
		return "" // caller treats empty as "not available" → inbox fallback
	}
	return filepath.Join(filepath.Dir(exe), "conpty.dll")
}

// loadBundled loads the bundled conpty.dll and resolves its exports. It
// memoizes only SUCCESS: a transient failure (e.g. AV briefly locking the
// freshly extracted dll) is not cached, so a later call can retry instead of
// disabling the bundled host for the daemon's lifetime. OpenConsole.exe must
// sit next to conpty.dll (the dll locates it relative to its own module path).
func loadBundled() error {
	bundledMu.Lock()
	defer bundledMu.Unlock()
	if bundledLoaded {
		return nil
	}
	path := bundledDLLPath()
	if path == "" {
		return fmt.Errorf("winconpty: no conpty.dll path")
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("winconpty: conpty.dll not found at %s: %w", path, err)
	}
	dll := windows.NewLazyDLL(path)
	if err := dll.Load(); err != nil {
		return fmt.Errorf("winconpty: load %s: %w", path, err)
	}
	pc := dll.NewProc("ConptyCreatePseudoConsole")
	pr := dll.NewProc("ConptyResizePseudoConsole")
	pcl := dll.NewProc("ConptyClosePseudoConsole")
	for _, p := range []*windows.LazyProc{pc, pr, pcl} {
		if err := p.Find(); err != nil {
			return fmt.Errorf("winconpty: resolve %s: %w", p.Name, err)
		}
	}
	procCreate, procResize, procClose = pc, pr, pcl
	bundledLoaded = true
	return nil
}

// Available reports whether the bundled conpty.dll is present and loadable.
func Available() bool { return loadBundled() == nil }

// coordToUintptr packs a COORD (two int16) into a single uintptr argument, the
// calling convention golang.org/x/sys/windows uses for CreatePseudoConsole.
// Pure arithmetic — no unsafe / ABI assumptions (amd64 is the only target).
func coordToUintptr(size windows.Coord) uintptr {
	return uintptr(uint32(uint16(size.X)) | uint32(uint16(size.Y))<<16)
}

func bundledCreatePseudoConsole(size windows.Coord, in, out windows.Handle, flags uint32, hpc *windows.Handle) error {
	if err := loadBundled(); err != nil {
		return err
	}
	r0, _, _ := syscall.SyscallN(procCreate.Addr(),
		coordToUintptr(size), uintptr(in), uintptr(out), uintptr(flags), uintptr(unsafe.Pointer(hpc)))
	if int32(r0) < 0 { // FAILED(hr)
		return fmt.Errorf("ConptyCreatePseudoConsole: hr=0x%08x", uint32(r0))
	}
	return nil
}

func bundledResizePseudoConsole(hpc windows.Handle, size windows.Coord) error {
	if err := loadBundled(); err != nil {
		return err
	}
	r0, _, _ := syscall.SyscallN(procResize.Addr(), uintptr(hpc), coordToUintptr(size))
	if int32(r0) < 0 {
		return fmt.Errorf("ConptyResizePseudoConsole: hr=0x%08x", uint32(r0))
	}
	return nil
}

func bundledClosePseudoConsole(hpc windows.Handle) {
	// A live HPCON only exists after bundledCreatePseudoConsole succeeded, so
	// the procs are already loaded; loadBundled here is a cheap, mutex-guarded
	// re-check (returns nil immediately) that also gives the read of procClose a
	// happens-before edge for the race detector. It can never early-return on a
	// real handle.
	if loadBundled() != nil {
		return
	}
	_, _, _ = syscall.SyscallN(procClose.Addr(), uintptr(hpc))
}
