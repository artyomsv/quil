// Package atomicfile writes a file so a reader sees the old bytes or the new
// bytes, never a mix: a temp file with a unique name in the same folder, then
// a rename. Stdlib only, so every package may use it.
//
// The name is unique because quil's files have several writers in separate
// processes (the TUI, quil web, the daemon): a shared "<path>.tmp" let one
// writer truncate or remove another's half-written copy, and a change was lost
// with no error. os.CreateTemp opens it with O_EXCL, so a link planted at any
// name is never followed.
package atomicfile

import (
	"os"
	"path/filepath"
	"time"
)

// TempPattern is the os.CreateTemp pattern after the target's base name: the
// temp is "<base>.<digits>.tmp". ipc.IsQuilHomeEntry accepts it through the
// target's own stem, so a crash leftover in QUIL_HOME never makes ProtectDir
// refuse the folder (internal/ipc/acl.go). Do not change it to a dot- or
// dash-prefixed name.
const TempPattern = ".*.tmp"

// Write replaces path with data, mode 0600. On Windows a rename fails while
// another process holds the target open, so it is retried once after a short
// pause. The temp file is removed on every failure.
func Write(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+TempPattern)
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.Write(data)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmp, 0o600)
	}
	if werr == nil {
		if werr = renameFn(tmp, path); werr != nil {
			time.Sleep(renameRetryDelay)
			werr = renameFn(tmp, path)
		}
	}
	if werr != nil {
		os.Remove(tmp)
		return werr
	}
	return nil
}

// renameFn is os.Rename; a test replaces it to make an attempt fail.
var renameFn = os.Rename

const renameRetryDelay = 50 * time.Millisecond
