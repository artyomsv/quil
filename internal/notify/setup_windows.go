//go:build windows

package notify

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// classesKey is where a per-user URI scheme lives. HKCU, so no admin rights and
// no machine-wide effect.
func classesKey(opts Options) string {
	return `Software\Classes\` + opts.Scheme
}

// ShortcutPath is where the Start Menu shortcut for a variant lives.
//
// Windows will not display a toast from an unpackaged executable whose AUMID
// has no backing Start Menu shortcut, and the shortcut is also where the
// toast's displayed name and icon come from.
func ShortcutPath(opts Options) (string, error) {
	appData, err := windows.KnownFolderPath(windows.FOLDERID_RoamingAppData, 0)
	if err != nil {
		return "", fmt.Errorf("notify: locating AppData: %w", err)
	}
	return filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs", ShortcutBaseName(opts)), nil
}

// Registered reports whether both artifacts exist.
//
// Both, not either: a shortcut with no protocol key gives a toast that cannot
// be clicked, and a protocol key with no shortcut gives no toast at all. Half a
// registration is not a state worth reporting as success.
func Registered(opts Options) (bool, error) {
	path, err := ShortcutPath(opts)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("notify: stat shortcut: %w", err)
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, classesKey(opts)+`\shell\open\command`, registry.QUERY_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("notify: opening protocol key: %w", err)
	}
	defer k.Close()
	v, _, err := k.GetStringValue("")
	if err != nil || v == "" {
		return false, nil
	}
	return true, nil
}

// Setup writes both artifacts and returns what it wrote, so the command can
// print it. Nothing here needs admin rights.
func Setup(opts Options, exePath, home string) ([]string, error) {
	path, err := ShortcutPath(opts)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("notify: creating Start Menu directory: %w", err)
	}
	// Pinned and COM-initialised for the duration: apartment state is
	// per-thread, and a goroutine can migrate mid-sequence — which would leave
	// the IPersistFile::Save on a thread that never ran CoInitializeEx.
	if err := lockedCOM(func() error {
		return createShortcut(opts, exePath, path)
	}); err != nil {
		return nil, err
	}
	if err := writeProtocolKey(opts, exePath, home); err != nil {
		// Leave no half-registration behind: a shortcut without the protocol
		// key produces toasts that silently do nothing when clicked, which is
		// worse than no toasts at all.
		_ = os.Remove(path)
		return nil, err
	}
	return []string{path, `HKCU\` + classesKey(opts)}, nil
}

// Remove deletes both artifacts. Idempotent: a missing artifact is not an
// error, because a setup command the user cannot safely re-run is one they
// should not have been asked to run.
func Remove(opts Options) ([]string, error) {
	var removed []string

	path, err := ShortcutPath(opts)
	if err != nil {
		return nil, err
	}
	switch err := os.Remove(path); {
	case err == nil:
		removed = append(removed, path)
	case errors.Is(err, os.ErrNotExist):
	default:
		return removed, fmt.Errorf("notify: removing shortcut: %w", err)
	}

	// Depth-first: DeleteKey refuses a key that still has subkeys.
	for _, sub := range []string{`\shell\open\command`, `\shell\open`, `\shell`, ``} {
		err := registry.DeleteKey(registry.CURRENT_USER, classesKey(opts)+sub)
		if err != nil && !errors.Is(err, registry.ErrNotExist) {
			return removed, fmt.Errorf("notify: removing %s: %w", classesKey(opts)+sub, err)
		}
		if err == nil && sub == `` {
			removed = append(removed, `HKCU\`+classesKey(opts))
		}
	}
	return removed, nil
}

// writeProtocolKey registers the quil:// (or quil-dev://) handler.
//
// The command is the ONLY thing a click can reach — see RunActivation for why
// that ceiling matters.
func writeProtocolKey(opts Options, exePath, home string) error {
	root, _, err := registry.CreateKey(registry.CURRENT_USER, classesKey(opts), registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("notify: creating protocol key: %w", err)
	}
	defer root.Close()
	if err := root.SetStringValue("", "URL:Quil Protocol"); err != nil {
		return fmt.Errorf("notify: setting protocol description: %w", err)
	}
	// The presence of this value, empty, is what marks the key as a URI scheme.
	if err := root.SetStringValue("URL Protocol", ""); err != nil {
		return fmt.Errorf("notify: marking URL Protocol: %w", err)
	}

	cmd, _, err := registry.CreateKey(registry.CURRENT_USER, classesKey(opts)+`\shell\open\command`, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("notify: creating command key: %w", err)
	}
	defer cmd.Close()
	if err := cmd.SetStringValue("", activateCommand(opts, exePath, home)); err != nil {
		return fmt.Errorf("notify: setting command: %w", err)
	}
	return nil
}

// createShortcut writes the .lnk and stamps System.AppUserModel.ID on it.
//
// The AUMID is not a property WScript.Shell exposes — it lives in the link's
// IPropertyStore, which is why this is Go/COM rather than a PowerShell
// one-liner.
func createShortcut(opts Options, target, path string) error {
	var link uintptr
	r, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidShellLink)),
		0,
		1, // CLSCTX_INPROC_SERVER
		uintptr(unsafe.Pointer(&iidShellLinkW)),
		uintptr(unsafe.Pointer(&link)),
	)
	if err := hrErr("CoCreateInstance(ShellLink)", r); err != nil {
		return err
	}
	defer release(link)

	targetPtr, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return fmt.Errorf("notify: encoding target path: %w", err)
	}
	if err := hrErr("IShellLinkW::SetPath", call(link, idxShellLinkSetPath, uintptr(unsafe.Pointer(targetPtr)))); err != nil {
		return err
	}
	// Icon from the executable itself: quil.exe already carries the brand mark
	// via winres/, so the toast gets an icon with no extra asset to ship.
	if err := hrErr("IShellLinkW::SetIconLocation", call(link, idxShellLinkSetIconLocation, uintptr(unsafe.Pointer(targetPtr)), 0)); err != nil {
		return err
	}

	store, err := queryInterface(link, &iidPropertyStore, "IPropertyStore")
	if err != nil {
		return err
	}
	defer release(store)

	aumidPtr, err := windows.UTF16PtrFromString(opts.AUMID)
	if err != nil {
		return fmt.Errorf("notify: encoding AUMID: %w", err)
	}
	pv := propVariant{vt: vtLPWSTR, val: uintptr(unsafe.Pointer(aumidPtr))}
	key := propertyKey{fmtid: pkeyAppUserModelIDFmtID, pid: 5}

	if err := hrErr("IPropertyStore::SetValue", call(store, idxPropertyStoreSetValue,
		uintptr(unsafe.Pointer(&key)), uintptr(unsafe.Pointer(&pv)),
	)); err != nil {
		return err
	}
	if err := hrErr("IPropertyStore::Commit", call(store, idxPropertyStoreCommit)); err != nil {
		return err
	}
	// Deliberately NOT PropVariantClear(&pv): it CoTaskMemFree's the pointer
	// inside, and this PROPVARIANT was built over a Go-allocated UTF-16 buffer.
	// SetValue copies into the store, so there is nothing of ours to release.
	// Keeping aumidPtr alive until here is what runtime.KeepAlive guarantees.
	runtime.KeepAlive(aumidPtr)

	persist, err := queryInterface(link, &iidPersistFile, "IPersistFile")
	if err != nil {
		return err
	}
	defer release(persist)

	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return fmt.Errorf("notify: encoding shortcut path: %w", err)
	}
	if err := hrErr("IPersistFile::Save", call(persist, idxPersistFileSave,
		uintptr(unsafe.Pointer(pathPtr)), 1, // fRemember
	)); err != nil {
		return err
	}
	return nil
}
