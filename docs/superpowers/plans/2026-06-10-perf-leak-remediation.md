# Performance & Leak Remediation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix every confirmed finding from the 2026-06-10 audit (techdebt/ register): 2 critical daemon-safety bugs, 4 resource leaks, and the highest-value daemon + TUI performance items.

**Architecture:** Four independently shippable phases, one PR each: (0) daemon instance safety, (1) resource-leak fixes, (2) daemon hot-path performance, (3) TUI render performance. Every task is TDD: failing test → minimal fix → green → commit. No phase depends on a later phase; Phase 2 Task 8 (ring buffer) is a prerequisite for Tasks 9–10 within its phase.

**Tech Stack:** Go 1.25 (Docker builds via `scripts/dev.sh` — no local Go), Bubble Tea v2 TUI, stdlib testing only.

---

## Conventions used by every task

**Run targeted tests** (from repo root, Bash):

```bash
PROJECT_DIR="$(pwd -W 2>/dev/null || pwd)"
DRUN="docker run --rm -v ${PROJECT_DIR}:/src -v quil-gomod:/go/pkg/mod -w //src golang:1.25-alpine"
$DRUN go test -run <TestName> -v ./internal/<pkg>/
```

**Per-phase gate** (before the phase's final commit):

```bash
./scripts/dev.sh test && ./scripts/dev.sh vet
$DRUN sh -c "apk add --no-cache gcc musl-dev >/dev/null && CGO_ENABLED=1 go test -race -tags=integration ./..."
```

**Branching:** phases land **in order** — branch each phase off `origin/master` only after the previous phase's PR has merged (`git fetch origin && git switch -c <branch> origin/master --no-track`). Later phases use earlier symbols (Phase 2/3 tests call `Dispose()` from Phase 1; Phase 2 Task 10 uses Task 8's `Gen()`). Squash-merge PRs (CONTRIBUTING.md). Every commit message ends with:

