package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/artyomsv/quil/internal/atomicfile"
	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/panehistory"
	"github.com/artyomsv/quil/internal/sbx"
)

// Claude hooks inside an sbx sandbox.
//
// A host claude pane gets its spinner, its notifications, its input history
// and its session-id tracking from hooks: claude runs `quild claude-hook` on
// each event and the hook writes into $QUIL_HOOK_HOME. A docker sandbox pane
// gets the same because the daemon mounts a folder of its own into the
// container. An sbx pane had none of it — the VM is another tool's, and Quil
// put nothing inside.
//
// It now shares two folders of its OWN with the sandbox, at their host paths
// (the sbx convention, so nothing is translated):
//
//   - $QUIL_HOME/sandbox/sbx/<sandbox>/, read-write: one tree per SANDBOX,
//     with sessions/, events/ and history/ under it, where the hook writes.
//     Per sandbox rather than per pane because a sandbox's panes are one
//     trust domain already (they share the VM), and because the tree must
//     outlive every pane: sbx restores a saved mount after stop/start and
//     REFUSES TO START a sandbox whose saved mount has no host folder, so
//     this tree is never deleted by Quil. `quil sandbox` has no remover; the
//     user's remedy for a tree removed by hand is `sbx umount <sandbox>
//     <path>`.
//   - the folder of the Linux hook binary (ensureLinuxQuild), read-only:
//     the agent must run it and must not be able to replace it.
//
// The hook's output comes back the way a docker pane's does: the spool
// forwarder copies new event and history lines out of the tree through an
// os.Root (a symlink the agent plants there would otherwise resolve on the
// host) into the daemon's own directories, and the session record is synced
// the same way — so every consumer, the sidebar, the work-state indicator,
// the restart that resumes the session claude rotated on /clear, reads what
// it always read.
//
// Hooks are a degraded mode here, not a refusal: the session is kept by the
// host-side probe (sandbox_sbx_resume.go), so a pane that cannot get hooks —
// a dev build with no QUIL_SANDBOX_QUILD, a Windows host whose paths mean
// nothing in the VM, a mount sbx refused — still starts, told why on screen.

// sbxHookTree is the folder shared read-write with one sandbox.
func sbxHookTree(quilDir, name string) string {
	return filepath.Join(sandboxRoot(quilDir), "sbx", name)
}

// sbxHooks is what a pane's spawn needs to register hooks.
type sbxHooks struct {
	tree  string // the shared tree, QUIL_HOOK_HOME inside the VM
	quild string // the Linux hook binary, at the same path inside the VM
}

// sbxHookState is the daemon's record of which panes have hooks, runtime
// only: a restored pane prepares again.
type sbxHookState struct {
	mu    sync.Mutex
	panes map[string]sbxHooks
}

// prepareSbxHooks makes the hook tree, fetches the hook binary for the VM's
// architecture (starting the sandbox, which `sbx mount` needs running) and
// shares both. It records the result for the panes named, and reports
// whether they got hooks. It never fails the start: a pane without hooks
// is told so and runs hookless.
func (d *Daemon) prepareSbxHooks(ctx context.Context, c sbx.Client, name string, panes []string) bool {
	if sbxHostOSFn() == "windows" {
		// A Windows host path is not a path inside the Linux VM, and sbx
		// mounts it under a drive-letter root the hook settings would not
		// name. Not supported yet.
		return false
	}
	tree := sbxHookTree(config.QuilDir(), name)
	h, err := d.shareSbxHooks(ctx, c, name, tree)
	if err != nil {
		log.Printf("sbx: sandbox %s: hooks unavailable: %v", name, err)
		for _, id := range panes {
			d.tellPane(id, "Note: claude hooks are not available in sandbox "+name+": "+crlf(err.Error())+"\r\n"+
				"The pane runs without the work spinner, notifications and input history.\r\n")
			d.forgetSbxHooks(id)
		}
		return false
	}
	// Offsets BEFORE the pane is published: the watcher forwards every
	// published pane on its tick, and a pane it saw before its baseline
	// would have its retained file forwarded from zero (M-2).
	for _, id := range panes {
		d.baselineSbxHookOffsets(h.tree, id)
	}
	d.sbxHookState.mu.Lock()
	if d.sbxHookState.panes == nil {
		d.sbxHookState.panes = map[string]sbxHooks{}
	}
	for _, id := range panes {
		d.sbxHookState.panes[id] = h
	}
	d.sbxHookState.mu.Unlock()
	return true
}

