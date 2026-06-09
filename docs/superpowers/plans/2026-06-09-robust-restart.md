# Robust Restart Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make daemon restart with a large workspace robust — defer pane spawns (lazy restore), bound log growth (rotation), and stop the daemon from force-closing a busy-but-alive TUI (IPC backpressure hardening).

**Architecture:** Three independent workstreams in one PR, built **B → A → C**. B (log rotation) is isolated; A (lazy restore) mirrors the existing `Muted` per-pane-flag plumbing end-to-end and adds a `Pending` deferral state with spawn-on-first-access; C (IPC hardening) splits each connection's single send queue into a critical queue + a droppable live-output queue drained by a priority select.

**Tech Stack:** Go 1.25, Bubble Tea v2 TUI, length-prefixed JSON IPC over a Unix socket, TOML config. No local Go — all builds/tests run in Docker via `./scripts/dev.sh`.

**Design spec:** `docs/superpowers/specs/2026-06-09-robust-restart-design.md`

### How to run tests

Full suite (what every verification step below uses):
```bash
./scripts/dev.sh test
```
Optional targeted run of one package/test (same Docker image the script uses):
```bash
docker run --rm -v "${PWD}:/src" -v quil-gomod:/go/pkg/mod -w //src golang:1.25-alpine \
  go test ./internal/ipc/ -run TestName -v
```
Race detector: `./scripts/dev.sh test-race`. Vet: `./scripts/dev.sh vet`.

---

## File Structure

**Workstream B — Log rotation**
- Create `internal/logger/rotate.go` — `RotatingWriter` (size-based rotation + prune).
- Create `internal/logger/rotate_test.go` — rotation/prune/seed tests.
- Modify `internal/config/config.go:145-146` — default `MaxSizeMB: 5`, `MaxFiles: 10`.
- Modify `cmd/quild/main.go:89` — `initLogging` uses `RotatingWriter`.
- Modify `cmd/quil/main.go:236` — TUI log file uses `RotatingWriter`.
- Modify `docs/architecture.md:432`, `docs/roadmap.md:213` — mark rotation implemented.

**Workstream A — Lazy pane restoration**
- Modify `internal/daemon/session.go:23` — add `Eager`, `Pending` to `Pane`.
- Modify `internal/ipc/protocol.go:160,~185` — add `Eager *bool` to `UpdatePanePayload`, `Pending bool` to `PaneInfo`.
- Modify `internal/daemon/daemon.go` — restore-read eager (`:480/:494`), state/persist map (`:1387`), `handleUpdatePane` (`:1066`), selective `respawnPanes` (`:546`) + `ensurePaneSpawned`/`ensureTabSpawned`, `handleSwitchTab` trigger (`:831`), `Running` fix + spawn-on-access in MCP handlers, `PaneInfo.Pending`.
- Modify `internal/config/config.go:130,202` — add `ToggleEager` keybinding field + default.
- Modify `internal/tui/model.go` — `PaneInfo`/`PaneModel` `Eager`+`Pending`, state parse (`:2643`), apply (`:1904,:1930`), key case (`:1423`), `toggleActivePaneEager`, `tabLabel` marker (`:2162`).
- Modify `internal/tui/pane.go:29` — add `Eager bool` to `PaneModel`.
- Create `internal/daemon/lazy_restore_test.go` — selection + spawn-on-access + ghost-preservation tests.
- Modify `docs/keybindings.md`, `docs/configuration.md`, `docs/features.md` — document eager/lazy.

**Workstream C — IPC backpressure hardening**
- Modify `internal/ipc/server.go` — dual-queue `Conn` (`critCh`/`outCh`), `enqueue(frame, droppable)`, priority `sendLoop`, `Broadcast` classification.
- Create `internal/ipc/lossy_test.go` — droppable-drop + output-flood-doesn't-close tests.

---

# Workstream B — Log Rotation

## Task B1: RotatingWriter

**Files:**
- Create: `internal/logger/rotate.go`
- Test: `internal/logger/rotate_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/logger/rotate_test.go`:

```go
package logger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotatingWriter_RotatesAtMaxSize(t *testing.T) {
	dir := t.TempDir()
	w, err := NewRotatingWriter(dir, "quild.log", 100, 10)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer w.Close()

	if _, err := w.Write([]byte(strings.Repeat("a", 60))); err != nil {
		t.Fatalf("write1: %v", err)
	}
	// +60 crosses 100 → rotation happens before this write lands.
	if _, err := w.Write([]byte(strings.Repeat("b", 60))); err != nil {
		t.Fatalf("write2: %v", err)
	}

	archives, _ := filepath.Glob(filepath.Join(dir, "quild-*.log"))
	if len(archives) != 1 {
		t.Fatalf("want 1 archive, got %d: %v", len(archives), archives)
	}
	if arch, _ := os.ReadFile(archives[0]); string(arch) != strings.Repeat("a", 60) {
		t.Errorf("archive content = %q", arch)
	}
	if cur, _ := os.ReadFile(filepath.Join(dir, "quild.log")); string(cur) != strings.Repeat("b", 60) {
		t.Errorf("active content = %q", cur)
	}
}

func TestRotatingWriter_PrunesBeyondMaxFiles(t *testing.T) {
	dir := t.TempDir()
	w, err := NewRotatingWriter(dir, "quild.log", 10, 2)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer w.Close()
	for i := 0; i < 5; i++ { // each 11-byte write forces a rotation
		if _, err := w.Write([]byte(strings.Repeat("x", 11))); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	archives, _ := filepath.Glob(filepath.Join(dir, "quild-*.log"))
	if len(archives) > 2 {
		t.Errorf("want <=2 archives after prune, got %d", len(archives))
	}
}

func TestRotatingWriter_RotatesOversizedExistingFileOnOpen(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "quil.log"), []byte(strings.Repeat("z", 200)), 0o600)
	w, err := NewRotatingWriter(dir, "quil.log", 100, 10)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer w.Close()
	archives, _ := filepath.Glob(filepath.Join(dir, "quil-*.log"))
	if len(archives) != 1 {
		t.Fatalf("want 1 archive from oversized-on-open, got %d", len(archives))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `./scripts/dev.sh test`
Expected: FAIL — `undefined: NewRotatingWriter`.

- [ ] **Step 3: Implement RotatingWriter**

Create `internal/logger/rotate.go`:

```go
package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// RotatingWriter is an io.WriteCloser that writes to dir/base and, when the
// active file would exceed maxSize bytes, rotates it to a timestamped archive
// (stem-YYYYMMDD-HHMMSS.ext) and opens a fresh base file. At most maxFiles
// archives are kept; older ones are pruned by modification time. Safe for
// concurrent Write — the logger fans in from many goroutines.
type RotatingWriter struct {
	dir      string
	base     string // e.g. "quild.log"
	maxSize  int64
	maxFiles int

	mu   sync.Mutex
	f    *os.File
	size int64
}

