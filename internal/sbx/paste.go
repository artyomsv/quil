package sbx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
)

// Copying a pasted image into a sandbox.
//
// The TUI saves a pasted clipboard image on its own machine and types the
// file's path into the pane. Inside a sandbox that path names nothing: the VM
// mounts only its workspaces. CopyIn puts the image in the VM's own /tmp
// instead, so the agent is handed a path that exists where it runs. One image
// goes to one sandbox — mounting the paste folder instead would hand every
// sandbox every screenshot ever pasted, in any pane.
//
// Measured against sbx 0.48: `sbx cp` creates missing destination folders,
// keeps the source's mode and leaves root as the owner of the file and the
// folders. The agent runs as an ordinary user and is refused a 0600 file, so
// the copy is followed by a chmod run as root.

// PasteDir is the folder inside the VM pasted images are copied to.
const PasteDir = "/tmp/quil-paste"

// pasteTimeout bounds the copy and the chmod together. A stopped sandbox is
// started by neither, but a running one can still be slow to answer.
const pasteTimeout = 30 * time.Second

// pasteScript makes the folder listable and the file readable by every user
// in the VM. Both paths are ARGUMENTS, never script text.
const pasteScript = `chmod 755 "$1" && chmod 644 "$2"`

// CopyArgs is the sbx call that copies the host file src into the sandbox.
func CopyArgs(src, name, dst string) []string {
	return []string{"cp", src, name + ":" + dst}
}

// ReadableArgs is the sbx call that makes the copied file readable.
func ReadableArgs(name, dir, file string) []string {
	return []string{"exec", "-u", "root", name, "sh", "-c", pasteScript, "sh", dir, file}
}

// ValidPasteName reports whether base can name a file in PasteDir: one plain
// path element, no hidden file, nothing a shell or sbx's "name:path" spelling
// could read as more than a file name.
func ValidPasteName(base string) bool {
	if base == "" || len(base) > 128 || strings.HasPrefix(base, ".") {
		return false
	}
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

// CopyIn copies the host file src into the sandbox as PasteDir/base and
// returns its path inside the VM.
func (c Client) CopyIn(ctx context.Context, name, src, base string) (string, error) {
	if !ValidName(name) {
		return "", errors.New("sbx: invalid sandbox name")
	}
	if !ValidPasteName(base) {
		return "", errors.New("sbx: invalid file name for a pasted image")
	}
	dst := path.Join(PasteDir, base)
	ctx, cancel := context.WithTimeout(ctx, pasteTimeout)
	defer cancel()
	var buf bytes.Buffer
	if err := c.run(ctx, "", &buf, CopyArgs(src, name, dst)...); err != nil {
		return "", fmt.Errorf("sbx: copying the image into %s: %w%s", name, err, errorLine(buf.String()))
	}
	buf.Reset()
	if err := c.run(ctx, "", &buf, ReadableArgs(name, PasteDir, dst)...); err != nil {
		return "", fmt.Errorf("sbx: making the image readable in %s: %w%s", name, err, errorLine(buf.String()))
	}
	return dst, nil
}