func (d *Daemon) shareSbxHooks(ctx context.Context, c sbx.Client, name, tree string) (sbxHooks, error) {
	for _, dir := range []string{tree, filepath.Join(tree, "sessions"), filepath.Join(tree, "events"), filepath.Join(tree, "history")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return sbxHooks{}, fmt.Errorf("create %s: %w", dir, err)
		}
	}
	arch, err := c.Arch(ctx, name)
	if err != nil {
		return sbxHooks{}, err
	}
	quild, err := ensureLinuxQuild(ctx, arch)
	if err != nil {
		return sbxHooks{}, fmt.Errorf("hook binary: %w", err)
	}
	quild, err = filepath.Abs(quild)
	if err != nil {
		return sbxHooks{}, err
	}
	if err := c.Mount(ctx, name, tree); err != nil {
		return sbxHooks{}, err
	}
	if err := c.MountReadOnly(ctx, name, filepath.Dir(quild)); err != nil {
		return sbxHooks{}, err
	}
	log.Printf("sbx: sandbox %s: hooks shared (tree=%s, quild=%s)", name, tree, quild)
	return sbxHooks{tree: tree, quild: quild}, nil
}

// sbxHooksFor is the hook setup recorded for a pane by the preparation.
func (d *Daemon) sbxHooksFor(paneID string) (sbxHooks, bool) {
	d.sbxHookState.mu.Lock()
	defer d.sbxHookState.mu.Unlock()
	h, ok := d.sbxHookState.panes[paneID]
	return h, ok
}

// forgetSbxHooks drops a pane's record and the forwarder's offsets for it.
// The tree stays (see the package comment).
func (d *Daemon) forgetSbxHooks(paneID string) {
	d.sbxHookState.mu.Lock()
	delete(d.sbxHookState.panes, paneID)
	d.sbxHookState.mu.Unlock()
	if d.spoolFwd != nil {
		d.spoolFwd.forget(paneID)
		d.spoolFwd.forget(paneID + "/history")
	}
}

// sbxHookPanes lists the panes with hooks and their trees.
func (d *Daemon) sbxHookPanes() map[string]string {
	d.sbxHookState.mu.Lock()
	defer d.sbxHookState.mu.Unlock()
	out := make(map[string]string, len(d.sbxHookState.panes))
	for id, h := range d.sbxHookState.panes {
		out[id] = h.tree
	}
	return out
}

// sbxSessionRecordMax bounds a synced session record. A hook record is two
// short lines; anything larger is not one.
const sbxSessionRecordMax = 4096

// forwardSbxHookFiles moves what the hooks wrote in each sandbox tree to
// where the daemon reads it: event lines into the spool directory, history
// lines into the history directory, and the session record (the id file and
// its transcript sidecar) into sessions/. Every read goes through os.Root
// over the tree.
func (d *Daemon) forwardSbxHookFiles() {
	if d.spoolFwd == nil {
		return // before Start
	}
	quilDir := config.QuilDir()
	for _, dir := range []string{"events", "history", "sessions"} {
		if err := os.MkdirAll(filepath.Join(quilDir, dir), 0o700); err != nil {
			log.Printf("sbx: create %s: %v", dir, err)
			return
		}
	}
	for paneID, tree := range d.sbxHookPanes() {
		if d.session.Pane(paneID) == nil {
			d.forgetSbxHooks(paneID)
			continue
		}
		rel := paneID + ".jsonl"
		if n, err := d.spoolFwd.forward(tree, "events/"+rel, filepath.Join(quilDir, "events", rel), paneID); err != nil {
			log.Printf("sbx: pane %s: forward events: %v", paneID, err)
		} else if n >= sandboxForwardCap {
			log.Printf("sbx: pane %s: event spool hit the %d-byte per-pass cap", paneID, sandboxForwardCap)
		}
		if _, err := d.spoolFwd.forward(tree, "history/"+rel, filepath.Join(quilDir, "history", rel), paneID+"/history"); err != nil {
			log.Printf("sbx: pane %s: forward history: %v", paneID, err)
		}
		for _, ext := range []string{".id", ".transcript"} {
			if err := syncSbxFile(tree, "sessions/"+paneID+ext, filepath.Join(quilDir, "sessions", paneID+ext)); err != nil {
				log.Printf("sbx: pane %s: sync session record: %v", paneID, err)
			}
		}
	}
}

// syncSbxFile copies one small file out of a tree when its content differs
// from the copy, so a record claude's hook rewrote on /clear reaches the
// daemon's sessions/ before the next restart reads it. A file the agent made
// larger than a record is ignored, not copied.
func syncSbxFile(rootDir, srcRel, dstPath string) error {
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer root.Close()
	src, err := root.Open(srcRel)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer src.Close()
	body, err := io.ReadAll(io.LimitReader(src, sbxSessionRecordMax+1))
	if err != nil {
		return err
	}
	if len(body) > sbxSessionRecordMax {
		return fmt.Errorf("%s is larger than a session record", srcRel)
	}
	if cur, err := os.ReadFile(dstPath); err == nil && string(cur) == string(body) {
		return nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o700); err != nil {
		return err
	}
	return atomicfile.Write(dstPath, body)
}