// NewRotatingWriter opens dir/base for appending. If the existing file already
// exceeds maxSizeBytes it is rotated immediately. maxSizeBytes <= 0 or
// maxFiles <= 0 are coerced to safe minimums so a misconfigured value can never
// disable writing entirely.
func NewRotatingWriter(dir, base string, maxSizeBytes int64, maxFiles int) (*RotatingWriter, error) {
	if maxSizeBytes <= 0 {
		maxSizeBytes = 5 << 20
	}
	if maxFiles <= 0 {
		maxFiles = 10
	}
	w := &RotatingWriter{dir: dir, base: base, maxSize: maxSizeBytes, maxFiles: maxFiles}
	if err := w.open(); err != nil {
		return nil, err
	}
	if w.size > w.maxSize {
		if err := w.rotate(); err != nil {
			return nil, err
		}
	}
	return w, nil
}

func (w *RotatingWriter) open() error {
	if err := os.MkdirAll(w.dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(w.dir, w.base), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	w.f = f
	w.size = info.Size()
	return nil
}

func (w *RotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.size+int64(len(p)) > w.maxSize {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

// rotate closes the active file, renames it to a timestamped archive, opens a
// fresh base file, and prunes old archives. Caller must hold w.mu.
func (w *RotatingWriter) rotate() error {
	if w.f != nil {
		w.f.Close()
		w.f = nil
	}
	ext := filepath.Ext(w.base)           // ".log"
	stem := w.base[:len(w.base)-len(ext)] // "quild"
	ts := time.Now().Format("20060102-150405")
	dest := filepath.Join(w.dir, fmt.Sprintf("%s-%s%s", stem, ts, ext))
	for i := 1; ; i++ { // collision suffix if two rotations land in the same second
		if _, err := os.Stat(dest); os.IsNotExist(err) {
			break
		}
		dest = filepath.Join(w.dir, fmt.Sprintf("%s-%s-%d%s", stem, ts, i, ext))
	}
	_ = os.Rename(filepath.Join(w.dir, w.base), dest)
	if err := w.open(); err != nil {
		return err
	}
	w.prune(stem, ext)
	return nil
}

// prune deletes all but the newest maxFiles archives (by modification time, so
// same-second collision suffixes can't fool a name sort).
func (w *RotatingWriter) prune(stem, ext string) {
	matches, _ := filepath.Glob(filepath.Join(w.dir, stem+"-*"+ext))
	if len(matches) <= w.maxFiles {
		return
	}
	type fi struct {
		path string
		mod  time.Time
	}
	infos := make([]fi, 0, len(matches))
	for _, m := range matches {
		st, err := os.Stat(m)
		if err != nil {
			continue
		}
		infos = append(infos, fi{m, st.ModTime()})
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].mod.Before(infos[j].mod) })
	for _, old := range infos[:len(infos)-w.maxFiles] {
		_ = os.Remove(old.path)
	}
}

func (w *RotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `./scripts/dev.sh test`
Expected: PASS (all three new tests + existing suite).

- [ ] **Step 5: Commit**

```bash
git add internal/logger/rotate.go internal/logger/rotate_test.go
git commit -m "feat(logger): add size-based RotatingWriter with prune"
```

## Task B2: Wire rotation into config defaults and both mains

**Files:**
- Modify: `internal/config/config.go:145-146`
- Modify: `cmd/quild/main.go:89-105` and its caller
- Modify: `cmd/quil/main.go:228-240`

- [ ] **Step 1: Flip the config defaults to 5 MB / 10 files**

In `internal/config/config.go`, the `Logging` block of `Default()` currently reads:

```go
		Logging: LoggingConfig{
			Level:     "info",
			MaxSizeMB: 10,
			MaxFiles:  3,
		},
```

Change to:

```go
		Logging: LoggingConfig{
			Level:     "info",
			MaxSizeMB: 5,
			MaxFiles:  10,
		},
```

- [ ] **Step 2: Switch the daemon log to RotatingWriter**

In `cmd/quild/main.go`, `initLogging` currently is:

```go
func initLogging(level string) *os.File {
	logDir := config.QuilDir()
	if logDir == "" {
		return nil
	}
	os.MkdirAll(logDir, 0700)
	f, err := os.OpenFile(filepath.Join(logDir, "quild.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil
	}
	logger.Init(level, f)
	return f
}
```

Replace with (note the return type changes to `io.Closer`):

```go
func initLogging(level string, maxSizeMB, maxFiles int) io.Closer {
	logDir := config.QuilDir()
	if logDir == "" {
		return nil
	}
	w, err := logger.NewRotatingWriter(logDir, "quild.log", int64(maxSizeMB)<<20, maxFiles)
	if err != nil {
		return nil
	}
	logger.Init(level, w)
	return w
}
```

Add `"io"` to the import block. Then update the single call site of `initLogging(...)` in `main()` — it currently passes just the level; change it to pass the config values. The daemon's config is loaded into a `cfg` variable before this call; locate that call (`initLogging(level)` or `initLogging(cfg.Logging.Level)`) and change it to:

```go
	closer := initLogging(cfg.Logging.Level, cfg.Logging.MaxSizeMB, cfg.Logging.MaxFiles)
	if closer != nil {
		defer closer.Close()
	}
```

(If the existing code assigned the `*os.File` to a variable and deferred `f.Close()`, replace that variable's type/use with `closer` as above. If `cfg` is not yet in scope at the call site, load it via the same `config.Load()` the daemon already calls earlier in `main`.)

- [ ] **Step 3: Switch the TUI log to RotatingWriter**

In `cmd/quil/main.go`, this block:

```go
	logFile, err := os.OpenFile(logPath, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err == nil && logFile != nil {
		logger.Init(logLevel, logFile)
		defer logFile.Close()
	}
```

Replace with:

```go
	logWriter, err := logger.NewRotatingWriter(logDir, "quil.log", int64(cfg.Logging.MaxSizeMB)<<20, cfg.Logging.MaxFiles)
	if err == nil && logWriter != nil {
		logger.Init(logLevel, logWriter)
		defer logWriter.Close()
	}
```

The `logPath := filepath.Join(logDir, "quil.log")` line above it becomes unused — remove it. `cfg` is already in scope here (`cfg.Logging.Level` is used two lines above).

- [ ] **Step 4: Build to verify both mains compile**

Run: `./scripts/dev.sh build`
Expected: all 6 binaries build with no errors.

- [ ] **Step 5: Update the reserved-field docs**

In `docs/architecture.md`, the line at ~432 currently says `LoggingConfig.MaxSizeMB` / `MaxFiles` are "reserved fields documented as 'not yet honored' ... planned via lumberjack". Replace with:

```
- `LoggingConfig.MaxSizeMB` (default 5) / `MaxFiles` (default 10) drive native log rotation in `internal/logger/rotate.go` (`RotatingWriter`): the active `quild.log` / `quil.log` is rotated to a `stem-YYYYMMDD-HHMMSS.log` archive once it would exceed `MaxSizeMB`, keeping the newest `MaxFiles` archives. No external dependency (lumberjack was the original plan; a ~120-line in-tree writer was simpler).
```

In `docs/roadmap.md`, the line at ~213 (`Log rotation — wire MaxSizeMB/MaxFiles ... via lumberjack`) — move it to the "done" section or strike it as completed in this PR, matching the file's existing done/planned convention.

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go cmd/quild/main.go cmd/quil/main.go docs/architecture.md docs/roadmap.md
git commit -m "feat(logger): honor MaxSizeMB/MaxFiles, rotate at 5MB keep 10"
```

---

# Workstream A — Lazy Pane Restoration

## Task A1: Eager flag plumbing (daemon + IPC), mirroring Muted

**Files:**
- Modify: `internal/daemon/session.go:53` (Pane struct)
- Modify: `internal/ipc/protocol.go:167` (UpdatePanePayload)
- Modify: `internal/daemon/daemon.go:480,494` (restore read), `:1387` (state/persist map), `:1066` (handleUpdatePane)
- Test: `internal/daemon/event_mute_test.go` pattern → new test in `internal/daemon/lazy_restore_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/daemon/lazy_restore_test.go` with the Eager-toggle test (mirrors `TestHandleUpdatePane_MutedFieldToggle`):

```go
package daemon

import (
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

// callUpdatePaneEager drives handleUpdatePane with just the Eager field set.
func callUpdatePaneEager(t *testing.T, d *Daemon, paneID string, eager bool) {
	t.Helper()
	msg, err := ipc.NewMessage(ipc.MsgUpdatePane, ipc.UpdatePanePayload{
		PaneID: paneID,
		Eager:  &eager,
	})
	if err != nil {
		t.Fatalf("build msg: %v", err)
	}
	d.handleUpdatePane(msg)
}

func TestHandleUpdatePane_EagerFieldToggle(t *testing.T) {
	d := newTestDaemon(t) // see helper note below
	d.session.RestoreTab(&Tab{ID: "tab-00000001", Name: "t"}, []*Pane{
		{ID: "pane-00000001", TabID: "tab-00000001", Type: "terminal", Name: "keep"},
	})

	callUpdatePaneEager(t, d, "pane-00000001", true)
	if p := d.session.Pane("pane-00000001"); !p.Eager {
		t.Errorf("after update: Eager should be true")
	}
	if p := d.session.Pane("pane-00000001"); p.Name != "keep" {
		t.Errorf("Name should be preserved when only Eager is updated: got %q", p.Name)
	}

	callUpdatePaneEager(t, d, "pane-00000001", false)
	if p := d.session.Pane("pane-00000001"); p.Eager {
		t.Errorf("after second update: Eager should be false")
	}
}
```

> **Helper note:** reuse whatever constructor `event_mute_test.go` uses to obtain a `*Daemon` with a live `session`. If it builds the daemon inline rather than via a `newTestDaemon` helper, copy that exact construction here instead of calling `newTestDaemon`. Check `event_mute_test.go:69-97` for the precise pattern before writing this test.

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test`
Expected: FAIL — `UpdatePanePayload` has no field `Eager` / `Pane` has no field `Eager`.

- [ ] **Step 3: Add the Eager + Pending fields to Pane**

In `internal/daemon/session.go`, right after the `Muted bool` field (line 53) add:

```go
	// Eager, when true, makes this pane respawn immediately on daemon restart
	// instead of being deferred until first access. Toggled via
	// MsgUpdatePane{Eager: true} (default keybinding Alt+Shift+E), persisted in
	// the workspace snapshot, and marked on the tab label. Read under PluginMu.
	Eager bool
	// Pending is true between restore and first spawn for a deferred pane: the
	// model + ghost buffer exist but no PTY has been created yet. Runtime-only,
	// never persisted. Cleared by ensurePaneSpawned.
	Pending bool
```

- [ ] **Step 4: Add Eager to UpdatePanePayload**

In `internal/ipc/protocol.go`, the `UpdatePanePayload` struct (line 160) gains a field after `Muted`:

```go
	// Eager is a pointer for the same nil-vs-false tri-state reason as Muted.
	Eager *bool `json:"eager,omitempty"`
```

- [ ] **Step 5: Read Eager on restore**

In `internal/daemon/daemon.go`, beside `muted, _ := paneData["muted"].(bool)` (line 480) add:

```go
				eager, _ := paneData["eager"].(bool)
```

and in the `pane := &Pane{...}` literal (after `Muted: muted,` at line 494) add:

```go
					Eager:        eager,
```

- [ ] **Step 6: Write Eager into the shared state/persist map**

In `internal/daemon/daemon.go`, inside `workspaceStateFromSnapshot`, beside the muted write (lines 1387-1389):

```go
				if pane.Muted {
					paneData["muted"] = true
				}
```

add (still inside the `PluginMu` lock span, before `pane.PluginMu.Unlock()` at line 1390):

```go
				if pane.Eager {
					paneData["eager"] = true
				}
```

This single site feeds both the TUI broadcast (for the tab marker) and `workspace.json` (persistence).

- [ ] **Step 7: Handle Eager in handleUpdatePane**

In `internal/daemon/daemon.go`, after the `Muted` block (lines 1066-1071) and before `d.broadcastState()`:

```go
	if payload.Eager != nil {
		pane.PluginMu.Lock()
		pane.Eager = *payload.Eager
		pane.PluginMu.Unlock()
		log.Printf("pane %s: eager=%v", pane.ID, *payload.Eager)
	}
```

- [ ] **Step 8: Run test to verify it passes**

Run: `./scripts/dev.sh test`
Expected: PASS — `TestHandleUpdatePane_EagerFieldToggle` green, full suite green.

- [ ] **Step 9: Commit**

```bash
git add internal/daemon/session.go internal/ipc/protocol.go internal/daemon/daemon.go internal/daemon/lazy_restore_test.go
git commit -m "feat(daemon): add per-pane Eager flag (persisted, mirrors Muted)"
```

## Task A2: Selective respawn + spawn-on-first-access

**Files:**
- Modify: `internal/daemon/daemon.go:546` (respawnPanes), `:831` (handleSwitchTab)
- Test: `internal/daemon/lazy_restore_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/daemon/lazy_restore_test.go`:

```go
func TestRespawnPanes_DefersNonActiveNonEager(t *testing.T) {
	d := newTestDaemon(t)
	// Two tabs; tab-A is active. tab-B has a normal pane and an eager pane.
	d.session.RestoreTab(&Tab{ID: "tab-0000000a", Name: "A"}, []*Pane{
		{ID: "pane-0000000a", TabID: "tab-0000000a", Type: "terminal"},
	})
	d.session.RestoreTab(&Tab{ID: "tab-0000000b", Name: "B"}, []*Pane{
		{ID: "pane-0000000b", TabID: "tab-0000000b", Type: "terminal"},
		{ID: "pane-0000000e", TabID: "tab-0000000b", Type: "terminal", Eager: true},
	})
	d.session.SwitchTab("tab-0000000a")

	d.respawnPanes()

	// Active-tab pane spawned.
	if p := d.session.Pane("pane-0000000a"); p.PTY == nil || p.Pending {
		t.Errorf("active-tab pane should be spawned, not pending")
	}
	// Eager pane spawned even though its tab is inactive.
	if p := d.session.Pane("pane-0000000e"); p.PTY == nil || p.Pending {
		t.Errorf("eager pane should be spawned")
	}
	// Non-active, non-eager pane deferred.
	if p := d.session.Pane("pane-0000000b"); p.PTY != nil || !p.Pending {
		t.Errorf("non-active non-eager pane should be pending, not spawned")
	}
}

func TestEnsurePaneSpawned_IsIdempotent(t *testing.T) {
	d := newTestDaemon(t)
	pane := &Pane{ID: "pane-0000000c", TabID: "tab-0000000c", Type: "terminal", Pending: true}
	d.session.RestoreTab(&Tab{ID: "tab-0000000c", Name: "C"}, []*Pane{pane})

	d.ensurePaneSpawned(pane)
	first := pane.PTY
	if first == nil || pane.Pending {
		t.Fatalf("first ensure should spawn and clear Pending")
	}
	d.ensurePaneSpawned(pane) // second call must be a no-op
	if pane.PTY != first {
		t.Errorf("second ensure must not respawn (PTY pointer changed)")
	}
}
```

> **Spawn in tests:** `spawnPane` starts a real child process. If `newTestDaemon` cannot spawn real PTYs in the Docker test environment, gate these two tests with the same mechanism the existing daemon tests use for spawn paths (check `daemon_test.go` / `spawn_args_test.go` for a `testing.Short()` skip or a fake PTY injection point) and follow that exact pattern. Do not invent a new fake.

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test`
Expected: FAIL — `d.ensurePaneSpawned` undefined; `respawnPanes` still spawns everything.

- [ ] **Step 3: Refactor respawnPanes to be selective + extract spawnRestoredPane**

In `internal/daemon/daemon.go`, replace the body of `respawnPanes` (lines 546-573). The current per-pane spawn block becomes `spawnRestoredPane`, and `respawnPanes` decides eager vs deferred:

```go
// respawnPanes starts processes for restored panes. Only the active tab's
// panes and panes flagged Eager are spawned immediately; everything else is
// marked Pending and spawned lazily on first access (tab switch or MCP op).
// This keeps a large-workspace restart from launching N heavy children at once.
func (d *Daemon) respawnPanes() {
	active := d.session.ActiveTab()
	for _, tab := range d.session.Tabs() {
		for _, pane := range d.session.Panes(tab.ID) {
			if pane.PTY != nil {
				continue // Already has a PTY
			}
			if tab.ID == active || pane.Eager {
				d.spawnRestoredPane(pane)
			} else {
				pane.Pending = true
				log.Printf("respawn: deferring pane %s (type=%s, tab=%s)", pane.ID, pane.Type, tab.ID)
			}
		}
	}
}

// spawnRestoredPane spawns a single restored pane, applying the saved-cwd
// sanity check and the fallback-to-terminal recovery. Extracted from
// respawnPanes so the lazy-spawn path (ensurePaneSpawned) reuses it verbatim.
func (d *Daemon) spawnRestoredPane(pane *Pane) {
	ptySession := newRestoredPTY(pane)
	if pane.CWD != "" {
		if info, err := os.Stat(pane.CWD); err != nil || !info.IsDir() {
			log.Printf("pane %s: saved cwd %q gone, using default", pane.ID, pane.CWD)
			pane.CWD = ""
		}
	}
	if err := d.spawnPane(pane, ptySession, true); err != nil {
		log.Printf("respawn pane %s (type=%s): %v — falling back to terminal", pane.ID, pane.Type, err)
		pane.Type = "terminal"
		ptySession2 := newRestoredPTY(pane)
		if err := d.spawnPane(pane, ptySession2, false); err != nil {
			log.Printf("fallback shell for pane %s also failed: %v", pane.ID, err)
		}
	} else {
		log.Printf("respawned pane %s (type=%s, cwd=%s, size=%dx%d)", pane.ID, pane.Type, pane.CWD, pane.Cols, pane.Rows)
	}
}

// ensurePaneSpawned spawns a deferred pane on first access. Idempotent and
// race-safe: the double-check under PluginMu means a tab switch and an MCP op
// hitting the same pending pane spawn it exactly once.
func (d *Daemon) ensurePaneSpawned(pane *Pane) {
	pane.PluginMu.Lock()
	defer pane.PluginMu.Unlock()
	if pane.PTY != nil || !pane.Pending {
		return
	}
	d.spawnRestoredPane(pane)
	pane.Pending = false
}

// ensureTabSpawned spawns every deferred pane in a tab (handles splits).
func (d *Daemon) ensureTabSpawned(tabID string) {
	for _, pane := range d.session.Panes(tabID) {
		d.ensurePaneSpawned(pane)
	}
}
```

> **Lock check:** `spawnPane` and the daemon's PTY-output goroutine also take `pane.PluginMu`. Verify `spawnRestoredPane`/`spawnPane` do **not** themselves lock `PluginMu` (which would deadlock under `ensurePaneSpawned`'s held lock). From the read of `spawnPane` (daemon.go:1746) and `spawnRestoredPane` above, neither locks `PluginMu` on the calling goroutine before the PTY-output goroutine starts — confirm during implementation by reading `spawnPane` fully. If `spawnPane` does lock `PluginMu`, move the `Pending=false` assignment + double-check into a tiny dedicated `spawnMu sync.Mutex` on `Pane` instead of reusing `PluginMu`.

- [ ] **Step 4: Trigger lazy spawn on tab switch**

In `internal/daemon/daemon.go`, `handleSwitchTab` (line 831) currently:

```go
	log.Printf("tab switch: %s", payload.TabID)
	d.session.SwitchTab(payload.TabID)
	d.broadcastState()
	d.requestSnapshot()
```

Insert the spawn before the broadcast so the now-live panes are in the broadcast state:

```go
	log.Printf("tab switch: %s", payload.TabID)
	d.session.SwitchTab(payload.TabID)
	d.ensureTabSpawned(payload.TabID)
	d.broadcastState()
	d.requestSnapshot()
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `./scripts/dev.sh test`
Expected: PASS — selection + idempotency tests green.

- [ ] **Step 6: Commit**

```bash
git add internal/daemon/daemon.go internal/daemon/lazy_restore_test.go
git commit -m "feat(daemon): lazy restore — defer non-active panes, spawn on switch"
```

## Task A3: nil-PTY guards — Running flag, PaneInfo.Pending, MCP spawn-on-access

**Files:**
- Modify: `internal/ipc/protocol.go` (PaneInfo)
- Modify: `internal/daemon/daemon.go:2390` (handleListPanesReq), and the MCP pane-targeting handlers
- Test: `internal/daemon/lazy_restore_test.go`

- [ ] **Step 1: Add Pending to PaneInfo and fix Running**

In `internal/ipc/protocol.go`, the `PaneInfo` struct (near line 185, the one with `Running bool`) gains:

```go
	Pending bool `json:"pending,omitempty"`
```

In `internal/daemon/daemon.go`, `handleListPanesReq` (line 2390) currently:

```go
			running := pane.ExitCode == nil
```

Change to:

```go
			running := pane.PTY != nil && pane.ExitCode == nil
```

and add `Pending: pane.Pending,` to the `ipc.PaneInfo{...}` literal (after `Running: running,` at line 2399).

- [ ] **Step 2: Spawn-on-access in interactive MCP handlers**

The daemon request handlers that operate on a live PTY must spawn a deferred target first. At the top of each of these handlers in `internal/daemon/daemon.go`, immediately after the handler resolves its `pane` via `d.session.Pane(<id>)` and nil-checks it, insert:

```go
	d.ensurePaneSpawned(pane)
```

Apply to: `handleReadPaneOutputReq`, `handleSendToPane` / pane-input request handler, `handleSendKeys` (if a distinct handler), `handleScreenshotPaneReq`, `handleRestartPaneReq`, `handleSetActivePane`. Do **not** add it to `handleListPanesReq`, `handlePaneStatusReq`, or the memory-report handler — those must report deferred state without booting every pane.

> **Find the exact handlers:** grep `func (d *Daemon) handle` in `daemon.go` for the MCP request handlers and confirm each resolves a single `*Pane` before adding the call. For any handler that reaches the pane through a helper (e.g. `respondTo`-style indirection), add the `ensurePaneSpawned` at the point the `*Pane` is in hand.

- [ ] **Step 3: Guard the non-MCP PTY paths**

Confirm `handleResizePane` and `handlePaneInput` (which run for the *active, visible* pane and therefore should never see a deferred pane) still nil-check `pane.PTY` before dereferencing. If either dereferences `pane.PTY` unconditionally, wrap with `if pane.PTY != nil`. (A deferred pane can't be the active visible pane, but resize broadcasts can arrive during the spawn window.)

- [ ] **Step 4: Write/extend the test**

Append to `internal/daemon/lazy_restore_test.go`:

```go
func TestListPanes_DeferredPaneReportsNotRunning(t *testing.T) {
	d := newTestDaemon(t)
	d.session.RestoreTab(&Tab{ID: "tab-0000000d", Name: "D"}, []*Pane{
		{ID: "pane-0000000d", TabID: "tab-0000000d", Type: "terminal", Pending: true},
	})
	// Build the PaneInfo list the way handleListPanesReq does and assert the
	// deferred pane is Running=false, Pending=true. If handleListPanesReq is
	// not directly callable without a *ipc.Conn, extract its pure list-building
	// half into a helper (buildPaneInfos) in this task and test that helper.
	infos := d.buildPaneInfos()
	var found bool
	for _, pi := range infos {
		if pi.ID == "pane-0000000d" {
			found = true
			if pi.Running {
				t.Errorf("deferred pane should report Running=false")
			}
			if !pi.Pending {
				t.Errorf("deferred pane should report Pending=true")
			}
		}
	}
	if !found {
		t.Fatalf("deferred pane missing from list")
	}
}
```

If `handleListPanesReq` mixes IPC response with list building, extract the loop (lines 2382-2403) into `func (d *Daemon) buildPaneInfos() []ipc.PaneInfo` and have the handler call it + `respondTo`. The test calls `buildPaneInfos` directly.

- [ ] **Step 5: Run tests**

Run: `./scripts/dev.sh test`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/ipc/protocol.go internal/daemon/daemon.go internal/daemon/lazy_restore_test.go
git commit -m "feat(daemon): deferred-pane nil-PTY guards + MCP spawn-on-access"
```

## Task A4: Ghost-preservation regression test

**Files:**
- Test: `internal/daemon/lazy_restore_test.go`

This locks the §A.4 invariant: a snapshot must not wipe a deferred pane's on-disk ghost buffer. It passes by construction today (restore pre-fills `OutputBuf` at daemon.go:498-501; `SaveAllBuffers` only writes provided buffers; `CleanBuffers` keys on all snapshot pane IDs) — the test guards against a future regression.

- [ ] **Step 1: Write the test**

Append to `internal/daemon/lazy_restore_test.go`:

```go
import (
	"os"
	"path/filepath"
	// ...existing imports
)

func TestSnapshot_PreservesDeferredPaneGhost(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir()) // isolate from production ~/.quil
	d := newTestDaemon(t)

	// Seed an on-disk ghost buffer, then restore so the pane is Pending with
	// OutputBuf pre-filled from that ghost.
	bufDir := config.BufferDir()
	os.MkdirAll(bufDir, 0o700)
	ghost := []byte("important scrollback history\n")
	if err := persist.SaveBuffer(bufDir, "pane-0000000f", ghost); err != nil {
		t.Fatalf("seed ghost: %v", err)
	}
	pane := &Pane{
		ID: "pane-0000000f", TabID: "tab-0000000f", Type: "terminal", Pending: true,
		OutputBuf: ringbuf.NewRingBuffer(d.session.bufSize),
	}
	pane.OutputBuf.Write(ghost) // restore pre-fills OutputBuf — replicate that
	d.session.RestoreTab(&Tab{ID: "tab-0000000f", Name: "F"}, []*Pane{pane})

	d.snapshot()

	got, err := persist.LoadBuffer(bufDir, "pane-0000000f")
	if err != nil {
		t.Fatalf("load ghost after snapshot: %v", err)
	}
	if string(got) != string(ghost) {
		t.Errorf("deferred pane ghost clobbered by snapshot: got %q want %q", got, ghost)
	}
}
```

> Confirm the imports `config`, `persist`, `ringbuf` match the packages used elsewhere in the `daemon` package (they are imported in `daemon.go`). Adjust the `OutputBuf` field construction to match exactly how `restoreWorkspace` builds it (daemon.go:493).

- [ ] **Step 2: Run the test — it should PASS immediately (invariant holds)**

Run: `./scripts/dev.sh test`
Expected: PASS. If it FAILS, the deferral path emptied `OutputBuf` somewhere — fix by ensuring deferred panes keep their pre-filled `OutputBuf` (do not reset it on defer).

- [ ] **Step 3: Commit**

```bash
git add internal/daemon/lazy_restore_test.go
git commit -m "test(daemon): lock deferred-pane ghost preservation on snapshot"
```

## Task A5: TUI — Eager toggle, state plumbing, keybinding

**Files:**
- Modify: `internal/config/config.go:130` (struct), `:202` (default)
- Modify: `internal/tui/pane.go:29` (PaneModel)
- Modify: `internal/tui/model.go:56` (PaneInfo), `:2643` (parse), `:1904,:1930` (apply), `:1423` (key case), add `toggleActivePaneEager`
- Modify: `internal/tui/dialog.go:274` (shortcuts list)

- [ ] **Step 1: Add the keybinding field + default**

In `internal/config/config.go`, after the `Redraw string` field (line 130) in `KeybindingsConfig`:

```go
	// ToggleEager flips the active pane's eager-restore flag. Eager panes
	// respawn immediately on daemon restart (vs the default lazy deferral) and
	// show a ● marker on their tab.
	ToggleEager string `toml:"toggle_eager"`
```

In `Default()`, after `NotesToggle: "alt+e",` (line 202) add:

```go
				ToggleEager:        "alt+shift+e",
```

(also add `Redraw: "alt+shift+l",` if the default block doesn't already set it — check it's present; do not duplicate.)

- [ ] **Step 2: Add Eager/Pending to the TUI pane models**

In `internal/tui/pane.go`, after `Muted bool` (line 29):

```go
	Eager          bool   // eager-restore flag (daemon-authoritative; mirrored for tab marker)
```

In `internal/tui/model.go`, the `PaneInfo` struct (line 56) after `Muted bool` (line 62):

```go
	Eager   bool
	Pending bool
```

- [ ] **Step 3: Parse eager from the workspace-state map**

In `internal/tui/model.go`, beside the muted parse (lines 2643-2644):

```go
				if muted, ok := pm["muted"].(bool); ok {
					pi.Muted = muted
				}
```

add:

```go
				if eager, ok := pm["eager"].(bool); ok {
					pi.Eager = eager
				}
				if pending, ok := pm["pending"].(bool); ok {
					pi.Pending = pending
				}
```

> Note: `pending` is currently only emitted by the MCP `PaneInfo` path, not by `workspaceStateFromSnapshot`. The tab marker only needs `Eager`, so the `pending` parse is optional/forward-looking — include it only if `workspaceStateFromSnapshot` is also extended to emit `pending`. For this PR the marker depends solely on `eager`; you may omit the `pending` parse here.

- [ ] **Step 4: Apply eager in both apply branches**

In `internal/tui/model.go`, beside `leaf.Pane.Muted = info.Muted` (line 1904):

```go
						leaf.Pane.Eager = info.Eager
```

and beside `pane.Muted = info.Muted` (line 1930):

```go
				pane.Eager = info.Eager
```

- [ ] **Step 5: Add the key case + toggle command**

In `internal/tui/model.go`, beside the mute case (lines 1423-1424):

```go
		case kbMatches(key, kb.MutePane):
			return m, m.toggleActivePaneMute()
```

add:

```go
		case kbMatches(key, kb.ToggleEager):
			return m, m.toggleActivePaneEager()
```

Add `toggleActivePaneEager` next to `toggleActivePaneMute` (after line 3329-ish), mirroring it exactly:

```go
// toggleActivePaneEager flips the eager-restore flag on the focused pane and
// sends the daemon the authoritative update; the tab ● marker updates from the
// next workspace_state broadcast. No-op if no active pane.
func (m Model) toggleActivePaneEager() tea.Cmd {
	tab := m.activeTabModel()
	if tab == nil {
		return nil
	}
	pane := tab.ActivePaneModel()
	if pane == nil {
		return nil
	}
	next := !pane.Eager
	paneID := pane.ID
	return func() tea.Msg {
		msg, err := ipc.NewMessage(ipc.MsgUpdatePane, ipc.UpdatePanePayload{
			PaneID: paneID,
			Eager:  &next,
		})
		if err != nil {
			log.Printf("toggleActivePaneEager build msg: %v", err)
			return nil
		}
		if err := m.client.Send(msg); err != nil {
			log.Printf("toggleActivePaneEager send: %v", err)
		}
		return nil
	}
}
```

- [ ] **Step 6: Add to the F1 shortcuts dialog**

In `internal/tui/dialog.go`, beside the `{kbDisplay(kb.RenamePane), "Rename pane"},` entry (line 274) in the shortcuts list, add:

```go
		{kbDisplay(kb.ToggleEager), "Toggle eager restore (active pane)"},
```

- [ ] **Step 7: Verify the key isn't globally exempt-blocked in notes mode**

`alt+shift+e` is a new global shortcut. If `notesKeyExempt` (referenced in CLAUDE.md) enumerates allowed global shortcuts during notes mode, decide whether eager-toggle should work in notes mode. Recommendation: leave it OUT of `notesKeyExempt` (notes mode already auto-enters focus; toggling eager there is niche). No change needed unless you want it exempt.

- [ ] **Step 8: Build + run tests**

Run: `./scripts/dev.sh build && ./scripts/dev.sh test`
Expected: builds clean; suite green (existing keybinding tests in `notes_test.go` iterate `kb` fields — confirm none assert an exhaustive field count that the new field breaks; if one does, update its expected count).

- [ ] **Step 9: Commit**

```bash
git add internal/config/config.go internal/tui/pane.go internal/tui/model.go internal/tui/dialog.go
git commit -m "feat(tui): Alt+Shift+E toggles pane eager-restore flag"
```

## Task A6: TUI — eager marker on the tab label

**Files:**
- Modify: `internal/tui/model.go:2162` (tabLabel)
- Test: `internal/tui/model_test.go` (or the file holding tab-bar tests)

- [ ] **Step 1: Write the failing test**

Add to the TUI test file that constructs a `Model` with tabs (follow the existing tab-bar / `tabLabel` test setup; if none exists, place it in `internal/tui/model_test.go`):

```go
func TestTabLabel_ShowsEagerMarker(t *testing.T) {
	m := Model{activeTab: 0}
	// Build a tab whose layout tree has one eager pane.
	root := &LayoutNode{Pane: &PaneModel{ID: "pane-1", Eager: true}}
	m.tabs = []*TabModel{{ID: "tab-1", Name: "build", Root: root}}

	got := m.tabLabel(0)
	if !strings.Contains(got, "●") {
		t.Errorf("tabLabel for a tab with an eager pane should contain ●; got %q", got)
	}
}

func TestTabLabel_NoMarkerWithoutEagerPane(t *testing.T) {
	m := Model{activeTab: 0}
	root := &LayoutNode{Pane: &PaneModel{ID: "pane-1", Eager: false}}
	m.tabs = []*TabModel{{ID: "tab-1", Name: "build", Root: root}}

	if got := m.tabLabel(0); strings.Contains(got, "●") {
		t.Errorf("tabLabel without eager panes should not contain ●; got %q", got)
	}
}
```

> Confirm `TabModel` field names (`ID`, `Name`, `Root`) and `LayoutNode`/`PaneModel` literal construction against `internal/tui/layout.go` and the `TabModel` definition before finalizing — adjust the struct literals to match. `LayoutNode.Leaves()` (layout.go:44) returns `[]*PaneModel`.

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test`
Expected: FAIL — no `●` in the label.

- [ ] **Step 3: Add the marker to tabLabel**

In `internal/tui/model.go`, `tabLabel` (lines 2162-2171) currently:

```go
func (m Model) tabLabel(idx int) string {
	if m.renaming && idx == m.activeTab {
		return "* " + m.renameInput + "▎"
	}
	name := fmt.Sprintf("%d:%s", idx+1, m.tabs[idx].Name)
	if idx == m.activeTab {
		return "* " + name
	}
	return name
}
```

Replace with:

```go
// eagerTabMarker is a single-width BMP glyph (deliberately not an emoji — wide
// glyphs drift conhost columns; see pane_widechar_test.go). Shown on any tab
// containing at least one eager-restore pane.
const eagerTabMarker = "●"

func (m Model) tabHasEagerPane(idx int) bool {
	if m.tabs[idx].Root == nil {
		return false
	}
	for _, p := range m.tabs[idx].Root.Leaves() {
		if p != nil && p.Eager {
			return true
		}
	}
	return false
}

func (m Model) tabLabel(idx int) string {
	if m.renaming && idx == m.activeTab {
		return "* " + m.renameInput + "▎"
	}
	name := fmt.Sprintf("%d:%s", idx+1, m.tabs[idx].Name)
	if m.tabHasEagerPane(idx) {
		name = eagerTabMarker + name
	}
	if idx == m.activeTab {
		return "* " + name
	}
	return name
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `./scripts/dev.sh test`
Expected: PASS. Existing tab-bar hit-test tests (`hitTestTab`/`tabLabel` width alignment, per CLAUDE.md) still pass because `tabLabel` is the single shared width source — confirm no width assertion hardcodes a label that now gains a marker.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/model.go internal/tui/model_test.go
git commit -m "feat(tui): mark tabs with an eager pane using a ● glyph"
```

## Task A7: Documentation

**Files:**
- Modify: `docs/keybindings.md`, `docs/configuration.md`, `docs/features.md`

- [ ] **Step 1: Document the keybinding and behavior**

- `docs/keybindings.md`: add a row for `toggle_eager` / `Alt+Shift+E` — "Toggle eager restore on the active pane (eager panes respawn immediately on restart; others load lazily on tab open). Marked with ● on the tab."
- `docs/configuration.md`: add `toggle_eager = "alt+shift+e"` to the keybindings example block (beside `mute_pane`), and document the lazy-restore behavior + that `max_size_mb`/`max_files` now drive log rotation (defaults 5 / 10).
- `docs/features.md`: add a short "Lazy restore" entry under the persistence/restore area describing deferred spawn + the eager opt-in + tab marker.

- [ ] **Step 2: Commit**

```bash
git add docs/keybindings.md docs/configuration.md docs/features.md
git commit -m "docs: document lazy restore, eager flag, and log rotation"
```

---

# Workstream C — IPC Backpressure Hardening

## Task C1: Dual-queue Conn (critical + droppable output)

**Files:**
- Modify: `internal/ipc/server.go`
- Test: `internal/ipc/lossy_test.go` (new), existing tests must stay green

- [ ] **Step 1: Write the failing tests**

Create `internal/ipc/lossy_test.go`:

```go
package ipc_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

// A flood of live-output broadcasts to a client that never reads must NOT close
// that client (output is lossy — frames are dropped, the connection survives).
// This is the regression test for the production crash: the busy TUI was being
// force-disconnected during a restore output storm.
func TestBroadcast_OutputFloodDoesNotCloseSlowConn(t *testing.T) {
	t.Parallel()
	sockPath := filepath.Join(t.TempDir(), "output-flood.sock")

	srv := ipc.NewServer(sockPath, func(*ipc.Conn, *ipc.Message) {}, nil)
	if err := srv.Start(); err != nil {
		t.Fatalf("server start: %v", err)
	}
	defer srv.Stop()

	slow, err := ipc.NewClient(sockPath)
	if err != nil {
		t.Fatalf("slow client: %v", err)
	}
	defer slow.Close()

	waitForConnCount(t, srv, 1, 2*time.Second)

	// Flood with live PaneOutput (droppable) and never read on the client.
	payload := ipc.PaneOutputPayload{PaneID: "pane-1", Data: make([]byte, 4000)}
	for i := 0; i < 500; i++ {
		msg, _ := ipc.NewMessage(ipc.MsgPaneOutput, payload)
		srv.Broadcast(msg)
		time.Sleep(time.Millisecond)
	}

	// The slow client must still be connected — output frames were dropped, the
	// connection was NOT torn down.
	if got := srv.ConnCount(); got != 1 {
		t.Errorf("slow client closed by output flood: ConnCount=%d, want 1", got)
	}
}
```

> `waitForConnCount` already exists in `broadcast_resilience_test.go` (same `package ipc_test`), so it's reusable here. Confirm `PaneOutputPayload` field names (`PaneID`, `Data`) from `protocol.go` — they match `daemon.go:766`.

Also add the internal-package drop test to `internal/ipc/conn_internal_test.go`:

```go
// TestEnqueue_DropsOutputFrameWhenFull verifies a full output queue drops the
// frame (and does NOT trip overflow/close), while the connection stays usable.
func TestEnqueue_DropsOutputFrameWhenFull(t *testing.T) {
	t.Parallel()
	local, remote := net.Pipe()
	defer remote.Close()
	c := newConn(local)
	defer c.Close()

	// Remote never reads → sendLoop blocks on its first write → outCh fills.
	// Push more droppable frames than the queue can hold.
	for i := 0; i < sendBufSize*3; i++ {
		_ = c.enqueue([]byte{0, 0, 0, 1, byte('x')}, true)
	}
	if c.overflow.Load() {
		t.Errorf("droppable flood must not set overflow")
	}
	if c.closed.Load() {
		t.Errorf("droppable flood must not close the conn")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `./scripts/dev.sh test`
Expected: FAIL — `enqueue` undefined; `MsgPaneOutput` flood currently overflows+closes so `ConnCount` would drop to 0 (or compile error first).

- [ ] **Step 3: Rewrite the Conn send path as a dual queue**

In `internal/ipc/server.go`, replace the `Conn` struct (lines 51-58), `newConn` (60-68), `sendFrame` (102-122), and `sendLoop` (124-141) with:

```go
type Conn struct {
	raw       net.Conn
	critCh    chan []byte // must-deliver: state, responses, ghost replay, lifecycle
	outCh     chan []byte // droppable: live PaneOutput broadcast frames
	done      chan struct{}
	closeOnce sync.Once
	closed    atomic.Bool
	overflow  atomic.Bool
	dropped   atomic.Uint64
}

func newConn(raw net.Conn) *Conn {
	c := &Conn{
		raw:    raw,
		critCh: make(chan []byte, sendBufSize),
		outCh:  make(chan []byte, sendBufSize),
		done:   make(chan struct{}),
	}
	go c.sendLoop()
	return c
}

// sendFrame queues a must-deliver frame. Retained for callers (Send) and the
// existing tests that exercise the critical-overflow → close path.
func (c *Conn) sendFrame(frame []byte) error {
	return c.enqueue(frame, false)
}

// enqueue queues a pre-encoded frame. Droppable frames (live PTY output) are
// dropped silently when the output queue is full — a busy client sheds cosmetic
// output (the next frame supersedes it) instead of being disconnected. Critical
// frames use the bounded critical queue; if THAT overflows the peer cannot
// drain 64 low-volume frames and is genuinely wedged, so it is closed (the
// original slow-client defense, now scoped to critical traffic only).
func (c *Conn) enqueue(frame []byte, droppable bool) error {
	if c.closed.Load() || c.overflow.Load() {
		return ErrSendOverflow
	}
	if droppable {
		select {
		case c.outCh <- frame:
		default:
			n := c.dropped.Add(1)
			logger.Debug("ipc: dropped output frame (slow client, total=%d)", n)
		}
		return nil
	}
	select {
	case c.critCh <- frame:
		return nil
	default:
		if c.overflow.CompareAndSwap(false, true) {
			logger.Warn("ipc: dropping slow client (critical send buffer overflow)")
			go c.Close()
		}
		return ErrSendOverflow
	}
}

// sendLoop drains the two queues, draining critical first so an output flood
// can never starve state/responses.
func (c *Conn) sendLoop() {
	for {
		// Priority: take any pending critical frame before considering output.
		select {
		case <-c.done:
			return
		case frame := <-c.critCh:
			if !c.write(frame) {
				return
			}
			continue
		default:
		}
		select {
		case <-c.done:
			return
		case frame := <-c.critCh:
			if !c.write(frame) {
				return
			}
		case frame := <-c.outCh:
			if !c.write(frame) {
				return
			}
		}
	}
}

// write applies the per-frame write deadline and writes. Returns false on any
// error so sendLoop exits (read side detects the matching error + cleans up).
func (c *Conn) write(frame []byte) bool {
	_ = c.raw.SetWriteDeadline(time.Now().Add(writeDeadline))
	_, err := c.raw.Write(frame)
	return err == nil
}
```

Update the `sendBufSize` doc comment (lines 16-24) to note it is now the depth of *each* of the two queues. The `Close` method (152-160) is unchanged — it closes `done` and the raw conn; queued frames in both channels are discarded (the existing contract).

- [ ] **Step 4: Classify frames in Broadcast**

In `internal/ipc/server.go`, `Broadcast` (lines 224-259), the fan-out loop (251-258) currently calls `c.sendFrame(frame)`. Replace with droppable classification:

```go
	droppable := msg.Type == MsgPaneOutput
	for _, c := range conns {
		if err := c.enqueue(frame, droppable); err != nil && !errors.Is(err, ErrSendOverflow) {
			logger.Error("ipc: broadcast send: %v", err)
		}
	}
```

`Send` (line 90, `return c.sendFrame(buf.Bytes())`) stays as-is — `sendFrame` routes to the critical queue, so unicast paths (MCP responses, `sendGhostChunked`'s ghost replay) are never dropped.

- [ ] **Step 5: Run the full suite**

Run: `./scripts/dev.sh test`
Expected: PASS — new lossy tests green; the three existing IPC tests still green (they broadcast `MsgStateUpdate`, which is critical → unchanged close-on-overflow behavior; and call `sendFrame`, which still routes to `critCh`).

- [ ] **Step 6: Run the race detector on the IPC package**

Run: `./scripts/dev.sh test-race`
Expected: PASS, no data races (the dual-queue adds `dropped atomic.Uint64`; channels and atomics are the only shared state).

- [ ] **Step 7: Commit**

```bash
git add internal/ipc/server.go internal/ipc/lossy_test.go internal/ipc/conn_internal_test.go
git commit -m "fix(ipc): dual-queue send — live output is lossy, never closes busy peer"
```

---

# Final: Integration Verification

## Task F1: Build, full test, manual dev-mode smoke

**Files:** none (verification only)

- [ ] **Step 1: Full build + suite + vet + race**

```bash
./scripts/dev.sh build
./scripts/dev.sh vet
./scripts/dev.sh test
./scripts/dev.sh test-race
```
Expected: all green.

- [ ] **Step 2: Manual dev-mode smoke (per `.claude/rules/dev-environment.md` — NEVER touch production `~/.quil/`)**

Run the dev build (`./quil-dev.exe`, stores state in `./.quil/`). Confirm `[dev]` in the status bar. Create several tabs/panes (a mix of terminal + claude-code if available), then close and relaunch the dev TUI so the dev daemon restarts:
- Only the active tab's pane(s) spawn on restart (check `./.quil/quild.log` for `respawn: deferring pane ...` lines for the other tabs).
- Switching to a deferred tab boots its pane on demand and replays its ghost history.
- **Zero** `dropping slow client` lines in `./.quil/quild.log` across the restart.
- `Alt+Shift+E` on a pane adds the `●` marker to its tab; after restart that pane spawns eagerly.
- Unopened tabs retain their scrollback across the restart.
- Write enough log volume (or set `max_size_mb` low in `./.quil/config.toml`) to confirm `quild-*.log` / `quil-*.log` archives appear and cap at `max_files`.

- [ ] **Step 3: Final commit (if any doc/cleanup tweaks emerged)**

```bash
git add -A
git commit -m "chore: robust-restart final cleanup"
```

---

## Self-Review (completed during authoring)

**Spec coverage:** A (§A.1–A.7) → Tasks A1–A7; B → Tasks B1–B2 (+docs); C → Task C1. §A.4 ghost preservation → Task A4. nil-PTY audit (§A.3) → Task A3. All spec sections map to a task.

**Placeholder scan:** No "TBD/TODO". The few `> Note:` callouts point at exact files/lines to confirm a signature before writing (e.g. the `newTestDaemon` helper, `spawnPane`'s locking) — these are verification instructions with the fallback spelled out, not deferred work.

**Type consistency:** `Pane.Eager`/`Pane.Pending` (daemon) ↔ `UpdatePanePayload.Eager *bool` ↔ `PaneInfo.Eager/Pending` ↔ TUI `PaneInfo.Eager/Pending` + `PaneModel.Eager`. `enqueue(frame, droppable)` used consistently in C; `sendFrame` retained as the `enqueue(_, false)` alias for existing tests. `ensurePaneSpawned`/`ensureTabSpawned`/`spawnRestoredPane` names consistent across A2/A3.

**Known confirm-before-write points (read the cited lines first):** the daemon test constructor (`event_mute_test.go`), whether `spawnPane` self-locks `PluginMu` (A2 Step 3), and the `TabModel` literal shape (A6 Step 1).