```
Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

**Techdebt register:** the task that fixes a finding deletes its `techdebt/**.md` file in the same commit. Update `CHANGELOG.md` `[Unreleased]` once per phase (final task of the phase).

**CLAUDE.md:** Phase 0 Task 2 and Phase 2 Task 8 change documented behavior — update `.claude/CLAUDE.md` in those commits (documentation-maintenance rule).

---

# Phase 0 — Critical daemon safety (branch `fix/daemon-instance-safety`)

Closes `techdebt/daemon/1-2-quild-no-single-instance-guard.md` and `techdebt/daemon/1-3-pane-env-quil-home-retargets-dev-builds-at-production.md`.

### Task 1: Single-instance guard in `Daemon.Start`

A second `quild` against the same `QUIL_HOME` currently unlinks the live daemon's socket (`Server.Start` does `os.Remove(s.path)` unconditionally) and overwrites `quild.pid`. Guard: probe the socket before doing anything; a live listener means a daemon is serving — refuse to start.

**Files:**
- Create: `internal/daemon/singleinstance.go`
- Modify: `internal/daemon/daemon.go` (top of `func (d *Daemon) Start()`)
- Test: `internal/daemon/singleinstance_integration_test.go`

- [ ] **Step 1: Write the failing tests**

```go
//go:build integration

package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/artyomsv/quil/internal/config"
)

// TestStart_SecondDaemonRefused: a second daemon against the same QUIL_HOME
// must refuse to start while the first is serving, instead of unlinking the
// live socket and clobbering the PID file (the production-bricking incident
// of 2026-06-10).
func TestStart_SecondDaemonRefused(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())

	d1 := New(config.Default())
	if err := d1.Start(); err != nil {
		t.Fatalf("first daemon Start: %v", err)
	}
	defer d1.Stop()

	d2 := New(config.Default())
	if err := d2.Start(); err == nil {
		d2.Stop()
		t.Fatal("second daemon started against a live socket — expected refusal")
	}
}

// TestStart_StaleSocketStillCleaned: a leftover socket file with no listener
// behind it must NOT block startup — the existing stale-cleanup behavior is
// preserved.
func TestStart_StaleSocketStillCleaned(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("QUIL_HOME", tmp)
	if err := os.MkdirAll(tmp, 0700); err != nil {
		t.Fatal(err)
	}
	// Plain file at the socket path = stale socket (nothing accepts on it).
	if err := os.WriteFile(filepath.Join(tmp, "quild.sock"), nil, 0600); err != nil {
		t.Fatal(err)
	}

	d := New(config.Default())
	if err := d.Start(); err != nil {
		t.Fatalf("Start with stale socket file: %v", err)
	}
	d.Stop()
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
$DRUN go test -tags=integration -run "TestStart_SecondDaemonRefused|TestStart_StaleSocketStillCleaned" -v ./internal/daemon/
```

Expected: `TestStart_SecondDaemonRefused` FAILS ("second daemon started…"); the stale-socket test passes (it pins current behavior).

- [ ] **Step 3: Implement the guard**

`internal/daemon/singleinstance.go`:

```go
package daemon

import (
	"fmt"
	"net"
	"time"
)

// probeExistingDaemon dials the daemon socket. A successful connect means a
// live daemon is serving this QUIL_HOME — starting a second one would unlink
// its socket (Server.Start removes the path unconditionally) and overwrite
// the PID file, bricking the original for new clients. A failed connect
// means the socket is stale or absent; the normal stale-cleanup proceeds.
//
// Socket reachability is the invariant that matters, and it is portable —
// a PID-file liveness check would need per-OS process probing and can lie
// after PID reuse.
func probeExistingDaemon(socketPath string) error {
	conn, err := net.DialTimeout("unix", socketPath, 500*time.Millisecond)
	if err != nil {
		return nil
	}
	_ = conn.Close()
	return fmt.Errorf("daemon already running and serving %s — refusing to start (use that instance, or remove the socket if you are certain it is dead)", socketPath)
}
```

`internal/daemon/daemon.go` — at the very top of `Start()`, before `os.MkdirAll(quilDir, 0700)` succeeds is fine, but the probe needs no dir, so insert as the first statement after `quilDir := config.QuilDir()`:

```go
	if err := probeExistingDaemon(config.SocketPath()); err != nil {
		return err
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Same command as Step 2. Expected: both PASS. (`cmd/quild/main.go` already exits 1 with the error printed when `Start` fails — no change needed there.)

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/singleinstance.go internal/daemon/singleinstance_integration_test.go internal/daemon/daemon.go
git rm techdebt/daemon/1-2-quild-no-single-instance-guard.md
git commit -m "fix(daemon): refuse to start when another daemon serves the socket"
```

### Task 2: Pane env `QUIL_HOOK_HOME` — stop retargeting dev builds at production

The daemon exports `QUIL_HOME=<production home>` into every claude-code/opencode pane env; every child process inherits it, so `quil-dev.exe` launched inside a pane silently targets production `~/.quil/`. Fix: rename the pane-env variable to `QUIL_HOOK_HOME` (hook consumers read it, with `QUIL_HOME` fallback for one release), and add a dev-build belt that ignores an inherited `QUIL_HOME` equal to the production default.

**Files:**
- Modify: `internal/daemon/daemon.go:1605` (opencode env), `:1647` (claude env)
- Modify: `cmd/quild/hook.go` (hook-side resolution)
- Modify: `internal/opencodehook/scripts/quil-session-tracker.js:47`
- Modify: `internal/config/config.go` (add `DefaultQuilDir`, `IsDefaultQuilDir`)
- Modify: `cmd/quil/main.go:45`, `cmd/quild/main.go:28` (dev gate belt)
- Modify: `.claude/CLAUDE.md` (env var references)
- Test: `internal/config/config_test.go` (append), `internal/daemon/spawn_env_test.go` (create)

- [ ] **Step 1: Write the failing tests**

Append to `internal/config/config_test.go`:

```go
func TestIsDefaultQuilDir(t *testing.T) {
	def := DefaultQuilDir()
	if def == "" {
		t.Skip("no home dir on this runner")
	}
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"exact default", def, true},
		{"trailing separator", def + string(filepath.Separator), true},
		{"different dir", filepath.Join(def, "sub"), false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsDefaultQuilDir(tt.in); got != tt.want {
				t.Errorf("IsDefaultQuilDir(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
```

Create `internal/daemon/spawn_env_test.go` (white-box, package `daemon`):

```go
package daemon

import (
	"strings"
	"testing"
)

// TestClaudeHookSpawnPrep_PaneEnvUsesHookHome: the pane env must carry
// QUIL_HOOK_HOME, NOT QUIL_HOME — children inherit the pane env, and an
// inherited QUIL_HOME silently retargets quil dev builds at production
// (techdebt/daemon/1-3). The hook subcommand reads QUIL_HOOK_HOME.
func TestClaudeHookSpawnPrep_PaneEnvUsesHookHome(t *testing.T) {
	orig := claudeHookExeFn
	claudeHookExeFn = func() (string, error) { return "/fake/quild", nil }
	defer func() { claudeHookExeFn = orig }()

	_, env := claudeHookSpawnPrep("/data/quil", "pane-abc123", "default", nil)
	assertHookHomeOnly(t, env, "/data/quil")
}

func assertHookHomeOnly(t *testing.T, env []string, dir string) {
	t.Helper()
	var hookHome bool
	for _, kv := range env {
		if strings.HasPrefix(kv, "QUIL_HOME=") {
			t.Errorf("pane env still carries %s — retargets dev builds at production", kv)
		}
		if kv == "QUIL_HOOK_HOME="+dir {
			hookHome = true
		}
	}
	if !hookHome {
		t.Errorf("pane env missing QUIL_HOOK_HOME=%s; env = %v", dir, env)
	}
}
```

Note: if `claudeHookExeFn` is named differently, find it with `grep -n "claudeHookExeFn" internal/daemon/daemon.go` — CLAUDE.md documents it as the injectable `os.Executable`. Add an equivalent test for the opencode prep function (locate with `grep -n '"QUIL_HOME=" + absQuilDir' internal/daemon/daemon.go` and test its enclosing function the same way, reusing `assertHookHomeOnly`).

- [ ] **Step 2: Run tests to verify they fail**

```bash
$DRUN go test -run "TestIsDefaultQuilDir|TestClaudeHookSpawnPrep_PaneEnvUsesHookHome" -v ./internal/config/ ./internal/daemon/
```

Expected: config test FAILS to compile (`IsDefaultQuilDir` undefined); daemon test FAILS ("pane env still carries QUIL_HOME=…").

- [ ] **Step 3: Implement**

`internal/config/config.go` — below `QuilDir()`:

```go
// DefaultQuilDir returns the production default data dir (~/.quil),
// ignoring QUIL_HOME. Used by dev builds to detect an inherited
// production-pointing QUIL_HOME.
func DefaultQuilDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".quil")
}

// IsDefaultQuilDir reports whether dir resolves to the production default
// data dir. Case-insensitive on Windows.
func IsDefaultQuilDir(dir string) bool {
	def := DefaultQuilDir()
	if def == "" || dir == "" {
		return false
	}
	a, b := filepath.Clean(dir), filepath.Clean(def)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
```

(Add `runtime` and `strings` to the imports.)

`internal/daemon/daemon.go` — two one-line changes:

- line 1605: `"QUIL_HOME=" + absQuilDir,` → `"QUIL_HOOK_HOME=" + absQuilDir,`
- line 1647: `"QUIL_HOME=" + quilDir,` → `"QUIL_HOOK_HOME=" + quilDir,` and update the comment above it (`// QUIL_HOOK_HOME is passed explicitly … the native subcommand resolves it via the QUIL_HOOK_HOME env (QUIL_HOME fallback for panes spawned by a pre-rename daemon).`)

`cmd/quild/hook.go` — replace `QuilDir: config.QuilDir(),` with:

```go
		QuilDir: hookHomeDir(),
```

and add:

```go
// hookHomeDir resolves the data dir for hook writes. The daemon sets
// QUIL_HOOK_HOME in pane envs (renamed from QUIL_HOME, which children
// inherited and which retargeted quil dev builds at production —
// techdebt/daemon/1-3). QUIL_HOME remains as a fallback for panes spawned
// by a pre-rename daemon that survives the upgrade; remove the fallback
// after one release.
func hookHomeDir() string {
	if dir := os.Getenv("QUIL_HOOK_HOME"); dir != "" {
		return dir
	}
	return config.QuilDir()
}
```

`internal/opencodehook/scripts/quil-session-tracker.js` line 47:

```js
  const quilHome = process.env.QUIL_HOOK_HOME || process.env.QUIL_HOME || "";
```

(`EnsureScripts` rewrites the plugin file at daemon startup, so the change deploys with the daemon.)

`cmd/quil/main.go` and `cmd/quild/main.go` — in both dev gates, before the existing `os.Getenv("QUIL_HOME") == ""` check, insert:

```go
	if buildDevMode == "true" && config.IsDefaultQuilDir(os.Getenv("QUIL_HOME")) {
		// Inherited from a production pane env (pre-rename daemon) or a
		// stray export. A dev build pointed at production ~/.quil violates
		// the isolation rule — ignore it and fall through to the
		// project-local default.
		fmt.Fprintln(os.Stderr, "dev build: ignoring inherited QUIL_HOME pointing at production ~/.quil")
		os.Unsetenv("QUIL_HOME")
	}
```

`.claude/CLAUDE.md`: update the three references that say pane PTY env carries `QUIL_HOME` (claudehook section, opencodehook section, session-id rotation bullet) to say `QUIL_HOOK_HOME` (with one-release `QUIL_HOME` fallback in consumers).

- [ ] **Step 4: Run tests to verify they pass; check for stale assertions**

```bash
grep -rn "QUIL_HOME=" --include="*_test.go" internal/ cmd/
$DRUN go test ./internal/config/ ./internal/daemon/ ./internal/claudehook/ ./internal/opencodehook/
```

Expected: PASS. If the grep shows existing tests asserting `QUIL_HOME=` in spawn envs, update them to `QUIL_HOOK_HOME=`.

- [ ] **Step 5: Commit, gate, PR**

```bash
git add -A
git rm techdebt/daemon/1-3-pane-env-quil-home-retargets-dev-builds-at-production.md
git commit -m "fix(daemon): pane env QUIL_HOOK_HOME stops retargeting dev builds

Children of claude/opencode panes inherited QUIL_HOME=<production>,
so quil dev builds launched inside a pane silently operated on
production state (clobbered PID file + socket in the 2026-06-10
incident). Hook consumers now read QUIL_HOOK_HOME (QUIL_HOME fallback
for one release); dev builds additionally ignore an inherited
QUIL_HOME that points at the production default."
```

Add a `### Fixed` entry to CHANGELOG `[Unreleased]` for both Phase 0 fixes, commit `docs(changelog): …`, run the phase gate, push, open PR `fix(daemon): single-instance guard + dev-build isolation`.

---

# Phase 1 — Resource leaks (branch `fix/resource-leaks`)

Closes `techdebt/tui/2-2-vt-emulator-leak-on-pane-prune.md`, `techdebt/tui/2-1-sidebar-tick-chains-stack.md`, `techdebt/daemon/2-1-coalescer-unbounded-accumulator.md`, `techdebt/daemon/3-1-hook-cleanup-missing-tab-destroy-replace.md`, `techdebt/pty/4-1-windows-process-handle-leak.md`.

### Task 3: Dispose VT emulators of pruned panes

**Files:**
- Modify: `internal/tui/pane.go` (add `Dispose`)
- Modify: `internal/tui/model.go` (`applyWorkspaceState`, after the tab-rebuild loop)
- Test: `internal/tui/pane_dispose_test.go`

- [ ] **Step 1: Write the failing test**

```go
package tui

import (
	"runtime"
	"testing"
	"time"
)

// TestPaneModel_Dispose_StopsDrainGoroutine: every PaneModel starts a
// drainVTResponses goroutine parked on the emulator's response pipe; only
// emulator Close unblocks it. Dispose must close the emulator so pruned
// panes don't leak one goroutine + a 10k-line scrollback each.
func TestPaneModel_Dispose_StopsDrainGoroutine(t *testing.T) {
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	panes := make([]*PaneModel, 8)
	for i := range panes {
		panes[i] = NewPaneModel("pane-dispose-test", 1024)
	}
	time.Sleep(50 * time.Millisecond)
	if got := runtime.NumGoroutine(); got < baseline+8 {
		t.Fatalf("expected 8 drain goroutines to start, baseline=%d now=%d", baseline, got)
	}

	for _, p := range panes {
		p.Dispose()
	}
	runtime.GC()
	time.Sleep(100 * time.Millisecond)

	if got := runtime.NumGoroutine(); got > baseline+1 {
		t.Errorf("drain goroutines leaked after Dispose: baseline=%d, after=%d", baseline, got)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
$DRUN go test -run TestPaneModel_Dispose_StopsDrainGoroutine -v ./internal/tui/
```

Expected: compile FAIL — `p.Dispose undefined`.

- [ ] **Step 3: Implement `Dispose` + wire into prune**

`internal/tui/pane.go`, below `replaceVT`:

```go
// Dispose closes the VT emulator, stopping its drainVTResponses goroutine
// and releasing the scrollback grid. Must be called for every PaneModel
// removed from the layout tree — without it each closed pane leaks a parked
// goroutine plus up to a 10,000-line scrollback. The PaneModel must not be
// rendered or written to afterwards.
func (p *PaneModel) Dispose() {
	if p.vt != nil {
		_ = p.vt.Close()
	}
}
```

`internal/tui/model.go`, in `applyWorkspaceState`: the function already builds `existingPanes` (map of every pre-reconciliation PaneModel). After the `for _, tabInfo := range state.Tabs { … }` loop completes (all surviving tabs/panes are in `m.tabs`), insert:

```go
	// Dispose panes that did not survive reconciliation — both panes pruned
	// from surviving tabs and every pane of tabs the daemon dropped. Without
	// this, each removed pane leaks its VT emulator (drain goroutine +
	// scrollback grid) for the TUI session's lifetime.
	surviving := make(map[string]bool)
	for _, tab := range m.tabs {
		if tab.Root != nil {
			for id := range tab.Root.PaneIDs() {
				surviving[id] = true
			}
		}
	}
	for id, pane := range existingPanes {
		if !surviving[id] {
			pane.Dispose()
		}
	}
```

- [ ] **Step 4: Verify green + no regressions in reconciliation tests**

```bash
$DRUN go test ./internal/tui/
```

Expected: PASS (including existing `applyWorkspaceState`/notes-reconciliation tests — reused panes are in `surviving`, so they are never disposed).

- [ ] **Step 5: Commit**

```bash
git add internal/tui/pane.go internal/tui/model.go internal/tui/pane_dispose_test.go
git rm techdebt/tui/2-2-vt-emulator-leak-on-pane-prune.md
git commit -m "fix(tui): dispose VT emulators of pruned panes"
```

### Task 4: Running-guards for sidebar and notes tick chains

**Files:**
- Modify: `internal/tui/model.go` (fields near `workTickRunning` ~line 308; handlers at ~810, ~814-818, ~821-827, ~1460, ~1469; `toggleNotesMode` ~1074)
- Test: `internal/tui/tick_guard_test.go`

- [ ] **Step 1: Write the failing test**

```go
package tui

import "testing"

// TestStartSidebarTick_SingleChain: scheduling must be idempotent while a
// chain is in flight — without the guard every paneEventMsg with the sidebar
// visible stacked a new immortal 10 s chain (techdebt/tui/2-1).
func TestStartSidebarTick_SingleChain(t *testing.T) {
	m := Model{}
	if cmd := m.startSidebarTick(); cmd == nil {
		t.Fatal("first startSidebarTick returned nil — chain never starts")
	}
	if cmd := m.startSidebarTick(); cmd != nil {
		t.Error("second startSidebarTick started a duplicate chain")
	}
	// Chain decided not to reschedule (sidebar hidden) → flag clears →
	// a new chain may start.
	m.sidebarTickRunning = false
	if cmd := m.startSidebarTick(); cmd == nil {
		t.Error("startSidebarTick after chain end returned nil — sidebar refresh dead")
	}
}

func TestStartNotesTick_SingleChain(t *testing.T) {
	m := Model{}
	if cmd := m.startNotesTick(); cmd == nil {
		t.Fatal("first startNotesTick returned nil")
	}
	if cmd := m.startNotesTick(); cmd != nil {
		t.Error("second startNotesTick started a duplicate chain")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
$DRUN go test -run "TestStartSidebarTick_SingleChain|TestStartNotesTick_SingleChain" -v ./internal/tui/
```

Expected: compile FAIL — `startSidebarTick`/`startNotesTick`/`sidebarTickRunning` undefined.

- [ ] **Step 3: Implement**

Fields (next to `workTickRunning bool`, ~`model.go:308`):

```go
	sidebarTickRunning bool
	notesTickRunning   bool
```

Helpers (next to `sidebarTick()` ~line 1022; pointer receivers, matching `exitNotesModeInPlace` precedent):

```go
// startSidebarTick schedules the sidebar refresh chain unless one is already
// in flight. Mirrors workTickRunning: the chain self-perpetuates inside the
// sidebarTickMsg handler, so unguarded scheduling stacks immortal chains.
func (m *Model) startSidebarTick() tea.Cmd {
	if m.sidebarTickRunning {
		return nil
	}
	m.sidebarTickRunning = true
	return m.sidebarTick()
}

// startNotesTick — same guard for the notes auto-save debounce chain.
func (m *Model) startNotesTick() tea.Cmd {
	if m.notesTickRunning {
		return nil
	}
	m.notesTickRunning = true
	return m.notesTick()
}
```

Handler changes:

```go
	case sidebarTickMsg:
		if m.notifications.visible && m.notifications.Count() > 0 {
			return m, m.sidebarTick() // chain continues; running flag stays set
		}
		m.sidebarTickRunning = false
		return m, nil

	case notesTickMsg:
		if m.notesMode && m.notesEditor != nil {
			m.notesEditor.MaybeAutoSave()
			return m, m.notesTick()
		}
		m.notesTickRunning = false
		return m, nil
```

Call sites — replace every direct scheduling of `m.sidebarTick()` outside the `sidebarTickMsg` handler with `m.startSidebarTick()` (three sites: paneEventMsg ~line 810, Alt+N ~1460, Ctrl+Alt+N ~1469; `tea.Batch` ignores nil cmds, so the call slots in unchanged), and the `m.notesTick()` call in `toggleNotesMode` (~1074) with `m.startNotesTick()`. Verify no site is missed:

```bash
grep -n "m\.sidebarTick()\|m\.notesTick()" internal/tui/model.go
```

Expected after the change: only the two handler-internal reschedules and the two helper bodies.

- [ ] **Step 4: Verify green**

```bash
$DRUN go test ./internal/tui/
```

- [ ] **Step 5: Commit**

```bash
git add internal/tui/model.go internal/tui/tick_guard_test.go
git rm techdebt/tui/2-1-sidebar-tick-chains-stack.md
git commit -m "fix(tui): guard sidebar/notes tick chains against stacking"
```

### Task 5: Cap the PTY output coalescer accumulator

**Files:**
- Modify: `internal/daemon/daemon.go:1246-1320` (`streamPTYOutput` — extract `runCoalescer`)
- Test: `internal/daemon/coalescer_test.go`

- [ ] **Step 1: Write the failing test**

```go
package daemon

import (
	"bytes"
	"testing"
)

// TestRunCoalescer_CapsBatchSize: the coalescer is a debounce — without a
// size cap, chunks arriving faster than the 2 ms timer grow the accumulator
// without bound (techdebt/daemon/2-1). Every flushed batch must respect
// coalesceMaxBytes and no byte may be lost or reordered.
func TestRunCoalescer_CapsBatchSize(t *testing.T) {
	const chunks, chunkLen = 200, 1024
	dataCh := make(chan []byte, chunks)
	for i := 0; i < chunks; i++ {
		dataCh <- bytes.Repeat([]byte{byte('a' + i%26)}, chunkLen)
	}
	close(dataCh)

	var got []byte
	var batches int
	runCoalescer(dataCh, func() {}, func(b []byte) {
		if len(b) > coalesceMaxBytes {
			t.Errorf("batch %d bytes exceeds cap %d", len(b), coalesceMaxBytes)
		}
		got = append(got, b...)
		batches++
	})

	if len(got) != chunks*chunkLen {
		t.Errorf("delivered %d bytes, want %d", len(got), chunks*chunkLen)
	}
	if batches < 2 {
		t.Errorf("expected multiple capped batches for %d KiB, got %d", chunks, batches)
	}
}

// TestRunCoalescer_FiresOnFirstChunk verifies the resize-kick hook runs
// exactly once, on the first chunk.
func TestRunCoalescer_FiresOnFirstChunk(t *testing.T) {
	dataCh := make(chan []byte, 2)
	dataCh <- []byte("a")
	dataCh <- []byte("b")
	close(dataCh)
	first := 0
	runCoalescer(dataCh, func() { first++ }, func([]byte) {})
	if first != 1 {
		t.Errorf("onFirstChunk fired %d times, want 1", first)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
$DRUN go test -run TestRunCoalescer -v ./internal/daemon/
```

Expected: compile FAIL — `runCoalescer`/`coalesceMaxBytes` undefined.

- [ ] **Step 3: Extract and cap**

In `internal/daemon/daemon.go`, replace the coalescing section of `streamPTYOutput` (from `const coalesceDelay …` through the end of the `for { select { … } }` loop) with a call + the exit-code block, and add the extracted function:

```go
// coalesceMaxBytes caps a single coalesced flush. The 2 ms timer is a
// debounce: a PTY that streams without 2 ms gaps would otherwise grow the
// accumulator (and the resulting broadcast frame) without bound.
const coalesceMaxBytes = 64 * 1024

// runCoalescer batches chunks from dataCh and calls onFlush with batches of
// at most coalesceMaxBytes, flushing early when the cap is reached and
// otherwise 2 ms after the last chunk. onFirstChunk fires once, before the
// first batch (resize-kick hook). Returns after a final flush when dataCh
// closes. onFlush must not retain the slice — the accumulator is reused.
func runCoalescer(dataCh <-chan []byte, onFirstChunk func(), onFlush func([]byte)) {
	const coalesceDelay = 2 * time.Millisecond
	var acc []byte

	flushTimer := time.NewTimer(0)
	if !flushTimer.Stop() {
		<-flushTimer.C
	}
	stopTimer := func() {
		if !flushTimer.Stop() {
			select {
			case <-flushTimer.C:
			default:
			}
		}
	}
	flush := func() {
		if len(acc) == 0 {
			return
		}
		onFlush(acc)
		if cap(acc) > 4*coalesceMaxBytes {
			acc = nil // release a burst-peak backing array
		} else {
			acc = acc[:0]
		}
	}

	first := true
	for {
		select {
		case chunk, ok := <-dataCh:
			if !ok {
				stopTimer()
				flush()
				return
			}
			if first {
				first = false
				onFirstChunk()
			}
			acc = append(acc, chunk...)
			if len(acc) >= coalesceMaxBytes {
				stopTimer()
				flush()
				continue
			}
			stopTimer()
			flushTimer.Reset(coalesceDelay)
		case <-flushTimer.C:
			flush()
		}
	}
}
```

`streamPTYOutput` becomes (reader goroutine unchanged above; the existing first-chunk resize-kick and exit-code blocks move into the callback / after the call):

```go
	runCoalescer(dataCh,
		func() {
			// First output proves the child's console is wired up — re-apply
			// the size in case the initial resize event was dropped during
			// boot (see resizeKick).
			if pane := d.session.Pane(paneID); pane != nil {
				resizeKick(pty, pane.Cols, pane.Rows)
			}
		},
		func(b []byte) { d.flushPaneOutput(paneID, b) },
	)

	// dataCh closed: PTY EOF. Capture process exit code (PluginMu-protected).
	if pane := d.session.Pane(paneID); pane != nil {
		code := pty.WaitExit()
		// … existing exit-code + process_exit event block, unchanged …
	}
```

(The `firstChunk` variable and the old inline loop are deleted; keep the existing exit-code/event code byte-for-byte, just relocated after `runCoalescer` returns.)

- [ ] **Step 4: Verify green**

```bash
$DRUN go test ./internal/daemon/ && $DRUN go vet ./internal/daemon/
```

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/daemon.go internal/daemon/coalescer_test.go
git rm techdebt/daemon/2-1-coalescer-unbounded-accumulator.md
git commit -m "fix(daemon): cap PTY coalescer batches at 64 KiB"
```

### Task 6: `cleanupPaneArtifacts` on every destroy path

**Files:**
- Modify: `internal/daemon/daemon.go` (new helper; wire into `handleDestroyPane`, `handleDestroyPaneReq`, `handleDestroyTab`, `handleReplacePane`)
- Modify: `internal/hookevents/spool.go` (`Cleanup` also deletes `parseErrCounts`)
- Test: `internal/hookevents/spool_test.go` (append), `internal/daemon/pane_cleanup_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/hookevents/spool_test.go` (white-box; if the existing file is `package hookevents_test`, create `spool_internal_test.go` with `package hookevents` instead):

```go
func TestSpoolCleanup_RemovesParseErrCount(t *testing.T) {
	s := NewSpool(t.TempDir())
	s.mu.Lock()
	s.parseErrCounts["pane-x"] = 3
	s.mu.Unlock()

	s.Cleanup("pane-x")

	s.mu.Lock()
	_, ok := s.parseErrCounts["pane-x"]
	s.mu.Unlock()
	if ok {
		t.Error("Cleanup left parseErrCounts entry — monotonic map growth")
	}
}
```

Create `internal/daemon/pane_cleanup_test.go` (white-box):

```go
package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/hookevents"
	"github.com/artyomsv/quil/internal/ipc"
)

// seedPaneArtifacts creates the on-disk files cleanupPaneArtifacts must remove.
func seedPaneArtifacts(t *testing.T, paneID string) (spoolFile, sessFile string) {
	t.Helper()
	if err := os.MkdirAll(config.EventsDir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(config.SessionsDir(), 0700); err != nil {
		t.Fatal(err)
	}
	spoolFile = filepath.Join(config.EventsDir(), paneID+".jsonl")
	sessFile = filepath.Join(config.SessionsDir(), paneID+".id")
	if err := os.WriteFile(spoolFile, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sessFile, []byte("sess"), 0600); err != nil {
		t.Fatal(err)
	}
	return spoolFile, sessFile
}

func assertGone(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still exists after cleanup (err=%v)", p, err)
		}
	}
}

// TestHandleDestroyTab_CleansHookArtifacts: destroying a tab must clean every
// contained pane's hook artifacts, same as destroying the pane directly
// (techdebt/daemon/3-1).
func TestHandleDestroyTab_CleansHookArtifacts(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())

	d := New(config.Default())
	d.hookSpool = hookevents.NewSpool(config.EventsDir())
	d.hookIngester = hookevents.NewIngester(func(hookevents.Payload) {})

	// Two tabs: destroying the only tab triggers handleDestroyTab's
	// auto-create-replacement path, which spawns a real PTY — not what
	// this test is about.
	tab := d.session.CreateTab("Shell")
	d.session.CreateTab("Keep")
	pane, err := d.session.CreatePane(tab.ID, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	spoolFile, sessFile := seedPaneArtifacts(t, pane.ID)

	msg, _ := ipc.NewMessage(ipc.MsgDestroyTab, ipc.DestroyTabPayload{TabID: tab.ID})
	d.handleDestroyTab(msg)

	assertGone(t, spoolFile, sessFile)
}

// TestCleanupPaneArtifacts_RemovesAll covers the helper directly, including
// the opencode session-id variant.
func TestCleanupPaneArtifacts_RemovesAll(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())

	d := New(config.Default())
	d.hookSpool = hookevents.NewSpool(config.EventsDir())
	d.hookIngester = hookevents.NewIngester(func(hookevents.Payload) {})

	spoolFile, sessFile := seedPaneArtifacts(t, "pane-abc123")
	ocFile := filepath.Join(config.SessionsDir(), "opencode-pane-abc123.id")
	if err := os.WriteFile(ocFile, []byte("oc"), 0600); err != nil {
		t.Fatal(err)
	}

	d.cleanupPaneArtifacts("pane-abc123")

	assertGone(t, spoolFile, sessFile, ocFile)
}
```

Note: `handleDestroyTab` calls the nil-guarded `d.broadcast` and `requestSnapshot`; if `New()` does not initialize `snapshotCh`, check `grep -n "snapshotCh" internal/daemon/daemon.go` and follow whatever the existing white-box handler tests (e.g. in `lazy_restore_test.go`) do to construct a handler-callable daemon.

- [ ] **Step 2: Run to verify they fail**

```bash
$DRUN go test -run "TestSpoolCleanup_RemovesParseErrCount|TestHandleDestroyTab_CleansHookArtifacts|TestCleanupPaneArtifacts_RemovesAll" -v ./internal/hookevents/ ./internal/daemon/
```

Expected: spool test FAILS (entry survives); daemon tests FAIL to compile (`cleanupPaneArtifacts` undefined) — and after adding only the helper, `TestHandleDestroyTab_CleansHookArtifacts` FAILS (files survive).

- [ ] **Step 3: Implement**

`internal/hookevents/spool.go` — in `Cleanup`, extend the locked section:

```go
	s.mu.Lock()
	delete(s.offsets, paneID)
	delete(s.parseErrCounts, paneID)
	s.mu.Unlock()
```

`internal/daemon/daemon.go` — add near `handleDestroyPane`:

```go
// cleanupPaneArtifacts tears down everything keyed by paneID outside the
// session maps: the hook spool file + offset/parse-error entries, the
// ingester's pending coalescers and rate buckets, and the persisted
// session-id files. MUST be called on every pane-destruction path —
// destroy-pane, destroy-tab, replace — or the daemon leaks the map entries
// and re-polls the dead spool file every 200 ms forever.
func (d *Daemon) cleanupPaneArtifacts(paneID string) {
	if d.hookSpool != nil {
		d.hookSpool.Cleanup(paneID)
	}
	if d.hookIngester != nil {
		d.hookIngester.Cancel(paneID)
	}
	for _, name := range []string{paneID + ".id", "opencode-" + paneID + ".id"} {
		p := filepath.Join(config.SessionsDir(), name)
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("cleanup pane %s: remove session id %s: %v", paneID, name, err)
		}
	}
}
```

Wire-up:

- `handleDestroyPane` and `handleDestroyPaneReq`: replace the existing inline `d.hookSpool.Cleanup(...)` + `d.hookIngester.Cancel(...)` pairs with `d.cleanupPaneArtifacts(payload.PaneID)` (keep the surrounding comment, shortened).
- `handleDestroyTab` (daemon.go:893): capture the pane list before destruction and clean after:

```go
	log.Printf("tab destroy: %s", payload.TabID)
	panes := d.session.Panes(payload.TabID)
	d.session.DestroyTab(payload.TabID)
	for _, p := range panes {
		d.cleanupPaneArtifacts(p.ID)
	}
```

- `handleReplacePane` (daemon.go:1035): after the successful `d.session.ReplacePane(payload.ReplacePaneID, newPane)` swap, add `d.cleanupPaneArtifacts(payload.ReplacePaneID)`.

- [ ] **Step 4: Verify green**

```bash
$DRUN go test ./internal/hookevents/ ./internal/daemon/
```

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/daemon.go internal/daemon/pane_cleanup_test.go internal/hookevents/spool.go internal/hookevents/spool_test.go
git rm techdebt/daemon/3-1-hook-cleanup-missing-tab-destroy-replace.md
git commit -m "fix(daemon): clean hook artifacts on tab destroy and pane replace"
```

### Task 7: Close the Windows child-process handle

**Files:**
- Modify: `internal/pty/session_windows.go` (`WaitExit`)
- Test: `internal/pty/session_windows_test.go` (runs on Windows only; cross-compile-checked in Docker)

- [ ] **Step 1: Write the test** (it can only run on a Windows host — the Docker runner is Linux — so the TDD loop here is compile-gated + manual)

```go
//go:build windows

package pty

import "testing"

// TestWinSession_WaitExit_IdempotentAfterHandleClose: WaitExit closes the
// process handle (techdebt/pty/4-1); repeated calls must return the cached
// exit code, not -1 from the zeroed handle.
func TestWinSession_WaitExit_IdempotentAfterHandleClose(t *testing.T) {
	s := New().(*winSession)
	if err := s.Start("cmd.exe", "/c", "exit", "3"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()

	if code := s.WaitExit(); code != 3 {
		t.Errorf("first WaitExit = %d, want 3", code)
	}
	if code := s.WaitExit(); code != 3 {
		t.Errorf("second WaitExit = %d, want 3 (cached) — handle-zeroing broke the cache", code)
	}
}
```

- [ ] **Step 2: Implement**

Replace `WaitExit` in `internal/pty/session_windows.go`:

```go
func (s *winSession) WaitExit() int {
	s.waitOnce.Do(func() {
		if s.handle == 0 {
			return
		}
		windows.WaitForSingleObject(s.handle, windows.INFINITE)
		var code uint32
		if err := windows.GetExitCodeProcess(s.handle, &code); err == nil {
			s.exitCode = int(code)
		}
		// The kernel keeps the process object alive while any handle is
		// open; without this Close the daemon retains one HANDLE per
		// destroyed/restarted pane for its whole lifetime.
		_ = windows.CloseHandle(s.handle)
		s.handle = 0
	})
	return s.exitCode
}
```

(The old top-level `if s.handle == 0 { return -1 }` guard moves inside the `Do` — outside it would return -1 on second calls after zeroing. `s.exitCode` is initialized to -1 by the constructors, preserving the never-started behavior.)

- [ ] **Step 3: Cross-compile check + full suite**

```bash
$DRUN sh -c "GOOS=windows GOARCH=amd64 go vet ./internal/pty/ && GOOS=windows GOARCH=amd64 go test -c -o /dev/null ./internal/pty/"
$DRUN go test ./...
```

Expected: vet + test-compile clean. Note in the PR that the new test needs a Windows host run (`go test ./internal/pty/` on the dev box once Go is available, or rely on dogfooding).

- [ ] **Step 4: Commit, gate, PR**

```bash
git add internal/pty/session_windows.go internal/pty/session_windows_test.go
git rm techdebt/pty/4-1-windows-process-handle-leak.md
git commit -m "fix(pty): close Windows process handle after WaitExit"
```

CHANGELOG `[Unreleased]` `### Fixed` entries for Tasks 3-7, commit, phase gate, PR `fix: resource leak cleanup (VT emulators, tick chains, coalescer, hook artifacts, win32 handles)`.

---

# Phase 2 — Daemon hot-path performance (branch `perf/daemon-hot-paths`)

Closes `techdebt/ringbuf/3-2-circular-buffer-realloc-per-write.md`, `techdebt/daemon/3-2-snapshot-skips-unchanged-buffers.md`, `techdebt/ipc/3-1-bufio-read-path.md`.

### Task 8: True circular RingBuffer with `Tail` and `Gen`

**Files:**
- Rewrite: `internal/ringbuf/ringbuf.go`
- Test: `internal/ringbuf/ringbuf_test.go` (append; existing tests must pass unmodified)

- [ ] **Step 1: Write the failing tests** (append to the existing test file)

```go
func TestRingBuffer_TailReturnsLastN(t *testing.T) {
	rb := NewRingBuffer(8)
	rb.Write([]byte("abcdefghij")) // wraps: buffer holds "cdefghij"

	tests := []struct {
		n    int
		want string
	}{
		{4, "ghij"},
		{8, "cdefghij"},
		{20, "cdefghij"}, // n > Len → everything
		{0, ""},
	}
	for _, tt := range tests {
		if got := string(rb.Tail(tt.n)); got != tt.want {
			t.Errorf("Tail(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

func TestRingBuffer_GenIncrementsOnMutation(t *testing.T) {
	rb := NewRingBuffer(8)
	g0 := rb.Gen()
	rb.Write([]byte("ab"))
	if rb.Gen() == g0 {
		t.Error("Gen unchanged after Write")
	}
	g1 := rb.Gen()
	rb.Reset()
	if rb.Gen() == g1 {
		t.Error("Gen unchanged after Reset")
	}
	g2 := rb.Gen()
	if rb.Gen() != g2 {
		t.Error("Gen changed without mutation")
	}
}

// TestRingBuffer_WriteSteadyStateZeroAllocs pins the entire point of the
// rewrite: the old implementation reallocated + copied the full buffer on
// every write once full (techdebt/ringbuf/3-2).
func TestRingBuffer_WriteSteadyStateZeroAllocs(t *testing.T) {
	rb := NewRingBuffer(4096)
	rb.Write(make([]byte, 4096)) // reach steady state (full)
	chunk := make([]byte, 512)
	allocs := testing.AllocsPerRun(100, func() {
		rb.Write(chunk)
	})
	if allocs != 0 {
		t.Errorf("steady-state Write allocates %.1f times per call, want 0", allocs)
	}
}

func TestRingBuffer_WrapContentMatchesNaive(t *testing.T) {
	rb := NewRingBuffer(100)
	var naive []byte
	for i := 0; i < 50; i++ {
		chunk := bytes.Repeat([]byte{byte('a' + i%26)}, 7+i%13)
		rb.Write(chunk)
		naive = append(naive, chunk...)
		if len(naive) > 100 {
			naive = naive[len(naive)-100:]
		}
		if !bytes.Equal(rb.Bytes(), naive) {
			t.Fatalf("iteration %d: ring %q != naive %q", i, rb.Bytes(), naive)
		}
	}
}
```

(Add `"bytes"` to the test imports if absent.)

- [ ] **Step 2: Run to verify failures**

```bash
$DRUN go test ./internal/ringbuf/
```

Expected: compile FAIL (`Tail`/`Gen` undefined). The zero-allocs test would also fail against the old implementation.

- [ ] **Step 3: Rewrite the buffer**

Replace the body of `internal/ringbuf/ringbuf.go` (keep package comment style):

```go
package ringbuf

import "sync"

// RingBuffer is a thread-safe circular byte buffer that keeps the most
// recent data when capacity is exceeded. Used to buffer PTY output for
// replay on TUI reconnect.
//
// The backing array is allocated once at construction and never reallocated:
// steady-state Write is zero-allocation. (The previous implementation
// append-reallocated and fully recompacted on every write once full — ~768 KB
// of memcpy per coalesced flush per busy pane.)
type RingBuffer struct {
	buf   []byte // fixed backing array, len == capacity
	start int    // index of the oldest byte
	size  int    // bytes currently stored
	gen   uint64 // bumped on every mutation; snapshot change detection
	mu    sync.Mutex
}

// NewRingBuffer creates a ring buffer with the given byte capacity.
func NewRingBuffer(capacity int) *RingBuffer {
	if capacity <= 0 {
		capacity = 1
	}
	return &RingBuffer{buf: make([]byte, capacity)}
}

// Write appends p, trimming the oldest bytes when capacity is exceeded.
func (rb *RingBuffer) Write(p []byte) {
	if len(p) == 0 {
		return
	}
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.gen++

	c := len(rb.buf)
	if len(p) >= c {
		copy(rb.buf, p[len(p)-c:])
		rb.start, rb.size = 0, c
		return
	}
	end := (rb.start + rb.size) % c
	n := copy(rb.buf[end:], p)
	if n < len(p) {
		copy(rb.buf, p[n:])
	}
	rb.size += len(p)
	if rb.size > c {
		rb.start = (rb.start + rb.size - c) % c
		rb.size = c
	}
}

// Bytes returns a copy of all buffered data.
func (rb *RingBuffer) Bytes() []byte {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.tailLocked(rb.size)
}

// Tail returns a copy of the last n bytes (all bytes when n >= Len).
// Replaces the "Bytes() then keep 4 KB" pattern that copied the full
// buffer to read an excerpt.
func (rb *RingBuffer) Tail(n int) []byte {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	if n > rb.size {
		n = rb.size
	}
	return rb.tailLocked(n)
}

// tailLocked copies the last n (<= rb.size) bytes. Caller holds rb.mu.
func (rb *RingBuffer) tailLocked(n int) []byte {
	if n <= 0 {
		return nil
	}
	out := make([]byte, n)
	c := len(rb.buf)
	first := (rb.start + rb.size - n) % c
	m := copy(out, rb.buf[first:])
	if m < n {
		copy(out[m:], rb.buf)
	}
	return out
}

// Len returns the current number of bytes in the buffer.
func (rb *RingBuffer) Len() int {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.size
}

// Gen returns the mutation generation, monotonically increasing across
// Write/Reset. Equal generations guarantee identical contents.
func (rb *RingBuffer) Gen() uint64 {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.gen
}

// Reset clears all buffered data (the backing array is retained).
func (rb *RingBuffer) Reset() {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.start, rb.size = 0, 0
	rb.gen++
}
```

Note `tailLocked`'s first copy: `rb.buf[first:]` may cover more than `n` bytes when the region is contiguous — `copy` stops at `len(out)`, so the math is safe in both the contiguous and wrapped cases.

- [ ] **Step 4: Verify green — old AND new tests, plus dependents**

```bash
$DRUN go test ./internal/ringbuf/ ./internal/daemon/ ./internal/tui/ ./internal/memreport/
```

Expected: all PASS (callers use only `Write/Bytes/Len/Reset`, all behavior-preserved). One semantic change to verify: memory accounting that previously reflected `len(data)` growth now sees fixed capacity — check `grep -n "OutputBuf" internal/daemon/session.go internal/memreport/*.go` and confirm it uses `Len()` (current-bytes semantics preserved).

- [ ] **Step 5: Update `.claude/CLAUDE.md` + commit**

Add to the persistence/architecture notes: ring buffers are fixed-allocation circular buffers; `Tail(n)`/`Gen()` exist for excerpts and snapshot change-detection.

```bash
git add internal/ringbuf/ .claude/CLAUDE.md
git rm techdebt/ringbuf/3-2-circular-buffer-realloc-per-write.md
git commit -m "perf(ringbuf): true circular buffer — zero-alloc steady-state writes"
```

### Task 9: Use `Tail` for excerpts and idle analysis

**Files:**
- Modify: `internal/daemon/daemon.go` (`paneOutputExcerpt` ~2319, `analyzeIdleTitle` ~2272)
- Test: existing excerpt tests (`internal/daemon/event_excerpt_test.go`) must stay green

- [ ] **Step 1: Change `paneOutputExcerpt`**

```go
func paneOutputExcerpt(pane *Pane, n int) string {
	if pane == nil || pane.OutputBuf == nil {
		return ""
	}
	// Tail copies only the 4 KiB window — Bytes() copied the full 256-512 KB
	// ring to read the same excerpt, on every event emit.
	raw := pane.OutputBuf.Tail(4096)
	if len(raw) == 0 {
		return ""
	}
	return lastNLines(ansi.Strip(string(trimToNewlineSafe(raw, 4096))), n)
}
```

(`trimToNewlineSafe` stays — it still advances past a partial ANSI sequence at the window's left edge.)

- [ ] **Step 2: Same change in `analyzeIdleTitle`**

Locate with `grep -n "OutputBuf.Bytes()" internal/daemon/daemon.go`. In `analyzeIdleTitle` (reads the last 4 KB for idle-handler matching), replace `pane.OutputBuf.Bytes()` with `pane.OutputBuf.Tail(4096)` and drop any subsequent manual tail-slicing of that value (the `trimToNewlineSafe(raw, 4096)` call stays). Leave the other `OutputBuf.Bytes()` call sites (snapshot, ghost replay, read_pane_output, screenshot) unchanged — they genuinely need the full buffer.

- [ ] **Step 3: Verify green**

```bash
$DRUN go test -run "Excerpt|Idle" -v ./internal/daemon/ && $DRUN go test ./internal/daemon/
```

Expected: PASS — excerpt tests pin the user-visible behavior; only the copy size changed.

- [ ] **Step 4: Commit**

```bash
git add internal/daemon/daemon.go
git commit -m "perf(daemon): excerpt/idle reads copy a 4 KiB tail, not the full ring"
```

### Task 10: Snapshot skips unchanged ghost buffers

**Files:**
- Modify: `internal/daemon/daemon.go` (`snapshot` ~329; new `snapGens` field on `Daemon`, initialized where `Daemon` is constructed — `grep -n "func New(" internal/daemon/daemon.go`)
- Test: `internal/daemon/snapshot_gen_test.go`

- [ ] **Step 1: Write the failing test**

```go
package daemon

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/artyomsv/quil/internal/config"
)

// TestSnapshot_SkipsUnchangedBuffers: a pane that produced no output since
// the last snapshot must not have its buffer file rewritten (~20 GB/day of
// identical bytes at defaults — techdebt/daemon/3-2). Deleting the file
// between snapshots makes "skipped" directly observable.
func TestSnapshot_SkipsUnchangedBuffers(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())

	d := New(config.Default())
	tab := d.session.CreateTab("Shell")
	pane, err := d.session.CreatePane(tab.ID, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	pane.OutputBuf.Write(bytes.Repeat([]byte{'x'}, 1024))

	bufFile := filepath.Join(config.BufferDir(), pane.ID+".bin")

	d.snapshot()
	if _, err := os.Stat(bufFile); err != nil {
		t.Fatalf("first snapshot did not write buffer: %v", err)
	}

	// No writes since → second snapshot must skip this pane entirely.
	if err := os.Remove(bufFile); err != nil {
		t.Fatal(err)
	}
	d.snapshot()
	if _, err := os.Stat(bufFile); !os.IsNotExist(err) {
		t.Error("unchanged buffer was rewritten — generation skip not working")
	}

	// New output → third snapshot must write again.
	pane.OutputBuf.Write([]byte("more"))
	d.snapshot()
	if _, err := os.Stat(bufFile); err != nil {
		t.Errorf("changed buffer was not rewritten: %v", err)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
$DRUN go test -run TestSnapshot_SkipsUnchangedBuffers -v ./internal/daemon/
```

Expected: FAIL — "unchanged buffer was rewritten".

- [ ] **Step 3: Implement**

Add to the `Daemon` struct: `snapGens map[string]uint64` and initialize `snapGens: make(map[string]uint64),` in `New()`. In `snapshot()`, replace the buffer-collection block:

```go
			if pane.OutputBuf != nil {
				gen := pane.OutputBuf.Gen()
				if prev, ok := d.snapGens[pane.ID]; ok && prev == gen {
					continue // unchanged since last snapshot — file already matches
				}
				if data := pane.OutputBuf.Bytes(); len(data) > 0 {
					buffers[pane.ID] = data
					totalBytes += len(data)
					d.snapGens[pane.ID] = gen
				}
			}
```

And after the pane loop (still inside `snapshot()`), prune dead entries so destroyed panes don't accumulate generations:

```go
	// snapGens is only touched here; snapshot() is serialized (debounce loop,
	// then the final flush after the loop exits), so no lock is needed.
	live := make(map[string]bool, len(activePaneIDs))
	for _, id := range activePaneIDs {
		live[id] = true
	}
	for id := range d.snapGens {
		if !live[id] {
			delete(d.snapGens, id)
		}
	}
```

Important: skipped panes are still in `activePaneIDs` (appended above the skip), so `persist.CleanBuffers` keeps their files.

- [ ] **Step 4: Verify green** (including the existing `TestSnapshot_PaneSetConsistentAcrossWorkspaceAndBuffers` integration test)

```bash
$DRUN go test ./internal/daemon/ && $DRUN go test -tags=integration -run TestSnapshot ./internal/daemon/
```

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/daemon.go internal/daemon/snapshot_gen_test.go
git rm techdebt/daemon/3-2-snapshot-skips-unchanged-buffers.md
git commit -m "perf(daemon): snapshot skips ghost buffers unchanged since last write"
```

### Task 11: IPC — buffered reads + single-allocation wire frames

**Files:**
- Modify: `internal/ipc/protocol.go` (`ReadMessage` prefix read; new `EncodeFrame`; `WriteMessage` via `EncodeFrame`)
- Modify: `internal/ipc/server.go` (`Conn` gets `bufio.Reader`; `Send`/`SendBlocking` use `EncodeFrame`; `Broadcast` drops `slices.Clone`)
- Modify: `internal/ipc/client.go` (client read side gets `bufio.Reader`)
- Test: `internal/ipc/protocol_test.go` (append)

- [ ] **Step 1: Write the failing test**

```go
func TestEncodeFrame_RoundTrip(t *testing.T) {
	msg, err := ipc.NewMessage(ipc.MsgPaneOutput, ipc.PaneOutputPayload{
		PaneID: "pane-x", Data: []byte("hello"), Ghost: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	frame, err := ipc.EncodeFrame(msg)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ipc.ReadMessage(bytes.NewReader(frame))
	if err != nil {
		t.Fatalf("ReadMessage on encoded frame: %v", err)
	}
	if got.Type != msg.Type || !bytes.Equal(got.Payload, msg.Payload) {
		t.Errorf("round trip mismatch: got %+v want %+v", got, msg)
	}
}
```

- [ ] **Step 2: Run to verify compile failure, then implement**

`internal/ipc/protocol.go`:

```go
// EncodeFrame marshals msg into a single length-prefixed wire frame in one
// allocation. Shared by WriteMessage and the per-conn send queues — replaces
// the marshal → bytes.Buffer → clone chain that copied every broadcast frame
// up to four times.
func EncodeFrame(msg *Message) ([]byte, error) {
	data, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshal message: %w", err)
	}
	frame := make([]byte, 4+len(data))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(data)))
	copy(frame[4:], data)
	return frame, nil
}
```

`WriteMessage` becomes (one syscall instead of two):

```go
func WriteMessage(w io.Writer, msg *Message) error {
	frame, err := EncodeFrame(msg)
	if err != nil {
		return err
	}
	if _, err := w.Write(frame); err != nil {
		return fmt.Errorf("write frame: %w", err)
	}
	return nil
}
```

`ReadMessage` prefix read (drops `binary.Read`'s reflection + per-call alloc; error string `read length:` is load-bearing — log scrapers and the attach test fingerprint reference it):

```go
	var lenBuf [4]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return nil, fmt.Errorf("read length: %w", err)
	}
	length := binary.BigEndian.Uint32(lenBuf[:])
```

`internal/ipc/server.go`:
- `Conn` struct: add `br *bufio.Reader`; in `newConn`: `br: bufio.NewReader(raw),`; `Receive()` reads `ReadMessage(c.br)` (read path is single-goroutine per conn — `handleConn` — so this is race-free).
- `Send` and `SendBlocking`: replace the `var buf bytes.Buffer; WriteMessage(&buf, msg)` blocks with `frame, err := EncodeFrame(msg); if err != nil { return err }` and pass `frame` onward (delete the now-stale per-conn-ownership comments about `buf.Bytes()`).
- `Broadcast`: replace the buffer + `slices.Clone` block with `frame, err := EncodeFrame(msg)` — the frame is freshly allocated per broadcast, so the defensive clone (and its comment) goes away. Remove the unused `slices` and `bytes` imports if nothing else uses them.

`internal/ipc/client.go`: mirror the read-side change — wrap the conn in a `bufio.Reader` at construction and use it in the client's `Receive`/read loop (locate with `grep -n "ReadMessage" internal/ipc/client.go`).

- [ ] **Step 3: Verify green — the whole IPC suite exercises framing, backpressure, and overflow**

```bash
$DRUN go test ./internal/ipc/ ./internal/daemon/ && $DRUN sh -c "apk add --no-cache gcc musl-dev >/dev/null && CGO_ENABLED=1 go test -race ./internal/ipc/"
```

- [ ] **Step 4: Commit, CHANGELOG, gate, PR**

```bash
git add internal/ipc/
git rm techdebt/ipc/3-1-bufio-read-path.md
git commit -m "perf(ipc): buffered reads + single-allocation wire frames"
```

CHANGELOG `### Changed` entries for Tasks 8-11, phase gate, PR `perf: daemon hot paths (ring buffer, snapshot, IPC framing)`.

---

# Phase 3 — TUI render performance (branch `perf/tui-render`)

Closes `techdebt/tui/3-3-per-pane-render-caching.md`.

### Task 12: Per-pane render cache

**Files:**
- Modify: `internal/tui/pane.go` (`PaneModel` fields, `View()`, mutation sites)
- Modify: `internal/tui/model.go` (redraw key clears caches)
- Test: `internal/tui/pane_cache_test.go`

- [ ] **Step 1: Write the failing test**

```go
package tui

import "testing"

// TestPaneView_CachedUntilContentChanges: View() must not rebuild the frame
// string when nothing changed — every Update triggers View for every visible
// pane, so an uncached pane pays a full VT-grid render per spinner tick and
// per output frame on ANY pane (techdebt/tui/3-3).
func TestPaneView_CachedUntilContentChanges(t *testing.T) {
	p := NewPaneModel("pane-cache-test", 1024)
	defer p.Dispose()
	p.Width, p.Height = 40, 12

	first := p.View()
	renders := p.renderCount
	if second := p.View(); second != first {
		t.Error("identical state rendered differently")
	}
	if p.renderCount != renders {
		t.Errorf("clean View() recomputed (renderCount %d -> %d)", renders, p.renderCount)
	}

	p.AppendOutput([]byte("hello"))
	_ = p.View()
	if p.renderCount == renders {
		t.Error("View() after AppendOutput served stale cache")
	}

	renders = p.renderCount
	p.Active = true // comparable field changes invalidate via the render key
	_ = p.View()
	if p.renderCount == renders {
		t.Error("View() after Active change served stale cache")
	}
}
```

- [ ] **Step 2: Run to verify compile failure** (`renderCount` undefined)

```bash
$DRUN go test -run TestPaneView_CachedUntilContentChanges -v ./internal/tui/
```

- [ ] **Step 3: Implement**

`PaneModel` additions:

```go
	// Render cache: View() output is reused while renderKey is unchanged.
	// contentGen covers VT-grid mutations (the grid itself has no public
	// change counter); the comparable key covers everything else that feeds
	// the frame. renderCount is test observability.
	contentGen  uint64
	selGen      uint64
	cachedKey   paneRenderKey
	cachedView  string
	hasCache    bool
	renderCount int
```

```go
// paneRenderKey is the comparable fingerprint of everything View() reads.
// Adding a new visual input to View()/renderContent/buildTopBorder REQUIRES
// adding it here — a missing field means stale frames.
type paneRenderKey struct {
	contentGen, selGen                  uint64
	width, height, scrollBack           int
	active, ghost, resuming, preparing  bool
	mcpHighlight, muted, focusMode      bool
	working                             bool
	spinnerFrame, workFrame             int
	name, cwd                           string
	cursorVisible                       bool
}

func (p *PaneModel) renderKey() paneRenderKey {
	return paneRenderKey{
		contentGen: p.contentGen, selGen: p.selGen,
		width: p.Width, height: p.Height, scrollBack: p.scrollBack,
		active: p.Active, ghost: p.ghost, resuming: p.resuming, preparing: p.preparing,
		mcpHighlight: p.mcpHighlight, muted: p.Muted, focusMode: p.focusMode,
		working: p.working, spinnerFrame: p.spinnerFrame, workFrame: p.workFrame,
		name: p.Name, cwd: p.CWD, cursorVisible: p.cursorVisible,
	}
}
```

(Field names must match the actual `PaneModel` — verify each with `grep -n "<name>" internal/tui/pane.go` and adjust case; e.g. if mute is `p.Muted` and focus is `p.focusMode` as used in `View()` today, mirror exactly what `View()`/`buildTopBorder` read.)

`View()` — wrap the existing body:

```go
func (p *PaneModel) View() string {
	key := p.renderKey()
	if p.hasCache && key == p.cachedKey {
		return p.cachedView
	}
	p.renderCount++

	// … existing View body unchanged, building `topLine + "\n" + body` …

	out := topLine + "\n" + body
	p.cachedKey, p.cachedView, p.hasCache = key, out, true
	return out
}
```

Bump `contentGen` in the three VT-mutating methods: `AppendOutput` (`p.contentGen++` after `p.vt.Write(data)`), `ResetVT`, `ResizeVT` (after the in-place resize). Bump `selGen` at every `activeSel` mutation site — enumerate with:

```bash
grep -n "activeSel" internal/tui/*.go
```

Expected sites: selection begin/extend/clear in `selection.go` and the mouse handlers in `model.go`; add `p.selGen++` adjacent to each assignment (where the pane is `p`/`pane`/`leaf.Pane` per site). If `activeSel` is only ever assigned through PaneModel methods, bump inside those methods instead — prefer the narrowest set of sites that compiles with no `activeSel =` assignment left unbumped.

`model.go` redraw key (`alt+shift+l`, the `kb.Redraw` case): add cache invalidation before the ClearScreen so the escape hatch also covers a stale-cache bug:

```go
		for _, tab := range m.tabs {
			if tab.Root != nil {
				for _, pane := range tab.Root.Leaves() {
					pane.hasCache = false
				}
			}
		}
```

- [ ] **Step 4: Verify green + visual smoke**

```bash
$DRUN go test ./internal/tui/
./scripts/dev.sh build
```

Then launch `./scripts/quil-dev.ps1`, confirm `[dev]` in the status bar, and verify: typing renders, selection highlights live, spinner animates, scrollback scrolls, ghost panes dim. Any visual staleness = a missing key field.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/pane.go internal/tui/model.go internal/tui/pane_cache_test.go
git commit -m "perf(tui): cache pane frames, rebuild only on content/state change"
```

### Task 13: Cheap render-path wins (`Leaves` cache, `recordMsg` allocation)

**Files:**
- Modify: `internal/tui/tab.go` (cached leaves), `internal/tui/layout.go` (callers), `internal/tui/model.go` (callers + `recordMsg` call), `internal/tui/workstate.go` (callers), `internal/tui/perf.go` or wherever `recordMsg` lives
- Test: `internal/tui/tab_leaves_test.go`

- [ ] **Step 1: Write the failing test**

```go
package tui

import "testing"

// TestTabModel_LeavesCached: Leaves() is called ~30x per frame (tab bar,
// View loop, spinner ticks); the per-call recursive allocation is pure
// churn. The cache must invalidate on tree mutation.
func TestTabModel_LeavesCached(t *testing.T) {
	tab := NewTabModel("tab-1", "Test")
	p1 := NewPaneModel("pane-1", 64)
	defer p1.Dispose()
	tab.Root = &LayoutNode{Pane: p1}

	a := tab.Leaves()
	b := tab.Leaves()
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("Leaves() = %d/%d panes, want 1/1", len(a), len(b))
	}
	if &a[0] != &b[0] {
		t.Error("Leaves() reallocated without tree mutation — cache not working")
	}

	tab.RemovePane("pane-1")
	if got := tab.Leaves(); len(got) != 0 {
		t.Errorf("Leaves() after RemovePane = %d panes, want 0 — stale cache", len(got))
	}
}
```

(Adjust `LayoutNode{Pane: p1}` construction to the real literal shape — check `grep -n "LayoutNode{" internal/tui/layout_test.go` for the established pattern.)

- [ ] **Step 2: Run to verify compile failure** (`tab.Leaves` undefined — today callers use `tab.Root.Leaves()`)

- [ ] **Step 3: Implement**

`internal/tui/tab.go`:

```go
	// leavesCache memoizes Root.Leaves(); nil = invalid. The tab bar alone
	// walks every tab's tree twice per render without it.
	leavesCache []*PaneModel
```

```go
// Leaves returns the tab's panes in layout order, cached until the tree
// mutates. Mutating methods call invalidateLeaves.
func (t *TabModel) Leaves() []*PaneModel {
	if t.Root == nil {
		return nil
	}
	if t.leavesCache == nil {
		t.leavesCache = t.Root.Leaves()
	}
	return t.leavesCache
}

func (t *TabModel) invalidateLeaves() { t.leavesCache = nil }
```

Add `t.invalidateLeaves()` to every method that mutates the tree: `SplitAtPane`, `RemovePane`, and any site that assigns `t.Root` (enumerate with `grep -n "\.Root = " internal/tui/*.go` — includes `restoreTabLayout` and `applyWorkspaceState` pane-add paths; for assignments outside `TabModel` methods, call `tab.invalidateLeaves()` at the assignment site).

Convert hot callers from `tab.Root.Leaves()` to `tab.Leaves()` (enumerate with `grep -n "Root.Leaves()" internal/tui/`): `tabHasEagerPane`/`tabHasWorkingPane` (model.go ~2225-2240), the View leaf loop (~1336), `anyPaneWorking` (workstate.go), the spinner mirror loop (model.go ~700). Callers that mutate the tree right after may keep the direct call.

`recordMsg` allocation — in the Update entry (model.go ~370), replace `fmt.Sprintf("%T", msg)` with a call to:

```go
// msgTypeName avoids per-Update reflection for the hot message types; the
// default arm keeps unknown types observable.
func msgTypeName(msg tea.Msg) string {
	switch msg.(type) {
	case tea.KeyPressMsg:
		return "tea.KeyPressMsg"
	case tea.MouseMotionMsg:
		return "tea.MouseMotionMsg"
	case tea.MouseClickMsg:
		return "tea.MouseClickMsg"
	case tea.MouseReleaseMsg:
		return "tea.MouseReleaseMsg"
	case tea.MouseWheelMsg:
		return "tea.MouseWheelMsg"
	case tea.WindowSizeMsg:
		return "tea.WindowSizeMsg"
	default:
		return fmt.Sprintf("%T", msg)
	}
}
```

Then extend the switch with the package-local hot types — enumerate the actual case list with `grep -n "^	case .*Msg:" internal/tui/model.go` and add the top ~10 (pane output, paneEventMsg, the tick messages) using their real identifiers.

- [ ] **Step 4: Verify green, full gate, PR**

```bash
$DRUN go test ./internal/tui/ && ./scripts/dev.sh test && ./scripts/dev.sh vet
```

- [ ] **Step 5: Commit**

```bash
git add internal/tui/
git rm techdebt/tui/3-3-per-pane-render-caching.md
git commit -m "perf(tui): cache tab leaves; type-switch perf instrumentation"
```

CHANGELOG `### Changed` entry, phase gate (incl. race + integration), PR `perf(tui): render caching`.

---

## Deferred (tracked, deliberately not in this plan)

Low-impact or contract-sensitive items stay in the techdebt register / audit report rather than padding these PRs:

- Event queue newest-first prepend → append+reverse (touches `Events()`/`FindSince` ordering contract; low frequency)
- MCP output-subscription flag (protocol addition; design with the next MCP change)
- VT scrollback size as a `[ui]` config knob + memory-report visibility
- claude-code error-handler literal prescan before regex
- Spool `os.Stat`-before-open on idle ticks
- `hitTestTab` width memoization (drag-only)
- `read_pane_output` tail-trim before ANSI strip (needs a lines→bytes heuristic)
- Bounded `WaitForSingleObject` + `TerminateProcess` fallback for ConPTY-surviving children (speculative)
- TUI-side leak: `mcpHighlightSeq` map entries (a few bytes per pane ever highlighted)
- IPC message batching in the TUI's `listenForMessages` (drain-all per Update cycle — measure after Phase 3 lands)

## Verification summary (success checks per phase)

| Phase | Check |
|---|---|
| 0 | `TestStart_SecondDaemonRefused` + `TestClaudeHookSpawnPrep_PaneEnvUsesHookHome` green; full gate green |
| 1 | Goroutine-delta, tick-guard, coalescer-cap, artifact-cleanup tests green; full gate green |
| 2 | Zero-alloc ring write, snapshot-skip, frame round-trip green; `-race` on ipc green |
| 3 | Render-cache + leaves-cache tests green; manual dev-mode visual smoke (selection, spinner, scrollback, ghost dim) |
