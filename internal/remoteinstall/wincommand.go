package remoteinstall

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// The Windows install is three ssh round trips rather than one, because
// Windows PowerShell 5.1 started by Win32-OpenSSH CANNOT read ssh stdin —
// [Console]::OpenStandardInput().CopyTo hangs, and so does reading an exact
// byte count (measured 2026-09-27, Ruling R-10). The host's own tar.exe reads
// the same stdin without trouble, so PowerShell prepares and finalizes, and
// tar.exe alone touches the archive:
//
//  1. prepare  — no stdin; creates the install dir and a staging dir under it
//  2. extract  — tar.exe -xf - with the tar.gz on stdin, into the staging dir
//  3. finalize — no stdin; verifies each file's SHA-256, swaps them in
//
// Staging INSIDE the install dir keeps the final Move-Item a same-volume
// rename, which is what lets it replace a running quil.exe (renamed aside
// first) without copying.

// prepareSentinel marks where remote-prepare.ps1's own output begins.
const prepareSentinel = "__quil_prepare__"

// stagingPrefix is the staging dir's name before its 32-hex GUID.
const stagingPrefix = ".quil-staging-"

// stagingGUIDLen is [guid]::NewGuid().ToString('N'): 32 lower-case hex digits.
const stagingGUIDLen = 32

// exeEntryName is what a FileSHA256 key may look like. Keys land inside a
// PowerShell single-quoted literal, and the finalize step refuses any staged
// file not named here, so the shape is fixed rather than escaped.
var exeEntryName = regexp.MustCompile(`^[a-z][a-z-]*\.exe$`)

// WindowsPrepareCommand creates t.Dir and a fresh staging dir under it and
// prints them (see remote-prepare.ps1). Runs with no stdin: Windows PowerShell
// started by Win32-OpenSSH cannot read ssh stdin (measured, Ruling R-10).
func WindowsPrepareCommand(t Target) (string, error) {
	if err := CheckRemotePathWindows("install directory", t.Dir); err != nil {
		return "", err
	}
	script := strings.NewReplacer("__DIR__", psQuotes.Replace(t.Dir)).Replace(windowsPrepareScript)
	return EncodePowerShell(script), nil
}

// ParsePrepareOutput returns the staging dir and tar.exe path the prepare step
// printed after its LAST `__quil_prepare__` sentinel. Both are validated with
// CheckRemotePathWindows, and the staging dir must be t.Dir + `\.quil-staging-`
// + 32 lower-case hex (compare the t.Dir prefix case-insensitively).
//
// The LAST sentinel, as in ParseProbe: a profile that echoes could replay a
// forged block ahead of ours. The staging shape is checked rather than trusted
// because the finalize step deletes that directory recursively.
func ParsePrepareOutput(t Target, out string) (staging, tar string, err error) {
	lines := strings.Split(out, "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == prepareSentinel {
			start = i + 1
		}
	}
	if start < 0 {
		return "", "", fmt.Errorf("malformed prepare output: no %s marker", prepareSentinel)
	}
	if len(lines)-start < 2 {
		return "", "", fmt.Errorf("malformed prepare output: got %d lines after the marker, want 2", len(lines)-start)
	}
	staging = strings.TrimSpace(lines[start])
	tar = strings.TrimSpace(lines[start+1])
	if err := CheckRemotePathWindows("tar.exe path", tar); err != nil {
		return "", "", err
	}
	if err := checkStaging(t, staging); err != nil {
		return "", "", err
	}
	return staging, tar, nil
}

// checkStaging accepts exactly t.Dir\.quil-staging-<32 lower-case hex>.
func checkStaging(t Target, staging string) error {
	if err := CheckRemotePathWindows("staging directory", staging); err != nil {
		return err
	}
	prefix := winJoin(t.Dir, stagingPrefix)
	if len(staging) != len(prefix)+stagingGUIDLen ||
		!strings.EqualFold(staging[:len(prefix)], prefix) ||
		!isLowerHex(staging[len(prefix):]) {
		return fmt.Errorf("staging directory %q is not %s<guid>", staging, prefix)
	}
	return nil
}

// WindowsExtractCommand runs the host's own tar.exe on the archive arriving on
// stdin: `<quoted tar> -xf - -C <quoted staging>`, quoted for the host shell.
func WindowsExtractCommand(shell, tar, staging string) (string, error) {
	cmd, err := QuoteCommand(shell, tar)
	if err != nil {
		return "", err
	}
	dir, err := quoteArg(shell, staging)
	if err != nil {
		return "", err
	}
	return cmd + " -xf - -C " + dir, nil
}

// WindowsFinalizeCommand verifies every staged file against src.FileSHA256,
// swaps them into t.Dir (running exes renamed aside to .old/.old.N) and removes
// the staging dir (see remote-finalize.ps1). No stdin.
func WindowsFinalizeCommand(t Target, staging string, src Source) (string, error) {
	if err := CheckRemotePathWindows("install directory", t.Dir); err != nil {
		return "", err
	}
	if err := checkStaging(t, staging); err != nil {
		return "", err
	}
	hashes, err := hashTable(src.FileSHA256)
	if err != nil {
		return "", err
	}
	script := strings.NewReplacer(
		"__DIR__", psQuotes.Replace(t.Dir),
		"__STAGING__", psQuotes.Replace(staging),
		"__HASHES__", hashes,
	).Replace(windowsFinalizeScript)
	return EncodePowerShell(script), nil
}

// hashTable renders files as the body of a PowerShell hashtable literal, in
// sorted key order: `'quil.exe'='<hex>';'quild.exe'='<hex>'`. Every key and
// value is validated rather than escaped — a value outside the shape is a bug
// on this side, never something to smuggle through quoting.
func hashTable(files map[string]string) (string, error) {
	for _, required := range []string{"quil.exe", "quild.exe"} {
		if _, ok := files[required]; !ok {
			return "", fmt.Errorf("archive hashes lack %s", required)
		}
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		sum := files[name]
		if !exeEntryName.MatchString(name) {
			return "", fmt.Errorf("archive entry name %q is not a plain .exe name", name)
		}
		if len(sum) != 64 || !isLowerHex(sum) {
			return "", fmt.Errorf("malformed SHA-256 %q for %s", sum, name)
		}
		parts = append(parts, "'"+name+"'='"+sum+"'")
	}
	return strings.Join(parts, ";"), nil
}

func isLowerHex(s string) bool {
	return s != "" && strings.Trim(s, "0123456789abcdef") == ""
}