// sbxPanesWantingHooks is the subset of panes whose plugin uses claude
// sessions: the only hooks an sbx pane registers today are claude's.
func (d *Daemon) sbxPanesWantingHooks(ids []string) []string {
	var out []string
	for _, id := range ids {
		pane := d.session.Pane(id)
		if pane == nil {
			continue
		}
		pane.PluginMu.Lock()
		typ := pane.Type
		pane.PluginMu.Unlock()
		typ, _ = splitSandboxType(typ)
		if p := d.registry.Get(typ); p != nil && p.UsesClaudeSessions() {
			out = append(out, id)
		}
	}
	return out
}

// baselineSbxHookOffsets decides where forwarding of a pane's files starts.
// The tree outlives the daemon, so after a restart the files still carry
// everything the previous daemon delivered, and a forwarder starting at zero
// would replay all of it into a spool Spool.Init just cleared (PR #279
// review, M-2). It runs BEFORE the pane is published to the watcher and
// before the agent starts, so nothing written from here on is skipped.
//
// Events start at the END of the file: they are ephemeral, and an event the
// previous daemon never delivered is one nobody waits for. History is user
// data, and its length is not an acknowledgement — the hook can append a
// prompt after the last tick and before a crash — so it resumes after the
// LAST source entry the daemon's own copy already holds (M-4): everything
// after that is pending and forwarded, nothing before it is forwarded twice.
// The session record is synced by content and needs no baseline.
func (d *Daemon) baselineSbxHookOffsets(tree, paneID string) {
	if d.spoolFwd == nil {
		return
	}
	root, err := os.OpenRoot(tree)
	if err != nil {
		return
	}
	defer root.Close()
	var size int64
	if info, err := root.Stat("events/" + paneID + ".jsonl"); err == nil && info.Mode().IsRegular() {
		size = info.Size()
	}
	d.spoolFwd.setOffset(paneID, size)
	off, err := historyBaseline(root, "history/"+paneID+".jsonl", panehistory.Path(config.QuilDir(), paneID))
	if err != nil {
		log.Printf("sbx: pane %s: history baseline: %v; resuming from the start", paneID, err)
		off = 0
	}
	d.spoolFwd.setOffset(paneID+"/history", off)
}

// sbxHistoryScanMax bounds how much of a source history file the baseline
// reads. A pane's history is prompts; a file past this is not one, and the
// baseline then forwards from the start, which Compact on the copy bounds.
const sbxHistoryScanMax = 64 << 20

// historyBaseline is the byte offset in the source history (srcRel under
// root) just past the last entry the daemon's copy (dstPath) already holds,
// matched by the entry timestamp the hook writes (ts_ms). 0 when the copy
// holds none of them. Reads the source through root: it is agent-written.
func historyBaseline(root *os.Root, srcRel, dstPath string) (int64, error) {
	src, err := root.Open(srcRel)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	defer src.Close()
	body, err := io.ReadAll(io.LimitReader(src, sbxHistoryScanMax+1))
	if err != nil {
		return 0, err
	}
	if len(body) > sbxHistoryScanMax {
		return 0, fmt.Errorf("%s is larger than %d bytes", srcRel, sbxHistoryScanMax)
	}
	delivered := map[int64]bool{}
	if dst, err := os.ReadFile(dstPath); err == nil {
		for _, line := range bytes.Split(dst, []byte("\n")) {
			if ts, ok := historyEntryTS(line); ok {
				delivered[ts] = true
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	var off, pos int64
	for _, line := range bytes.SplitAfter(body, []byte("\n")) {
		pos += int64(len(line))
		if !bytes.HasSuffix(line, []byte("\n")) {
			break // a partial trailing line is still being written
		}
		if ts, ok := historyEntryTS(line); ok && delivered[ts] {
			off = pos
		}
	}
	return off, nil
}

// historyEntryTS is the ts_ms of one history line, the key the hook gives
// every entry. Anything else — blank, malformed, no timestamp — is false.
func historyEntryTS(line []byte) (int64, bool) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return 0, false
	}
	var e struct {
		TS int64 `json:"ts_ms"`
	}
	if err := json.Unmarshal(line, &e); err != nil || e.TS == 0 {
		return 0, false
	}
	return e.TS, true
}
