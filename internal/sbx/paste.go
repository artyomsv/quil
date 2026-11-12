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
// Measured against sbx 0.48: `sbx cp` created missing destination folders.
// Measured against sbx 0.53: it does NOT — a copy into a folder that does not
// exist fails with `destination directory "…" does not exist`. Both keep the
// source's mode (and, on 0.53 from macOS, its numeric uid), so a 0600 file is
// unreadable by the agent, who runs as an ordinary user. A paste is therefore
// three calls: a root mkdir of the folder, the copy, a root chmod of the file.
//
// The chmod runs as root on a path inside a VM the agent controls, so the
// path must be one the agent cannot have prepared or swap out. Every paste
// therefore gets a NEW folder directly under /tmp with an unguessable name,
// made by a root `mkdir` WITHOUT -p: the agent cannot create it first (it does
// not know the name, and a folder already there fails the mkdir), and cannot
// rename or replace it afterwards (root owns it, and /tmp is sticky). A fixed
// /tmp/quil-paste folder would not do — the agent could make it, or a link by
// that name, before the first paste, and point the root chmod anywhere.

// PastePrefix starts the name of every paste folder in the VM's /tmp.
const PastePrefix = "/tmp/quil-paste-"

// PasteFile is the image's name inside its paste folder.
const PasteFile = "image.png"

// pasteTimeout bounds the mkdir, the copy and the chmod together. A stopped
// sandbox is started by none of them, but a running one can still be slow to
// answer.
const pasteTimeout = 30 * time.Second

// mkdirScript makes the paste folder, listable by every user in the VM. No
// -p: a folder already there is refused, never reused. The path is an
// ARGUMENT, never script text.
const mkdirScript = `mkdir -m 755 "$1"`

// readableScript makes the copied file readable by every user in the VM.
const readableScript = `chmod 644 "$1"`

// CopyArgs is the sbx call that copies the host file src into the sandbox.
func CopyArgs(src, name, dst string) []string {
	return []string{"cp", src, name + ":" + dst}
}

// MkdirArgs is the sbx call that makes the paste folder before the copy.
func MkdirArgs(name, dir string) []string {
	return []string{"exec", "-u", "root", name, "sh", "-c", mkdirScript, "sh", dir}
}

// ReadableArgs is the sbx call that makes the copied file readable.
func ReadableArgs(name, file string) []string {
	return []string{"exec", "-u", "root", name, "sh", "-c", readableScript, "sh", file}
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
	if err := c.run(ctx, "", &buf, MkdirArgs(name, dir)...); err != nil {
		return "", fmt.Errorf("sbx: making the image folder in %s: %w%s", name, err, errorLine(buf.String()))
	}
	buf.Reset()
	if err := c.run(ctx, "", &buf, CopyArgs(src, name, dst)...); err != nil {
		return "", fmt.Errorf("sbx: copying the image into %s: %w%s", name, err, errorLine(buf.String()))
	}
	buf.Reset()
	if err := c.run(ctx, "", &buf, ReadableArgs(name, dst)...); err != nil {
		return "", fmt.Errorf("sbx: making the image readable in %s: %w%s", name, err, errorLine(buf.String()))
	}
	return dst, nil
}
