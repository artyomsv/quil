package daemon

import (
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/artyomsv/quil/internal/config"
)

// The spool forwarder.
//
// A sandbox pane's hook writes into the pane's OWN tree
// ($QUIL_HOME/sandbox/panes/<id>/events/<id>.jsonl), because the shared
// $QUIL_HOME/events is the mount that made the first design a cross-pane
// escape. But hookevents.Spool scans one flat directory and cannot see a
// per-pane subtree, so something has to move the lines across.
//
// A forwarder rather than a second Spool: it keeps ONE ingest path, so every
// consumer of an event — the sidebar, the work-state indicator, the MCP
// watchers — sees sandbox and host panes identically.
//
// Four Spool invariants a naive forwarder breaks, all of them documented in
// internal/hookevents/spool.go and each fixed here:
//
//  1. It opens, appends and CLOSES per batch. Spool warns that "a LONG-LIVED
//     producer holding the spool handle open would be stranded" — on Windows
//     NTFS reports a stale directory-entry size, Tick skips the file, and
//     events stop arriving with no error anywhere.
//  2. It CAPS bytes. Spool caps a line but not a file, and rotates only its
//     own and only when idle, so an agent writing a gigabyte would have it
//     copied onto the host's disk.
//  3. It does not REPLAY. Spool.Init unlinks precisely to avoid that; a
//     forwarder resuming from offset zero would push a pane's whole event
//     history into the sidebar on every daemon restart.
//  4. It handles TRUNCATION. size < offset means the agent rewrote the file
//     underneath us, deliberately or otherwise.

// sandboxForwardCap bounds how much one pane's spool may contribute per pass.
//
// Generous against any honest producer — a hook line is a few hundred bytes —
// and small enough that a pane trying to fill the disk is throttled to a
// visible trickle rather than doing it at copy speed.
const sandboxForwardCap = 1 << 20 // 1 MiB

// spoolForwarder copies new hook-event lines from per-pane sandbox spools into
// the daemon's own spool directory.
type spoolForwarder struct {
	mu      sync.Mutex
	offsets map[string]int64
}

func newSpoolForwarder() *spoolForwarder {
	return &spoolForwarder{offsets: map[string]int64{}}
}

// forget drops a pane's offset when its tree goes, so a later pane that draws
// the same id does not resume from a stranger's position.
func (f *spoolForwarder) forget(paneID string) {
	f.mu.Lock()
	delete(f.offsets, paneID)
	f.mu.Unlock()
}

// forward moves one pane's new lines. Returns the number of bytes copied.
//
// Whole lines only. A hook append that is still in flight leaves a partial
// trailing line, and forwarding it would hand the daemon's parser a truncated
// JSON object — the same reason Spool itself stops at the last complete "\n".
// The source is opened through an os.Root over the pane's own tree, which
// refuses symlink and ".." escapes. The file is written by a process inside
// the container, and on a Linux host a symlink planted there resolves on the
// HOST when the daemon follows it — so an unrooted open would read whatever
// the agent pointed the spool at and forward it into the notification sidebar.
func (f *spoolForwarder) forward(rootDir, srcRel, dstPath, paneID string) (int64, error) {
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil // no sandbox tree for this pane
		}
		return 0, err
	}
	defer root.Close()

	src, err := root.Open(srcRel)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil // the pane has produced no events yet
		}
		return 0, err
	}
	defer src.Close()

	info, err := src.Stat()
	if err != nil {
		return 0, err
	}

	f.mu.Lock()
	off := f.offsets[paneID]
	f.mu.Unlock()

	if info.Size() < off {
		// Truncated or replaced under us. Start from the beginning rather
		// than seeking past the end, which would silently forward nothing
		// for the life of the pane.
		off = 0
	}
	if info.Size() == off {
		return 0, nil
	}

	n := info.Size() - off
	capped := n > sandboxForwardCap
	if capped {
		n = sandboxForwardCap
	}
	if _, err := src.Seek(off, io.SeekStart); err != nil {
		return 0, err
	}
	buf := make([]byte, n)
	read, err := io.ReadFull(src, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return 0, err
	}
	buf = buf[:read]

	// Stop at the last complete line.
	end := lastNewline(buf)
	if end < 0 {
		// No complete line in this window. With a cap in play that can also
		// mean one enormous line, which nothing downstream could parse
		// anyway; skip past it so the pane is not wedged forever.
		if capped {
			f.mu.Lock()
			f.offsets[paneID] = off + int64(read)
			f.mu.Unlock()
		}
		return 0, nil
	}
	buf = buf[:end+1]

	// Open, append, CLOSE. Holding this handle is invariant 1.
	dst, err := os.OpenFile(dstPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	written, werr := dst.Write(buf)
	cerr := dst.Close()
	if werr != nil {
		return 0, werr
	}
	if cerr != nil {
		return 0, cerr
	}

	f.mu.Lock()
	f.offsets[paneID] = off + int64(written)
	f.mu.Unlock()
	return int64(written), nil
}

// lastNewline returns the index of the final "\n", or -1.
func lastNewline(b []byte) int {
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] == '\n' {
			return i
		}
	}
	return -1
}

// forwardSandboxSpools moves every live sandbox pane's new events into the
// daemon's spool directory.
//
// Called from the same tick that drives the spool watcher, so a sandbox pane's
// notification latency matches a host pane's.
func (d *Daemon) forwardSandboxSpools() {
	quilDir := config.QuilDir()
	for _, paneID := range d.sandboxPaneIDs() {
		paneRoot := filepath.Join(sandboxRoot(quilDir), "panes", paneID)
		dst := filepath.Join(quilDir, "events", paneID+".jsonl")
		n, err := d.spoolFwd.forward(paneRoot, "events/"+paneID+".jsonl", dst, paneID)
		if err != nil {
			log.Printf("sandbox: pane %s: forward events: %v", paneID, err)
			continue
		}
		if n >= sandboxForwardCap {
			// The pane hit the per-pass cap. Say so once per pass rather
			// than silently throttling: a producer generating a megabyte of
			// events between ticks is a bug in something, and the operator
			// should see which pane.
			log.Printf("sandbox: pane %s: event spool hit the %d-byte per-pass cap", paneID, sandboxForwardCap)
		}
	}
	// An sbx pane's hooks write into the tree shared with its sandbox.
	d.forwardSbxHookFiles()
}
