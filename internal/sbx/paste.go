package sbx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
//
// That chmod runs as root on a path inside a VM the agent controls, so the
// path must be one the agent cannot have prepared or swap out. Every paste
// therefore gets a NEW folder directly under /tmp with an unguessable name:
// the agent cannot create it first (it does not know the name), and cannot
// rename or replace it after `sbx cp` made it (root owns it, and /tmp is
// sticky). A fixed /tmp/quil-paste folder would not do — the agent could make
// it, or a link by that name, before the first paste, and point the root
// chmod anywhere.

// PastePrefix starts the name of every paste folder in the VM's /tmp.
const PastePrefix = "/tmp/quil-paste-"

// PasteFile is the image's name inside its paste folder.
const PasteFile = "image.png"

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

// PastePath is the image's path in the VM for a paste folder id.
func PastePath(id string) string {
	return PastePrefix + id + "/" + PasteFile
}

// ValidPasteID reports whether id can complete a paste folder's name: 16 to
// 64 characters from a set no shell, path or sbx "name:path" spelling reads
// as anything but a name.
func ValidPasteID(id string) bool {
	if len(id) < 16 || len(id) > 64 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		default:
			return false
		}
	}
	return true
}

// ValidPastePath reports whether p is exactly PastePath of a valid id.
func ValidPastePath(p string) bool {
	rest, ok := strings.CutPrefix(p, PastePrefix)
	if !ok {
		return false
	}
	id, ok := strings.CutSuffix(rest, "/"+PasteFile)
	return ok && ValidPasteID(id)
}

// CopyIn copies the host file src into the sandbox as PastePath(id) and
// returns that path. id must be unguessable — see the comment above.
func (c Client) CopyIn(ctx context.Context, name, src, id string) (string, error) {
	if !ValidName(name) {
		return "", errors.New("sbx: invalid sandbox name")
	}
	if !ValidPasteID(id) {
		return "", errors.New("sbx: invalid folder name for a pasted image")
	}
	dir := PastePrefix + id
	dst := PastePath(id)
	ctx, cancel := context.WithTimeout(ctx, pasteTimeout)
	defer cancel()
	var buf bytes.Buffer
	if err := c.run(ctx, "", &buf, CopyArgs(src, name, dst)...); err != nil {
		return "", fmt.Errorf("sbx: copying the image into %s: %w%s", name, err, errorLine(buf.String()))
	}
	buf.Reset()
	if err := c.run(ctx, "", &buf, ReadableArgs(name, dir, dst)...); err != nil {
		return "", fmt.Errorf("sbx: making the image readable in %s: %w%s", name, err, errorLine(buf.String()))
	}
	return dst, nil
}
